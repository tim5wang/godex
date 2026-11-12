package workcontext

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/textutil"
)

type ExtractionSource struct {
	Truncated  bool   `json:"truncated,omitempty"`
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	SHA256     string `json:"sha256"`
	Tool       string `json:"tool,omitempty"`
	Successful bool   `json:"successful"`
}
type ExtractionJob struct {
	ID        string             `json:"id"`
	ContextID string             `json:"context_id"`
	SessionID string             `json:"session_id"`
	TurnID    string             `json:"turn_id"`
	InputHash string             `json:"input_hash"`
	Sources   []ExtractionSource `json:"sources"`
	Attempt   int                `json:"attempt"`
}
type ExtractedObservation struct {
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Kind      string   `json:"kind"`
	SourceIDs []string `json:"source_ids"`
}
type ExtractionResult struct {
	Model        string                 `json:"model,omitempty"`
	InputBytes   int                    `json:"input_bytes"`
	ModelMS      int64                  `json:"model_ms"`
	Observations []ExtractedObservation `json:"observations"`
	Added        int                    `json:"added"`
	Duplicates   int                    `json:"duplicates"`
	FactIDs      []string               `json:"fact_ids"`
}

func observationKey(input FactInput) string {
	normalized := strings.TrimSpace(strings.ReplaceAll(input.Content, "\r\n", "\n"))
	raw := strings.Join([]string{input.Kind, dreamScopeKey(input.Scope), input.Subject, input.Predicate, normalized, input.ValidUntil}, "\n")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CreateObservation is the atomic, ADD-only path for automatic capture and
// remember. Replays across sessions cannot create duplicates or revive retirees.
func (s *Store) CreateObservation(ctx context.Context, scope Scope, id string, input FactInput) (Fact, bool, error) {
	if err := checkScopeWritable(scope); err != nil {
		return Fact{}, false, err
	}
	if err := validateObservation(input); err != nil {
		return Fact{}, false, err
	}
	tenant, owner := scopeArgs(scope)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Fact{}, false, err
	}
	defer tx.Rollback()
	if _, err = requireEditableContextTx(ctx, tx, tenant, owner, id); err != nil {
		return Fact{}, false, err
	}
	f, added, err := addObservationTx(ctx, tx, tenant, owner, id, input, "candidate")
	if err == nil {
		err = tx.Commit()
	}
	return f, added, err
}
func validateObservation(input FactInput) error {
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.Content) == "" || len([]rune(input.Title)) > 200 || len([]rune(input.Content)) > 8000 {
		return fmt.Errorf("%w: bounded observation title and content required", ErrInvalid)
	}
	if err := validateScopeJSON(input.Scope); err != nil {
		return err
	}
	if err := validateObservedAt(input.ValidUntil); err != nil {
		return err
	}
	return validateFactSource(input.Source, false)
}
func addObservationTx(ctx context.Context, tx *sql.Tx, tenant, owner, id string, input FactInput, status string) (Fact, bool, error) {
	key := observationKey(input)
	var factID string
	err := tx.QueryRowContext(ctx, `SELECT fact_id FROM wc_observation_keys WHERE tenant_id=? AND context_id=? AND fingerprint=?`, tenant, id, key).Scan(&factID)
	if err == nil {
		f, e := getFactTx(ctx, tx, tenant, id, factID)
		return f, false, e
	}
	if err != sql.ErrNoRows {
		return Fact{}, false, err
	}
	// Backfill the deduplication key lazily for observations recorded before v10.
	rows, err := tx.QueryContext(ctx, `SELECT id,content,scope,subject,predicate,valid_until FROM wc_facts WHERE tenant_id=? AND context_id=? AND kind=? ORDER BY CASE status WHEN 'confirmed' THEN 0 WHEN 'observed' THEN 1 WHEN 'candidate' THEN 2 ELSE 3 END,created_at DESC,id`, tenant, id, input.Kind)
	if err != nil {
		return Fact{}, false, err
	}
	for rows.Next() {
		var existing Fact
		if err = rows.Scan(&existing.ID, &existing.Content, &existing.Scope, &existing.Subject, &existing.Predicate, &existing.ValidUntil); err != nil {
			rows.Close()
			return Fact{}, false, err
		}
		candidate := FactInput{Kind: input.Kind, Content: existing.Content, Scope: existing.Scope, Subject: existing.Subject, Predicate: existing.Predicate, ValidUntil: existing.ValidUntil}
		if observationKey(candidate) == key {
			factID = existing.ID
			break
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return Fact{}, false, err
	}
	rows.Close()
	added := factID == ""
	var f Fact
	if added {
		f = Fact{ID: newID(), ContextID: id, Title: strings.TrimSpace(input.Title), Content: strings.TrimSpace(input.Content), Kind: input.Kind, Scope: input.Scope, Subject: input.Subject, Predicate: input.Predicate, Source: input.Source, SourceExcerpt: input.SourceExcerpt, SourceRevision: input.SourceRevision, ObservedAt: input.ObservedAt, ValidUntil: input.ValidUntil, Provenance: "derived", Status: status, Version: 1, CreatedAt: nowRFC3339()}
		if err = insertFactTx(ctx, tx, tenant, owner, f); err != nil {
			return f, false, err
		}
		indexFactTx(ctx, tx, tenant, f)
		factID = f.ID
		if err = recordEventTx(ctx, tx, tenant, id, "fact_created", "新增观察："+f.Title, f.Source, "fact:"+f.ID, f.CreatedAt); err != nil {
			return f, false, err
		}
	} else {
		f, err = getFactTx(ctx, tx, tenant, id, factID)
		if err != nil {
			return f, false, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO wc_observation_keys VALUES(?,?,?,?)`, tenant, id, key, factID)
	return f, added, err
}

// EnqueueMemoryExtraction persists a bounded delta. Unchanged source content
// (including an empty earlier extraction result) is idempotent across restarts.
func (s *Store) EnqueueMemoryExtraction(ctx context.Context, scope Scope, job ExtractionJob) (bool, error) {
	if err := checkScopeWritable(scope); err != nil {
		return false, err
	}
	if job.SessionID == "" || job.TurnID == "" || len(job.Sources) == 0 || len(job.Sources) > 16 {
		return false, ErrInvalid
	}
	hashInput := []string{}
	for _, source := range job.Sources {
		if source.ID == "" || len(source.SHA256) != 64 {
			return false, ErrInvalid
		}
		hashInput = append(hashInput, fmt.Sprintf("%s|%s|%t|%s", source.Kind, source.Tool, source.Successful, source.SHA256))
	}
	sum := sha256.Sum256([]byte(strings.Join(hashInput, "\n")))
	job.InputHash = hex.EncodeToString(sum[:])
	job.ID = newID()
	raw, _ := json.Marshal(job)
	if len(raw) > 16000 {
		return false, fmt.Errorf("%w: extraction input exceeds 16000 bytes", ErrInvalid)
	}
	tenant, owner := scopeArgs(scope)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = requireEditableContextTx(ctx, tx, tenant, owner, job.ContextID); err != nil {
		return false, err
	}
	r, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO wc_memory_extractions(id,tenant_id,owner_id,context_id,session_id,turn_id,input_hash,input_json,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, job.ID, tenant, owner, job.ContextID, job.SessionID, job.TurnID, job.InputHash, string(raw), nowRFC3339())
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}

// ClaimMemoryExtraction leases exactly one persisted batch. A failed batch is
// retried with a one-minute backoff, at most three attempts, never a model loop.
func (s *Store) ClaimMemoryExtraction(ctx context.Context, scope Scope, id string, now time.Time) (*ExtractionJob, error) {
	tenant, owner := scopeArgs(scope)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = requireEditableContextTx(ctx, tx, tenant, owner, id); err != nil {
		return nil, err
	}
	var raw string
	var attempt int
	err = tx.QueryRowContext(ctx, `SELECT input_json,attempt FROM wc_memory_extractions WHERE tenant_id=? AND owner_id=? AND context_id=? AND attempt<3 AND (status='pending' OR (status IN ('running','failed') AND lease_until<=?)) ORDER BY rowid LIMIT 1`, tenant, owner, id, dreamTime(now)).Scan(&raw, &attempt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var job ExtractionJob
	if err = json.Unmarshal([]byte(raw), &job); err != nil {
		return nil, err
	}
	job.Attempt = attempt + 1
	_, err = tx.ExecContext(ctx, `UPDATE wc_memory_extractions SET status='running',attempt=?,lease_until=? WHERE id=?`, job.Attempt, dreamTime(now.Add(90*time.Second)), job.ID)
	if err != nil {
		return nil, err
	}
	return &job, tx.Commit()
}

func (s *Store) FinishMemoryExtraction(ctx context.Context, scope Scope, job ExtractionJob, result ExtractionResult, runErr error) error {
	tenant, owner := scopeArgs(scope)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = requireEditableContextTx(ctx, tx, tenant, owner, job.ContextID); err != nil {
		return err
	}
	var status, lease string
	var attempt int
	if err = tx.QueryRowContext(ctx, `SELECT status,attempt,lease_until FROM wc_memory_extractions WHERE tenant_id=? AND owner_id=? AND context_id=? AND id=?`, tenant, owner, job.ContextID, job.ID).Scan(&status, &attempt, &lease); err != nil {
		return err
	}
	if status != "running" || attempt != job.Attempt || lease <= dreamTime(time.Now().UTC()) {
		return ErrConflict
	}
	status = "completed"
	errorText := ""
	if runErr != nil {
		status = "failed"
		errorText = textutil.TruncateRunes(runErr.Error(), 800)
	} else if err = applyExtractedObservations(ctx, tx, tenant, owner, job, &result); err != nil {
		return err
	}
	raw, _ := json.Marshal(result)
	_, err = tx.ExecContext(ctx, `UPDATE wc_memory_extractions SET status=?,result_json=?,last_error=?,completed_at=?,lease_until=? WHERE id=?`, status, string(raw), errorText, nowRFC3339(), dreamTime(time.Now().UTC().Add(time.Minute)), job.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func applyExtractedObservations(ctx context.Context, tx *sql.Tx, tenant, owner string, job ExtractionJob, result *ExtractionResult) error {
	if len(result.Observations) > 6 {
		return fmt.Errorf("%w: at most 6 extracted observations", ErrInvalid)
	}
	sources := map[string]ExtractionSource{}
	for _, s := range job.Sources {
		sources[s.ID] = s
	}
	result.Added = 0
	result.Duplicates = 0
	result.FactIDs = nil
	for _, observation := range result.Observations {
		input, err := extractionFactInput(job, observation, sources)
		if err != nil {
			return err
		}
		f, added, err := addObservationTx(ctx, tx, tenant, owner, job.ContextID, input, "observed")
		if err != nil {
			return err
		}
		result.FactIDs = append(result.FactIDs, f.ID)
		if added {
			result.Added++
		} else {
			result.Duplicates++
		}
	}
	return nil
}
func extractionFactInput(job ExtractionJob, o ExtractedObservation, sources map[string]ExtractionSource) (FactInput, error) {
	if len(o.SourceIDs) == 0 || len(o.SourceIDs) > 4 {
		return FactInput{}, fmt.Errorf("%w: original extraction sources required", ErrInvalid)
	}
	switch o.Kind {
	case "fact", "method", "warning", "preference", "decision":
	default:
		return FactInput{}, fmt.Errorf("%w: unsupported observation kind", ErrInvalid)
	}
	excerpts := []string{}
	for _, id := range o.SourceIDs {
		s, ok := sources[id]
		if !ok {
			return FactInput{}, fmt.Errorf("%w: unknown extraction source", ErrInvalid)
		}
		excerpts = append(excerpts, id+" ["+s.Kind+"]: "+textutil.TruncateRunes(s.Text, 400))
	}
	scope, _ := json.Marshal(map[string]string{"work_context": job.ContextID})
	input := FactInput{Title: o.Title, Content: o.Content, Kind: o.Kind, Scope: string(scope), Source: "extraction://" + job.ID, SourceExcerpt: strings.Join(excerpts, "\n"), SourceRevision: job.InputHash, ObservedAt: nowRFC3339(), Provenance: "derived"}
	return input, validateObservation(input)
}

func (s *Store) PendingExtractionTargets(ctx context.Context, now time.Time) ([]DreamTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT x.tenant_id,x.owner_id,x.context_id FROM wc_memory_extractions x JOIN wc_contexts c ON c.tenant_id=x.tenant_id AND c.id=x.context_id WHERE c.status<>'archived' AND x.attempt<3 AND (x.status='pending' OR (x.status IN ('running','failed') AND x.lease_until<=?)) LIMIT 2`, dreamTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DreamTarget{}
	for rows.Next() {
		var t DreamTarget
		if err = rows.Scan(&t.Scope.TenantID, &t.Scope.OwnerID, &t.ContextID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ExtractionHistory exposes bounded diagnostics without re-injecting raw input.
func (s *Store) ExtractionHistory(ctx context.Context, scope Scope, id string) ([]map[string]any, error) {
	tenant, owner := scopeArgs(scope)
	if _, err := s.getContext(ctx, tenant, owner, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,turn_id,status,attempt,input_hash,result_json,last_error,created_at,completed_at FROM wc_memory_extractions WHERE tenant_id=? AND owner_id=? AND context_id=? ORDER BY rowid DESC LIMIT 20`, tenant, owner, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var key, session, turn, status, hash, raw, errorText, created, completed string
		var attempt int
		if err = rows.Scan(&key, &session, &turn, &status, &attempt, &hash, &raw, &errorText, &created, &completed); err != nil {
			return nil, err
		}
		var result any
		_ = json.Unmarshal([]byte(raw), &result)
		out = append(out, map[string]any{"id": key, "session_id": session, "turn_id": turn, "status": status, "attempt": attempt, "input_hash": hash, "result": result, "error": errorText, "created_at": created, "completed_at": completed})
	}
	return out, rows.Err()
}
