package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"time"

	"github.com/getlantern/golog"
	"github.com/getlantern/hidden"
	"github.com/getlantern/proxy/filters"
)

var (
	log = golog.LoggerFor("proxy")
)

// DialFunc is the dial function to use for dialing the proxy.
type DialFunc func(isCONNECT bool, network, addr string) (conn net.Conn, err error)

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
	// returns no response, nothing is written to the client. (HTTP only)
	OnError func(ctx context.Context, req *http.Request, err error) *http.Response

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
		opts.Dial = func(isCONNECT bool, network, addr string) (conn net.Conn, err error) {
			return net.DialTimeout(network, addr, 30*time.Second)
		}
	}
	opts.applyHTTPDefaults()
	opts.applyCONNECTDefaults()
	return &proxy{opts}
}

func (opts *Opts) addIdleKeepAlive(header http.Header) {
	if opts.IdleTimeout > 0 {
		// Tell the client when we're going to time out due to idle connections
		header.Set("Keep-Alive", fmt.Sprintf("timeout=%d", int(opts.IdleTimeout.Seconds())-2))
	}
}

func respondBadGateway(writer io.Writer, req *http.Request, err error) {
	log.Debugf("Responding BadGateway: %v", err)
	respond(writer, req, http.StatusBadGateway, nil, []byte(hidden.Clean(err.Error())))
}

func respond(writer io.Writer, req *http.Request, statusCode int, header http.Header, body []byte) error {
	defer func() {
		if req.Body != nil {
			if err := req.Body.Close(); err != nil {
				log.Debugf("Error closing body of request: %s", err)
			}
		}
	}()

	if header == nil {
		header = make(http.Header)
	}
	resp := &http.Response{
		Header:     header,
		StatusCode: statusCode,
		ProtoMajor: 1,
		ProtoMinor: 1,
	}
	if body != nil {
		resp.Body = ioutil.NopCloser(bytes.NewReader(body))
	}
	return resp.Write(writer)
}
