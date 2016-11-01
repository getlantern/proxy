package proxy

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/getlantern/golog"
	"github.com/getlantern/ops"
)

var (
	log = golog.LoggerFor("proxy")
)

type DialFunc func(network, addr string) (conn net.Conn, err error)

type Interceptor func(op ops.Op, w http.ResponseWriter, req *http.Request)

func isUnexpected(err error) bool {
	text := err.Error()
	return !strings.HasSuffix(text, "EOF") && !strings.Contains(text, "use of closed network connection") && !strings.Contains(text, "Use of idled network connection")
}

func addIdleKeepAlive(header http.Header, idleTimeout time.Duration) {
	if idleTimeout > 0 {
		// Tell the client when we're going to time out due to idle connections
		header.Set("Keep-Alive", fmt.Sprintf("timeout: %d", int(idleTimeout.Seconds())-2))
	}
}
