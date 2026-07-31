package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/notes"
	coreproviders "github.com/tim5wang/godex/internal/core/providers"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	rtchannels "github.com/tim5wang/godex/internal/runtime/channels"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/usage"
	"github.com/tim5wang/godex/internal/tools"
	"github.com/tim5wang/godex/internal/version"
)

func NewHandler(
	manager *config.Manager,
	service *backend.Service,
	channels statusProvider,
	weixinAuth weixinAuthProvider,
	cronRuntime cronAutomationProvider,
	heartbeatRuntime heartbeatAutomationProvider,
	usageService *usage.Service,
) http.Handler {
	return NewHandlerWithRuntime(manager, service, channels, weixinAuth, cronRuntime, heartbeatRuntime, nil, usageService)
}

func NewHandlerWithRuntime(
	manager *config.Manager,
	service *backend.Service,
	channels statusProvider,
	weixinAuth weixinAuthProvider,
	cronRuntime cronAutomationProvider,
	heartbeatRuntime heartbeatAutomationProvider,
	serviceRuntime serviceRuntimeProvider,
	usageService *usage.Service,
	controlRegistries ...controlNodeRegistry,
) http.Handler {
	mux := http.NewServeMux()
	var controlRegistry controlNodeRegistry
	if len(controlRegistries) > 0 {
		controlRegistry = controlRegistries[0]
	}
	mux.Handle("GET /meta", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := manager.Current()
		writeJSON(w, http.StatusOK, metaResponse{
			LeadName:     cfg.LeadName,
			Model:        cfg.Model,
			WorkspaceDir: cfg.WorkspaceDir,
			AuthRequired: strings.TrimSpace(cfg.WebToken) != "",
			Version:      version.Current(),
		})
	}))
	protected := withBearerAuthProvider(func() string {
		return manager.Current().WebToken
	})
	mux.Handle("GET /config/meta", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.Meta())
	})))
	mux.Handle("GET /config/schema", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.Schema())
	})))
	mux.Handle("GET /config", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.View())
	})))
	mux.Handle("PUT /config", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req updateConfigRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
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
	mux.Handle("POST /config/reload", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		view, err := manager.ReloadFromDisk(r.Context())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("POST /config/reveal", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("GET /config/doctor", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.Doctor())
	})))
	mux.Handle("GET /runtime/service", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serviceRuntime == nil {
			writeJSON(w, http.StatusOK, map[string]any{"managed": false, "detail": "service runtime control is unavailable"})
			return
		}
		status, err := serviceRuntime.Status(r.Context())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})))
	mux.Handle("POST /runtime/service/restart", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serviceRuntime == nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("service runtime control is unavailable"))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "message": "Service restart requested."})
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = serviceRuntime.Restart(context.Background())
		}()
	})))
	mux.Handle("GET /control/nodes", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeJSON(w, http.StatusOK, []noderegistry.NodeView{})
			return
		}
		nodes, err := controlRegistry.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, nodes)
	})))
	mux.Handle("GET /control/nodes/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("control node registry is unavailable"))
			return
		}
		node, err := controlRegistry.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, node)
	})))
	mux.Handle("POST /control/nodes/register", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("control node registry is unavailable"))
			return
		}
		var input noderegistry.NodeInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		node, err := controlRegistry.Register(r.Context(), input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, node)
	})))
	mux.Handle("POST /control/nodes/{id}/heartbeat", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("control node registry is unavailable"))
			return
		}
		var input noderegistry.NodeInput
		if err := decodeJSONAllowEmpty(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		node, err := controlRegistry.Heartbeat(r.Context(), r.PathValue("id"), input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, node)
	})))
	mux.Handle("GET /providers", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, coreproviders.List(manager.Current()))
	})))
	mux.Handle("POST /providers/{id}/test", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := coreproviders.Test(r.Context(), manager.Current(), r.PathValue("id"))
		status := http.StatusOK
		if !result.OK {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, result)
	})))
	mux.Handle("POST /providers/{id}/models", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := coreproviders.DiscoverModels(r.Context(), manager.Current(), r.PathValue("id"))
		status := http.StatusOK
		if !result.OK {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, result)
	})))

	mux.Handle("GET /providers/import/codex", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imported, err := config.ImportCodexProviders("", "")
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, imported)
	})))
	mux.Handle("POST /providers/import/codex", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imported, err := config.ImportCodexProviders("", "")
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cfg := manager.Current()
		merged := make(map[string]llm.ProviderConfig, len(cfg.LLMProviders)+len(imported))
		for id, p := range cfg.LLMProviders {
			merged[id] = p
		}
		added := 0
		for _, p := range imported {
			targetID := "codex-" + p.ProviderID
			if _, exists := merged[targetID]; exists {
				continue
			}
			merged[targetID] = p.ProviderConfig
			added++
		}
		if added == 0 {
			writeError(w, http.StatusConflict, fmt.Errorf("all providers already exist"))
			return
		}
		if err := manager.UpdateProviders(merged); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"imported": added,
			"providers": coreproviders.List(manager.Current()),
		})
	})))

	mux.Handle("GET /channels", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if channels == nil {
			writeJSON(w, http.StatusOK, rtchannels.StatusReport{GeneratedAt: time.Now(), Channels: nil})
			return
		}
		writeJSON(w, http.StatusOK, channels.StatusReport())
	})))
	mux.Handle("POST /v1/chat/completions", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		// Otherwise use existing web-token-protected handler
		protected := withBearerAuthProvider(func() string {
			return manager.Current().WebToken
		})
		protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleOpenAIChatCompletions(w, r, service)
		})).ServeHTTP(w, r)
	}))

	// Anthropic Messages API endpoint
	// Supports two auth modes:
	// 1. Usage gateway: Authorization: Bearer gdx_xxx (proxy key auth)
	// 2. Usage gateway: x-api-key: gdx_xxx (Anthropic SDK default; some clients
	//    such as Pi send the proxy key in the x-api-key header instead of
	//    Authorization: Bearer, so we must accept it here).
	// 3. Web token: Authorization: Bearer <web_token> (for clients using ANTHROPIC_BASE_URL)
	mux.Handle("POST /v1/messages", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			handleAnthropicWebTokenMessages(w, r, usageService, manager)
			return
		}
		// Require Bearer auth
		writeError(w, http.StatusUnauthorized, fmt.Errorf("Invalid API Key. Please provide a valid proxy key with gdx_ prefix or use the configured web token."))
	}))

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
				"id":      m.PublicModel,
				"object":  "model",
				"created": time.Now().Unix(),
				"owned_by": "godex",
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"object": "list",
			"data":   openAIModels,
		})
	}))

	mux.Handle("GET /models", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		view, err := service.Models(r.Context(), strings.TrimSpace(r.URL.Query().Get("session_id")))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("GET /security/summary", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		summary, err := service.SecuritySummary(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, summary)
	})))
	mux.Handle("GET /security/audit", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid audit limit"))
				return
			}
			limit = parsed
		}
		items, err := service.SecurityAudit(r.Context(), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /packages", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListPackages(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /packages/quality", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		report, err := service.PackageQuality(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, report)
	})))
	mux.Handle("POST /packages/install", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req installPackageRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		item, err := service.InstallPackage(r.Context(), strings.TrimSpace(req.Source))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /packages/remove", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req removePackageRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		item, err := service.RemovePackage(r.Context(), strings.TrimSpace(req.Name))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /packages/{name}/reinstall", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.ReinstallPackage(r.Context(), strings.TrimSpace(r.PathValue("name")))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /packages/{name}/smoke/{smoke}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req packageSmokeRunRequest
		if r.Body != nil && r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		run, err := service.RunPackageSmoke(
			r.Context(),
			strings.TrimSpace(r.PathValue("name")),
			strings.TrimSpace(r.PathValue("smoke")),
			strings.TrimSpace(req.SessionID),
		)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})))
	mux.Handle("GET /prompts", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		includeContent := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_content")), "true")
		items, err := service.ListPrompts(r.Context(), includeContent)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /packages/commands", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		includeContent := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_content")), "true")
		items, err := service.ListPackageCommands(r.Context(), includeContent)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /packages/roles", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		includeContent := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_content")), "true")
		items, err := service.ListPackageRoles(r.Context(), includeContent)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /channels/weixin/auth", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if weixinAuth == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("weixin web auth unavailable"))
			return
		}
		status, err := weixinAuth.Status(r.Context(), strings.TrimSpace(r.URL.Query().Get("account_id")))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})))
	mux.Handle("POST /channels/weixin/auth/start", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if weixinAuth == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("weixin web auth unavailable"))
			return
		}
		var req accountRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		status, err := weixinAuth.Start(r.Context(), req.AccountID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})))
	mux.Handle("POST /channels/weixin/auth/logout", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if weixinAuth == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("weixin web auth unavailable"))
			return
		}
		var req accountRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		status, err := weixinAuth.Logout(r.Context(), req.AccountID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})))
	mux.Handle("GET /automation/cron/jobs", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		jobs, err := cronRuntime.ListJobs()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, jobs)
	})))
	mux.Handle("POST /automation/cron/jobs", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		var input automation.CronCreateInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		input.CreatedBy = "web"
		input.CreatedFromSession = "web-ui"
		job, err := cronRuntime.CreateJob(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})))
	mux.Handle("GET /automation/cron/jobs/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		job, err := cronRuntime.GetJob(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})))
	mux.Handle("PATCH /automation/cron/jobs/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		var input automation.CronUpdateInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		input.ID = r.PathValue("id")
		job, err := cronRuntime.UpdateJob(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})))
	mux.Handle("DELETE /automation/cron/jobs/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		if err := cronRuntime.DeleteJob(r.PathValue("id")); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("POST /automation/cron/jobs/{id}/run", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		run, err := cronRuntime.RunNow(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})))
	mux.Handle("GET /automation/cron/jobs/{id}/runs", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		runs, err := cronRuntime.ListRunLogs(r.PathValue("id"), 20)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, runs)
	})))
	mux.Handle("GET /automation/heartbeat", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if heartbeatRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("heartbeat automation unavailable"))
			return
		}
		rule, err := heartbeatRuntime.GetRule()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, rule)
	})))
	mux.Handle("PUT /automation/heartbeat", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if heartbeatRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("heartbeat automation unavailable"))
			return
		}
		var input automation.HeartbeatSetInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		input.CreatedBy = "web"
		input.CreatedFromSession = "web-ui"
		rule, err := heartbeatRuntime.SetRule(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, rule)
	})))
	mux.Handle("POST /automation/heartbeat/test", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if heartbeatRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("heartbeat automation unavailable"))
			return
		}
		run, err := heartbeatRuntime.TestNow(r.Context())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})))
	mux.Handle("GET /automation/heartbeat/logs", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if heartbeatRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("heartbeat automation unavailable"))
			return
		}
		runs, err := heartbeatRuntime.ListRunLogs(20)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, runs)
	})))
	mux.Handle("GET /memory", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opts := memory.SearchOptions{
			Query:  strings.TrimSpace(r.URL.Query().Get("q")),
			Type:   memory.Type(strings.TrimSpace(r.URL.Query().Get("memory_type"))),
			Tag:    strings.TrimSpace(r.URL.Query().Get("tag")),
			Source: strings.TrimSpace(r.URL.Query().Get("source")),
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid memory limit"))
				return
			}
			opts.Limit = parsed
		}
		items, err := service.ListMemory(r.Context(), opts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /memory/candidates", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListMemoryCandidates(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /memory/audit", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid memory audit limit"))
				return
			}
			limit = parsed
		}
		items, err := service.ListMemoryAudit(r.Context(), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("POST /memory/digest", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.DigestMemory(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /memory/mine/project", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.MineProjectMemoryCandidates(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /memory/suppressions", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListMemorySuppressions(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /memory/context", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		layers, err := service.PreviewMemoryContext(r.Context(), strings.TrimSpace(r.URL.Query().Get("q")))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, layers)
	})))
	mux.Handle("POST /memory/remember", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rememberMemoryRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := service.RememberMemory(r.Context(), memory.SaveInput{
			Title:   req.Title,
			Summary: req.Summary,
			Content: req.Content,
			Type:    memory.Type(req.MemoryType),
			Source:  req.Source,
			Tags:    append([]string{}, req.Tags...),
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	})))
	mux.Handle("POST /memory/update", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req updateMemoryRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := service.UpdateMemory(r.Context(), memory.UpdateInput{
			Match: memory.ForgetInput{
				Title: strings.TrimSpace(req.MatchTitle),
				File:  strings.TrimSpace(req.MatchFile),
			},
			Title:   req.Title,
			Summary: req.Summary,
			Content: req.Content,
			Type:    memory.Type(req.MemoryType),
			Source:  req.Source,
			Tags:    append([]string{}, req.Tags...),
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	})))
	mux.Handle("POST /memory/forget", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req forgetMemoryRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := service.ForgetMemory(r.Context(), memory.ForgetInput{
			Title: strings.TrimSpace(req.Title),
			File:  strings.TrimSpace(req.File),
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	})))
	mux.Handle("POST /memory/candidates/{fingerprint}/accept", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req acceptMemoryCandidateRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := service.AcceptMemoryCandidate(r.Context(), memory.AcceptCandidateInput{
			Fingerprint:   r.PathValue("fingerprint"),
			AlwaysInclude: req.AlwaysInclude,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	})))
	mux.Handle("POST /memory/candidates/{fingerprint}/dismiss", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		candidate, err := service.DismissMemoryCandidate(r.Context(), r.PathValue("fingerprint"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, candidate)
	})))
	mux.Handle("POST /memory/audit/{id}/restore", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req restoreMemoryAuditRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := service.RestoreMemoryAudit(r.Context(), r.PathValue("id"), req.Target)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	})))
	mux.Handle("GET /notes", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListNotes(r.Context(), notes.SearchOptions{
			Query: strings.TrimSpace(r.URL.Query().Get("q")),
			Tag:   strings.TrimSpace(r.URL.Query().Get("tag")),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /notes/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.GetNote(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /notes", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req saveNoteRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		item, err := service.SaveNote(r.Context(), notes.SaveInput{
			ID:      req.ID,
			Title:   req.Title,
			Summary: req.Summary,
			Tags:    append([]string{}, req.Tags...),
			Content: req.Content,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("DELETE /notes/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.DeleteNote(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /sessions", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openSessionRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		opened, err := service.OpenSession(r.Context(), req.Locator)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, opened)
	})))
	mux.Handle("GET /sessions", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := service.ListSessions(r.Context(), backend.SessionListFilter{
			Channel: strings.TrimSpace(r.URL.Query().Get("channel")),
		})
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})))
	mux.Handle("DELETE /sessions/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := service.DeleteSession(r.Context(), r.PathValue("id")); err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("POST /sessions/{id}/fork", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req forkSessionRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		opened, err := service.ForkSession(r.Context(), r.PathValue("id"), backend.ForkRequest{
			TurnID:       strings.TrimSpace(req.TurnID),
			MessageIndex: req.MessageIndex,
			Title:        strings.TrimSpace(req.Title),
		})
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, opened)
	})))
	mux.Handle("POST /sessions/{id}/model", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req setSessionModelRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		view, err := service.SetSessionModelProfileWithReasoning(r.Context(), r.PathValue("id"), strings.TrimSpace(req.ProfileID), strings.TrimSpace(req.ReasoningEffort))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("GET /sessions/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := service.Snapshot(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	})))
	mux.Handle("GET /sessions/{id}/context-inspector", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inspector, err := service.ContextInspector(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, inspector)
	})))
	mux.Handle("GET /sessions/{id}/ledger", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ledger, err := service.ProjectLedger(r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, ledger)
	})))
	mux.Handle("POST /sessions/{id}/ledger", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req updateProjectLedgerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ledger, err := service.UpdateProjectLedger(r.PathValue("id"), backend.ProjectLedgerPatch{
			Goal:         req.Goal,
			CurrentPhase: req.CurrentPhase,
			ChangedFiles: req.ChangedFiles,
			Validation:   req.Validation,
			Decisions:    req.Decisions,
			Risks:        req.Risks,
			Blockers:     req.Blockers,
			NextSteps:    req.NextSteps,
		})
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, ledger)
	})))
	mux.Handle("GET /sessions/{id}/timeline", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid timeline limit"))
				return
			}
			limit = parsed
		}
		items, err := service.Timeline(r.Context(), r.PathValue("id"), limit)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/timeline/page", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		limit := 50
		if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid timeline limit"))
				return
			}
			limit = parsed
		}
		if raw := strings.TrimSpace(query.Get("cursor")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid timeline cursor"))
				return
			}
		}
		var types []string
		for _, typ := range strings.Split(query.Get("type"), ",") {
			if typ = strings.TrimSpace(typ); typ != "" {
				types = append(types, typ)
			}
		}
		page, err := service.TimelinePage(r.Context(), r.PathValue("id"), backend.TimelinePageRequest{
			Limit:  limit,
			Cursor: strings.TrimSpace(query.Get("cursor")),
			Types:  types,
			Query:  strings.TrimSpace(query.Get("q")),
			JobID:  strings.TrimSpace(query.Get("job_id")),
			TurnID: strings.TrimSpace(query.Get("turn_id")),
		})
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})))
	mux.Handle("GET /sessions/{id}/subagents", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListSubagents(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/subagents/{jobID}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.GetSubagent(r.Context(), r.PathValue("id"), r.PathValue("jobID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("GET /sessions/{id}/subagents/{jobID}/review", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		review, err := service.ReviewSubagent(r.Context(), r.PathValue("id"), r.PathValue("jobID"))
		if err != nil {
			writeError(w, statusForSubagentActionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, review)
	})))
	mux.Handle("POST /sessions/{id}/subagents/{jobID}/cancel", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.CancelSubagent(r.Context(), r.PathValue("id"), r.PathValue("jobID"))
		if err != nil {
			writeError(w, statusForSubagentActionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /sessions/{id}/subagents/{jobID}/resume", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.ResumeSubagent(r.Context(), r.PathValue("id"), r.PathValue("jobID"))
		if err != nil {
			writeError(w, statusForSubagentActionError(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, item)
	})))
	mux.Handle("POST /sessions/{id}/subagents/{jobID}/merge", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.MergeSubagent(r.Context(), r.PathValue("id"), r.PathValue("jobID"))
		if err != nil {
			writeError(w, statusForSubagentActionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("GET /sessions/{id}/longtasks", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListLongTasks(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("POST /sessions/{id}/longtasks", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.LongTaskArgs
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		view, err := service.CreateLongTask(r.Context(), r.PathValue("id"), req)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusCreated, view)
	})))
	mux.Handle("GET /sessions/{id}/longtasks/{workflowID}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		view, err := service.GetLongTask(r.Context(), r.PathValue("id"), r.PathValue("workflowID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("POST /sessions/{id}/longtasks/{workflowID}/run", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.LongTaskArgs
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		view, err := service.RunLongTask(r.Context(), r.PathValue("id"), r.PathValue("workflowID"), req)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("POST /sessions/{id}/longtasks/{workflowID}/cancel", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req longTaskNodeRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var (
			view agent.LongTaskView
			err  error
		)
		if req.CancelAll {
			view, err = service.CancelLongTaskAll(r.Context(), r.PathValue("id"), r.PathValue("workflowID"))
		} else {
			view, err = service.CancelLongTask(r.Context(), r.PathValue("id"), r.PathValue("workflowID"), req.NodeID)
		}
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("POST /sessions/{id}/longtasks/{workflowID}/finalize", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req longTaskNodeRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		view, err := service.FinalizeLongTaskStory(r.Context(), r.PathValue("id"), r.PathValue("workflowID"), req.NodeID)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	// T12: commit-hash reverse lookup.
	mux.Handle("POST /sessions/{id}/longtasks/{workflowID}/lookup", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req longTaskLookupRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Commit) == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("missing commit"))
			return
		}
		out, err := service.LookupLongTask(r.Context(), r.PathValue("id"), req.Commit, r.PathValue("workflowID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})))
	// T12: rollback. Empty reason is allowed; >1024 bytes is rejected.
	mux.Handle("POST /sessions/{id}/longtasks/{workflowID}/rollback", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req longTaskRollbackRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if len(req.Reason) > 1024 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("rollback reason exceeds 1024 bytes (got %d)", len(req.Reason)))
			return
		}
		result, err := service.RollbackLongTaskStory(r.Context(), r.PathValue("id"), r.PathValue("workflowID"), req.NodeID, req.Reason)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	// T12: explicit lazy GC. Default dry-run; older_than_seconds=0
	// means permanent retention (T12 default).
	mux.Handle("POST /sessions/{id}/longtasks/{workflowID}/gc", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req longTaskGCRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.GCLongTaskArtifacts(r.Context(), r.PathValue("id"), r.PathValue("workflowID"), req.OlderThanSeconds, req.Apply)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("GET /sessions/{id}/permissions", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.PendingPermissions(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("POST /sessions/{id}/permissions/{requestID}/approve", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req permissionApproveRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		scope := tools.PermissionGrantScope(strings.TrimSpace(req.Scope))
		resolution, err := service.ApprovePermission(r.Context(), r.PathValue("id"), r.PathValue("requestID"), scope)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, resolution)
	})))
	mux.Handle("POST /sessions/{id}/permissions/{requestID}/deny", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req permissionDenyRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resolution, err := service.DenyPermission(r.Context(), r.PathValue("id"), r.PathValue("requestID"), req.Reason)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, resolution)
	})))
	mux.Handle("GET /sessions/{id}/skills/catalog", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListSessionSkills(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/skills/sources", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		mode := strings.TrimSpace(r.URL.Query().Get("mode"))
		var (
			items []tools.SkillSourceEntry
			err   error
		)
		if query != "" {
			items, err = service.SearchSessionSkillSources(r.Context(), r.PathValue("id"), query)
		} else if mode == "trending" {
			items, err = service.ListTrendingSessionSkillSources(r.Context(), r.PathValue("id"))
		} else {
			items, err = service.ListSessionSkillSources(r.Context(), r.PathValue("id"))
		}
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/skills/active", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ActiveSessionSkills(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/skills/{name}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.GetSessionSkill(r.Context(), r.PathValue("id"), r.PathValue("name"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /sessions/{id}/skills/install", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req skillInstallRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.InstallSessionSkill(r.Context(), r.PathValue("id"), strings.TrimSpace(req.Source), strings.TrimSpace(req.Name))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/skills/normalize", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req skillLoadRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.NormalizeSessionSkill(r.Context(), r.PathValue("id"), strings.TrimSpace(req.Name))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("DELETE /sessions/{id}/skills/{name}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.RemoveSessionSkill(r.Context(), r.PathValue("id"), r.PathValue("name"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/skills/load", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req skillLoadRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.ActivateSessionSkill(r.Context(), r.PathValue("id"), strings.TrimSpace(req.Name))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/skills/expand", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req skillExpandRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.ExpandSessionSkill(r.Context(), r.PathValue("id"), strings.TrimSpace(req.Name), append([]string{}, req.Sections...))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/skills/unload", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req skillLoadRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.UnloadSessionSkill(r.Context(), r.PathValue("id"), strings.TrimSpace(req.Name))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/messages", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req submitMessageRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		envelope := req.Envelope
		if strings.TrimSpace(envelope.Text) == "" && strings.TrimSpace(req.Text) != "" {
			envelope = message.NewRuntimeEnvelope(message.SourceGateway, r.PathValue("id"), req.Sender, req.Text, time.Now(), nil)
		}
		if envelope.Source == "" {
			envelope.Source = message.SourceGateway
		}
		if strings.TrimSpace(envelope.BodyText()) == "" && len(envelope.Attachments) == 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("message text or attachments required"))
			return
		}
		var (
			result *backend.SubmitResult
			err    error
		)
		if queueMode := strings.TrimSpace(req.QueueMode); queueMode != "" {
			result, err = service.SubmitAsync(r.Context(), r.PathValue("id"), envelope, backend.SubmitOptions{QueueMode: backend.QueueMode(queueMode)})
		} else {
			result, err = service.SubmitAsync(r.Context(), r.PathValue("id"), envelope)
		}
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	})))
	mux.Handle("POST /sessions/{id}/turns/{turnID}/cancel", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.CancelTurn(r.Context(), r.PathValue("id"), r.PathValue("turnID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/turns/{turnID}/retry", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.RetryTurnAsync(r.Context(), r.PathValue("id"), r.PathValue("turnID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	})))
	mux.Handle("POST /sessions/{id}/turns/{turnID}/resume", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.ResumeTurnAsync(r.Context(), r.PathValue("id"), r.PathValue("turnID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	})))
	mux.Handle("POST /sessions/{id}/attachments", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, backend.MaxAttachmentUploadBytes()+(1<<20))
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		if r.MultipartForm == nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("no multipart files uploaded"))
			return
		}

		var uploaded []message.AttachmentRef
		for _, files := range r.MultipartForm.File {
			for _, header := range files {
				file, err := header.Open()
				if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
				attachment, err := service.StoreAttachment(r.Context(), r.PathValue("id"), backend.AttachmentUpload{
					Name:     header.Filename,
					MIMEType: header.Header.Get("Content-Type"),
					Reader:   file,
				})
				closeErr := file.Close()
				if err != nil {
					writeError(w, statusForSessionError(err), err)
					return
				}
				if closeErr != nil {
					writeError(w, http.StatusInternalServerError, closeErr)
					return
				}
				uploaded = append(uploaded, attachment)
			}
		}
		if len(uploaded) == 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("no files uploaded"))
			return
		}
		writeJSON(w, http.StatusOK, attachmentListResponse{Attachments: uploaded})
	})))
	mux.Handle("GET /sessions/{id}/attachments/{attachmentID}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attachment, absolutePath, err := service.ResolveAttachment(r.PathValue("id"), r.PathValue("attachmentID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		if attachment.MIMEType != "" {
			w.Header().Set("Content-Type", attachment.MIMEType)
		}
		if attachment.Name != "" {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": attachment.Name}))
		}
		http.ServeFile(w, r, absolutePath)
	})))
	mux.Handle("POST /sessions/{id}/commands", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req commandRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cmd, err := normalizeCommandRequest(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.ExecuteCommand(r.Context(), r.PathValue("id"), cmd)
		if err != nil && !errors.Is(err, commands.ErrUnknownCommand) {
			writeError(w, statusForSessionError(err), err)
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"result": result,
				"error":  err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("GET /sessions/{id}/events", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		_, _ = fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()

		ctx := r.Context()
		eventCh := make(chan events.Event, 16)
		var subscribeOnce sync.Once
		go func() {
			replay := backend.EventReplayOptions{}
			if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("replay")), "active") {
				replay.ActiveOnly = true
			}
			err := service.SubscribeReplay(ctx, r.PathValue("id"), events.SinkFunc(func(event events.Event) {
				select {
				case <-ctx.Done():
				case eventCh <- event:
				}
			}), replay)
			subscribeOnce.Do(func() {
				close(eventCh)
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				subscribeOnce.Do(func() {
					close(eventCh)
				})
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-eventCh:
				if !ok {
					return
				}
				data, marshalErr := json.Marshal(event)
				if marshalErr != nil {
					continue
				}
				_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	})))
	registerUsageRoutes(mux, protected, usageService, manager)
	registerFileRoutes(mux, protected, manager)
	registerTerminalRoutes(mux)
	return mux
}

// handleAnthropicWebTokenMessages handles POST /v1/messages requests with web token auth.
// This is used by clients like pi that use ANTHROPIC_BASE_URL pointing to godex.
//
// Unlike the previous implementation, which routed the request to the
// godex AI agent backend (a different product surface that does not
// support streaming, tool calls, or extended thinking), this version
// treats the web token as an admin-level identity and dispatches the
// request through the same Anthropic LLM gateway the proxy-key path
// uses. The admin is implicitly allowed to invoke any model mapping,
// and the budget is treated as unlimited (BudgetCredits == 0 maps to
// the "unlimited" branch in usage.Service.CheckBudget). This makes
// the web-token path a drop-in for Pi and other Anthropic SDK
// clients that configure the web token instead of a gdx_ proxy key.
func handleAnthropicWebTokenMessages(w http.ResponseWriter, r *http.Request, usageService *usage.Service, manager *config.Manager) {
	// Synthesise a virtual "admin" key. We never persist this row;
	// the dispatcher only reads AllowedModels / BudgetCredits /
	// Enabled, and the absence of those fields means "use defaults".
	adminKey := &usage.ProxyAPIKey{
		ID:        "system:web_token",
		Name:      "Web Token Admin",
		KeyPrefix: "web_token",
		Enabled:   true,
		// AllowedModels is nil: the dispatcher treats nil as
		// "allow every model".
		// BudgetCredits is 0: CheckBudget treats 0 as
		// unlimited (the existing contract; see
		// service.CheckBudget in services/usage/service.go).
	}
	dispatchAnthropicMessages(w, r, usageService, manager, adminKey)
}

// anthropicMessagesToText converts Anthropic message format to plain text.
// Retained because the legacy agent-backend path used it; the LLM
// gateway path doesn't need a string projection, but we keep the
// helper exported for the few call sites that still want to dump an
// Anthropic conversation to a single string (e.g. the route-time
// debug log). The conversion is deliberately lossy — it concatenates
// text blocks and drops images / tool_use / tool_result / thinking —
// because the only consumer is the agent backend's text-only prompt.
func anthropicMessagesToText(messages []anthropicMessage) string {
	var parts []string
	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// collectAnthropicResponse waits for the agent response.
func collectAnthropicResponse(ctx context.Context, service *backend.Service, sessionID, turnID string) (string, error) {
	eventCh := make(chan events.Event, 128)
	unsubscribe, err := service.AttachSink(sessionID, events.SinkFunc(func(event events.Event) {
		select {
		case <-ctx.Done():
		case eventCh <- event:
		default:
		}
	}))
	if err != nil {
		return "", err
	}
	defer unsubscribe()

	var builder strings.Builder
	for {
		select {
		case <-ctx.Done():
			return builder.String(), ctx.Err()
		case event := <-eventCh:
			if event.TurnID != turnID {
				continue
			}
			switch event.Type {
			case events.EventAssistantTextDelta:
				if payload, ok := event.Payload.(events.TextPayload); ok {
					builder.WriteString(payload.Text)
				}
			case events.EventErrorRaised:
				if payload, ok := event.Payload.(events.NoticePayload); ok {
					return builder.String(), fmt.Errorf("error: %s", payload.Message)
				}
			case events.EventTurnCompleted:
				return builder.String(), nil
			}
		}
	}
}

// ---- Usage Gateway Chat Completions ----
func withBearerAuthProvider(token func() string) func(http.Handler) http.Handler {
	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.TrimSpace(token()) == "" {
				handler.ServeHTTP(w, r)
				return
			}
			if !bearerAuthorized(r, token()) {
				writeError(w, http.StatusUnauthorized, fmt.Errorf("missing or invalid bearer token"))
				return
			}
			handler.ServeHTTP(w, r)
		})
	}
}

// extractProxyKeySecret returns the presented proxy key from whichever
// header the client used, or "" if neither header carries a non-empty
// value. It accepts both Authorization: Bearer <secret> (OpenAI-style
// clients) and x-api-key: <secret> (Anthropic SDK style; required for
// clients such as Pi that send the proxy key without the Bearer prefix).
func extractProxyKeySecret(r *http.Request) string {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		// Strip optional "Bearer " (case-insensitive) and surrounding whitespace.
		lower := strings.ToLower(auth)
		if strings.HasPrefix(lower, "bearer ") {
			return strings.TrimSpace(auth[len("bearer "):])
		}
		return auth
	}
	if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
		return key
	}
	return ""
}

func bearerAuthorized(r *http.Request, token string) bool {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return false
	}
	return strings.TrimSpace(header[len("Bearer "):]) == token
}

func decodeJSON(r *http.Request, dest interface{}) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	if r.ContentLength == 0 {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(dest)
}

func decodeJSONAllowEmpty(r *http.Request, dest interface{}) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	if r.ContentLength == 0 {
		return nil
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

func normalizeCommandRequest(req commandRequest) (commands.Command, error) {
	if strings.TrimSpace(req.Command) != "" {
		if cmd, ok := commands.Parse(req.Command); ok {
			cmd.Metadata = cloneStringMap(req.Metadata)
			return cmd, nil
		}
		return commands.Command{}, fmt.Errorf("invalid command: %s", req.Command)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return commands.Command{}, fmt.Errorf("missing command")
	}
	return commands.Command{
		Name:     strings.ToLower(strings.TrimPrefix(name, "/")),
		Args:     append([]string{}, req.Args...),
		Raw:      "/" + strings.ToLower(strings.TrimPrefix(name, "/")),
		Metadata: cloneStringMap(req.Metadata),
	}, nil
}

func cloneStringMap(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func statusForSessionError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if errors.Is(err, backend.ErrSessionNotFound) || errors.Is(err, backend.ErrAttachmentNotFound) || errors.Is(err, agent.ErrDurableSubagentNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, backend.ErrTurnNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, backend.ErrTurnNotRetryable) {
		return http.StatusConflict
	}
	if errors.Is(err, backend.ErrTurnNotResumable) {
		return http.StatusConflict
	}
	if errors.Is(err, backend.ErrSessionBusy) {
		return http.StatusConflict
	}
	if errors.Is(err, backend.ErrSessionCorrupt) {
		return http.StatusConflict
	}
	if errors.Is(err, skill.ErrSkillNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, skill.ErrSkillInvalidRequest) {
		return http.StatusBadRequest
	}
	if errors.Is(err, skill.ErrSkillConflict) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func statusForSubagentActionError(err error) int {
	if errors.Is(err, backend.ErrSessionNotFound) || errors.Is(err, agent.ErrDurableSubagentNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// registerTerminalRoutes adds v2.0 terminal endpoints to the mux.
// These endpoints are unprotected (no web-token check) because the
// terminal is a local development tool spawned inside the same process.
func registerTerminalRoutes(mux *http.ServeMux) {
	tm := globalTerminalManager
	mux.HandleFunc("POST /v1/terminal/create", tm.handleCreateTerminal)
	mux.HandleFunc("GET /v1/terminal/{id}/output", tm.handleTerminalOutput)
	mux.HandleFunc("POST /v1/terminal/{id}/input", tm.handleTerminalInput)
	mux.HandleFunc("POST /v1/terminal/{id}/resize", tm.handleTerminalResize)
	mux.HandleFunc("DELETE /v1/terminal/{id}", tm.handleTerminalDelete)
}
