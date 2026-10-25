package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/accounts"
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
	// AuthMethodSession is a browser/app login session (email + password). Recorded
	// so the audit trail distinguishes a signed-in person from the shared web token,
	// which grants a very different set of capabilities.
	AuthMethodSession = "session"
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
// are directly testable. Order is significant, and in two ways:
//
//  1. a node-originated credential must be classified before the operator
//     default, otherwise a node would inherit the center operator's capabilities;
//  2. a session token must ALSO be classified before the operator default —
//     otherwise every signed-in tenant user would be classified as the platform
//     superadmin. That is a privilege escalation, not a display bug, which is why
//     the session resolver is consulted before the default rather than after.
func classifyPrincipal(r *http.Request, nodeProxyToken string, trustedRelay bool, session func(*http.Request) (identity.Principal, bool)) identity.Principal {
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
	if session != nil {
		if principal, ok := session(r); ok {
			return principal
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
//
// session resolves a login-session token, or nil when multi-tenant sign-in is not
// wired (single-tenant nodes, the relay proxy). Passing it here rather than
// checking sessions elsewhere keeps "who is calling?" in exactly one function.
func principalForRequest(manager *config.Manager, relayTrust, nodeProxyToken func(*http.Request) bool, session func(*http.Request) (identity.Principal, bool)) func(*http.Request) identity.Principal {
	return func(r *http.Request) identity.Principal {
		cfg := manager.Current()
		trustedRelay := relayTrust != nil && relayTrust(r)
		return classifyPrincipal(
			r,
			strings.TrimSpace(cfg.Control.NodeProxyToken),
			trustedRelay,
			session,
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
//
// The session resolver is part of the same call for a security reason, not just
// for tidiness: classification falls through to the platform operator when no
// checker claims the request, so a caller that ACCEPTS a session token but
// resolves without this resolver would classify every signed-in tenant user as
// the superadmin. Accepting the credential and naming it must therefore be wired
// together, which is why this is one constructor rather than two knobs.
func NewPrincipalResolver(manager *config.Manager, accountsService *accounts.Service) func(*http.Request) identity.Principal {
	return principalForRequest(manager, relayTrustChecker(manager), nodeProxyTokenChecker(manager), sessionPrincipal(accountsService))
}

// NewSessionTokenChecker exposes the login-session credential check for callers
// outside this package.
//
// cmd/godex's relay proxy gate needs the same answer to "is this a valid login
// session?", and duplicating the check there would let the two paths drift: one
// accepting a token the other rejects turns a working sign-in into an
// intermittent 401 that depends on which surface the request happened to hit.
func NewSessionTokenChecker(accountsService *accounts.Service) func(*http.Request) bool {
	return sessionTokenChecker(accountsService)
}

// sessionTokenChecker accepts a valid login-session token as an authentication
// credential, for use as an additional checker in withBearerAuthProvider.
//
// It only decides "may this request in?"; the identity it yields is resolved by
// sessionPrincipal via classifyPrincipal. Both consult the same service, so a
// token cannot be accepted by one and unrecognized by the other.
func sessionTokenChecker(accountsService *accounts.Service) func(*http.Request) bool {
	if accountsService == nil {
		return nil
	}
	return func(r *http.Request) bool {
		_, ok := accountsService.Authenticate(extractProxyKeySecret(r))
		return ok
	}
}

// sessionPrincipal resolves a login-session token into its Principal.
//
// Returns ok=false for anything that is not a valid session, which lets
// classifyPrincipal fall through to its other branches.
func sessionPrincipal(accountsService *accounts.Service) func(*http.Request) (identity.Principal, bool) {
	if accountsService == nil {
		return nil
	}
	return func(r *http.Request) (identity.Principal, bool) {
		return accountsService.Authenticate(extractProxyKeySecret(r))
	}
}
