// Package helloreader is used to read ClientHellos off of connections.
package helloreader

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/getlantern/tlsutil"
)

// OnHello is a callback invoked when a ClientHello is read off a connection. Exactly one of hello
// or err will be non-nil. This callback is not invoked for incomplete ClientHellos. In other words,
// err is non-nil only if what has been read off the connection could not possibly constitute a
// ClientHello, regardless of further data from the connection.
type OnHello func(hello []byte, err error)

// WrapClient wraps the input client connection and executes the callback on the first ClientHello
// written to the connection. The callback is invoked synchronously during invocation of Write; if
// the callback blocks, Write will block as well.
func WrapClient(c net.Conn, onHello OnHello) net.Conn {
	return &conn{Conn: c, onHello: onHello, client: true}
}

// WrapServer wraps the input server connection and executes the callback on the first ClientHello
// read from the connection. The callback is invoked synchronously during invocation of Read; if the
// callback blocks, Read will block as well.
func WrapServer(c net.Conn, onHello OnHello) net.Conn {
	return &conn{Conn: c, onHello: onHello}
}

type conn struct {
	net.Conn
	client bool

	helloLock sync.Mutex
	helloRead bool
	helloBuf  bytes.Buffer
	onHello   OnHello
}

func (c *conn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	if c.client {
		return
	}
	c.checkHello(b[:n])
	return
}

func (c *conn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)
	if !c.client {
		return
	}
	c.checkHello(b[:n])
	return
}

func (c *conn) checkHello(newBytes []byte) {
	c.helloLock.Lock()
	if !c.helloRead { // TODO: need len(newBytes) > 0?
		c.helloBuf.Write(newBytes)
		nHello, parseErr := tlsutil.ValidateClientHello(c.helloBuf.Bytes())
		if parseErr == nil {
			c.onHello(c.helloBuf.Bytes()[:nHello], nil)
			c.helloRead = true
		} else if !errors.Is(parseErr, io.EOF) {
			c.onHello(nil, parseErr)
			c.helloRead = true
		}
	}
	c.helloLock.Unlock()
}
