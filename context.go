package proxy

import (
	"context"
	"net"
)

type contextKey string

const (
	ctxKeyUpstream      = contextKey("upstream")
	ctxKeyUpstreamAddr  = contextKey("upstreamAddr")
	ctxKeyMITMTLSConfig = contextKey("mitmTLSConfig")
	ctxKeyNoRespondOkay = contextKey("noRespondOK")
)

func upstreamConn(ctx context.Context) net.Conn {
	upstream := ctx.Value(ctxKeyUpstream)
	if upstream == nil {
		return nil
	}
	return upstream.(net.Conn)
}

func upstreamAddr(ctx context.Context) string {
	upstreamAddr := ctx.Value(ctxKeyUpstreamAddr)
	if upstreamAddr == nil {
		return ""
	}
	return upstreamAddr.(string)
}
