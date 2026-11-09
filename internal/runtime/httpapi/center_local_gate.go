package httpapi

import (
	"fmt"
	"net/http"

	"github.com/tim5wang/godex/internal/core/identity"
)

// Center-local data gate (decision ⑨, reinforced by the 2026-11-09 shape decision).
//
// A "center-local" surface is one whose data lives on THIS instance only: the flow
// store, the notes store, the taskboard ledger, the model config, the local model
// traffic. Those resources have no tenant dimension BY DESIGN. Under the chosen
// shape ("多节点平台多租户": the center authenticates, meters and forwards, while a
// tenant's execution happens on the tenant's own node) they are the platform
// operator's own machine state, not tenant data. Design §6 ("只转发不存用户数据")
// says the center holds no tenant content; what it does hold belongs to the operator.
//
// The hole this closes is the gap between those two facts. Such routes were
// registered behind `protected` alone — "any authenticated human" — so a signed-in
// tenant user could list every flow on the center, read any version, and create,
// publish or delete any flow, while the session and execution surfaces already
// refused them with 403. Authentication is not authority: the caller a
// center-local route INTENDS is the operator (or a node acting for it), and this
// gate states that once instead of per handler.
//
// Shape:
//   - The platform admin (the single web-token operator, decision ⑨) passes.
//   - A node actor passes: a request proxied to a remote node arrives wearing the
//     relay-trust identity, and that node's own flows ARE center-local to it. This
//     mirrors requirePlatformAdminOrNodeActor and authorizeConfigWrite, which
//     reached the same conclusion for /channels/* and restricted settings — a gate
//     that forgets this exemption silently breaks the remote-node Settings/Flows UI.
//   - Everyone else — tenant member and tenant admin alike — is refused with 403.
//   - A missing principal is 401, not 403: it means the route was not behind
//     authentication, which must fail closed rather than assume an operator (same
//     reasoning as requireCenterExecution / requirePlatformAdmin).
//
// Deliberately NOT a per-tenant binding. Multi-tenant storage partitioning of these
// stores does not exist and is not what is needed: the tenant's own flows and runs
// live on the tenant's node, and the center holds none of their content (§6).
// Adding tenant_id columns here would create the illusion of isolation on a surface
// where the correct boundary is "not reachable at all" — and would still depend on
// every future query remembering the predicate, which is exactly how the flow
// surface leaked in the first place.
func requireCenterLocalData(protected func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(handler http.Handler) http.Handler {
		return protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated principal"))
				return
			}
			if principal.IsPlatformAdmin() || principal.IsNodeActor() {
				handler.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusForbidden, fmt.Errorf(
				"center-local data requires the platform %q role", identity.RolePlatformAdmin))
		}))
	}
}
