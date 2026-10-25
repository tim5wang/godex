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
