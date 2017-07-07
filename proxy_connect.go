package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/getlantern/errors"
	"github.com/getlantern/idletiming"
	"github.com/getlantern/lampshade"
	"github.com/getlantern/netx"
	"github.com/getlantern/proxy/filters"
)

// BufferSource is a source for buffers used in reading/writing.
type BufferSource interface {
	Get() []byte
	Put(buf []byte)
}

func (opts *Opts) applyCONNECTDefaults() {
	// Apply defaults
	if opts.BufferSource == nil {
		opts.BufferSource = &defaultBufferSource{}
	}
}

// interceptor configures an Interceptor.
type connectInterceptor struct {
	idleTimeout        time.Duration
	bufferSource       BufferSource
	dial               DialFunc
	okWaitsForUpstream bool
}

func (proxy *proxy) handleCONNECT(ctx context.Context, downstream net.Conn, req *http.Request) error {
	resp, err := proxy.Filter.Apply(ctx, req, proxy.nextCONNECT(downstream))
	if err != nil {
		resp = proxy.OnError(ctx, req, err)
	}
	if resp != nil {
		return proxy.writeResponse(downstream, req, resp)
	}
	return err
}

func (proxy *proxy) nextCONNECT(downstream net.Conn) filters.Next {
	return func(ctx context.Context, modifiedReq *http.Request) (*http.Response, error) {
		var upstream net.Conn
		closeUpstream := false
		defer func() {
			if closeErr := downstream.Close(); closeErr != nil {
				log.Tracef("Error closing downstream connection: %s", closeErr)
			}
			if closeUpstream {
				if closeErr := upstream.Close(); closeErr != nil {
					log.Tracef("Error closing upstream connection: %s", closeErr)
				}
			}
		}()

		var err error

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
			err = proxy.respondOK(downstream, modifiedReq)
			if err != nil {
				return nil, err
			}
		}

		// Note - for CONNECT requests, we use the Host from the request URL, not the
		// Host header. See discussion here:
		// https://ask.wireshark.org/questions/22988/http-host-header-with-and-without-port-number
		if upstream, err = proxy.Dial(true, "tcp", modifiedReq.URL.Host); err != nil {
			if proxy.OKWaitsForUpstream {
				respondBadGateway(downstream, modifiedReq, err)
			} else {
				log.Error(err)
			}
			return nil, err
		}
		closeUpstream = true

		if proxy.OKWaitsForUpstream {
			// In this case, we're waiting to successfully dial upstream before
			// responding OK. Lantern uses this logic on server-side proxies so that the
			// Lantern client retains the opportunity to fail over to a different proxy
			// server just in case that one is able to reach the origin. This is
			// relevant, for example, if some proxy servers reside in jurisdictions
			// where an origin site is blocked but other proxy servers don't.
			err = proxy.respondOK(downstream, modifiedReq)
			if err != nil {
				return nil, err
			}
		}

		return nil, proxy.copy(upstream, downstream)
	}
}

func (proxy *proxy) copy(upstream, downstream net.Conn) error {
	// Pipe data between the client and the proxy.
	bufOut := proxy.BufferSource.Get()
	bufIn := proxy.BufferSource.Get()
	defer proxy.BufferSource.Put(bufOut)
	defer proxy.BufferSource.Put(bufIn)
	writeErr, readErr := netx.BidiCopy(upstream, downstream, bufOut, bufIn)
	// Note - we ignore idled errors because these are okay per the HTTP spec.
	// See https://www.w3.org/Protocols/rfc2616/rfc2616-sec8.html#sec8.1.4
	// We also ignore "broken pipe" errors on piping to downstream because they're
	// usually caused by the client disconnecting and we don't worry about that.
	if readErr != nil && readErr != io.EOF && !strings.Contains(readErr.Error(), "broken pipe") {
		return errors.New("Error piping data to downstream: %v", readErr)
	} else if writeErr != nil && writeErr != idletiming.ErrIdled {
		return errors.New("Error piping data to upstream: %v", writeErr)
	}
	return nil
}

func (proxy *proxy) respondOK(writer io.Writer, req *http.Request) error {
	err := respond(writer, req, http.StatusOK, proxy.idleKeepAliveHeader(), nil)
	if err != nil {
		fullErr := errors.New("Unable to respond OK: ", err)
		log.Error(fullErr)
		return fullErr
	}
	return nil
}

func (proxy *proxy) idleKeepAliveHeader() http.Header {
	header := make(http.Header, 1)
	proxy.addIdleKeepAlive(header)
	return header
}

type defaultBufferSource struct{}

func (dbs *defaultBufferSource) Get() []byte {
	// We limit ourselves to lampshade.MaxDataLen to ensure compatibility with it
	return make([]byte, lampshade.MaxDataLen)
}

func (dbs *defaultBufferSource) Put(buf []byte) {
	// do nothing
}
