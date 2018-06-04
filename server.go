package proxy

import (
	"context"
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
			err := proxy.Handle(context.Background(), conn, conn)
			if err != nil {
				log.Error(err)
			}
		}()
	}
}
