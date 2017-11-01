package proxy

import (
	"context"
	"net"
	"net/http"
)

type contextKey string

const (
	ctxKeyUpstream      = contextKey("upstream")
	ctxKeyUpstreamAddr  = contextKey("upstreamAddr")
	ctxKeyNoRespondOkay = contextKey("noRespondOK")
	ctxKeyOrigURLScheme = contextKey("origURLScheme")
	ctxKeyOrigURLHost   = contextKey("origURLHost")
	ctxKeyOrigHost      = contextKey("origHost")
	ctxKeyAwareConn     = contextKey("awareConn")
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

func withAwareConn(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyAwareConn, make(map[string]interface{}, 2))
}

func setRequestForAwareConn(ctx context.Context, req *http.Request) {
	ctx.Value(ctxKeyAwareConn).(map[string]interface{})["req"] = req
}

func requestForAwareConn(ctx context.Context) *http.Request {
	return ctx.Value(ctxKeyAwareConn).(map[string]interface{})["req"].(*http.Request)
}

func setUpstreamForAwareConn(ctx context.Context, upstream net.Conn) {
	ctx.Value(ctxKeyAwareConn).(map[string]interface{})["up"] = upstream
}

func upstreamForAwareConn(ctx context.Context) net.Conn {
	upstream := ctx.Value(ctxKeyAwareConn).(map[string]interface{})["up"]
	if upstream == nil {
		return nil
	}
	return upstream.(net.Conn)
}
