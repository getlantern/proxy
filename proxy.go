package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/getlantern/errors"
	"github.com/getlantern/golog"
	"github.com/getlantern/mitm"
	"github.com/getlantern/proxy/filters"
)

const (
	serverTimingHeader = "Server-Timing"

	// MetricDialUpstream is the Server-Timing metric to record milliseconds to
	// dial upstream site when handling a CONNECT request.
	MetricDialUpstream = "dialupstream"

	// DialTimeoutHeader is a header that sets a timeout for dialing upstream
	// (only respected on CONNECT requests). The timeout is specified in
	// milliseconds.
	DialTimeoutHeader = "X-Lantern-Dial-Timeout"
)

// DialFunc is the dial function to use for dialing the proxy.
type DialFunc func(ctx context.Context, isCONNECT bool, network, addr string) (conn net.Conn, err error)

// Proxy is a proxy that can handle HTTP(S) traffic
type Proxy interface {
	// Handle handles a single connection, with in specified separately in case
	// there's a buffered reader or something of that sort in use.
	Handle(ctx context.Context, in io.Reader, conn net.Conn) error

	// Connect opens a CONNECT tunnel to the origin without requiring a CONNECT
	// request to first be sent on conn. It will not reply with CONNECT OK.
	Connect(ctx context.Context, in io.Reader, conn net.Conn, origin string) error

	// Serve runs a server on the given Listener
	Serve(l net.Listener) error
}

// RequestAware is an interface for connections that are able to modify requests
// before they're sent on the connection.
type RequestAware interface {
	// OnRequest allows this connection to make modifications to the request as
	// needed
	OnRequest(req *http.Request)
}

// ResponseAware is an interface for connections that are interested in knowing
// about responses received on the connection.
type ResponseAware interface {
	// OnResponse allows this connection to learn about responses
	OnResponse(req *http.Request, resp *http.Response, err error)
}

// Opts defines options for configuring a Proxy
type Opts struct {
	// IdleTimeout, if specified, lets us know to include an appropriate
	// KeepAlive: timeout header in the responses.
	IdleTimeout time.Duration

	// BufferSource specifies a BufferSource, leave nil to use default.
	BufferSource BufferSource

	// Filter is an optional Filter that will be invoked for every Request
	Filter filters.Filter

	// OnError, if specified, can return a response to be presented to the client
	// in the event that there's an error round-tripping upstream. If the function
	// returns no response, nothing is written to the client. Read indicates
	// whether the error occurred on reading a request or not. (HTTP only)
	OnError func(ctx filters.Context, req *http.Request, read bool, err error) *http.Response

	// OKWaitsForUpstream specifies whether or not to wait on dialing upstream
	// before responding OK to a CONNECT request (CONNECT only).
	OKWaitsForUpstream bool
	// OKSendsServerTiming specifies whether or not to send back Server-Timing header
	// when responding OK to CONNECT request.
	// See https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Server-Timing.
	// The only metric for now is dialupstream, so the value is in the form "dialupstream;dur=42".
	OKSendsServerTiming bool

	// Dial is the function that's used to dial upstream.
	Dial DialFunc

	// ShouldMITM is an optional function for determining whether or not the given
	// HTTP CONNECT request to the given upstreamAddr is eligible for being MITM'ed.
	ShouldMITM func(req *http.Request, upstreamAddr string) bool

	// MITMOpts, if specified, instructs proxy to attempt to man-in-the-middle
	// connections and handle them as an HTTP proxy. If the connection cannot be
	// mitm'ed (e.g. Client Hello doesn't include an SNI header) or if the
	// contents isn't HTTP, the connection is handled as normal without MITM.
	MITMOpts *mitm.Opts
}

type proxy struct {
	*Opts
	mitmIC      *mitm.Interceptor
	mitmDomains []*regexp.Regexp
	log         golog.Logger
}

// New creates a new Proxy configured with the specified Opts. If there's an
// error initializing MITM, this returns an mitmErr, however the proxy is still
// usable (it just won't MITM).
func New(opts *Opts) (newProxy Proxy, mitmErr error) {
	if opts.Dial == nil {
		opts.Dial = func(ctx context.Context, isCONNECT bool, network, addr string) (conn net.Conn, err error) {
			timeout := 30 * time.Second
			deadline, hasDeadline := ctx.Deadline()
			if hasDeadline {
				timeout = deadline.Sub(time.Now())
			}
			return net.DialTimeout(network, addr, timeout)
		}
	}
	p := &proxy{
		Opts:        opts,
		mitmDomains: make([]*regexp.Regexp, 0),
		log:         golog.LoggerFor("proxy"),
	}
	p.applyHTTPDefaults()
	p.applyCONNECTDefaults()

	if opts.MITMOpts != nil {
		p.mitmIC, mitmErr = mitm.Configure(opts.MITMOpts)
		if mitmErr != nil {
			mitmErr = errors.New("Unable to configure MITM: %v", mitmErr)
		} else {
			for _, domain := range opts.MITMOpts.Domains {
				re, err := domainToRegex(domain)
				if err != nil {
					p.log.Errorf("Unable to convert domain %v to regex: %v", domain, err)
				} else {
					p.mitmDomains = append(p.mitmDomains, re)
				}
			}
		}
	}

	return p, mitmErr
}

// OnFirstOnly returns a filter that applies the given filter only on the first
// request on a given connection.
func OnFirstOnly(filter filters.Filter) filters.Filter {
	return filters.FilterFunc(func(ctx filters.Context, req *http.Request, next filters.Next) (*http.Response, filters.Context, error) {
		requestNumber := ctx.RequestNumber()
		if requestNumber == 1 {
			return filter.Apply(ctx, req, next)
		}
		return next(ctx, req)
	})
}

func (opts *Opts) addIdleKeepAlive(header http.Header) {
	if opts.IdleTimeout > 0 {
		// Tell the client when we're going to time out due to idle connections
		header.Set("Keep-Alive", fmt.Sprintf("timeout=%d", int(opts.IdleTimeout.Seconds())-2))
	}
}
