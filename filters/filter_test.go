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
	resp, _, err := chain.Apply(nil, nil, func(ctx Context, req *http.Request) (*http.Response, Context, error) {
		return expectedResp, ctx, nil
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
	finalNext := func(ctx Context, req *http.Request) (*http.Response, Context, error) {
		calledFinalNext = true
		return &http.Response{
			Request:    req,
			Header:     make(http.Header),
			StatusCode: http.StatusConflict,
		}, ctx, sampleErr
	}
	resp, _, err := chain.Apply(BackgroundContext(), req, finalNext)
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

func (f *testFilter) Apply(ctx Context, req *http.Request, next Next) (*http.Response, Context, error) {
	if f.key == "" {
		// short circuit
		return ShortCircuit(ctx, req, &http.Response{
			Request:    req,
			StatusCode: http.StatusMovedPermanently,
			Body:       ioutil.NopCloser(strings.NewReader("shortcircuited")),
		})
	}
	req.Header.Add("In-Order", f.key)
	req.Header.Set(f.key, f.value)
	resp, ctx, err := next(ctx, req)
	if resp != nil {
		resp.Header.Add("Out-Order", f.key)
	}
	return resp, ctx, err
}
