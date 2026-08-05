package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/tools"
	"strconv"
	"strings"
	"time"
)

func (s *Service) ContextSummary(ctx context.Context, sessionID string) (tools.ContextInspection, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.ContextInspection{}, err
	}
	return session.agent.InspectContext(ctx, sessionID)
}

// SessionSummary returns a lightweight runtime summary for one session.
func (s *Service) SessionSummary(ctx context.Context, sessionID string) (tools.SessionSummary, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.SessionSummary{}, err
	}
	snapshot := s.snapshotFromSession(session)
	return tools.SessionSummary{
		SessionID:              snapshot.SessionID,
		Channel:                strings.TrimSpace(snapshot.Locator.Channel),
		Key:                    strings.TrimSpace(snapshot.Locator.Key),
		UserID:                 strings.TrimSpace(snapshot.Locator.UserID),
		MessageCount:           len(snapshot.Messages),
		ActiveSkillCount:       len(snapshot.ActiveSkills),
		PendingPermissionCount: len(snapshot.PendingPermissions),
		Running:                snapshot.Running,
		UpdatedAt:              snapshot.UpdatedAt,
	}, nil
}

// Timeline returns recent structured runtime events for one session.
func (s *Service) Timeline(ctx context.Context, sessionID string, limit int) ([]events.Event, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	if session.timeline == nil {
		return nil, nil
	}
	return session.timeline.Entries(limit), nil
}

// TimelinePage returns a durable newest-first page sourced from the full
// session event journal when available.
func (s *Service) TimelinePage(ctx context.Context, sessionID string, req TimelinePageRequest) (TimelinePage, error) {
	_ = ctx
	if _, err := s.requireSession(sessionID); err != nil {
		return TimelinePage{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := 0
	if strings.TrimSpace(req.Cursor) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(req.Cursor))
		if err != nil || parsed < 0 {
			return TimelinePage{}, fmt.Errorf("invalid timeline cursor")
		}
		offset = parsed
	}

	filtered := filterTimelineEvents(s.readSessionTimeline(sessionID), req)
	reverseTimelineEvents(filtered)
	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	items := append([]events.Event(nil), filtered[offset:end]...)
	page := TimelinePage{
		Items:   items,
		Total:   total,
		HasMore: end < total,
	}
	if page.HasMore {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func filterTimelineEvents(items []events.Event, req TimelinePageRequest) []events.Event {
	typeSet := make(map[string]struct{})
	for _, typ := range req.Types {
		typ = strings.TrimSpace(typ)
		if typ != "" {
			typeSet[typ] = struct{}{}
		}
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))
	jobID := strings.TrimSpace(req.JobID)
	turnID := strings.TrimSpace(req.TurnID)
	out := make([]events.Event, 0, len(items))
	for _, item := range items {
		if len(typeSet) > 0 {
			if _, ok := typeSet[string(item.Type)]; !ok {
				continue
			}
		}
		if turnID != "" && item.TurnID != turnID {
			continue
		}
		if jobID != "" && timelinePayloadString(item.Payload, "job_id") != jobID {
			continue
		}
		if query != "" && !strings.Contains(timelineSearchText(item), query) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func reverseTimelineEvents(items []events.Event) {
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
}

func timelineSearchText(item events.Event) string {
	parts := []string{
		string(item.Type),
		item.SessionID,
		item.TurnID,
		item.Timestamp.Format(time.RFC3339Nano),
	}
	if data, err := json.Marshal(item.Payload); err == nil {
		parts = append(parts, string(data))
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func timelinePayloadString(payload any, key string) string {
	if payload == nil || key == "" {
		return ""
	}
	if values, ok := payload.(map[string]any); ok {
		if value, ok := values[key]; ok {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return ""
	}
	if value, ok := values[key]; ok {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return ""
}

// ListSubagents returns durable subagent jobs scoped to one session.
