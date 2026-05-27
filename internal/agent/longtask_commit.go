package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
	"github.com/tim5wang/godex/internal/platform/tooling"
)

func longTaskCommitBlocksStory(artifact longTaskCommitArtifact) bool {
	switch artifact.MergeStatus {
	case longTaskMergeSkippedNoJob, longTaskMergeSkippedNoScope, subagentMergeMerged, subagentMergeNoChanges:
	default:
		return true
	}
	return artifact.CommitStatus == longTaskCommitFailed
}

func longTaskCommitMessage(spec longTaskSpec, node workflowNode) string {
	storyID := node.ID
	if idx := strings.Index(storyID, "_repair_"); idx > 0 {
		storyID = storyID[:idx]
	}
	title := strings.TrimSpace(node.Title)
	if title == "" {
		title = storyID
	}
	return fmt.Sprintf("longtask(%s): complete %s %s", firstNonEmpty(spec.ID, spec.WorkflowID), storyID, title)
}

func longTaskGitRepo(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func longTaskGitCommit(ctx context.Context, dir string, changes []subagentFileChange, message string) (string, error) {
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		path := strings.TrimSpace(change.Path)
		if path != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return "", nil
	}
	addArgs := append([]string{"add", "--"}, paths...)
	if out, err := longTaskRunGit(ctx, dir, addArgs...); err != nil {
		return "", fmt.Errorf("git add: %w: %s", err, out)
	}
	if out, err := longTaskRunGit(ctx, dir, "commit", "-m", message); err != nil {
		return "", fmt.Errorf("git commit: %w: %s", err, out)
	}
	hash, err := longTaskRunGit(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w: %s", err, hash)
	}
	return strings.TrimSpace(hash), nil
}

func longTaskRunGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (a *Agent) runLongTaskValidation(ctx context.Context, spec longTaskSpec, node workflowNode) (longTaskValidation, error) {
	attempt := node.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	validation := longTaskValidation{
		WorkflowID: spec.WorkflowID,
		NodeID:     node.ID,
		Attempt:    attempt,
		Status:     longTaskValidationSkipped,
		CreatedAt:  time.Now().UTC(),
	}
	checks := normalizeWorkflowStrings(spec.QualityChecks)
	if len(checks) == 0 {
		return validation, nil
	}
	validation.Status = longTaskValidationPass
	timeoutMS := spec.ValidationTimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = longTaskDefaultValidationTimeoutMS
	}
	workspaceDir := a.longTaskValidationWorkspace(node)
	executor := tooling.NewWorkspaceExecutorWithTempDirAndExecution(workspaceDir, a.cfg.TempDir, executionConfigFromRuntime(a.cfg.Tools.Execution))
	for _, command := range checks {
		checkCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
		started := time.Now().UTC()
		output, err := executor.RunShellBudgeted(checkCtx, command)
		finished := time.Now().UTC()
		cancel()
		check := longTaskValidationCheck{
			Command:       command,
			Status:        longTaskValidationPass,
			OutputPreview: strings.TrimSpace(output.ModelText()),
			DurationMS:    finished.Sub(started).Milliseconds(),
			StartedAt:     started,
			FinishedAt:    finished,
		}
		if err != nil {
			check.Status = longTaskValidationFail
			check.Error = err.Error()
			validation.Status = longTaskValidationFail
		}
		validation.Checks = append(validation.Checks, check)
		if err != nil {
			break
		}
	}
	return validation, nil
}

func (a *Agent) longTaskValidationWorkspace(node workflowNode) string {
	if strings.TrimSpace(node.JobID) != "" {
		if job, err := a.subagentJobs.Get(node.JobID); err == nil {
			if dir := strings.TrimSpace(job.WorktreeDir); dir != "" {
				if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
					return dir
				}
			}
		}
	}
	return a.cfg.WorkspaceDir
}

func longTaskValidationRef(nodeID string, attempt int) string {
	if attempt <= 0 {
		attempt = 1
	}
	return filepath.ToSlash(filepath.Join(longTaskValidationsDir, strings.TrimSpace(nodeID), fmt.Sprintf("%d.json", attempt)))
}

func longTaskCommitRef(nodeID string, attempt int) string {
	if attempt <= 0 {
		attempt = 1
	}
	return filepath.ToSlash(filepath.Join(longTaskCommitsDir, strings.TrimSpace(nodeID), fmt.Sprintf("%d.json", attempt)))
}

func (s *workflowStore) writeLongTaskValidation(workflowID string, validation longTaskValidation) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return err
	}
	path := filepath.Join(s.dir, workflowID, longTaskValidationRef(validation.NodeID, validation.Attempt))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(path, validation, 0644)
}

func (s *workflowStore) writeLongTaskCommit(workflowID string, artifact longTaskCommitArtifact) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return err
	}
	if artifact.Attempt <= 0 {
		artifact.Attempt = 1
	}
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = time.Now().UTC()
	}
	path := filepath.Join(s.dir, workflowID, longTaskCommitRef(artifact.NodeID, artifact.Attempt))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(path, artifact, 0644)
}

func (s *workflowStore) loadLongTaskValidation(workflowID, nodeID string, attempt int) (longTaskValidation, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return longTaskValidation{}, fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return longTaskValidation{}, err
	}
	var validation longTaskValidation
	if err := readJSONFile(filepath.Join(s.dir, workflowID, longTaskValidationRef(nodeID, attempt)), &validation); err != nil {
		return longTaskValidation{}, err
	}
	return validation, nil
}

func (s *workflowStore) loadLongTaskCommit(workflowID, nodeID string, attempt int) (longTaskCommitArtifact, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return longTaskCommitArtifact{}, fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return longTaskCommitArtifact{}, err
	}
	var artifact longTaskCommitArtifact
	if err := readJSONFile(filepath.Join(s.dir, workflowID, longTaskCommitRef(nodeID, attempt)), &artifact); err != nil {
		return longTaskCommitArtifact{}, err
	}
	return artifact, nil
}
