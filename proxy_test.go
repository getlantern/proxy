package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	ht "net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/getlantern/fdcount"
	"github.com/getlantern/mockconn"
	"github.com/getlantern/proxy/filters"
	"github.com/stretchr/testify/assert"
)

const (
	okHeader = "X-Test-OK"
)

func TestDialFailureHTTP(t *testing.T) {
	errorText := "I don't want to dial"
	d := mockconn.FailingDialer(errors.New(errorText))
	onError := func(ctx context.Context, req *http.Request, err error) *http.Response {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       ioutil.NopCloser(bytes.NewReader([]byte(err.Error()))),
		}
	}
	p := New(&Opts{
		OnError: onError,
		Dial: func(isConnect bool, net, addr string) (net.Conn, error) {
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
	p := New(&Opts{
		OKWaitsForUpstream: true,
		Dial: func(isConnect bool, net, addr string) (net.Conn, error) {
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
	p := New(&Opts{
		OKWaitsForUpstream: false,
		Dial: func(isConnect bool, net, addr string) (net.Conn, error) {
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

func TestShortCircuitHTTP(t *testing.T) {
	p := New(&Opts{
		Filter: filters.FilterFunc(func(ctx context.Context, req *http.Request, next filters.Next) (*http.Response, error) {
			return filters.ShortCircuit(req, &http.Response{
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
	p := New(&Opts{
		Filter: filters.FilterFunc(func(ctx context.Context, req *http.Request, next filters.Next) (*http.Response, error) {
			return filters.ShortCircuit(req, &http.Response{
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

func TestCONNECTWaitForUpstream(t *testing.T) {
	doTest(t, "CONNECT", false, true)
}

func TestCONNECTDontWaitForUpstream(t *testing.T) {
	doTest(t, "CONNECT", false, false)
}

func TestHTTPForwardFirst(t *testing.T) {
	doTest(t, "GET", false, false)
}

func TestHTTPDontForwardFirst(t *testing.T) {
	doTest(t, "GET", true, false)
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

	p := New(&Opts{
		IdleTimeout: 30 * time.Second,
		Dial: func(isConnect bool, network, addr string) (net.Conn, error) {
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
			go p.Handle(context.Background(), conn)
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

func doTest(t *testing.T, requestMethod string, discardFirstRequest bool, okWaitsForUpstream bool) {
	l, err := net.Listen("tcp", "localhost:0")
	if !assert.NoError(t, err) {
		return
	}
	defer l.Close()

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
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(req.Host))
	}))

	dial := func(isConnect bool, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", l.Addr().String())
	}

	filter := filters.FilterFunc(func(ctx context.Context, req *http.Request, next filters.Next) (*http.Response, error) {
		if req.RemoteAddr == "" {
			t.Fatal("Request missing RemoteAddr!")
		}
		return next(ctx, req)
	})

	isConnect := requestMethod == "CONNECT"
	p := New(&Opts{
		IdleTimeout:         30 * time.Second,
		OKWaitsForUpstream:  okWaitsForUpstream,
		DiscardFirstRequest: discardFirstRequest,
		Filter:              filter,
		Dial:                dial,
	})

	go func() {
		for {
			conn, acceptErr := pl.Accept()
			if acceptErr != nil {
				return
			}
			go p.Handle(context.Background(), conn)
		}
	}()

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
			if rtErr != nil {
				return nil, "", rtErr
			}
			body, rtErr := ioutil.ReadAll(resp.Body)
			if rtErr != nil {
				return resp, "", rtErr
			}
			return resp, string(body), nil
		}
		return nil, "", nil
	}

	req, _ := http.NewRequest(requestMethod, "http://subdomain.thehost:756", nil)
	req.RemoteAddr = "remoteaddr:134"

	includeFirst := isConnect || !discardFirstRequest
	resp, body, err := roundTrip(req, includeFirst)
	if !assert.NoError(t, err) {
		return
	}
	if !isConnect && !discardFirstRequest {
		assert.Equal(t, "subdomain.thehost:756", body, "Should have left port alone")
	}
	if !discardFirstRequest {
		log.Debug(resp)
		assert.Regexp(t, "timeout=\\d+", resp.Header.Get("Keep-Alive"), "All HTTP responses' headers should contain a Keep-Alive timeout")
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
	}

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
	}

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

func roundTrip(p Proxy, req *http.Request) (resp *http.Response, roundTripErr error, handleErr error) {
	toSend := &bytes.Buffer{}
	roundTripErr = req.Write(toSend)
	if roundTripErr != nil {
		return
	}
	received := &bytes.Buffer{}
	handleErr = p.Handle(context.Background(), mockconn.New(received, toSend))
	resp, roundTripErr = http.ReadResponse(bufio.NewReader(bytes.NewReader(received.Bytes())), req)
	return
}
