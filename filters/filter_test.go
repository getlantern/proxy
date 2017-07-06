package filters

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilterChain(t *testing.T) {
	doTestFilterChain(t, false)
}

func TestFilterChainShortCircuit(t *testing.T) {
	doTestFilterChain(t, true)
}

func doTestFilterChain(t *testing.T, shortCircuit bool) {
	sampleErr := errors.New("Sample Error")
	chain := Join(&testFilter{"a", "1"}, &testFilter{"b", "2"}).Append(&testFilter{"c", "3"})
	if shortCircuit {
		chain = chain.Append(&testFilter{"", ""})
	}

	req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
	calledFinalNext := false
	finalNext := func(ctx context.Context, req *http.Request) (*http.Response, error) {
		calledFinalNext = true
		return &http.Response{
			Request:    req,
			Header:     make(http.Header),
			StatusCode: http.StatusConflict,
		}, sampleErr
	}
	resp, err := chain.Apply(context.Background(), req, finalNext)
	expectedErr := sampleErr
	if shortCircuit {
		expectedErr = nil
	}
	assert.Equal(t, expectedErr, err)
	if !shortCircuit {
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

func (f *testFilter) Apply(ctx context.Context, req *http.Request, next Next) (*http.Response, error) {
	if f.key == "" {
		// short circuit
		return ShortCircuit(req, &http.Response{
			Request:    req,
			StatusCode: http.StatusMovedPermanently,
		})
	}
	req.Header.Add("In-Order", f.key)
	req.Header.Set(f.key, f.value)
	resp, err := next(ctx, req)
	if resp != nil {
		resp.Header.Add("Out-Order", f.key)
	}
	return resp, err
}
