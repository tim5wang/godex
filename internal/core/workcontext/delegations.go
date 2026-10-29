package workcontext

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Reservation states for a delegation.
const (
	delegationStateReserved  = "reserved"
	delegationStateCompleted = "completed"
)

// ReserveDelegation records a delegation reservation keyed by request_id. The
// same request_id returns the existing reservation unchanged (and never
// mutates the already-captured pack), making retries idempotent.
func (s *Store) ReserveDelegation(ctx context.Context, scope Scope, contextID string, input Delegation) (Delegation, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	requestID := strings.TrimSpace(input.RequestID)
	if err := requiredText("request_id", requestID); err != nil {
		return Delegation{}, err
	}
	if err := checkScopeWritable(scope); err != nil {
		return Delegation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Delegation{}, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return Delegation{}, err
	}
	// Idempotent replay: return the stored reservation without touching its pack.
	if existing, err := getDelegationByRequestTx(ctx, tx, tenantID, contextID, requestID); err == nil {
		if err := tx.Commit(); err != nil {
			return Delegation{}, err
		}
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Delegation{}, err
	}
	pack := strings.TrimSpace(input.Pack)
	if pack == "" {
		pack = "{}"
	}
	now := nowRFC3339()
	item := Delegation{
		ID:        newID(),
		RequestID: requestID,
		CardID:    input.CardID,
		ProjectID: input.ProjectID,
		Title:     input.Title,
		Pack:      pack,
		State:     delegationStateReserved,
		CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wc_context_tasks(tenant_id, id, context_id, request_id, card_id, project_id, title, pack, state, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tenantID, item.ID, contextID, item.RequestID, item.CardID, item.ProjectID, item.Title, item.Pack, item.State, now, now); err != nil {
		return Delegation{}, err
	}
	if err := recordEventTx(ctx, tx, tenantID, contextID, "delegation_reserved",
		"预留委派："+item.Title, "store", "delegation:"+requestID, now); err != nil {
		return Delegation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Delegation{}, err
	}
	return item, nil
}

// CompleteDelegation attaches the created card/project to an existing
// reservation and marks it completed. Completing twice with the same card is
// idempotent; a different card returns ErrConflict.
func (s *Store) CompleteDelegation(ctx context.Context, scope Scope, contextID, id, cardID, projectID string) (Delegation, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	id = strings.TrimSpace(id)
	if err := requiredText("id", id); err != nil {
		return Delegation{}, err
	}
	if err := checkScopeWritable(scope); err != nil {
		return Delegation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Delegation{}, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return Delegation{}, err
	}
	item, err := getDelegationTx(ctx, tx, tenantID, contextID, id)
	if err != nil {
		return Delegation{}, err
	}
	cardID = strings.TrimSpace(cardID)
	if item.State == delegationStateCompleted {
		if item.CardID == cardID {
			if err := tx.Commit(); err != nil {
				return Delegation{}, err
			}
			return item, nil
		}
		return Delegation{}, fmt.Errorf("%w: delegation already completed with another card", ErrConflict)
	}
	now := nowRFC3339()
	if projectID != "" {
		item.ProjectID = projectID
	}
	item.CardID = cardID
	item.State = delegationStateCompleted
	if _, err := tx.ExecContext(ctx, `
		UPDATE wc_context_tasks SET card_id=?, project_id=?, state=?, updated_at=?
		WHERE tenant_id=? AND id=? AND context_id=?`,
		item.CardID, item.ProjectID, item.State, now, tenantID, id, contextID); err != nil {
		return Delegation{}, err
	}
	if err := recordEventTx(ctx, tx, tenantID, contextID, "delegation_completed",
		fmt.Sprintf("委派已建卡 %s", item.CardID), "store", "delegation_complete:"+id+":"+item.CardID, now); err != nil {
		return Delegation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Delegation{}, err
	}
	return item, nil
}

// RecordEvent appends an audit event. A non-empty dedup key collapses duplicate
// events within the same context, making automated recording idempotent.
func (s *Store) RecordEvent(ctx context.Context, scope Scope, contextID, kind, title, source, dedup string) error {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	if err := requiredText("kind", kind); err != nil {
		return err
	}
	if err := checkScopeWritable(scope); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return err
	}
	if err := recordEventTx(ctx, tx, tenantID, contextID, kind, title, source, dedup, nowRFC3339()); err != nil {
		return err
	}
	return tx.Commit()
}

// --- helpers ---

// recordEventTx inserts an event row, ignoring duplicate dedup keys.
func recordEventTx(ctx context.Context, tx *sql.Tx, tenantID, contextID, kind, title, source, dedup, createdAt string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO wc_events(tenant_id, id, context_id, kind, title, source, dedup_key, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id, context_id, dedup_key) DO NOTHING`,
		tenantID, newID(), contextID, kind, title, source, dedup, createdAt)
	return err
}

// getDelegationTx loads one delegation scoped by tenant and context.
func getDelegationTx(ctx context.Context, tx *sql.Tx, tenantID, contextID, id string) (Delegation, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, request_id, card_id, project_id, title, pack, state, created_at
		FROM wc_context_tasks WHERE tenant_id=? AND context_id=? AND id=?`, tenantID, contextID, id)
	return scanDelegationRow(row)
}

// getDelegationByRequestTx loads one delegation by its idempotency key.
func getDelegationByRequestTx(ctx context.Context, tx *sql.Tx, tenantID, contextID, requestID string) (Delegation, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, request_id, card_id, project_id, title, pack, state, created_at
		FROM wc_context_tasks WHERE tenant_id=? AND context_id=? AND request_id=?`, tenantID, contextID, requestID)
	return scanDelegationRow(row)
}

// listDelegations returns the delegation reservations for a context.
func (s *Store) listDelegations(ctx context.Context, tenantID, contextID string) ([]Delegation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, request_id, card_id, project_id, title, pack, state, created_at
		FROM wc_context_tasks WHERE tenant_id=? AND context_id=? ORDER BY created_at, id`, tenantID, contextID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delegation{}
	for rows.Next() {
		item, err := scanDelegationRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// scanDelegationRow scans the standard delegation column set.
func scanDelegationRow(scanner interface{ Scan(...any) error }) (Delegation, error) {
	var item Delegation
	if err := scanner.Scan(&item.ID, &item.RequestID, &item.CardID, &item.ProjectID, &item.Title, &item.Pack, &item.State, &item.CreatedAt); err != nil {
		return Delegation{}, notFound(err)
	}
	return item, nil
}

// listEvents returns the audit events for a context, newest first.
func (s *Store) listEvents(ctx context.Context, tenantID, contextID string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, context_id, kind, title, source, created_at FROM wc_events
		WHERE tenant_id=? AND context_id=? ORDER BY created_at DESC, id DESC`, tenantID, contextID)
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
