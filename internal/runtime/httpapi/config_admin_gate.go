package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/identity"
)

// Platform-admin-only config settings (design §3.3 / decision ⑫).
//
// The gate is applied per CONFIG ITEM, not per route, and that distinction is
// load-bearing: `/config` is node-scoped in the Web UI (apiClient.ts only keeps
// /meta, /control.., /push.. and /relay local), so when a remote node is
// selected the Settings page's GET/PUT /config are proxied to that node. A
// route-level platform-admin gate would therefore 403 all remote settings editing
// — the request arrives over the relay as a node actor, which is not a platform
// admin.

// platformAdminOnlyPaths collects the config paths the schema marks as
// platform-admin-only.
//
// Read from the schema rather than hard-coded here: the marker sits next to the
// field definition, so a new restricted setting cannot be added without the
// gate learning about it, and the UI (which renders the same schema) can
// disable the control.
func platformAdminOnlyPaths(manager *config.Manager) map[string]bool {
	if manager == nil {
		return nil
	}
	paths := map[string]bool{}
	for _, section := range manager.Schema() {
		for _, field := range section.Fields {
			if field.PlatformAdmin {
				paths[normalizeConfigPath(field.Path)] = true
			}
		}
	}
	return paths
}

// normalizeConfigPath makes path comparison tolerant of the spellings a client
// may send: leading/trailing space and the optional literal \u0000 prefix forms
// are not used here, but case and surrounding whitespace are.
func normalizeConfigPath(path string) string {
	return strings.ToLower(strings.TrimSpace(path))
}

// restrictedConfigKeys returns the requested writes that touch a
// platform-admin-only setting, sorted for a deterministic message.
//
// Both `values` and `clear_secrets` are inspected: clearing a restricted secret
// is as much a change as setting it, and would otherwise be a way to disable a
// control (e.g. wiping web.token) without passing the gate.
func restrictedConfigKeys(manager *config.Manager, values map[string]interface{}, clearSecrets []string) []string {
	restricted := platformAdminOnlyPaths(manager)
	if len(restricted) == 0 {
		return nil
	}
	var hits []string
	for path := range values {
		if restricted[normalizeConfigPath(path)] {
			hits = append(hits, normalizeConfigPath(path))
		}
	}
	for _, path := range clearSecrets {
		if restricted[normalizeConfigPath(path)] {
			hits = append(hits, normalizeConfigPath(path))
		}
	}
	sort.Strings(hits)
	return hits
}

// configWriteAuthError distinguishes "no identity at all" (401) from "identity
// present but not permitted" (403). Collapsing them into one error would report
// an unauthenticated caller as forbidden, which misleads clients into retrying
// with the same (absent) credentials.
type configWriteAuthError struct {
	msg             string
	unauthenticated bool
}

func (e configWriteAuthError) Error() string { return e.msg }

// statusForConfigAuthError maps a config-write authorization error onto an HTTP
// status, mirroring the statusForXxxError helpers used elsewhere (which return
// StatusOK for a nil error).
func statusForConfigAuthError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var authErr configWriteAuthError
	if errors.As(err, &authErr) && authErr.unauthenticated {
		return http.StatusUnauthorized
	}
	return http.StatusForbidden
}

// authorizeConfigWrite decides whether this caller may apply these config
// changes, returning an error for the first refusal.
//
// Node actors are allowed to write their own local config: within the
// single-tenant model the node is operated by the operator, and the node's own
// settings (model, workspace, ...) are exactly what remote Settings editing
// manages. Restricted settings are the exception only for principals that are
// neither the platform admin nor a node acting for it.
func authorizeConfigWrite(r *http.Request, manager *config.Manager, values map[string]interface{}, clearSecrets []string) error {
	restricted := restrictedConfigKeys(manager, values, clearSecrets)
	if len(restricted) == 0 {
		return nil
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		// Fail closed: a route without authentication must not be able to edit
		// the settings that govern who may use the instance.
		return configWriteAuthError{msg: "missing authenticated principal", unauthenticated: true}
	}
	if principal.IsPlatformAdmin() || principal.IsNodeActor() {
		return nil
	}
	return configWriteAuthError{msg: fmt.Sprintf(
		"changing %s requires the platform %q role (these settings decide who may use this instance)",
		strings.Join(restricted, ", "), identity.RolePlatformAdmin)}
}
