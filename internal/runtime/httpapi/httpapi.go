package httpapi

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llmcapture"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
	"github.com/tim5wang/godex/internal/services/usage"
	"github.com/tim5wang/godex/internal/version"
)

// Dependencies is the explicit composition boundary for the HTTP runtime.
// Route packages still receive their narrow provider interfaces; this object
// prevents the process entrypoint from depending on constructor argument order.
type Dependencies struct {
	Config          *config.Manager
	Backend         *backend.Service
	Channels        statusProvider
	WeixinAuth      weixinAuthProvider
	Cron            cronAutomationProvider
	Heartbeat       heartbeatAutomationProvider
	ServiceRuntime  serviceRuntimeProvider
	Usage           *usage.Service
	ControlRegistry controlNodeRegistry
	// LlmCapture optionally injects the LLM request/response capture sink. When
	// nil, NewHandlerWithDependencies creates one under the config StateDir so
	// the /llm-capture endpoints always work.
	LlmCapture *llmcapture.Capture
}

func NewHandler(
	manager *config.Manager,
	service *backend.Service,
	channels statusProvider,
	weixinAuth weixinAuthProvider,
	cronRuntime cronAutomationProvider,
	heartbeatRuntime heartbeatAutomationProvider,
	usageService *usage.Service,
) http.Handler {
	return NewHandlerWithDependencies(Dependencies{
		Config: manager, Backend: service, Channels: channels, WeixinAuth: weixinAuth,
		Cron: cronRuntime, Heartbeat: heartbeatRuntime, Usage: usageService,
	})
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
	var controlRegistry controlNodeRegistry
	if len(controlRegistries) > 0 {
		controlRegistry = controlRegistries[0]
	}
	return NewHandlerWithDependencies(Dependencies{
		Config: manager, Backend: service, Channels: channels, WeixinAuth: weixinAuth,
		Cron: cronRuntime, Heartbeat: heartbeatRuntime, ServiceRuntime: serviceRuntime,
		Usage: usageService, ControlRegistry: controlRegistry,
	})
}

// NewHandlerWithDependencies builds the API surface from named runtime dependencies.
func NewHandlerWithDependencies(deps Dependencies) http.Handler {
	manager := deps.Config
	service := deps.Backend
	channels := deps.Channels
	weixinAuth := deps.WeixinAuth
	cronRuntime := deps.Cron
	heartbeatRuntime := deps.Heartbeat
	serviceRuntime := deps.ServiceRuntime
	usageService := deps.Usage
	mux := http.NewServeMux()
	// Plugin-contributed HTTP surfaces (P-A): mount every registered plugin
	// prefix; plugins activated later are mounted automatically via the
	// manager's route root.
	if service != nil {
		if pm := service.PluginManager(); pm != nil {
			pm.MountRoutes(mux)
		}
	}
	controlRegistry := deps.ControlRegistry
	// The registry object may also carry an observation store (relay.EventStore)
	// that serves aggregated node overviews; detect it by type assertion.
	var overviewProvider nodeOverviewProvider
	if deps.ControlRegistry != nil {
		if provider, ok := deps.ControlRegistry.(nodeOverviewProvider); ok {
			overviewProvider = provider
		}
	}
	mux.Handle("GET /meta", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := manager.Current()
		exec := cfg.Tools.Execution
		writeJSON(w, http.StatusOK, metaResponse{
			LeadName:      cfg.LeadName,
			Model:         cfg.Model,
			WorkspaceDir:  cfg.WorkspaceDir,
			AuthRequired:  strings.TrimSpace(cfg.WebToken) != "",
			Version:       version.Current(),
			ExecutionMode: exec.Mode,
			SSHTarget:     exec.SSHTarget,
			SSHWorkspace:  exec.SSHWorkspace,
			SSHOptions:    exec.SSHOptions,
			DockerImage:   exec.DockerImage,
			DockerNetwork: exec.DockerNetwork,
			VoiceEnabled:  cfg.Media.Audio.VoiceEnabled,
		})
	}))
	protected := withBearerAuthProvider(func() string {
		return manager.Current().WebToken
	}, relayTrustChecker(manager))
	registerConfigRoutes(mux, manager, protected)
	registerRuntimeServiceRoutes(mux, serviceRuntime, protected)
	registerControlNodeRoutes(mux, controlRegistry, overviewProvider, protected)
	registerProviderRoutes(mux, manager, protected)

	registerChannelStatusRoute(mux, channels, protected)
	registerGatewayRoutes(mux, manager, service, usageService, protected)
	registerServiceCatalogRoutes(mux, service, protected)
	registerPackageManagementRoutes(mux, service, protected)
	registerPromptAndCommandRoutes(mux, service, protected)
	registerPackageCatalogRoutes(mux, service, protected)
	registerWeixinRoutes(mux, weixinAuth, protected)
	registerAutomationRoutes(mux, cronRuntime, heartbeatRuntime, protected)
	registerMemoryRoutes(mux, service, protected)
	registerNoteRoutes(mux, service, protected)
	registerSessionRoutes(mux, service, protected)
	registerWorkflowRoutes(mux, service, protected)
	registerAgentTemplateRoutes(mux, service, protected)
	registerSkillRoutes(mux, service, protected)
	registerTurnRoutes(mux, manager, service, protected)
	registerUsageRoutes(mux, protected, usageService, manager)
	registerBizRoutes(mux, protected, usageService, service)
	registerStepRoutes(mux, usageService, service)
	registerStepTrackRoutes(mux, usageService, service)
	registerMCPRoutes(mux, protected, service.MCPManager())
	registerFileRoutes(mux, protected, manager)
	registerTerminalRoutes(mux)
	registerVoiceRoutes(mux, service, manager, protected, func() string { return manager.Current().WebToken })
	registerPreviewRoutes(mux, manager)
	registerGitRoutes(mux, protected, manager)
	capture := deps.LlmCapture
	if capture == nil {
		capture = llmcapture.New(llmcapture.Options{DumpDir: manager.Current().StateDir})
	}
	registerLlmCaptureRoutes(mux, capture, protected)
	return withGzip(mux)
}

// gzipResponseWriter compresses responses when the client accepts gzip.
// SSE streams are passed through uncompressed (they need streaming flushes),
// as are range requests (compressing would break byte serving).
type gzipResponseWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	compress bool
	written  bool
}

func (g *gzipResponseWriter) WriteHeader(status int) {
	if g.written {
		return
	}
	g.written = true
	ct := g.Header().Get("Content-Type")
	if g.Header().Get("Content-Encoding") == "" && !strings.HasPrefix(strings.ToLower(ct), "text/event-stream") {
		g.Header().Del("Content-Length")
		g.Header().Set("Content-Encoding", "gzip")
		g.Header().Add("Vary", "Accept-Encoding")
		g.compress = true
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if !g.written {
		g.WriteHeader(http.StatusOK)
	}
	if g.compress {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipResponseWriter) Flush() {
	if g.written && g.compress {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// withGzip wraps a handler with on-the-fly gzip compression for clients that
// advertise Accept-Encoding: gzip.
// WebSocket Upgrade 请求必须原样透传：gzipResponseWriter 不支持 http.Hijacker，
// 包装后 gorilla/websocket 升级必然失败（浏览器握手带 Accept-Encoding: gzip → 500）。
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) ||
			r.Header.Get("Range") != "" ||
			!strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gz := gzip.NewWriter(w)
		defer gz.Close()
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gz: gz}, r)
	})
}

// isWebSocketUpgrade 判断是否为 WebSocket 升级请求（Connection: Upgrade + Upgrade: websocket）。
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
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
// withBearerAuthProvider wraps a handler with web-token auth. When relayTrust
// checkers are supplied, a request carrying a valid relay-channel trust header
// (signed with this instance's own node credential) is allowed without the
// web token — this is what lets the center proxy forward remote operations
// (chat/terminal/files) to a node that has its own web token configured.
func withBearerAuthProvider(token func() string, relayTrust ...func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, trust := range relayTrust {
				if trust != nil && trust(r) {
					handler.ServeHTTP(w, r)
					return
				}
			}
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

// relayTrustChecker returns a request checker that accepts requests carrying a
// valid X-Godex-Relay-Trusted header signed with this instance's own node
// credential (control.credential + node id). Only the node-side relay agent,
// which holds the credential, can produce a matching signature, so a forged
// header from an unauthorized client is rejected. Instances without a control
// credential configured (pure centers) never trust the header.
func relayTrustChecker(manager *config.Manager) func(*http.Request) bool {
	return func(r *http.Request) bool {
		value := strings.TrimSpace(r.Header.Get(relay.RelayTrustHeader))
		if value == "" {
			return false
		}
		cfg := manager.Current()
		credential := strings.TrimSpace(cfg.Control.Credential)
		if credential == "" {
			return false
		}
		nodeID := strings.TrimSpace(cfg.Control.NodeID)
		if nodeID == "" {
			// Fall back to the persisted auto-generated id.
			if id, err := noderegistry.EnsureNodeID(cfg.StateDir, ""); err == nil {
				nodeID = id
			}
		}
		if nodeID == "" {
			return false
		}
		return relay.ValidateRelayTrust(value, nodeID, credential)
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

// gunzipReadCloser wraps a gzip.Reader over the original request body and
// closes both when done (closed body streams must not leak).
type gunzipReadCloser struct {
	*gzip.Reader
	closer io.Closer
}

func (g *gunzipReadCloser) Close() error {
	_ = g.Reader.Close()
	return g.closer.Close()
}

// gunzipBody transparently decompresses a gzip Content-Encoding request body
// (as sent by a godex client with request_gzip enabled) so downstream handlers
// read plain JSON. The Content-Encoding header is stripped so handlers never
// double-decode; the body is treated as chunked (ContentLength unknown) since
// the compressed length differs from the uncompressed one.
func gunzipBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
			if zr, err := gzip.NewReader(r.Body); err == nil {
				r.Body = &gunzipReadCloser{Reader: zr, closer: r.Body}
				r.Header.Del("Content-Encoding")
				r.ContentLength = -1
			}
		}
		next.ServeHTTP(w, r)
	})
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
	if errors.Is(err, backend.ErrInvalidWorkspaceDir) {
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
