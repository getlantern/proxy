package filters

import (
	"io"
	"net"
	"net/http"
)

// Intercept returns a Handler that intercepts the specified Handler with the
// given Filter.
func Intercept(handler http.Handler, filter Filter) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		var cs *ConnectionState
		cs.downstream = func() net.Conn {
			if hj, ok := resp.(http.Hijacker); ok {
				downstream, _, _ := hj.Hijack()
				return downstream
			}
			return nil
		}

		next := func(cs *ConnectionState, filteredReq *http.Request) (*http.Response, *ConnectionState, error) {
			handler.ServeHTTP(resp, filteredReq)
			return nil, nil, nil
		}

		filteredResp, _, _ := filter.Apply(cs, req, next)
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
