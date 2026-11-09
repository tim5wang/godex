package workcontext

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// validTodoStatuses are the accepted plan-item states.
var validTodoStatuses = map[string]bool{"todo": true, "doing": true, "done": true, "blocked": true}

// TodoStatusPriority ranks plan items for display: blocked/active first, done
// last. It is exported so callers (service, UI) sort identically.
func TodoStatusPriority(status string) int {
	switch status {
	case "blocked":
		return 0
	case "doing":
		return 1
	case "todo":
		return 2
	case "done":
		return 3
	default:
		return 4
	}
}

// ListTodos returns the context's plan items, in stored order.
func (s *Store) ListTodos(ctx context.Context, scope Scope, contextID string) ([]Todo, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	if _, err := s.getContext(ctx, tenantID, ownerID, contextID); err != nil {
		return nil, err
	}
	return s.listTodos(ctx, tenantID, contextID)
}

func (s *Store) listTodos(ctx context.Context, tenantID, contextID string) ([]Todo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, context_id, text, status, assignee, due_at, source, position, version, created_at, updated_at, completed_at
		FROM wc_todos WHERE tenant_id=? AND context_id=?
		ORDER BY position, created_at, id`, tenantID, contextID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Todo{}
	for rows.Next() {
		item, err := scanTodoRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// CreateTodo adds a plan item. Text is required; status defaults to todo.
func (s *Store) CreateTodo(ctx context.Context, scope Scope, contextID string, input TodoInput) (Todo, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	if err := checkScopeWritable(scope); err != nil {
		return Todo{}, err
	}
	if err := requiredText("text", input.Text); err != nil {
		return Todo{}, err
	}
	status, err := normalizeTodoStatus(input.Status)
	if err != nil {
		return Todo{}, err
	}
	if err := validateDueAt(input.DueAt); err != nil {
		return Todo{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Todo{}, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return Todo{}, err
	}
	now := nowRFC3339()
	var position int
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM wc_todos WHERE tenant_id=? AND context_id=?`, tenantID, contextID).Scan(&position)
	item := Todo{
		ID: newID(), ContextID: contextID, Text: strings.TrimSpace(input.Text), Status: status,
		Owner: strings.TrimSpace(input.Owner), DueAt: strings.TrimSpace(input.DueAt),
		Source: strings.TrimSpace(input.Source), Position: position, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	// An item created directly as done still has a completion time.
	if status == "done" {
		item.CompletedAt = now
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wc_todos(tenant_id, id, context_id, owner_id, text, status, assignee, due_at, source, position, version, created_at, updated_at, completed_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tenantID, item.ID, contextID, ownerID, item.Text, item.Status, item.Owner, item.DueAt,
		item.Source, item.Position, item.Version, item.CreatedAt, item.UpdatedAt, item.CompletedAt); err != nil {
		return Todo{}, err
	}
	if err := recordEventTx(ctx, tx, tenantID, contextID, "todo_created", "新增计划："+item.Text, "desktop", "todo:"+item.ID+":v1", now); err != nil {
		return Todo{}, err
	}
	if err := tx.Commit(); err != nil {
		return Todo{}, err
	}
	return item, nil
}

// PatchTodo edits one plan item in place under optimistic concurrency, so the UI
// can change status/owner/due without rewriting the whole plan.
func (s *Store) PatchTodo(ctx context.Context, scope Scope, contextID, todoID string, patch TodoPatch) (Todo, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	todoID = strings.TrimSpace(todoID)
	if err := checkScopeWritable(scope); err != nil {
		return Todo{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Todo{}, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return Todo{}, err
	}
	current, err := getTodoTx(ctx, tx, tenantID, contextID, todoID)
	if err != nil {
		return Todo{}, err
	}
	// IfVersion 0 means "no explicit precondition", matching SaveCheckpoint, so a
	// caller that has not read the item can still patch it; the UPDATE below still
	// guards on the version we just read, so a concurrent write can never be lost
	// silently. A non-zero value that does not match reports the item's real
	// version, because if_version is overloaded across actions (stream version for
	// checkpoint vs item version here) and a bare "version conflict" left the
	// caller unable to tell which entity to re-read.
	if patch.IfVersion != 0 && current.Version != patch.IfVersion {
		return Todo{}, fmt.Errorf("%w: plan item %s is at version %d but if_version=%d (todo_patch guards on the PLAN ITEM's own version, not the stream version); re-read it with action=plan and use its \"version\" field, or omit the version to patch without a precondition", ErrConflict, todoID, current.Version, patch.IfVersion)
	}
	next := current
	if patch.Text != nil {
		if err := requiredText("text", *patch.Text); err != nil {
			return Todo{}, err
		}
		next.Text = strings.TrimSpace(*patch.Text)
	}
	if patch.Status != nil {
		status, err := normalizeTodoStatus(*patch.Status)
		if err != nil {
			return Todo{}, err
		}
		next.Status = status
	}
	if patch.Owner != nil {
		next.Owner = strings.TrimSpace(*patch.Owner)
	}
	if patch.DueAt != nil {
		if err := validateDueAt(*patch.DueAt); err != nil {
			return Todo{}, err
		}
		next.DueAt = strings.TrimSpace(*patch.DueAt)
	}
	if patch.Position != nil {
		next.Position = *patch.Position
	}
	now := nowRFC3339()
	next.Version = current.Version + 1
	next.UpdatedAt = now
	// Stamp the completion time exactly once, on the transition INTO done, and clear it when
	// the item leaves done. Leaving it alone on a re-patch of an already-done item is what
	// keeps "finished today" exact (see Todo.CompletedAt).
	switch {
	case next.Status == "done" && current.Status != "done":
		next.CompletedAt = now
	case next.Status != "done":
		next.CompletedAt = ""
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE wc_todos SET text=?, status=?, assignee=?, due_at=?, position=?, version=?, updated_at=?, completed_at=?
		WHERE tenant_id=? AND context_id=? AND id=? AND version=?`,
		next.Text, next.Status, next.Owner, next.DueAt, next.Position, next.Version, now, next.CompletedAt,
		tenantID, contextID, todoID, current.Version)
	if err != nil {
		return Todo{}, err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return Todo{}, err
	} else if affected == 0 {
		return Todo{}, fmt.Errorf("%w: plan item %s changed while patching; re-read it with action=plan and retry", ErrConflict, todoID)
	}
	title := "更新计划：" + next.Text
	if next.Status == "done" {
		title = "完成计划：" + next.Text
	} else if next.Status == "blocked" {
		title = "计划受阻：" + next.Text
	}
	if err := recordEventTx(ctx, tx, tenantID, contextID, "todo_updated", title, "desktop", fmt.Sprintf("todo:%s:v%d", todoID, next.Version), now); err != nil {
		return Todo{}, err
	}
	if err := tx.Commit(); err != nil {
		return Todo{}, err
	}
	return next, nil
}

// DeleteTodo removes a plan item.
func (s *Store) DeleteTodo(ctx context.Context, scope Scope, contextID, todoID string) error {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	todoID = strings.TrimSpace(todoID)
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
	item, err := getTodoTx(ctx, tx, tenantID, contextID, todoID)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM wc_todos WHERE tenant_id=? AND context_id=? AND id=?`, tenantID, contextID, todoID)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrNotFound
	}
	if err := recordEventTx(ctx, tx, tenantID, contextID, "todo_deleted", "删除计划："+item.Text, "desktop", fmt.Sprintf("todo:%s:del", todoID), nowRFC3339()); err != nil {
		return err
	}
	return tx.Commit()
}

// getTodoTx loads one plan item scoped by tenant and context.
func getTodoTx(ctx context.Context, tx *sql.Tx, tenantID, contextID, todoID string) (Todo, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, context_id, text, status, assignee, due_at, source, position, version, created_at, updated_at, completed_at
		FROM wc_todos WHERE tenant_id=? AND context_id=? AND id=?`, tenantID, contextID, todoID)
	item, err := scanTodoRow(row)
	if err != nil {
		return Todo{}, notFound(err)
	}
	return item, nil
}

func scanTodoRow(scanner interface{ Scan(...any) error }) (Todo, error) {
	var item Todo
	if err := scanner.Scan(&item.ID, &item.ContextID, &item.Text, &item.Status, &item.Owner, &item.DueAt,
		&item.Source, &item.Position, &item.Version, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt); err != nil {
		return Todo{}, err
	}
	return item, nil
}

// CountTodosCompletedSince counts the owner's plan items that became done since a
// timestamp. It is the NUMERATOR of the attention-yield ratio ("finished work" per unit
// of attention).
//
// Scoped by owner_id on wc_todos directly, not by joining contexts: a plan item carries
// its own owner, so the count needs no per-stream lookup and cannot leak across owners.
// (The attention-yield numerator deliberately does NOT use taskboard card completions: a
// card has no owner field — only the acting name in created_by — so counting those would
// mix every tenant's work into one user's yield.)
func (s *Store) CountTodosCompletedSince(ctx context.Context, scope Scope, since time.Time) (int, error) {
	tenantID, ownerID := scopeArgs(scope)
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wc_todos
		WHERE tenant_id=? AND owner_id=? AND status='done' AND completed_at >= ?`,
		tenantID, ownerID, since.UTC().Format(time.RFC3339Nano)).Scan(&count)
	return count, err
}

// normalizeTodoStatus defaults an empty status to todo and rejects unknown values.
func normalizeTodoStatus(status string) (string, error) {
	status = strings.TrimSpace(status)
	if status == "" {
		return "todo", nil
	}
	if !validTodoStatuses[status] {
		return "", fmt.Errorf("%w: unknown todo status %q", ErrInvalid, status)
	}
	return status, nil
}

// validateDueAt accepts an empty due time or an RFC3339 timestamp.
func validateDueAt(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("%w: due_at must be RFC3339", ErrInvalid)
	}
	return nil
}
