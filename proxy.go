package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/getlantern/errors"
	"github.com/getlantern/golog"
	"github.com/getlantern/mitm"
	"github.com/getlantern/proxy/filters"
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
	Handle(ctx context.Context, in io.Reader, conn net.Conn) error

	// Connect opens a CONNECT tunnel to the origin without requiring a CONNECT
	// request to first be sent on conn. It will not reply with CONNECT OK.
	Connect(ctx context.Context, in io.Reader, conn net.Conn, origin string) error

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

	// MITMOpts, if specified, instructs proxy to attempt to man-in-the-middle
	// connections and handle them as an HTTP proxy. If the connection cannot be
	// mitm'ed (e.g. Client Hello doesn't include an SNI header) or if the
	// contents isn't HTTP, the connection is handled as normal without MITM.
	MITMOpts *mitm.Opts
}

type proxy struct {
	*Opts
	mitmIC *mitm.Interceptor
}

// New creates a new Proxy configured with the specified Opts.
func New(opts *Opts) (Proxy, error) {
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

	p := &proxy{Opts: opts}
	var mitmErr error
	if opts.MITMOpts != nil {
		p.mitmIC, mitmErr = mitm.Configure(opts.MITMOpts)
		if mitmErr != nil {
			mitmErr = errors.New("Unable to configure MITM: %v", mitmErr)
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
