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
	"github.com/getlantern/netx"
	"github.com/getlantern/preconn"
	"github.com/getlantern/proxy/filters"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/siddontang/go/log"
)

func (opts *Opts) applyHTTPDefaults() {
	// Apply defaults
	if opts.Filter == nil {
		opts.Filter = filters.FilterFunc(defaultFilter)
	}
	if opts.OnError == nil {
		opts.OnError = defaultOnError
	}
}

// Handle implements the interface Proxy
func (proxy *proxy) Handle(ctx context.Context, downstreamIn io.Reader, downstream net.Conn) (err error) {
	defer func() {
		p := recover()
		if p != nil {
			proxy.safeClose(downstream)
			err = errors.New("Recovered from panic handling connection: %v", p)
		}
	}()

	err = proxy.handle(ctx, downstreamIn, downstream, nil)
	return
}

func (proxy *proxy) safeClose(conn net.Conn) {
	defer func() {
		p := recover()
		if p != nil {
			proxy.log.Errorf("Panic on closing connection: %v", p)
		}
	}()

	conn.Close()
}

func (proxy *proxy) logInitialReadError(downstream net.Conn, err error) error {
	rem := downstream.RemoteAddr()
	r := ""
	if rem != nil {
		r = rem.String()
	}
	txt := err.Error()
	// Ignore our generated error that should have already been reported.
	if strings.HasPrefix(txt, "Client Hello has no cipher suites") {
		proxy.log.Debugf("No cipher suites in common -- old Lantern client")
		return err
	}
	// These errors should all typically be internal go errors, typically with TLS. Break them up
	// for stackdriver grouping.
	if strings.Contains(txt, "oversized") {
		return proxy.log.Errorf("Oversized record on initial read: %v from %v", err, r)
	}
	if strings.Contains(txt, "first record does not") {
		return proxy.log.Errorf("Not a TLS client connection: %v from %v", err, r)
	}
	return proxy.log.Errorf("Initial ReadRequest: %v from %v", err, r)
}

func (proxy *proxy) handle(ctx context.Context, downstreamIn io.Reader, downstream net.Conn, upstream net.Conn) error {
	defer func() {
		if closeErr := downstream.Close(); closeErr != nil {
			proxy.log.Tracef("Error closing downstream connection: %s", closeErr)
		}
	}()

	downstreamBuffered := bufio.NewReader(downstreamIn)
	fctx := filters.WrapContext(withAwareConn(ctx), downstream)

	// Read initial request
	req, err := http.ReadRequest(downstreamBuffered)
	proxy.log.Debugf("In new proxy code for %v", downstream.RemoteAddr().String())
	if req != nil {
		remoteAddr := downstream.RemoteAddr()
		if remoteAddr != nil {
			req.RemoteAddr = remoteAddr.String()
		}
		if origURLScheme(ctx) == "" {
			fctx = fctx.
				WithValue(ctxKeyOrigURLScheme, req.URL.Scheme).
				WithValue(ctxKeyOrigURLHost, req.URL.Host).
				WithValue(ctxKeyOrigHost, req.Host)
		}

		finishSpan := proxy.extractSpan(fctx, req)
		defer finishSpan()
	}

	if err != nil {
		if isUnexpected(err) {
			errResp := proxy.OnError(fctx, req, true, err)
			if errResp != nil {
				proxy.writeResponse(downstream, req, errResp)
			}

			return proxy.logInitialReadError(downstream, err)
		}
		return nil
	}

	var next filters.Next
	if req.Method == http.MethodConnect {
		next = proxy.nextCONNECT(downstream)
	} else {
		var tr idleClosingTransport
		if upstream != nil {
			setUpstreamForAwareConn(fctx, upstream)
			tr = &addressLoggingTransport{
				Transport: &http.Transport{
					DialContext: func(ctx context.Context, net, addr string) (net.Conn, error) {
						// always use the supplied upstream connection, but don't allow it to
						// be closed by the transport
						return &noCloseConn{upstream}, nil
					},
					// this transport is only used once, don't keep any idle connections,
					// however still allow the transport to close the connection after using
					// it
					MaxIdleConnsPerHost: -1,
				},
				upstream: upstream,
			}
		} else {
			tr = &http.Transport{
				DialContext: func(ctx context.Context, net, addr string) (net.Conn, error) {
					conn, err := proxy.Dial(ctx, false, net, addr)
					if err == nil {
						// On first dialing conn, handle RequestAware
						setUpstreamForAwareConn(ctx, conn)
						handleRequestAware(ctx)
					}
					return conn, err
				},
				IdleConnTimeout: proxy.IdleTimeout,
				// since we have one transport per downstream connection, we don't need
				// more than this
				MaxIdleConnsPerHost: 1,
			}
		}

		defer tr.CloseIdleConnections()
		next = func(ctx filters.Context, modifiedReq *http.Request) (*http.Response, filters.Context, error) {
			modifiedReq = modifiedReq.WithContext(ctx)
			setRequestForAwareConn(ctx, modifiedReq)
			handleRequestAware(ctx)
			resp, err := tr.RoundTrip(prepareRequest(modifiedReq))
			handleResponseAware(ctx, modifiedReq, resp, err)
			if err != nil {
				err = errors.New("Unable to round-trip http request to upstream: %v", err)
			}
			return resp, ctx, err
		}
	}

	return proxy.processRequests(fctx, req.RemoteAddr, req, downstream, downstreamBuffered, next)
}

func (proxy *proxy) extractSpan(ctx context.Context, req *http.Request) func() {
	if strings.HasPrefix(req.RemoteAddr, "65.214.166.18") {
		proxy.log.Debugf("Attempting to extract span from %#v", req)
	} else {
		return func() {}
	}

	// If we're running on the server side, the HTTP request will have the span data while if we're
	// running on the client side the context will have the span data.
	if parentSpan := opentracing.SpanFromContext(ctx); parentSpan != nil {
		span := opentracing.GlobalTracer().StartSpan("proxyHandle", opentracing.ChildOf(parentSpan.Context()))
		proxy.log.Debug("Extracted span from incoming proxy context")

		err := opentracing.GlobalTracer().Inject(span.Context(), opentracing.HTTPHeaders, opentracing.HTTPHeadersCarrier(req.Header))
		if err != nil {
			log.Errorf("Could not inject span in client request: %v", err)
			return func() {}
		}
		return span.Finish
	}

	// We're on the server, and this is an incomming HTTP request that should contain span context data
	// it its headers.
	wireContext, err := opentracing.GlobalTracer().Extract(
		opentracing.HTTPHeaders,
		opentracing.HTTPHeadersCarrier(req.Header))
	if err != nil {
		// This likely indicates a client that does not support tracing, which in practice will be most
		// clients certainly initially.
		log.Errorf("Could not find span on server in client request: %v", err)
		return func() {}
	}
	proxy.log.Debug("Extracted span from incoming HTTP request!!")
	span := opentracing.StartSpan("proxyHandle", ext.RPCServerOption(wireContext))
	return span.Finish
}

func (proxy *proxy) processRequests(ctx filters.Context, remoteAddr string, req *http.Request, downstream net.Conn, downstreamBuffered *bufio.Reader, next filters.Next) error {
	var readErr error
	var resp *http.Response
	var err error

	for {
		if req.URL.Scheme == "" {
			req.URL.Scheme = origURLScheme(ctx)
		}
		if req.URL.Host == "" {
			req.URL.Host = origURLHost(ctx)
		}
		if req.Host == "" {
			req.Host = origHost(ctx)
		}
		resp, ctx, err = proxy.Filter.Apply(ctx, req, next)
		if err != nil && resp == nil {
			resp = proxy.OnError(ctx, req, false, err)
			if resp != nil {
				proxy.log.Debugf("Closing client connection on error: %v", err)
				// On error, we will always close the connection
				resp.Close = true
			}
		}

		if resp != nil {
			writeErr := proxy.writeResponse(downstream, req, resp)
			if writeErr != nil {
				if isUnexpected(writeErr) {
					return proxy.log.Errorf("Unable to write response to downstream: %v", writeErr)
				}
				// Error is not unexpected, but we're done
				return err
			}
		}

		if err != nil {
			// We encountered an error on round-tripping, stop now
			return err
		}

		upstream := upstreamConn(ctx)
		upstreamAddr := upstreamAddr(ctx)
		isConnect := upstream != nil || upstreamAddr != ""

		buffered := downstreamBuffered.Buffered()
		if buffered > 0 {
			b, _ := downstreamBuffered.Peek(buffered)
			downstream = preconn.Wrap(downstream, b)
		}

		if isConnect {
			return proxy.proceedWithConnect(ctx, req, upstreamAddr, upstream, downstream)
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
				return proxy.log.Errorf("Unable to read next request from downstream: %v", readErr)
			}
			return err
		}

		// Preserve remote address from original request
		ctx = ctx.IncrementRequestNumber()
		req.RemoteAddr = remoteAddr
		req = req.WithContext(ctx)
	}
}

func handleRequestAware(ctx context.Context) {
	upstream := upstreamForAwareConn(ctx)
	if upstream == nil {
		return
	}

	netx.WalkWrapped(upstream, func(wrapped net.Conn) bool {
		switch t := wrapped.(type) {
		case RequestAware:
			req := requestForAwareConn(ctx)
			t.OnRequest(req)
		}
		return true
	})
}

func handleResponseAware(ctx context.Context, req *http.Request, resp *http.Response, err error) {
	upstream := upstreamForAwareConn(ctx)
	if upstream == nil {
		return
	}

	netx.WalkWrapped(upstream, func(wrapped net.Conn) bool {
		switch t := wrapped.(type) {
		case ResponseAware:
			t.OnResponse(req, resp, err)
		}
		return true
	})
}

func (proxy *proxy) writeResponse(downstream io.Writer, req *http.Request, resp *http.Response) error {
	if resp.Request == nil {
		resp.Request = req
	}
	out := downstream
	if resp.ProtoMajor == 0 {
		resp.ProtoMajor = 1
		resp.ProtoMinor = 1
	}
	belowHTTP11 := !resp.ProtoAtLeast(1, 1)
	if belowHTTP11 && resp.StatusCode < 200 {
		// HTTP 1.0 doesn't define status codes below 200, discard response
		// see http://coad.measurement-factory.com/cgi-bin/coad/SpecCgi?spec_id=rfc2616#excerpt/rfc2616/859a092cb26bde76c25284196171c94d
		out = ioutil.Discard
	} else {
		resp = prepareResponse(resp, belowHTTP11)
		proxy.addIdleKeepAlive(resp.Header)
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
	req.Proto = "HTTP/1.1"
	req.ProtoMajor = 1
	req.ProtoMinor = 1
	// Overwrite close flag: keep persistent connection for the backend servers
	req.Close = false

	// Request Header
	newHeader := make(http.Header)
	copyHeadersForForwarding(newHeader, req.Header)
	// Ensure we have a HOST header (important for Go 1.6+ because http.Server
	// strips the HOST header from the inbound request)
	newHeader.Set("Host", req.Host)
	req.Header = newHeader

	// Request URL
	req.URL = cloneURL(req.URL)
	// If req.URL.Scheme was blank, it's http. Otherwise, it's https and we leave
	// it alone.
	if req.URL.Scheme == "" {
		req.URL.Scheme = "http"
	}
	// We need to make sure the host is defined in the URL (not the actual URI)
	req.URL.Host = req.Host

	userAgent := req.UserAgent()
	if userAgent == "" {
		req.Header.Del("User-Agent")
	} else {
		req.Header.Set("User-Agent", userAgent)
	}

	return req
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

func isUnexpected(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		return false
	}
	// This is okay per the HTTP spec.
	// See https://www.w3.org/Protocols/rfc2616/rfc2616-sec8.html#sec8.1.4
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return false
	}

	text := err.Error()
	return !strings.HasSuffix(text, "EOF") &&
		!strings.Contains(text, "i/o timeout") &&
		!strings.Contains(text, "Use of idled network connection") &&
		!strings.Contains(text, "use of closed network connection") &&
		// usually caused by client disconnecting
		!strings.Contains(text, "broken pipe") &&
		// usually caused by client disconnecting
		!strings.Contains(text, "connection reset by peer")
}

func defaultFilter(ctx filters.Context, req *http.Request, next filters.Next) (*http.Response, filters.Context, error) {
	return next(ctx, req)
}

func defaultOnError(ctx filters.Context, req *http.Request, read bool, err error) *http.Response {
	return nil
}

type idleClosingTransport interface {
	RoundTrip(req *http.Request) (*http.Response, error)
	CloseIdleConnections()
}

type addressLoggingTransport struct {
	*http.Transport
	upstream net.Conn
}

func (alt *addressLoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := alt.Transport.RoundTrip(req)
	if err != nil {
		err = errors.New("Error round-tripping to %v: %v", alt.upstream.RemoteAddr(), err)
	}
	return resp, err
}
