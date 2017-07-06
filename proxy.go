package proxy

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"time"

	"github.com/getlantern/golog"
	"github.com/getlantern/hidden"
)

var (
	log = golog.LoggerFor("proxy")
)

// DialFunc is the dial function to use for dialing the proxy.
type DialFunc func(network, addr string) (conn net.Conn, err error)

// Proxy is a proxy that can handle HTTP(S) traffic
type Proxy interface {
	Handle(conn net.Conn) error
}

// Opts defines options for configuring a Proxy
type Opts struct {
	// IdleTimeout, if specified, lets us know to include an appropriate
	// KeepAlive: timeout header in the responses.
	IdleTimeout time.Duration

	// BufferSource specifies a BufferSource, leave nil to use default.
	BufferSource BufferSource

	// OnRequest, if specified, is called on every read request. It must always
	// return a Request. It can optionally return a Response, in which case
	// processing is interrupted and that Response is written downstream.
	// (HTTP only).
	OnRequest func(req *http.Request) (*http.Request, *http.Response)

	// OnCONNECT, if specified, is called on all CONNECT requests. It behaves
	// identically to OnRequest.
	OnCONNECT func(req *http.Request) (*http.Request, *http.Response)

	// OnResponse, if specified, is called on every read response (HTTP only).
	OnResponse func(resp *http.Response) *http.Response

	// OnError, if specified, can return a response to be presented to the client
	// in the event that there's an error round-tripping upstream. If the function
	// returns no response, nothing is written to the client. (HTTP only)
	OnError func(req *http.Request, err error) *http.Response

	// DiscardFirstRequest, if true, causes the first request to the handler to be
	// discarded (HTTP only).
	DiscardFirstRequest bool

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
