package httpapi

import (
	"fmt"
	"net/http"

	"github.com/tim5wang/godex/internal/core/identity"
)

// Center execution gate (decision ⑨): only the platform superadmin account may
// run an agent ON the center instance; tenant users are relayed to their own
// nodes instead. This is the "server can only run an agent for one user" rule
// from the requirement, expressed once instead of per handler.

// canExecuteOnCenter reports whether this principal may trigger agent execution
// on the instance serving the request.
//
// A node actor is exempt because relay-originated execution IS the documented
// remote-node path: the operator drives a remote node from the center, the
// request passes NodeAccess at the center, and the node then executes it under
// a relay-trust identity. Refusing node actors here would break that capability.
//
// This is an exemption, not a capability grant: a node actor never holds
// center_execution (see identity.Principal / IsNodeActor), so it remains unable
// to satisfy AuthorizeCenterExecution anywhere else.
func canExecuteOnCenter(principal identity.Principal) bool {
	return principal.IsNodeActor() || principal.CanExecuteOnCenter()
}

// requireCenterExecution wraps a protected-middleware chain with the center
// execution gate: authentication first, then the capability check, then the
// handler.
//
// Ordering matters and is why this wraps `protected` instead of being wrapped
// by it — the gate reads the principal that authentication attached.
func requireCenterExecution(protected func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(handler http.Handler) http.Handler {
		return protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				// No principal means the route was not behind authentication.
				// Fail closed rather than assume an operator.
				writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated principal"))
				return
			}
			if !canExecuteOnCenter(principal) {
				writeError(w, http.StatusForbidden, fmt.Errorf(
					"running an agent on the center requires the %q entitlement",
					identity.EntitlementCenterExecution))
				return
			}
			handler.ServeHTTP(w, r)
		}))
	}
}
