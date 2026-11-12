package workcontext

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/retrieval"
	"github.com/tim5wang/godex/internal/platform/textutil"
)

const dreamBatchSize = 16
const dreamLease = 2 * time.Minute

// MemoryState separates source changes from derived maintenance and read receipts.
type MemoryState struct {
	SourceSeq       int64  `json:"source_seq"`
	ProcessedSeq    int64  `json:"processed_seq"`
	Summary         string `json:"summary"`
	Enabled         bool   `json:"enabled"`
	IntervalSeconds int    `json:"interval_seconds"`
	NextRunAt       string `json:"next_run_at"`
	RunID           string `json:"run_id"`
	LeaseUntil      string `json:"lease_until"`
	Status          string `json:"status"`
	LastError       string `json:"last_error,omitempty"`
	FailureCount    int    `json:"failure_count"`
	RescanCursor    int64  `json:"rescan_cursor"`
	RescanThrough   int64  `json:"rescan_through"`
}

type MemoryChange struct {
	Seq      int64  `json:"seq"`
	Kind     string `json:"kind"`
	EntityID string `json:"entity_id"`
}

type DreamInput struct {
	RunID           string         `json:"run_id"`
	ContextID       string         `json:"context_id"`
	ThroughSeq      int64          `json:"through_seq"`
	Context         Context        `json:"context"`
	PreviousSummary string         `json:"previous_summary"`
	Changes         []MemoryChange `json:"changes"`
	Facts           []Fact         `json:"facts"`
	Records         []DreamRecord  `json:"records,omitempty"`
	Mode            string         `json:"mode"`
}

// DreamRecord contains one changed plan/link's bounded current state. A missing
// value denotes deletion. It supplies digest context, never evidence for facts.
type DreamRecord struct {
	Kind     string `json:"kind"`
	EntityID string `json:"entity_id"`
	Value    string `json:"value,omitempty"`
}

type DreamCandidate struct {
	Title         string   `json:"title"`
	Content       string   `json:"content"`
	SourceFactIDs []string `json:"source_fact_ids"`
}

type DreamResult struct {
	Summary    string           `json:"summary"`
	Duplicates [][]string       `json:"duplicates,omitempty"`
	Conflicts  [][]string       `json:"conflicts,omitempty"`
	Candidates []DreamCandidate `json:"candidates,omitempty"`
}

type DreamTarget struct {
	Scope     Scope
	ContextID string
}

func dreamTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func scanMemoryState(row interface{ Scan(...any) error }) (MemoryState, error) {
	var v MemoryState
	err := row.Scan(&v.SourceSeq, &v.ProcessedSeq, &v.Summary, &v.Enabled, &v.IntervalSeconds, &v.NextRunAt, &v.RunID, &v.LeaseUntil, &v.Status, &v.LastError, &v.FailureCount, &v.RescanCursor, &v.RescanThrough)
	return v, err
}

const memoryStateColumns = `source_seq,processed_seq,summary,enabled,interval_seconds,next_run_at,run_id,lease_until,status,last_error,failure_count,rescan_cursor,rescan_through`

func (s *Store) MemoryState(ctx context.Context, scope Scope, id string) (MemoryState, error) {
	tenant, owner := scopeArgs(scope)
	if _, err := s.getContext(ctx, tenant, owner, id); err != nil {
		return MemoryState{}, err
	}
	return scanMemoryState(s.db.QueryRowContext(ctx, `SELECT `+memoryStateColumns+` FROM wc_memory_state WHERE tenant_id=? AND context_id=?`, tenant, id))
}

func (s *Store) MemoryRevision(ctx context.Context, scope Scope) string {
	tenant, owner := scopeArgs(scope)
	var count, total int64
	if s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(m.source_seq),0) FROM wc_memory_state m JOIN wc_contexts c ON c.tenant_id=m.tenant_id AND c.id=m.context_id WHERE c.tenant_id=? AND c.owner_id=?`, tenant, owner).Scan(&count, &total) != nil {
		return "unavailable"
	}
	return fmt.Sprintf("%d:%d", count, total)
}

func (s *Store) Fact(ctx context.Context, scope Scope, contextID, factID string) (Fact, error) {
	tenant, owner := scopeArgs(scope)
	if _, err := s.getContext(ctx, tenant, owner, contextID); err != nil {
		return Fact{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Fact{}, err
	}
	defer tx.Rollback()
	return getFactTx(ctx, tx, tenant, contextID, factID)
}

// Facts loads only the owner's evidence, including retired history, without
// hydrating resources, events, sessions and task projections on each write.
func (s *Store) Facts(ctx context.Context, scope Scope, contextID string) ([]Fact, error) {
	tenant, owner := scopeArgs(scope)
	if _, err := s.getContext(ctx, tenant, owner, contextID); err != nil {
		return nil, err
	}
	return s.listFacts(ctx, tenant, contextID)
}

// ConfigureDream changes scheduling, not source memory. It never dirties itself.
func (s *Store) ConfigureDream(ctx context.Context, scope Scope, id string, enabled bool, interval int, now time.Time) (MemoryState, error) {
	if interval < 60 || interval > 30*24*3600 {
		return MemoryState{}, fmt.Errorf("%w: interval_seconds must be 60..2592000", ErrInvalid)
	}
	tenant, owner := scopeArgs(scope)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryState{}, err
	}
	defer tx.Rollback()
	if _, err = requireEditableContextTx(ctx, tx, tenant, owner, id); err != nil {
		return MemoryState{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE wc_memory_state SET enabled=?,interval_seconds=?,next_run_at=? WHERE tenant_id=? AND context_id=?`, enabled, interval, dreamTime(now.Add(time.Duration(interval)*time.Second)), tenant, id)
	if err != nil {
		return MemoryState{}, err
	}
	if err = tx.Commit(); err != nil {
		return MemoryState{}, err
	}
	return s.MemoryState(ctx, scope, id)
}

// DueDreams is host-only. Unchanged desktops are filtered in SQL before any
// job, conversation or model call is created. Expired leases can be recovered.
func (s *Store) DueDreams(ctx context.Context, now time.Time) ([]DreamTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.tenant_id,c.owner_id,c.id FROM wc_memory_state m JOIN wc_contexts c ON c.tenant_id=m.tenant_id AND c.id=m.context_id
 WHERE c.status='active' AND m.enabled=1 AND (m.source_seq>m.processed_seq OR m.rescan_cursor>=0) AND m.next_run_at<=? AND (m.run_id='' OR m.lease_until<=?) ORDER BY m.next_run_at,c.id LIMIT 2`, dreamTime(now), dreamTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DreamTarget
	for rows.Next() {
		var v DreamTarget
		if err = rows.Scan(&v.Scope.TenantID, &v.Scope.OwnerID, &v.ContextID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// BeginDream takes an immutable, bounded source snapshot and one per-desktop
// lease. Manual full mode bypasses the dirty check; automatic work never does.
func (s *Store) BeginDream(ctx context.Context, scope Scope, id string, force, automatic bool, now time.Time) (*DreamInput, MemoryState, error) {
	tenant, owner := scopeArgs(scope)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, MemoryState{}, err
	}
	defer tx.Rollback()
	c, err := requireEditableContextTx(ctx, tx, tenant, owner, id)
	if err != nil {
		return nil, MemoryState{}, err
	}
	state, err := scanMemoryState(tx.QueryRowContext(ctx, `SELECT `+memoryStateColumns+` FROM wc_memory_state WHERE tenant_id=? AND context_id=?`, tenant, id))
	if err != nil {
		return nil, state, err
	}
	if state.RunID != "" && state.LeaseUntil > dreamTime(now) {
		return nil, state, nil
	}
	if (!force || automatic) && state.SourceSeq <= state.ProcessedSeq && state.RescanCursor < 0 {
		return nil, state, nil
	}
	if automatic && (!state.Enabled || state.NextRunAt > dreamTime(now)) {
		return nil, state, nil
	}
	if state.RunID != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE wc_memory_runs SET status='failed',error='lease expired; recovered by a new run',completed_at=? WHERE id=? AND status='running'`, dreamTime(now), state.RunID); err != nil {
			return nil, state, err
		}
	}
	input := DreamInput{RunID: newID(), ContextID: id, Context: c, PreviousSummary: state.Summary, Mode: "manual"}
	if automatic {
		input.Mode = "automatic"
	}
	from, to := state.ProcessedSeq, state.SourceSeq
	if force && !automatic {
		state.RescanCursor = 0
		state.RescanThrough = state.SourceSeq
		if _, err = tx.ExecContext(ctx, `UPDATE wc_memory_state SET rescan_cursor=0,rescan_through=? WHERE tenant_id=? AND context_id=?`, state.SourceSeq, tenant, id); err != nil {
			return nil, state, err
		}
	}
	if state.RescanCursor >= 0 {
		from, to = state.RescanCursor, state.RescanThrough
		input.Mode = "full"
	}
	input.Changes, err = dreamChanges(ctx, tx, tenant, id, from, to)
	if err != nil {
		return nil, state, err
	}
	input.ThroughSeq = state.SourceSeq
	if len(input.Changes) > 0 {
		input.ThroughSeq = input.Changes[len(input.Changes)-1].Seq
	}
	input.Facts, err = dreamFacts(ctx, tx, tenant, id, input.Changes)
	if err != nil {
		return nil, state, err
	}
	input.Facts, err = dreamRelatedFacts(ctx, tx, tenant, id, input.Facts)
	if err != nil {
		return nil, state, err
	}
	input.Records, err = dreamRecords(ctx, tx, tenant, owner, id, input.Changes)
	if err != nil {
		return nil, state, err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, state, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO wc_memory_runs(id,tenant_id,context_id,through_seq,status,mode,input_json,created_at) VALUES(?,?,?,?,'running',?,?,?)`, input.RunID, tenant, id, input.ThroughSeq, input.Mode, string(raw), dreamTime(now))
	if err != nil {
		return nil, state, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE wc_memory_state SET run_id=?,lease_until=?,status='running',last_error='' WHERE tenant_id=? AND context_id=?`, input.RunID, dreamTime(now.Add(dreamLease)), tenant, id)
	if err != nil {
		return nil, state, err
	}
	if err = tx.Commit(); err != nil {
		return nil, state, err
	}
	state.RunID = input.RunID
	state.Status = "running"
	state.LeaseUntil = dreamTime(now.Add(dreamLease))
	return &input, state, nil
}

func dreamRecords(ctx context.Context, tx *sql.Tx, tenant, owner, id string, changes []MemoryChange) ([]DreamRecord, error) {
	seen := map[string]bool{}
	var records []DreamRecord
	for _, c := range changes {
		key := c.Kind + ":" + c.EntityID
		if c.Kind == "context" || c.Kind == "fact" || seen[key] {
			continue
		}
		seen[key] = true
		var value any
		var err error
		switch c.Kind {
		case "todo":
			value, err = getTodoTx(ctx, tx, tenant, id, c.EntityID)
		case "delegation":
			value, err = getDelegationTx(ctx, tx, tenant, id, c.EntityID)
		case "resource":
			var r Resource
			err = tx.QueryRowContext(ctx, `SELECT r.id,r.title,r.kind,r.locator,r.node_id,cr.purpose FROM wc_resources r JOIN wc_context_resources cr ON cr.tenant_id=r.tenant_id AND cr.resource_id=r.id WHERE r.tenant_id=? AND r.owner_id=? AND cr.context_id=? AND r.id=?`, tenant, owner, id, c.EntityID).Scan(&r.ID, &r.Title, &r.Kind, &r.Locator, &r.NodeID, &r.Purpose)
			value = r
		case "session":
			var link SessionLink
			err = tx.QueryRowContext(ctx, `SELECT id,session_id,topic,created_at FROM wc_context_sessions WHERE tenant_id=? AND context_id=? AND id=?`, tenant, id, c.EntityID).Scan(&link.ID, &link.SessionID, &link.Topic, &link.CreatedAt)
			value = link
		case "relation":
			var kind, from, to, condition string
			err = tx.QueryRowContext(ctx, `SELECT kind,from_id,to_id,condition FROM wc_relations WHERE tenant_id=? AND context_id=? AND id=?`, tenant, id, c.EntityID).Scan(&kind, &from, &to, &condition)
			value = map[string]string{"kind": kind, "from_id": from, "to_id": to, "condition": condition}
		default:
			continue
		}
		r := DreamRecord{Kind: c.Kind, EntityID: c.EntityID}
		if err == nil {
			raw, e := json.Marshal(value)
			if e != nil {
				return nil, e
			}
			r.Value = textutil.TruncateRunes(string(raw), 600)
		} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}

func dreamChanges(ctx context.Context, tx *sql.Tx, tenant, id string, from, to int64) ([]MemoryChange, error) {
	// Repeated edits of one entity contribute its latest snapshot once. Ordering
	// by the last sequence keeps the watermark safe for every unprocessed entity.
	rows, err := tx.QueryContext(ctx, `SELECT MAX(seq) AS last_seq,kind,entity_id FROM wc_memory_changes WHERE tenant_id=? AND context_id=? AND seq>? AND seq<=? GROUP BY kind,entity_id ORDER BY last_seq LIMIT ?`, tenant, id, from, to, dreamBatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryChange
	for rows.Next() {
		var v MemoryChange
		if err = rows.Scan(&v.Seq, &v.Kind, &v.EntityID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func dreamFacts(ctx context.Context, tx *sql.Tx, tenant, id string, changes []MemoryChange) ([]Fact, error) {
	seen := map[string]bool{}
	var out []Fact
	for _, c := range changes {
		if c.Kind != "fact" || seen[c.EntityID] {
			continue
		}
		seen[c.EntityID] = true
		fact, err := getFactTx(ctx, tx, tenant, id, c.EntityID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, fact)
	}
	return out, nil
}

// Compare an increment with a bounded relevant part of the desktop's existing
// knowledge. This is not a claim of exhaustive semantic conflict detection.
func dreamRelatedFacts(ctx context.Context, tx *sql.Tx, tenant, id string, changed []Fact) ([]Fact, error) {
	if len(changed) == 0 {
		return changed, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,context_id,title,content,kind,status,scope,subject,predicate,source,source_excerpt,source_revision,observed_at,verified_at,provenance,supersedes_id,memory_candidate_id,version,created_at,valid_until FROM wc_facts WHERE tenant_id=? AND context_id=? AND status NOT IN ('rejected','superseded') ORDER BY created_at DESC,id LIMIT 64`, tenant, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var query strings.Builder
	for _, f := range changed {
		seen[f.ID] = true
		query.WriteString(f.Title + " " + textutil.TruncateRunes(f.Content, 160) + " ")
	}
	terms := retrieval.Terms(query.String())
	type rankedFact struct {
		fact  Fact
		score int
	}
	var ranked []rankedFact
	for rows.Next() {
		f, e := scanFactRow(rows)
		if e != nil {
			return nil, e
		}
		if seen[f.ID] {
			continue
		}
		score := 0
		text := strings.ToLower(f.Title + " " + f.Content)
		for _, term := range terms {
			if strings.Contains(text, term) {
				score++
			}
		}
		for _, source := range changed {
			if source.Subject != "" && source.Subject == f.Subject && source.Predicate == f.Predicate {
				score += 20
			}
		}
		if score > 0 {
			ranked = append(ranked, rankedFact{f, score})
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	for i := 0; i < min(4, len(ranked)); i++ {
		changed = append(changed, ranked[i].fact)
	}
	return changed, nil
}

// ModelInput does not contain tool schemas or the full conversation history.
func (in DreamInput) ModelInput() string {
	in.Context.Title = textutil.TruncateRunes(in.Context.Title, 160)
	in.Context.Background = textutil.TruncateRunes(in.Context.Background, 400)
	in.Context.Goal = textutil.TruncateRunes(in.Context.Goal, 300)
	in.Context.Checkpoint.Summary = textutil.TruncateRunes(in.Context.Checkpoint.Summary, 600)
	in.Context.Checkpoint.Completed = nil
	in.Context.Checkpoint.EvidenceRefs = nil
	in.Context.Checkpoint.InProgress = nil
	in.Context.Checkpoint.NextActions = nil
	in.Context.Checkpoint.OpenQuestions = nil
	in.Context.Checkpoint.Blockers = nil
	in.PreviousSummary = textutil.TruncateRunes(in.PreviousSummary, 1200)
	in.Facts = append([]Fact(nil), in.Facts...)
	for i := range in.Facts {
		in.Facts[i].Title = textutil.TruncateRunes(in.Facts[i].Title, 160)
		in.Facts[i].Content = textutil.TruncateRunes(in.Facts[i].Content, 300)
		in.Facts[i].SourceExcerpt = textutil.TruncateRunes(in.Facts[i].SourceExcerpt, 160)
		in.Facts[i].Scope = textutil.TruncateRunes(in.Facts[i].Scope, 400)
		in.Facts[i].Source = textutil.TruncateRunes(in.Facts[i].Source, 240)
		in.Facts[i].SourceRevision = textutil.TruncateRunes(in.Facts[i].SourceRevision, 160)
		in.Facts[i].Subject = textutil.TruncateRunes(in.Facts[i].Subject, 160)
		in.Facts[i].Predicate = textutil.TruncateRunes(in.Facts[i].Predicate, 100)
		in.Facts[i].Kind = textutil.TruncateRunes(in.Facts[i].Kind, 60)
	}
	raw, _ := json.Marshal(in)
	return string(raw)
}

// FinishDream applies derived output under CAS. It never rewrites confirmed
// evidence or advances past the snapshot's processed source watermark.
func (s *Store) FinishDream(ctx context.Context, scope Scope, in DreamInput, result DreamResult, runErr error, now time.Time) error {
	tenant, owner := scopeArgs(scope)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = requireEditableContextTx(ctx, tx, tenant, owner, in.ContextID); err != nil {
		return err
	}
	state, err := scanMemoryState(tx.QueryRowContext(ctx, `SELECT `+memoryStateColumns+` FROM wc_memory_state WHERE tenant_id=? AND context_id=?`, tenant, in.ContextID))
	if err != nil {
		return err
	}
	if state.RunID != in.RunID {
		return ErrConflict
	}
	if runErr == nil {
		runErr = validateDreamEvidence(ctx, tx, tenant, in, result)
	}
	if runErr != nil {
		err = failDream(ctx, tx, tenant, in, state, runErr, now)
	} else {
		err = applyDream(ctx, tx, tenant, owner, in, result, state, now)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return runErr
}

func validateDreamEvidence(ctx context.Context, tx *sql.Tx, tenant string, in DreamInput, result DreamResult) error {
	if len([]rune(result.Summary)) > 2400 || len(result.Candidates) > 4 || len(result.Duplicates) > 16 || len(result.Conflicts) > 16 {
		return fmt.Errorf("%w: oversized Dream output", ErrInvalid)
	}
	known := map[string]Fact{}
	for _, f := range in.Facts {
		known[f.ID] = f
	}
	for _, pair := range append(append([][]string{}, result.Duplicates...), result.Conflicts...) {
		for _, id := range pair {
			f, ok := known[id]
			if !ok {
				return fmt.Errorf("%w: Dream referenced outside evidence", ErrInvalid)
			}
			current, err := getFactTx(ctx, tx, tenant, in.ContextID, id)
			if err != nil {
				return err
			}
			if current.Version != f.Version {
				return ErrConflict
			}
		}
	}
	for _, proposal := range result.Candidates {
		if strings.TrimSpace(proposal.Title) == "" || strings.TrimSpace(proposal.Content) == "" || len([]rune(proposal.Title)) > 200 || len([]rune(proposal.Content)) > 2000 || len(proposal.SourceFactIDs) == 0 {
			return fmt.Errorf("%w: invalid Dream candidate", ErrInvalid)
		}
		scope := ""
		for _, id := range proposal.SourceFactIDs {
			f, ok := known[id]
			if !ok {
				return fmt.Errorf("%w: unknown Dream source", ErrInvalid)
			}
			current, err := getFactTx(ctx, tx, tenant, in.ContextID, id)
			if err != nil {
				return err
			}
			if current.Version != f.Version || current.Status == "rejected" || current.Status == "superseded" {
				return ErrConflict
			}
			if scope != "" && scope != f.Scope {
				return fmt.Errorf("%w: cannot widen applicability", ErrInvalid)
			}
			scope = f.Scope
		}
	}
	return nil
}

func applyDream(ctx context.Context, tx *sql.Tx, tenant, owner string, in DreamInput, result DreamResult, state MemoryState, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO wc_memory_guard VALUES(?,?)`, tenant, in.ContextID); err != nil {
		return err
	}
	generated := []string{}
	for _, c := range result.Candidates {
		id, err := insertDreamCandidate(ctx, tx, tenant, owner, in, c, now)
		if err != nil {
			return err
		}
		if id != "" {
			generated = append(generated, id)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wc_memory_guard WHERE tenant_id=? AND context_id=?`, tenant, in.ContextID); err != nil {
		return err
	}
	if strings.TrimSpace(result.Summary) == "" {
		result.Summary = state.Summary
	}
	raw, _ := json.Marshal(struct {
		DreamResult
		GeneratedFactIDs []string `json:"generated_fact_ids"`
	}{result, generated})
	if _, err := tx.ExecContext(ctx, `UPDATE wc_memory_runs SET status='completed',result_json=?,completed_at=? WHERE id=?`, string(raw), dreamTime(now), in.RunID); err != nil {
		return err
	}
	next := now.Add(time.Duration(state.IntervalSeconds) * time.Second)
	cursor := state.RescanCursor
	if in.Mode == "full" {
		cursor = in.ThroughSeq
		if cursor >= state.RescanThrough {
			cursor = -1
		}
	}
	if state.SourceSeq > in.ThroughSeq || cursor >= 0 {
		next = now.Add(time.Minute)
	}
	_, err := tx.ExecContext(ctx, `UPDATE wc_memory_state SET processed_seq=MAX(processed_seq,?),summary=?,run_id='',lease_until='',status='completed',last_error='',failure_count=0,next_run_at=?,rescan_cursor=? WHERE tenant_id=? AND context_id=?`, in.ThroughSeq, result.Summary, dreamTime(next), cursor, tenant, in.ContextID)
	return err
}

func insertDreamCandidate(ctx context.Context, tx *sql.Tx, tenant, owner string, in DreamInput, c DreamCandidate, now time.Time) (string, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM wc_facts WHERE tenant_id=? AND context_id=? AND title=? AND content=? AND status NOT IN ('rejected','superseded')`, tenant, in.ContextID, c.Title, c.Content).Scan(&count); err != nil {
		return "", err
	}
	if count > 0 {
		return "", nil
	}
	source, err := getFactTx(ctx, tx, tenant, in.ContextID, c.SourceFactIDs[0])
	if err != nil {
		return "", err
	}
	for _, sourceID := range c.SourceFactIDs[1:] {
		f, e := getFactTx(ctx, tx, tenant, in.ContextID, sourceID)
		if e != nil {
			return "", e
		}
		if f.ValidUntil != "" {
			deadline, _ := time.Parse(time.RFC3339Nano, f.ValidUntil)
			current, _ := time.Parse(time.RFC3339Nano, source.ValidUntil)
			if source.ValidUntil == "" || deadline.Before(current) {
				source.ValidUntil = f.ValidUntil
			}
		}
	}
	refsData := make([]map[string]any, 0, len(c.SourceFactIDs))
	for _, id := range c.SourceFactIDs {
		for _, f := range in.Facts {
			if f.ID == id {
				refsData = append(refsData, map[string]any{"fact_id": id, "version": f.Version})
			}
		}
	}
	refs, _ := json.Marshal(refsData)
	fact := Fact{ValidUntil: source.ValidUntil, ID: newID(), ContextID: in.ContextID, Title: c.Title, Content: c.Content, Kind: "method", Status: "candidate", Scope: source.Scope, Source: "dream://" + in.RunID, SourceRevision: string(refs), SourceExcerpt: "Derived from source fact IDs; requires review: " + string(refs), ObservedAt: source.ObservedAt, Provenance: provenanceDerived, Version: 1, CreatedAt: dreamTime(now)}
	if err = insertFactTx(ctx, tx, tenant, owner, fact); err != nil {
		return "", err
	}
	indexFactTx(ctx, tx, tenant, fact)
	return fact.ID, nil
}

func failDream(ctx context.Context, tx *sql.Tx, tenant string, in DreamInput, state MemoryState, runErr error, now time.Time) error {
	message := textutil.TruncateRunes(runErr.Error(), 600)
	backoff := 5 * time.Minute * time.Duration(1<<min(state.FailureCount, 6))
	if _, err := tx.ExecContext(ctx, `UPDATE wc_memory_runs SET status='failed',error=?,completed_at=? WHERE id=?`, message, dreamTime(now), in.RunID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE wc_memory_state SET run_id='',lease_until='',status='failed',last_error=?,failure_count=failure_count+1,next_run_at=? WHERE tenant_id=? AND context_id=?`, message, dreamTime(now.Add(backoff)), tenant, in.ContextID)
	return err
}

// DreamRuns retains immutable inputs/results for inspection and recovery.
func (s *Store) DreamRuns(ctx context.Context, scope Scope, id string) ([]map[string]any, error) {
	tenant, owner := scopeArgs(scope)
	if _, err := s.getContext(ctx, tenant, owner, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,status,through_seq,mode,result_json,error,created_at,completed_at FROM wc_memory_runs WHERE tenant_id=? AND context_id=? ORDER BY created_at DESC,rowid DESC LIMIT 20`, tenant, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var run, status, mode, result, message, created, completed string
		var seq int64
		if err = rows.Scan(&run, &status, &seq, &mode, &result, &message, &created, &completed); err != nil {
			return nil, err
		}
		var decoded any
		_ = json.Unmarshal([]byte(result), &decoded)
		out = append(out, map[string]any{"id": run, "status": status, "through_seq": seq, "mode": mode, "result": decoded, "error": message, "created_at": created, "completed_at": completed})
	}
	return out, rows.Err()
}

// RevertDream restores derived output only. Any new evidence or human-reviewed
// generated fact makes the rollback conflict instead of undoing somebody's work.
func (s *Store) RevertDream(ctx context.Context, scope Scope, id, runID string) (MemoryState, error) {
	tenant, owner := scopeArgs(scope)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryState{}, err
	}
	defer tx.Rollback()
	if _, err = requireEditableContextTx(ctx, tx, tenant, owner, id); err != nil {
		return MemoryState{}, err
	}
	state, err := scanMemoryState(tx.QueryRowContext(ctx, `SELECT `+memoryStateColumns+` FROM wc_memory_state WHERE tenant_id=? AND context_id=?`, tenant, id))
	if err != nil {
		return state, err
	}
	if state.RunID != "" || state.SourceSeq != state.ProcessedSeq || state.RescanCursor >= 0 {
		return state, ErrConflict
	}
	var latest, inputJSON, resultJSON string
	err = tx.QueryRowContext(ctx, `SELECT id,input_json,result_json FROM wc_memory_runs WHERE tenant_id=? AND context_id=? AND status='completed' ORDER BY created_at DESC,rowid DESC LIMIT 1`, tenant, id).Scan(&latest, &inputJSON, &resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return state, ErrNotFound
	}
	if err != nil {
		return state, err
	}
	if latest != runID {
		return state, ErrConflict
	}
	var input DreamInput
	var output struct {
		GeneratedFactIDs []string `json:"generated_fact_ids"`
	}
	if err = json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return state, err
	}
	if err = json.Unmarshal([]byte(resultJSON), &output); err != nil {
		return state, err
	}
	for _, factID := range output.GeneratedFactIDs {
		f, e := getFactTx(ctx, tx, tenant, id, factID)
		if e != nil {
			return state, e
		}
		if f.Status != "candidate" || f.Version != 1 {
			return state, ErrConflict
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO wc_memory_guard VALUES(?,?)`, tenant, id); err != nil {
		return state, err
	}
	for _, factID := range output.GeneratedFactIDs {
		if _, err = tx.ExecContext(ctx, `UPDATE wc_facts SET status='rejected',version=version+1 WHERE tenant_id=? AND context_id=? AND id=?`, tenant, id, factID); err != nil {
			return state, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM wc_facts_fts WHERE tenant_id=? AND fact_id=?`, tenant, factID); err != nil {
			return state, err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM wc_memory_guard WHERE tenant_id=? AND context_id=?`, tenant, id); err != nil {
		return state, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wc_memory_runs SET status='reverted' WHERE id=?`, runID); err != nil {
		return state, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wc_memory_state SET summary=?,status='reverted' WHERE tenant_id=? AND context_id=?`, input.PreviousSummary, tenant, id); err != nil {
		return state, err
	}
	if err = tx.Commit(); err != nil {
		return state, err
	}
	return s.MemoryState(ctx, scope, id)
}
