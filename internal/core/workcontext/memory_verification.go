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

// Evidence is supplied by the host's tool-event recorder. Model
// arguments only name tool call IDs; they cannot construct these receipts.
type VerificationEvidence struct {
	Successful   bool           `json:"successful"`
	Error        string         `json:"error,omitempty"`
	ToolCallID   string         `json:"tool_call_id"`
	ToolName     string         `json:"tool_name"`
	Input        map[string]any `json:"input"`
	OutputSHA256 string         `json:"output_sha256"`
	Excerpt      string         `json:"excerpt"`
	ObservedAt   string         `json:"observed_at"`
}

type VerificationOperation struct {
	Action      string                 `json:"action"`
	FactID      string                 `json:"fact_id,omitempty"`
	IfVersion   int                    `json:"if_version,omitempty"`
	FactIDs     []string               `json:"fact_ids,omitempty"`
	Versions    map[string]int         `json:"versions,omitempty"`
	KeepID      string                 `json:"keep_id,omitempty"`
	IssueID     string                 `json:"issue_id,omitempty"`
	Reason      string                 `json:"reason"`
	BlockKind   string                 `json:"block_kind,omitempty"`
	Replacement *FactInput             `json:"replacement,omitempty"`
	Evidence    []VerificationEvidence `json:"evidence,omitempty"`
}

type VerificationChange struct {
	Before *Fact `json:"before,omitempty"`
	After  Fact  `json:"after"`
}

type VerificationEntry struct {
	Action    string                 `json:"action"`
	Reason    string                 `json:"reason"`
	BlockKind string                 `json:"block_kind,omitempty"`
	Evidence  []VerificationEvidence `json:"evidence,omitempty"`
	Changes   []VerificationChange   `json:"changes"`
}

type MemoryVerificationRecord struct {
	ID        string              `json:"id"`
	SessionID string              `json:"session_id"`
	TurnID    string              `json:"turn_id"`
	CreatedAt string              `json:"created_at"`
	Entries   []VerificationEntry `json:"entries"`
}

// ApplyMemoryVerification is one atomic batch. Authorization is host-owned in
// the session tool adapter; all identities here are supplied by that adapter.
func (s *Store) ApplyMemoryVerification(ctx context.Context, scope Scope, id, sessionID, turnID string, ops []VerificationOperation) (MemoryVerificationRecord, error) {
	var record MemoryVerificationRecord
	if err := checkScopeWritable(scope); err != nil {
		return record, err
	}
	if len(ops) == 0 || len(ops) > 16 || sessionID == "" || turnID == "" {
		return record, fmt.Errorf("%w: verification requires a task and 1..16 operations", ErrInvalid)
	}
	tenant, owner := scopeArgs(scope)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return record, err
	}
	defer tx.Rollback()
	if _, err = requireEditableContextTx(ctx, tx, tenant, owner, id); err != nil {
		return record, err
	}
	now := time.Now().UTC()
	record = MemoryVerificationRecord{ID: newID(), SessionID: sessionID, TurnID: turnID, CreatedAt: dreamTime(now)}
	for _, op := range ops {
		if strings.TrimSpace(op.Reason) == "" || len([]rune(op.Reason)) > 1200 {
			return record, fmt.Errorf("%w: a bounded verification reason is required", ErrInvalid)
		}
		entry, e := applyVerificationOperation(ctx, tx, tenant, owner, id, record.ID, op, now)
		if e != nil {
			return record, e
		}
		record.Entries = append(record.Entries, entry)
	}
	raw, _ := json.Marshal(record)
	if _, err = tx.ExecContext(ctx, `INSERT INTO wc_memory_verifications VALUES(?,?,?,?,?,?,?)`, record.ID, tenant, id, sessionID, turnID, record.CreatedAt, string(raw)); err != nil {
		return record, err
	}
	if err = recordEventTx(ctx, tx, tenant, id, "fact_reviewed", fmt.Sprintf("Agent 深度核实：%d 项处理", len(ops)), "session://"+sessionID, "memory_verification:"+record.ID, record.CreatedAt); err != nil {
		return record, err
	}
	err = tx.Commit()
	return record, err
}

func applyVerificationOperation(ctx context.Context, tx *sql.Tx, tenant, owner, id, auditID string, op VerificationOperation, now time.Time) (VerificationEntry, error) {
	entry := VerificationEntry{Action: op.Action, Reason: op.Reason, BlockKind: op.BlockKind, Evidence: op.Evidence}
	var err error
	switch op.Action {
	case "confirm", "archive", "block", "replace":
		entry.Changes, err = verifyOneFact(ctx, tx, tenant, owner, id, auditID, op, now)
	case "merge", "resolve_conflict":
		entry.Changes, err = verifyFactGroup(ctx, tx, tenant, id, auditID, op, now)
	default:
		err = fmt.Errorf("%w: unsupported verification action", ErrInvalid)
	}
	return entry, err
}

func requireVerificationEvidence(evidence []VerificationEvidence) error {
	if len(evidence) == 0 || len(evidence) > 8 {
		return fmt.Errorf("%w: successful objective tool evidence is required", ErrInvalid)
	}
	for _, e := range evidence {
		if !e.Successful || e.ToolCallID == "" || e.ToolName == "" || len(e.OutputSHA256) != 64 || e.Excerpt == "" || e.ObservedAt == "" || validateObservedAt(e.ObservedAt) != nil {
			return fmt.Errorf("%w: invalid host evidence receipt", ErrInvalid)
		}
	}
	return nil
}

func confirmedWithEvidence(f Fact, auditID string, op VerificationOperation, now time.Time) (Fact, error) {
	if err := requireVerificationEvidence(op.Evidence); err != nil {
		return f, err
	}
	if err := validateScopeJSON(f.Scope); err != nil {
		return f, err
	}
	if f.ValidUntil != "" {
		deadline, e := time.Parse(time.RFC3339Nano, f.ValidUntil)
		if e != nil || !now.Before(deadline) {
			return f, fmt.Errorf("%w: expired evidence must be archived or replaced", ErrInvalid)
		}
	}
	f.Status = "confirmed"
	f.Provenance = "system_query"
	f.ObservedAt = dreamTime(now)
	f.VerifiedAt = dreamTime(now)
	f.Source = "verification://" + auditID + "/" + f.ID
	raw, _ := json.Marshal(op.Evidence)
	sum := sha256.Sum256(raw)
	f.SourceRevision = hex.EncodeToString(sum[:])
	f.SourceExcerpt = textutil.TruncateRunes(op.Reason+"\n"+op.Evidence[0].ToolName+" "+op.Evidence[0].Excerpt, 1800)
	return f, nil
}

func writeVerifiedFact(ctx context.Context, tx *sql.Tx, tenant string, before, after Fact) (VerificationChange, error) {
	after.Version = before.Version + 1
	r, err := tx.ExecContext(ctx, `UPDATE wc_facts SET status=?,source=?,source_excerpt=?,source_revision=?,observed_at=?,verified_at=?,provenance=?,version=? WHERE tenant_id=? AND context_id=? AND id=? AND version=?`, after.Status, after.Source, after.SourceExcerpt, after.SourceRevision, after.ObservedAt, after.VerifiedAt, after.Provenance, after.Version, tenant, before.ContextID, before.ID, before.Version)
	if err != nil {
		return VerificationChange{}, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return VerificationChange{}, err
	}
	if n != 1 {
		return VerificationChange{}, ErrConflict
	}
	if retiredDreamFact(after) {
		_, err = tx.ExecContext(ctx, `DELETE FROM wc_facts_fts WHERE tenant_id=? AND fact_id=?`, tenant, after.ID)
	} else {
		indexFactTx(ctx, tx, tenant, after)
	}
	return VerificationChange{Before: &before, After: after}, err
}

func verifyOneFact(ctx context.Context, tx *sql.Tx, tenant, owner, id, auditID string, op VerificationOperation, now time.Time) ([]VerificationChange, error) {
	f, err := getFactTx(ctx, tx, tenant, id, op.FactID)
	if err != nil {
		return nil, err
	}
	if op.IfVersion <= 0 || f.Version != op.IfVersion {
		return nil, ErrConflict
	}
	after := f
	if op.Action == "confirm" || op.Action == "replace" {
		if err := requireVerificationPredecessor(ctx, tx, tenant, id, f, nil); err != nil {
			return nil, err
		}
	}
	switch op.Action {
	case "confirm":
		if retiredDreamFact(f) {
			return nil, fmt.Errorf("%w: retired facts cannot be silently revived", ErrInvalid)
		}
		after, err = confirmedWithEvidence(f, auditID, op, now)
	case "archive":
		expired := false
		if f.ValidUntil != "" {
			t, e := time.Parse(time.RFC3339Nano, f.ValidUntil)
			expired = e == nil && !now.Before(t)
		}
		if !expired && !retiredDreamFact(f) {
			err = requireVerificationEvidence(op.Evidence)
		}
		after.Status = "archived"
	case "block":
		if retiredDreamFact(f) {
			return nil, fmt.Errorf("%w: retired fact does not require verification", ErrInvalid)
		}
		if op.BlockKind != "decision" && op.BlockKind != "missing_evidence" && op.BlockKind != "access" {
			return nil, fmt.Errorf("%w: block_kind must explain the blocker", ErrInvalid)
		}
		if (op.BlockKind != "decision" || (f.Kind != "preference" && f.Kind != "decision")) && len(op.Evidence) == 0 {
			return nil, fmt.Errorf("%w: attempt objective checks before recording a blocker", ErrInvalid)
		}
		if op.BlockKind == "access" {
			failed := false
			for _, e := range op.Evidence {
				failed = failed || !e.Successful
			}
			if !failed {
				return nil, fmt.Errorf("%w: access blocker needs a failed tool receipt", ErrInvalid)
			}
		}
		after.Status = "verification_blocked"
		after.SourceExcerpt = textutil.TruncateRunes("核实受阻 ["+op.BlockKind+"]: "+op.Reason+"\n"+f.SourceExcerpt, 1800)
	case "replace":
		return replaceVerifiedFact(ctx, tx, tenant, owner, id, auditID, f, op, now)
	}
	if err != nil {
		return nil, err
	}
	change, err := writeVerifiedFact(ctx, tx, tenant, f, after)
	return []VerificationChange{change}, err
}

func replaceVerifiedFact(ctx context.Context, tx *sql.Tx, tenant, owner, id, auditID string, old Fact, op VerificationOperation, now time.Time) ([]VerificationChange, error) {
	if op.Replacement == nil || strings.TrimSpace(op.Replacement.Title) == "" || strings.TrimSpace(op.Replacement.Content) == "" || len([]rune(op.Replacement.Title)) > 200 || len([]rune(op.Replacement.Content)) > 4000 {
		return nil, fmt.Errorf("%w: a bounded replacement title and content is required", ErrInvalid)
	}
	if retiredDreamFact(old) {
		return nil, fmt.Errorf("%w: retired fact cannot be replaced again", ErrInvalid)
	}
	if op.Replacement.Scope != "" && dreamScopeKey(op.Replacement.Scope) != dreamScopeKey(old.Scope) {
		return nil, fmt.Errorf("%w: replacement cannot widen applicability", ErrInvalid)
	}
	f := Fact{ID: newID(), ContextID: id, Title: op.Replacement.Title, Content: op.Replacement.Content, Kind: old.Kind, Scope: old.Scope, Subject: old.Subject, Predicate: old.Predicate, SupersedesID: old.ID, Version: 1, CreatedAt: dreamTime(now), ValidUntil: op.Replacement.ValidUntil}
	if err := validateObservedAt(f.ValidUntil); err != nil {
		return nil, err
	}
	f, err := confirmedWithEvidence(f, auditID, op, now)
	if err != nil {
		return nil, err
	}
	if err = insertFactTx(ctx, tx, tenant, owner, f); err != nil {
		return nil, err
	}
	indexFactTx(ctx, tx, tenant, f)
	after := old
	after.Status = "superseded"
	change, err := writeVerifiedFact(ctx, tx, tenant, old, after)
	if err != nil {
		return nil, err
	}
	return []VerificationChange{change, {After: f}}, nil
}

func verifyFactGroup(ctx context.Context, tx *sql.Tx, tenant, id, auditID string, op VerificationOperation, now time.Time) ([]VerificationChange, error) {
	ids := op.FactIDs
	var issue *MemoryIssue
	if op.Action == "resolve_conflict" {
		issues, err := memoryIssuesTx(ctx, tx, tenant, id)
		if err != nil {
			return nil, err
		}
		for i := range issues {
			if issues[i].ID == op.IssueID {
				issue = &issues[i]
				break
			}
		}
		if issue == nil {
			return nil, ErrConflict
		}
		ids = nil
		for _, f := range issue.Facts {
			ids = append(ids, f.ID)
		}
	}
	if len(ids) < 2 || len(ids) > 32 {
		return nil, fmt.Errorf("%w: a bounded fact group is required", ErrInvalid)
	}
	var facts []Fact
	seen := map[string]bool{}
	var keep Fact
	for _, key := range ids {
		if seen[key] {
			return nil, ErrInvalid
		}
		seen[key] = true
		f, err := getFactTx(ctx, tx, tenant, id, key)
		if err != nil {
			return nil, err
		}
		if op.Versions[key] <= 0 || f.Version != op.Versions[key] {
			return nil, ErrConflict
		}
		if retiredDreamFact(f) {
			return nil, ErrConflict
		}
		facts = append(facts, f)
		if f.ID == op.KeepID {
			keep = f
		}
	}
	if keep.ID == "" {
		return nil, ErrInvalid
	}
	for _, f := range facts {
		if dreamScopeKey(f.Scope) != dreamScopeKey(keep.Scope) || f.Kind != keep.Kind || f.Subject != keep.Subject || f.Predicate != keep.Predicate {
			return nil, fmt.Errorf("%w: group cannot widen applicability or change its subject", ErrInvalid)
		}
	}
	if err := requireVerificationPredecessor(ctx, tx, tenant, id, keep, seen); err != nil {
		return nil, err
	}
	verified, err := confirmedWithEvidence(keep, auditID, op, now)
	if err != nil {
		return nil, err
	}
	var changes []VerificationChange
	for _, f := range facts {
		after := f
		after.Status = "archived"
		if f.ID == keep.ID {
			after = verified
		}
		change, e := writeVerifiedFact(ctx, tx, tenant, f, after)
		if e != nil {
			return nil, e
		}
		changes = append(changes, change)
	}
	if issue != nil {
		raw, _ := json.Marshal(changes)
		for _, key := range issue.issueIDs {
			if _, err = tx.ExecContext(ctx, `UPDATE wc_memory_issues SET status='resolved',resolution_json=? WHERE tenant_id=? AND context_id=? AND id=?`, string(raw), tenant, id, key); err != nil {
				return nil, err
			}
		}
	}
	return changes, nil
}

func (s *Store) MemoryVerificationHistory(ctx context.Context, scope Scope, id, sessionID, turnID string) ([]MemoryVerificationRecord, error) {
	tenant, owner := scopeArgs(scope)
	if _, err := s.getContext(ctx, tenant, owner, id); err != nil {
		return nil, err
	}
	q := `SELECT result_json FROM wc_memory_verifications WHERE tenant_id=? AND context_id=?`
	args := []any{tenant, id}
	if sessionID != "" && turnID != "" {
		q += ` AND session_id=? AND turn_id=?`
		args = append(args, sessionID, turnID)
	}
	q += ` ORDER BY rowid DESC`
	if sessionID == "" || turnID == "" {
		q += ` LIMIT 50`
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemoryVerificationRecord{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v MemoryVerificationRecord
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// A pending superseding proposal must not silently leave its predecessor live,
// or mutate a predecessor whose version was never checked by this operation.
func requireVerificationPredecessor(ctx context.Context, tx *sql.Tx, tenant, id string, f Fact, included map[string]bool) error {
	if f.SupersedesID == "" {
		return nil
	}
	predecessor, err := getFactTx(ctx, tx, tenant, id, f.SupersedesID)
	if err != nil {
		return err
	}
	if !retiredDreamFact(predecessor) && !included[predecessor.ID] {
		return fmt.Errorf("%w: pending supersedes proposal requires merge including the predecessor and both versions before confirmation/replacement", ErrInvalid)
	}
	return nil
}
