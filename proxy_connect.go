package proxy

import (
	"bytes"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/getlantern/errors"
	"github.com/getlantern/hidden"
	"github.com/getlantern/idletiming"
	"github.com/getlantern/netx"
	"github.com/getlantern/ops"
)

// BufferSource is a source for buffers used in reading/writing.
type BufferSource interface {
	Get() []byte
	Put(buf []byte)
}

// CONNECT returns a Handler that processes CONNECT requests.
//
// defaultPort: specifies which port to use if none was found in the initial
// request
//
// idleTimeout: if specified, lets us know to include an appropriate
// KeepAlive: timeout header in the CONNECT response
//
// bufferSource: specifies a BufferSource, leave nil to use default
//
// dial: the function that's used to dial upstream
func CONNECT(
	defaultPort int,
	idleTimeout time.Duration,
	bufferSource BufferSource,
	dial DialFunc,
) Interceptor {
	// Apply defaults
	if bufferSource == nil {
		bufferSource = &defaultBufferSource{}
	}

	ic := &connectInterceptor{
		defaultPort:  defaultPort,
		idleTimeout:  idleTimeout,
		bufferSource: bufferSource,
		dial:         dial,
	}
	return ic.connect
}

// interceptor configures an Interceptor.
type connectInterceptor struct {
	idleTimeout  time.Duration
	defaultPort  int
	bufferSource BufferSource
	dial         DialFunc
}

func (ic *connectInterceptor) connect(op ops.Op, w http.ResponseWriter, req *http.Request) {
	var downstream net.Conn
	var upstream net.Conn
	var err error

	closeDownstream := false
	closeUpstream := false
	defer func() {
		if closeDownstream {
			if closeErr := downstream.Close(); closeErr != nil {
				log.Tracef("Error closing downstream connection: %s", closeErr)
			}
		}
		if closeUpstream {
			if closeErr := upstream.Close(); closeErr != nil {
				log.Tracef("Error closing upstream connection: %s", closeErr)
			}
		}
	}()

	// Hijack underlying connection.
	downstream, _, err = w.(http.Hijacker).Hijack()
	if err != nil {
		respondBadGateway(w, op.FailIf(errors.New("Unable to hijack connection: %s", err)))
		return
	}
	closeDownstream = true

	upstream, err = ic.dial("tcp", ic.hostIncludingPort(req))
	if err != nil {
		ic.respondBadGatewayHijacked(downstream, req, err)
		return
	}
	closeUpstream = true

	// Send OK response
	err = ic.respondOK(downstream, req, w.Header())
	if err != nil {
		op.FailIf(log.Errorf("Unable to respond OK: %s", err))
		return
	}

	// Pipe data between the client and the proxy.
	bufOut := ic.bufferSource.Get()
	bufIn := ic.bufferSource.Get()
	defer ic.bufferSource.Put(bufOut)
	defer ic.bufferSource.Put(bufIn)
	writeErr, readErr := netx.BidiCopy(upstream, downstream, bufOut, bufIn)
	// Note - we ignore idled errors because these are okay per the HTTP spec.
	// See https://www.w3.org/Protocols/rfc2616/rfc2616-sec8.html#sec8.1.4
	if readErr != nil && readErr != io.EOF {
		log.Debugf("Error piping data to downstream: %v", readErr)
	} else if writeErr != nil && writeErr != idletiming.ErrIdled {
		log.Debugf("Error piping data to upstream: %v", writeErr)
	}
}

// hostIncludingPort extracts the host:port from a request.  It fills in a
// a default port if none was found in the request.
func (ic *connectInterceptor) hostIncludingPort(req *http.Request) string {
	_, port, err := net.SplitHostPort(req.Host)
	if port == "" || err != nil {
		return req.Host + ":" + strconv.Itoa(ic.defaultPort)
	}
	return req.Host
}

func (ic *connectInterceptor) respondOK(writer io.Writer, req *http.Request, respHeaders http.Header) error {
	addIdleKeepAlive(respHeaders, ic.idleTimeout)
	return ic.respondHijacked(writer, req, http.StatusOK, respHeaders, nil)
}

func (ic *connectInterceptor) respondBadGatewayHijacked(writer io.Writer, req *http.Request, err error) error {
	log.Debugf("Responding %v", http.StatusBadGateway)
	var body []byte
	if err != nil {
		body = []byte(hidden.Clean(err.Error()))
	}
	return ic.respondHijacked(writer, req, http.StatusBadGateway, make(http.Header), body)
}

func (ic *connectInterceptor) respondHijacked(writer io.Writer, req *http.Request, statusCode int, respHeaders http.Header, body []byte) error {
	defer func() {
		if req.Body != nil {
			if err := req.Body.Close(); err != nil {
				log.Debugf("Error closing body of request: %s", err)
			}
		}
	}()

	if respHeaders == nil {
		respHeaders = make(http.Header)
	}
	resp := &http.Response{
		Header:     respHeaders,
		StatusCode: statusCode,
		ProtoMajor: 1,
		ProtoMinor: 1,
	}
	if body != nil {
		resp.Body = ioutil.NopCloser(bytes.NewReader(body))
	}
	return resp.Write(writer)
}

type defaultBufferSource struct{}

func (dbs *defaultBufferSource) Get() []byte {
	// We use the same default buffer size as io.Copy.
	return make([]byte, 32768)
}

func (dbs *defaultBufferSource) Put(buf []byte) {
	// do nothing
}
