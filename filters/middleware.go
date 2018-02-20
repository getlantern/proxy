package filters

import (
	"io"
	"net"
	"net/http"
)

// Intercept returns a Handler that intercepts the specified Handler with the
// given Filter.
func Intercept(handler http.Handler, filter Filter) http.Handler {
	var conn net.Conn
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		getDownstream := func() net.Conn {
			if conn == nil {
				conn, _, _ = resp.(http.Hijacker).Hijack()
			}
			return conn
		}
		ctx := BackgroundContext().WithValue(ctxKeyDownstream, getDownstream)

		next := func(ctx Context, filteredReq *http.Request) (*http.Response, Context, error) {
			handler.ServeHTTP(resp, filteredReq)
			return nil, nil, nil
		}

		filteredResp, _, _ := filter.Apply(ctx, req, next)
		if filteredResp != nil {
			for key, value := range filteredResp.Header {
				resp.Header()[key] = value
			}
			resp.WriteHeader(filteredResp.StatusCode)
			if filteredResp.Body != nil {
				io.Copy(resp, filteredResp.Body)
				filteredResp.Body.Close()
			}
		}
	})
}
