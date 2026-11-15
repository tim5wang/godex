package workcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// EventDetail is read on demand, not injected wholesale into every chat turn.
// Checkpoints are resolved to the immutable revision, never the current pointer.
type EventDetail struct {
	Event
	Checkpoint *Checkpoint `json:"checkpoint,omitempty"`
	SessionID  string      `json:"session_id,omitempty"`
	TurnID     string      `json:"turn_id,omitempty"`
	Request    string      `json:"request,omitempty"`
	Result     string      `json:"result,omitempty"`
	Status     string      `json:"status,omitempty"`
	Target     string      `json:"target,omitempty"`
}

func (s *Store) EventDetail(ctx context.Context, scope Scope, contextID, eventID string) (EventDetail, error) {
	tenant, owner := scopeArgs(scope)
	var event Event
	var key, raw string
	err := s.db.QueryRowContext(ctx, `SELECT e.id,e.context_id,e.kind,e.title,e.source,e.created_at,e.dedup_key,e.detail_json FROM wc_events e JOIN wc_contexts c ON c.tenant_id=e.tenant_id AND c.id=e.context_id WHERE c.tenant_id=? AND c.owner_id=? AND c.id=? AND e.id=?`, tenant, owner, contextID, eventID).Scan(&event.ID, &event.ContextID, &event.Kind, &event.Title, &event.Source, &event.CreatedAt, &key, &raw)
	if err != nil {
		return EventDetail{}, notFound(err)
	}
	var out EventDetail
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return EventDetail{}, err
	}
	out.Event = event
	if event.Kind == "checkpoint_saved" && strings.HasPrefix(key, "checkpoint:") {
		rev, err := strconv.Atoi(strings.TrimPrefix(key, "checkpoint:"))
		if err != nil {
			return EventDetail{}, fmt.Errorf("%w: invalid checkpoint revision", ErrInvalid)
		}
		var payload string
		if err := s.db.QueryRowContext(ctx, `SELECT payload_json FROM wc_checkpoints WHERE tenant_id=? AND context_id=? AND revision=?`, tenant, contextID, rev).Scan(&payload); err != nil {
			return EventDetail{}, notFound(err)
		}
		cp := decodeCheckpoint(payload)
		out.Checkpoint = &cp
	}
	if strings.HasPrefix(event.Source, "session://") {
		out.SessionID = strings.TrimPrefix(event.Source, "session://")
		if _, turn, ok := strings.Cut(key, ":turn:"); ok {
			out.TurnID = turn
		}
	}
	if out.Target == "" {
		switch {
		case strings.HasPrefix(event.Source, "taskboard://"):
			out.Target = "delegations"
		case strings.HasPrefix(event.Source, "resource://"):
			out.Target = "resources"
		}
	}
	return out, nil
}

func eventPreview(text string) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > 100 {
		return string(runes[:100]) + "…"
	}
	return string(runes)
}
