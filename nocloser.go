package proxy

import (
	"net"
)

type noCloseConn struct {
	net.Conn
}

func (conn *noCloseConn) Close() error {
	// don't close!
	return nil
}
