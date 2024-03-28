package proxy

import (
	"sync"
)

const defaultBufferSize = 2 << 11 // 4K

type BufferSource interface {
	Get() *[]byte
	Put(buf *[]byte)
}

type defaultBufferSource struct{ sync.Pool }

func (dbs *defaultBufferSource) Get() *[]byte {
	return dbs.Pool.Get().(*[]byte)
}

func (dbs *defaultBufferSource) Put(buf *[]byte) {
	dbs.Pool.Put(buf)
}

func newBufferSource() BufferSource {
	return &defaultBufferSource{
		Pool: sync.Pool{
			New: func() any {
				b := make([]byte, defaultBufferSize)
				return &b
			},
		},
	}
}
