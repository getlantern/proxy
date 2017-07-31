package proxy

import (
	"net"

	"github.com/getlantern/proxy/filters"
)

type contextKey string

const (
	ctxKeyUpstream     = contextKey("upstream")
	ctxKeyUpstreamAddr = contextKey("upstreamAddr")
)

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
