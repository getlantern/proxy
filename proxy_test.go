package proxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	ht "net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/getlantern/fdcount"
	"github.com/getlantern/httptest"
	"github.com/getlantern/keyman"
	"github.com/getlantern/mitm"
	"github.com/getlantern/mockconn"
	"github.com/getlantern/tlsdefaults"
	"github.com/stretchr/testify/assert"
)

const (
	okHeader = "X-Test-OK"
)

func init() {
	// Clean up certs
	os.Remove("serverpk.pem")
	os.Remove("servercert.pem")
	os.Remove("proxypk.pem")
	os.Remove("proxycert.pem")
}

func TestDialFailureHTTP(t *testing.T) {
	errorText := "I don't want to dial"
	d := mockconn.FailingDialer(errors.New(errorText))
	w := httptest.NewRecorder(nil)
	onError := func(req *http.Request, err error) *http.Response {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       ioutil.NopCloser(bytes.NewReader([]byte(err.Error()))),
		}
	}
	h := HTTP(false, 0, nil, nil, nil, onError, func(net, addr string) (net.Conn, error) {
		return d.Dial(net, addr)
	})
	req, _ := http.NewRequest("GET", "http://thehost:123", nil)
	err := h(w, req)
	if !assert.Error(t, err, "Should have gotten error") {
		return
	}
	assert.Equal(t, "thehost:123", d.LastDialed(), "Should have used specified port of 123")
	resp, err := http.ReadResponse(bufio.NewReader(w.Body()), req)
	if !assert.NoError(t, err) {
		return
	}
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
	w := httptest.NewRecorder(nil)
	h, buildErr := CONNECT(0, nil, true, nil, func(net, addr string) (net.Conn, error) {
		return d.Dial(net, addr)
	})
	if !assert.NoError(t, buildErr) {
		return
	}
	req, _ := http.NewRequest("CONNECT", "http://thehost:123", nil)
	err := h(w, req)
	if !assert.Error(t, err, "Should have gotten error") {
		return
	}
	assert.Equal(t, "thehost:123", d.LastDialed(), "Should have used specified port of 123")
	resp, err := http.ReadResponse(bufio.NewReader(w.Body()), req)
	if !assert.NoError(t, err) {
		return
	}
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
	w := httptest.NewRecorder(nil)
	h, buildErr := CONNECT(0, nil, false, nil, func(net, addr string) (net.Conn, error) {
		return d.Dial(net, addr)
	})
	if !assert.NoError(t, buildErr) {
		return
	}
	req, _ := http.NewRequest("CONNECT", "http://thehost:123", nil)
	err := h(w, req)
	if !assert.Error(t, err, "Should have gotten error") {
		return
	}
	assert.Equal(t, "thehost:123", d.LastDialed(), "Should have used specified port of 123")
	resp, err := http.ReadResponse(bufio.NewReader(w.Body()), req)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestCONNECTWaitForUpstream(t *testing.T) {
	doTest(t, http.MethodConnect, false, false, true, false)
}

func TestCONNECTMITMWaitForUpstream(t *testing.T) {
	doTest(t, http.MethodConnect, false, false, true, true)
}

func TestCONNECTDontWaitForUpstream(t *testing.T) {
	doTest(t, http.MethodConnect, false, false, false, false)
}

func TestHTTPForwardFirst(t *testing.T) {
	doTest(t, http.MethodGet, false, false, false, false)
}

func TestHTTPDontForwardFirst(t *testing.T) {
	doTest(t, http.MethodGet, true, false, false, false)
}

func TestHTTPForwardFirstShortCircuit(t *testing.T) {
	doTest(t, http.MethodGet, false, true, false, false)
}

func TestHTTPDontForwardFirstShortCircuit(t *testing.T) {
	doTest(t, http.MethodGet, true, true, false, false)
}

func TestCONNECTMITMForwardFirstShortCircuit(t *testing.T) {
	doTest(t, http.MethodConnect, false, true, true, true)
}

func TestCONNECTMITMDontForwardFirstShortCircuit(t *testing.T) {
	doTest(t, http.MethodConnect, true, true, true, true)
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

	intercept := HTTP(false, 30*time.Second, nil, nil, nil, nil, func(network, addr string) (net.Conn, error) {
		return net.Dial("tcp", origin.Listener.Addr().String())
	})
	proxy := ht.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		intercept(failingHijacker{w}, req)
	}))
	defer proxy.Close()

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
			conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
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

func doTest(t *testing.T, requestMethod string, discardFirstRequest bool, shouldShortCircuit bool, okWaitsForUpstream bool, shouldMITM bool) {
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
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(req.Host))
	}))

	dial := func(network, addr string) (net.Conn, error) {
		if requestMethod == http.MethodGet {
			return tls.Dial("tcp", l.Addr().String(), &tls.Config{
				ServerName: "localhost",
				RootCAs:    serverCert.PoolContainingCert(),
			})
		}
		return net.Dial("tcp", l.Addr().String())
	}

	onRequest := func(req *http.Request) *http.Request {
		if req.RemoteAddr == "" {
			t.Fatal("Request missing RemoteAddr!")
		}
		return req
	}

	shortCircuit := func(req *http.Request) *http.Response {
		if shouldShortCircuit {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Proto:      req.Proto,
				ProtoMajor: req.ProtoMajor,
				ProtoMinor: req.ProtoMinor,
				Close:      !req.ProtoAtLeast(1, 1),
			}
		}
		return nil
	}

	isConnect := requestMethod == "CONNECT"
	var intercept Interceptor
	if isConnect {
		var mitmOpts *MITMOpts
		if shouldMITM {
			mitmOpts = &MITMOpts{
				Opts: mitm.Opts{
					PKFile:   "proxypk.pem",
					CertFile: "proxycert.pem",
					ClientTLSConfig: &tls.Config{
						RootCAs: serverCert.PoolContainingCert(),
					},
				},
				OnRequest:    onRequest,
				ShortCircuit: shortCircuit,
			}
		}
		intercept, err = CONNECT(30*time.Second, nil, okWaitsForUpstream, mitmOpts, dial)
		if !assert.NoError(t, err) {
			return
		}
	} else {
		intercept = HTTP(discardFirstRequest, 30*time.Second, onRequest, shortCircuit, nil, nil, dial)
	}

	go http.Serve(pl, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		intercept(w, req)
	}))

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
			return nil, "", fmt.Errorf("Unable to write request: %v", rtErr)
		}
		if readResponse {
			resp, rtErr := http.ReadResponse(br, req)
			if rtErr != nil {
				return nil, "", fmt.Errorf("Unable to read response: %v", rtErr)
			}
			defer resp.Body.Close()
			body, rtErr := ioutil.ReadAll(resp.Body)
			if rtErr != nil {
				return resp, "", fmt.Errorf("Unable to read response body: %v", rtErr)
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
	if !isConnect && !discardFirstRequest && !shouldShortCircuit {
		assert.Equal(t, "subdomain.thehost:756", body, "Should have left port alone")
	}
	if !discardFirstRequest {
		assert.Regexp(t, "timeout=\\d+", resp.Header.Get("Keep-Alive"), "All HTTP responses' headers should contain a Keep-Alive timeout")
	}

	if isConnect {
		log.Debug("Upgrading to TLS")
		// Upgrade to TLS
		var tlsConfig *tls.Config
		if shouldMITM {
			proxyCert, err := keyman.LoadCertificateFromFile("proxycert.pem")
			if !assert.NoError(t, err) {
				return
			}
			tlsConfig = &tls.Config{
				ServerName: "localhost",
				RootCAs:    proxyCert.PoolContainingCert(),
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
	nestedReq.ProtoMajor = 1
	nestedReq.ProtoMinor = 1
	resp, body, err = roundTrip(nestedReq, true)
	if !assert.NoError(t, err) {
		return
	}
	if shouldShortCircuit {
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	} else {
		assert.Equal(t, "subdomain2.thehost", body, "Should have gotten right host")
	}
	if !isConnect {
		assert.Contains(t, resp.Header.Get("Keep-Alive"), "timeout", "All HTTP responses' headers should contain a Keep-Alive timeout")
	}

	nestedReq2Body := []byte("My Request")
	nestedReq2, _ := http.NewRequest("POST", "http://subdomain3.thehost/b", ioutil.NopCloser(bytes.NewBuffer(nestedReq2Body)))
	nestedReq2.ProtoMajor = 1
	nestedReq2.ProtoMinor = 0
	nestedReq2.ContentLength = int64(len(nestedReq2Body))
	resp, body, err = roundTrip(nestedReq2, true)
	if !assert.NoError(t, err) {
		return
	}
	if shouldShortCircuit {
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	} else {
		assert.Equal(t, "subdomain3.thehost", body, "Should have gotten right host")
	}
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
	if shouldShortCircuit {
		expectedConnections = 0
	}
	mx.RLock()
	defer mx.RUnlock()
	assert.Equal(t, expectedConnections, len(seenAddresses))

	conn.Close()
	assert.NoError(t, counter.AssertDelta(0), "All connections should have been closed")
}

func dumpRequest(req *http.Request) string {
	buf := &bytes.Buffer{}
	req.Write(buf)
	return buf.String()
}

func dumpResponse(resp *http.Response) string {
	buf := &bytes.Buffer{}
	resp.Write(buf)
	return buf.String()
}
