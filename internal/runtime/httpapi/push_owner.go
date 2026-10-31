package httpapi

import (
	"net/http"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/core/workcontext"
)

// NewPushOwnerResolver maps a push request onto the work-context scope key that
// owns the resulting subscription.
//
// This exists because push delivery is owner-targeted: a background nudge quotes
// that user's stream titles, so it must only reach browsers registered by the
// same (tenant, user). Resolving the owner from the same classifier the rest of
// the API uses keeps push attribution from drifting away from the identity that
// owns the streams — re-deriving it from, say, a user id in the request body
// would let a caller subscribe somebody else's browser to their own work.
//
// A request that resolves to no valid principal subscribes as unattributed,
// which receives broadcasts only.
func NewPushOwnerResolver(resolve func(*http.Request) identity.Principal) func(*http.Request) string {
	return func(r *http.Request) string {
		if resolve == nil {
			return ""
		}
		principal := resolve(r)
		if !principal.Valid() {
			return ""
		}
		return workcontext.ScopeKey(workcontext.Scope{TenantID: principal.TenantID, OwnerID: principal.UserID})
	}
}
