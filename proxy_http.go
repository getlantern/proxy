package proxy

import (
	"bufio"
	"context"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/getlantern/errors"
	"github.com/getlantern/proxy/filters"
)

type contextKey string

const (
	ctxKeyDownstream         = contextKey("downstream")
	ctxKeyDownstreamBuffered = contextKey("downstreamBuffered")
	ctxKeyRequestNumber      = contextKey("requestNumber")
)

// DownstreamConn retrieves the downstream connection from the given Context.
func DownstreamConn(ctx context.Context) net.Conn {
	return ctx.Value(ctxKeyDownstream).(net.Conn)
}

// DownstreamBuffered retrieves the downstream buffered reader from the given
// Context.
func DownstreamBuffered(ctx context.Context) *bufio.Reader {
	return ctx.Value(ctxKeyDownstreamBuffered).(*bufio.Reader)
}

// RequestNumber indicates how many requests have been received on the current
// connection. The RequestNumber for the first request is 1, for the second is 2
// and so forth.
func RequestNumber(ctx context.Context) int {
	return ctx.Value(ctxKeyRequestNumber).(int)
}

func (opts *Opts) applyHTTPDefaults() {
	// Apply defaults
	if opts.Filter == nil {
		opts.Filter = filters.FilterFunc(defaultFilter)
	}
	if opts.OnError == nil {
		opts.OnError = defaultOnError
	}
	if opts.IdleTimeout > 0 {
		opts.Filter = filters.Join(filters.FilterFunc(func(ctx context.Context, req *http.Request, next filters.Next) (*http.Response, error) {
			resp, err := next(ctx, req)
			if resp != nil {
				opts.addIdleKeepAlive(resp.Header)
			}
			return resp, err
		}), opts.Filter)
	}
}

// Handle implements the interface Proxy
func (proxy *proxy) Handle(ctx context.Context, downstream net.Conn) error {
	defer func() {
		if closeErr := downstream.Close(); closeErr != nil {
			log.Tracef("Error closing downstream connection: %s", closeErr)
		}
	}()

	downstreamBuffered := bufio.NewReader(downstream)
	ctx = context.WithValue(ctx, ctxKeyDownstream, downstream)
	ctx = context.WithValue(ctx, ctxKeyDownstreamBuffered, downstreamBuffered)
	ctx = context.WithValue(ctx, ctxKeyRequestNumber, 1)

	// Read initial request
	req, err := http.ReadRequest(downstreamBuffered)
	if req != nil {
		remoteAddr := downstream.RemoteAddr()
		if remoteAddr != nil {
			req.RemoteAddr = downstream.RemoteAddr().String()
		}
	}
	if err != nil {
		errResp := proxy.OnError(ctx, req, true, err)
		if errResp != nil {
			proxy.writeResponse(downstream, req, errResp)
		}
		return err
	}

	if req.Method == http.MethodConnect {
		return proxy.handleCONNECT(ctx, downstream, req)
	}
	return proxy.handleHTTP(ctx, downstream, downstreamBuffered, req)
}

func (proxy *proxy) handleHTTP(ctx context.Context, downstream net.Conn, downstreamBuffered *bufio.Reader, req *http.Request) error {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, net, addr string) (net.Conn, error) {
			return proxy.Dial(false, net, addr)
		},
		IdleConnTimeout: proxy.IdleTimeout,
		// since we have one transport per downstream connection, we don't need
		// more than this
		MaxIdleConnsPerHost: 1,
	}

	defer tr.CloseIdleConnections()
	return proxy.processRequests(ctx, req.RemoteAddr, req, downstream, downstreamBuffered, tr)
}

func (proxy *proxy) processRequests(ctx context.Context, remoteAddr string, req *http.Request, downstream net.Conn, downstreamBuffered *bufio.Reader, tr *http.Transport) error {
	var readErr error

	for {
		resp, err := proxy.Filter.Apply(ctx, req, func(ctx context.Context, modifiedReq *http.Request) (*http.Response, error) {
			return tr.RoundTrip(prepareRequest(modifiedReq))
		})

		if err != nil && resp == nil {
			resp = proxy.OnError(ctx, req, false, err)
		}

		if resp != nil {
			writeErr := proxy.writeResponse(downstream, req, resp)
			if writeErr != nil {
				if isUnexpected(writeErr) {
					return errors.New("Unable to write response to downstream: %v", writeErr)
				}
				// Error is not unexpected, but we're done
				return err
			}
		}

		if req.Close {
			// Client signaled that they would close the connection after this
			// request, finish
			return err
		}

		if err == nil && resp != nil && resp.Close {
			// Last response, finish
			return err
		}

		// read the next request
		req, readErr = http.ReadRequest(downstreamBuffered)
		if readErr != nil {
			if isUnexpected(readErr) {
				errResp := proxy.OnError(ctx, req, true, readErr)
				if errResp != nil {
					proxy.writeResponse(downstream, req, errResp)
				}
				return errors.New("Unable to read next request from downstream: %v", readErr)
			}
			return err
		}

		// Preserve remote address from original request
		req.RemoteAddr = remoteAddr
		ctx = context.WithValue(ctx, ctxKeyRequestNumber, RequestNumber(ctx)+1)
	}
}

func (proxy *proxy) writeResponse(downstream io.Writer, req *http.Request, resp *http.Response) error {
	if resp.Request == nil {
		resp.Request = req
	}
	out := downstream
	if resp.Request == nil {
		resp.ProtoMajor = 1
		resp.ProtoMinor = 1
	}
	belowHTTP11 := resp.Request != nil && !resp.Request.ProtoAtLeast(1, 1)
	if belowHTTP11 && resp.StatusCode < 200 {
		// HTTP 1.0 doesn't define status codes below 200, discard response
		// see http://coad.measurement-factory.com/cgi-bin/coad/SpecCgi?spec_id=rfc2616#excerpt/rfc2616/859a092cb26bde76c25284196171c94d
		out = ioutil.Discard
	} else {
		resp = prepareResponse(resp, belowHTTP11)
	}
	err := resp.Write(out)
	// resp.Write closes the body only if it's successfully sent. Close
	// manually when error happens.
	if err != nil && resp.Body != nil {
		resp.Body.Close()
	}
	return err
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
		// case "Keep-Alive":
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

func isUnexpected(err error) bool {
	text := err.Error()
	return !strings.HasSuffix(text, "EOF") && !strings.Contains(text, "use of closed network connection") && !strings.Contains(text, "Use of idled network connection") && !strings.Contains(text, "broken pipe")
}

func defaultFilter(ctx context.Context, req *http.Request, next filters.Next) (*http.Response, error) {
	return next(ctx, req)
}

func defaultOnError(ctx context.Context, req *http.Request, read bool, err error) *http.Response {
	return nil
}
