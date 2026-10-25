package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
)

// Anonymous-exposure guard at the REQUEST layer (design §5, the gap the startup
// guard cannot close).
//
// With no web token configured, this instance treats an anonymous caller as the
// platform operator — the intended behaviour for a loopback developer instance.
// `internal/app`'s startup guard refuses to start that configuration on a
// non-loopback address, which covers the obvious mistake.
//
// It does NOT cover the reverse-proxy case: an instance bound to 127.0.0.1 behind
// nginx (or a tunnel, or a cloud load balancer) is "loopback" from the kernel's
// point of view while being publicly reachable. In that arrangement the startup
// guard passes and the whole management API — including the surfaces that spawn
// shells — is served to the internet as the operator.
//
// The signal that distinguishes it is the forwarding headers a proxy adds: they
// mean something in front of us believes it is brokering someone else's
// connection, so "the caller is this host's user" is no longer a safe assumption.
// Refusing them keeps local development working (a direct caller sends no such
// header) while closing the reachable-anonymous path.

// proxyIndicatorHeaders are the headers a reverse proxy adds when it forwards a
// request. Checked as a set because any one of them is enough to disprove
// "direct local caller".
var proxyIndicatorHeaders = []string{
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Real-Ip",
	"Forwarded",
	"Cf-Connecting-Ip",
	"X-Client-Ip",
}

// rejectProxiedAnonymous refuses proxied requests while no web token is
// configured.
//
// Takes and returns a MIDDLEWARE rather than a handler so it can wrap the
// existing auth chain at the same layer every route already uses
// (`protected := anonymousGuard(withPrincipalAuth(withBearerAuthProvider(...)))`).
// That placement is what makes the guard cover every authenticated surface at
// once: with no token, an anonymous caller receives the operator capability set on
// ALL of them, so guarding a subset would leave the rest reachable.
func rejectProxiedAnonymous(manager *config.Manager) func(func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next func(http.Handler) http.Handler) func(http.Handler) http.Handler {
		return func(handler http.Handler) http.Handler {
			inner := next(handler)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if manager == nil || strings.TrimSpace(manager.Current().WebToken) != "" {
					// A token IS configured, so the auth middleware decides on the
					// credential and the anonymous promotion below cannot happen.
					inner.ServeHTTP(w, r)
					return
				}
				if header := proxyIndicator(r); header != "" {
					writeError(w, http.StatusForbidden, fmt.Errorf(
						"refusing a proxied request (%s) on an instance with no web token: "+
							"an anonymous caller is treated as the platform operator here, so this "+
							"instance must not be reachable through a proxy — set web.token / "+
							"GODEX_WEB_TOKEN to serve it behind one", header))
					return
				}
				inner.ServeHTTP(w, r)
			})
		}
	}
}

// proxyIndicator returns the name of the first forwarding header present, or "".
// The name is reported so the refusal explains WHICH header proved the request was
// proxied — a bare "forbidden" would be the kind of unexplainable 403 this
// codebase works to avoid.
func proxyIndicator(r *http.Request) string {
	for _, header := range proxyIndicatorHeaders {
		if strings.TrimSpace(r.Header.Get(header)) != "" {
			return header
		}
	}
	return ""
}
