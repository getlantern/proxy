package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/getlantern/golog"
	"github.com/getlantern/hidden"
)

var (
	log = golog.LoggerFor("proxy")
)

type DialFunc func(ctx context.Context, network, addr string) (conn net.Conn, err error)

// Interceptor is a function that will intercept a connection to an HTTP server
// and start proxying traffic. If proxying fails, it will return an error.
type Interceptor func(ctx context.Context, w http.ResponseWriter, req *http.Request) error

func addIdleKeepAlive(header http.Header, idleTimeout time.Duration) {
	if idleTimeout > 0 {
		// Tell the client when we're going to time out due to idle connections
		header.Set("Keep-Alive", fmt.Sprintf("timeout: %d", int(idleTimeout.Seconds())-2))
	}
}

func respondBadGateway(w http.ResponseWriter, err error) {
	log.Debugf("Responding BadGateway: %v", err)
	w.WriteHeader(http.StatusBadGateway)
	if _, writeError := w.Write([]byte(hidden.Clean(err.Error()))); writeError != nil {
		log.Debugf("Error writing error to ResponseWriter: %v", writeError)
	}
}
