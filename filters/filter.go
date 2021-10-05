package filters

import (
	"io"
	"io/ioutil"
	"net/http"
	"strings"
)

// Filter supports intercepting and modifying http requests and responses
type Filter interface {
	Apply(cs *ConnectionState, req *http.Request, next Next) (*http.Response, *ConnectionState, error)
}

// FilterFunc adapts a function to a Filter
type FilterFunc func(cs *ConnectionState, req *http.Request, next Next) (*http.Response, *ConnectionState, error)

// Apply implements the interface Filter
func (ff FilterFunc) Apply(cs *ConnectionState, req *http.Request, next Next) (*http.Response, *ConnectionState, error) {
	return ff(cs, req, next)
}

// Next is a function that's used to indicate that request processing should
// continue as usual.
type Next func(cs *ConnectionState, req *http.Request) (*http.Response, *ConnectionState, error)

// ShortCircuit is a convenience method for creating short-circuiting responses.
func ShortCircuit(cs *ConnectionState, req *http.Request, resp *http.Response) (*http.Response, *ConnectionState, error) {
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	resp.Proto = req.Proto
	resp.ProtoMajor = req.ProtoMajor
	resp.ProtoMinor = req.ProtoMinor
	if resp.Body != nil && resp.ContentLength <= 0 && len(resp.TransferEncoding) == 0 {
		resp.ContentLength = -1
		resp.TransferEncoding = []string{"chunked"}
	}
	return resp, cs, nil
}

// Fail fails processing, returning a response with the given status code and
// description populated from error.
func Fail(cs *ConnectionState, req *http.Request, statusCode int, err error) (*http.Response, *ConnectionState, error) {
	errString := err.Error()
	resp := &http.Response{
		Proto:         req.Proto,
		ProtoMajor:    req.ProtoMajor,
		ProtoMinor:    req.ProtoMinor,
		StatusCode:    statusCode,
		Header:        make(http.Header),
		Body:          ioutil.NopCloser(strings.NewReader(errString)),
		ContentLength: int64(len(errString)),
		Close:         true,
	}
	return resp, cs, err
}

// Discard discards the given request. Make sure to use this when discarding
// requests in order to make sure that the request body is read.
func Discard(cs *ConnectionState, req *http.Request) (*http.Response, *ConnectionState, error) {
	if req.Body != nil {
		io.Copy(ioutil.Discard, req.Body)
		req.Body.Close()
	}
	return nil, cs, nil
}

// Chain is a chain of Filters that acts as an http.Handler.
type Chain []Filter

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
func (c Chain) Apply(cs *ConnectionState, req *http.Request, next Next) (*http.Response, *ConnectionState, error) {
	return c.apply(cs, req, next, 0)
}

func (c Chain) apply(cs *ConnectionState, req *http.Request, next Next, idx int) (*http.Response, *ConnectionState, error) {
	if idx == len(c) {
		return next(cs, req)
	}
	return c[idx].Apply(cs, req,
		func(cs *ConnectionState, req *http.Request) (*http.Response, *ConnectionState, error) {
			return c.apply(cs, req, next, idx+1)
		})
}
