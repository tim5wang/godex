package httpapi

import (
	"net/http"

	"github.com/tim5wang/godex/internal/core/config"
)

func registerConfigRoutes(mux *http.ServeMux, manager *config.Manager, protected func(http.Handler) http.Handler) {
	// This instance's configuration is operator data: it names the workspace and
	// project directories, the model, the env files. Measured 2026-11-10 with a
	// TENANT MEMBER's session, before this gate: GET /config, /config/meta,
	// /config/schema and /config/doctor all returned 200 (the secrets inside the
	// view ARE masked, so this was disclosure of machine layout rather than of
	// credentials), and PUT /config accepted writes to the non-restricted items
	// (`model`, `workspace_dir`, ...) because authorizeConfigWrite only refuses the
	// schema-marked ones.
	//
	// Why this is safe now, when the design previously argued against a route-level
	// gate: that argument (§3.3) was that /config is node-scoped in the Web UI, so a
	// route gate wrapped around `requirePlatformAdmin` would 403 every remote node's
	// own Settings editing. The premise is still right — apiClient proxies /config
	// to the selected node — but the conclusion no longer follows, because
	// requireCenterLocalData admits NODE ACTORS: the proxied request arrives as the
	// relay-trust node identity and edits the node's own config, which is exactly
	// what remote Settings does today.
	//
	// authorizeConfigWrite stays, as the second layer it now is: its schema marker
	// is what the UI reads to disable platform-only controls, and keeping it means a
	// future caller that reaches the handler by another path still meets the
	// item-level rule.
	local := requireCenterLocalData(protected)
	mux.Handle("GET /config/meta", local(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.Meta())
	})))
	mux.Handle("GET /config/schema", local(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.Schema())
	})))
	mux.Handle("GET /config", local(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.View())
	})))
	mux.Handle("PUT /config", local(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req updateConfigRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		// Platform-admin-only settings are still checked per ITEM: the route gate
		// above admits the operator and a node acting for it, and this rule says
		// WHICH items a node may not change on itself either.
		if err := authorizeConfigWrite(r, manager, req.Values, req.ClearSecrets); err != nil {
			writeError(w, statusForConfigAuthError(err), err)
			return
		}
		view, err := manager.Update(r.Context(), config.UpdateRequest{
			Values:       req.Values,
			ClearSecrets: append([]string{}, req.ClearSecrets...),
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("POST /config/reload", local(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		view, err := manager.ReloadFromDisk(r.Context())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	// POST /config/reveal returns a credential in PLAINTEXT (web.token,
	// channels.feishu.app_secret, the search-provider and media API keys). It was
	// behind `protected` alone — "any authenticated human" — so a signed-in tenant
	// user could read the instance's own secrets, web.token included, which is the
	// credential that authorises the operator. This is disclosure, not settings
	// editing, so the per-ITEM treatment PUT /config needs (design §3.3: /config is
	// node-scoped in the UI, so a route gate would break remote editing) does not
	// apply here; what does matter is the same node exemption, because with a
	// remote node selected the UI proxies this call and that node must be able to
	// reveal ITS OWN secret.
	mux.Handle("POST /config/reveal", local(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req revealSecretRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		value, err := manager.Reveal(req.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": req.Path, "value": value})
	})))
	mux.Handle("GET /config/doctor", local(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.Doctor())
	})))
}
