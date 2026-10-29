package workcontext

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// relationWalkDepth caps multi-hop dependency traversal.
const relationWalkDepth = 64

// AddResource links (creating if needed) a shared resource to a context.
// When input.ID matches an existing resource in the same scope the row is
// reused so one source of truth serves many contexts.
func (s *Store) AddResource(ctx context.Context, scope Scope, contextID string, input ResourceInput) (Resource, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	if err := checkScopeWritable(scope); err != nil {
		return Resource{}, err
	}
	if err := requiredText("title", input.Title); err != nil {
		return Resource{}, err
	}
	if err := validateLocator(input.Locator); err != nil {
		return Resource{}, err
	}
	if input.Kind == "directory" && strings.TrimSpace(input.NodeID) == "" {
		return Resource{}, fmt.Errorf("%w: directory resources must bind a node_id", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Resource{}, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return Resource{}, err
	}
	now := nowRFC3339()
	resourceID := strings.TrimSpace(input.ID)
	if resourceID == "" {
		resourceID = newID()
	}
	var existingID string
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM wc_resources WHERE tenant_id=? AND id=? AND owner_id=?`,
		tenantID, resourceID, ownerID).Scan(&existingID)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx, `
			UPDATE wc_resources SET kind=?, title=?, locator=?, node_id=?, purpose=?, updated_at=?
			WHERE tenant_id=? AND id=? AND owner_id=?`,
			input.Kind, strings.TrimSpace(input.Title), input.Locator, input.NodeID, input.Purpose, now,
			tenantID, resourceID, ownerID); err != nil {
			return Resource{}, err
		}
	case err == sql.ErrNoRows:
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO wc_resources(tenant_id, id, owner_id, kind, title, locator, node_id, purpose, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			tenantID, resourceID, ownerID, input.Kind, strings.TrimSpace(input.Title), input.Locator,
			input.NodeID, input.Purpose, now, now); err != nil {
			return Resource{}, err
		}
	default:
		return Resource{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wc_context_resources(tenant_id, context_id, resource_id, purpose, created_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id, context_id, resource_id) DO UPDATE SET purpose=excluded.purpose`,
		tenantID, contextID, resourceID, input.Purpose, now); err != nil {
		return Resource{}, err
	}
	resource, err := getResourceTx(ctx, tx, tenantID, ownerID, resourceID)
	if err != nil {
		return Resource{}, err
	}
	if err := recordEventTx(ctx, tx, tenantID, contextID, "resource_linked",
		"关联资源："+resource.Title, "store", "resource:"+resourceID+":"+now, now); err != nil {
		return Resource{}, err
	}
	if err := tx.Commit(); err != nil {
		return Resource{}, err
	}
	return resource, nil
}

// RemoveResource unlinks a resource from a context. The resource row is deleted
// only when no other context still references it.
func (s *Store) RemoveResource(ctx context.Context, scope Scope, contextID, resourceID string) error {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	resourceID = strings.TrimSpace(resourceID)
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
	res, err := tx.ExecContext(ctx, `
		DELETE FROM wc_context_resources WHERE tenant_id=? AND context_id=? AND resource_id=?`,
		tenantID, contextID, resourceID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM wc_context_resources WHERE tenant_id=? AND resource_id=?`,
		tenantID, resourceID).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM wc_resources WHERE tenant_id=? AND id=? AND owner_id=?`,
			tenantID, resourceID, ownerID); err != nil {
			return err
		}
	}
	now := nowRFC3339()
	if err := recordEventTx(ctx, tx, tenantID, contextID, "resource_unlinked",
		"断开资源引用", "store", "unresource:"+resourceID+":"+now, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ListResources returns every resource owned by the caller with its context
// references resolved.
func (s *Store) ListResources(ctx context.Context, scope Scope) ([]Resource, error) {
	tenantID, ownerID := scopeArgs(scope)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, kind, locator, node_id, purpose, created_at
		FROM wc_resources WHERE tenant_id=? AND owner_id=?
		ORDER BY updated_at DESC, id`, tenantID, ownerID)
	if err != nil {
		return nil, err
	}
	out := []Resource{}
	for rows.Next() {
		var item Resource
		if err := rows.Scan(&item.ID, &item.Title, &item.Kind, &item.Locator, &item.NodeID, &item.Purpose, &item.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		item.ContextIDs = []string{}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range out {
		links, err := s.resourceContextIDs(ctx, tenantID, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].ContextIDs = links
	}
	return out, nil
}

// LinkSession associates an existing session with a context. Linking the same
// session twice returns the existing link (idempotent).
func (s *Store) LinkSession(ctx context.Context, scope Scope, contextID string, link SessionLink) (SessionLink, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	if err := requiredText("session_id", link.SessionID); err != nil {
		return SessionLink{}, err
	}
	if err := checkScopeWritable(scope); err != nil {
		return SessionLink{}, err
	}
	sessionID := strings.TrimSpace(link.SessionID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionLink{}, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return SessionLink{}, err
	}
	// A session identity must not be shared across owners: binding it to a
	// context of a different owner would leak one principal's conversation into
	// another's desktop. The same session may still be linked to several
	// contexts that share the same owner.
	var crossOwner int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM wc_context_sessions s
		JOIN wc_contexts c ON c.tenant_id = s.tenant_id AND c.id = s.context_id
		WHERE s.tenant_id=? AND s.session_id=? AND c.owner_id<>?`,
		tenantID, sessionID, ownerID).Scan(&crossOwner); err != nil {
		return SessionLink{}, err
	}
	if crossOwner > 0 {
		return SessionLink{}, fmt.Errorf("%w: session %s is already bound to another owner", ErrConflict, sessionID)
	}
	now := nowRFC3339()
	var existingID, existingTopic, existingCreated string
	err = tx.QueryRowContext(ctx, `
		SELECT id, topic, created_at FROM wc_context_sessions
		WHERE tenant_id=? AND context_id=? AND session_id=?`,
		tenantID, contextID, sessionID).Scan(&existingID, &existingTopic, &existingCreated)
	if err == nil {
		if topic := strings.TrimSpace(link.Topic); topic != "" && topic != existingTopic {
			if _, err := tx.ExecContext(ctx, `UPDATE wc_context_sessions SET topic=? WHERE tenant_id=? AND id=?`,
				topic, tenantID, existingID); err != nil {
				return SessionLink{}, err
			}
			existingTopic = topic
		}
		if err := tx.Commit(); err != nil {
			return SessionLink{}, err
		}
		return SessionLink{ID: existingID, SessionID: sessionID, Topic: existingTopic, CreatedAt: existingCreated}, nil
	}
	if err != sql.ErrNoRows {
		return SessionLink{}, err
	}
	item := SessionLink{ID: newID(), SessionID: sessionID, Topic: link.Topic, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wc_context_sessions(tenant_id, id, context_id, session_id, topic, created_at)
		VALUES(?, ?, ?, ?, ?, ?)`,
		tenantID, item.ID, contextID, item.SessionID, item.Topic, item.CreatedAt); err != nil {
		return SessionLink{}, err
	}
	if err := recordEventTx(ctx, tx, tenantID, contextID, "session_linked",
		"关联会话", "store", "session:"+item.ID+":"+now, now); err != nil {
		return SessionLink{}, err
	}
	if err := tx.Commit(); err != nil {
		return SessionLink{}, err
	}
	return item, nil
}

// UnlinkSession removes a session association from a context.
func (s *Store) UnlinkSession(ctx context.Context, scope Scope, contextID, sessionID string) error {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	sessionID = strings.TrimSpace(sessionID)
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
	res, err := tx.ExecContext(ctx, `
		DELETE FROM wc_context_sessions WHERE tenant_id=? AND context_id=? AND session_id=?`,
		tenantID, contextID, sessionID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	now := nowRFC3339()
	if err := recordEventTx(ctx, tx, tenantID, contextID, "session_unlinked",
		"断开会话引用", "store", "unsession:"+sessionID+":"+now, now); err != nil {
		return err
	}
	return tx.Commit()
}

// AddRelation creates a directed relation from contextID to input.ToID. It
// rejects self references, unknown targets, unknown kinds, and cycles in
// depends_on chains.
func (s *Store) AddRelation(ctx context.Context, scope Scope, contextID string, input RelationInput) (Relation, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	toID := strings.TrimSpace(input.ToID)
	if toID == "" {
		return Relation{}, fmt.Errorf("%w: to_id is required", ErrInvalid)
	}
	if toID == contextID {
		return Relation{}, fmt.Errorf("%w: a context cannot relate to itself", ErrInvalid)
	}
	kind := strings.TrimSpace(input.Kind)
	if kind == "" {
		kind = "related_to"
	}
	if !validRelationKinds[kind] {
		return Relation{}, fmt.Errorf("%w: unknown relation kind %q", ErrInvalid, kind)
	}
	if kind == "depends_on" && strings.TrimSpace(input.Condition) == "" {
		return Relation{}, fmt.Errorf("%w: depends_on requires a satisfaction condition", ErrInvalid)
	}
	if err := checkScopeWritable(scope); err != nil {
		return Relation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Relation{}, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return Relation{}, err
	}
	if _, err := getContextTx(ctx, tx, tenantID, ownerID, toID); err != nil {
		return Relation{}, err
	}
	if kind == "depends_on" {
		cyclic, err := wouldCycleTx(ctx, tx, tenantID, contextID, toID)
		if err != nil {
			return Relation{}, err
		}
		if cyclic {
			return Relation{}, fmt.Errorf("%w: dependency cycle", ErrInvalid)
		}
	}
	now := nowRFC3339()
	item := Relation{
		ID:        newID(),
		FromID:    contextID,
		ToID:      toID,
		Kind:      kind,
		Condition: input.Condition,
		CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wc_relations(tenant_id, id, context_id, from_id, to_id, kind, condition, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		tenantID, item.ID, contextID, item.FromID, item.ToID, item.Kind, item.Condition, item.CreatedAt); err != nil {
		return Relation{}, err
	}
	if err := recordEventTx(ctx, tx, tenantID, contextID, "relation_added",
		fmt.Sprintf("新增关系 %s", kind), "store", "relation:"+item.ID+":"+now, now); err != nil {
		return Relation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Relation{}, err
	}
	return item, nil
}

// RemoveRelation deletes a relation belonging to the given context.
func (s *Store) RemoveRelation(ctx context.Context, scope Scope, contextID, relationID string) error {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	relationID = strings.TrimSpace(relationID)
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
	res, err := tx.ExecContext(ctx, `
		DELETE FROM wc_relations WHERE tenant_id=? AND id=? AND context_id=?`,
		tenantID, relationID, contextID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	now := nowRFC3339()
	if err := recordEventTx(ctx, tx, tenantID, contextID, "relation_removed",
		"删除关系", "store", "unrelation:"+relationID+":"+now, now); err != nil {
		return err
	}
	return tx.Commit()
}

// --- helpers ---

// getResourceTx loads one scoped resource with its context references.
func getResourceTx(ctx context.Context, tx *sql.Tx, tenantID, ownerID, resourceID string) (Resource, error) {
	var item Resource
	var createdAt string
	if err := tx.QueryRowContext(ctx, `
		SELECT id, title, kind, locator, node_id, purpose, created_at
		FROM wc_resources WHERE tenant_id=? AND id=? AND owner_id=?`,
		tenantID, resourceID, ownerID).Scan(
		&item.ID, &item.Title, &item.Kind, &item.Locator, &item.NodeID, &item.Purpose, &createdAt); err != nil {
		return Resource{}, notFound(err)
	}
	item.CreatedAt = createdAt
	item.ContextIDs = []string{}
	rows, err := tx.QueryContext(ctx, `
		SELECT context_id FROM wc_context_resources WHERE tenant_id=? AND resource_id=? ORDER BY context_id`,
		tenantID, resourceID)
	if err != nil {
		return Resource{}, err
	}
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			rows.Close()
			return Resource{}, err
		}
		item.ContextIDs = append(item.ContextIDs, cid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Resource{}, err
	}
	rows.Close()
	return item, nil
}

// listContextResources returns the resources linked to one context.
func (s *Store) listContextResources(ctx context.Context, tenantID, contextID string) ([]Resource, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.title, r.kind, r.locator, r.node_id, r.purpose, r.created_at
		FROM wc_resources r
		JOIN wc_context_resources cr ON cr.tenant_id = r.tenant_id AND cr.resource_id = r.id
		WHERE cr.tenant_id=? AND cr.context_id=?
		ORDER BY r.title, r.id`, tenantID, contextID)
	if err != nil {
		return nil, err
	}
	out := []Resource{}
	for rows.Next() {
		var item Resource
		if err := rows.Scan(&item.ID, &item.Title, &item.Kind, &item.Locator, &item.NodeID, &item.Purpose, &item.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		item.ContextIDs = []string{}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range out {
		ids, err := s.resourceContextIDs(ctx, tenantID, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].ContextIDs = ids
	}
	return out, nil
}

// resourceContextIDs resolves all contexts referencing a resource.
func (s *Store) resourceContextIDs(ctx context.Context, tenantID, resourceID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT context_id FROM wc_context_resources WHERE tenant_id=? AND resource_id=? ORDER BY context_id`,
		tenantID, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		out = append(out, cid)
	}
	return out, rows.Err()
}

// listSessions returns the session links for a context.
func (s *Store) listSessions(ctx context.Context, tenantID, contextID string) ([]SessionLink, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, topic, created_at FROM wc_context_sessions
		WHERE tenant_id=? AND context_id=? ORDER BY created_at, id`, tenantID, contextID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionLink{}
	for rows.Next() {
		var item SessionLink
		if err := rows.Scan(&item.ID, &item.SessionID, &item.Topic, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// listRelations returns the relations whose owner context is contextID.
func (s *Store) listRelations(ctx context.Context, tenantID, contextID string) ([]Relation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, from_id, to_id, kind, condition, created_at FROM wc_relations
		WHERE tenant_id=? AND context_id=? ORDER BY created_at, id`, tenantID, contextID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Relation{}
	for rows.Next() {
		var item Relation
		if err := rows.Scan(&item.ID, &item.FromID, &item.ToID, &item.Kind, &item.Condition, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// wouldCycleTx reports whether adding from->to would close a depends_on cycle.
// It walks existing depends_on edges starting at to and looks for from.
func wouldCycleTx(ctx context.Context, tx *sql.Tx, tenantID, fromID, toID string) (bool, error) {
	if fromID == toID {
		return true, nil
	}
	visited := map[string]bool{toID: true}
	frontier := []string{toID}
	for depth := 0; depth < relationWalkDepth && len(frontier) > 0; depth++ {
		next := make([]string, 0, len(frontier))
		for _, node := range frontier {
			if node == fromID {
				return true, nil
			}
			rows, err := tx.QueryContext(ctx, `
				SELECT to_id FROM wc_relations WHERE tenant_id=? AND from_id=? AND kind='depends_on'`,
				tenantID, node)
			if err != nil {
				return false, err
			}
			for rows.Next() {
				var target string
				if err := rows.Scan(&target); err != nil {
					rows.Close()
					return false, err
				}
				if target == fromID {
					rows.Close()
					return true, nil
				}
				if !visited[target] {
					visited[target] = true
					next = append(next, target)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return false, err
			}
			rows.Close()
		}
		frontier = next
	}
	return false, nil
}
