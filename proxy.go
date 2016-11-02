package proxy

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/getlantern/golog"
)

var (
	log = golog.LoggerFor("proxy")
)

type DialFunc func(network, addr string) (conn net.Conn, err error)

// Interceptor is a function that will intercept a connection to an HTTP server
// and start proxying traffic. If proxying fails, it will return an error.
type Interceptor func(w http.ResponseWriter, req *http.Request) error

func addIdleKeepAlive(header http.Header, idleTimeout time.Duration) {
	if idleTimeout > 0 {
		// Tell the client when we're going to time out due to idle connections
		header.Set("Keep-Alive", fmt.Sprintf("timeout: %d", int(idleTimeout.Seconds())-2))
	}
}
