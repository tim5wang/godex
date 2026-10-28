package httpapi

import (
	"fmt"
	"net/http"

	"github.com/tim5wang/godex/internal/core/identity"
)

// requirePlatformAdmin gates platform-level node administration (design §4.4).
//
// Deliberately NOT requireCenterExecution: that gate exempts node actors so the
// center can drive remote execution, and reusing it here would let a joined node
// claim (i.e. reassign) other nodes. Administration changes ownership, so it is
// operator-only, with no exemptions.
// requirePlatformAdminOrNodeActor gates instance-wide settings that a NODE must
// still be able to change about itself.
//
// The distinction from requirePlatformAdmin is deliberate and load-bearing:
// /channels/* is NOT in the web client's "answered by this instance" list, so
// when a remote node is active the request is proxied to that node and arrives
// wearing the relay-trust node identity. Gating on the platform role alone would
// therefore break binding a channel bot on any remote node — the operator could
// still bind it on the center and nowhere else.
//
// This mirrors authorizeConfigWrite, which reaches the same conclusion for the
// same reason: restricted settings stay editable by the operator and by a node
// acting for it, and are refused only to principals that are neither.
func requirePlatformAdminOrNodeActor(protected func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(handler http.Handler) http.Handler {
		return protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				// Fail closed, exactly as requirePlatformAdmin does: a missing
				// principal means the route was not behind authentication.
				writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated principal"))
				return
			}
			if principal.IsPlatformAdmin() || principal.IsNodeActor() {
				handler.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusForbidden, fmt.Errorf(
				"this instance-wide setting requires the platform %q role", identity.RolePlatformAdmin))
		}))
	}
}

func requirePlatformAdmin(protected func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(handler http.Handler) http.Handler {
		return protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				// Same reasoning as requireCenterExecution: a missing principal
				// means the route is not behind authentication, and that must
				// fail closed rather than assume an operator.
				writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated principal"))
				return
			}
			if !principal.IsPlatformAdmin() {
				writeError(w, http.StatusForbidden, fmt.Errorf("node administration requires the platform %q role", identity.RolePlatformAdmin))
				return
			}
			handler.ServeHTTP(w, r)
		}))
	}
}
