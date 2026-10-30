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
		SELECT id, context_id, text, status, assignee, due_at, source, position, version, created_at, updated_at
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
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wc_todos(tenant_id, id, context_id, owner_id, text, status, assignee, due_at, source, position, version, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tenantID, item.ID, contextID, ownerID, item.Text, item.Status, item.Owner, item.DueAt,
		item.Source, item.Position, item.Version, item.CreatedAt, item.UpdatedAt); err != nil {
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
	if current.Version != patch.IfVersion {
		return Todo{}, ErrConflict
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
	res, err := tx.ExecContext(ctx, `
		UPDATE wc_todos SET text=?, status=?, assignee=?, due_at=?, position=?, version=?, updated_at=?
		WHERE tenant_id=? AND context_id=? AND id=? AND version=?`,
		next.Text, next.Status, next.Owner, next.DueAt, next.Position, next.Version, now,
		tenantID, contextID, todoID, patch.IfVersion)
	if err != nil {
		return Todo{}, err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return Todo{}, err
	} else if affected == 0 {
		return Todo{}, ErrConflict
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
		SELECT id, context_id, text, status, assignee, due_at, source, position, version, created_at, updated_at
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
		&item.Source, &item.Position, &item.Version, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return Todo{}, err
	}
	return item, nil
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
