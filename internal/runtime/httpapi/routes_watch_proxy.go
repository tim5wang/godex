package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/tim5wang/godex/internal/services/relay"
	"github.com/tim5wang/godex/internal/services/watchauth"
)

// Center-proxy multi-node remote control for the wrist.
//
// The watch pairs with exactly ONE node (its `wch_` credential lives in that
// node's registry). Asking it to drive another node must therefore not require a
// second pairing: the paired node forwards on the watch's behalf, over the same
// center proxy tunnel the web UI and the Android/PC clients already use:
//
//	watch → A (paired) → center /control/nodes/{B}/proxy/watch/... → B
//
// Nothing new is invented on the network layer; this file only teaches the watch
// surface to take part in it.
//
// # Why the receiving node accepts the call
//
// B's watch routes are guarded by withWatchScope, which wants a `wch_` token —
// and B has never seen this watch, so it cannot validate one. The request does,
// however, arrive over B's authenticated relay channel: B's own relay agent
// injects X-Godex-Relay-Trusted, an HMAC over B's node id keyed by B's control
// credential (see internal/services/relay/trust.go). Only the center could have
// relayed it, so the caller is a trusted sibling node rather than an anonymous
// client. That is exactly the argument routes_sandbox.go already relies on.
//
// Trusting the channel is not the same as trusting the caller's authority, so
// the authority travels explicitly: A sends the scopes the watch actually holds
// (WatchScopesHeader), and B grants only the intersection with what this route
// requires. A request that omits the header gets no authority at all — the
// default is deny, never "full watch scope", because an empty header must not be
// read as "unrestricted".

// WatchScopesHeader carries the scopes the calling node has already verified on
// the watch credential. Comma-separated watchauth scope strings.
const WatchScopesHeader = relay.WatchScopesHeader

// watchNodeQuery is the query parameter selecting the target node.
//
// Absent or empty means "the node serving this request", which is the historical
// behaviour and stays byte-identical. A value matching this node is treated the
// same way, so a client that always sends its target still works when the target
// happens to be local.
const watchNodeQuery = "node_id"

// watchLocalNodeID is the id the node uses for its own sessions. It matches what
// collectWatchCrossDevice reports so the device list and the session list agree
// on which entries are local.
const watchLocalNodeID = "local"

// watchNodeTarget reads the requested target node from the query, normalised so
// that "local", "" and this node's own id all mean "serve locally".
func watchNodeTarget(r *http.Request) string {
	target := strings.TrimSpace(r.URL.Query().Get(watchNodeQuery))
	if target == watchLocalNodeID {
		return ""
	}
	return target
}

// WatchRelayHolder gives the watch forwarder access to this instance's own node
// proxy handler, which is the same handler mounted at
// /control/nodes/{id}/proxy/ for the web and Android clients.
//
// A holder rather than a constructor argument because of an ordering cycle: the
// proxy handler wraps the API handler, while the watch routes live inside that
// API handler. The handler is therefore installed after both exist.
type WatchRelayHolder struct {
	v atomic.Value // http.Handler
}

// NewWatchRelayHolder returns an empty holder; callers must Set it before use.
func NewWatchRelayHolder() *WatchRelayHolder { return &WatchRelayHolder{} }

// Set installs the proxy handler.
func (h *WatchRelayHolder) Set(handler http.Handler) {
	if h != nil && handler != nil {
		h.v.Store(handler)
	}
}

// Handler returns the installed proxy handler, or nil when none was set.
func (h *WatchRelayHolder) Handler() http.Handler {
	if h == nil {
		return nil
	}
	v := h.v.Load()
	if v == nil {
		return nil
	}
	return v.(http.Handler)
}

// watchProxyForwarder forwards a watch request aimed at another node.
//
// It prefers the instance's own node proxy handler, because that handler already
// implements the local-vs-center decision exactly once: the node is served
// in-process when it is the self node or is connected to this relay hub, and
// otherwise relayed through the configured center. Re-implementing that here is
// what made "watch paired with the center" fail: the center IS the center, so it
// has no center_url to forward to, and the old code answered 503 even though the
// target node was connected to it and reachable one hop away.
//
// The center bridge remains as a fallback for wiring without a local handler
// (tests, and any instance that mounts the watch surface on its own).
type watchProxyForwarder struct {
	local  *WatchRelayHolder
	bridge *CenterBridge
	// webToken authorises the in-process hop: the relay proxy handler mounted on
	// this instance expects the same web token the browser sends.
	webToken func() string
	// selfID is this node's own id; a request naming it is served locally, which
	// also prevents the forwarder from relaying into itself.
	selfID string
}

// forward sends the request to nodeID over the center proxy and returns true.
//
// It reports false only when the request did not need forwarding (no target), so
// handlers read as "if the wrist asked for another node, that is handled here".
// Failures are answered in place, because there is no sensible local fallback: a
// request aimed at node B must never silently execute on node A.
func (f *watchProxyForwarder) forward(w http.ResponseWriter, r *http.Request, scopes []string) bool {
	target := watchNodeTarget(r)
	if target == "" {
		return false
	}
	// Naming this node explicitly is the same as naming none: serve locally. This
	// also breaks what would otherwise be an infinite relay loop (api handler ->
	// proxy handler -> same api handler) for a client that sends our real node id
	// instead of the local sentinel.
	if f != nil && f.selfID != "" && target == f.selfID {
		return false
	}
	if f == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf(
			"无法访问节点 %q：当前节点未配置中继能力", target))
		return true
	}

	// Preferred path: hand the request to this instance's own node proxy handler.
	if handler := f.local.Handler(); handler != nil {
		f.serveViaProxy(handler, w, r, target, scopes)
		return true
	}

	if f.bridge == nil || !f.bridge.Enabled() {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf(
			"无法访问节点 %q：当前节点未配置中心（control.center_url / control.center_token）", target))
		return true
	}

	// Fallback path: go straight to the center's proxy endpoint. ServeProxy sets
	// the center Authorization header itself, so the clone is handed over as-is
	// (its path is already the /control/nodes/{id}/proxy/... form it expects).
	f.bridge.ServeProxy(w, f.proxiedRequest(r, target, scopes))
	return true
}

// serveViaProxy runs the request through this instance's node proxy handler.
func (f *watchProxyForwarder) serveViaProxy(
	handler http.Handler,
	w http.ResponseWriter,
	r *http.Request,
	target string,
	scopes []string,
) {
	clone := f.proxiedRequest(r, target, scopes)
	// The proxy handler guards itself with the instance web token — the same
	// credential the browser sends. The watch credential is meaningless there
	// (it is a wch_ token, not a web token), so it is replaced rather than kept.
	if f.webToken != nil {
		if token := strings.TrimSpace(f.webToken()); token != "" {
			clone.Header.Set("Authorization", "Bearer "+token)
		}
	}
	handler.ServeHTTP(w, clone)
}

// proxiedRequest builds the /control/nodes/{id}/proxy/... clone of a watch call.
func (f *watchProxyForwarder) proxiedRequest(r *http.Request, target string, scopes []string) *http.Request {
	clone := r.Clone(r.Context())
	clone.URL.Path = "/control/nodes/" + target + "/proxy" + r.URL.Path
	clone.RequestURI = ""
	// Strip the node selector: the target must execute this locally. Leaving it in
	// place would make the target read node_id again and forward to itself forever
	// (the target is a plain node, so nothing else would break the loop).
	if q := clone.URL.Query(); q.Has(watchNodeQuery) {
		q.Del(watchNodeQuery)
		clone.URL.RawQuery = q.Encode()
	}
	// Authority travels as scopes; the watch credential does not (the target has
	// never seen this wrist and could not validate it).
	clone.Header.Del("Authorization")
	clone.Header.Set(WatchScopesHeader, strings.Join(scopes, ","))
	return clone
}

// withForwardToNode makes a watch handler able to serve a request aimed at a
// different node.
//
// When the caller named no node (or named this one) the handler runs exactly as
// before; otherwise the request is relayed through the center and the handler
// body never runs locally. That shape matters: the alternative (forward inside
// each handler) would let a new handler forget the check and silently execute a
// remote command on the wrong machine.
func (f *watchProxyForwarder) withForwardToNode(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.forward(w, r, forwardScopes(r)) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// watchGrantedScopeKey records the single scope a guard has already verified for
// this request, so the forwarder can advertise exactly that and nothing more.
//
// Without it a wrist holding read+command+approve would forward all three on
// every call, including a plain session read. The receiving node intersects with
// what the route needs, so that would still be safe — but it hands the next hop
// authority this request never used, which is exactly the kind of standing
// privilege that turns one bug into a privilege escalation.
type watchGrantedScopeKey struct{}

// watchScopeFromContext returns the scopes of the watch that made this request.
func watchScopeFromContext(ctx context.Context) []string {
	device, ok := watchDeviceFromContext(ctx)
	if !ok {
		return nil
	}
	return device.Scopes
}

// forwardScopes returns the authority to advertise when forwarding a request to
// another node.
//
// Preference order is narrowest first:
//
//  1. the exact scope this route's guard just verified (the normal case);
//  2. the watch's own scopes, when the request carries a real watch credential;
//  3. the scopes claimed by the node that forwarded to us, so a two-hop relay
//     keeps the original authority rather than inventing new or dropping it.
//
// Unknown scope strings are dropped at every step, so a malformed or hostile
// header cannot expand what the next hop is allowed to do.
func forwardScopes(r *http.Request) []string {
	if scope, ok := r.Context().Value(watchGrantedScopeKey{}).(string); ok && scope != "" {
		return []string{scope}
	}
	if scopes := watchScopeFromContext(r.Context()); len(scopes) > 0 {
		return knownWatchScopes(scopes)
	}
	return knownWatchScopes(strings.Split(r.Header.Get(WatchScopesHeader), ","))
}

// knownWatchScopes filters to scopes this build recognises, preserving order.
func knownWatchScopes(in []string) []string {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		scope := strings.TrimSpace(raw)
		switch scope {
		case watchauth.ScopeRead, watchauth.ScopeCommand, watchauth.ScopeApprove:
			out = append(out, scope)
		}
	}
	return out
}

// relayWatchScopes resolves authority for a relay-originated watch request.
//
// Returns relay=true when the request arrived over the authenticated relay
// channel; [granted] is then non-empty only if the origin node claimed the scope
// this route requires. A caller that is not on the relay channel gets
// relay=false, so a direct/anonymous caller cannot obtain authority by sending
// the header. The two results are kept separate so a trusted-but-underprivileged
// forward answers 403 (a real permission problem) instead of 401 ("no
// credential"), which would send the watch chasing a pairing that already exists.
func relayWatchScopes(
	r *http.Request,
	trust func(*http.Request) bool,
	required string,
) (granted []string, relay bool) {
	if trust == nil || !trust(r) {
		return nil, false
	}
	raw := strings.TrimSpace(r.Header.Get(WatchScopesHeader))
	for _, part := range strings.Split(raw, ",") {
		scope := strings.TrimSpace(part)
		// Only scopes this build understands, and only the one this route needs:
		// a forwarded request must not accumulate authority it was not asked for.
		if scope == required {
			switch scope {
			case watchauth.ScopeRead, watchauth.ScopeCommand, watchauth.ScopeApprove:
				granted = append(granted, scope)
			}
		}
	}
	return granted, true
}

// withWatchScopeOrRelay guards a watch route with either a real watch credential
// or a relay-originated forward from the node this watch paired with.
//
// Node B has never seen the wrist, so it cannot validate a wch_ token; what it
// can verify is that the request came over its own authenticated relay channel
// plus the exact scope the origin node vouched for. A request that is on the
// relay channel but lacks the required scope is rejected — never widened to the
// route's own scope, which would let one node grant itself another's authority.
func withWatchScopeOrRelay(
	auth *watchauth.Service,
	scope string,
	trust func(*http.Request) bool,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// record tags the request with the scope that was just verified, so a
		// forward advertises only this and not the device's whole grant.
		//
		// It also attaches the caller's identity (P0 §7). Two cases: a watch device
		// on the direct path (already attached by withWatchScope; re-derived here
		// to the same value, so the two paths cannot disagree), or a relay-vouched
		// forward when the wrist is paired with a different node than the one
		// serving the request — that path carries scopes rather than a device id,
		// so the wrist cannot be named and the principal records the forward.
		record := func(w http.ResponseWriter, r *http.Request) {
			principal := watchRelayPrincipal()
			if device, ok := watchDeviceFromContext(r.Context()); ok {
				principal = watchDevicePrincipal(device)
			}
			ctx := context.WithValue(r.Context(), watchGrantedScopeKey{}, scope)
			ctx = WithPrincipal(ctx, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		}
		withDevice := withWatchScope(auth, scope)(http.HandlerFunc(record))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			granted, onRelay := relayWatchScopes(r, trust, scope)
			if onRelay {
				if len(granted) == 0 {
					writeError(w, http.StatusForbidden, fmt.Errorf(
						"relay request lacks required watch scope %q", scope))
					return
				}
				record(w, r)
				return
			}
			withDevice.ServeHTTP(w, r)
		})
	}
}
