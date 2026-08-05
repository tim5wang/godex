package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProxyHandler forwards incoming center-side requests to a target node over
// the relay channel, translating the user-facing control-plane URL
// (/control/nodes/{id}/proxy/{path...}) into a relay request.
type ProxyHandler struct {
	hub     *Hub
	authorize func(*http.Request) bool
	// Timeout bounds a single forwarded request; zero uses a 30s default.
	Timeout time.Duration
}

// NewProxyHandler creates the center-side proxy endpoint. authorize must
// return true for requests that may forward to nodes (web token check).
func NewProxyHandler(hub *Hub, authorize func(*http.Request) bool) *ProxyHandler {
	return &ProxyHandler{hub: hub, authorize: authorize}
}

func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.authorize != nil && !p.authorize(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// Path layout: /control/nodes/{id}/proxy/{path...}
	rest := strings.TrimPrefix(r.URL.Path, "/control/nodes/")
	nodeID, after, ok := strings.Cut(rest, "/proxy/")
	if !ok || nodeID == "" {
		http.Error(w, `{"error":"invalid proxy path"}`, http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
		return
	}

	req := ForwardRequest{
		Method: r.Method,
		Path:   "/" + after,
		Query:  r.URL.RawQuery,
		Body:   body,
	}
	req.Headers = map[string]string{}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Headers["Content-Type"] = ct
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		req.Headers["Authorization"] = auth
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	resp, err := p.hub.Forward(ctx, nodeID, req)
	if err != nil {
		switch {
		case errors.Is(err, ErrNodeOffline):
			http.Error(w, `{"error":"node offline"}`, http.StatusServiceUnavailable)
		case errors.Is(err, context.DeadlineExceeded):
			http.Error(w, `{"error":"node timeout"}`, http.StatusGatewayTimeout)
		default:
			http.Error(w, `{"error":"forward failed"}`, http.StatusBadGateway)
		}
		return
	}

	for key, value := range resp.Headers {
		w.Header().Set(key, value)
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}
