package workcontext

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/textutil"
)

type DreamMetrics struct {
	Model          string `json:"model"`
	InputBytes     int    `json:"input_bytes"`
	ModelMS        int64  `json:"model_ms"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// Mutations preserve every original field; automatic organization only changes
// status/version. Retired evidence remains available in the archive and audit.
type DreamMutation struct {
	Before       Fact   `json:"before"`
	AfterVersion int    `json:"after_version"`
	AfterStatus  string `json:"after_status"`
	Reason       string `json:"reason"`
	CanonicalID  string `json:"canonical_id,omitempty"`
}

type dreamAppliedResult struct {
	DreamResult
	GeneratedFactIDs  []string                `json:"generated_fact_ids"`
	References        []DreamCandidate        `json:"references,omitempty"`
	SkippedCandidates []DreamSkippedCandidate `json:"skipped_candidates,omitempty"`
	Mutations         []DreamMutation         `json:"mutations,omitempty"`
	IssueIDs          []string                `json:"issue_ids,omitempty"`
	Metrics           *DreamMetrics           `json:"metrics,omitempty"`
}

func dreamModelInput(in DreamInput) string {
	type modelFact struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		Content    string `json:"content"`
		Kind       string `json:"kind"`
		Status     string `json:"status"`
		Source     string `json:"source"`
		ObservedAt string `json:"observed_at,omitempty"`
		ValidUntil string `json:"valid_until,omitempty"`
		Truncated  bool   `json:"truncated,omitempty"`
	}
	facts := make([]modelFact, 0, len(in.Facts))
	var modelSources []Fact
	for _, f := range in.Facts {
		if retiredDreamFact(f) || (f.Status == "candidate" && strings.HasPrefix(f.Source, "dream://")) {
			continue
		}
		modelSources = append(modelSources, f)
		facts = append(facts, modelFact{f.ID, textutil.TruncateRunes(f.Title, 120), textutil.TruncateRunes(f.Content, 600), f.Kind, f.Status, textutil.TruncateRunes(f.Source, 120), f.ObservedAt, f.ValidUntil, len([]rune(f.Content)) > 600 || len([]rune(f.Title)) > 120})
	}
	groups := dreamScopeGroups(modelSources)
	for i := range groups {
		groups[i].Scope = textutil.TruncateRunes(groups[i].Scope, 160)
	}
	records := append([]DreamRecord(nil), in.Records...)
	for i := range records {
		records[i].Value = textutil.TruncateRunes(records[i].Value, 400)
	}
	raw, _ := json.Marshal(struct {
		Title           string            `json:"title"`
		Goal            string            `json:"goal"`
		Checkpoint      string            `json:"checkpoint"`
		PreviousSummary string            `json:"previous_summary"`
		Facts           []modelFact       `json:"facts"`
		Groups          []dreamScopeGroup `json:"scope_groups"`
		Records         []DreamRecord     `json:"records,omitempty"`
	}{textutil.TruncateRunes(in.Context.Title, 120), textutil.TruncateRunes(in.Context.Goal, 160), textutil.TruncateRunes(in.Context.Checkpoint.Summary, 300), textutil.TruncateRunes(in.PreviousSummary, 400), facts, groups, records})
	return string(raw)
}

func retiredDreamFact(f Fact) bool {
	return f.Status == "archived" || f.Status == "superseded" || f.Status == "rejected"
}

func changeDreamStatus(ctx context.Context, tx *sql.Tx, tenant string, f Fact, status, reason, canonical string) (DreamMutation, error) {
	m := DreamMutation{Before: f, AfterVersion: f.Version + 1, AfterStatus: status, Reason: reason, CanonicalID: canonical}
	r, err := tx.ExecContext(ctx, `UPDATE wc_facts SET status=?,version=version+1 WHERE tenant_id=? AND context_id=? AND id=? AND version=?`, status, tenant, f.ContextID, f.ID, f.Version)
	if err != nil {
		return m, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return m, err
	}
	if n != 1 {
		return m, ErrConflict
	}
	f.Status = status
	f.Version++
	if retiredDreamFact(f) {
		_, err = tx.ExecContext(ctx, `DELETE FROM wc_facts_fts WHERE tenant_id=? AND fact_id=?`, tenant, f.ID)
	} else {
		indexFactTx(ctx, tx, tenant, f)
	}
	return m, err
}

func sameDreamApplicability(a, b Fact) bool {
	return strings.TrimSpace(a.Scope) != "" && dreamScopeKey(a.Scope) == dreamScopeKey(b.Scope) && a.Kind == b.Kind && a.Subject == b.Subject && a.Predicate == b.Predicate && a.ObservedAt == b.ObservedAt && a.ValidUntil == b.ValidUntil && a.SourceRevision == b.SourceRevision
}

// Never merge incomplete model projections, competing confirmations, or facts
// already proposed for personal promotion. Canonical content is not rewritten.
func canMergeDreamFacts(a, b Fact, semantic bool) bool {
	if retiredDreamFact(a) || retiredDreamFact(b) || !sameDreamApplicability(a, b) || a.Status == "stale" || b.Status == "stale" || a.Status == "verification_blocked" || b.Status == "verification_blocked" || a.MemoryCandidateID != "" || b.MemoryCandidateID != "" {
		return false
	}
	if !semantic {
		return strings.TrimSpace(a.Content) == strings.TrimSpace(b.Content)
	}
	return len([]rune(a.Content)) <= 600 && len([]rune(b.Content)) <= 600 && len([]rune(a.Title)) <= 120 && len([]rune(b.Title)) <= 120
}

func organizeDreamFacts(ctx context.Context, tx *sql.Tx, tenant string, in DreamInput, result DreamResult, now time.Time) ([]DreamMutation, []string, error) {
	facts, err := loadDreamOrganizationFacts(ctx, tx, tenant, in)
	if err != nil {
		return nil, nil, err
	}
	conflicting := map[string]bool{}
	for _, group := range result.Conflicts {
		for _, id := range group {
			conflicting[id] = true
		}
	}
	var mutations []DreamMutation
	change := func(id, status, reason, canonical string) error {
		f := facts[id]
		m, e := changeDreamStatus(ctx, tx, tenant, f, status, reason, canonical)
		if e == nil {
			mutations = append(mutations, m)
			f.Status = status
			f.Version = m.AfterVersion
			facts[id] = f
		}
		return e
	}
	ids := make([]string, 0, len(facts))
	for id := range facts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		f := facts[id]
		if retiredDreamFact(f) || f.MemoryCandidateID != "" {
			continue
		}
		reason := ""
		if f.Status == "candidate" && strings.HasPrefix(f.Source, "dream://") {
			reason = "derived_reference"
		}
		if f.ValidUntil != "" {
			if deadline, e := time.Parse(time.RFC3339Nano, f.ValidUntil); e == nil && !now.Before(deadline) {
				reason = "expired"
			}
		}
		if reason != "" {
			if err = change(id, "archived", reason, ""); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, members := range dreamMergeSets(facts, ids, result.Duplicates, conflicting) {
		canonical := facts[members[0]]
		for _, id := range members[1:] {
			if err = change(id, "archived", "duplicate", canonical.ID); err != nil {
				return nil, nil, err
			}
		}
	}
	issueIDs, issueChanges, err := recordDreamIssues(ctx, tx, tenant, in, result.Conflicts, facts, now)
	mutations = append(mutations, issueChanges...)
	if err != nil {
		return nil, nil, err
	}
	// Ordinary observations remain explicitly unverified, without requiring a
	// separate approval task for every captured sentence.
	for _, id := range ids {
		if facts[id].Status == "candidate" {
			if err = change(id, "observed", "observation", ""); err != nil {
				return nil, nil, err
			}
		}
	}
	return mutations, issueIDs, err
}

func loadDreamOrganizationFacts(ctx context.Context, tx *sql.Tx, tenant string, in DreamInput) (map[string]Fact, error) {
	// Include old Dream-generated suggestions even though those derived writes
	// intentionally did not enter the source change journal.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM wc_facts WHERE tenant_id=? AND context_id=? AND status='candidate' AND source LIKE 'dream://%' AND memory_candidate_id='' ORDER BY id LIMIT 64`, tenant, in.ContextID)
	if err != nil {
		return nil, err
	}
	var legacyIDs []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		legacyIDs = append(legacyIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	facts := map[string]Fact{}
	for _, f := range in.Facts {
		current, e := getFactTx(ctx, tx, tenant, in.ContextID, f.ID)
		if e != nil {
			return nil, e
		}
		if current.Version != f.Version {
			return nil, ErrConflict
		}
		facts[f.ID] = f
	}
	for _, id := range legacyIDs {
		f, e := getFactTx(ctx, tx, tenant, in.ContextID, id)
		if e != nil {
			return nil, e
		}
		facts[id] = f
	}
	return facts, nil
}

func loadDreamInput(ctx context.Context, tx *sql.Tx, tenant, owner, id string, in *DreamInput) error {
	var err error
	in.Facts, err = dreamFacts(ctx, tx, tenant, id, in.Changes)
	if err != nil {
		return err
	}
	in.Records, err = dreamRecords(ctx, tx, tenant, owner, id, in.Changes)
	return err
}

func boundDreamInput(ctx context.Context, tx *sql.Tx, tenant, owner, id string, in *DreamInput) error {
	if err := loadDreamInput(ctx, tx, tenant, owner, id, in); err != nil {
		return err
	}
	for len(in.ModelInput()) > dreamInputBytes && len(in.Changes) > 1 {
		in.Changes = in.Changes[:len(in.Changes)-1]
		in.ThroughSeq = in.Changes[len(in.Changes)-1].Seq
		if err := loadDreamInput(ctx, tx, tenant, owner, id, in); err != nil {
			return err
		}
	}
	// Fill remaining space with comparisons after reserving room for changed
	// evidence. Large old records must not force tiny batches of new evidence.
	all, err := dreamRelatedFacts(ctx, tx, tenant, id, in.Facts)
	if err != nil {
		return err
	}
	for _, f := range all[len(in.Facts):] {
		in.Facts = append(in.Facts, f)
		if len(in.ModelInput()) > dreamInputBytes {
			in.Facts = in.Facts[:len(in.Facts)-1]
		}
	}
	return nil
}

// Union overlapping equivalent proposals before retiring any member. This
// prevents a chain A=B, B=C from losing its canonical record halfway through.
func dreamMergeSets(facts map[string]Fact, ids []string, proposals [][]string, conflicting map[string]bool) [][]string {
	parent := map[string]string{}
	for _, id := range ids {
		parent[id] = id
	}
	find := func(id string) string {
		for parent[id] != id {
			id = parent[id]
		}
		return id
	}
	join := func(a, b string, semantic bool) {
		if conflicting[a] || conflicting[b] || !canMergeDreamFacts(facts[a], facts[b], semantic) {
			return
		}
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	for _, group := range proposals {
		for i, a := range group {
			for _, b := range group[i+1:] {
				join(a, b, true)
			}
		}
	}
	for i, a := range ids {
		for _, b := range ids[i+1:] {
			join(a, b, false)
		}
	}
	grouped := map[string][]string{}
	for _, id := range ids {
		root := find(id)
		grouped[root] = append(grouped[root], id)
	}
	var groups [][]string
	for _, members := range grouped {
		if len(members) < 2 {
			continue
		}
		sort.Slice(members, func(i, j int) bool {
			a, b := facts[members[i]], facts[members[j]]
			if (a.Status == "confirmed") != (b.Status == "confirmed") {
				return a.Status == "confirmed"
			}
			return members[i] < members[j]
		})
		groups = append(groups, members)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}
