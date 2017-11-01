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
	ctxKeyOrigURLScheme = contextKey("origURLScheme")
	ctxKeyOrigURLHost   = contextKey("origURLHost")
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

func origURLScheme(ctx context.Context) string {
	origHost := ctx.Value(ctxKeyOrigURLScheme)
	if origHost == nil {
		return ""
	}
	return origHost.(string)
}

func origURLHost(ctx context.Context) string {
	origHost := ctx.Value(ctxKeyOrigURLHost)
	if origHost == nil {
		return ""
	}
	return origHost.(string)
}

func origHost(ctx context.Context) string {
	origHost := ctx.Value(ctxKeyOrigHost)
	if origHost == nil {
		return ""
	}
	return origHost.(string)
}
