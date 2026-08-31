package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/usage"
)

// Step tracking endpoints (Agent Step Platform, details §2.4): after a
// synchronous step times out (408), the caller can poll the terminal result
// (GET), cancel the run (POST cancel), or subscribe to the underlying session
// events (existing GET /sessions/{id}/events). step_id doubles as the session
// locator key, so the session is deterministically re-resolvable.

// registerStepTrackRoutes registers GET /v1/agent-steps/{id},
// POST /v1/agent-steps/{id}/cancel, POST /v1/agent-steps/{id}/reply and
// GET /v1/agent-steps/{id}/events behind biz-key auth.
func registerStepTrackRoutes(mux *http.ServeMux, usageService *usage.Service, service *backend.Service) {
	if service == nil {
		return
	}
	mux.Handle("GET /v1/agent-steps/{id}", withBizKeyAuth(usageService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleGetStep(w, r, service)
	})))
	mux.Handle("POST /v1/agent-steps/{id}/cancel", withBizKeyAuth(usageService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleCancelStep(w, r, service)
	})))
	mux.Handle("POST /v1/agent-steps/{id}/reply", withBizKeyAuth(usageService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleReplyStep(w, r, service)
	})))
	mux.Handle("GET /v1/agent-steps/{id}/events", withBizKeyAuth(usageService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stepID := strings.TrimSpace(r.PathValue("id"))
		if stepID == "" {
			writeStepError(w, http.StatusBadRequest, "invalid_request", fmt.Errorf("step_id is required"), "", "")
			return
		}
		opened, err := service.OpenSession(r.Context(), stepLocator(stepID, bizProjectDir(r)))
		if err != nil {
			writeStepError(w, statusForSessionError(err), "step_failed", err, stepID, "")
			return
		}
		serveSessionEventStream(w, r, service, opened.SessionID)
	})))
}

// serveSessionEventStream streams a session's runtime events as SSE (shared by
// GET /sessions/{id}/events and GET /v1/agent-steps/{id}/events).
// ?replay=active replays only the current active turn; ?turn_id filters to one
// turn. A 20s heartbeat keeps the connection alive through proxy read timeouts.
func serveSessionEventStream(w http.ResponseWriter, r *http.Request, service *backend.Service, sessionID string) {
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
		if turnID := strings.TrimSpace(r.URL.Query().Get("turn_id")); turnID != "" {
			replay.TurnID = turnID
		}
		err := service.SubscribeReplay(ctx, sessionID, events.SinkFunc(func(event events.Event) {
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

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
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
}

// handleGetStep returns the current terminal state of a step session:
// the assistant text, tools used (from snapshot messages), and the turn
// status. The session is resolved by step_id via the same locator the
// POST /v1/agent-steps handler used (channel=step, key=step_id).
func handleGetStep(w http.ResponseWriter, r *http.Request, service *backend.Service) {
	stepID := strings.TrimSpace(r.PathValue("id"))
	if stepID == "" {
		writeStepError(w, http.StatusBadRequest, "invalid_request", fmt.Errorf("step_id is required"), "", "")
		return
	}
	opened, err := service.OpenSession(r.Context(), stepLocator(stepID, bizProjectDir(r)))
	if err != nil {
		writeStepError(w, statusForSessionError(err), "step_failed", err, stepID, "")
		return
	}
	sessionID := opened.SessionID

	snapshot, err := service.Snapshot(r.Context(), sessionID)
	if err != nil {
		writeStepError(w, statusForSessionError(err), "step_failed", err, stepID, sessionID)
		return
	}

	text, tools := extractStepSnapshot(snapshot)
	status := "running"
	if !snapshot.Running && snapshot.ActiveTurnID == "" {
		status = "completed"
	}
	writeJSON(w, http.StatusOK, stepResponse{
		StepID:    stepID,
		SessionID: sessionID,
		Status:    status,
		Text:      text,
		ToolsUsed: tools,
		CreatedAt: time.Now(),
	})
}

// handleCancelStep aborts the active turn of a step session.
func handleCancelStep(w http.ResponseWriter, r *http.Request, service *backend.Service) {
	stepID := strings.TrimSpace(r.PathValue("id"))
	if stepID == "" {
		writeStepError(w, http.StatusBadRequest, "invalid_request", fmt.Errorf("step_id is required"), "", "")
		return
	}
	opened, err := service.OpenSession(r.Context(), stepLocator(stepID, bizProjectDir(r)))
	if err != nil {
		writeStepError(w, statusForSessionError(err), "step_failed", err, stepID, "")
		return
	}
	snapshot, err := service.Snapshot(r.Context(), opened.SessionID)
	if err != nil {
		writeStepError(w, statusForSessionError(err), "step_failed", err, stepID, opened.SessionID)
		return
	}
	if snapshot.ActiveTurnID == "" {
		writeStepError(w, http.StatusConflict, "step_not_running", fmt.Errorf("no active turn to cancel"), stepID, opened.SessionID)
		return
	}
	if _, err := service.CancelTurn(r.Context(), opened.SessionID, snapshot.ActiveTurnID); err != nil {
		writeStepError(w, statusForSessionError(err), "step_failed", err, stepID, opened.SessionID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"step_id":    stepID,
		"session_id": opened.SessionID,
		"status":     "canceling",
	})
}

// stepReplyRequest is the body of POST /v1/agent-steps/{id}/reply: the value a
// user submitted through a ui_card form / button, injected back into the step
// session so the agent can continue on the same conversation.
type stepReplyRequest struct {
	Value any `json:"value"`
	Text  string `json:"text,omitempty"`
}

// handleReplyStep injects a ui_card interaction result back into the step
// session and continues the agent turn (async; the caller polls the terminal
// state via GET as usual).
func handleReplyStep(w http.ResponseWriter, r *http.Request, service *backend.Service) {
	stepID := strings.TrimSpace(r.PathValue("id"))
	if stepID == "" {
		writeStepError(w, http.StatusBadRequest, "invalid_request", fmt.Errorf("step_id is required"), "", "")
		return
	}
	var req stepReplyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeStepError(w, http.StatusBadRequest, "invalid_request", err, stepID, "")
		return
	}
	if req.Value == nil && strings.TrimSpace(req.Text) == "" {
		writeStepError(w, http.StatusBadRequest, "invalid_request", fmt.Errorf("value or text is required"), stepID, "")
		return
	}

	opened, err := service.OpenSession(r.Context(), stepLocator(stepID, bizProjectDir(r)))
	if err != nil {
		writeStepError(w, statusForSessionError(err), "step_failed", err, stepID, "")
		return
	}

	// Render the interaction result as a marked user message so the agent sees
	// what the user submitted through the card.
	payload, _ := json.Marshal(req.Value)
	text := strings.TrimSpace(req.Text)
	if text == "" {
		text = string(payload)
	} else {
		text += "\n```json\n" + string(payload) + "\n```"
	}
	envelope := message.NewTextEnvelope(message.SourceStep, opened.SessionID, "step", text, time.Now())
	result, err := service.SubmitAsync(r.Context(), opened.SessionID, envelope)
	if err != nil {
		writeStepError(w, statusForSessionError(err), "step_failed", err, stepID, opened.SessionID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"step_id":    stepID,
		"session_id": opened.SessionID,
		"turn_id":    result.TurnID,
		"status":     "queued",
	})
}

// stepLocator returns the deterministic session locator for a step id,
// matching the one used by POST /v1/agent-steps. projectDir (the business
// key's configured working directory) is part of the identity hash, so every
// route that resolves a step session must pass the same value.
func stepLocator(stepID, projectDir string) backend.SessionLocator {
	meta := map[string]string{"system_key": "step"}
	if projectDir != "" {
		meta["project_dir"] = projectDir
	}
	return backend.SessionLocator{
		Channel:  "step",
		Key:      stepID,
		Metadata: meta,
	}
}

// bizProjectDir returns the authenticated business key's configured working
// directory (empty when absent). All step routes derive it from the same
// context so the session identity stays consistent.
func bizProjectDir(r *http.Request) string {
	key := BizKeyFromContext(r.Context())
	if key == nil {
		return ""
	}
	return key.ProjectDir
}

// extractStepSnapshot pulls the last assistant text and tool names from a
// session snapshot.
func extractStepSnapshot(snapshot backend.Snapshot) (string, []stepToolUse) {
	var texts []string
	var tools []stepToolUse
	seen := map[string]struct{}{}
	for _, msg := range snapshot.Messages {
		if msg.Role != protocol.RoleAssistant {
			continue
		}
		for _, block := range msg.Content {
			switch block.Type {
			case protocol.BlockText:
				if strings.TrimSpace(block.Text) != "" {
					texts = append(texts, block.Text)
				}
			case protocol.BlockToolUse:
				if block.Name != "" {
					if _, ok := seen[block.Name]; !ok {
						seen[block.Name] = struct{}{}
						tools = append(tools, stepToolUse{Name: block.Name, Kind: toolKindFor(block.Name)})
					}
				}
			}
		}
	}
	return strings.TrimSpace(strings.Join(texts, "\n")), tools
}
