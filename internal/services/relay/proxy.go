package relay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/platform/logger"
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
	// AuthorizeNode, when set, decides whether the CALLER may reach the target
	// node (identity.Policy.Authorize). This is the P0 NodeAccess seam on the
	// relay path: authorize above only proves "some valid credential", while
	// this decides "this caller may touch THIS node".
	//
	// Without it the center forwarded to any node on behalf of any credential —
	// so once tenants exist, a member of tenant A could drive tenant B's nodes.
	// nil skips the check, which keeps every existing caller working unchanged.
	AuthorizeNode func(r *http.Request, nodeID string) identity.Decision
	// UsageRecorder, when set, receives one record per FORWARDED request (design
	// §7 basic tier: forward requests/bytes, for reconciliation).
	//
	// nil skips metering, keeping every existing caller unchanged.
	UsageRecorder func(ForwardUsage)
}

// ForwardUsage is one forwarded request's metering record.
//
// A local type rather than the metering package's struct so this package stays
// free of a storage dependency: the relay's job is to move bytes, and a caller
// that wants counters adapts this at the wiring point.
//
// Carries no request or response CONTENT, only sizes: the center forwards tenant
// data without storing it (decision ②), so a metering record must not become the
// place that data accumulates.
type ForwardUsage struct {
	// TenantID/UserID come from the NodeAccess decision, not from headers — the
	// caller cannot choose which tenant its usage is billed to.
	TenantID string
	UserID   string
	NodeID   string
	Method   string
	Path     string
	// Status is the status the target returned (0 when the forward failed before
	// any response arrived, e.g. node offline).
	Status        int
	RequestBytes  int
	ResponseBytes int
}

const trustApprovedHeader = "X-Godex-Trust-Approved"

// WatchScopesHeader carries the watch scopes the origin node has already
// verified, so a node that has never seen this wrist can still act on its
// behalf (center-proxy multi-node remote control). Exported because the
// receiving side in httpapi reads it.
const WatchScopesHeader = "X-Godex-Watch-Scopes"

// relayForwardedHeaders is the allowlist of caller headers that survive the
// center hop. Everything else is dropped, so a caller cannot influence the
// target node beyond these fields. RelayTrustHeader is deliberately absent: the
// target's own relay agent generates it, and honouring a forwarded copy would
// let any caller claim relay trust.
var relayForwardedHeaders = []string{
	"Content-Type",
	"Authorization",
	WatchScopesHeader,
}

// defaultForwardTimeout bounds non-streaming forwarded requests when the
// handler has no explicit Timeout. Without it a node that silently stops
// answering (half-open relay, wedged process) would hang the proxy forever;
// SSE-style long-lived streams are exempt and keep following the client
// context.
const defaultForwardTimeout = 60 * time.Second

// isSSEStream reports whether the client expects a long-lived server-sent
// event stream, which must never be cut by the default forward timeout.
func isSSEStream(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}

// isMutatingMethod reports whether an HTTP method changes server state.
func isMutatingMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// clientIP extracts the caller's IP for audit logging, honoring
// X-Forwarded-For when present (center sits behind a reverse proxy).
func clientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
	// Audit: who called which node proxy path (P0-2). Logged before serving so
	// denied/guarded requests are also recorded.
	logger.Infof("relay proxy: caller=%s target=%s %s %s", clientIP(r), nodeID, r.Method, after)

	// NodeAccess (P0): the caller must be allowed to reach THIS node. Checked
	// before any local/hub dispatch so a denial short-circuits whether the
	// target is the center itself or a remote node.
	var decision identity.Decision
	if p.AuthorizeNode != nil {
		decision = p.AuthorizeNode(r, nodeID)
		if !decision.Allowed {
			logger.Warnf("relay proxy: denied caller=%s target=%s reason=%s user=%s",
				clientIP(r), nodeID, decision.Reason, decision.UserID)
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
	}

	// Local direct: the center also runs as its own node. Serve the request
	// in-process with the path the local httpapi expects (relay prefix
	// stripped), keeping body/query/headers untouched.
	if p.localHandler != nil && nodeID == p.localNodeID {
		clone := r.Clone(r.Context())
		clone.URL.Path = "/" + after
		clone.RequestURI = ""
		// Metered like a forward, even though no relay hop happens: it is still a
		// tenant's traffic through the center's proxy surface, and omitting it
		// would make a tenant's reported usage depend on where its node happened to
		// be attached.
		counter := &countingWriter{ResponseWriter: w, status: http.StatusOK}
		p.localHandler.ServeHTTP(counter, clone)
		p.recordUsage(decision, r, nodeID, counter.status, requestBodySize(r), counter.written)
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
	// Relay-internal headers that must survive the hop. This is a deliberately
	// short allowlist: the center decides what the target node is allowed to
	// believe about a request, so a caller cannot smuggle arbitrary headers
	// through. Without the watch-scope header a wrist request forwarded from the
	// node it paired with would arrive at the target node with no authority at
	// all and be rejected — the multi-node remote control would fail at runtime
	// even though every individual piece looked correct.
	for _, h := range relayForwardedHeaders {
		if v := r.Header.Get(h); v != "" {
			req.Headers[h] = v
		}
	}

	timeout := p.Timeout
	if timeout <= 0 && !isSSEStream(r) {
		timeout = defaultForwardTimeout
	}
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
	responseStatus := 0
	responseBytes := 0
	err = p.hub.ForwardStream(ctx, nodeID, req, func(status int, headers map[string]string, chunk []byte, final bool) error {
		if !wroteHeader {
			for key, value := range headers {
				w.Header().Set(key, value)
			}
			w.WriteHeader(status)
			wroteHeader = true
			responseStatus = status
		}
		if len(chunk) > 0 {
			if _, werr := w.Write(chunk); werr != nil {
				return werr
			}
			responseBytes += len(chunk)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		return nil
	})
	if err != nil {
		failureStatus := http.StatusBadGateway
		switch {
		case errors.Is(err, ErrNodeOffline):
			failureStatus = http.StatusServiceUnavailable
			http.Error(w, `{"error":"node offline"}`, failureStatus)
		case errors.Is(err, context.DeadlineExceeded):
			failureStatus = http.StatusGatewayTimeout
			http.Error(w, `{"error":"node timeout"}`, failureStatus)
		default:
			http.Error(w, `{"error":"forward failed"}`, failureStatus)
		}
		// Metered even though it failed: a tenant repeatedly hammering an offline
		// node is exactly the pattern reconciliation should surface, and recording
		// only successes would hide it. Status is the failure code, not 0, so the
		// row is self-describing.
		p.recordUsage(decision, r, nodeID, failureStatus, len(body), responseBytes)
		return
	}
	p.recordUsage(decision, r, nodeID, responseStatus, len(body), responseBytes)
}

// recordUsage reports one proxied request to the meter, if one is wired.
//
// Tenant and user come from the NodeAccess decision rather than from request
// headers: a caller must not be able to choose whose usage it is billed to.
func (p *ProxyHandler) recordUsage(decision identity.Decision, r *http.Request, nodeID string, status, requestBytes, responseBytes int) {
	if p.UsageRecorder == nil {
		return
	}
	p.UsageRecorder(ForwardUsage{
		TenantID:      decision.TenantID,
		UserID:        decision.UserID,
		NodeID:        nodeID,
		Method:        r.Method,
		Path:          r.URL.Path,
		Status:        status,
		RequestBytes:  requestBytes,
		ResponseBytes: responseBytes,
	})
}

// countingWriter records the status and body size served by the local handler.
//
// Needed because the local path writes straight to the ResponseWriter: without
// counting here, a self-node request would be metered as zero bytes.
//
// It forwards Flush and Hijack and exposes Unwrap, because the local handler
// serves the SAME surface as the API server — including WebSocket upgrades
// (terminal, watch frames). A wrapper that hides http.Hijacker would break those
// paths, which is the same hazard httpapi's gzipResponseWriter documents.
type countingWriter struct {
	http.ResponseWriter
	status  int
	written int
}

func (c *countingWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *countingWriter) Write(data []byte) (int, error) {
	written, err := c.ResponseWriter.Write(data)
	c.written += written
	return written, err
}

// Flush forwards to the wrapped writer so SSE streaming still works through it.
func (c *countingWriter) Flush() {
	if flusher, ok := c.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack forwards to the wrapped writer so protocol upgrades (WebSocket) keep
// working. Returning an error rather than panicking matches the interface: the
// upgrade code reports "not supported" the same way it would without a wrapper.
func (c *countingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := c.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

// Unwrap lets http.ResponseController (Go 1.20+) reach the underlying writer, so
// handlers that use the controller instead of type assertions are unaffected.
func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// requestBodySize returns the request body length when already known, for the
// local path (which does not read the body itself).
func requestBodySize(r *http.Request) int {
	if r.ContentLength > 0 {
		return int(r.ContentLength)
	}
	return 0
}
