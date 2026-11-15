package workcontext

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func init() {
	migrations[12] = []string{
		`ALTER TABLE wc_context_sessions ADD COLUMN role TEXT NOT NULL DEFAULT 'member' CHECK(role IN ('member','lead','worker'))`,
		// Existing desktops get one stable lead, not whichever conversation was last used.
		`UPDATE wc_context_sessions SET role='worker' WHERE session_id LIKE 'taskboard-%'`,
		`UPDATE wc_context_sessions SET role='lead' WHERE id=(SELECT s.id FROM wc_context_sessions s JOIN wc_session_bindings b ON b.session_id=s.session_id AND b.tenant_id=s.tenant_id AND b.context_id=s.context_id WHERE s.tenant_id=wc_context_sessions.tenant_id AND s.context_id=wc_context_sessions.context_id AND s.role='member' ORDER BY s.created_at,s.id LIMIT 1)`,
		`CREATE UNIQUE INDEX wc_one_lead ON wc_context_sessions(tenant_id,context_id) WHERE role='lead'`,
		`ALTER TABLE wc_resources ADD COLUMN source TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE wc_events ADD COLUMN detail_json TEXT NOT NULL DEFAULT '{}'`,
	}
}

func promoteMainSessionTx(ctx context.Context, tx *sql.Tx, tenant, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE wc_context_sessions SET role='lead' WHERE tenant_id=? AND context_id=? AND id=(SELECT s.id FROM wc_context_sessions s JOIN wc_session_bindings b ON b.session_id=s.session_id AND b.tenant_id=s.tenant_id AND b.context_id=s.context_id WHERE s.tenant_id=? AND s.context_id=? AND s.role='member' ORDER BY s.created_at,s.id LIMIT 1) AND NOT EXISTS(SELECT 1 FROM wc_context_sessions WHERE tenant_id=? AND context_id=? AND role='lead')`, tenant, id, tenant, id, tenant, id)
	return err
}

// SetMainSession promotes an existing human conversation atomically. A worker
// keeps its execution role; it must not silently become the user's leader.
func (s *Store) SetMainSession(ctx context.Context, scope Scope, contextID, sessionID string) (SessionLink, error) {
	tenant, owner := scopeArgs(scope)
	if err := checkScopeWritable(scope); err != nil {
		return SessionLink{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionLink{}, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenant, owner, contextID); err != nil {
		return SessionLink{}, err
	}
	var link SessionLink
	err = tx.QueryRowContext(ctx, `SELECT id,session_id,topic,created_at,role FROM wc_context_sessions WHERE tenant_id=? AND context_id=? AND session_id=?`, tenant, contextID, strings.TrimSpace(sessionID)).Scan(&link.ID, &link.SessionID, &link.Topic, &link.CreatedAt, &link.Role)
	if err != nil {
		return SessionLink{}, notFound(err)
	}
	var primary string
	if err := tx.QueryRowContext(ctx, `SELECT context_id FROM wc_session_bindings WHERE session_id=? AND tenant_id=? AND owner_id=?`, link.SessionID, tenant, owner).Scan(&primary); err != nil {
		return SessionLink{}, notFound(err)
	}
	if primary != contextID {
		return SessionLink{}, fmt.Errorf("%w: conversation is primarily bound to another desktop", ErrConflict)
	}
	if link.Role == "worker" {
		return SessionLink{}, fmt.Errorf("%w: execution conversations cannot be the lead", ErrInvalid)
	}
	if link.Role != "lead" {
		if _, err := tx.ExecContext(ctx, `UPDATE wc_context_sessions SET role='member' WHERE tenant_id=? AND context_id=? AND role='lead'`, tenant, contextID); err != nil {
			return SessionLink{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE wc_context_sessions SET role='lead' WHERE tenant_id=? AND id=?`, tenant, link.ID); err != nil {
			return SessionLink{}, err
		}
		if err := recordEventTx(ctx, tx, tenant, contextID, "main_session_changed", "设置主对话："+link.Topic, "session://"+link.SessionID, "main:"+link.ID+":"+nowRFC3339(), nowRFC3339()); err != nil {
			return SessionLink{}, err
		}
	}
	link.Role = "lead"
	return link, tx.Commit()
}

func (s *Store) SessionRole(ctx context.Context, scope Scope, contextID, sessionID string) (string, error) {
	tenant, owner := scopeArgs(scope)
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT s.role FROM wc_context_sessions s JOIN wc_contexts c ON c.tenant_id=s.tenant_id AND c.id=s.context_id WHERE c.tenant_id=? AND c.owner_id=? AND c.id=? AND s.session_id=?`, tenant, owner, contextID, sessionID).Scan(&role)
	return role, notFound(err)
}

// DelegationBinding is a backend-only ownership lookup from the persisted
// delegation, never from a prompt or a caller-supplied desktop id.
func (s *Store) DelegationBinding(ctx context.Context, cardID string) (Scope, string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.tenant_id,c.owner_id,c.id FROM wc_context_tasks t JOIN wc_contexts c ON c.tenant_id=t.tenant_id AND c.id=t.context_id WHERE t.card_id=?`, cardID)
	if err != nil {
		return Scope{}, "", err
	}
	defer rows.Close()
	var scope Scope
	var id string
	count := 0
	for rows.Next() {
		if err := rows.Scan(&scope.TenantID, &scope.OwnerID, &id); err != nil {
			return Scope{}, "", err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return Scope{}, "", err
	}
	if count == 0 {
		return Scope{}, "", ErrNotFound
	}
	if count != 1 {
		return Scope{}, "", ErrConflict
	}
	return scope, id, nil
}
