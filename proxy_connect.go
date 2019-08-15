package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/getlantern/lampshade"
	"github.com/getlantern/netx"
	"github.com/getlantern/proxy/filters"
	"github.com/getlantern/reconn"
	"github.com/opentracing/opentracing-go"
)

const (
	connectRequest = "CONNECT %v HTTP/1.1\r\nHost: %v\r\n\r\n"

	maxHTTPSize = 2 << 15 // 64K
)

// BufferSource is a source for buffers used in reading/writing.
type BufferSource interface {
	Get() []byte
	Put(buf []byte)
}

func (proxy *proxy) applyCONNECTDefaults() {
	// Apply defaults
	if proxy.BufferSource == nil {
		proxy.BufferSource = &defaultBufferSource{}
	}
	if proxy.ShouldMITM == nil {
		proxy.ShouldMITM = proxy.defaultShouldMITM
	} else {
		orig := proxy.ShouldMITM
		proxy.ShouldMITM = func(req *http.Request, upstreamAddr string) bool {
			if !orig(req, upstreamAddr) {
				return false
			}
			return proxy.defaultShouldMITM(req, upstreamAddr)
		}
	}
}

// interceptor configures an Interceptor.
type connectInterceptor struct {
	idleTimeout        time.Duration
	bufferSource       BufferSource
	dial               DialFunc
	okWaitsForUpstream bool
}

func (proxy *proxy) nextCONNECT(downstream net.Conn) filters.Next {
	return func(ctx filters.Context, modifiedReq *http.Request) (*http.Response, filters.Context, error) {
		var resp *http.Response
		upstreamAddr := modifiedReq.URL.Host
		nextCtx := ctx.WithValue(ctxKeyUpstreamAddr, upstreamAddr)

		proxy.log.Debugf("proxy_connect modifiedReq %#v", modifiedReq)
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
			resp, nextCtx = respondOK(resp, modifiedReq, nextCtx)
			if proxy.OKSendsServerTiming {
				proxy.addDialUpstreamHeader(resp, 0)
			}
			return resp, nextCtx, nil
		}

		var start time.Time
		if proxy.OKSendsServerTiming {
			start = time.Now()
		}

		// Note - for CONNECT requests, we use the Host from the request URL, not the
		// Host header. See discussion here:
		// https://ask.wireshark.org/questions/22988/http-host-header-with-and-without-port-number
		dialCtx, cancelDial := proxy.addDialDeadlineIfNecessary(ctx, modifiedReq)
		upstream, err := proxy.Dial(dialCtx, true, "tcp", upstreamAddr)
		cancelDial()
		if err != nil {
			if proxy.OKWaitsForUpstream {
				return proxy.badGateway(ctx, modifiedReq, err)
			}
			return nil, ctx, err
		}

		// In this case, waited to successfully dial upstream before responding
		// OK. Lantern uses this logic on server-side proxies so that the Lantern
		// client retains the opportunity to fail over to a different proxy server
		// just in case that one is able to reach the origin. This is relevant,
		// for example, if some proxy servers reside in jurisdictions where an
		// origin site is blocked but other proxy servers don't.
		resp, nextCtx = respondOK(resp, modifiedReq, nextCtx)
		if proxy.OKSendsServerTiming {
			proxy.addDialUpstreamHeader(resp, time.Since(start))
		}

		nextCtx = nextCtx.WithValue(ctxKeyUpstream, upstream)
		return resp, nextCtx, nil
	}
}

func (proxy *proxy) addDialUpstreamHeader(resp *http.Response, duration time.Duration) {
	resp.Header.Add(serverTimingHeader, fmt.Sprintf("dialupstream;dur=%d", duration/time.Millisecond))
}

func (proxy *proxy) addDialDeadlineIfNecessary(ctx context.Context, req *http.Request) (context.Context, context.CancelFunc) {
	timeoutString := req.Header.Get(DialTimeoutHeader)
	if timeoutString == "" {
		return ctx, noopCancel
	}

	timeoutInt, err := strconv.ParseInt(timeoutString, 10, 64)
	if err != nil {
		proxy.log.Errorf("Invalid %v, expected integer, got '%v'", DialTimeoutHeader, timeoutString)
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

func respondOK(resp *http.Response, req *http.Request, ctx filters.Context) (*http.Response, filters.Context) {
	suppressOK := ctx.Value(ctxKeyNoRespondOkay) != nil
	if !suppressOK {
		resp, ctx, _ = filters.ShortCircuit(ctx, req, &http.Response{
			StatusCode: http.StatusOK,
		})
	}
	return resp, ctx
}

func (proxy *proxy) Connect(ctx context.Context, in io.Reader, conn net.Conn, origin string) error {
	pin := io.MultiReader(strings.NewReader(fmt.Sprintf(connectRequest, origin, origin)), in)
	return proxy.Handle(context.WithValue(ctx, ctxKeyNoRespondOkay, "true"), pin, conn)
}

func (proxy *proxy) proceedWithConnect(ctx filters.Context, req *http.Request, upstreamAddr string, upstream net.Conn, downstream net.Conn) error {
	if upstream == nil {
		var dialErr error
		upstream, dialErr = proxy.Dial(ctx, true, "tcp", upstreamAddr)
		if dialErr != nil {
			return dialErr
		}
	}
	defer func() {
		if closeErr := upstream.Close(); closeErr != nil {
			proxy.log.Tracef("Error closing upstream connection: %s", closeErr)
		}
	}()
	if parentSpan := opentracing.SpanFromContext(ctx); parentSpan != nil {
		proxy.log.Debug("Extracted span from incoming proxy context in balancer")
	} else {
		proxy.log.Debug("No parent span in context in balancer!!")
	}
	span, spannedCtx := opentracing.StartSpanFromContext(ctx, "proxy-data-shuffling")
	ctx = filters.AdaptContext(spannedCtx)
	defer span.Finish()

	var rr io.Reader
	if proxy.ShouldMITM(req, upstreamAddr) {
		// Try to MITM the connection
		downstreamMITM, upstreamMITM, mitming, err := proxy.mitmIC.MITM(downstream, upstream)
		if err != nil {
			return proxy.log.Errorf("Unable to MITM connection: %v", err)
		}
		downstream = downstreamMITM
		upstream = upstreamMITM
		if mitming {
			// Try to read HTTP request and process as HTTP assuming that requests
			// (not including body) are always smaller than 65K. If this assumption is
			// violated, we won't be able to process the data on this connection.
			downstreamRR := reconn.Wrap(downstream, maxHTTPSize)
			_, peekReqErr := http.ReadRequest(bufio.NewReader(downstreamRR))
			var rrErr error
			rr, rrErr = downstreamRR.Rereader()
			if rrErr != nil {
				// Reading request overflowed, abort
				return proxy.log.Errorf("Unable to re-read data: %v", rrErr)
			}
			if peekReqErr == nil {
				// Handle as HTTP, prepend already read HTTP request
				fullDownstream := io.MultiReader(rr, downstream)
				// Remove upstream info from context so that handle doesn't try to
				// process this as a CONNECT
				ctx = ctx.WithValue(ctxKeyUpstream, nil).WithValue(ctxKeyUpstreamAddr, nil)
				ctx = ctx.WithMITMing()
				return proxy.handle(ctx, fullDownstream, downstream, upstream)
			}

			// We couldn't read the first HTTP Request, fall back to piping data
		}
	}

	// Prepare to pipe data between the client and the proxy.
	bufOut := proxy.BufferSource.Get()
	bufIn := proxy.BufferSource.Get()
	defer proxy.BufferSource.Put(bufOut)
	defer proxy.BufferSource.Put(bufIn)

	if rr != nil {
		// We tried and failed to MITM. First copy already read data to upstream
		// before we start piping as usual
		_, copyErr := io.CopyBuffer(upstream, rr, bufOut)
		if copyErr != nil {
			return proxy.log.Errorf("Error copying initial data to upstream: %v", copyErr)
		}
	}

	// Pipe data between the client and the proxy.
	writeErr, readErr := netx.BidiCopy(upstream, downstream, bufOut, bufIn)
	if isUnexpected(readErr) {
		return proxy.log.Errorf("Error piping data to downstream: %v", readErr)
	} else if isUnexpected(writeErr) {
		return proxy.log.Errorf("Error piping data to upstream at %v: %v", upstream.RemoteAddr(), writeErr)
	}
	return nil
}

func (proxy *proxy) badGateway(ctx filters.Context, req *http.Request, err error) (*http.Response, filters.Context, error) {
	proxy.log.Debugf("Responding BadGateway: %v", err)
	return filters.Fail(ctx, req, http.StatusBadGateway, err)
}

type defaultBufferSource struct{}

func (dbs *defaultBufferSource) Get() []byte {
	// We limit ourselves to lampshade.MaxDataLen to ensure compatibility with it
	return make([]byte, lampshade.MaxDataLen)
}

func (dbs *defaultBufferSource) Put(buf []byte) {
	// do nothing
}

func (proxy *proxy) defaultShouldMITM(req *http.Request, upstreamAddr string) bool {
	if proxy.mitmIC == nil {
		return false
	}
	host, _, err := net.SplitHostPort(upstreamAddr)
	if err != nil {
		return false
	}
	for _, mitmDomain := range proxy.mitmDomains {
		if mitmDomain.MatchString(host) {
			return true
		}
	}
	return false
}
