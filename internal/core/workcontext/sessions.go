package workcontext

import (
	"context"
	"fmt"
	"strings"
)

// SessionBinding resolves the work context bound to a session for trusted,
// backend-only per-turn context injection. It is not exposed to end users: the
// caller must already be inside the trusted backend.
//
// A session identity must not collapse two owners into one. If the same
// session_id is linked to contexts owned by different principals the lookup
// fails with ErrConflict rather than guessing, which would leak one principal's
// desktop into another's turn. When several contexts of the one owner are found,
// the most recently updated context is returned deterministically.
func (s *Store) SessionBinding(ctx context.Context, sessionID string) (Scope, string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Scope{}, "", fmt.Errorf("%w: session_id is required", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.tenant_id, c.owner_id, c.id, c.updated_at
		FROM wc_context_sessions s
		JOIN wc_contexts c ON c.tenant_id = s.tenant_id AND c.id = s.context_id
		WHERE s.session_id=?
		ORDER BY c.updated_at DESC, c.id`, sessionID)
	if err != nil {
		return Scope{}, "", err
	}
	type binding struct {
		scope     Scope
		contextID string
	}
	bindings := []binding{}
	for rows.Next() {
		var tenantID, ownerID, contextID, updatedAt string
		if err := rows.Scan(&tenantID, &ownerID, &contextID, &updatedAt); err != nil {
			rows.Close()
			return Scope{}, "", err
		}
		_ = updatedAt // ordering is applied in SQL
		bindings = append(bindings, binding{scope: Scope{TenantID: tenantID, OwnerID: ownerID}, contextID: contextID})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Scope{}, "", err
	}
	rows.Close()
	if len(bindings) == 0 {
		return Scope{}, "", ErrNotFound
	}
	first := bindings[0].scope
	for _, b := range bindings[1:] {
		if b.scope != first {
			return Scope{}, "", fmt.Errorf("%w: session %s is bound to multiple owners", ErrConflict, sessionID)
		}
	}
	return first, bindings[0].contextID, nil
}

// SessionTurnEventKind is the wc_events kind recorded when a conversation linked to a
// work stream finishes a turn.
//
// It lives here rather than in the backend because two layers need the same string: the
// turn path writes it and the desktop projection reads it. A local constant in either one
// would silently drift from the other's copy, and the only symptom would be a card that
// never reports a finished conversation.
const SessionTurnEventKind = "session_turn"

// SessionIndex returns, per non-archived stream, the sessions linked to it.
//
// It exists because the desktop list must answer two questions about a stream the
// user has NOT opened: "is one of its conversations running right now?" and "which
// conversation did I last use?". Both need a stream's links, and reading each
// stream's full detail would be N Get calls for one list. One owner-scoped
// projection instead, archived streams excluded here so the caller never has to
// filter them out.
func (s *Store) SessionIndex(ctx context.Context, scope Scope) (map[string][]SessionLink, error) {
	tenantID, ownerID := scopeArgs(scope)
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.context_id, s.id, s.session_id, s.topic, s.created_at
		FROM wc_context_sessions s
		JOIN wc_contexts c ON c.tenant_id = s.tenant_id AND c.id = s.context_id
		WHERE s.tenant_id = ? AND c.owner_id = ? AND c.status <> 'archived'
		ORDER BY s.created_at, s.id`, tenantID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]SessionLink{}
	for rows.Next() {
		var contextID string
		var item SessionLink
		if err := rows.Scan(&contextID, &item.ID, &item.SessionID, &item.Topic, &item.CreatedAt); err != nil {
			return nil, err
		}
		out[contextID] = append(out[contextID], item)
	}
	return out, rows.Err()
}

// NewestSessionTurnByContext returns, per stream, the newest event written by a
// linked conversation finishing a turn.
//
// The desktop list has to answer "did this stream's conversation finish while I was
// looking elsewhere?" for every stream at once, and "there is an unread change" is not
// enough: it does not say whether the change was a finished conversation, a checkpoint
// edit or a plan item. This is the one signal that names it, projected in one query
// instead of an events read per stream. Only the newest row per stream is kept, because
// the card shows the latest state, not a log.
func (s *Store) NewestSessionTurnByContext(ctx context.Context, scope Scope, kind string) (map[string]Event, error) {
	tenantID, ownerID := scopeArgs(scope)
	kind = strings.TrimSpace(kind)
	out := map[string]Event{}
	if kind == "" {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.context_id, e.id, e.kind, e.title, e.source, e.created_at
		FROM wc_events e
		JOIN wc_contexts c ON c.tenant_id = e.tenant_id AND c.id = e.context_id
		WHERE e.tenant_id = ? AND c.owner_id = ? AND c.status <> 'archived' AND e.kind = ?
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT 500`, tenantID, ownerID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var contextID string
		var item Event
		if err := rows.Scan(&contextID, &item.ID, &item.Kind, &item.Title, &item.Source, &item.CreatedAt); err != nil {
			return nil, err
		}
		// Rows arrive newest first, so the first row seen for a stream is its newest.
		if _, seen := out[contextID]; seen {
			continue
		}
		item.ContextID = contextID
		out[contextID] = item
	}
	return out, rows.Err()
}
