package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/getlantern/netx"
	"github.com/getlantern/proxy/v3/filters"
)

const (
	connectRequest = "CONNECT %v HTTP/1.1\r\nHost: %v\r\n\r\n"
)

func (proxy *proxy) nextCONNECT(dialCtx context.Context, downstream net.Conn, respondOK bool) filters.Next {
	return func(cs *filters.ConnectionState, modifiedReq *http.Request) (*http.Response, *filters.ConnectionState, error) {
		var resp *http.Response
		upstreamAddr := modifiedReq.URL.Host
		nextCS := cs.Clone()
		nextCS.SetUpstreamAddr(upstreamAddr)

		if !proxy.OKWaitsForUpstream {
			// We preemptively respond with an OK on the client. Some user agents like
			// Chrome consider any non-200 OK response from the proxy to indicate that
			// there's a problem with the proxy rather than the origin, causing the user
			// agent to mark the proxy itself as bad and avoid using it in the future.
			// By immediately responding 200 OK irrespective of what happens with the
			// origin, we are signaling to the user agent that the proxy itself is good.
			// If there is a subsequent problem dialing the origin, the user agent will
			// (mostly correctly) attribute that to a problem with the origin rather
			// than the proxy and continue to consider the proxy good. See the extensive
			// discussion here: https://github.com/getlantern/lantern/issues/5514.
			if respondOK {
				resp, nextCS = doRespondOK(nextCS, modifiedReq)
			}
			if proxy.OKSendsServerTiming {
				addDialUpstreamHeader(resp, 0)
			}
			return resp, nextCS, nil
		}

		var start time.Time
		if proxy.OKSendsServerTiming {
			start = time.Now()
		}

		// Note - for CONNECT requests, we use the Host from the request URL, not the
		// Host header. See discussion here:
		// https://ask.wireshark.org/questions/22988/http-host-header-with-and-without-port-number
		dialCtx, cancelDial := addDialDeadlineIfNecessary(dialCtx, modifiedReq)
		upstream, err := proxy.Dial(dialCtx, true, "tcp", upstreamAddr)
		cancelDial()
		if err != nil {
			if proxy.OKWaitsForUpstream {
				return badGateway(cs, modifiedReq, err)
			}
			return nil, cs, err
		}

		// In this case, waited to successfully dial upstream before responding
		// OK. Lantern uses this logic on server-side proxies so that the Lantern
		// client retains the opportunity to fail over to a different proxy server
		// just in case that one is able to reach the origin. This is relevant,
		// for example, if some proxy servers reside in jurisdictions where an
		// origin site is blocked but other proxy servers don't.
		if respondOK {
			resp, nextCS = doRespondOK(nextCS, modifiedReq)
		}
		if proxy.OKSendsServerTiming {
			addDialUpstreamHeader(resp, time.Since(start))
		}

		nextCS.SetUpstream(upstream)
		return resp, nextCS, nil
	}
}

func addDialUpstreamHeader(resp *http.Response, duration time.Duration) {
	resp.Header.Add(serverTimingHeader, fmt.Sprintf("dialupstream;dur=%d", duration/time.Millisecond))
}

func addDialDeadlineIfNecessary(ctx context.Context, req *http.Request) (context.Context, context.CancelFunc) {
	timeoutString := req.Header.Get(DialTimeoutHeader)
	if timeoutString == "" {
		return ctx, noopCancel
	}

	timeoutInt, err := strconv.ParseInt(timeoutString, 10, 64)
	if err != nil {
		log.Errorf("Invalid %v, expected integer, got '%v'", DialTimeoutHeader, timeoutString)
		return ctx, noopCancel
	}

	newDeadline := time.Now().Add(time.Duration(timeoutInt) * time.Millisecond)
	existingDeadline, contextHasDeadline := ctx.Deadline()
	if contextHasDeadline && existingDeadline.Before(newDeadline) {
		return ctx, noopCancel
	}

	return context.WithDeadline(ctx, newDeadline)
}

func noopCancel() {
}

func (proxy *proxy) Connect(dialCtx context.Context, in io.Reader, conn net.Conn, origin string) error {
	pin := io.MultiReader(strings.NewReader(fmt.Sprintf(connectRequest, origin, origin)), in)
	return proxy.handle(dialCtx, pin, conn, nil, false)
}

func (proxy *proxy) proceedWithConnect(
	dialCtx context.Context, cs *filters.ConnectionState, req *http.Request,
	upstreamAddr string, upstream net.Conn, downstream net.Conn, respondOK bool) error {

	if upstream == nil {
		var dialErr error
		upstream, dialErr = proxy.Dial(dialCtx, true, "tcp", upstreamAddr)
		if dialErr != nil {
			return dialErr
		}
	}
	defer func() {
		if closeErr := upstream.Close(); closeErr != nil {
			log.Tracef("Error closing upstream connection: %s", closeErr)
		}
	}()

	var rr io.Reader

	// Prepare to pipe data between the client and the proxy.
	bufOut := proxy.BufferSource.Get()
	bufIn := proxy.BufferSource.Get()
	defer proxy.BufferSource.Put(bufOut)
	defer proxy.BufferSource.Put(bufIn)

	if rr != nil {
		// We tried and failed to MITM. First copy already read data to upstream
		// before we start piping as usual
		_, copyErr := io.CopyBuffer(upstream, rr, *bufOut)
		if copyErr != nil {
			return log.Errorf("Error copying initial data to upstream: %v", copyErr)
		}
	}

	// Pipe data between the client and the proxy.
	writeErr, readErr := netx.BidiCopy(upstream, downstream, *bufOut, *bufIn)
	if isUnexpected(readErr) {
		return log.Errorf("Error piping data to downstream: %v", readErr)
	} else if isUnexpected(writeErr) {
		return log.Errorf("Error piping data to upstream at %v: %v", upstream.RemoteAddr(), writeErr)
	}
	return nil
}

func badGateway(cs *filters.ConnectionState, req *http.Request, err error) (*http.Response, *filters.ConnectionState, error) {
	log.Debugf("Responding BadGateway: %v", err)
	return filters.Fail(cs, req, http.StatusBadGateway, err)
}

func doRespondOK(cs *filters.ConnectionState, req *http.Request) (*http.Response, *filters.ConnectionState) {
	// filter.ShortCircuit never returns an error, so it's okay to ignore.
	resp, nextCS, _ := filters.ShortCircuit(cs, req, &http.Response{
		StatusCode: http.StatusOK,
	})
	return resp, nextCS
}
