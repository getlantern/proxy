package proxy

import (
	"bufio"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/getlantern/errors"
	"github.com/getlantern/hidden"
	"github.com/getlantern/ops"
)

// HTTP returns a Handler that processes plain-text HTTP requests.
//
// discardFirstRequest: if true, causes the first request to the handler to be
// discarded
//
// idleTimeout: if specified, lets us know to include an appropriate
// KeepAlive: timeout header in the CONNECT response
//
// onRequest: if specified, is called on every read request
//
// onResponse: if specified, is called on every read response
//
// onError: if specified, if there's an error making a round trip, this function
// can return a response to be presented to the client. If the function returns
// no response, nothing is written to the client.
//
// dial: the function that's used to dial upstream
func HTTP(
	discardFirstRequest bool,
	idleTimeout time.Duration,
	onRequest func(req *http.Request) *http.Request,
	onResponse func(resp *http.Response) *http.Response,
	onError func(req *http.Request, err error) *http.Response,
	dial DialFunc,
) Interceptor {
	// Apply defaults
	if onRequest == nil {
		onRequest = defaultOnRequest
	}
	if onResponse == nil {
		onResponse = defaultOnResponse
	}
	if onError == nil {
		onError = defaultOnError
	}
	if idleTimeout > 0 {
		origOnResponse := onResponse
		onResponse = func(resp *http.Response) *http.Response {
			resp = origOnResponse(resp)
			addIdleKeepAlive(resp.Header, idleTimeout)
			return resp
		}
	}

	ic := &httpInterceptor{
		discardFirstRequest: discardFirstRequest,
		idleTimeout:         idleTimeout,
		onRequest:           onRequest,
		onResponse:          onResponse,
		onError:             onError,
		dial:                dial,
	}
	return ic.intercept
}

type httpInterceptor struct {
	discardFirstRequest bool
	idleTimeout         time.Duration
	onRequest           func(req *http.Request) *http.Request
	onResponse          func(resp *http.Response) *http.Response
	onError             func(req *http.Request, err error) *http.Response
	dial                DialFunc
}

func (ic *httpInterceptor) intercept(op ops.Op, w http.ResponseWriter, req *http.Request) {
	var downstream net.Conn
	var downstreamBuffered *bufio.ReadWriter
	tr := &http.Transport{
		Dial:                ic.dial,
		IdleConnTimeout:     ic.idleTimeout,
		MaxIdleConnsPerHost: 8, // just to keep clients from getting too greedy
	}
	var err error

	closeDownstream := false
	defer func() {
		if closeDownstream {
			if closeErr := downstream.Close(); closeErr != nil {
				log.Tracef("Error closing downstream connection: %s", closeErr)
			}
		}
		tr.CloseIdleConnections()
	}()

	// Hijack underlying connection.
	downstream, downstreamBuffered, err = w.(http.Hijacker).Hijack()
	if err != nil {
		respondBadGateway(w, op.FailIf(errors.New("Unable to hijack connection: %s", err)))
		return
	}
	closeDownstream = true

	ic.processRequests(op, req.RemoteAddr, req, downstream, downstreamBuffered, tr)
	return
}

func (ic *httpInterceptor) processRequests(op ops.Op, remoteAddr string, req *http.Request, downstream net.Conn, downstreamBuffered *bufio.ReadWriter, tr *http.Transport) {
	var readErr error

	first := true
	for {
		discardRequest := first && ic.discardFirstRequest
		if discardRequest {
			err := ic.onRequest(req).Write(ioutil.Discard)
			if err != nil {
				log.Debugf("Error discarding first request: %v", err)
				return
			}
		} else {
			resp, err := tr.RoundTrip(prepareRequest(ic.onRequest(req)))
			if err != nil {
				log.Debugf("Error round tripping: %v", err)
				errResp := ic.onError(req, err)
				if errResp != nil {
					errResp.Request = req
					ic.writeResponse(op, downstream, errResp)
				}
				return
			}
			if !ic.writeResponse(op, downstream, resp) {
				return
			}
		}

		if req.Close {
			// Client signaled that they would close the connection after this
			// request, finish
			return
		}

		req, readErr = http.ReadRequest(downstreamBuffered.Reader)
		if readErr != nil {
			if isUnexpected(readErr) {
				log.Debug(op.FailIf(errors.New("Unable to read next request from downstream: %v", readErr)))
			}
			break
		}

		// Preserve remote address from original request
		req.RemoteAddr = remoteAddr
		first = false
	}
}

func (ic *httpInterceptor) writeResponse(op ops.Op, downstream net.Conn, resp *http.Response) bool {
	var out io.Writer = downstream
	belowHTTP11 := !resp.Request.ProtoAtLeast(1, 1)
	if belowHTTP11 && resp.StatusCode < 200 {
		// HTTP 1.0 doesn't define status codes below 200, discard response
		// see http://coad.measurement-factory.com/cgi-bin/coad/SpecCgi?spec_id=rfc2616#excerpt/rfc2616/859a092cb26bde76c25284196171c94d
		out = ioutil.Discard
	} else {
		resp = ic.onResponse(prepareResponse(resp, belowHTTP11))
	}
	writeErr := resp.Write(out)
	if writeErr != nil {
		if isUnexpected(writeErr) {
			log.Debug(op.FailIf(errors.New("Unable to write response to downstream: %v", writeErr)))
		}
		return false
	}
	return true
}

// prepareRequest prepares the request in line with the HTTP spec for proxies.
func prepareRequest(req *http.Request) *http.Request {
	outReq := new(http.Request)
	// Beware, this will make a shallow copy. We have to copy all maps
	*outReq = *req

	outReq.Proto = "HTTP/1.1"
	outReq.ProtoMajor = 1
	outReq.ProtoMinor = 1
	// Overwrite close flag: keep persistent connection for the backend servers
	outReq.Close = false

	// Request Header
	outReq.Header = make(http.Header)
	copyHeadersForForwarding(outReq.Header, req.Header)
	// Ensure we have a HOST header (important for Go 1.6+ because http.Server
	// strips the HOST header from the inbound request)
	outReq.Header.Set("Host", req.Host)

	// Request URL
	outReq.URL = cloneURL(req.URL)
	// We know that is going to be HTTP always because HTTPS isn't forwarded.
	// We need to hardcode it here because req.URL.Scheme can be undefined, since
	// client request don't need to use absolute URIs
	outReq.URL.Scheme = "http"
	// We need to make sure the host is defined in the URL (not the actual URI)
	outReq.URL.Host = req.Host
	outReq.URL.RawQuery = req.URL.RawQuery
	outReq.Body = req.Body

	userAgent := req.UserAgent()
	if userAgent == "" {
		outReq.Header.Del("User-Agent")
	} else {
		outReq.Header.Set("User-Agent", userAgent)
	}

	return outReq
}

// prepareResponse prepares the response in line with the HTTP spec
func prepareResponse(resp *http.Response, belowHTTP11 bool) *http.Response {
	origHeader := resp.Header
	resp.Header = make(http.Header)
	copyHeadersForForwarding(resp.Header, origHeader)
	// Below added due to CoAdvisor test failure
	if resp.Header.Get("Date") == "" {
		resp.Header.Set("Date", time.Now().Format(time.RFC850))
	}
	if belowHTTP11 {
		// Also, make sure we're not sending chunked transfer encoding to 1.0 clients
		resp.TransferEncoding = nil
	}
	return resp
}

// cloneURL provides update safe copy by avoiding shallow copying User field
func cloneURL(i *url.URL) *url.URL {
	out := *i
	if i.User != nil {
		out.User = &(*i.User)
	}
	return &out
}

// copyHeadersForForwarding will copy the headers but filter those that shouldn't be
// forwarded
func copyHeadersForForwarding(dst, src http.Header) {
	var extraHopByHopHeaders []string
	for k, vv := range src {
		switch k {
		// Skip hop-by-hop headers, ref section 13.5.1 of http://www.ietf.org/rfc/rfc2616.txt
		case "Connection":
			// section 14.10 of rfc2616
			// the slice is short typically, don't bother sort it to speed up lookup
			extraHopByHopHeaders = vv
		case "Keep-Alive":
		case "Proxy-Authenticate":
		case "Proxy-Authorization":
		case "TE":
		case "Trailers":
		case "Transfer-Encoding":
		case "Upgrade":
		default:
			if !contains(k, extraHopByHopHeaders) {
				for _, v := range vv {
					dst.Add(k, v)
				}
			}
		}
	}
}

func contains(k string, s []string) bool {
	for _, h := range s {
		if k == h {
			return true
		}
	}
	return false
}

func respondBadGateway(w http.ResponseWriter, err error) {
	log.Debugf("Responding BadGateway: %v", err)
	w.WriteHeader(http.StatusBadGateway)
	if _, writeError := w.Write([]byte(hidden.Clean(err.Error()))); writeError != nil {
		log.Debugf("Error writing error to ResponseWriter: %v", writeError)
	}
}

func defaultOnRequest(req *http.Request) *http.Request {
	return req
}

func defaultOnResponse(resp *http.Response) *http.Response {
	return resp
}

func defaultOnError(req *http.Request, err error) *http.Response {
	return nil
}