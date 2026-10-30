package workcontext

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// schemaVersion is the current ordered migration version.
const schemaVersion = 2

// statusArkid is the terminal read-only status.
const statusArchived = "archived"

// validStatuses are the accepted work context statuses.
var validStatuses = map[string]bool{"active": true, "paused": true, "archived": true}

// validFactStatuses are the accepted fact review statuses.
var validFactStatuses = map[string]bool{
	"candidate": true, "confirmed": true, "stale": true,
	"superseded": true, "rejected": true,
}

// validProvenance values distinguish a human report from a system query. A
// missing value defaults to human_report, which is never presented as a system
// verified result.
const (
	provenanceHumanReport  = "human_report"
	provenanceSystemQuery  = "system_query"
	provenanceDerived      = "derived"
	provenanceUnclassified = "unclassified"
)

var validProvenance = map[string]bool{
	provenanceHumanReport:  true,
	provenanceSystemQuery:  true,
	provenanceDerived:      true,
	provenanceUnclassified: true,
}

// validRelationKinds are the accepted relation kinds.
var validRelationKinds = map[string]bool{"related_to": true, "depends_on": true}

// ValidRelationKinds lists the exact relation kinds accepted by the store, for
// callers (e.g. the UI) that must render them.
var ValidRelationKinds = []string{"related_to", "depends_on"}

// BuiltInScopes are the scope keys every fact can be classified by. Callers may
// add their own keys; these are the recognized baseline.
var BuiltInScopes = []string{"service", "business", "env", "region"}

// unknownScopeValues are placeholders that must never be silently upgraded to a
// concrete scope such as production.
var unknownScopeValues = map[string]bool{"unknown": true, "unk": true, "n/a": true, "na": true, "none": true, "?": true}

// Store is a SQLite-backed work context store. All access is isolated by the
// (tenant_id, owner_id) scope generated from the caller identity.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path with WAL,
// foreign_keys and a busy timeout, then applies ordered migrations.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: empty database path", ErrInvalid)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A single writer connection keeps short transactions simple and avoids
	// SQLITE_BUSY between the pool's connections. PRAGMAs are therefore
	// guaranteed to apply to the only connection in use.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("work context: pragma %q: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.rebuildSearchIndexes(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// migrate applies ordered schema migrations recorded in wc_schema_migrations.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS wc_schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM wc_schema_migrations`).Scan(&current); err != nil {
		return err
	}
	for version := current + 1; version <= schemaVersion; version++ {
		statements, ok := migrations[version]
		if !ok {
			return fmt.Errorf("work context: missing migration %d", version)
		}
		if err := s.applyMigration(version, statements); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(version int, statements []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("work context: migration %d: %w", version, err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO wc_schema_migrations(version, applied_at) VALUES(?, ?)`,
		version, nowRFC3339()); err != nil {
		return err
	}
	return tx.Commit()
}

// rebuildSearchIndexes regenerates the FTS projections from the fact rows. The
// projections are rebuildable derived data, so this healing pass runs on every
// open: it repairs facts written before the facts index existed, and re-indexes
// rows whose projection write failed. It is best-effort after the first pass and
// does not fail Open once the tables exist.
func (s *Store) rebuildSearchIndexes() error {
	rows, err := s.db.Query(`
		SELECT tenant_id, id, context_id, title, content, kind, status, scope, subject, predicate,
		       source, source_excerpt, source_revision, observed_at, verified_at,
		       provenance, supersedes_id, memory_candidate_id, version, created_at
		FROM wc_facts`)
	if err != nil {
		return err
	}
	items := []struct {
		tenantID string
		fact     Fact
	}{}
	for rows.Next() {
		var tenantID string
		var item Fact
		err := rows.Scan(&tenantID, &item.ID, &item.ContextID, &item.Title, &item.Content, &item.Kind, &item.Status,
			&item.Scope, &item.Subject, &item.Predicate, &item.Source, &item.SourceExcerpt, &item.SourceRevision,
			&item.ObservedAt, &item.VerifiedAt, &item.Provenance, &item.SupersedesID, &item.MemoryCandidateID,
			&item.Version, &item.CreatedAt)
		if err != nil {
			rows.Close()
			return err
		}
		items = append(items, struct {
			tenantID string
			fact     Fact
		}{tenantID, item})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, entry := range items {
		item := entry.fact
		if item.Status == "superseded" || item.Status == "rejected" {
			_, _ = s.db.Exec(`DELETE FROM wc_facts_fts WHERE tenant_id=? AND fact_id=?`, entry.tenantID, item.ID)
			continue
		}
		text := indexingText(item.Title, item.Subject, item.Predicate, item.Content, item.Scope)
		if _, err := s.db.Exec(`DELETE FROM wc_facts_fts WHERE tenant_id=? AND fact_id=?`, entry.tenantID, item.ID); err != nil {
			continue
		}
		if text == "" {
			continue
		}
		_, _ = s.db.Exec(`INSERT INTO wc_facts_fts(tenant_id, context_id, fact_id, text) VALUES(?, ?, ?, ?)`,
			entry.tenantID, item.ContextID, item.ID, text)
	}
	// Contexts projection is also derived data; rebuild it so a stream saved before
	// this index existed is searchable. Archived streams stay out.
	return s.rebuildContextIndexes()
}

// rebuildContextIndexes regenerates the wc_contexts_fts projection from the
// context rows (checkpoint text included). Best-effort per row, like the facts pass.
func (s *Store) rebuildContextIndexes() error {
	rows, err := s.db.Query(`
		SELECT tenant_id, id, owner_id, title, background, checkpoint_json, status FROM wc_contexts`)
	if err != nil {
		return err
	}
	type ctxEntry struct {
		tenantID, id, ownerID, title, background, checkpointJSON, status string
	}
	items := []ctxEntry{}
	for rows.Next() {
		var e ctxEntry
		if err := rows.Scan(&e.tenantID, &e.id, &e.ownerID, &e.title, &e.background, &e.checkpointJSON, &e.status); err != nil {
			rows.Close()
			return err
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, e := range items {
		if _, err := s.db.Exec(`DELETE FROM wc_contexts_fts WHERE tenant_id=? AND id=?`, e.tenantID, e.id); err != nil {
			continue
		}
		if e.status == statusArchived {
			continue
		}
		cp := decodeCheckpoint(e.checkpointJSON)
		text := indexingText(e.title, e.background, cp.Summary,
			strings.Join(cp.Completed, " "), strings.Join(cp.InProgress, " "),
			strings.Join(cp.NextActions, " "), strings.Join(cp.Blockers, " "),
			strings.Join(cp.OpenQuestions, " "))
		if text == "" {
			continue
		}
		_, _ = s.db.Exec(`INSERT INTO wc_contexts_fts(tenant_id, id, owner_id, text) VALUES(?, ?, ?, ?)`,
			e.tenantID, e.id, e.ownerID, text)
	}
	return nil
}

// migrations holds the ordered DDL. Each version is applied in one transaction.
var migrations = map[int][]string{
	1: {
		`CREATE TABLE IF NOT EXISTS wc_contexts (
			tenant_id TEXT NOT NULL,
			id TEXT NOT NULL,
			owner_id TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			background TEXT NOT NULL DEFAULT '',
			goal TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			version INTEGER NOT NULL DEFAULT 1,
			checkpoint_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			archived_at TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (tenant_id, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wc_contexts_owner ON wc_contexts(tenant_id, owner_id, status, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS wc_checkpoints (
			tenant_id TEXT NOT NULL,
			id TEXT NOT NULL,
			context_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			payload_json TEXT NOT NULL,
			actor_id TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			PRIMARY KEY (tenant_id, id),
			UNIQUE (tenant_id, context_id, revision)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wc_checkpoints_context ON wc_checkpoints(tenant_id, context_id, revision DESC)`,
		`CREATE TABLE IF NOT EXISTS wc_resources (
			tenant_id TEXT NOT NULL,
			id TEXT NOT NULL,
			owner_id TEXT NOT NULL,
			kind TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			locator TEXT NOT NULL DEFAULT '',
			node_id TEXT NOT NULL DEFAULT '',
			purpose TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (tenant_id, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wc_resources_owner ON wc_resources(tenant_id, owner_id, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS wc_context_resources (
			tenant_id TEXT NOT NULL,
			context_id TEXT NOT NULL,
			resource_id TEXT NOT NULL,
			purpose TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			PRIMARY KEY (tenant_id, context_id, resource_id)
		)`,
		`CREATE TABLE IF NOT EXISTS wc_context_sessions (
			tenant_id TEXT NOT NULL,
			id TEXT NOT NULL,
			context_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			topic TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			PRIMARY KEY (tenant_id, id),
			UNIQUE (tenant_id, context_id, session_id)
		)`,
		`CREATE TABLE IF NOT EXISTS wc_context_tasks (
			tenant_id TEXT NOT NULL,
			id TEXT NOT NULL,
			context_id TEXT NOT NULL,
			request_id TEXT NOT NULL,
			card_id TEXT NOT NULL DEFAULT '',
			project_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			pack TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (tenant_id, id),
			UNIQUE (tenant_id, context_id, request_id)
		)`,
		`CREATE TABLE IF NOT EXISTS wc_relations (
			tenant_id TEXT NOT NULL,
			id TEXT NOT NULL,
			context_id TEXT NOT NULL,
			from_id TEXT NOT NULL,
			to_id TEXT NOT NULL,
			kind TEXT NOT NULL DEFAULT 'related_to',
			condition TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			PRIMARY KEY (tenant_id, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wc_relations_from ON wc_relations(tenant_id, from_id)`,
		`CREATE INDEX IF NOT EXISTS idx_wc_relations_to ON wc_relations(tenant_id, to_id)`,
		`CREATE TABLE IF NOT EXISTS wc_facts (
			tenant_id TEXT NOT NULL,
			id TEXT NOT NULL,
			context_id TEXT NOT NULL,
			owner_id TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'candidate',
			scope TEXT NOT NULL DEFAULT '',
			subject TEXT NOT NULL DEFAULT '',
			predicate TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			source_excerpt TEXT NOT NULL DEFAULT '',
			source_revision TEXT NOT NULL DEFAULT '',
			observed_at TEXT NOT NULL DEFAULT '',
			verified_at TEXT NOT NULL DEFAULT '',
			provenance TEXT NOT NULL DEFAULT '',
			supersedes_id TEXT NOT NULL DEFAULT '',
			memory_candidate_id TEXT NOT NULL DEFAULT '',
			version INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			PRIMARY KEY (tenant_id, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wc_facts_ctx ON wc_facts(tenant_id, context_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_wc_facts_key ON wc_facts(tenant_id, subject, predicate, scope, status)`,
		`CREATE TABLE IF NOT EXISTS wc_events (
			tenant_id TEXT NOT NULL,
			id TEXT NOT NULL,
			context_id TEXT NOT NULL,
			kind TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			dedup_key TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			PRIMARY KEY (tenant_id, id),
			UNIQUE (tenant_id, context_id, dedup_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wc_events_ctx ON wc_events(tenant_id, context_id, created_at DESC, id)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS wc_contexts_fts USING fts5(
			tenant_id UNINDEXED,
			id UNINDEXED,
			owner_id UNINDEXED,
			text,
			tokenize='unicode61'
		)`,
	},
	2: {
		// Facts gain their own FTS projection so knowledge recall ranks candidates
		// with MATCH instead of a full LIKE scan. Seeded text is pre-segmented (see
		// indexingText); the table is a rebuildable projection.
		`CREATE VIRTUAL TABLE IF NOT EXISTS wc_facts_fts USING fts5(
			tenant_id UNINDEXED,
			context_id UNINDEXED,
			fact_id UNINDEXED,
			text,
			tokenize='unicode61'
		)`,
	},
}

// --- generation ---

// newID returns a random, application-level identifier.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("wc-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// nowRFC3339 returns the current time as UTC RFC3339.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// --- scope isolation ---

// scopeArgs returns the (tenant_id, owner_id) pair used by every scoped query.
func scopeArgs(scope Scope) (string, string) {
	return strings.TrimSpace(scope.TenantID), strings.TrimSpace(scope.OwnerID)
}

// --- JSON helpers ---

// marshalStringList encodes a list as JSON, normalizing nil to an empty array
// so that stored payloads never contain a bare null.
func marshalStringList(values []string) string {
	if values == nil {
		values = []string{}
	}
	b, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// unmarshalStringList decodes a JSON list, always returning non-nil.
func unmarshalStringList(raw string) []string {
	out := []string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// encodeCheckpoint marshals a checkpoint, normalizing every list to non-nil.
func encodeCheckpoint(cp Checkpoint) string {
	normalized := cp
	normalized.Completed = nonNil(cp.Completed)
	normalized.InProgress = nonNil(cp.InProgress)
	normalized.NextActions = nonNil(cp.NextActions)
	normalized.Blockers = nonNil(cp.Blockers)
	normalized.OpenQuestions = nonNil(cp.OpenQuestions)
	normalized.EvidenceRefs = nonNil(cp.EvidenceRefs)
	b, err := json.Marshal(normalized)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// decodeCheckpoint unmarshals a checkpoint, normalizing every list to non-nil.
func decodeCheckpoint(raw string) Checkpoint {
	cp := Checkpoint{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &cp)
	}
	cp.Completed = nonNil(cp.Completed)
	cp.InProgress = nonNil(cp.InProgress)
	cp.NextActions = nonNil(cp.NextActions)
	cp.Blockers = nonNil(cp.Blockers)
	cp.OpenQuestions = nonNil(cp.OpenQuestions)
	cp.EvidenceRefs = nonNil(cp.EvidenceRefs)
	return cp
}

// nonNil returns an empty slice when values is nil.
func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// --- text recall helpers ---

// cjkTokenRE matches runs of CJK ideographs so they can be segmented for FTS.
var cjkTokenRE = regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}]+`)

// recallTerms splits a query into lowercase terms. CJK runs are segmented into
// unigrams, bigrams and the whole run so SQLite's unicode61 tokenizer (which
// does not segment CJK on its own) can still match Chinese text. The caller uses
// the same list for both the FTS MATCH expression and the LIKE fallback.
func recallTerms(query string) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(term string) {
		term = strings.TrimSpace(term)
		if term == "" {
			return
		}
		if _, ok := seen[term]; ok {
			return
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	for _, tok := range tokenRE.FindAllString(q, -1) {
		if cjkTokenRE.MatchString(tok) {
			runes := []rune(tok)
			for _, r := range runes {
				add(string(r))
			}
			for i := 0; i+1 < len(runes); i++ {
				add(string(runes[i : i+2]))
			}
			add(string(runes))
			continue
		}
		add(tok)
	}
	return out
}

// indexingText normalizes text for the FTS projection by pre-segmenting CJK
// runs with recallTerms and joining the terms with spaces. unicode61 cannot
// tokenize CJK, so without this step a whole Chinese run becomes one token and
// short substrings never match; pre-segmenting writes "\u77ed\u53e5" as its own token so
// a two-character CJK query can match. Latin text is passed through lowercased.
func indexingText(parts ...string) string {
	joined := strings.Join(parts, "\n")
	terms := recallTerms(joined)
	if len(terms) == 0 {
		return ""
	}
	return strings.Join(terms, " ")
}

var tokenRE = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// ftsMatchExpr builds a bounded FTS5 MATCH expression from recall terms. Each
// term is quoted so user text can never become an arbitrary MATCH expression.
func ftsMatchExpr(terms []string) string {
	clauses := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		escaped := strings.ReplaceAll(term, `"`, `""`)
		clauses = append(clauses, `"`+escaped+`"`)
	}
	return strings.Join(clauses, " OR ")
}

// escapeLike escapes LIKE wildcards in a user-provided substring.
func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}

// --- validation ---

// requiredText returns an error when value is blank.
func requiredText(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, field)
	}
	return nil
}

// checkScopeWritable rejects writes that lack an owner identity. The owner is
// generated from the authenticated principal; an empty owner would leak data
// across identities. TenantID may be empty for an operator/self-host deployment,
// in which case queries still isolate by owner.
func checkScopeWritable(scope Scope) error {
	_, ownerID := scopeArgs(scope)
	if ownerID == "" {
		return fmt.Errorf("%w: owner identity is required", ErrInvalid)
	}
	return nil
}

// validateScopeJSON validates a fact scope key. The value must be a JSON object
// mapping scope dimensions to values; unknown values must not masquerade as a
// concrete environment such as production.
func validateScopeJSON(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%w: fact scope is required", ErrInvalid)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		// Accept a bare dimension/value token for callers that pass a single key.
		obj = map[string]any{"subject": raw}
	}
	if len(obj) == 0 {
		return fmt.Errorf("%w: fact scope must not be empty", ErrInvalid)
	}
	for key, value := range obj {
		value := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", value)))
		if unknownScopeValues[value] {
			return fmt.Errorf("%w: scope %q cannot be %q; leave it unknown instead of defaulting", ErrInvalid, key, value)
		}
	}
	return nil
}

// normalizeProvenance defaults an empty provenance to human_report and rejects
// unknown values, so a human report is never mislabeled as a system query.
func normalizeProvenance(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return provenanceHumanReport, nil
	}
	if !validProvenance[value] {
		return "", fmt.Errorf("%w: unknown provenance %q", ErrInvalid, value)
	}
	return value, nil
}

// validateFactSource validates a fact source. When requireURI is set (confirm
// path) the source must be present and locator-safe (http(s) URL or explicit
// object reference) so a confirmed fact always points at inspectable evidence.
func validateFactSource(source string, requireURI bool) error {
	source = strings.TrimSpace(source)
	if source == "" {
		if requireURI {
			return fmt.Errorf("%w: a confirmed fact requires a source", ErrInvalid)
		}
		return nil
	}
	return validateLocator(source)
}

// validateObservedAt ensures a supplied observation time is RFC3339, so
// "recorded now" is never confused with "observed then".
func validateObservedAt(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("%w: observed_at must be RFC3339", ErrInvalid)
	}
	return nil
}

// validateLocator enforces the resource locator scheme policy: URLs are limited
// to http(s), must not target cloud metadata endpoints (SSRF), and must not
// embed credentials or signed parameters; node-bound local paths are allowed.
func validateLocator(locator string) error {
	locator = strings.TrimSpace(locator)
	if locator == "" {
		return nil
	}
	scheme, _, hasScheme := strings.Cut(locator, ":")
	if hasScheme && !strings.ContainsAny(scheme, "/\\ ") {
		switch strings.ToLower(scheme) {
		case "http", "https":
			parsed, err := url.Parse(locator)
			if err != nil {
				return fmt.Errorf("%w: locator is not a valid URL", ErrInvalid)
			}
			if host := strings.ToLower(parsed.Hostname()); host != "" && isBlockedHost(host) {
				return fmt.Errorf("%w: locator host %q is a metadata endpoint and is not allowed", ErrInvalid, host)
			}
			for key := range parsed.Query() {
				switch strings.ToLower(key) {
				case "token", "access_token", "api_key", "apikey", "secret", "password", "signature", "x-amz-signature", "x-amz-credential":
					return fmt.Errorf("%w: locator must not embed credentials or signed parameters", ErrInvalid)
				}
			}
			return nil
		case "file", "javascript", "data", "ftp", "ssh", "gopher", "dict":
			return fmt.Errorf("%w: locator scheme %q is not allowed", ErrInvalid, scheme)
		default:
			// Other explicit schemes (e.g. node://, git://, repo ids) are treated
			// as opaque object identifiers.
			return nil
		}
	}
	// Relative or absolute local path: allowed, directories are node-bound by
	// convention (locator + node_id).
	return nil
}

// isBlockedHost reports whether a host is a known cloud metadata endpoint.
func isBlockedHost(host string) bool {
	switch host {
	case "169.254.169.254", "metadata", "metadata.google.internal", "100.100.100.200", "fd00:ec2::254":
		return true
	}
	return false
}

// --- row scanning ---

// scanContextRow scans the standard context column set.
func scanContextRow(scanner interface{ Scan(...any) error }) (Context, error) {
	var (
		id, title, background, goal, status string
		version                             int
		checkpointJSON                      string
		createdAt, updatedAt                string
	)
	if err := scanner.Scan(&id, &title, &background, &goal, &status, &version, &checkpointJSON, &createdAt, &updatedAt); err != nil {
		return Context{}, err
	}
	return Context{
		ID:         id,
		Title:      title,
		Background: background,
		Goal:       goal,
		Status:     status,
		Version:    version,
		Checkpoint: decodeCheckpoint(checkpointJSON),
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
	}, nil
}

// rowScanner dereferences a nullable sql.NullString into a plain string.
func nullToString(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

// isForeignKeyErr reports whether err is a SQLite constraint violation.
func isConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "constraint failed") || strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "foreign key constraint")
}

// notFound wraps sql.ErrNoRows as ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
