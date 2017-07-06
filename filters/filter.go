package filters

import (
	"context"
	"net/http"

	"github.com/getlantern/errors"
)

// Filter supports intercepting and modifying http requests and responses
type Filter interface {
	Apply(ctx context.Context, req *http.Request, next Next) (*http.Response, error)
}

// FilterFunc adapts a function to a Filter
type FilterFunc func(ctx context.Context, req *http.Request, next Next) (*http.Response, error)

// Apply implements the interface Filter
func (ff FilterFunc) Apply(ctx context.Context, req *http.Request, next Next) (*http.Response, error) {
	return ff(ctx, req, next)
}

// Next is a function that's used to indicate that request processing should
// continue as usual.
type Next func(ctx context.Context, req *http.Request) (*http.Response, error)

// ShortCircuit is a convenience method for creating short-circuiting responses.
func ShortCircuit(req *http.Request, resp *http.Response) (*http.Response, error) {
	resp.Request = req
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	return resp, nil
}

// Chain is a chain of Filters that acts as an http.Handler.
type Chain []Filter

// Fail fails execution of the current chain
func Fail(msg string, args ...interface{}) error {
	return errors.New(msg, args...)
}

// Stop stops execution of the current chain
func Stop() error {
	return nil
}

// Join constructs a new chain of filters that executes the filters in order
// until it encounters a filter that returns false.
func Join(filters ...Filter) Chain {
	return Chain(filters)
}

// Append creates a new Chain by appending the given filters.
func (c Chain) Append(post ...Filter) Chain {
	return append(c, post...)
}

// Prepend creates a new chain by prepending the given filter.
func (c Chain) Prepend(pre Filter) Chain {
	result := make(Chain, len(c)+1)
	result[0] = pre
	copy(result[1:], c)
	return result
}

// Apply implements the interface Filter
func (c Chain) Apply(ctx context.Context, req *http.Request, next Next) (*http.Response, error) {
	return c.apply(ctx, req, next, 0)
}

func (c Chain) apply(ctx context.Context, req *http.Request, next Next, idx int) (*http.Response, error) {
	_next := next
	if idx < len(c)-1 {
		_next = func(ctx context.Context, req *http.Request) (*http.Response, error) {
			return c.apply(ctx, req, next, idx+1)
		}
	}
	return c[idx].Apply(ctx, req, _next)
}
