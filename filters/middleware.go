package filters

import (
	"io"
	"net/http"
)

// Intercept returns a Handler that intercepts the specified Handler with the
// given Filter.
func Intercept(handler http.Handler, filter Filter) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		var cm *ConnectionMetadata
		if hj, ok := resp.(http.Hijacker); ok {
			cm.downstream, _, _ = hj.Hijack()
		}

		next := func(cm *ConnectionMetadata, filteredReq *http.Request) (*http.Response, *ConnectionMetadata, error) {
			handler.ServeHTTP(resp, filteredReq)
			return nil, nil, nil
		}

		filteredResp, _, _ := filter.Apply(cm, req, next)
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
