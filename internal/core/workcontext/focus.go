package workcontext

import (
	"context"
	"strings"
)

// Focus summarizes what a viewer should notice about a stream: plan items and the
// events recorded since the stream was last seen. It is read-only — marking the
// stream seen is a separate, explicit call so merely opening the desktop does not
// silently clear the "what changed" highlight.
type Focus struct {
	LastSeenAt string  `json:"last_seen_at"`
	Changes    []Event `json:"changes"`
	Todos      []Todo  `json:"todos"`
}

// lastSeenChangesLimit caps how many change events a focus view returns.
const lastSeenChangesLimit = 50

// LastSeen returns the stored "last seen" watermark for a stream, or "" when it
// has never been marked seen.
func (s *Store) LastSeen(ctx context.Context, scope Scope, contextID string) (string, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	if _, err := s.getContext(ctx, tenantID, ownerID, contextID); err != nil {
		return "", err
	}
	var seen string
	err := s.db.QueryRowContext(ctx, `SELECT last_seen_at FROM wc_seen WHERE tenant_id=? AND context_id=?`, tenantID, contextID).Scan(&seen)
	if err != nil {
		// No row yet: the stream has never been marked seen.
		return "", nil
	}
	return seen, nil
}

// markSeen sets the watermark to now. Viewing is not an edit, so this never
// touches the context version.
func (s *Store) markSeen(ctx context.Context, scope Scope, contextID string) error {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	if err := checkScopeWritable(scope); err != nil {
		return err
	}
	now := nowRFC3339()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO wc_seen(tenant_id, context_id, owner_id, last_seen_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(tenant_id, context_id) DO UPDATE SET last_seen_at=excluded.last_seen_at, owner_id=excluded.owner_id`,
		tenantID, contextID, ownerID, now)
	return err
}

// MarkSeen records that the caller has now seen the stream and returns the
// previous watermark (so the caller can report what had changed until now).
func (s *Store) MarkSeen(ctx context.Context, scope Scope, contextID string) (string, error) {
	previous, err := s.LastSeen(ctx, scope, contextID)
	if err != nil {
		return "", err
	}
	if err := s.markSeen(ctx, scope, contextID); err != nil {
		return "", err
	}
	return previous, nil
}

// eventsSince returns events created strictly after the given watermark, newest
// first, bounded by lastSeenChangesLimit. An empty watermark means "everything on
// record" (first look).
func (s *Store) eventsSince(ctx context.Context, tenantID, contextID, since string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, context_id, kind, title, source, created_at FROM wc_events
		WHERE tenant_id=? AND context_id=? AND created_at > ?
		ORDER BY created_at DESC, id DESC LIMIT ?`,
		tenantID, contextID, since, lastSeenChangesLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var item Event
		if err := rows.Scan(&item.ID, &item.ContextID, &item.Kind, &item.Title, &item.Source, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Focus assembles the plan items and the changes since the stream was last seen.
// It does not mutate the watermark.
func (s *Store) Focus(ctx context.Context, scope Scope, contextID string) (Focus, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	if _, err := s.getContext(ctx, tenantID, ownerID, contextID); err != nil {
		return Focus{}, err
	}
	seen, err := s.LastSeen(ctx, scope, contextID)
	if err != nil {
		return Focus{}, err
	}
	changes, err := s.eventsSince(ctx, tenantID, contextID, seen)
	if err != nil {
		return Focus{}, err
	}
	todos, err := s.listTodos(ctx, tenantID, contextID)
	if err != nil {
		return Focus{}, err
	}
	return Focus{LastSeenAt: seen, Changes: changes, Todos: todos}, nil
}
