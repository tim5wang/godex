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
