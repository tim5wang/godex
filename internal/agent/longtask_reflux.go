package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
)

// longTaskRefluxPayload is the structured form of a longtask assistant
// reflux message. The rendered text is the human-readable summary; the
// payload metadata is what the Web / TUI clients key off of to render
// the reflux bubble.
type longTaskRefluxPayload struct {
	LongTaskID       string                 `json:"longtask_id"`
	RunID            string                 `json:"run_id"`
	Status           string                 `json:"status"`
	UpdatedAt        time.Time              `json:"updated_at"`
	Iterations       int                    `json:"iterations"`
	Stories          []longTaskRefluxStory  `json:"stories"`
	Repaired         []longTaskRepairSummary `json:"repaired,omitempty"`
	BlockedBy        string                 `json:"blocked_by,omitempty"`
	Message          string                 `json:"message,omitempty"`
	SuggestedActions []string               `json:"suggested_actions,omitempty"`
}

type longTaskRefluxStory struct {
	StoryID         string `json:"story_id"`
	Status          string `json:"status"`
	Verdict         string `json:"verdict,omitempty"`
	ValidationRef   string `json:"validation_ref,omitempty"`
	CommitRef       string `json:"commit_ref,omitempty"`
	CommitHash      string `json:"commit_hash,omitempty"`
	ResultPreview   string `json:"result_preview,omitempty"`
	Error           string `json:"error,omitempty"`
}

func buildLongTaskRefluxMessage(view longTaskView, runID string) protocol.Message {
	payload := longTaskRefluxPayload{
		LongTaskID: view.LongTaskID,
		RunID:      runID,
		UpdatedAt:  time.Now().UTC(),
	}
	if view.Run != nil {
		payload.Status = view.Run.Status
		payload.Iterations = view.Run.Iterations
		payload.Repaired = view.Run.Repaired
		payload.BlockedBy = view.Run.BlockedBy
		payload.Message = view.Run.Message
	}
	for _, story := range view.Stories {
		payload.Stories = append(payload.Stories, longTaskRefluxStory{
			StoryID:       story.ID,
			Status:        story.Status,
			Verdict:       story.Verdict,
			ValidationRef: story.ValidationRef,
			CommitRef:     story.CommitRef,
			CommitHash:    story.CommitHash,
			ResultPreview: truncateForReflux(story.ResultPreview, 240),
			Error:         story.Error,
		})
	}
	switch payload.Status {
	case workflowStatusCompleted:
		payload.SuggestedActions = []string{"status", "lookup"}
	case "blocked", "interrupted", "max_iterations":
		payload.SuggestedActions = []string{"wait", "rerun", "cancel"}
	case "stalled":
		payload.SuggestedActions = []string{"rerun", "cancel"}
	}

	rendered := renderLongTaskRefluxText(payload)
	msg := protocol.NewTextMessage(protocol.RoleAssistant, rendered)
	msg.Metadata = &protocol.Metadata{
		Kind:      protocol.KindLongTaskReflux,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}
	// Mark it ephemeral so the long task reflux does not pollute the
	// persisted transcript: the durable record lives in runs/<runID>.json.
	msg.Metadata.Ephemeral = true
	return msg
}

func renderLongTaskRefluxText(p longTaskRefluxPayload) string {
	var b strings.Builder
	b.WriteString("LongTask ")
	b.WriteString(p.LongTaskID)
	b.WriteString(": ")
	b.WriteString(p.Status)
	if p.Iterations > 0 {
		fmt.Fprintf(&b, " (iter=%d", p.Iterations)
		if len(p.Repaired) > 0 {
			fmt.Fprintf(&b, ", repaired=%d", len(p.Repaired))
		}
		b.WriteString(")")
	}
	b.WriteString("\n")
	for _, s := range p.Stories {
		marker := "[ ]"
		switch s.Status {
		case workflowStatusCompleted:
			marker = "[x]"
		case workflowStatusError:
			marker = "[!]"
		case workflowStatusCanceled:
			marker = "[-]"
		}
		fmt.Fprintf(&b, "  %s %s  %s", marker, s.StoryID, shortRefluxTitle(s))
		if s.CommitHash != "" {
			fmt.Fprintf(&b, "  (commit %s)", s.CommitHash[:minInt(8, len(s.CommitHash))])
		}
		if s.Error != "" {
			fmt.Fprintf(&b, "  err: %s", truncateForReflux(s.Error, 120))
		}
		b.WriteString("\n")
	}
	if p.BlockedBy != "" {
		fmt.Fprintf(&b, "  blocked by: %s", p.BlockedBy)
		if p.Message != "" {
			fmt.Fprintf(&b, " (%s)", p.Message)
		}
		b.WriteString("\n")
	}
	if len(p.SuggestedActions) > 0 {
		fmt.Fprintf(&b, "Suggested actions: %s\n", strings.Join(p.SuggestedActions, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func shortRefluxTitle(s longTaskRefluxStory) string {
	if s.ResultPreview != "" {
		return truncateForReflux(strings.SplitN(s.ResultPreview, "\n", 2)[0], 80)
	}
	return s.StoryID
}

func truncateForReflux(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// longTaskRefluxKey is the dedupe key for the T11 reflux message. The
// same Status with the same UpdatedAt nanosecond is treated as a
// no-op reflux; any change in either dimension triggers a fresh
// emission so the chat history reflects new progress.
func longTaskRefluxKey(runID, status string, updatedAt time.Time) string {
	return fmt.Sprintf("%s|%s|%d", runID, status, updatedAt.UnixNano())
}

// appendLongTaskReflux writes the longtask reflux assistant message to
// the agent's message history unless the same dedupe key was already
// recorded. Returns the key that was persisted (or the existing one
// if dedupe suppressed the emission). The new value of the run
// record's LastRefluxKey is written through the workflow store as a
// side effect so subsequent restarts can verify dedupe across
// process boundaries.
func (a *Agent) appendLongTaskReflux(view longTaskView, runID string) (string, error) {
	if view.LongTaskID == "" || runID == "" {
		return "", fmt.Errorf("longtask reflux requires longtask_id and run_id")
	}
	if args_no_reflux_disable(a) {
		return "", nil
	}
	if view.Run == nil {
		return "", fmt.Errorf("longtask reflux requires a run summary")
	}
	key := longTaskRefluxKey(runID, view.Run.Status, view.Run.UpdatedAt)
	rec, err := a.workflows.loadLongTaskRun(view.WorkflowID, runID)
	if err == nil && rec.LastRefluxKey == key {
		return key, nil
	}
	msg := buildLongTaskRefluxMessage(view, runID)
	a.appendMessage(msg)
	if err == nil {
		rec.LastRefluxKey = key
		rec.UpdatedAt = time.Now().UTC()
		_ = a.workflows.writeLongTaskRun(rec)
	}
	return key, nil
}

// args_no_reflux_disable returns true when the agent's current run was
// configured with NoReflux. The check is best-effort: if no
// in-flight args are recorded we default to enabled (so a call site
// that never had NoReflux in scope still refluxes).
func args_no_reflux_disable(a *Agent) bool {
	if a == nil {
		return false
	}
	if a.currentLongTaskArgs != nil && a.currentLongTaskArgs.NoReflux {
		return true
	}
	return false
}
