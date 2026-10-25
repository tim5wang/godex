package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

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

// watchProxyForwarder forwards a watch request to another node through the
// center. It is nil when this instance has no center configured, which is the
// normal case for a standalone node — callers must treat nil as "cannot reach
// other nodes" and answer 503 rather than pretending.
type watchProxyForwarder struct {
	bridge *CenterBridge
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
	if f == nil || f.bridge == nil || !f.bridge.Enabled() {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf(
			"无法访问节点 %q：当前节点未配置中心（control.center_url / control.center_token）", target))
		return true
	}

	// Build the proxied path and hand the request to the existing bridge, which
	// already knows how to authenticate to the center and stream the reply back.
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
	// The incoming watch credential is meaningless to the center and to B, so it
	// is dropped; authority is carried by the scopes header instead.
	clone.Header.Del("Authorization")
	clone.Header.Set(WatchScopesHeader, strings.Join(scopes, ","))

	f.bridge.ServeProxy(w, clone)
	return true
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
		record := func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), watchGrantedScopeKey{}, scope)))
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
