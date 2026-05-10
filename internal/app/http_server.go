package app

import (
	"context"
	"net"
	"net/http"
)

// BindHTTPServerContext configures an HTTP server so active request contexts
// inherit and react to the provided parent lifecycle context.
func BindHTTPServerContext(ctx context.Context, server *http.Server) *http.Server {
	if server == nil {
		return nil
	}
	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	server.BaseContext = func(net.Listener) context.Context {
		return baseCtx
	}
	return server
}
