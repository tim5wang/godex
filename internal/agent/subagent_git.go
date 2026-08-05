package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func cleanGitRepository(workspace string) (string, bool) {
	repoRoot, err := gitOutput(workspace, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(repoRoot) == "" {
		return "", false
	}
	status, err := gitOutput(repoRoot, "status", "--porcelain")
	if err != nil || strings.TrimSpace(status) != "" {
		return strings.TrimSpace(repoRoot), false
	}
	return strings.TrimSpace(repoRoot), true
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func gitOutputBytes(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func subagentGitBranchName(jobID string) string {
	base := strings.ToLower(strings.TrimSpace(jobID))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	base = strings.Trim(b.String(), "-.")
	if base == "" {
		base = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if len(base) > 48 {
		base = base[len(base)-48:]
	}
	return "godex-subagent-" + base
}

func createSubagentGitWorktree(repoRoot, worktreeDir, branch string) error {
	if err := os.RemoveAll(worktreeDir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(worktreeDir), 0755); err != nil {
		return err
	}
	cmd := exec.Command("git", "worktree", "add", "-b", branch, worktreeDir, "HEAD")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func applyGitDirtyOverlay(repoRoot, worktreeDir string) error {
	diff, err := gitOutputBytes(repoRoot, "diff", "--binary", "HEAD", "--", ".")
	if err != nil {
		return fmt.Errorf("git diff dirty overlay: %w: %s", err, strings.TrimSpace(string(diff)))
	}
	if len(strings.TrimSpace(string(diff))) > 0 {
		cmd := exec.Command("git", "apply", "--binary", "--whitespace=nowarn")
		cmd.Dir = worktreeDir
		cmd.Stdin = strings.NewReader(string(diff))
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git apply dirty overlay: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return copyGitDirtyOverlayUntracked(repoRoot, worktreeDir)
}

const subagentDirtyOverlayMaxUntrackedBytes int64 = 2 * 1024 * 1024

func copyGitDirtyOverlayUntracked(repoRoot, worktreeDir string) error {
	out, err := gitOutputBytes(repoRoot, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("git ls-files untracked: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, raw := range strings.Split(string(out), "\x00") {
		rel := filepath.Clean(strings.TrimSpace(raw))
		if rel == "" || rel == "." || shouldSkipDirtyOverlayPath(rel) {
			continue
		}
		src, err := safeJoinUnderRoot(repoRoot, rel)
		if err != nil {
			return err
		}
		dst, err := safeJoinUnderRoot(worktreeDir, rel)
		if err != nil {
			return err
		}
		info, err := os.Lstat(src)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			continue
		}
		if info.Mode().IsRegular() && info.Size() > subagentDirtyOverlayMaxUntrackedBytes {
			continue
		}
		if err := copyFileOrSymlink(src, dst, info); err != nil {
			return err
		}
	}
	return nil
}

func shouldSkipDirtyOverlayPath(rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	if rel == "." || rel == "" {
		return true
	}
	base := pathBase(rel)
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	for _, part := range strings.Split(rel, "/") {
		switch part {
		case ".git", ".godex", "node_modules", ".pnpm-store", ".next", ".nuxt", ".turbo", ".cache", "coverage", "dist", "build", "tmp", "temp":
			return true
		}
	}
	return false
}

func pathBase(rel string) string {
	rel = strings.TrimSuffix(filepath.ToSlash(rel), "/")
	idx := strings.LastIndex(rel, "/")
	if idx >= 0 {
		return rel[idx+1:]
	}
	return rel
}

func (a *Agent) applyPreviewJobsToSubagentWorkspace(job *subagentJob, worktreeDir, baselineDir string) error {
	if job == nil || len(job.PreviewJobIDs) == 0 {
		return nil
	}
	for _, depJobID := range normalizeWorkflowStrings(job.PreviewJobIDs) {
		depJob, err := a.subagentJobs.Get(depJobID)
		if err != nil {
			return fmt.Errorf("preview merge dependency %s: %w", depJobID, err)
		}
		if depJob.Status != subagentStatusCompleted {
			return fmt.Errorf("preview merge dependency %s is %s", depJobID, depJob.Status)
		}
		review, err := reviewSubagentJob(depJob)
		if err != nil {
			return fmt.Errorf("preview merge dependency %s: %w", depJobID, err)
		}
		if len(review.Changes) == 0 {
			continue
		}
		if err := applySubagentChanges(worktreeDir, depJob.WorktreeDir, review.Changes); err != nil {
			return fmt.Errorf("preview merge dependency %s into worktree: %w", depJobID, err)
		}
		if len(job.WriteScope) > 0 {
			if err := applySubagentChanges(baselineDir, depJob.WorktreeDir, review.Changes); err != nil {
				return fmt.Errorf("preview merge dependency %s into baseline: %w", depJobID, err)
			}
		}
	}
	return nil
}

func (a *Agent) ensureSubagentWorkspace(job *subagentJob) error {
	if job == nil {
		return fmt.Errorf("missing subagent job")
	}
	if strings.TrimSpace(job.WorktreeDir) == "" {
		prepared, err := a.prepareSubagentWorkspace(job)
		if err != nil {
			return err
		}
		*job = *prepared
		return nil
	}
	info, err := os.Stat(job.WorktreeDir)
	if err != nil {
		return fmt.Errorf("subagent worktree is unavailable: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("subagent worktree is not a directory: %s", job.WorktreeDir)
	}
	if len(job.WriteScope) > 0 {
		if info, err := os.Stat(job.BaselineDir); err != nil || !info.IsDir() {
			return fmt.Errorf("subagent baseline is unavailable for merge review: %s", job.BaselineDir)
		}
	}
	return nil
}
