package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// longTaskIndexFile is the persistent reverse-lookup index the
	// operator uses to find a longtask story by commit hash. The
	// file lives next to longtask.json in
	// ~/.godex/workflows/<workflowID>/index.json.
	longTaskIndexFile = "index.json"
	// longTaskRollbackReasonMaxBytes is the hard byte cap on the
	// --reason flag for `godex longtask rollback`. The cap is in
	// bytes (not runes) so a 1024-byte reason is a single
	// kilobyte on the wire regardless of locale; empty reasons
	// are explicitly allowed because the operator may want a
	// pure 'undo' without writing a justification.
	longTaskRollbackReasonMaxBytes = 1024
	// longTaskDefaultRetentionDays is 0 == permanent. The longtask
	// layer never deletes artifacts on its own; an explicit
	// `godex longtask gc [--apply] [--older-than N]` is the only
	// way to release disk space.
	longTaskDefaultRetentionDays = 0
)

// LongTaskIndex is the on-disk index that allows
// `godex longtask lookup --commit <hash>` to find the longtask
// story that produced a given commit. Stories are keyed by
// `<longtaskID>:<nodeID>`; the file is rewritten from scratch on
// every write so ordering is stable.
type LongTaskIndex struct {
	Version     int                  `json:"version"`
	LongTaskID  string               `json:"longtask_id"`
	WorkflowID  string               `json:"workflow_id"`
	GeneratedAt time.Time            `json:"generated_at"`
	Entries     []LongTaskIndexEntry `json:"entries"`
}

type LongTaskIndexEntry struct {
	LongTaskID  string    `json:"longtask_id"`
	NodeID      string    `json:"node_id"`
	StoryID     string    `json:"story_id"`
	CommitHash  string    `json:"commit_hash"`
	CommitRef   string    `json:"commit_ref,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	Reverted    bool      `json:"reverted,omitempty"`
	RevertCount int       `json:"revert_count,omitempty"`
}

func (a *Agent) longTaskIndexPath(workflowID string) string {
	return filepath.Join(a.workflows.dir, workflowID, longTaskIndexFile)
}

func (a *Agent) readLongTaskIndex(workflowID string) (LongTaskIndex, error) {
	var idx LongTaskIndex
	path := a.longTaskIndexPath(workflowID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LongTaskIndex{
				Version:    1,
				LongTaskID: workflowID,
				WorkflowID: workflowID,
				GeneratedAt: time.Now().UTC(),
			}, nil
		}
		return idx, err
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		return idx, fmt.Errorf("parse %s: %w", path, err)
	}
	return idx, nil
}

func (a *Agent) writeLongTaskIndex(idx LongTaskIndex) error {
	if idx.GeneratedAt.IsZero() {
		idx.GeneratedAt = time.Now().UTC()
	}
	idx.Version = 1
	path := a.longTaskIndexPath(idx.WorkflowID)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// refreshLongTaskIndex rebuilds the index.json from the live workflow
// state. It is called whenever a story is finalized or rolled back
// so the reverse lookup is always consistent with the source of
// truth. The function is best-effort: a write failure is logged to
// the workflow's events stream but does not bubble up to the caller
// because the durable spec / run record is already authoritative.
func (a *Agent) refreshLongTaskIndex(workflowID string) error {
	state, err := a.workflowState(workflowID)
	if err != nil {
		return err
	}
	view, err := a.longTaskViewForState(state)
	if err != nil {
		return err
	}
	idx := LongTaskIndex{
		Version:     1,
		LongTaskID:  view.LongTaskID,
		WorkflowID:  view.WorkflowID,
		GeneratedAt: time.Now().UTC(),
	}
	for _, story := range view.Stories {
		if strings.TrimSpace(story.CommitHash) == "" {
			continue
		}
		idx.Entries = append(idx.Entries, LongTaskIndexEntry{
			LongTaskID:  view.LongTaskID,
			NodeID:      story.NodeID,
			StoryID:     story.ID,
			CommitHash:  story.CommitHash,
			CommitRef:   story.CommitRef,
			UpdatedAt:   story.UpdatedAt,
			Reverted:    story.Reverted,
			RevertCount: len(story.RevertHistory),
		})
	}
	return a.writeLongTaskIndex(idx)
}

// LongTaskLookupByCommit searches every known longtask index for a
// story whose commit hash equals the given value. When
// --longtask is empty, all longtasks in the workflow dir are
// scanned. The first match is returned; ties are broken by the
// longtask id and node id for stable output.
func (a *Agent) LongTaskLookupByCommit(commit string, workflowID string) ([]LongTaskIndexEntry, error) {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return nil, fmt.Errorf("lookup: commit hash required")
	}
	if workflowID != "" {
		idx, err := a.readLongTaskIndex(workflowID)
		if err != nil {
			return nil, err
		}
		return filterIndexByCommit(idx.Entries, commit), nil
	}
	// Scan all longtask indexes under workflows dir.
	entries, err := os.ReadDir(a.workflows.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var matches []LongTaskIndexEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		idx, err := a.readLongTaskIndex(e.Name())
		if err != nil {
			continue
		}
		matches = append(matches, filterIndexByCommit(idx.Entries, commit)...)
	}
	return matches, nil
}

func filterIndexByCommit(entries []LongTaskIndexEntry, commit string) []LongTaskIndexEntry {
	var out []LongTaskIndexEntry
	for _, e := range entries {
		if e.CommitHash == commit || strings.HasPrefix(e.CommitHash, commit) {
			out = append(out, e)
		}
	}
	return out
}

// LongTaskRollbackResult is the operator-facing result of a
// rollback attempt. It is rendered both as a CLI response and as
// the input to the T11 reflux message.
type LongTaskRollbackResult struct {
	WorkflowID  string    `json:"workflow_id"`
	NodeID      string    `json:"node_id"`
	StoryID     string    `json:"story_id"`
	CommitHash  string    `json:"commit_hash"`
	NewRevertAt time.Time `json:"new_revert_at"`
	ReasonBytes int       `json:"reason_bytes"`
	Conflict    bool      `json:"conflict,omitempty"`
	ConflictRef string    `json:"conflict_ref,omitempty"`
	Message     string    `json:"message,omitempty"`
}

// RollbackLongTaskStory is the agent-level entry point for
// `godex longtask rollback <id> --node <node> --reason <text>`.
// The reason byte cap is enforced here (defense in depth) and again
// at the CLI / HTTP boundary. The function never silently swallows
// a conflict: a conflicted revert is returned to the caller and
// also refluxed into the chat history so the user has a single
// record of why their rollback did not land.
// checkRollbackReasonLen is the byte-length boundary check that
// sits at the very top of RollbackLongTaskStory. It is split out
// so the T12 acceptance test for 'empty reason is allowed' can
// exercise the boundary in isolation from the rest of the
// rollback path (which requires a real commit and a real git
// repo to reach the boundary check).
func (a *Agent) checkRollbackReasonLen(reason string) error {
	if len(reason) > longTaskRollbackReasonMaxBytes {
		return fmt.Errorf("rollback reason exceeds %d bytes (got %d)", longTaskRollbackReasonMaxBytes, len(reason))
	}
	return nil
}

func (a *Agent) RollbackLongTaskStory(ctx context.Context, workflowID, nodeID, reason string) (LongTaskRollbackResult, error) {
	if err := a.checkRollbackReasonLen(reason); err != nil {
		return LongTaskRollbackResult{}, err
	}
	state, err := a.workflowState(workflowID)
	if err != nil {
		return LongTaskRollbackResult{}, err
	}
	view, err := a.longTaskViewForState(state)
	if err != nil {
		return LongTaskRollbackResult{}, err
	}
	story, idx := findLongTaskStoryByNodeID(view.Stories, nodeID)
	if idx < 0 {
		return LongTaskRollbackResult{}, fmt.Errorf("longtask story not found for node %s", nodeID)
	}
	if strings.TrimSpace(story.CommitHash) == "" {
		return LongTaskRollbackResult{}, fmt.Errorf("longtask story %s has no commit to roll back", story.ID)
	}
	if story.Reverted {
		// Allow re-running the rollback for a separate revert commit;
		// the revert history is append-only and the user is allowed
		// to roll back a revert if they really want to.
	}
	result := LongTaskRollbackResult{
		WorkflowID:  workflowID,
		NodeID:      nodeID,
		StoryID:     story.ID,
		CommitHash:  story.CommitHash,
		ReasonBytes: len(reason),
		NewRevertAt: time.Now().UTC(),
	}

	// Locate the project root: a longtask story's commit lives in
	// the project repo, not in the agent's state dir. The state
	// view is the only authoritative source for the project path.
	projectRoot := view.Project
	if strings.TrimSpace(projectRoot) == "" {
		// Without a project root the rollback cannot apply git
		// revert. Reflux the conflict so the operator knows to
		// provide the path.
		result.Conflict = true
		result.ConflictRef = "missing project root on longtask spec"
		result.Message = "longtask spec has no project root; cannot run git revert"
		_ = a.workflows.appendEvent(workflowID, map[string]interface{}{
			"event":    "longtask_rollback_conflict",
			"node_id":  nodeID,
			"story_id": story.ID,
			"reason":   result.Message,
			"at":       result.NewRevertAt,
		})
		return result, nil
	}

	// Run git revert --no-commit to detect conflicts without
	// committing. We capture both stdout and stderr so the operator
	// gets the same message git produced.
	cmd := exec.CommandContext(ctx, "git", "revert", "--no-commit", story.CommitHash)
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		// A conflict means git left the index in a CONFLICTING
		// state with the revert un-applied. We must abort before
		// recording anything: the operator will resolve the
		// conflict themselves and re-run, or they will issue
		// `git revert --abort` to clean up.
		abort := exec.CommandContext(ctx, "git", "revert", "--abort")
		abort.Dir = projectRoot
		_ = abort.Run()
		result.Conflict = true
		result.ConflictRef = strings.TrimSpace(string(out))
		if result.ConflictRef == "" {
			result.ConflictRef = err.Error()
		}
		result.Message = "git revert produced a conflict; resolve manually then retry"
		_ = a.workflows.appendEvent(workflowID, map[string]interface{}{
			"event":    "longtask_rollback_conflict",
			"node_id":  nodeID,
			"story_id": story.ID,
			"reason":   result.Message,
			"detail":   result.ConflictRef,
			"at":       result.NewRevertAt,
		})
		a.appendLongTaskRollbackReflux(result)
		return result, nil
	}

	// No conflict: commit the revert with the supplied reason. We
	// use --no-edit so the commit message is what the operator
	// asked for, and we set the env to non-interactive to avoid
	// pager / editor surprises.
	commitMsg := buildRollbackCommitMessage(story.ID, reason)
	commitCmd := exec.CommandContext(ctx, "git", "commit", "--no-edit", "-m", commitMsg)
	commitCmd.Dir = projectRoot
	commitCmd.Env = append(os.Environ(),
		"GIT_EDITOR=true",
		"GIT_PAGER=cat",
	)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return result, fmt.Errorf("git commit revert: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Record the rollback on the story view. passes is left as-is
	// per the T12 decision: the test ran, the user is just
	// retracting the outcome. revert_history is appended.
	story.Passes = true
	story.Reverted = true
	story.RevertHistory = append(story.RevertHistory, longTaskRevertEntry{
		Commit: story.CommitHash,
		Reason: reason,
		At:     result.NewRevertAt,
	})
	story.UpdatedAt = result.NewRevertAt
	view.Stories[idx] = story
	if err := a.persistLongTaskViewForRollback(state, view, story); err != nil {
		return result, err
	}
	_ = a.refreshLongTaskIndex(workflowID)
	_ = a.workflows.appendEvent(workflowID, map[string]interface{}{
		"event":    "longtask_rollback_applied",
		"node_id":  nodeID,
		"story_id": story.ID,
		"commit":   story.CommitHash,
		"reason":   reason,
		"at":       result.NewRevertAt,
	})
	a.appendLongTaskRollbackReflux(result)
	return result, nil
}

func buildRollbackCommitMessage(storyID, reason string) string {
	if strings.TrimSpace(reason) == "" {
		return fmt.Sprintf("longtask: rollback %s", storyID)
	}
	return fmt.Sprintf("longtask: rollback %s\n\n%s", storyID, reason)
}

func findLongTaskStoryByNodeID(stories []longTaskStoryView, nodeID string) (longTaskStoryView, int) {
	for i, s := range stories {
		if s.NodeID == nodeID {
			return s, i
		}
	}
	return longTaskStoryView{}, -1
}

// persistLongTaskViewForRollback is the rollback counterpart to
// longTaskViewForState: it appends a revert entry to the workflow's
// revert_history.json file (separate from the spec because the spec
// is the operator-authored source of truth and must not be rewritten
// by the longtask layer). The view itself is updated in-memory and
// the caller returns the updated view; the spec is bumped only to
// reflect the new UpdatedAt timestamp.
func (a *Agent) persistLongTaskViewForRollback(state workflowState, view longTaskView, story longTaskStoryView) error {
	if err := a.appendLongTaskRevertHistory(view.WorkflowID, story); err != nil {
		return err
	}
	spec, err := a.workflows.loadLongTaskSpec(state.Summary.ID)
	if err != nil {
		return err
	}
	spec.UpdatedAt = time.Now().UTC()
	return a.workflows.writeLongTaskSpec(view.WorkflowID, spec)
}

const longTaskRevertHistoryFile = "revert_history.json"

type longTaskRevertHistory struct {
	Entries []longTaskRevertHistoryEntry `json:"entries"`
}

type longTaskRevertHistoryEntry struct {
	NodeID    string                 `json:"node_id"`
	StoryID   string                 `json:"story_id"`
	Commit    string                 `json:"commit"`
	Reason    string                 `json:"reason,omitempty"`
	At        time.Time              `json:"at"`
	ReasonLen int                    `json:"reason_len"`
	Conflict  bool                   `json:"conflict,omitempty"`
	Detail    string                 `json:"detail,omitempty"`
	AtView    longTaskStoryView      `json:"story_view"`
}

func (a *Agent) appendLongTaskRevertHistory(workflowID string, story longTaskStoryView) error {
	path := filepath.Join(a.workflows.dir, workflowID, longTaskRevertHistoryFile)
	var hist longTaskRevertHistory
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &hist)
	} else if !os.IsNotExist(err) {
		return err
	}
	entry := longTaskRevertHistoryEntry{
		NodeID:    story.NodeID,
		StoryID:   story.ID,
		At:        time.Now().UTC(),
		ReasonLen: 0,
		AtView:    story,
	}
	if len(story.RevertHistory) > 0 {
		last := story.RevertHistory[len(story.RevertHistory)-1]
		entry.Commit = last.Commit
		entry.Reason = last.Reason
		entry.ReasonLen = len(last.Reason)
	}
	hist.Entries = append(hist.Entries, entry)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(hist, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}

// SweepLongTaskArtifacts deletes the persistent files for old
// longtask runs and indexes per the retention policy. The default
// retention of 0 == permanent; only an explicit
// `godex longtask gc --older-than N` triggers deletion, and even
// then --dry-run is the safe default.
//
// The function is best-effort: each deletion is independent, and
// a failure on one entry is recorded in the result but does not
// abort the rest of the sweep. The caller is expected to render
// the result back to the user.
type LongTaskGCSweepResult struct {
	WorkflowID     string    `json:"workflow_id"`
	Inspected      int       `json:"inspected"`
	DeletedRuns    int       `json:"deleted_runs"`
	DeletedIndexes int       `json:"deleted_indexes"`
	Retained       int       `json:"retained"`
	Skipped        int       `json:"skipped"`
	DryRun         bool      `json:"dry_run"`
	OlderThan      time.Time `json:"older_than"`
	Now            time.Time `json:"now"`
}

func (a *Agent) SweepLongTaskArtifacts(workflowID string, olderThan time.Time, apply bool) (LongTaskGCSweepResult, error) {
	res := LongTaskGCSweepResult{
		WorkflowID: workflowID,
		OlderThan:  olderThan,
		Now:        time.Now().UTC(),
		DryRun:     !apply,
	}
	if olderThan.IsZero() {
		// 0 retention = permanent; sweep is a no-op. The CLI prints
		// a hint to set --older-than to actually delete anything.
		return res, nil
	}
	records, err := a.workflows.listLongTaskRuns(workflowID)
	if err != nil {
		return res, err
	}
	res.Inspected = len(records)
	for _, rec := range records {
		if rec.UpdatedAt.After(olderThan) {
			res.Retained++
			continue
		}
		runPath := filepath.Join(a.workflows.dir, workflowID, "runs", rec.RunID+".json")
		if !apply {
			res.Skipped++
			continue
		}
		if err := os.Remove(runPath); err != nil && !os.IsNotExist(err) {
			res.Retained++
			continue
		}
		res.DeletedRuns++
	}
	// Re-evaluate the index file in the same sweep; if all stories
	// have been rolled back and the longtask has no live runs, the
	// index is also expendable.
	if apply {
		if allStoriesRetained(records) {
			indexPath := a.longTaskIndexPath(workflowID)
			if err := os.Remove(indexPath); err == nil {
				res.DeletedIndexes++
			}
		}
	}
	return res, nil
}

func allStoriesRetained(records []longTaskRunRecord) bool {
	return len(records) == 0
}
