package workcontext

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// List returns the caller's work contexts ordered by most recently updated.
func (s *Store) List(ctx context.Context, scope Scope) ([]Context, error) {
	tenantID, ownerID := scopeArgs(scope)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, background, goal, status, version, checkpoint_json, created_at, updated_at
		FROM wc_contexts
		WHERE tenant_id = ? AND owner_id = ?
		ORDER BY updated_at DESC, id`, tenantID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Context{}
	for rows.Next() {
		item, err := scanContextRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SearchContexts returns the caller's non-archived streams whose indexed text
// (title, background, checkpoint) matches the query. An empty query is List. The
// projection is pre-segmented (see indexingText), so a two-character CJK query
// can match checkpoint text, not just the stream title.
func (s *Store) SearchContexts(ctx context.Context, scope Scope, query string) ([]Context, error) {
	terms := recallTerms(query)
	if len(terms) == 0 {
		return s.List(ctx, scope)
	}
	tenantID, ownerID := scopeArgs(scope)
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.title, c.background, c.goal, c.status, c.version, c.checkpoint_json, c.created_at, c.updated_at
		FROM wc_contexts_fts f
		JOIN wc_contexts c ON c.tenant_id = f.tenant_id AND c.id = f.id
		WHERE f.tenant_id = ? AND f.owner_id = ? AND c.status <> 'archived' AND wc_contexts_fts MATCH ?
		ORDER BY c.updated_at DESC, c.id`, tenantID, ownerID, ftsMatchExpr(terms))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Context{}
	for rows.Next() {
		item, err := scanContextRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Create inserts a new active work context owned by scope.
func (s *Store) Create(ctx context.Context, scope Scope, input ContextInput) (Context, error) {
	if err := requiredText("title", input.Title); err != nil {
		return Context{}, err
	}
	tenantID, ownerID := scopeArgs(scope)
	if err := checkScopeWritable(scope); err != nil {
		return Context{}, err
	}
	now := nowRFC3339()
	cp := Checkpoint{Revision: 0, CreatedAt: now}
	item := Context{
		ID:         newID(),
		Title:      strings.TrimSpace(input.Title),
		Background: input.Background,
		Goal:       input.Goal,
		Status:     "active",
		Version:    1,
		Checkpoint: decodeCheckpoint(encodeCheckpoint(cp)),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Context{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wc_contexts(tenant_id, id, owner_id, title, background, goal, status, version, checkpoint_json, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tenantID, item.ID, ownerID, item.Title, item.Background, item.Goal, item.Status, item.Version,
		encodeCheckpoint(item.Checkpoint), item.CreatedAt, item.UpdatedAt); err != nil {
		return Context{}, err
	}
	if err := indexContextTx(ctx, tx, tenantID, ownerID, item.ID, item.Title, item.Background, item.Checkpoint, item.Status); err != nil {
		return Context{}, err
	}
	if err := recordEventTx(ctx, tx, tenantID, item.ID, "context_created", "创建工作桌面", "store", "created:"+item.ID, now); err != nil {
		return Context{}, err
	}
	if err := tx.Commit(); err != nil {
		return Context{}, err
	}
	return item, nil
}

// Get returns the full detail (context plus every related collection) for id.
func (s *Store) Get(ctx context.Context, scope Scope, id string) (Detail, error) {
	tenantID, ownerID := scopeArgs(scope)
	item, err := s.getContext(ctx, tenantID, ownerID, strings.TrimSpace(id))
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{Context: item}
	if detail.Resources, err = s.listContextResources(ctx, tenantID, id); err != nil {
		return Detail{}, err
	}
	if detail.Sessions, err = s.listSessions(ctx, tenantID, id); err != nil {
		return Detail{}, err
	}
	if detail.Delegations, err = s.listDelegations(ctx, tenantID, id); err != nil {
		return Detail{}, err
	}
	if detail.Facts, err = s.listFacts(ctx, tenantID, id); err != nil {
		return Detail{}, err
	}
	if detail.Relations, err = s.listRelations(ctx, tenantID, id); err != nil {
		return Detail{}, err
	}
	if detail.Events, err = s.listEvents(ctx, tenantID, id); err != nil {
		return Detail{}, err
	}
	return detail, nil
}

// Patch applies a partial update guarded by optimistic concurrency.
func (s *Store) Patch(ctx context.Context, scope Scope, id string, patch ContextPatch) (Context, error) {
	tenantID, ownerID := scopeArgs(scope)
	id = strings.TrimSpace(id)
	if id == "" {
		return Context{}, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Context{}, err
	}
	defer tx.Rollback()
	current, err := getContextTx(ctx, tx, tenantID, ownerID, id)
	if err != nil {
		return Context{}, err
	}
	if current.Status == statusArchived {
		// Archived contexts are read-only except for an explicit restore: the
		// only accepted change is the status moving back to active/paused.
		if patch.Status == nil || patch.Title != nil || patch.Background != nil || patch.Goal != nil {
			return Context{}, ErrArchived
		}
		restore := strings.TrimSpace(*patch.Status)
		if restore != "active" && restore != "paused" {
			return Context{}, ErrArchived
		}
	}
	next := current
	if patch.Title != nil {
		if err := requiredText("title", *patch.Title); err != nil {
			return Context{}, err
		}
		next.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.Background != nil {
		next.Background = *patch.Background
	}
	if patch.Goal != nil {
		next.Goal = *patch.Goal
	}
	if patch.Status != nil {
		status := strings.TrimSpace(*patch.Status)
		switch status {
		case "active", "paused":
			next.Status = status
		case statusArchived:
			// Archiving is allowed whether or not the context was paused.
			next.Status = statusArchived
		default:
			return Context{}, fmt.Errorf("%w: unknown status %q", ErrInvalid, status)
		}
	}
	now := nowRFC3339()
	next.Version = current.Version + 1
	next.UpdatedAt = now
	var patchApplied bool
	if next.Status == statusArchived && current.Status != statusArchived {
		res, err := tx.ExecContext(ctx, `
			UPDATE wc_contexts SET title=?, background=?, goal=?, status=?, version=?, updated_at=?, archived_at=?
			WHERE tenant_id=? AND id=? AND owner_id=? AND version=?`,
			next.Title, next.Background, next.Goal, next.Status, next.Version, now, now,
			tenantID, id, ownerID, patch.IfVersion)
		if err != nil {
			return Context{}, err
		}
		if affected, err := res.RowsAffected(); err != nil {
			return Context{}, err
		} else {
			patchApplied = affected > 0
		}
	} else {
		res, err := tx.ExecContext(ctx, `
			UPDATE wc_contexts SET title=?, background=?, goal=?, status=?, version=?, updated_at=?
			WHERE tenant_id=? AND id=? AND owner_id=? AND version=?`,
			next.Title, next.Background, next.Goal, next.Status, next.Version, now,
			tenantID, id, ownerID, patch.IfVersion)
		if err != nil {
			return Context{}, err
		}
		if affected, err := res.RowsAffected(); err != nil {
			return Context{}, err
		} else {
			patchApplied = affected > 0
		}
	}
	if !patchApplied {
		return Context{}, s.versionMiss(ctx, tx, tenantID, ownerID, id)
	}
	if err := indexContextTx(ctx, tx, tenantID, ownerID, id, next.Title, next.Background, next.Checkpoint, next.Status); err != nil {
		return Context{}, err
	}
	if err := recordEventTx(ctx, tx, tenantID, id, "context_updated", "更新工作桌面", "store",
		fmt.Sprintf("patch:%d", next.Version), now); err != nil {
		return Context{}, err
	}
	if err := tx.Commit(); err != nil {
		return Context{}, err
	}
	return next, nil
}

// SaveCheckpoint records an immutable checkpoint revision and advances the
// context's current pointer under optimistic concurrency.
func (s *Store) SaveCheckpoint(ctx context.Context, scope Scope, id string, ifVersion int, checkpoint Checkpoint) (Context, error) {
	tenantID, ownerID := scopeArgs(scope)
	id = strings.TrimSpace(id)
	if id == "" {
		return Context{}, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Context{}, err
	}
	defer tx.Rollback()
	current, err := getContextTx(ctx, tx, tenantID, ownerID, id)
	if err != nil {
		return Context{}, err
	}
	if current.Status == statusArchived {
		return Context{}, ErrArchived
	}
	if ifVersion != 0 && ifVersion != current.Version {
		return Context{}, ErrConflict
	}
	var maxRevision int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(revision), 0) FROM wc_checkpoints WHERE tenant_id=? AND context_id=?`,
		tenantID, id).Scan(&maxRevision); err != nil {
		return Context{}, err
	}
	now := nowRFC3339()
	stored := checkpoint
	stored.Revision = maxRevision + 1
	stored.CreatedAt = now
	stored = decodeCheckpoint(encodeCheckpoint(stored))

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wc_checkpoints(tenant_id, id, context_id, revision, payload_json, actor_id, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		tenantID, newID(), id, stored.Revision, encodeCheckpoint(stored), ownerID, now); err != nil {
		return Context{}, err
	}
	nextVersion := current.Version + 1
	res, err := tx.ExecContext(ctx, `
		UPDATE wc_contexts SET checkpoint_json=?, version=?, updated_at=?
		WHERE tenant_id=? AND id=? AND owner_id=? AND version=?`,
		encodeCheckpoint(stored), nextVersion, now, tenantID, id, ownerID, current.Version)
	if err != nil {
		return Context{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return Context{}, err
	}
	if affected == 0 {
		return Context{}, s.versionMiss(ctx, tx, tenantID, ownerID, id)
	}
	next := current
	next.Checkpoint = stored
	next.Version = nextVersion
	next.UpdatedAt = now
	if err := indexContextTx(ctx, tx, tenantID, ownerID, id, next.Title, next.Background, stored, next.Status); err != nil {
		return Context{}, err
	}
	if err := recordEventTx(ctx, tx, tenantID, id, "checkpoint_saved",
		fmt.Sprintf("保存检查点 r%d", stored.Revision), "store",
		fmt.Sprintf("checkpoint:%d", stored.Revision), now); err != nil {
		return Context{}, err
	}
	if err := tx.Commit(); err != nil {
		return Context{}, err
	}
	return next, nil
}

// Checkpoints returns the immutable checkpoint revision history, newest first.
func (s *Store) Checkpoints(ctx context.Context, scope Scope, id string) ([]Checkpoint, error) {
	tenantID, ownerID := scopeArgs(scope)
	id = strings.TrimSpace(id)
	if _, err := s.getContext(ctx, tenantID, ownerID, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT payload_json FROM wc_checkpoints
		WHERE tenant_id=? AND context_id=?
		ORDER BY revision DESC`, tenantID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Checkpoint{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		out = append(out, decodeCheckpoint(payload))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// --- helpers ---

// getContext loads a scoped context on a fresh connection.
func (s *Store) getContext(ctx context.Context, tenantID, ownerID, id string) (Context, error) {
	if id == "" {
		return Context{}, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, title, background, goal, status, version, checkpoint_json, created_at, updated_at
		FROM wc_contexts WHERE tenant_id=? AND owner_id=? AND id=?`, tenantID, ownerID, id)
	item, err := scanContextRow(row)
	if err != nil {
		return Context{}, notFound(err)
	}
	return item, nil
}

// getContextTx loads a scoped context inside a transaction.
func getContextTx(ctx context.Context, tx *sql.Tx, tenantID, ownerID, id string) (Context, error) {
	if id == "" {
		return Context{}, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	row := tx.QueryRowContext(ctx, `
		SELECT id, title, background, goal, status, version, checkpoint_json, created_at, updated_at
		FROM wc_contexts WHERE tenant_id=? AND owner_id=? AND id=?`, tenantID, ownerID, id)
	item, err := scanContextRow(row)
	if err != nil {
		return Context{}, notFound(err)
	}
	return item, nil
}

// requireEditableContextTx loads a scoped context for a write and rejects edits
// while it is archived. Every mutating path routes through this so the archive
// guard cannot be forgotten on a subset of operations.
func requireEditableContextTx(ctx context.Context, tx *sql.Tx, tenantID, ownerID, contextID string) (Context, error) {
	item, err := getContextTx(ctx, tx, tenantID, ownerID, contextID)
	if err != nil {
		return Context{}, err
	}
	if item.Status == statusArchived {
		return Context{}, ErrArchived
	}
	return item, nil
}

// ensureVersionApplied resolves a conditional UPDATE that affected zero rows
// into ErrNotFound (row absent) or ErrConflict (a concurrent write won).
func (s *Store) versionMiss(ctx context.Context, tx *sql.Tx, tenantID, ownerID, id string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM wc_contexts WHERE tenant_id=? AND owner_id=? AND id=?`,
		tenantID, ownerID, id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	// The row exists but the version guard did not match: someone else wrote it.
	return ErrConflict
}

// indexContextTx refreshes the searchable projection for one context. It is a
// rebuildable index: failures must not roll back a saved checkpoint, so errors
// are intentionally swallowed after the write attempt.
func indexContextTx(ctx context.Context, tx *sql.Tx, tenantID, ownerID, id, title, background string, cp Checkpoint, status string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM wc_contexts_fts WHERE tenant_id=? AND id=?`, tenantID, id); err != nil {
		return nil
	}
	if status == statusArchived {
		// Archived contexts are excluded from recall.
		return nil
	}
	text := indexingText(title, background, cp.Summary,
		strings.Join(cp.Completed, " "), strings.Join(cp.InProgress, " "),
		strings.Join(cp.NextActions, " "), strings.Join(cp.Blockers, " "),
		strings.Join(cp.OpenQuestions, " "))
	if text == "" {
		return nil
	}
	_, _ = tx.ExecContext(ctx, `INSERT INTO wc_contexts_fts(tenant_id, id, owner_id, text) VALUES(?, ?, ?, ?)`,
		tenantID, id, ownerID, text)
	return nil
}
