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

func (conn *noCloseConn) Wrapped() net.Conn {
	return conn.Conn
}
