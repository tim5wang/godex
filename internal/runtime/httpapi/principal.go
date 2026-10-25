package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/identity"
)

// Auth method labels recorded on the resolved Principal. They are what the
// audit trail shows, so they name the credential, not the route.
const (
	AuthMethodWebToken       = "web_token"
	AuthMethodNodeProxyToken = "node_proxy_token"
	AuthMethodRelayTrust     = "relay_trust"
	// AuthMethodBizKey is a machine credential issued by the operator for the
	// Agent Step Platform surface. Recorded so audit entries name the key rather
	// than implying a human caller.
	AuthMethodBizKey = "biz_key"
	// AuthMethodProxyKey is a gdx_ usage-gateway key. Recorded for the same
	// reason as the biz key: so the audit entry names the credential instead of
	// leaving the caller anonymous.
	AuthMethodProxyKey = "proxy_key"
	// AuthMethodWatch is a paired watch device credential. Same reason again:
	// name the device rather than record an anonymous caller.
	AuthMethodWatch = "watch_device"
)

// principalContextKey carries the authenticated Principal down the handler
// chain (design §3.2). Unexported so only this package can populate it — a
// handler must not be able to install its own identity.
type principalContextKey struct{}

// WithPrincipal attaches the authenticated principal to ctx.
func WithPrincipal(ctx context.Context, principal identity.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext returns the principal attached by withPrincipalAuth, or
// ok=false when the route was not behind that middleware.
func PrincipalFromContext(ctx context.Context) (identity.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(identity.Principal)
	return principal, ok
}

// withPrincipalAuth wraps an auth middleware so that, on the requests that auth
// already allowed, the caller's identity is attached to the context before the
// handler runs.
//
// Deliberately layered this way: `next` performs the allow/deny decision and
// this only *records* who was let in. Classification therefore cannot widen
// access — a bug here yields a wrong audit entry, not an unauthorized request.
// The handler runs only if `next` invoked it, which is why the principal is
// always present downstream of a protected route.
func withPrincipalAuth(next func(http.Handler) http.Handler, principalFor func(*http.Request) identity.Principal) func(http.Handler) http.Handler {
	return func(handler http.Handler) http.Handler {
		return next(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principalFor(r))))
		}))
	}
}

// classifyPrincipal maps a request that has ALREADY passed authentication onto
// a Principal.
//
// Pure (no manager/HTTP side effects) so the capability boundaries that matter
// are directly testable. Order is significant: a node-originated credential
// must be classified before the operator default, otherwise a node would
// inherit the center operator's capabilities.
func classifyPrincipal(r *http.Request, nodeProxyToken string, trustedRelay bool) identity.Principal {
	if trustedRelay {
		return identity.Principal{
			UserID:       "relay-node",
			Kind:         identity.KindNode,
			Entitlements: identity.NormalizeEntitlements([]string{identity.EntitlementRelayOnly}),
			AuthMethod:   AuthMethodRelayTrust,
		}
	}
	if nodeProxyToken != "" && bearerAuthorized(r, nodeProxyToken) {
		// The restricted nk_ credential is what a joined node presents. It is
		// explicitly NOT the operator: granting it center_execution would mean
		// one compromised node could run agents on the center, which is exactly
		// the boundary the nk_ token exists to draw.
		return identity.Principal{
			UserID:       "relay-node",
			Kind:         identity.KindNode,
			Entitlements: identity.NormalizeEntitlements([]string{identity.EntitlementRelayOnly}),
			AuthMethod:   AuthMethodNodeProxyToken,
		}
	}
	// Single-tenant default (decision ⑨): the holder of the single web token IS
	// the platform superadmin, and is the only principal that may execute an
	// agent on the center. This preserves today's single-operator behaviour
	// exactly; multi-tenant sign-in replaces this branch with a real lookup.
	return operatorPrincipal()
}

// operatorPrincipal is the principal for the single web-token holder.
//
// Extracted because three authentication paths converge on "the operator
// presented the web token": the header path (classifyPrincipal), the
// query-parameter path used by WebSocket/iframe clients, and the preview cookie.
// Keeping one constructor means the capability set cannot drift between them — a
// drift here would silently grant or remove center execution on one entry point
// only, which is exactly the kind of bug that is invisible until it matters.
func operatorPrincipal() identity.Principal {
	return identity.Principal{
		UserID:       "local-operator",
		PlatformRole: identity.RolePlatformAdmin,
		Kind:         identity.KindPlatform,
		Entitlements: identity.NormalizeEntitlements([]string{
			identity.EntitlementCenterExecution,
			identity.EntitlementRelayOnly,
		}),
		AuthMethod: AuthMethodWebToken,
	}
}

// principalForRequest builds the classifier bound to this instance's config.
//
// The relay-trust and node-proxy checkers are the same functions the auth
// middleware uses, so "was this credential accepted?" has one implementation.
func principalForRequest(manager *config.Manager, relayTrust, nodeProxyToken func(*http.Request) bool) func(*http.Request) identity.Principal {
	return func(r *http.Request) identity.Principal {
		cfg := manager.Current()
		trustedRelay := relayTrust != nil && relayTrust(r)
		return classifyPrincipal(
			r,
			strings.TrimSpace(cfg.Control.NodeProxyToken),
			trustedRelay,
		)
	}
}

// NewPrincipalResolver returns a resolver that maps an already-authenticated
// request onto its Principal, using this instance's relay-trust and node-proxy
// checkers.
//
// Exported for the center's relay proxy, which lives in internal/services/relay
// and therefore cannot import this package (httpapi imports relay). Resolving
// identity in one place keeps the relay path and the HTTP path from drifting on
// questions like "is the nk_ credential the operator?" (it is not).
func NewPrincipalResolver(manager *config.Manager) func(*http.Request) identity.Principal {
	return principalForRequest(manager, relayTrustChecker(manager), nodeProxyTokenChecker(manager))
}
