package proxy

import "testing"

func BenchmarkPool(b *testing.B) {
	b.ReportAllocs()

	source := newBufferSource()
	for i := 0; i < b.N; i++ {
		buf := source.Get()
		source.Put(buf)
	}
}
