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
		go func() {
			if err := proxy.Handle(conn, conn); err != nil {
				log.Errorf("Error handling connection %v:", err)
			}
		}()
	}
}
