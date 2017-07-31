package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/getlantern/golog"
	"github.com/getlantern/proxy/filters"
)

var (
	log = golog.LoggerFor("proxy")
)

// DialFunc is the dial function to use for dialing the proxy.
type DialFunc func(ctx context.Context, isCONNECT bool, network, addr string) (conn net.Conn, err error)

// Proxy is a proxy that can handle HTTP(S) traffic
type Proxy interface {
	// Handle handles a single connection
	Handle(ctx context.Context, conn net.Conn) error

	// Serve runs a server on the given Listener
	Serve(l net.Listener) error
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

	// Dial is the function that's used to dial upstream.
	Dial DialFunc
}

type proxy struct {
	*Opts
}

// New creates a new Proxy configured with the specified Opts.
func New(opts *Opts) Proxy {
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
	opts.applyHTTPDefaults()
	opts.applyCONNECTDefaults()
	return &proxy{opts}
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
