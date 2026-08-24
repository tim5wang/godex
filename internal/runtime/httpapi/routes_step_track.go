package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/usage"
)

// Step tracking endpoints (Agent Step Platform, details §2.4): after a
// synchronous step times out (408), the caller can poll the terminal result
// (GET), cancel the run (POST cancel), or subscribe to the underlying session
// events (existing GET /sessions/{id}/events). step_id doubles as the session
// locator key, so the session is deterministically re-resolvable.

// registerStepTrackRoutes registers GET /v1/agent-steps/{id} and
// POST /v1/agent-steps/{id}/cancel behind biz-key auth.
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
	opened, err := service.OpenSession(r.Context(), stepLocator(stepID))
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
	opened, err := service.OpenSession(r.Context(), stepLocator(stepID))
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

// stepLocator returns the deterministic session locator for a step id,
// matching the one used by POST /v1/agent-steps.
func stepLocator(stepID string) backend.SessionLocator {
	return backend.SessionLocator{
		Channel: "step",
		Key:     stepID,
		Metadata: map[string]string{
			"system_key": "step",
		},
	}
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
