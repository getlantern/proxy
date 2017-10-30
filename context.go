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
	ctxKeyOrigHost      = contextKey("origHost")
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

// OrigHost exposes the original Host for HTTP/1.1 requests
func OrigHost(ctx context.Context) string {
	origHost := ctx.Value(ctxKeyOrigHost)
	if origHost == nil {
		return ""
	}
	return origHost.(string)
}
