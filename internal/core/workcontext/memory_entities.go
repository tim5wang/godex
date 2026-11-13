package workcontext

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/retrieval"
)

type EntityEvidence struct {
	FactID         string `json:"fact_id"`
	Version        int    `json:"fact_version"`
	Source         string `json:"source"`
	SourceRevision string `json:"source_revision,omitempty"`
	Current        bool   `json:"current"`
}
type MemoryEntity struct {
	Origin        string           `json:"origin"`
	AliasesActive bool             `json:"aliases_active"`
	ID            string           `json:"id"`
	Kind          string           `json:"kind"`
	Name          string           `json:"name"`
	Applicability string           `json:"applicability"`
	Aliases       []string         `json:"aliases"`
	Evidence      []EntityEvidence `json:"evidence"`
	Active        bool             `json:"active"`
	Archived      bool             `json:"archived"`
	Version       int              `json:"version"`
	ProofFactID   string           `json:"proof_fact_id,omitempty"`
	ProofVersion  int              `json:"proof_version,omitempty"`
}
type EntityInput struct {
	ID          string   `json:"id,omitempty"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases"`
	FactID      string   `json:"fact_id"`
	FactVersion int      `json:"fact_version"`
	IfVersion   int      `json:"if_version"`
	Archived    bool     `json:"archived"`
	Reason      string   `json:"reason"`
}
type EntityPage struct {
	Items      []MemoryEntity `json:"items"`
	Total      int            `json:"total"`
	NextCursor int            `json:"next_cursor"`
}
type EntityRelation struct {
	ID          string `json:"id"`
	FromID      string `json:"from_id"`
	ToID        string `json:"to_id"`
	Kind        string `json:"kind"`
	FactID      string `json:"fact_id"`
	FactVersion int    `json:"fact_version"`
	Active      bool   `json:"active"`
	Archived    bool   `json:"archived"`
	Version     int    `json:"version"`
}
type EntityRelationInput struct {
	FromID      string `json:"from_id"`
	ToID        string `json:"to_id"`
	Kind        string `json:"kind"`
	FactID      string `json:"fact_id"`
	FactVersion int    `json:"fact_version"`
	IfVersion   int    `json:"if_version"`
	Archived    bool   `json:"archived"`
	Reason      string `json:"reason"`
}

var entityKinds = map[string]bool{"file": true, "symbol": true, "tool": true, "module": true, "project": true, "concept": true, "node": true}

const entityColumns = `id,kind,name,applicability,aliases_json,proof_fact_id,proof_version,archived,version`

func entityID(tenant, contextID, kind, name, scope string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{tenant, contextID, kind, name, dreamScopeKey(scope)}, "\x00")))
	return "ent-" + hex.EncodeToString(sum[:16])
}
func entityGroup(contextID, scope string) string { return contextID + "\x00" + dreamScopeKey(scope) }
func scanEntity(row interface{ Scan(...any) error }) (MemoryEntity, error) {
	var e MemoryEntity
	var aliases string
	err := row.Scan(&e.ID, &e.Kind, &e.Name, &e.Applicability, &aliases, &e.ProofFactID, &e.ProofVersion, &e.Archived, &e.Version)
	if err == sql.ErrNoRows {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	err = json.Unmarshal([]byte(aliases), &e.Aliases)
	e.Evidence = []EntityEvidence{}
	e.Origin = "literal_mention"
	if e.ProofFactID != "" {
		e.Origin = "curated"
	}
	return e, err
}
func getEntityTx(ctx context.Context, tx *sql.Tx, tenant, id, key string) (MemoryEntity, error) {
	return scanEntity(tx.QueryRowContext(ctx, `SELECT `+entityColumns+` FROM wc_memory_entities WHERE tenant_id=? AND context_id=? AND id=?`, tenant, id, key))
}

// RefreshMemoryEntities updates only a rebuildable mention projection. Reading
// it creates no user event, changes no facts and does not dirty Dream's input.
func (s *Store) RefreshMemoryEntities(ctx context.Context, scope Scope, id string) error {
	tenant, owner := scopeArgs(scope)
	if _, err := s.getContext(ctx, tenant, owner, id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT fact_id FROM wc_entity_dirty WHERE tenant_id=? AND context_id=? LIMIT 512`, tenant, id)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, key)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, key := range ids {
		if err = refreshFactEntitiesTx(ctx, tx, tenant, id, key); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func refreshFactEntitiesTx(ctx context.Context, tx *sql.Tx, tenant, id, key string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM wc_entity_mentions WHERE tenant_id=? AND context_id=? AND fact_id=?`, tenant, id, key); err != nil {
		return err
	}
	f, err := getFactTx(ctx, tx, tenant, id, key)
	if err != nil && err != ErrNotFound {
		return err
	}
	if err == nil {
		mentions := retrieval.DiscoverEntities(f.Title + "\n" + f.Content + "\n" + f.Subject)
		for _, mention := range mentions {
			entity := entityID(tenant, id, mention.Kind, mention.Name, f.Scope)
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO wc_memory_entities(id,tenant_id,context_id,kind,name,identity_key,applicability,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, entity, tenant, id, mention.Kind, mention.Name, mention.Name, f.Scope, nowRFC3339(), nowRFC3339()); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO wc_entity_mentions VALUES(?,?,?,?,?)`, tenant, id, entity, f.ID, f.Version); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM wc_entity_dirty WHERE tenant_id=? AND context_id=? AND fact_id=?`, tenant, id, key)
	return err
}
func (s *Store) entityDirectory(ctx context.Context, scope Scope, id string) ([]MemoryEntity, map[string]Fact, error) {
	return s.entityDirectoryWithFacts(ctx, scope, id, nil)
}

func (s *Store) entityDirectoryWithFacts(ctx context.Context, scope Scope, id string, facts []Fact) ([]MemoryEntity, map[string]Fact, error) {
	if err := s.RefreshMemoryEntities(ctx, scope, id); err != nil {
		return nil, nil, err
	}
	tenant, _ := scopeArgs(scope)
	if facts == nil {
		var err error
		facts, err = s.Facts(ctx, scope, id)
		if err != nil {
			return nil, nil, err
		}
	}
	byFact := map[string]Fact{}
	for _, f := range facts {
		byFact[f.ID] = f
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+entityColumns+` FROM wc_memory_entities WHERE tenant_id=? AND context_id=? ORDER BY kind,name,id`, tenant, id)
	if err != nil {
		return nil, nil, err
	}
	entities := []MemoryEntity{}
	positions := map[string]int{}
	for rows.Next() {
		e, eErr := scanEntity(rows)
		if eErr != nil {
			rows.Close()
			return nil, nil, eErr
		}
		positions[e.ID] = len(entities)
		entities = append(entities, e)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	mentions, err := s.db.QueryContext(ctx, `SELECT entity_id,fact_id,fact_version FROM wc_entity_mentions WHERE tenant_id=? AND context_id=? ORDER BY fact_id`, tenant, id)
	if err != nil {
		return nil, nil, err
	}
	defer mentions.Close()
	for mentions.Next() {
		var entityID, factID string
		var version int
		if err = mentions.Scan(&entityID, &factID, &version); err != nil {
			return nil, nil, err
		}
		i, ok := positions[entityID]
		if !ok {
			continue
		}
		f, ok := byFact[factID]
		if !ok {
			continue
		}
		proof := entityEvidence(f, version)
		entities[i].Evidence = append(entities[i].Evidence, proof)
		entities[i].Active = entities[i].Active || proof.Current
	}
	if err = mentions.Err(); err != nil {
		return nil, nil, err
	}
	for i := range entities {
		e := &entities[i]
		if f, ok := byFact[e.ProofFactID]; ok {
			proof := entityEvidence(f, e.ProofVersion)
			e.Evidence = append(e.Evidence, proof)
			e.Active = e.Active || proof.Current
			e.AliasesActive = proof.Current && !e.Archived
		}
		e.Active = e.Active && !e.Archived
	}
	return entities, byFact, nil
}
func entityEvidence(f Fact, version int) EntityEvidence {
	return EntityEvidence{FactID: f.ID, Version: version, Source: f.Source, SourceRevision: f.SourceRevision, Current: f.Version == version && f.Current(time.Now())}
}
func (s *Store) ListMemoryEntities(ctx context.Context, scope Scope, id, query string, cursor, limit int) (EntityPage, error) {
	page := EntityPage{Items: []MemoryEntity{}, NextCursor: -1}
	if cursor < 0 {
		return page, ErrInvalid
	}
	if limit <= 0 {
		limit = 30
	}
	limit = min(limit, 100)
	entities, _, err := s.entityDirectory(ctx, scope, id)
	if err != nil {
		return page, err
	}
	filtered := []MemoryEntity{}
	q := retrieval.EntityNameKey(query)
	for _, e := range entities {
		if q == "" || strings.Contains(retrieval.EntityNameKey(e.Name+" "+strings.Join(e.Aliases, " ")), q) {
			filtered = append(filtered, e)
		}
	}
	page.Total = len(filtered)
	if cursor > page.Total {
		return page, ErrInvalid
	}
	end := min(cursor+limit, page.Total)
	page.Items = filtered[cursor:end]
	if end < page.Total {
		page.NextCursor = end
	}
	return page, nil
}
func normalizeEntityInput(input EntityInput) (EntityInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Kind == "file" {
		input.Name = path.Clean(input.Name)
	}
	if !entityKinds[input.Kind] || input.Name == "" || len([]rune(input.Name)) > 180 || strings.ContainsAny(input.Name, "\x00\n\r") || len(input.Aliases) > 24 || strings.TrimSpace(input.Reason) == "" || len([]rune(input.Reason)) > 600 {
		return input, ErrInvalid
	}
	seen := map[string]bool{}
	aliases := []string{}
	for _, alias := range input.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || len([]rune(alias)) > 180 || strings.ContainsAny(alias, "\x00\n\r") {
			return input, ErrInvalid
		}
		key := retrieval.EntityNameKey(alias)
		if !seen[key] && key != retrieval.EntityNameKey(input.Name) {
			seen[key] = true
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	input.Aliases = aliases
	return input, nil
}
func (s *Store) SaveMemoryEntity(ctx context.Context, scope Scope, id string, input EntityInput) (MemoryEntity, error) {
	if err := checkScopeWritable(scope); err != nil {
		return MemoryEntity{}, err
	}
	input, err := normalizeEntityInput(input)
	if err != nil {
		return MemoryEntity{}, err
	}
	tenant, owner := scopeArgs(scope)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryEntity{}, err
	}
	defer tx.Rollback()
	if _, err = requireEditableContextTx(ctx, tx, tenant, owner, id); err != nil {
		return MemoryEntity{}, err
	}
	proof, err := getFactTx(ctx, tx, tenant, id, input.FactID)
	if err != nil {
		return MemoryEntity{}, err
	}
	if (!proof.Current(time.Now()) && !input.Archived) || proof.Version != input.FactVersion {
		return MemoryEntity{}, ErrConflict
	}
	key := input.ID
	if key == "" {
		key = entityID(tenant, id, input.Kind, input.Name, proof.Scope)
	}
	before, e := getEntityTx(ctx, tx, tenant, id, key)
	if e != nil && e != ErrNotFound {
		return MemoryEntity{}, e
	}
	if e == nil && (before.Version != input.IfVersion || before.Kind != input.Kind || before.Name != input.Name || dreamScopeKey(before.Applicability) != dreamScopeKey(proof.Scope)) {
		return MemoryEntity{}, ErrConflict
	}
	if e == ErrNotFound && (input.ID != "" || input.IfVersion != 0) {
		return MemoryEntity{}, ErrConflict
	}
	raw, _ := json.Marshal(input.Aliases)
	version := before.Version + 1
	_, err = tx.ExecContext(ctx, `INSERT INTO wc_memory_entities(id,tenant_id,context_id,kind,name,identity_key,applicability,aliases_json,proof_fact_id,proof_version,archived,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET aliases_json=excluded.aliases_json,proof_fact_id=excluded.proof_fact_id,proof_version=excluded.proof_version,archived=excluded.archived,version=excluded.version,updated_at=excluded.updated_at`, key, tenant, id, input.Kind, input.Name, input.Name, proof.Scope, string(raw), proof.ID, proof.Version, input.Archived, version, nowRFC3339(), nowRFC3339())
	if err != nil {
		return MemoryEntity{}, err
	}
	after, err := getEntityTx(ctx, tx, tenant, id, key)
	if err != nil {
		return after, err
	}
	if err = writeEntityAudit(ctx, tx, tenant, id, "entity_save", key, input.Reason, before, after); err != nil {
		return after, err
	}
	after.Active = !after.Archived
	after.AliasesActive = after.Active
	after.Evidence = []EntityEvidence{entityEvidence(proof, proof.Version)}
	return after, tx.Commit()
}
func writeEntityAudit(ctx context.Context, tx *sql.Tx, tenant, id, action, key, reason string, before, after any) error {
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	_, err := tx.ExecContext(ctx, `INSERT INTO wc_entity_audits VALUES(?,?,?,?,?,?,?,?,?)`, newID(), tenant, id, action, key, reason, string(b), string(a), nowRFC3339())
	return err
}

func (s *Store) MemoryEntityCatalog(ctx context.Context, scope Scope, id string) (retrieval.Catalog, error) {
	return s.memoryEntityCatalogWithFacts(ctx, scope, id, nil)
}

func (s *Store) memoryEntityCatalogWithFacts(ctx context.Context, scope Scope, id string, known []Fact) (retrieval.Catalog, error) {
	catalog := retrieval.Catalog{Groups: map[string]string{}}
	entities, facts, err := s.entityDirectoryWithFacts(ctx, scope, id, known)
	if err != nil {
		return catalog, err
	}
	for _, f := range facts {
		catalog.Groups[f.ID] = entityGroup(id, f.Scope)
	}
	for _, e := range entities {
		if !e.Active {
			continue
		}
		aliases := []string{}
		if f, ok := facts[e.ProofFactID]; ok && f.Version == e.ProofVersion && f.Current(time.Now()) {
			aliases = e.Aliases
		}
		catalog.Entities = append(catalog.Entities, retrieval.CatalogEntity{ID: e.ID, Kind: e.Kind, Name: e.Name, Aliases: aliases, Group: entityGroup(id, e.Applicability)})
	}
	relations, err := s.listEntityRelations(ctx, scope, id, facts, entities)
	if err != nil {
		return catalog, err
	}
	for _, r := range relations {
		if r.Active {
			catalog.Relations = append(catalog.Relations, retrieval.CatalogRelation{ID: r.ID, FromID: r.FromID, ToID: r.ToID, Kind: r.Kind, FactID: r.FactID, FactVersion: r.FactVersion})
		}
	}
	return catalog, nil
}
func (s *Store) ListEntityRelations(ctx context.Context, scope Scope, id string) ([]EntityRelation, error) {
	entities, facts, err := s.entityDirectory(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	return s.listEntityRelations(ctx, scope, id, facts, entities)
}
func (s *Store) listEntityRelations(ctx context.Context, scope Scope, id string, facts map[string]Fact, entities []MemoryEntity) ([]EntityRelation, error) {
	tenant, _ := scopeArgs(scope)
	active := map[string]bool{}
	for _, e := range entities {
		active[e.ID] = e.Active
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,from_id,to_id,kind,fact_id,fact_version,archived,version FROM wc_entity_relations WHERE tenant_id=? AND context_id=? ORDER BY id`, tenant, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EntityRelation{}
	for rows.Next() {
		var r EntityRelation
		if err = rows.Scan(&r.ID, &r.FromID, &r.ToID, &r.Kind, &r.FactID, &r.FactVersion, &r.Archived, &r.Version); err != nil {
			return nil, err
		}
		f, ok := facts[r.FactID]
		r.Active = ok && f.Version == r.FactVersion && f.Current(time.Now()) && !r.Archived && active[r.FromID] && active[r.ToID]
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) SaveEntityRelation(ctx context.Context, scope Scope, id string, input EntityRelationInput) (EntityRelation, error) {
	var out EntityRelation
	if err := checkScopeWritable(scope); err != nil {
		return out, err
	}
	if input.FromID == input.ToID || input.Kind != "part_of" || strings.TrimSpace(input.Reason) == "" || len([]rune(input.Reason)) > 600 {
		return out, ErrInvalid
	}
	tenant, owner := scopeArgs(scope)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = requireEditableContextTx(ctx, tx, tenant, owner, id); err != nil {
		return out, err
	}
	from, err := getEntityTx(ctx, tx, tenant, id, input.FromID)
	if err != nil {
		return out, err
	}
	to, err := getEntityTx(ctx, tx, tenant, id, input.ToID)
	if err != nil {
		return out, err
	}
	proof, err := getFactTx(ctx, tx, tenant, id, input.FactID)
	if err != nil {
		return out, err
	}
	if (!input.Archived && (from.Archived || to.Archived || !proof.Current(time.Now()))) || dreamScopeKey(from.Applicability) != dreamScopeKey(to.Applicability) || dreamScopeKey(proof.Scope) != dreamScopeKey(from.Applicability) || proof.Version != input.FactVersion {
		return out, ErrConflict
	}
	sum := sha256.Sum256([]byte(tenant + "\x00" + id + "\x00" + input.FromID + "\x00" + input.ToID + "\x00part_of"))
	key := "rel-" + hex.EncodeToString(sum[:16])
	var before EntityRelation
	e := tx.QueryRowContext(ctx, `SELECT id,from_id,to_id,kind,fact_id,fact_version,archived,version FROM wc_entity_relations WHERE tenant_id=? AND context_id=? AND id=?`, tenant, id, key).Scan(&before.ID, &before.FromID, &before.ToID, &before.Kind, &before.FactID, &before.FactVersion, &before.Archived, &before.Version)
	if e != nil && e != sql.ErrNoRows {
		return out, e
	}
	if before.Version != input.IfVersion {
		return out, ErrConflict
	}
	if !input.Archived {
		var cycle int
		err = tx.QueryRowContext(ctx, `WITH RECURSIVE reach(id) AS (SELECT ? UNION SELECT r.to_id FROM wc_entity_relations r JOIN reach ON r.from_id=reach.id WHERE r.tenant_id=? AND r.context_id=? AND r.archived=0) SELECT COUNT(*) FROM reach WHERE id=?`, input.ToID, tenant, id, input.FromID).Scan(&cycle)
		if err != nil {
			return out, err
		}
		if cycle > 0 {
			return out, fmt.Errorf("%w: entity hierarchy would contain a cycle", ErrInvalid)
		}
	}
	out = EntityRelation{ID: key, FromID: input.FromID, ToID: input.ToID, Kind: input.Kind, FactID: proof.ID, FactVersion: proof.Version, Version: before.Version + 1, Archived: input.Archived, Active: !input.Archived}
	_, err = tx.ExecContext(ctx, `INSERT INTO wc_entity_relations VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET fact_id=excluded.fact_id,fact_version=excluded.fact_version,archived=excluded.archived,version=excluded.version`, out.ID, tenant, id, out.FromID, out.ToID, out.Kind, out.FactID, out.FactVersion, out.Archived, out.Version)
	if err != nil {
		return out, err
	}
	if err = writeEntityAudit(ctx, tx, tenant, id, "relation_save", key, input.Reason, before, out); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *Store) EntityAuditHistory(ctx context.Context, scope Scope, id string) ([]map[string]any, error) {
	tenant, owner := scopeArgs(scope)
	if _, err := s.getContext(ctx, tenant, owner, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT action,item_id,reason,before_json,after_json,created_at FROM wc_entity_audits WHERE tenant_id=? AND context_id=? ORDER BY rowid DESC LIMIT 50`, tenant, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var action, item, reason, before, after, created string
		if err = rows.Scan(&action, &item, &reason, &before, &after, &created); err != nil {
			return nil, err
		}
		var b, a any
		_ = json.Unmarshal([]byte(before), &b)
		_ = json.Unmarshal([]byte(after), &a)
		out = append(out, map[string]any{"action": action, "item_id": item, "reason": reason, "before": b, "after": a, "created_at": created})
	}
	return out, rows.Err()
}
