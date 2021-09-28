package filters

import (
	"errors"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilterChain(t *testing.T) {
	doTestFilterChain(t, false)
}

func TestFilterChainShortCircuit(t *testing.T) {
	doTestFilterChain(t, true)
}

func TestFilterChainEmpty(t *testing.T) {
	expectedResp := &http.Response{
		Header: make(http.Header),
	}
	expectedResp.Header.Set("X-Hi", "Hello")
	chain := Join()
	resp, _, err := chain.Apply(nil, nil, func(cm *ConnectionMetadata, req *http.Request) (*http.Response, *ConnectionMetadata, error) {
		return expectedResp, cm, nil
	})
	assert.NoError(t, err)
	assert.Equal(t, expectedResp, resp)
}

func doTestFilterChain(t *testing.T, shortCircuit bool) {
	sampleErr := errors.New("Sample Error")
	chain := Join(&testFilter{"a", "1"}, &testFilter{"b", "2"}).Append(&testFilter{"c", "3"})
	if shortCircuit {
		chain = chain.Append(&testFilter{"", ""})
	}

	req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
	calledFinalNext := false
	finalNext := func(cm *ConnectionMetadata, req *http.Request) (*http.Response, *ConnectionMetadata, error) {
		calledFinalNext = true
		return &http.Response{
			Request:    req,
			Header:     make(http.Header),
			StatusCode: http.StatusConflict,
		}, cm, sampleErr
	}
	resp, _, err := chain.Apply(new(ConnectionMetadata), req, finalNext)
	expectedErr := sampleErr
	if shortCircuit {
		expectedErr = nil
	}
	assert.Equal(t, expectedErr, err)
	if shortCircuit {
		t.Log(resp)
		assert.EqualValues(t, -1, resp.ContentLength)
		assert.Equal(t, []string{"chunked"}, resp.TransferEncoding)
	} else {
		assert.True(t, calledFinalNext)
	}
	assert.Equal(t, "1", req.Header.Get("a"))
	assert.Equal(t, "2", req.Header.Get("b"))
	assert.Equal(t, "3", req.Header.Get("c"))
	assert.EqualValues(t, []string{"a", "b", "c"}, req.Header["In-Order"])
	assert.EqualValues(t, []string{"c", "b", "a"}, resp.Header["Out-Order"])
	expectedStatus := http.StatusConflict
	if shortCircuit {
		expectedStatus = http.StatusMovedPermanently
	}
	assert.Equal(t, expectedStatus, resp.StatusCode)
}

type testFilter struct {
	key   string
	value string
}

func (f *testFilter) Apply(cm *ConnectionMetadata, req *http.Request, next Next) (*http.Response, *ConnectionMetadata, error) {
	if f.key == "" {
		// short circuit
		return ShortCircuit(cm, req, &http.Response{
			Request:    req,
			StatusCode: http.StatusMovedPermanently,
			Body:       ioutil.NopCloser(strings.NewReader("shortcircuited")),
		})
	}
	req.Header.Add("In-Order", f.key)
	req.Header.Set(f.key, f.value)
	resp, cm, err := next(cm, req)
	if resp != nil {
		resp.Header.Add("Out-Order", f.key)
	}
	return resp, cm, err
}
