package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
)

// CenterBridge lets a local godex node act as a client of its center: the
// local Web UI's node-scoped requests (chat/terminal/files/...) are forwarded
// to the center's /control/nodes/{id}/proxy/... endpoint, which relays them
// over the target node's outbound relay channel. Authentication uses the
// center web token (control.center_token), mirroring how the CLI jump-host
// commands reach nodes through the center.
type CenterBridge struct {
	Client *http.Client

	// ep carries the mutable endpoint (center URL + token). Replaced atomically
	// by SetEndpoint during config hot-reload so an already-built bridge keeps
	// working with the new center without being recreated.
	ep atomic.Value // bridgeEndpoint

	mu       sync.Mutex
	cache    []noderegistry.NodeView
	cachedAt time.Time

	// rotate re-issues this node's own nk_ credential when the center rejects the
	// current one. Installed by the node runtime; nil on a center-side bridge (a
	// center has no credential to rotate) and in tests that do not exercise it.
	rotate     func(context.Context) (string, error)
	lastRotate time.Time
}

// bridgeEndpoint is the immutable snapshot stored in CenterBridge.ep.
type bridgeEndpoint struct {
	CenterURL string
	Token     string
}

// centerBridgeCacheTTL bounds how stale the center node list may be; the UI
// refreshes on a 15s cadence, so a matching TTL keeps the merged list fresh.
const centerBridgeCacheTTL = 15 * time.Second

// centerBridgeRotateCooldown bounds how often a rejected credential triggers a
// rotation. Without it, a center that rejects the freshly rotated token (wrong
// node, revoked credential) would make every proxied request mint another one,
// turning a broken credential into a request storm against the center.
const centerBridgeRotateCooldown = 30 * time.Second

// SetUnauthorizedHandler installs the self-healing rotation used when the center
// answers 401 to this node's control.center_token.
//
// This is what lets the per-node migration converge without an operator visiting
// every machine: a not-yet-rotated node keeps working on the legacy token until
// that token stops being accepted, and then re-issues its own per-node token on
// the spot rather than failing until someone notices.
func (b *CenterBridge) SetUnauthorizedHandler(handler func(context.Context) (string, error)) {
	b.mu.Lock()
	b.rotate = handler
	b.mu.Unlock()
}

// NewCenterBridge creates the bridge client. centerURL is used verbatim;
// trailing slashes are tolerated. A nil Client falls back to http.DefaultClient.
func NewCenterBridge(centerURL, token string) *CenterBridge {
	b := &CenterBridge{}
	b.ep.Store(bridgeEndpoint{
		CenterURL: strings.TrimRight(strings.TrimSpace(centerURL), "/"),
		Token:     strings.TrimSpace(token),
	})
	if b.Client == nil {
		b.Client = http.DefaultClient
	}
	return b
}

// SetEndpoint hot-reloads the bridge endpoint (used by the config live-apply
// path when control.center_url / control.center_token change).
func (b *CenterBridge) SetEndpoint(centerURL, token string) {
	b.ep.Store(bridgeEndpoint{
		CenterURL: strings.TrimRight(strings.TrimSpace(centerURL), "/"),
		Token:     strings.TrimSpace(token),
	})
	b.mu.Lock()
	b.cache = nil
	b.cachedAt = time.Time{}
	b.mu.Unlock()
}

// Endpoint returns the current center URL and token.
func (b *CenterBridge) Endpoint() (centerURL, token string) {
	ep := b.endpoint()
	return ep.CenterURL, ep.Token
}

// endpoint returns the current endpoint snapshot.
func (b *CenterBridge) endpoint() bridgeEndpoint {
	v := b.ep.Load()
	if v == nil {
		return bridgeEndpoint{}
	}
	return v.(bridgeEndpoint)
}

// Enabled reports whether the bridge is configured with both a center URL and
// a center web token.
func (b *CenterBridge) Enabled() bool {
	if b == nil {
		return false
	}
	ep := b.endpoint()
	return ep.CenterURL != "" && ep.Token != ""
}

// rotateOnUnauthorized handles a 401 from the center by re-issuing this node's
// credential once, then reporting whether the caller should retry.
//
// Deliberately called only from the callers that already hold a 401 — never
// preemptively — so the happy path costs nothing. The cooldown means a center
// that keeps rejecting still produces one attempt per window instead of one per
// request.
func (b *CenterBridge) rotateOnUnauthorized(ctx context.Context) bool {
	b.mu.Lock()
	handler := b.rotate
	if handler == nil || time.Since(b.lastRotate) < centerBridgeRotateCooldown {
		b.mu.Unlock()
		return false
	}
	b.lastRotate = time.Now()
	b.mu.Unlock()

	token, err := handler(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		logger.Warnf("center bridge: credential rotation failed: %v", err)
		return false
	}
	// Swap the endpoint token in place. The center URL is unchanged, and the
	// cached node list is dropped because the previous read was refused.
	centerURL, _ := b.Endpoint()
	b.SetEndpoint(centerURL, token)
	logger.Infof("center bridge: rotated the per-node credential after a 401")
	return true
}

// apiURL joins the center base URL with an /api-prefixed path.
func (b *CenterBridge) apiURL(path string) string {
	base := b.endpoint().CenterURL
	if strings.HasSuffix(base, "/api") {
		return base + path
	}
	return base + "/api" + path
}

// ListNodes returns the center's node registry view, cached for TTL seconds.
// A failed fetch returns the error so callers can degrade to the local list.
func (b *CenterBridge) ListNodes(ctx context.Context) ([]noderegistry.NodeView, error) {
	b.mu.Lock()
	if b.cache != nil && time.Since(b.cachedAt) < centerBridgeCacheTTL {
		out := append([]noderegistry.NodeView(nil), b.cache...)
		b.mu.Unlock()
		return out, nil
	}
	b.mu.Unlock()

	nodes, err := b.fetchNodes(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.cache = nodes
	b.cachedAt = time.Now()
	b.mu.Unlock()
	return nodes, nil
}

// doAuthed issues a request built by build (which must attach the current
// Bearer credential) and, on a 401, rotates this node's credential once and
// retries.
//
// The build callback is re-invoked for the retry rather than the first request
// being reused: the credential is part of the request's headers, so retrying the
// original object would send the very token the center just refused. Callers
// keep their own status handling, so this only removes the duplicate "and retry
// after rotating" step from each one.
func (b *CenterBridge) doAuthed(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	req, err := build()
	if err != nil {
		return nil, err
	}
	resp, err := b.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || !b.rotateOnUnauthorized(ctx) {
		return resp, nil
	}
	// Rotated: close the refused response and try once with the fresh credential.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	retry, err := build()
	if err != nil {
		return nil, err
	}
	return b.Client.Do(retry)
}

func (b *CenterBridge) fetchNodes(ctx context.Context) ([]noderegistry.NodeView, error) {
	resp, err := b.doAuthed(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.apiURL("/control/nodes"), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+b.endpoint().Token)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("center node list: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var nodes []noderegistry.NodeView
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

// GetNode fetches a single node view from the center.
func (b *CenterBridge) GetNode(ctx context.Context, id string) (noderegistry.NodeView, error) {
	resp, err := b.doAuthed(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.apiURL("/control/nodes/"+id), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+b.endpoint().Token)
		return req, nil
	})
	if err != nil {
		return noderegistry.NodeView{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return noderegistry.NodeView{}, fmt.Errorf("center node lookup %s: %s", id, resp.Status)
	}
	var node noderegistry.NodeView
	if err := json.NewDecoder(resp.Body).Decode(&node); err != nil {
		return noderegistry.NodeView{}, err
	}
	return node, nil
}

// GetOverview fetches the center's aggregated observation view for one node.
func (b *CenterBridge) GetOverview(ctx context.Context, id string) (relay.NodeOverview, error) {
	resp, err := b.doAuthed(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.apiURL("/control/nodes/"+id+"/overview"), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+b.endpoint().Token)
		return req, nil
	})
	if err != nil {
		return relay.NodeOverview{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return relay.NodeOverview{}, fmt.Errorf("center overview %s: %s", id, resp.Status)
	}
	var payload struct {
		Overview relay.NodeOverview `json:"overview"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return relay.NodeOverview{}, err
	}
	return payload.Overview, nil
}

// MergeNodeViews combines the local registry view with the center's node list:
// local entries win on id conflicts and keep their status; center-only entries
// are marked Source="center" so the UI can label them as reachable through the
// bridge.
func MergeNodeViews(local, remote []noderegistry.NodeView) []noderegistry.NodeView {
	byID := make(map[string]noderegistry.NodeView, len(local)+len(remote))
	for _, n := range local {
		byID[n.ID] = n
	}
	for _, n := range remote {
		if _, ok := byID[n.ID]; ok {
			continue
		}
		n.Source = "center"
		byID[n.ID] = n
	}
	out := make([]noderegistry.NodeView, 0, len(byID))
	for _, n := range byID {
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return out[i].Status == noderegistry.StatusOnline
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// ServeProxy forwards a local /control/nodes/{id}/proxy/{path} request to the
// center's equivalent endpoint, replacing the caller's Authorization header
// with the center token. SSE responses (text/event-stream) are streamed chunk
// by chunk so chat events keep flowing in real time through the bridge.
func (b *CenterBridge) ServeProxy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	target := b.apiURL(r.URL.Path)
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	req.URL.RawQuery = r.URL.RawQuery
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	// Relay-internal watch authority must survive this hop. Without it the center
	// forwards an anonymous-looking watch request to the target node, which then
	// rejects it for having no scopes — the multi-node remote control would fail
	// at runtime even though every individual piece looked correct. Deliberately a
	// single named header rather than copying the caller's headers wholesale: the
	// target must only receive authority this node explicitly vouched for.
	if scopes := r.Header.Get(WatchScopesHeader); scopes != "" {
		req.Header.Set(WatchScopesHeader, scopes)
	}
	req.Header.Set("Authorization", "Bearer "+b.endpoint().Token)
	resp, err := b.Client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	defer resp.Body.Close()
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

// bridgeProxyHandler routes /control/nodes/{id}/proxy/... requests: local
// nodes (self or directly connected to the local hub) go through the local
// proxy path; everything else is forwarded to the configured center.
type bridgeProxyHandler struct {
	local  http.Handler
	bridge *CenterBridge
	// isLocalNode reports whether the target node id is reachable directly on
	// this instance (self node or a node connected to the local relay hub).
	isLocalNode func(string) bool
}

// NewBridgeProxyHandler wraps a local proxy handler with the center bridge:
// requests for local nodes are served in-process, requests for remote nodes
// are forwarded to the center.
func NewBridgeProxyHandler(local http.Handler, bridge *CenterBridge, isLocalNode func(string) bool) http.Handler {
	return &bridgeProxyHandler{local: local, bridge: bridge, isLocalNode: isLocalNode}
}

func (h *bridgeProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/control/nodes/")
	nodeID, _, ok := strings.Cut(rest, "/proxy/")
	if !ok || nodeID == "" {
		http.Error(w, `{"error":"invalid proxy path"}`, http.StatusBadRequest)
		return
	}
	if h.isLocalNode != nil && h.isLocalNode(nodeID) {
		h.local.ServeHTTP(w, r)
		return
	}
	if h.bridge == nil || !h.bridge.Enabled() {
		http.Error(w, `{"error":"node offline: not reachable locally and no center bridge configured"}`, http.StatusServiceUnavailable)
		return
	}
	h.bridge.ServeProxy(w, r)
}
