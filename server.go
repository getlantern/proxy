package proxy

import (
	"net"

	"github.com/getlantern/errors"
)

// Serve runs a proxy server using the given Listener
func (proxy *proxy) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return errors.New("Unable to accept: %v", err)
		}
		go proxy.Handle(conn, conn)
	}
}
