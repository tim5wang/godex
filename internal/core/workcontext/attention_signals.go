package workcontext

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// This file extends the cross-stream attention read models beyond cards and due
// dates. The original attention.go covered "a delegated card awaits acceptance"
// and "a plan item is due"; an inbox built only from those two sources forces
// the user to still scan the rest of the desktop by hand, which defeats the
// point of a single triage list.
//
// Two rules hold for every projection here:
//
//  1. It is stream-scoped. Each query joins wc_contexts and filters on
//     owner_id, so a cross-stream inbox can never surface one owner's work to
//     another. Signals that cannot be attributed to an owner are deliberately
//     NOT collected (see the flow-run note in the design doc §AE1).
//  2. It reuses existing tables. No new schema: candidate facts, blocked plan
//     items, checkpoint blockers/questions and dependency edges all already
//     have a home.

// CandidateFact is a fact awaiting human review, carried with its stream title
// so one row can be labelled without a second lookup.
type CandidateFact struct {
	Fact
	ContextTitle string `json:"context_title"`
}

// ListCandidateFacts returns facts in the "candidate" state across this owner's
// streams, newest first. A candidate is the one fact state that needs a person:
// confirmed/stale/superseded/rejected are all terminal for triage purposes.
func (s *Store) ListCandidateFacts(ctx context.Context, scope Scope, limit int) ([]CandidateFact, error) {
	tenantID, ownerID := scopeArgs(scope)
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.context_id, f.title, f.content, f.kind, f.status, f.scope, f.subject,
		       f.predicate, f.source, f.source_excerpt, f.source_revision, f.observed_at,
		       f.verified_at, f.provenance, f.supersedes_id, f.memory_candidate_id, f.version,
		       f.created_at, f.valid_until, c.title
		FROM wc_facts f
		JOIN wc_contexts c ON c.tenant_id = f.tenant_id AND c.id = f.context_id
		WHERE f.tenant_id = ? AND c.owner_id = ? AND c.status <> 'archived'
		  AND f.status = 'candidate'
		ORDER BY f.created_at DESC, f.id
		LIMIT ?`, tenantID, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CandidateFact{}
	for rows.Next() {
		var item CandidateFact
		if err := rows.Scan(&item.ID, &item.ContextID, &item.Title, &item.Content, &item.Kind,
			&item.Status, &item.Scope, &item.Subject, &item.Predicate, &item.Source,
			&item.SourceExcerpt, &item.SourceRevision, &item.ObservedAt, &item.VerifiedAt,
			&item.Provenance, &item.SupersedesID, &item.MemoryCandidateID, &item.Version,
			&item.CreatedAt, &item.ValidUntil, &item.ContextTitle); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListBlockedPlanTodos returns plan items explicitly marked "blocked", across
// this owner's streams. ListOpenPlanTodos cannot serve this: it filters on
// due_at <> ” because it exists for deadlines, while a blocked item is stuck
// whether or not anyone gave it a date — which is exactly the case a person
// needs reminded about.
func (s *Store) ListBlockedPlanTodos(ctx context.Context, scope Scope, limit int) ([]PlanTodo, error) {
	tenantID, ownerID := scopeArgs(scope)
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.context_id, t.text, t.status, t.assignee, t.due_at, t.source,
		       t.position, t.version, t.created_at, t.updated_at, c.title
		FROM wc_todos t
		JOIN wc_contexts c ON c.tenant_id = t.tenant_id AND c.id = t.context_id
		WHERE t.tenant_id = ? AND c.owner_id = ? AND c.status <> 'archived'
		  AND t.status = 'blocked'
		ORDER BY t.created_at, t.id
		LIMIT ?`, tenantID, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlanTodo{}
	for rows.Next() {
		var item PlanTodo
		if err := rows.Scan(&item.ID, &item.ContextID, &item.Text, &item.Status, &item.Owner,
			&item.DueAt, &item.Source, &item.Position, &item.Version, &item.CreatedAt,
			&item.UpdatedAt, &item.ContextTitle); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// DownstreamBlockedCounts returns, per stream, how many OTHER streams declare a
// depends_on edge onto it — i.e. how many streams are waiting on this one. This
// is the leverage dimension the inbox previously lacked: a stalled one-item
// stream nobody depends on, and a stalled stream three others are blocked on,
// are not worth the same attention.
//
// Direction: AddRelation(contextID -> toID) means "contextID depends on toID",
// so the count for a stream X is the number of rows whose to_id is X.
func (s *Store) DownstreamBlockedCounts(ctx context.Context, scope Scope) (map[string]int, error) {
	tenantID, ownerID := scopeArgs(scope)
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.to_id, COUNT(*)
		FROM wc_relations r
		JOIN wc_contexts c ON c.tenant_id = r.tenant_id AND c.id = r.to_id
		WHERE r.tenant_id = ? AND c.owner_id = ? AND c.status <> 'archived'
		  AND r.kind = 'depends_on' AND r.from_id <> r.to_id
		GROUP BY r.to_id`, tenantID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		out[id] = count
	}
	return out, rows.Err()
}

// NewestUnreadEventAt returns, per non-archived stream, the timestamp of its most
// recent unread event. It is the input to aging: a badge nobody can clear stops
// being a call to action once it is old, and an aged count is rendered as a
// de-emphasised summary instead of a red dot.
//
// A stream with nothing unread is ABSENT from the map rather than present with a
// zero time, so callers cannot mistake "never happened" for "long ago".
func (s *Store) NewestUnreadEventAt(ctx context.Context, scope Scope) (map[string]time.Time, error) {
	tenantID, ownerID := scopeArgs(scope)
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, MAX(e.created_at)
		FROM wc_contexts c
		LEFT JOIN wc_seen s ON s.tenant_id = c.tenant_id AND s.context_id = c.id
		LEFT JOIN wc_events e ON e.tenant_id = c.tenant_id AND e.context_id = c.id
			AND e.created_at > COALESCE(s.last_seen_at, '')
		WHERE c.tenant_id = ? AND c.owner_id = ? AND c.status <> 'archived'
		GROUP BY c.id`, tenantID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var latest sql.NullString
		if err := rows.Scan(&id, &latest); err != nil {
			return nil, err
		}
		// A stream with no unread rows yields NULL, which must not become a time.
		if !latest.Valid {
			continue
		}
		if parsed, ok := ParseDueAt(latest.String); ok {
			out[id] = parsed
		}
	}
	return out, rows.Err()
}

// EventStamp is one recorded event reduced to what a cadence calculation needs.
type EventStamp struct {
	ContextID string
	Kind      string
	At        time.Time
}

// RecentEventStamps returns recent events across the scope's non-archived streams,
// newest first, so the attention inbox can learn each stream's OWN pace.
//
// Why a stream's own pace: one global staleness threshold is wrong in both directions.
// A stream that genuinely moves twice a week is not stale after three days, and a
// stream that moves hourly is stale long before that. The stride between a stream's own
// recent events is the only honest scale for "has this gone quiet?".
//
// Kind is returned so the caller can exclude events the attention system itself wrote:
// a reminder must never count as the stream's own activity, or nudging a quiet stream
// would make it look busy and reset the very staleness that triggered the reminder.
func (s *Store) RecentEventStamps(ctx context.Context, scope Scope, lookback time.Duration, limit int) ([]EventStamp, error) {
	tenantID, ownerID := scopeArgs(scope)
	if limit <= 0 {
		limit = 2000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.context_id, e.kind, e.created_at
		FROM wc_events e
		JOIN wc_contexts c ON c.tenant_id = e.tenant_id AND c.id = e.context_id
		WHERE c.tenant_id = ? AND c.owner_id = ? AND c.status <> 'archived'
			AND e.created_at >= ?
		ORDER BY e.created_at DESC
		LIMIT ?`, tenantID, ownerID, formatStamp(time.Now().Add(-lookback)), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventStamp{}
	for rows.Next() {
		var stamp EventStamp
		var createdAt string
		if err := rows.Scan(&stamp.ContextID, &stamp.Kind, &createdAt); err != nil {
			return nil, err
		}
		at, ok := ParseDueAt(createdAt)
		if !ok {
			// An unparseable timestamp is skipped rather than treated as "now": a cadence
			// built on a bad value would silently move a threshold.
			continue
		}
		stamp.At = at
		out = append(out, stamp)
	}
	return out, rows.Err()
}

// CountEventsByKindSinceForContext counts nudges already delivered for ONE stream
// since a time. The sweep uses it so a single busy stream cannot spend the owner's
// entire daily notification budget on its own.
func (s *Store) CountEventsByKindSinceForContext(ctx context.Context, scope Scope, contextID, kind string, since time.Time) (int, error) {
	tenantID, ownerID := scopeArgs(scope)
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wc_events e
		JOIN wc_contexts c ON c.tenant_id = e.tenant_id AND c.id = e.context_id
		WHERE e.tenant_id = ? AND c.owner_id = ? AND e.context_id = ?
		  AND e.kind = ? AND e.created_at >= ?`,
		tenantID, ownerID, strings.TrimSpace(contextID), strings.TrimSpace(kind),
		formatStamp(since)).Scan(&count)
	return count, err
}
