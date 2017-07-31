package proxy

import (
	"context"
	"net"

	"github.com/getlantern/proxy/filters"
)

type contextKey string

const (
	ctxKeyDownstream    = contextKey("downstream")
	ctxKeyRequestNumber = contextKey("requestNumber")
	ctxKeyUpstream      = contextKey("upstream")
	ctxKeyUpstreamAddr  = contextKey("upstreamAddr")
)

// ctext implements filters.Context
type ctext struct {
	context.Context
}

func (ctx *ctext) DownstreamConn() net.Conn {
	return ctx.Value(ctxKeyDownstream).(net.Conn)
}

func (ctx *ctext) RequestNumber() int {
	return ctx.Value(ctxKeyRequestNumber).(int)
}

func upstreamConn(ctx filters.Context) net.Conn {
	upstream := ctx.Value(ctxKeyUpstream)
	if upstream == nil {
		return nil
	}
	return upstream.(net.Conn)
}

func upstreamAddr(ctx filters.Context) string {
	upstreamAddr := ctx.Value(ctxKeyUpstreamAddr)
	if upstreamAddr == nil {
		return ""
	}
	return upstreamAddr.(string)
}

func contextWithValue(parent context.Context, key interface{}, val interface{}) filters.Context {
	return &ctext{context.WithValue(parent, key, val)}
}
