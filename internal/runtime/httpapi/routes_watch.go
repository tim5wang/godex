// godex-feature: watch-api
// 手表端 REST 面（W2/W3/W4/W6/W7/W8）：配对、设备管理、会话/节点只读、审批、指令。
// 入口：/watch/*
// 文档：docs/wearos-watch-implementation-plan.md §4
package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/watchauth"
	"github.com/tim5wang/godex/internal/tools"
)

// watchDeviceContextKey carries the authenticated device down the chain.
type watchDeviceContextKey struct{}

func watchDeviceFromContext(ctx context.Context) (watchauth.Device, bool) {
	device, ok := ctx.Value(watchDeviceContextKey{}).(watchauth.Device)
	return device, ok
}

// authenticateWatchRequest resolves the presented watch credential.
//
// This deliberately does not reuse bearerAuthorized: that compares against one
// shared secret, whereas watch credentials are per-device and must be looked up
// in the device registry so an individual watch can be revoked.
func authenticateWatchRequest(auth *watchauth.Service, r *http.Request) (watchauth.Device, bool) {
	if auth == nil {
		return watchauth.Device{}, false
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return watchauth.Device{}, false
	}
	return auth.Authenticate(strings.TrimSpace(header[len("Bearer "):]))
}

// withWatchScope enforces that the calling device holds scope. The scope set is
// fixed per route, so a watch cannot widen its own authority by asking.
func withWatchScope(auth *watchauth.Service, scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			device, ok := authenticateWatchRequest(auth, r)
			if !ok {
				writeError(w, http.StatusUnauthorized, fmt.Errorf("missing or invalid watch credential"))
				return
			}
			if !device.HasScope(scope) {
				writeError(w, http.StatusForbidden,
					fmt.Errorf("watch device lacks required scope %q", scope))
				return
			}
			// Best-effort activity stamp so the device list shows liveness.
			auth.Touch(device.DeviceID)
			ctx := context.WithValue(r.Context(), watchDeviceContextKey{}, device)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// watchASRProvider is the optional voice-engine bridge (W9). Handlers detect it
// by type assertion so the watch surface still builds with voice disabled.
type watchASRProvider interface {
	Transcribe(ctx context.Context, audio []byte, mimeType string) (string, float64, error)
}

// ---- wire payloads (kept deliberately small: these travel over BLE/WiFi to a
// device with a 454px screen and a small battery) ----

type watchSessionItem struct {
	SessionID       string    `json:"session_id"`
	Title           string    `json:"title,omitempty"`
	Running         bool      `json:"running,omitempty"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	LastActivityAt  time.Time `json:"last_activity_at,omitempty"`
}

type watchNodeItem struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"last_seen,omitempty"`
}

// watchStep is one entry of the agent's own plan, trimmed for a wrist (mockup 05).
type watchStep struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed
}

// watchCrossDeviceItem is one session tagged with the node it lives on
// (mockup 14). The embedded session fields stay flat on the wire so the same
// client parser handles both /watch/sessions and /watch/cross-device.
type watchCrossDeviceItem struct {
	watchSessionItem
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Online   bool   `json:"online"`
	Remote   bool   `json:"remote"`
}

// watchApprovalItem unifies the two things a wrist genuinely needs to decide:
// tool-permission requests and flow human tasks.
type watchApprovalItem struct {
	Kind         string    `json:"kind"` // permission | flow_task
	SessionID    string    `json:"session_id,omitempty"`
	SessionTitle string    `json:"session_title,omitempty"`
	NodeName     string    `json:"node_name,omitempty"`
	RequestID    string    `json:"request_id,omitempty"`
	ToolName     string    `json:"tool_name,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`

	// Approval-detail fields (mockup 06). The single-approval screen must show
	// what will actually run before the user authorises it, so it needs the
	// untruncated command and the same risk verdict the other clients use.
	Command   string    `json:"command,omitempty"`
	Risk      string    `json:"risk,omitempty"`
	RiskLevel string    `json:"risk_level,omitempty"` // high | medium | low
	ExpiresAt time.Time `json:"expires_at,omitempty"`

	TaskID string `json:"task_id,omitempty"`
	RunID  string `json:"run_id,omitempty"`
	Queue  string `json:"queue,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

type watchCommandRequest struct {
	SessionID string            `json:"session_id"`
	Text      string            `json:"text"`
	Source    string            `json:"source,omitempty"` // text | voice
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type watchApproveRequest struct {
	Scope string `json:"scope,omitempty"` // permission scope: once|task|session|pattern
}

type watchDenyRequest struct {
	Reason string `json:"reason,omitempty"`
}

type watchOverviewResponse struct {
	Devices          []watchNodeItem   `json:"devices"`
	CurrentSession   *watchSessionItem `json:"current_session,omitempty"`
	ActiveSessions   int               `json:"active_sessions"`
	PendingApprovals int               `json:"pending_approvals"`
	ServerTime       time.Time         `json:"server_time"`
}

// maxApprovalScan bounds the number of sessions inspected when aggregating the
// approval queue. The wrist shows a queue, not an audit log; an unbounded scan
// would make a watch poll O(every session ever).
const maxApprovalScan = 50

// maxApprovalCommandRunes bounds the full command echoed to the detail screen
// (mockup 06). It is far above the 140-rune list summary because that screen
// exists precisely to show what will run, but still bounded so a huge script
// cannot inflate every poll of the approval queue.
const maxApprovalCommandRunes = 1200

// watchLocalNodeName labels sessions served by this node. Sessions held by the
// local service have no registry entry of their own, so they are attributed to
// the node answering the request instead of being shown with an empty device
// label (mockup 06 renders "BASH · PC-01 · 会话「…」").
const watchLocalNodeName = "本机"

// watchAPI bundles the dependencies the watch handlers need.
//
// It exists so route registration can be split by concern (pairing / devices /
// read / overview / approvals / commands) without threading five parameters
// through every function. Each function must stay inside the architecture
// complexity budget, which one monolithic registration function exceeds.
type watchAPI struct {
	auth            *watchauth.Service
	service         *backend.Service
	controlRegistry controlNodeRegistry
	asr             watchASRProvider
}

// registerWatchRoutes wires the whole watch surface. The work lives in the
// per-concern functions below; this only composes them.
func registerWatchRoutes(
	mux *http.ServeMux,
	auth *watchauth.Service,
	service *backend.Service,
	protected func(http.Handler) http.Handler,
	controlRegistry controlNodeRegistry,
	asr watchASRProvider,
) {
	api := watchAPI{auth: auth, service: service, controlRegistry: controlRegistry, asr: asr}

	read := withWatchScope(auth, watchauth.ScopeRead)
	command := withWatchScope(auth, watchauth.ScopeCommand)
	approve := withWatchScope(auth, watchauth.ScopeApprove)

	// The push queue is derived state, so it is created here (once per handler,
	// shared by every request) rather than stored on the node.
	pushQueue := newWatchPushQueue()

	registerWatchPairingRoutes(mux, api, protected)
	registerWatchDeviceRoutes(mux, api, protected)
	registerWatchReadRoutes(mux, api, read)
	registerWatchOverviewRoute(mux, api, read)
	registerWatchApprovalRoutes(mux, api, read, approve)
	registerWatchCommandRoutes(mux, api, command)
	registerWatchPushRoutes(mux, api, pushQueue, protected)
}

// registerWatchPairingRoutes covers device authorization (plan W2).
//
// These two endpoints are intentionally NOT behind `protected`: the watch has no
// credential yet (that is the point of pairing), and the trusted approver uses
// the normal web token. The device code is the bearer of the polling side, the
// short user code is what a human confirms.
func registerWatchPairingRoutes(mux *http.ServeMux, api watchAPI, protected func(http.Handler) http.Handler) {
	auth := api.auth
	mux.Handle("POST /watch/pair/start", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("watch pairing is not enabled on this node"))
			return
		}
		var req struct {
			Name string `json:"name,omitempty"`
			Kind string `json:"kind,omitempty"`
		}
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		pairing, err := auth.StartPairing(req.Name, req.Kind)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// device_code is only ever returned here, to the device that asked.
		writeJSON(w, http.StatusCreated, pairing)
	}))

	mux.Handle("POST /watch/pair/approve", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("watch pairing is not enabled on this node"))
			return
		}
		var req struct {
			UserCode string   `json:"user_code"`
			Scopes   []string `json:"scopes,omitempty"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.UserCode) == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("user_code is required"))
			return
		}
		device, err := auth.ApprovePairing(req.UserCode, req.Scopes)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, device)
	})))

	// The watch polls this until approved. A pending result is a normal 200 —
	// the watch must be able to distinguish "keep waiting" from "your code is
	// wrong", and an error status would make it retry thunder.
	mux.Handle("POST /watch/pair/token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("watch pairing is not enabled on this node"))
			return
		}
		var req struct {
			DeviceCode string `json:"device_code"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.DeviceCode) == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("device_code is required"))
			return
		}
		result, err := auth.ExchangeToken(req.DeviceCode)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
}

// registerWatchDeviceRoutes covers device management (plan W3).
//
// These are trusted-endpoint operations: only the normal web credential reaches
// them, never a watch's own scoped token. A compromised watch must not be able to
// enumerate or revoke its siblings.
func registerWatchDeviceRoutes(mux *http.ServeMux, api watchAPI, protected func(http.Handler) http.Handler) {
	auth := api.auth

	mux.Handle("GET /watch/devices", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth == nil {
			writeJSON(w, http.StatusOK, []watchauth.Device{})
			return
		}
		writeJSON(w, http.StatusOK, auth.List(r.Context()))
	})))

	mux.Handle("DELETE /watch/devices/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("watch registry is not enabled on this node"))
			return
		}
		device, err := auth.Revoke(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, device)
	})))
}

// registerWatchReadRoutes covers the read-only session/node surface (watch:read).
func registerWatchReadRoutes(mux *http.ServeMux, api watchAPI, read func(http.Handler) http.Handler) {
	service := api.service
	controlRegistry := api.controlRegistry

	mux.Handle("GET /watch/sessions", read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			writeJSON(w, http.StatusOK, []watchSessionItem{})
			return
		}
		limit := 20
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			if n := parseIntQuery(raw, 20); n > 0 && n <= 100 {
				limit = n
			}
		}
		sessions, err := service.ListSessions(r.Context(), backend.SessionListFilter{})
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		// Newest first, then trim: the watch only ever shows the head.
		sortSessionsByActivity(sessions)
		items := make([]watchSessionItem, 0, len(sessions))
		for _, s := range sessions {
			if len(items) >= limit {
				break
			}
			items = append(items, watchSessionItem{
				SessionID:       s.SessionID,
				Title:           s.Title,
				Running:         s.Running,
				ParentSessionID: s.ParentSessionID,
				LastActivityAt:  s.LastActivityAt,
			})
		}
		writeJSON(w, http.StatusOK, items)
	})))

	mux.Handle("GET /watch/sessions/{id}", read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("session service unavailable"))
			return
		}
		snapshot, err := loadWatchSession(r.Context(), service, r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, watchSessionDetail(snapshot))
	})))

	mux.Handle("GET /watch/nodes", read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeJSON(w, http.StatusOK, []watchNodeItem{})
			return
		}
		nodes, err := controlRegistry.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items := make([]watchNodeItem, 0, len(nodes))
		for _, n := range nodes {
			items = append(items, watchNodeItem{
				ID: n.ID, Name: n.Name, Status: n.Status, LastSeen: n.LastSeen,
			})
		}
		writeJSON(w, http.StatusOK, items)
	})))

	registerWatchCrossDeviceRoute(mux, api, read)
}

// registerWatchCrossDeviceRoute serves every session this center can see, each
// tagged with the node it lives on (mockup 14).
//
// The response is deliberately unfiltered: "filter by device" is a client-side
// view, not a server mode, so the watch receives one list and narrows it
// locally without a round-trip per filter change.
func registerWatchCrossDeviceRoute(mux *http.ServeMux, api watchAPI, read func(http.Handler) http.Handler) {
	mux.Handle("GET /watch/cross-device", read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, collectWatchCrossDevice(r.Context(), api.service, api.controlRegistry))
	})))
}

// collectWatchCrossDevice merges this node's own sessions with the session
// lists other nodes publish over the relay. Deduplication is by session id and
// prefers the local copy, because the relay view can lag behind the session
// service itself.
func collectWatchCrossDevice(
	ctx context.Context,
	service *backend.Service,
	controlRegistry controlNodeRegistry,
) []watchCrossDeviceItem {
	items := []watchCrossDeviceItem{}
	seen := map[string]struct{}{}

	if service != nil {
		if sessions, err := service.ListSessions(ctx, backend.SessionListFilter{}); err == nil {
			sortSessionsByActivity(sessions)
			for _, s := range sessions {
				seen[s.SessionID] = struct{}{}
				items = append(items, watchCrossDeviceItem{
					watchSessionItem: watchSessionItem{
						SessionID:       s.SessionID,
						Title:           s.Title,
						Running:         s.Running,
						ParentSessionID: s.ParentSessionID,
						LastActivityAt:  s.LastActivityAt,
					},
					NodeID:   "local",
					NodeName: "本机",
					Online:   true,
				})
			}
		}
	}

	provider, ok := controlRegistry.(nodeOverviewProvider)
	if !ok || controlRegistry == nil {
		return items
	}
	nodes, err := controlRegistry.List(ctx)
	if err != nil {
		return items
	}
	for _, n := range nodes {
		overview, ok := provider.Overview(n.ID)
		if !ok {
			continue
		}
		online := strings.EqualFold(n.Status, "online")
		for _, s := range overview.Sessions {
			if _, dup := seen[s.ID]; dup {
				continue
			}
			seen[s.ID] = struct{}{}
			items = append(items, watchCrossDeviceItem{
				watchSessionItem: watchSessionItem{
					SessionID:      s.ID,
					Title:          s.Title,
					Running:        s.Running,
					LastActivityAt: s.UpdatedAt,
				},
				NodeID:   n.ID,
				NodeName: n.Name,
				Online:   online,
				Remote:   true,
			})
		}
	}
	return items
}

// registerWatchOverviewRoute serves the aggregated home payload (plan W10).
//
// Split from the other read routes because it composes all of them: the home
// screen needs nodes + the active session + the approval count at once, and one
// response keeps a watch from making four trips (and four radio wake-ups) every
// time the user lifts their wrist.
func registerWatchOverviewRoute(mux *http.ServeMux, api watchAPI, read func(http.Handler) http.Handler) {
	service := api.service
	controlRegistry := api.controlRegistry
	mux.Handle("GET /watch/overview", read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := watchOverviewResponse{
			Devices:          []watchNodeItem{},
			ServerTime:       time.Now(),
			ActiveSessions:   0,
			PendingApprovals: 0,
		}

		if controlRegistry != nil {
			if nodes, err := controlRegistry.List(r.Context()); err == nil {
				for _, n := range nodes {
					resp.Devices = append(resp.Devices, watchNodeItem{
						ID: n.ID, Name: n.Name, Status: n.Status, LastSeen: n.LastSeen,
					})
				}
			}
		}

		if service != nil {
			if sessions, err := service.ListSessions(r.Context(), backend.SessionListFilter{}); err == nil {
				sortSessionsByActivity(sessions)
				for _, s := range sessions {
					if s.Running {
						resp.ActiveSessions++
					}
				}
				if len(sessions) > 0 {
					top := sessions[0]
					item := watchSessionItem{
						SessionID:       top.SessionID,
						Title:           top.Title,
						Running:         top.Running,
						ParentSessionID: top.ParentSessionID,
						LastActivityAt:  top.LastActivityAt,
					}
					resp.CurrentSession = &item
				}
			}
		}

		resp.PendingApprovals = len(collectWatchApprovals(r.Context(), service))
		writeJSON(w, http.StatusOK, resp)
	})))
}

// registerWatchApprovalRoutes covers the approval queue and decisions (W4/W6).
func registerWatchApprovalRoutes(
	mux *http.ServeMux,
	api watchAPI,
	read func(http.Handler) http.Handler,
	approve func(http.Handler) http.Handler,
) {
	service := api.service

	mux.Handle("GET /watch/approvals", read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := collectWatchApprovals(r.Context(), service)
		writeJSON(w, http.StatusOK, items)
	})))

	mux.Handle("POST /watch/approvals/{sessionID}/{requestID}/approve", approve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("session service unavailable"))
			return
		}
		var req watchApproveRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		// A wrist tap is a deliberate, narrow action: default to once-only and
		// require an explicit scope to broaden it.
		scope := permissionScopeOrDefault(req.Scope)
		resolution, err := service.ApprovePermission(r.Context(), r.PathValue("sessionID"), r.PathValue("requestID"), scope)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, resolution)
	})))

	mux.Handle("POST /watch/approvals/{sessionID}/{requestID}/deny", approve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("session service unavailable"))
			return
		}
		var req watchDenyRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		reason := strings.TrimSpace(req.Reason)
		if reason == "" {
			reason = "denied from watch"
		}
		resolution, err := service.DenyPermission(r.Context(), r.PathValue("sessionID"), r.PathValue("requestID"), reason)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, resolution)
	})))
}

// registerWatchCommandRoutes covers instruction intake and dictation (W8/W9).
func registerWatchCommandRoutes(mux *http.ServeMux, api watchAPI, command func(http.Handler) http.Handler) {
	service := api.service
	asr := api.asr

	mux.Handle("POST /watch/command", command(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		device, _ := watchDeviceFromContext(r.Context())
		if service == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("session service unavailable"))
			return
		}
		var req watchCommandRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		sessionID := strings.TrimSpace(req.SessionID)
		text := strings.TrimSpace(req.Text)
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("session_id is required"))
			return
		}
		if text == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("text is required"))
			return
		}

		// Voice and typed input differ in provenance, so they stay separable in
		// the transcript for later quality analysis.
		source := message.SourceWeb
		if strings.EqualFold(req.Source, "voice") {
			source = message.SourceVoice
		}
		envelope := message.NewTextEnvelope(source, sessionID, "watch:"+device.DeviceID, text, time.Now())
		envelope.Metadata = map[string]string{"watch_device_id": device.DeviceID}
		for k, v := range req.Metadata {
			envelope.Metadata[k] = v
		}

		// Async: the watch shows "accepted" immediately and follows progress via
		// the session detail endpoint. A blocking submit would outlive the
		// watch's screen-on window.
		result, err := service.SubmitAsync(r.Context(), sessionID, envelope)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	})))

	// Stop the running turn (mockup 05). The watch cannot know a turn id, so it
	// stops whatever is active; when nothing is running this returns 409 instead
	// of silently reporting success.
	mux.Handle("POST /watch/sessions/{id}/stop", command(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("session service unavailable"))
			return
		}
		sessionID := r.PathValue("id")
		snapshot, err := loadWatchSession(r.Context(), service, sessionID)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		if snapshot.ActiveTurnID == "" {
			writeError(w, http.StatusConflict, fmt.Errorf("没有正在运行的任务"))
			return
		}
		result, err := service.CancelTurn(r.Context(), sessionID, snapshot.ActiveTurnID)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))

	// ---- W9: dictation (watch:command) ----
	//
	// The watch prefers an on-device recogniser when one exists; this endpoint is
	// the path that also works when it does not. On a device with no speech
	// service it is the only path, so it is a first-class route rather than a
	// fallback.
	mux.Handle("POST /watch/asr", command(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if asr == nil {
			writeError(w, http.StatusServiceUnavailable,
				fmt.Errorf("voice transcription is not configured on this node"))
			return
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("expected multipart audio upload: %w", err))
			return
		}
		file, header, err := r.FormFile("audio")
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("missing audio part: %w", err))
			return
		}
		defer file.Close()
		audio, err := io.ReadAll(io.LimitReader(file, 8<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if len(audio) == 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("audio part is empty"))
			return
		}
		mimeType := "audio/wav"
		if header != nil && strings.TrimSpace(header.Header.Get("Content-Type")) != "" {
			mimeType = header.Header.Get("Content-Type")
		}
		text, confidence, err := asr.Transcribe(r.Context(), audio, mimeType)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"text": text, "confidence": confidence})
	})))
}

// loadWatchSession reads a session's snapshot, loading it from disk first when it
// is not already in memory.
//
// Sessions live on disk but only become readable through requireSession once
// loaded; the Web UI never notices because it opens a session before reading it.
// A watch starts cold and reads a session id straight out of the list, so after a
// node restart the list would resolve while detail/stop returned 404 — the list
// and the detail screen would disagree about whether a session even exists.
//
// The first Snapshot doubles as the "already loaded" probe and its result is
// returned, so the common path builds a snapshot exactly once (a snapshot can
// carry the whole message history, so building it twice is not free).
func loadWatchSession(ctx context.Context, service *backend.Service, sessionID string) (backend.Snapshot, error) {
	if service == nil {
		// backend.newSessionNotFoundError is unexported; wrap the exported sentinel
		// so statusForSessionError still maps this to 404.
		return backend.Snapshot{}, fmt.Errorf("%w: %s", backend.ErrSessionNotFound, sessionID)
	}

	snapshot, err := service.Snapshot(ctx, sessionID)
	if err == nil {
		return snapshot, nil
	}

	// Not in memory. Find the persisted session so its real locator can be
	// replayed: a bare session id does not carry the channel/key/project_dir that
	// the loader needs to rebuild the session identity.
	sessions, listErr := service.ListSessions(ctx, backend.SessionListFilter{})
	if listErr != nil {
		return backend.Snapshot{}, err
	}
	for _, s := range sessions {
		if s.SessionID != sessionID {
			continue
		}
		locator := s.Locator
		// The id is already known, so the process-wide latest-session pointer
		// must not redirect this open to a different session.
		locator.SkipLatestSessionLookup = true
		if _, openErr := service.OpenSession(ctx, locator); openErr != nil {
			break
		}
		return service.Snapshot(ctx, sessionID)
	}

	// Not on disk either: report the original read error (404).
	return backend.Snapshot{}, err
}

// watchSessionDetail trims a full snapshot down to what a wrist renders. The
// full snapshot can carry entire turn histories; shipping that to a watch would
// burn battery and bandwidth for content the screen cannot show.
func watchSessionDetail(snapshot backend.Snapshot) map[string]any {
	out := map[string]any{
		"session_id":        snapshot.SessionID,
		"running":           snapshot.Running,
		"updated_at":        snapshot.UpdatedAt,
		"pending_approvals": len(snapshot.PendingPermissions),
	}
	if snapshot.ActivePhase != "" {
		out["active_phase"] = snapshot.ActivePhase
	}
	if snapshot.ActiveTurnID != "" {
		// Lets the watch decide whether "停止" is actionable at all.
		out["active_turn_id"] = snapshot.ActiveTurnID
	}
	if len(snapshot.Turns) > 0 {
		last := snapshot.Turns[len(snapshot.Turns)-1]
		out["last_turn"] = map[string]any{
			"turn_id":    last.ID,
			"status":     last.Status,
			"updated_at": last.UpdatedAt,
		}
		// started_at lets the watch render a live elapsed time from the turn's own
		// start, instead of the server ticking a counter for every client.
		if !last.StartedAt.IsZero() {
			out["started_at"] = last.StartedAt
		}
	}
	// The agent's own todo list is the honest source for "第 4/9 步": godex keeps
	// no separate progress channel, and deriving a percentage from anything else
	// would be a fabricated number.
	if steps := watchSteps(snapshot.Todos); len(steps) > 0 {
		done := 0
		for _, s := range steps {
			if s.Status == string(todo.StatusCompleted) {
				done++
			}
		}
		out["steps"] = steps
		out["step_done"] = done
		out["step_total"] = len(steps)
		out["progress_percent"] = done * 100 / len(steps)
	}
	if tool := lastToolName(snapshot.Timeline); tool != "" {
		out["current_tool"] = tool
	}
	// A bounded sequence of recent tool calls, so the wrist can show what the agent
	// has been doing rather than only the single most recent tool name. Bounded
	// because a watch screen holds a handful of lines and a long turn can contain
	// hundreds of calls; the full log belongs on a larger screen.
	if tools := recentToolEvents(snapshot.Timeline, maxWatchToolEvents); len(tools) > 0 {
		out["recent_tools"] = tools
	}
	if len(snapshot.Messages) > 0 {
		// One-line preview of the newest message only.
		last := snapshot.Messages[len(snapshot.Messages)-1]
		out["last_message"] = map[string]any{
			"role": last.Role,
			"text": truncateRunes(protocol.MessageText(last), 140),
		}
	}
	return out
}

// watchSteps projects the todo list into the shape a wrist renders. The
// present-tense ActiveForm is preferred for the in-progress step because that is
// what the agent is doing right now.
func watchSteps(items []todo.Item) []watchStep {
	steps := make([]watchStep, 0, len(items))
	for _, it := range items {
		if it.Status == todo.StatusDeleted {
			continue
		}
		content := strings.TrimSpace(it.Content)
		if it.Status == todo.StatusInProgress {
			if active := strings.TrimSpace(it.ActiveForm); active != "" {
				content = active
			}
		}
		if content == "" {
			continue
		}
		steps = append(steps, watchStep{Content: truncateRunes(content, 80), Status: string(it.Status)})
	}
	return steps
}

// lastToolName reports the tool the agent most recently started, read from the
// session timeline. It is what makes "is the agent working or stuck?"
// answerable from a glance.
func lastToolName(timeline []events.Event) string {
	for i := len(timeline) - 1; i >= 0; i-- {
		if timeline[i].Type != events.EventToolCallStarted {
			continue
		}
		if name := toolNameFromPayload(timeline[i].Payload); name != "" {
			return name
		}
	}
	return ""
}

// maxWatchToolEvents bounds the tool sequence sent to the wrist. A watch screen
// holds a few lines, and a long turn can contain hundreds of calls, so sending
// the whole timeline would bloat every poll for data that cannot be displayed.
const maxWatchToolEvents = 5

// watchToolEvent is one tool call, trimmed for a wrist (mockup 05).
//
// Only the tool name, outcome and duration are included: the watch answers "what
// is it doing and did it work", and any richer detail belongs on a larger screen.
//
// A call with no outcome yet is one still in flight, which is what the wrist
// should present as the current activity.
type watchToolEvent struct {
	Name       string `json:"name"`
	Failed     bool   `json:"failed,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	// Pending marks a call whose finish event has not arrived, i.e. the current
	// activity. Stated explicitly because the client cannot infer it: a tool that
	// finishes in under a millisecond also reports duration_ms 0, and rendering
	// that as "still running" would be a visible lie about state.
	Pending bool `json:"pending,omitempty"`

	// settled is internal bookkeeping: true once a finish event has been folded in,
	// so a second finish for the same tool cannot overwrite an already-reported
	// outcome. Not serialised.
	settled bool
}

// recentToolEvents returns the most recent tool calls, newest last.
//
// Started and finished events are folded by tool name so a call appears once
// with its outcome, instead of twice with only half the information each time.
// A started event with no matching finish is a call still in flight, which is
// exactly what the wrist should show as the current activity.
func recentToolEvents(timeline []events.Event, limit int) []watchToolEvent {
	out := make([]watchToolEvent, 0, limit)
	for _, ev := range timeline {
		switch ev.Type {
		case events.EventToolCallStarted:
			name := toolNameFromPayload(ev.Payload)
			if name == "" {
				continue
			}
			out = append(out, watchToolEvent{Name: name})
		case events.EventToolCallFinished:
			payload, ok := toolCallPayload(ev.Payload)
			if !ok {
				continue
			}
			// Attach the outcome to the newest still-pending call of the same name:
			// searching from the end picks the current invocation when one tool is
			// used repeatedly within a single turn.
			matched := false
			for i := len(out) - 1; i >= 0; i-- {
				if out[i].Name != payload.Name || out[i].settled {
					continue
				}
				out[i].Failed = payload.Error != ""
				out[i].TimedOut = payload.TimedOut
				out[i].DurationMS = payload.DurationMS
				out[i].settled = true
				matched = true
				break
			}
			if !matched {
				// A finish whose start was not seen (events replayed from mid-turn)
				// must still be shown rather than silently dropped.
				out = append(out, watchToolEvent{
					Name:       payload.Name,
					Failed:     payload.Error != "",
					TimedOut:   payload.TimedOut,
					DurationMS: payload.DurationMS,
					settled:    true,
				})
			}
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	// A call with no folded outcome is still in flight. Computed once at the end so
	// the flag cannot drift from the settled bookkeeping.
	for i := range out {
		out[i].Pending = !out[i].settled
	}
	return out
}

// toolCallPayload tolerates both the in-process struct and the JSON-decoded map
// shape, because a snapshot may have been rehydrated from disk.
func toolCallPayload(payload any) (events.ToolCallPayload, bool) {
	switch v := payload.(type) {
	case events.ToolCallPayload:
		return v, true
	case *events.ToolCallPayload:
		if v != nil {
			return *v, true
		}
	case map[string]any:
		name, _ := v["name"].(string)
		if name == "" {
			return events.ToolCallPayload{}, false
		}
		out := events.ToolCallPayload{Name: name}
		if s, ok := v["error"].(string); ok {
			out.Error = s
		}
		if b, ok := v["timed_out"].(bool); ok {
			out.TimedOut = b
		}
		if f, ok := v["duration_ms"].(float64); ok {
			out.DurationMS = int64(f)
		}
		return out, true
	}
	return events.ToolCallPayload{}, false
}

// toolNameFromPayload tolerates both the in-process struct and the JSON-decoded
// map shape, because a snapshot may have been rehydrated from disk.
func toolNameFromPayload(payload any) string {
	switch v := payload.(type) {
	case events.ToolCallPayload:
		return v.Name
	case *events.ToolCallPayload:
		if v != nil {
			return v.Name
		}
	case map[string]any:
		if name, ok := v["name"].(string); ok {
			return name
		}
	}
	return ""
}

// collectWatchApprovals merges tool-permission requests and flow human tasks
// into one queue, because on a watch they are the same user action: decide.
func collectWatchApprovals(ctx context.Context, service *backend.Service) []watchApprovalItem {
	items := []watchApprovalItem{}
	if service == nil {
		return items
	}
	sessions, err := service.ListSessions(ctx, backend.SessionListFilter{})
	if err != nil {
		return items
	}
	sortSessionsByActivity(sessions)

	scanned := 0
	for _, s := range sessions {
		if scanned >= maxApprovalScan {
			break
		}
		scanned++
		pending, err := service.PendingPermissions(ctx, s.SessionID)
		if err != nil {
			continue
		}
		for _, p := range pending {
			// RiskLevel is derived from the shared verdict so the wrist labels a
			// command exactly as the other clients do, rather than re-deciding
			// what counts as dangerous.
			risk := tools.PermissionRiskSummary(p.Request)
			items = append(items, watchApprovalItem{
				Kind:         "permission",
				SessionID:    s.SessionID,
				SessionTitle: s.Title,
				NodeName:     watchLocalNodeName,
				RequestID:    p.ID,
				ToolName:     p.Request.ToolName,
				Summary:      truncateRunes(firstNonEmptyString(p.Request.Command, p.Request.Action, p.Request.ToolName), 140),
				// Full command for the detail screen (mockup 06). Bounded so a huge
				// script cannot bloat every poll response, but far above what the
				// 140-rune list summary shows.
				Command:   truncateRunes(p.Request.Command, maxApprovalCommandRunes),
				Risk:      risk,
				RiskLevel: watchRiskLevel(risk),
				ExpiresAt: p.ExpiresAt,
				CreatedAt: p.CreatedAt,
			})
		}
	}

	tasks, err := service.ListHumanTasks("", "")
	if err != nil {
		return items
	}
	for _, t := range tasks {
		if !strings.EqualFold(t.Status, "pending") && !strings.EqualFold(t.Status, "waiting") {
			continue
		}
		items = append(items, watchApprovalItem{
			Kind:      "flow_task",
			TaskID:    t.TaskID,
			RunID:     t.RunID,
			Queue:     t.Queue,
			Summary:   truncateRunes(firstNonEmptyString(t.Prompt, t.NodeID), 140),
			Prompt:    truncateRunes(t.Prompt, 280),
			CreatedAt: t.CreatedAt,
		})
	}
	return items
}

func sortSessionsByActivity(sessions []backend.ListedSession) {
	// Most-recently-active first; the watch list is a recency list by design.
	for i := 1; i < len(sessions); i++ {
		for j := i; j > 0; j-- {
			if sessionActivity(sessions[j]).After(sessionActivity(sessions[j-1])) {
				sessions[j], sessions[j-1] = sessions[j-1], sessions[j]
				continue
			}
			break
		}
	}
}

func sessionActivity(s backend.ListedSession) time.Time {
	if !s.LastActivityAt.IsZero() {
		return s.LastActivityAt
	}
	return s.UpdatedAt
}

// watchRiskLevel reduces the shared risk verdict to the coarse level the wrist
// acts on.
//
// It reads the level from the prefix of tools.PermissionRiskSummary rather than
// re-classifying the tool here, so the watch and every other client keep giving
// the same answer: duplicating the risk policy would let them drift, and the
// wrist is exactly where a wrong "safe" label is most costly.
func watchRiskLevel(summary string) string {
	switch {
	case strings.HasPrefix(summary, "high risk"):
		return "high"
	case strings.HasPrefix(summary, "medium risk"):
		return "medium"
	case strings.HasPrefix(summary, "low risk"):
		return "low"
	default:
		// An unrecognised verdict is reported as unknown rather than as safe.
		return ""
	}
}

func permissionScopeOrDefault(raw string) tools.PermissionGrantScope {
	value := strings.TrimSpace(raw)
	if value == "" {
		return tools.PermissionGrantOnce
	}
	switch value {
	case "once", "task", "session", "pattern":
		return tools.PermissionGrantScope(value)
	default:
		// Unknown scope must not silently widen; fall back to once.
		return tools.PermissionGrantOnce
	}
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
