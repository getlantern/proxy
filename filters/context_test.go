package filters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContext(t *testing.T) {
	ctx := context.WithValue(context.WithValue(context.Background(), 1, "one"), "b", "bee")
	adapted := AdaptContext(ctx).WithValue("c", "cee")
	require.Equal(t, "one", adapted.Value(1))
	require.Equal(t, "bee", adapted.Value("b"))
	require.Equal(t, "cee", adapted.Value("c"))
	adapted = adapted.WithValue("c", "cee2")
	require.Equal(t, "cee2", adapted.Value("c"))
	require.Equal(t, nil, adapted.Value("d"))
}

func BenchmarkRegularContext(b *testing.B) {
	ctx := context.Background()
	key := "key"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx = context.WithValue(ctx, key, i)
		for j := 0; j < 10; j++ {
			ctx.Value(key)
		}
	}
}

func BenchmarkAdaptedContext(b *testing.B) {
	ctx := AdaptContext(context.Background())
	key := "key"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx = ctx.WithValue(key, i)
		for j := 0; j < 10; j++ {
			ctx.Value(key)
		}
	}
}
