package workcontext

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type MemoryIssue struct {
	ID        string `json:"id"`
	Facts     []Fact `json:"facts"`
	CreatedAt string `json:"created_at"`
	issueIDs  []string
	before    map[string]Fact
}

type memoryIssueEvidence struct {
	Before   []Fact         `json:"before"`
	Versions map[string]int `json:"versions"`
}

func recordDreamIssues(ctx context.Context, tx *sql.Tx, tenant string, in DreamInput, groups [][]string, facts map[string]Fact, now time.Time) ([]string, []DreamMutation, error) {
	var issueIDs []string
	var mutations []DreamMutation
	for _, group := range groups {
		ids := map[string]bool{}
		valid := true
		var scope string
		for _, id := range group {
			f := facts[id]
			if retiredDreamFact(f) || f.ID == "" || strings.TrimSpace(f.Scope) == "" {
				valid = false
				break
			}
			key := dreamScopeKey(f.Scope)
			if scope != "" && scope != key {
				valid = false
				break
			}
			scope = key
			ids[id] = true
		}
		if !valid || len(ids) < 2 {
			continue
		}
		evidence := memoryIssueEvidence{Versions: map[string]int{}}
		for id := range ids {
			f := facts[id]
			evidence.Before = append(evidence.Before, f)
			if f.Status == "confirmed" || f.Status == "candidate" || f.Status == "observed" {
				m, err := changeDreamStatus(ctx, tx, tenant, f, "stale", "conflict", "")
				if err != nil {
					return nil, nil, err
				}
				mutations = append(mutations, m)
				f.Status = "stale"
				f.Version = m.AfterVersion
				facts[id] = f
			}
			evidence.Versions[id] = f.Version
		}
		// Overlapping proposals in this run share the final versions.
		raw, _ := json.Marshal(evidence)
		id := newID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO wc_memory_issues(id,tenant_id,context_id,run_id,evidence_json,created_at) VALUES(?,?,?,?,?,?)`, id, tenant, in.ContextID, in.RunID, string(raw), dreamTime(now)); err != nil {
			return nil, nil, err
		}
		issueIDs = append(issueIDs, id)
	}
	return issueIDs, mutations, nil
}

// MemoryIssues coalesces overlapping conflicts into one decision. Evidence
// changed by a person since detection is not silently acted on by an old issue.
func (s *Store) MemoryIssues(ctx context.Context, scope Scope, id string) ([]MemoryIssue, error) {
	tenant, owner := scopeArgs(scope)
	if _, err := s.getContext(ctx, tenant, owner, id); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return memoryIssuesTx(ctx, tx, tenant, id)
}

func memoryIssuesTx(ctx context.Context, tx *sql.Tx, tenant, id string) ([]MemoryIssue, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,evidence_json,created_at FROM wc_memory_issues WHERE tenant_id=? AND context_id=? AND status='open' ORDER BY created_at,id`, tenant, id)
	if err != nil {
		return nil, err
	}
	type row struct{ id, raw, created string }
	var saved []row
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.id, &r.raw, &r.created); err != nil {
			rows.Close()
			return nil, err
		}
		saved = append(saved, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	groups := []MemoryIssue{}
	for _, r := range saved {
		var e memoryIssueEvidence
		if err = json.Unmarshal([]byte(r.raw), &e); err != nil {
			return nil, err
		}
		issue := MemoryIssue{ID: r.id, CreatedAt: r.created, issueIDs: []string{r.id}, before: map[string]Fact{}}
		valid := true
		for _, before := range e.Before {
			f, e2 := getFactTx(ctx, tx, tenant, id, before.ID)
			if e2 != nil || f.Version != e.Versions[before.ID] || retiredDreamFact(f) {
				valid = false
				break
			}
			issue.Facts = append(issue.Facts, f)
			issue.before[f.ID] = before
		}
		if !valid || len(issue.Facts) < 2 {
			continue
		}
		groups = append(groups, issue)
	}
	return coalesceMemoryIssues(groups), nil
}

func coalesceMemoryIssues(groups []MemoryIssue) []MemoryIssue {
	for i := 0; i < len(groups); i++ {
		for j := i + 1; j < len(groups); {
			found := false
			for _, f := range groups[j].Facts {
				if _, ok := groups[i].before[f.ID]; ok {
					found = true
					break
				}
			}
			if !found {
				j++
				continue
			}
			for _, f := range groups[j].Facts {
				old, ok := groups[i].before[f.ID]
				if !ok {
					groups[i].Facts = append(groups[i].Facts, f)
				}
				if !ok || old.Status != "confirmed" {
					groups[i].before[f.ID] = groups[j].before[f.ID]
				}
			}
			groups[i].issueIDs = append(groups[i].issueIDs, groups[j].issueIDs...)
			groups = append(groups[:j], groups[j+1:]...)
			j = i + 1
		}
		sort.Slice(groups[i].Facts, func(a, b int) bool { return groups[i].Facts[a].ID < groups[i].Facts[b].ID })
	}
	return groups
}

type ResolveMemoryIssue struct {
	KeepID   string         `json:"keep_id"`
	Versions map[string]int `json:"versions"`
}

// Resolving a group is an explicit human decision. It never confirms an
// unverified observation; the selected observation can be verified separately.
func (s *Store) ResolveMemoryIssue(ctx context.Context, scope Scope, id, issueID string, input ResolveMemoryIssue) error {
	tenant, owner := scopeArgs(scope)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = requireEditableContextTx(ctx, tx, tenant, owner, id); err != nil {
		return err
	}
	issues, err := memoryIssuesTx(ctx, tx, tenant, id)
	if err != nil {
		return err
	}
	for _, issue := range issues {
		if issue.ID != issueID {
			continue
		}
		if _, ok := issue.before[input.KeepID]; !ok {
			return ErrInvalid
		}
		var mutations []DreamMutation
		for _, f := range issue.Facts {
			if input.Versions[f.ID] != f.Version {
				return ErrConflict
			}
			status := "archived"
			if f.ID == input.KeepID {
				status = "observed"
				if issue.before[f.ID].Current(time.Now()) {
					status = "confirmed"
				}
			}
			m, e := changeDreamStatus(ctx, tx, tenant, f, status, "human_resolution", input.KeepID)
			if e != nil {
				return e
			}
			mutations = append(mutations, m)
		}
		raw, _ := json.Marshal(mutations)
		for _, key := range issue.issueIDs {
			if _, err = tx.ExecContext(ctx, `UPDATE wc_memory_issues SET status='resolved',resolution_json=? WHERE tenant_id=? AND context_id=? AND id=?`, string(raw), tenant, id, key); err != nil {
				return err
			}
		}
		if err = recordEventTx(ctx, tx, tenant, id, "fact_reviewed", fmt.Sprintf("解决记忆冲突：保留 %s，归档 %d 条", input.KeepID, len(mutations)-1), "human", "memory_issue:"+issue.ID, nowRFC3339()); err != nil {
			return err
		}
		return tx.Commit()
	}
	return ErrConflict
}
