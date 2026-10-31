package workcontext

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// This file holds the cross-stream read models behind the desktop's attention
// inbox and its background nudge sweep. Nudges deliberately reuse wc_events
// (whose dedup_key has a UNIQUE constraint) instead of a new table: a delivered
// reminder then also shows up in the stream's own "what changed" list, and
// "have we already nudged about this?" is just a dedup insert. No new schema.

// OwnedScope is one (tenant, owner) pair that owns work streams. The background
// sweep walks these instead of guessing identities, because a background job has
// no request principal to derive a scope from.
type OwnedScope struct {
	Scope
	ContextCount int
}

// ListOwnedScopes returns every (tenant, owner) pair with at least one
// non-archived stream.
func (s *Store) ListOwnedScopes(ctx context.Context) ([]OwnedScope, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id, owner_id, COUNT(*) FROM wc_contexts
		WHERE status <> 'archived'
		GROUP BY tenant_id, owner_id
		ORDER BY tenant_id, owner_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OwnedScope{}
	for rows.Next() {
		var item OwnedScope
		if err := rows.Scan(&item.TenantID, &item.OwnerID, &item.ContextCount); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// RecordEventIfNew writes an event and reports whether it was new. Callers use
// the boolean as an idempotency gate: only the first write for a dedup key
// should trigger a side effect (a push), so a sweep that runs every minute does
// not re-notify about the same card version.
func (s *Store) RecordEventIfNew(ctx context.Context, scope Scope, contextID, kind, title, source, dedup string) (bool, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	if err := requiredText("kind", kind); err != nil {
		return false, err
	}
	if err := checkScopeWritable(scope); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return false, err
	}
	inserted, err := recordEventIfNewTx(ctx, tx, tenantID, contextID, kind, title, source, dedup, nowRFC3339())
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return inserted, nil
}

// recordEventIfNewTx is recordEventTx plus the insert/no-op outcome.
func recordEventIfNewTx(ctx context.Context, tx *sql.Tx, tenantID, contextID, kind, title, source, dedup, createdAt string) (bool, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO wc_events(tenant_id, id, context_id, kind, title, source, dedup_key, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id, context_id, dedup_key) DO NOTHING`,
		tenantID, newID(), contextID, kind, title, source, dedup, createdAt)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// CountEventsByKindSince counts events of a kind written since a timestamp. The
// sweep uses it to enforce a per-day notification budget.
func (s *Store) CountEventsByKindSince(ctx context.Context, scope Scope, kind string, since time.Time) (int, error) {
	tenantID, ownerID := scopeArgs(scope)
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wc_events e
		JOIN wc_contexts c ON c.tenant_id = e.tenant_id AND c.id = e.context_id
		WHERE e.tenant_id = ? AND c.owner_id = ? AND e.kind = ? AND e.created_at >= ?`,
		tenantID, ownerID, strings.TrimSpace(kind), since.UTC().Format(time.RFC3339Nano)).Scan(&count)
	return count, err
}

// PlanTodo is a plan item carried with its stream title, so the inbox can label
// an entry without a second lookup per row.
type PlanTodo struct {
	Todo
	ContextTitle string `json:"context_title"`
}

// OpenDelegation is a delegated task carried with its stream title. The card's
// live status is NOT stored here — the caller reads it from the taskboard, so a
// missing card can never masquerade as progress.
type OpenDelegation struct {
	Delegation
	// ContextID and ContextTitle are added by this cross-stream projection; the
	// per-stream Delegation deliberately does not carry them (they would be
	// redundant inside a Detail that already knows its stream).
	ContextID    string `json:"context_id"`
	ContextTitle string `json:"context_title"`
}

// ListOpenPlanTodos returns every not-done plan item with a due time, across this
// owner's streams. Ordering is the caller's job: due_at is a text column and
// comparing RFC3339 strings lexicographically is wrong when offsets differ
// (Z vs +08:00), so the caller parses before sorting.
func (s *Store) ListOpenPlanTodos(ctx context.Context, scope Scope, limit int) ([]PlanTodo, error) {
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
		  AND t.status <> 'done' AND t.due_at <> ''
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

// ListOpenDelegations returns delegations that carry a card id, across this
// owner's streams.
func (s *Store) ListOpenDelegations(ctx context.Context, scope Scope, limit int) ([]OpenDelegation, error) {
	tenantID, ownerID := scopeArgs(scope)
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.request_id, d.card_id, d.project_id, d.title, d.pack, d.state,
		       d.created_at, d.context_id, c.title
		FROM wc_context_tasks d
		JOIN wc_contexts c ON c.tenant_id = d.tenant_id AND c.id = d.context_id
		WHERE d.tenant_id = ? AND c.owner_id = ? AND c.status <> 'archived' AND d.card_id <> ''
		ORDER BY d.created_at DESC, d.id
		LIMIT ?`, tenantID, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpenDelegation{}
	for rows.Next() {
		var item OpenDelegation
		if err := rows.Scan(&item.ID, &item.RequestID, &item.CardID, &item.ProjectID,
			&item.Title, &item.Pack, &item.State, &item.CreatedAt, &item.ContextID, &item.ContextTitle); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// DueState classifies a plan item against a reference time.
type DueState string

const (
	DueNone    DueState = ""
	DueSoon    DueState = "due_soon"
	DueOverdue DueState = "overdue"
)

// ParseDueAt parses a stored due time, tolerating both offset forms and a bare
// date. A value that does not parse is reported as absent rather than overdue: a
// reminder built on an unparseable timestamp would be a lie.
func ParseDueAt(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// DueStateOf reports whether a plan item is overdue or due within the window.
func DueStateOf(dueAt string, now time.Time, window time.Duration) DueState {
	parsed, ok := ParseDueAt(dueAt)
	if !ok {
		return DueNone
	}
	switch {
	case !parsed.After(now):
		return DueOverdue
	case parsed.Before(now.Add(window)):
		return DueSoon
	default:
		return DueNone
	}
}

// UnreadCounts returns, per non-archived stream, how many events were recorded
// after that stream was last seen. One query rather than a Focus call per
// stream, because the desktop wall needs a badge for every stream at once. A
// stream that was never marked seen counts all of its events.
func (s *Store) UnreadCounts(ctx context.Context, scope Scope) (map[string]int, error) {
	tenantID, ownerID := scopeArgs(scope)
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, COUNT(e.id)
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
