package proxy

import (
	"context"
	"net"
	"net/http"
)

type contextKey string

const (
	// CtxKeyInitialRequest is the key in the context whose value stores the
	// first *http.Request received from the downstream connection, which may
	// contain some information the subsequent requests lack.
	CtxKeyInitialRequest = contextKey("initialRequest")
	ctxKeyUpstream       = contextKey("upstream")
	ctxKeyUpstreamAddr   = contextKey("upstreamAddr")
	ctxKeyNoRespondOkay  = contextKey("noRespondOK")
	ctxKeyAwareConn      = contextKey("awareConn")
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

func initialRequest(ctx context.Context) *http.Request {
	req := ctx.Value(CtxKeyInitialRequest)
	if req == nil {
		return nil
	}
	return req.(*http.Request)
}

func origURLScheme(ctx context.Context) string {
	if req := initialRequest(ctx); req != nil {
		return req.URL.Scheme
	}
	return ""
}

func origURLHost(ctx context.Context) string {
	if req := initialRequest(ctx); req != nil {
		return req.URL.Host
	}
	return ""
}

func origHost(ctx context.Context) string {
	if req := initialRequest(ctx); req != nil {
		return req.Host
	}
	return ""
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
