package proxy

import (
	"io"
)

// preReader is an io.Reader that allows inserting content at the head of the
// stream.
type preReader struct {
	io.Reader
	head []byte
}

// newPreReader wraps the supplied Reader, inserting the given bytes at the head
// of the stream.
func newPreReader(r io.Reader, head []byte) io.Reader {
	return &preReader{
		Reader: r,
		head:   head,
	}
}

// Read implements the method from io.Reader and first consumes the head before
// using the underlying connection.
func (r *preReader) Read(b []byte) (n int, err error) {
	n = copy(b, r.head)
	r.head = r.head[n:]
	if n > 0 {
		return
	}
	return r.Reader.Read(b)
}
