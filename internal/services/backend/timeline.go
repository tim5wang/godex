package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/tools"
)

// CompactionRecord describes one context compaction in a session. Events come
// from the session timeline (snapshot_ready + compacted=true) and historical
// records are recovered from summary messages persisted in session state so
// early compactions survive the recorder's rolling window.
type CompactionRecord struct {
	Timestamp     time.Time `json:"timestamp"`
	BeforeTokens  int       `json:"before_tokens,omitempty"`
	AfterTokens   int       `json:"after_tokens,omitempty"`
	Reasons       []string  `json:"reasons,omitempty"`
	Source        string    `json:"source,omitempty"`
	TranscriptRef string    `json:"transcript_ref,omitempty"`
}

var compactionTimestampPattern = regexp.MustCompile(`(?i)compressed\s+at\s*[:：]?\s*([0-9]{4}-[0-9]{2}-[0-9]{2}[ T][0-9]{2}:[0-9]{2})`)

// Compactions returns the full compaction history for one session. It merges
// durable snapshot_ready+compacted=true events from the session timeline with
// historical summary messages persisted in the session state, so early
// compactions that fell out of the recorder window are still reported.
func (s *Service) Compactions(ctx context.Context, sessionID string) ([]CompactionRecord, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}

	records := make([]CompactionRecord, 0)

	// 1) Durable timeline events (snapshot_ready + compacted=true).
	for _, event := range s.readSessionTimeline(sessionID) {
		if event.Type != events.EventSnapshotReady {
			continue
		}
		payload, ok := event.Payload.(map[string]any)
		if !ok {
			continue
		}
		if compacted, _ := payload["compacted"].(bool); !compacted {
			continue
		}
		records = append(records, CompactionRecord{
			Timestamp:    event.Timestamp,
			BeforeTokens: intValue(payload["token_estimate_before"]),
			AfterTokens:  intValue(payload["token_estimate_after"]),
			Reasons:      stringSliceValue(payload["compression_reasons"]),
			Source:       "snapshot_ready",
		})
	}

	// 2) Historical summary messages persisted in session state. These were
	// written by every compaction (auto and manual) and survive the recorder
	// window, so they recover compactions the timeline no longer holds.
	for _, msg := range session.agent.GetMessages() {
		if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindSummary {
			continue
		}
		records = append(records, CompactionRecord{
			Timestamp:     parseCompactionTimestamp(protocol.MessageText(msg)),
			Reasons:       []string{"summary"},
			Source:        "summary",
			TranscriptRef: strings.TrimSpace(msg.Metadata.Transcript),
		})
	}

	// Newest first, deduplicated by rounded timestamp + source.
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].Timestamp.After(records[j].Timestamp)
	})
	return dedupeCompactionRecords(records), nil
}

func parseCompactionTimestamp(text string) time.Time {
	if m := compactionTimestampPattern.FindStringSubmatch(text); len(m) > 1 {
		for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04"} {
			if ts, err := time.ParseInLocation(layout, m[1], time.Local); err == nil {
				return ts
			}
		}
	}
	return time.Time{}
}

func dedupeCompactionRecords(records []CompactionRecord) []CompactionRecord {
	seen := make(map[string]bool, len(records))
	out := make([]CompactionRecord, 0, len(records))
	for _, r := range records {
		key := r.Source + "|" + r.Timestamp.Truncate(time.Minute).Format(time.RFC3339)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

func intValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func stringSliceValue(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

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
