package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/workerruntime"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

func (a *Agent) ReviewDurableSubagent(id string) (subagentReview, error) {
	result, err := a.WorkerRuntime().Review(context.Background(), workerruntime.ReviewRequest{JobID: id, WorkerID: localGoDexWorkerID})
	if err != nil {
		return subagentReview{}, err
	}
	return subagentReviewFromWorkerReview(result), nil
}

func (a *Agent) reviewDurableSubagentDirect(id string) (subagentReview, error) {
	job, err := a.subagentJobs.Get(id)
	if err != nil {
		return subagentReview{}, err
	}
	return reviewSubagentJob(job)
}

func (a *Agent) MergeDurableSubagent(id string) (subagentMergeResult, error) {
	return a.MergeDurableSubagentWithContext(context.Background(), id)
}

func (a *Agent) MergeDurableSubagentWithContext(ctx context.Context, id string) (subagentMergeResult, error) {
	result, err := a.WorkerRuntime().Merge(ctx, workerruntime.MergeRequest{JobID: id, WorkerID: localGoDexWorkerID})
	if err != nil {
		return subagentMergeResult{}, err
	}
	return subagentMergeFromWorkerMerge(result), nil
}

func (a *Agent) mergeDurableSubagentDirect(ctx context.Context, id string) (subagentMergeResult, error) {
	job, err := a.subagentJobs.Get(id)
	if err != nil {
		return subagentMergeResult{}, err
	}
	if len(job.WriteScope) == 0 {
		return subagentMergeResult{}, fmt.Errorf("subagent merge requires write_scope")
	}
	if job.Status == subagentStatusRunning {
		return subagentMergeResult{}, fmt.Errorf("subagent job %s is still running", job.ID)
	}
	review, err := reviewSubagentJob(job)
	if err != nil {
		return subagentMergeResult{}, err
	}
	result := subagentMergeResult{
		JobID:       job.ID,
		Status:      subagentMergePending,
		WorktreeDir: job.WorktreeDir,
	}
	if len(review.Changes) == 0 {
		updated, err := a.subagentJobs.SetMergeStatus(job.ID, subagentMergeNoChanges, subagentProgressEvent{
			Phase:   "merge_reviewed",
			Message: "Subagent merge reviewed with no changes.",
		})
		if err != nil {
			return subagentMergeResult{}, err
		}
		subagentEventTargetFromContext(ctx).emit(updated, "merge_reviewed", "Subagent merge reviewed with no changes.", "", "", "", "")
		result.Status = subagentMergeNoChanges
		return result, nil
	}
	conflicts, err := detectSubagentMergeConflicts(a.cfg.WorkspaceDir, job.BaselineDir, job.WorktreeDir, review.Changes)
	if err != nil {
		return subagentMergeResult{}, err
	}
	if len(conflicts) > 0 {
		updated, updateErr := a.subagentJobs.SetMergeStatus(job.ID, subagentMergeConflict, subagentProgressEvent{
			Phase:   "merge_conflict",
			Message: "Subagent merge has conflicts.",
			Error:   strings.Join(conflicts, "\n"),
		})
		if updateErr != nil {
			return subagentMergeResult{}, updateErr
		}
		subagentEventTargetFromContext(ctx).emit(updated, "merge_conflict", "Subagent merge has conflicts.", "", "", strings.Join(conflicts, "\n"), "")
		result.Status = subagentMergeConflict
		result.Conflicts = conflicts
		return result, nil
	}
	if err := applySubagentChanges(a.cfg.WorkspaceDir, job.WorktreeDir, review.Changes); err != nil {
		return subagentMergeResult{}, err
	}
	updated, err := a.subagentJobs.SetMergeStatus(job.ID, subagentMergeMerged, subagentProgressEvent{
		Phase:   "merged",
		Message: fmt.Sprintf("Subagent merge applied %d file change(s).", len(review.Changes)),
	})
	if err != nil {
		return subagentMergeResult{}, err
	}
	subagentEventTargetFromContext(ctx).emit(updated, "merged", fmt.Sprintf("Subagent merge applied %d file change(s).", len(review.Changes)), "", "", "", "")
	result.Status = subagentMergeMerged
	result.Applied = review.Changes
	return result, nil
}

func (a *Agent) CleanupDurableSubagentWorkspace(id string) (subagentWorkspaceCleanupResult, error) {
	job, err := a.subagentJobs.Get(id)
	if err != nil {
		return subagentWorkspaceCleanupResult{}, err
	}
	if !subagentWorkspaceCanBeCleaned(job) {
		return subagentWorkspaceCleanupResult{JobID: job.ID, Reason: "subagent workspace is not eligible for cleanup"}, nil
	}
	if err := cleanupSubagentWorkspace(job, a.cfg.WorkspaceDir); err != nil {
		return subagentWorkspaceCleanupResult{}, err
	}
	if _, err := a.subagentJobs.SetWorkspaceCleaned(job.ID); err != nil {
		return subagentWorkspaceCleanupResult{}, err
	}
	return subagentWorkspaceCleanupResult{JobID: job.ID, Cleaned: true}, nil
}

func CleanupSubagentWorkspaces(cfg *config.Config, opts SubagentWorkspaceGCOptions) (SubagentWorkspaceGCResult, error) {
	if cfg == nil {
		return SubagentWorkspaceGCResult{DryRun: opts.DryRun}, fmt.Errorf("missing config")
	}
	store := newSubagentJobStore(subagentJobsDir(cfg))
	agent := &Agent{cfg: cfg, subagentJobs: store}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	result := SubagentWorkspaceGCResult{DryRun: opts.DryRun}
	for _, job := range store.List() {
		if !subagentWorkspaceGCEligible(job, opts, now) {
			continue
		}
		bytes := subagentWorkspaceBytes(job)
		item := SubagentWorkspaceGCItem{
			JobID:        job.ID,
			Isolation:    job.Isolation,
			MergeStatus:  job.MergeStatus,
			CleanupState: job.CleanupState,
			Bytes:        bytes,
		}
		result.Candidates++
		result.Bytes += bytes
		if !opts.DryRun {
			cleanup, err := agent.CleanupDurableSubagentWorkspace(job.ID)
			if err != nil {
				item.Reason = err.Error()
			} else {
				item.Cleaned = cleanup.Cleaned
				item.Reason = cleanup.Reason
				if cleanup.Cleaned {
					result.Cleaned++
				}
			}
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func subagentWorkspaceGCEligible(job *subagentJob, opts SubagentWorkspaceGCOptions, now time.Time) bool {
	if job == nil || job.CleanupState == subagentCleanupCleaned || strings.TrimSpace(job.WorktreeDir) == "" {
		return false
	}
	if opts.MergedOnly {
		return job.MergeStatus == subagentMergeMerged || job.MergeStatus == subagentMergeNoChanges
	}
	if subagentWorkspaceCanBeCleaned(job) {
		return true
	}
	if opts.OlderThan > 0 && subagentStatusTerminal(job.Status) && !job.FinishedAt.IsZero() {
		return now.Sub(job.FinishedAt) >= opts.OlderThan
	}
	return false
}

func subagentWorkspaceBytes(job *subagentJob) int64 {
	if job != nil && (job.Isolation == subagentIsolationSharedReadOnly || job.Isolation == subagentIsolationSharedApproval) {
		return dirSize(job.BaselineDir)
	}
	var total int64
	for _, path := range []string{job.WorktreeDir, job.BaselineDir} {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		total += dirSize(path)
	}
	return total
}

func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func subagentWorkspaceCanBeCleaned(job *subagentJob) bool {
	if job == nil || job.CleanupState == subagentCleanupCleaned {
		return false
	}
	switch job.MergeStatus {
	case subagentMergeMerged, subagentMergeNoChanges:
		return subagentStatusTerminal(job.Status)
	default:
		return false
	}
}

func cleanupSubagentWorkspace(job *subagentJob, repoRoot string) error {
	if job == nil {
		return nil
	}
	if job.Isolation == subagentIsolationGitWorktree && strings.TrimSpace(job.GitBranch) != "" {
		if err := removeSubagentGitWorktree(repoRoot, job.WorktreeDir, job.GitBranch); err != nil {
			return err
		}
	} else if strings.TrimSpace(job.WorktreeDir) != "" && filepath.Clean(job.WorktreeDir) != filepath.Clean(repoRoot) {
		if err := os.RemoveAll(job.WorktreeDir); err != nil {
			return err
		}
	}
	if strings.TrimSpace(job.BaselineDir) != "" {
		if err := os.RemoveAll(job.BaselineDir); err != nil {
			return err
		}
	}
	return nil
}

func removeSubagentGitWorktree(repoRoot, worktreeDir, branch string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		repoRoot = filepath.Dir(filepath.Dir(filepath.Clean(worktreeDir)))
	}
	if strings.TrimSpace(worktreeDir) != "" {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(worktreeDir)
			_, _ = gitOutput(repoRoot, "worktree", "prune")
			if strings.TrimSpace(string(out)) != "" {
				return fmt.Errorf("git worktree remove: %w: %s", err, strings.TrimSpace(string(out)))
			}
			return fmt.Errorf("git worktree remove: %w", err)
		}
	}
	if strings.TrimSpace(branch) != "" {
		cmd := exec.Command("git", "branch", "-D", branch)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil && !strings.Contains(string(out), "not found") {
			return fmt.Errorf("git branch delete: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func collectSubagentChanges(baselineDir, worktreeDir string, scope []string) ([]subagentFileChange, error) {
	paths := map[string]struct{}{}
	for _, root := range []string{baselineDir, worktreeDir} {
		for _, rel := range normalizeWriteScope(scope) {
			path, err := safeJoinUnderRoot(root, rel)
			if err != nil {
				return nil, err
			}
			if err := collectFilesUnder(root, path, paths); err != nil {
				return nil, err
			}
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	changes := make([]subagentFileChange, 0, len(ordered))
	for _, rel := range ordered {
		baseInfo, baseExists, err := fileSnapshot(filepath.Join(baselineDir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		workInfo, workExists, err := fileSnapshot(filepath.Join(worktreeDir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		switch {
		case !baseExists && !workExists:
			continue
		case !baseExists && workExists:
			changes = append(changes, subagentFileChange{Path: rel, Status: "added", Bytes: workInfo.size, Binary: workInfo.binary})
		case baseExists && !workExists:
			changes = append(changes, subagentFileChange{Path: rel, Status: "deleted", Bytes: baseInfo.size, Binary: baseInfo.binary})
		case baseInfo.hash != workInfo.hash || baseInfo.mode != workInfo.mode:
			changes = append(changes, subagentFileChange{Path: rel, Status: "modified", Bytes: workInfo.size, Binary: workInfo.binary || baseInfo.binary})
		}
	}
	return changes, nil
}

func collectFilesUnder(root, start string, out map[string]struct{}) error {
	info, err := os.Lstat(start)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	add := func(path string) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rel)), "/")
		if rel != "" && rel != "." {
			out[rel] = struct{}{}
		}
		return nil
	}
	if !info.IsDir() {
		return add(start)
	}
	return filepath.WalkDir(start, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == start {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipSubagentSnapshotDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		return add(path)
	})
}

type subagentFileSnapshot struct {
	hash   string
	size   int64
	mode   os.FileMode
	binary bool
}

func fileSnapshot(path string) (subagentFileSnapshot, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return subagentFileSnapshot{}, false, nil
	}
	if err != nil {
		return subagentFileSnapshot{}, false, err
	}
	if info.IsDir() {
		return subagentFileSnapshot{}, false, nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return subagentFileSnapshot{}, false, err
		}
		sum := sha256.Sum256([]byte("symlink:" + target))
		return subagentFileSnapshot{hash: fmt.Sprintf("%x", sum[:]), size: int64(len(target)), mode: info.Mode(), binary: false}, true, nil
	}
	if !info.Mode().IsRegular() {
		return subagentFileSnapshot{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return subagentFileSnapshot{}, false, err
	}
	sum := sha256.Sum256(data)
	return subagentFileSnapshot{
		hash:   fmt.Sprintf("%x", sum[:]),
		size:   info.Size(),
		mode:   info.Mode().Perm(),
		binary: !utf8.Valid(data),
	}, true, nil
}

func detectSubagentMergeConflicts(workspaceDir, baselineDir, worktreeDir string, changes []subagentFileChange) ([]string, error) {
	conflicts := make([]string, 0)
	for _, change := range changes {
		mainPath := filepath.Join(workspaceDir, filepath.FromSlash(change.Path))
		basePath := filepath.Join(baselineDir, filepath.FromSlash(change.Path))
		workPath := filepath.Join(worktreeDir, filepath.FromSlash(change.Path))
		mainInfo, mainExists, err := fileSnapshot(mainPath)
		if err != nil {
			return nil, err
		}
		baseInfo, baseExists, err := fileSnapshot(basePath)
		if err != nil {
			return nil, err
		}
		workInfo, workExists, err := fileSnapshot(workPath)
		if err != nil {
			return nil, err
		}
		if mainExists && workExists && mainInfo.hash == workInfo.hash && mainInfo.mode == workInfo.mode {
			continue
		}
		if baseExists != mainExists {
			conflicts = append(conflicts, change.Path)
			continue
		}
		if baseExists && (baseInfo.hash != mainInfo.hash || baseInfo.mode != mainInfo.mode) {
			conflicts = append(conflicts, change.Path)
		}
	}
	return conflicts, nil
}

func applySubagentChanges(workspaceDir, worktreeDir string, changes []subagentFileChange) error {
	for _, change := range changes {
		mainPath := filepath.Join(workspaceDir, filepath.FromSlash(change.Path))
		workPath := filepath.Join(worktreeDir, filepath.FromSlash(change.Path))
		switch change.Status {
		case "deleted":
			if err := os.Remove(mainPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		case "added", "modified":
			info, err := os.Lstat(workPath)
			if err != nil {
				return err
			}
			if err := copyFileOrSymlink(workPath, mainPath, info); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildSubagentDiffPreview(baselineDir, worktreeDir string, changes []subagentFileChange, limit int) (string, bool) {
	if limit <= 0 {
		limit = subagentDiffPreviewLimit
	}
	var builder strings.Builder
	truncated := false
	for _, change := range changes {
		if builder.Len() >= limit {
			truncated = true
			break
		}
		chunk := subagentDiffForChange(baselineDir, worktreeDir, change)
		if chunk == "" {
			continue
		}
		if builder.Len()+len(chunk) > limit {
			chunk = chunk[:limit-builder.Len()]
			truncated = true
		}
		builder.WriteString(chunk)
		if !strings.HasSuffix(chunk, "\n") {
			builder.WriteString("\n")
		}
	}
	return builder.String(), truncated
}

func subagentDiffForChange(baselineDir, worktreeDir string, change subagentFileChange) string {
	header := fmt.Sprintf("### %s (%s)\n", change.Path, change.Status)
	if change.Binary {
		return header + "[binary file omitted]\n"
	}
	basePath := filepath.Join(baselineDir, filepath.FromSlash(change.Path))
	workPath := filepath.Join(worktreeDir, filepath.FromSlash(change.Path))
	baseArg, cleanupBase, err := diffPathOrEmpty(basePath)
	if err != nil {
		return header + fmt.Sprintf("[diff unavailable: %v]\n", err)
	}
	defer cleanupBase()
	workArg, cleanupWork, err := diffPathOrEmpty(workPath)
	if err != nil {
		return header + fmt.Sprintf("[diff unavailable: %v]\n", err)
	}
	defer cleanupWork()
	cmd := exec.Command("diff", "-u", "--label", "a/"+change.Path, "--label", "b/"+change.Path, baseArg, workArg)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() > 1 {
			return header + fmt.Sprintf("[diff unavailable: %v]\n", err)
		}
	}
	if len(output) == 0 {
		return header
	}
	return header + string(output)
}

func diffPathOrEmpty(path string) (string, func(), error) {
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path, func() {}, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", func() {}, err
	}
	file, err := os.CreateTemp("", "godex-subagent-empty-*")
	if err != nil {
		return "", func() {}, err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", func() {}, err
	}
	return name, func() { _ = os.Remove(name) }, nil
}

func safeJoinUnderRoot(root, rel string) (string, error) {
	root = filepath.Clean(root)
	rel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rel)), "/")
	if rel == "" || rel == "." {
		return root, nil
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path %q is outside workspace", rel)
	}
	path := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if path != root {
		parent := root + string(os.PathSeparator)
		if !strings.HasPrefix(path, parent) {
			return "", fmt.Errorf("path %q is outside workspace", rel)
		}
	}
	return path, nil
}

func compactNonEmptyStrings(items ...string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
