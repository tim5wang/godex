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
// (/control/nodes/{id}/proxy/{path...}) into a relay request. Streaming
// responses (SSE/chat events) are relayed in real time.
//
// When the center also runs as its own node (self node), requests targeting
// that node are served locally via localHandler instead of going over the
// relay channel, so the server can be operated from its own web UI without a
// loopback relay connection.
type ProxyHandler struct {
	hub       *Hub
	authorize func(*http.Request) bool
	// localNodeID + localHandler: when the target node id equals localNodeID
	// and a local handler is wired, the request is served in-process.
	localNodeID  string
	localHandler http.Handler
	// Timeout bounds a single forwarded request; zero (default) follows the
	// client context so SSE-style long-lived streams are not cut short.
	Timeout time.Duration
	// TrustLevel reports the target node's trust level (from the node
	// registry). guarded-remote nodes require an explicit approval header on
	// mutating requests; nil TrustLevel skips the check entirely.
	TrustLevel func(nodeID string) string
}

const trustApprovedHeader = "X-Godex-Trust-Approved"

// isMutatingMethod reports whether an HTTP method changes server state.
func isMutatingMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// NewProxyHandler creates the center-side proxy endpoint. authorize must
// return true for requests that may forward to nodes (web token check).
func NewProxyHandler(hub *Hub, authorize func(*http.Request) bool) *ProxyHandler {
	return &ProxyHandler{hub: hub, authorize: authorize}
}

// SetLocalHandler wires an in-process handler for the given node id (normally
// the center's own self node). Requests targeting that id are served locally
// instead of being forwarded over the relay channel.
func (p *ProxyHandler) SetLocalHandler(nodeID string, handler http.Handler) {
	p.localNodeID = nodeID
	p.localHandler = handler
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

	// Local direct: the center also runs as its own node. Serve the request
	// in-process with the path the local httpapi expects (relay prefix
	// stripped), keeping body/query/headers untouched.
	if p.localHandler != nil && nodeID == p.localNodeID {
		clone := r.Clone(r.Context())
		clone.URL.Path = "/" + after
		clone.RequestURI = ""
		p.localHandler.ServeHTTP(w, clone)
		return
	}

	// guarded-remote nodes are not trusted for mutating operations unless the
	// caller explicitly approves this request (the trust-approved header).
	if p.TrustLevel != nil && isMutatingMethod(r.Method) &&
		strings.EqualFold(strings.TrimSpace(p.TrustLevel(nodeID)), "guarded-remote") &&
		strings.TrimSpace(r.Header.Get(trustApprovedHeader)) == "" {
		http.Error(w, `{"error":"node requires approval for write operations"}`, http.StatusForbidden)
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
	ctx := r.Context()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Stream the node's response through chunk by chunk. The first callback
	// carries the status and headers; later callbacks carry body chunks. For
	// SSE responses this keeps chat events flowing to the browser in real
	// time instead of buffering until the node finishes.
	wroteHeader := false
	err = p.hub.ForwardStream(ctx, nodeID, req, func(status int, headers map[string]string, chunk []byte, final bool) error {
		if !wroteHeader {
			for key, value := range headers {
				w.Header().Set(key, value)
			}
			w.WriteHeader(status)
			wroteHeader = true
		}
		if len(chunk) > 0 {
			if _, werr := w.Write(chunk); werr != nil {
				return werr
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		return nil
	})
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
}
