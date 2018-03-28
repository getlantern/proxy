package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	ht "net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getlantern/fdcount"
	"github.com/getlantern/keyman"
	"github.com/getlantern/mitm"
	"github.com/getlantern/mockconn"
	"github.com/getlantern/proxy/filters"
	"github.com/getlantern/tlsdefaults"
	"github.com/stretchr/testify/assert"
)

const (
	testReqHeader       = "X-Test-Req"
	testReqAwareHeader  = "X-Test-Req-Aware"
	testRespHeader      = "X-Test-Resp"
	testRespAwareHeader = "X-Test-Resp-Aware"
	testHeaderValue     = "true"
)

func init() {
	// Clean up certs
	files, _ := ioutil.ReadDir(".")
	for _, file := range files {
		filename := file.Name()
		if strings.Contains(filename, "serverpk.pem") ||
			strings.Contains(filename, "servercert.pem") ||
			strings.Contains(filename, "proxypk.pem") ||
			strings.Contains(filename, "proxycert.pem") {
			os.Remove(file.Name())
		}
	}
}

func TestDialFailureHTTP(t *testing.T) {
	errorText := "I don't want to dial"
	d := mockconn.FailingDialer(errors.New(errorText))
	onError := func(ctx filters.Context, req *http.Request, read bool, err error) *http.Response {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       ioutil.NopCloser(bytes.NewReader([]byte(err.Error()))),
		}
	}
	p := newProxy(&Opts{
		OnError: onError,
		Dial: func(context context.Context, isConnect bool, net, addr string) (net.Conn, error) {
			return d.Dial(net, addr)
		},
	})
	req, _ := http.NewRequest("GET", "http://thehost:123", nil)
	resp, roundTripErr, handleErr := roundTrip(p, req)
	if !assert.NoError(t, roundTripErr) {
		return
	}
	if !assert.Error(t, handleErr, "Should have gotten error") {
		return
	}
	assert.Equal(t, "thehost:123", d.LastDialed(), "Should have used specified port of 123")
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	body, err := ioutil.ReadAll(resp.Body)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, errorText, string(body))
}

func TestDialFailureCONNECTWaitForUpstream(t *testing.T) {
	errorText := "I don't want to dial"
	d := mockconn.FailingDialer(errors.New(errorText))
	p := newProxy(&Opts{
		OKWaitsForUpstream: true,
		Dial: func(ctx context.Context, isConnect bool, net, addr string) (net.Conn, error) {
			return d.Dial(net, addr)
		},
	})
	req, _ := http.NewRequest("CONNECT", "http://thehost:123", nil)
	resp, roundTripErr, handleErr := roundTrip(p, req)
	if !assert.NoError(t, roundTripErr) {
		return
	}
	if !assert.Error(t, handleErr, "Should have gotten error") {
		return
	}
	assert.Equal(t, "thehost:123", d.LastDialed(), "Should have used specified port of 123")
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	body, err := ioutil.ReadAll(resp.Body)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, errorText, string(body))
}

func TestDialFailureCONNECTDontWaitForUpstream(t *testing.T) {
	errorText := "I don't want to dial"
	d := mockconn.FailingDialer(errors.New(errorText))
	p := newProxy(&Opts{
		OKWaitsForUpstream: false,
		Dial: func(ctx context.Context, isConnect bool, net, addr string) (net.Conn, error) {
			return d.Dial(net, addr)
		},
	})
	req, _ := http.NewRequest("CONNECT", "http://thehost:123", nil)
	resp, roundTripErr, handleErr := roundTrip(p, req)
	if !assert.NoError(t, roundTripErr) {
		return
	}
	if !assert.Error(t, handleErr, "Should have gotten error") {
		return
	}
	assert.Equal(t, "thehost:123", d.LastDialed(), "Should have used specified port of 123")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestPanicRecover(t *testing.T) {
	p := newProxy(&Opts{
		Filter: filters.FilterFunc(func(ctx filters.Context, req *http.Request, next filters.Next) (*http.Response, filters.Context, error) {
			panic(errors.New("I'm panicking!"))
		}),
	})
	req, _ := http.NewRequest("GET", "http://thehost:123", nil)
	_, _, handleErr := roundTrip(p, req)
	assert.True(t, strings.Contains(handleErr.Error(), "I'm panicking"), "Panic should have propagated as error")
}

func TestConnectWaitForUpstream(t *testing.T) {
	doTestConnect(t, true)
}

func TestConnectDontWaitForUpstream(t *testing.T) {
	doTestConnect(t, false)
}

func doTestConnect(t *testing.T, okWaitsForUpstream bool) {
	successText := "I'm good!"
	originalOrigin := "origin:80"
	expectedOrigin := "origin:8080"
	receivedOrigin := ""
	var mx sync.Mutex
	d := mockconn.SucceedingDialer([]byte(successText))
	p := newProxy(&Opts{
		OKWaitsForUpstream: okWaitsForUpstream,
		Dial: func(ctx context.Context, isConnect bool, net, addr string) (net.Conn, error) {
			mx.Lock()
			receivedOrigin = addr
			mx.Unlock()
			return d.Dial(net, addr)
		},
		Filter: filters.FilterFunc(func(ctx filters.Context, req *http.Request, next filters.Next) (*http.Response, filters.Context, error) {
			req.Host = req.Host + "80"
			req.URL.Host = req.Host
			return next(ctx, req)
		}),
	})
	received := &bytes.Buffer{}
	conn := mockconn.New(received, strings.NewReader(""))
	err := p.Connect(context.Background(), conn, conn, originalOrigin)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, successText, received.String())
	mx.Lock()
	ro := receivedOrigin
	mx.Unlock()
	assert.Equal(t, expectedOrigin, ro)
}

func TestShortCircuitHTTP(t *testing.T) {
	p := newProxy(&Opts{
		Filter: filters.FilterFunc(func(ctx filters.Context, req *http.Request, next filters.Next) (*http.Response, filters.Context, error) {
			return filters.ShortCircuit(ctx, req, &http.Response{
				Header:     make(http.Header),
				StatusCode: http.StatusForbidden,
				Close:      true,
			})
		}),
	})
	req, _ := http.NewRequest(http.MethodGet, "http://thehost:123", nil)
	resp, roundTripErr, handleErr := roundTrip(p, req)
	if !assert.NoError(t, roundTripErr) {
		return
	}
	if !assert.NoError(t, handleErr) {
		return
	}
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestShortCircuitCONNECT(t *testing.T) {
	p := newProxy(&Opts{
		Filter: filters.FilterFunc(func(ctx filters.Context, req *http.Request, next filters.Next) (*http.Response, filters.Context, error) {
			return filters.ShortCircuit(ctx, req, &http.Response{
				Header:     make(http.Header),
				StatusCode: http.StatusForbidden,
			})
		}),
	})
	req, _ := http.NewRequest(http.MethodConnect, "http://thehost:123", nil)
	resp, roundTripErr, handleErr := roundTrip(p, req)
	if !assert.NoError(t, roundTripErr) {
		return
	}
	if !assert.NoError(t, handleErr) {
		return
	}
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestCONNECTWaitForUpstreamNoMITM(t *testing.T) {
	doTest(t, "CONNECT", false, true, false)
}

func TestCONNECTWaitForUpstreamMITM(t *testing.T) {
	doTest(t, "CONNECT", false, true, true)
}

func TestCONNECTDontWaitForUpstreamNoMITM(t *testing.T) {
	doTest(t, "CONNECT", false, false, false)
}

func TestCONNECTDontWaitForUpstreamMITM(t *testing.T) {
	doTest(t, "CONNECT", false, false, true)
}

func TestHTTPForwardFirst(t *testing.T) {
	doTest(t, "GET", false, false, false)
}

func TestHTTPDontForwardFirst(t *testing.T) {
	doTest(t, "GET", true, false, false)
}

type failingConn struct {
	net.Conn
}

func (c failingConn) Write(b []byte) (n int, err error) {
	return 0, fmt.Errorf("fail intentionally: %s->%s",
		c.Conn.LocalAddr().String(),
		c.Conn.RemoteAddr().String())
}

type failingHijacker struct {
	http.ResponseWriter
}

func (h failingHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	n, rw, e := h.ResponseWriter.(http.Hijacker).Hijack()
	return failingConn{n}, rw, e
}

func TestHTTPDownstreamError(t *testing.T) {
	origin := ht.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Write([]byte("hello"))
	}))
	defer origin.Close()

	p := newProxy(&Opts{
		IdleTimeout: 30 * time.Second,
		Dial: func(ctx context.Context, isConnect bool, network, addr string) (net.Conn, error) {
			return net.Dial("tcp", origin.Listener.Addr().String())
		},
	})

	l, err := net.Listen("tcp", ":0")
	if !assert.NoError(t, err) {
		return
	}
	defer l.Close()

	go func() {
		for {
			conn, acceptErr := l.Accept()
			if acceptErr != nil {
				return
			}
			conn.Close()
			go p.Handle(context.Background(), conn, conn)
		}
	}()

	_, counter, err := fdcount.Matching("TCP")
	if !assert.NoError(t, err) {
		return
	}

	n := 100
	var wg sync.WaitGroup
	wg.Add(n)
	chConnsToClose := make(chan net.Conn, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			conn, err := net.Dial("tcp", l.Addr().String())
			chConnsToClose <- conn
			if !assert.NoError(t, err) {
				return
			}

			req, _ := http.NewRequest("GET", origin.URL, nil)
			err = req.Write(conn)
			if !assert.NoError(t, err) {
				return
			}
			br := bufio.NewReader(conn)
			_, rtErr := http.ReadResponse(br, req)
			if !assert.Error(t, rtErr) {
				return
			}
		}()
	}

	wg.Wait()
	assert.NoError(t, counter.AssertDelta(n),
		"All connections should have been closed but the CLOSE_WAIT one from client to proxy")
	for i := 0; i < n; i++ {
		if conn := <-chConnsToClose; conn != nil {
			conn.Close()
		}
	}
	assert.NoError(t, counter.AssertDelta(0), "All connections should have been closed")
}

func doTest(t *testing.T, requestMethod string, discardFirstRequest bool, okWaitsForUpstream bool, shouldMITM bool) {
	l, err := tlsdefaults.Listen("localhost:0", "serverpk.pem", "servercert.pem")
	if !assert.NoError(t, err) {
		return
	}
	defer l.Close()

	serverCert, err := keyman.LoadCertificateFromFile("servercert.pem")
	if !assert.NoError(t, err) {
		return
	}

	pl, err := net.Listen("tcp", "localhost:0")
	if !assert.NoError(t, err) {
		return
	}
	defer pl.Close()

	var mx sync.RWMutex
	seenAddresses := make(map[string]bool)
	go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mx.Lock()
		seenAddresses[req.RemoteAddr] = true
		mx.Unlock()
		// Echo all testRespHeader headers
		for key, value := range req.Header {
			if strings.HasPrefix(key, "X-Test") {
				w.Header()[key] = value
			}
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(req.Host))
	}))

	requestNumber := 0
	dial := func(ctx context.Context, isConnect bool, network, addr string) (conn net.Conn, err error) {
		requestNumber = filters.AdaptContext(ctx).RequestNumber()
		if requestMethod == http.MethodGet {
			conn, err = tls.Dial("tcp", l.Addr().String(), &tls.Config{
				ServerName: "localhost",
				RootCAs:    serverCert.PoolContainingCert(),
			})
		} else {
			conn, err = net.Dial("tcp", l.Addr().String())
		}
		if err == nil {
			conn = &testConn{conn}
		}
		return conn, err
	}

	first := true
	filter := filters.FilterFunc(func(ctx filters.Context, req *http.Request, next filters.Next) (*http.Response, filters.Context, error) {
		if req.RemoteAddr == "" {
			t.Fatal("Request missing RemoteAddr!")
		}
		if discardFirstRequest && first {
			first = false
			return filters.Discard(ctx, req)
		}
		req.Header.Set(testReqHeader, testHeaderValue)
		resp, nextCtx, nextErr := next(ctx, req)
		if resp != nil {
			resp.Header.Set(testRespHeader, testHeaderValue)
		}

		isConnect := req.Method == http.MethodConnect
		if !isConnect && shouldMITM {
			assert.True(t, ctx.IsMITMing())
		} else {
			assert.False(t, ctx.IsMITMing())
		}

		return resp, nextCtx, nextErr
	})

	isConnect := requestMethod == "CONNECT"
	var mitmOpts *mitm.Opts
	if shouldMITM {
		mitmOpts = &mitm.Opts{
			PKFile:   "proxypk.pem",
			CertFile: "proxycert.pem",
			ClientTLSConfig: &tls.Config{
				RootCAs: serverCert.PoolContainingCert(),
			},
			Domains: []string{"localhost"},
		}
	}

	p := newProxy(&Opts{
		IdleTimeout:        30 * time.Second,
		OKWaitsForUpstream: okWaitsForUpstream,
		Filter:             filter,
		Dial:               dial,
		MITMOpts:           mitmOpts,
	})

	go p.Serve(pl)

	_, counter, err := fdcount.Matching("TCP")
	if !assert.NoError(t, err) {
		return
	}

	// We use a single connection for all requests, even though they're going to
	// different hosts. This simulates user agents like Firefox and Edge that
	// send requests for multiple hosts across a single proxy connection.
	conn, err := net.Dial("tcp", pl.Addr().String())
	if !assert.NoError(t, err) {
		return
	}
	defer conn.Close()
	br := bufio.NewReader(conn)

	roundTrip := func(req *http.Request, readResponse bool) (*http.Response, string, error) {
		rtErr := req.Write(conn)
		if rtErr != nil {
			return nil, "", rtErr
		}
		if readResponse {
			resp, rtErr := http.ReadResponse(br, req)
			if rtErr != nil && rtErr != io.EOF {
				return nil, "", rtErr
			}
			body, rtErr := ioutil.ReadAll(resp.Body)
			if rtErr != nil && rtErr != io.EOF {
				return resp, "", rtErr
			}
			return resp, string(body), nil
		}
		return nil, "", nil
	}

	req, _ := http.NewRequest(requestMethod, "http://subdomain.thehost:756", nil)
	req.RemoteAddr = "remoteaddr:134"

	includeFirst := isConnect || !discardFirstRequest
	expectedRequestNumber := 0
	if includeFirst {
		expectedRequestNumber = 1
	}
	resp, body, err := roundTrip(req, includeFirst)
	if !assert.NoError(t, err) {
		return
	}
	if !isConnect && !discardFirstRequest {
		assert.Equal(t, "subdomain.thehost:756", body, "Should have left port alone")
		assert.Equal(t, testHeaderValue, resp.Header.Get(testReqAwareHeader))
		assert.Equal(t, testHeaderValue, resp.Header.Get(testRespAwareHeader))
	}
	if !discardFirstRequest {
		assert.Regexp(t, "timeout=\\d+", resp.Header.Get("Keep-Alive"), "All HTTP responses' headers should contain a Keep-Alive timeout")
		assert.Equal(t, testHeaderValue, resp.Header.Get(testRespHeader))
	}
	assert.Equal(t, expectedRequestNumber, requestNumber)
	if !includeFirst {
		expectedRequestNumber++
	}

	if isConnect {
		// Upgrade to TLS
		var tlsConfig *tls.Config
		if shouldMITM {
			tlsConfig = &tls.Config{
				ServerName:         "localhost",
				InsecureSkipVerify: true,
			}
		} else {
			tlsConfig = &tls.Config{
				ServerName: "localhost",
				RootCAs:    serverCert.PoolContainingCert(),
			}
		}
		conn = tls.Client(conn, tlsConfig)
		br = bufio.NewReader(conn)
	}

	nestedReqBody := []byte("My Request")
	nestedReq, _ := http.NewRequest("POST", "http://subdomain2.thehost/a", ioutil.NopCloser(bytes.NewBuffer(nestedReqBody)))
	nestedReq.Proto = "HTTP/1.1"
	resp, body, err = roundTrip(nestedReq, true)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, "subdomain2.thehost", body, "Should have gotten right host")
	if !isConnect {
		assert.Contains(t, resp.Header.Get("Keep-Alive"), "timeout", "All HTTP responses' headers should contain a Keep-Alive timeout")
		assert.Equal(t, testHeaderValue, resp.Header.Get(testRespHeader))
		assert.Equal(t, testHeaderValue, resp.Header.Get(testReqAwareHeader))
		assert.Equal(t, testHeaderValue, resp.Header.Get(testRespAwareHeader))
		expectedRequestNumber++
	}
	assert.Equal(t, expectedRequestNumber, requestNumber)

	nestedReq2Body := []byte("My Request")
	nestedReq2, _ := http.NewRequest("POST", "http://subdomain3.thehost/b", ioutil.NopCloser(bytes.NewBuffer(nestedReq2Body)))
	nestedReq2.Proto = "HTTP/1.0"
	resp, body, err = roundTrip(nestedReq2, true)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, "subdomain3.thehost", body, "Should have gotten right host")
	if !isConnect {
		assert.Contains(t, resp.Header.Get("Keep-Alive"), "timeout", "All HTTP responses' headers should contain a Keep-Alive timeout")
		assert.Equal(t, testHeaderValue, resp.Header.Get(testRespHeader))
		assert.Equal(t, testHeaderValue, resp.Header.Get(testReqAwareHeader))
		assert.Equal(t, testHeaderValue, resp.Header.Get(testRespAwareHeader))
		expectedRequestNumber++
	}
	assert.Equal(t, expectedRequestNumber, requestNumber)

	expectedConnections := 3
	if discardFirstRequest {
		expectedConnections--
	}
	if isConnect {
		expectedConnections = 1
	}
	mx.RLock()
	defer mx.RUnlock()
	assert.Equal(t, expectedConnections, len(seenAddresses))

	conn.Close()
	assert.NoError(t, counter.AssertDelta(0), "All connections should have been closed")
}

func newProxy(opts *Opts) Proxy {
	p, _ := New(opts)
	return p
}

func roundTrip(p Proxy, req *http.Request) (resp *http.Response, roundTripErr error, handleErr error) {
	toSend := &bytes.Buffer{}
	roundTripErr = req.Write(toSend)
	if roundTripErr != nil {
		return
	}
	received := &bytes.Buffer{}
	conn := mockconn.New(received, toSend)
	handleErr = p.Handle(context.Background(), conn, conn)
	resp, roundTripErr = http.ReadResponse(bufio.NewReader(bytes.NewReader(received.Bytes())), req)
	return
}

type testConn struct {
	net.Conn
}

func (conn *testConn) OnRequest(req *http.Request) {
	req.Header.Set(testReqAwareHeader, testHeaderValue)
}

func (conn *testConn) OnResponse(req *http.Request, resp *http.Response, err error) {
	resp.Header.Set(testRespAwareHeader, testHeaderValue)
}

func (conn *testConn) Wrapped() net.Conn {
	return conn.Conn
}
