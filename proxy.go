package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/getlantern/golog"
	"github.com/getlantern/proxy/v3/filters"
)

const (
	defaultDialTimeout = 30 * time.Second

	serverTimingHeader = "Server-Timing"

	// MetricDialUpstream is the Server-Timing metric to record milliseconds to
	// dial upstream site when handling a CONNECT request.
	MetricDialUpstream = "dialupstream"

	// DialTimeoutHeader is a header that sets a timeout for dialing upstream
	// (only respected on CONNECT requests). The timeout is specified in
	// milliseconds.
	DialTimeoutHeader = "X-Lantern-Dial-Timeout"
)

var (
	log = golog.LoggerFor("proxy")
)

// DialFunc is the dial function to use for dialing the proxy.
type DialFunc func(ctx context.Context, isCONNECT bool, network, addr string) (conn net.Conn, err error)

// Proxy is a proxy that can handle HTTP(S) traffic
type Proxy interface {
	// Handle handles a single connection, with in specified separately in case
	// there's a buffered reader or something of that sort in use.
	// dialCtx applies only when dialing upstream.
	Handle(dialCtx context.Context, in io.Reader, conn net.Conn) error

	// Connect opens a CONNECT tunnel to the origin without requiring a CONNECT
	// request to first be sent on conn. It will not reply with CONNECT OK.
	// dialCtx applies only when dialing upstream.
	Connect(dialCtx context.Context, in io.Reader, conn net.Conn, origin string) error

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
	BufferSource bufferSource

	// Filter is an optional Filter that will be invoked for every Request
	Filter filters.Filter

	// OnError, if specified, can return a response to be presented to the client
	// in the event that there's an error round-tripping upstream. If the function
	// returns no response, nothing is written to the client. Read indicates
	// whether the error occurred on reading a request or not. (HTTP only)
	OnError func(cs *filters.ConnectionState, req *http.Request, read bool, err error) *http.Response

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
}

type proxy struct {
	*Opts
}

// New creates a new Proxy configured with the specified Opts.
func New(opts *Opts) (newProxy Proxy) {
	if opts.Dial == nil {
		opts.Dial = func(ctx context.Context, isCONNECT bool, network, addr string) (conn net.Conn, err error) {
			ctx, cancel := context.WithTimeout(ctx, defaultDialTimeout)
			defer cancel()
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
	}
	p := &proxy{
		Opts: opts,
	}
	p.applyHTTPDefaults()
	p.applyCONNECTDefaults()

	return p
}

// OnFirstOnly returns a filter that applies the given filter only on the first
// request on a given connection.
func OnFirstOnly(filter filters.Filter) filters.Filter {
	return filters.FilterFunc(func(cs *filters.ConnectionState, req *http.Request, next filters.Next) (*http.Response, *filters.ConnectionState, error) {
		requestNumber := cs.RequestNumber()
		if requestNumber == 1 {
			return filter.Apply(cs, req, next)
		}
		return next(cs, req)
	})
}

func (opts *Opts) addIdleKeepAlive(header http.Header) {
	if opts.IdleTimeout > 0 {
		// Tell the client when we're going to time out due to idle connections
		header.Set("Keep-Alive", fmt.Sprintf("timeout=%d", int(opts.IdleTimeout.Seconds())-2))
	}
}
