package proxy

import (
	"sync"
)

const defaultBufferSize = 2 << 11 // 4K

type bufferSource interface {
	get() *[]byte
	put(buf *[]byte)
}

type defaultBufferSource struct{ sync.Pool }

func (dbs *defaultBufferSource) get() *[]byte {
	return dbs.Pool.Get().(*[]byte)
}

func (dbs *defaultBufferSource) put(buf *[]byte) {
	dbs.Pool.Put(buf)
}

func newBufferSource() bufferSource {
	return &defaultBufferSource{
		Pool: sync.Pool{
			New: func() any {
				b := make([]byte, defaultBufferSize)
				return &b
			},
		},
	}
}
