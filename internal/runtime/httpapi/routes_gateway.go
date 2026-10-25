package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/localbash"
	"github.com/tim5wang/godex/internal/services/usage"
)

func registerGatewayRoutes(
	mux *http.ServeMux,
	manager *config.Manager,
	service *backend.Service,
	usageService *usage.Service,
	protected func(http.Handler) http.Handler,
) {
	mux.Handle("POST /v1/chat/completions", gunzipBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if this is a usage gateway request (proxy key auth)
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer gdx_") {
			if usageService != nil {
				handleUsageGatewayChatCompletions(w, r, usageService, manager)
			} else {
				writeError(w, http.StatusServiceUnavailable, fmt.Errorf("usage gateway not configured"))
			}
			return
		}
		// Otherwise use existing web-token-protected handler.
		//
		// Principal is attached on this branch too (P0 §7, gateway dual auth):
		// the bare bearer middleware let the caller through without recording who
		// they are, so this path was invisible to identity-based decisions.
		//
		// Gated as well: unlike the two pure LLM-gateway endpoints, this branch
		// starts a real agent session (resolve session → run turn), which is the
		// capability the operator restriction exists for. Relay-originated calls
		// still pass via the node-actor exemption.
		webTokenAuth := withPrincipalAuth(withBearerAuthProvider(func() string {
			return manager.Current().WebToken
		}, relayTrustChecker(manager)), principalForRequest(manager, relayTrustChecker(manager), nodeProxyTokenChecker(manager), nil))
		requireCenterExecution(webTokenAuth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleOpenAIChatCompletions(w, r, service)
		})).ServeHTTP(w, r)
	})))

	mux.Handle("POST /v1/responses", gunzipBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if this is a usage gateway request (proxy key auth)
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer gdx_") {
			if usageService != nil {
				handleUsageGatewayResponses(w, r, usageService, manager)
			} else {
				writeError(w, http.StatusServiceUnavailable, fmt.Errorf("usage gateway not configured"))
			}
			return
		}
		// Web-token auth: dispatch through the same LLM gateway so Responses
		// SDK clients see the full streaming + tools experience.
		//
		// Identity is attached (P0 §7): this is a pure LLM-gateway dispatch, so it
		// stays ungated, but the caller must no longer be anonymous.
		webToken := manager.Current().WebToken
		if webToken != "" && bearerAuthorized(r, webToken) {
			r = r.WithContext(WithPrincipal(r.Context(), operatorPrincipal()))
			handleResponsesWebToken(w, r, usageService, manager)
			return
		}
		writeError(w, http.StatusUnauthorized, fmt.Errorf("unauthorized"))
	})))

	// Anthropic Messages API endpoint
	// Supports two auth modes:
	// 1. Usage gateway: Authorization: Bearer gdx_xxx (proxy key auth)
	// 2. Usage gateway: x-api-key: gdx_xxx (Anthropic SDK default; some clients
	//    such as Pi send the proxy key in the x-api-key header instead of
	//    Authorization: Bearer, so we must accept it here).
	// 3. Web token: Authorization: Bearer <web_token> (for clients using ANTHROPIC_BASE_URL)
	mux.Handle("POST /v1/messages", gunzipBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if secret := extractProxyKeySecret(r); secret != "" && strings.HasPrefix(secret, "gdx_") {
			// Usage gateway auth
			if usageService != nil {
				handleAnthropicGatewayMessages(w, r, usageService, manager, secret)
			} else {
				writeError(w, http.StatusServiceUnavailable, fmt.Errorf("usage gateway not configured"))
			}
			return
		}
		// Try web-token auth (for pi and other clients using ANTHROPIC_BASE_URL)
		webToken := manager.Current().WebToken
		if webToken != "" && bearerAuthorized(r, webToken) {
			// Web-token auth: the caller is the admin (or anyone
			// who knows the web token). Dispatch through the same
			// LLM gateway the proxy-key path uses so Pi and other
			// Anthropic SDK clients see the full streaming + tools
			// experience. The previous implementation routed to the
			// godex agent backend, which doesn't support streaming
			// or tool calls and never worked for Pi.
			//
			// Identity is attached (P0 §7): a pure LLM-gateway dispatch, so it
			// stays ungated, but the caller must not be anonymous.
			r = r.WithContext(WithPrincipal(r.Context(), operatorPrincipal()))
			handleAnthropicWebTokenMessages(w, r, usageService, manager)
			return
		}
		// Require Bearer auth
		writeError(w, http.StatusUnauthorized, fmt.Errorf("Invalid API Key. Please provide a valid proxy key with gdx_ prefix or use the configured web token."))
	})))

	// POST /v1/exec - Run a shell command on this node and stream its output
	// as SSE events ({output, final, exit_code}). Used by the center-side
	// "godex node exec" jump-host command through the relay proxy.
	//
	// Center-execution gated: executing here is exactly the capability that is
	// restricted to the operator. A relay-originated call still passes (node
	// actor exemption) — that IS the jump-host path this endpoint exists for.
	mux.Handle("POST /v1/exec", requireCenterExecution(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Command      string `json:"command"`
			WorkspaceDir string `json:"workspace_dir"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Command) == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("missing command"))
			return
		}
		workspaceDir := strings.TrimSpace(req.WorkspaceDir)
		if workspaceDir == "" {
			workspaceDir = manager.Current().WorkspaceDir
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		chunks := localbash.RunBash(r.Context(), workspaceDir, req.Command)
		for chunk := range chunks {
			data, _ := json.Marshal(map[string]any{
				"output":    chunk.Output,
				"final":     chunk.Final,
				"exit_code": chunk.ExitCode,
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	})))

	// GET /v1/models - List available models (OpenAI-compatible format)
	// Requires gdx_ API key authentication
	mux.Handle("GET /v1/models", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(strings.ToLower(auth), "bearer gdx_") || usageService == nil {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("Invalid API Key."))
			return
		}
		models, err := usageService.ListModels()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// Convert to OpenAI models list format
		var openAIModels []map[string]interface{}
		for _, m := range models {
			openAIModels = append(openAIModels, map[string]interface{}{
				"id":       m.PublicModel,
				"object":   "model",
				"created":  time.Now().Unix(),
				"owned_by": "godex",
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"object": "list",
			"data":   openAIModels,
		})
	}))

}
