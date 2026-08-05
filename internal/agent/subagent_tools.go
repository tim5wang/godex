package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/tools"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func (a *Agent) executeSubagentToolForJob(ctx context.Context, name string, input map[string]interface{}, job *subagentJob) (conversation.ToolExecutionResult, error) {
	if job == nil {
		return conversation.ToolExecutionResult{}, fmt.Errorf("missing subagent job")
	}
	if strings.TrimSpace(job.SandboxID) != "" {
		ctx = tools.WithSandboxID(ctx, job.SandboxID)
	}
	if !subagentJobAllowsTool(job.ToolNames, name) {
		return conversation.ToolExecutionResult{}, fmt.Errorf("capability denied: subagent %s is not allowed to call tool:%s", job.ID, strings.TrimSpace(name))
	}
	if err := enforceSubagentWriteScope(name, input, job.WriteScope); err != nil {
		return conversation.ToolExecutionResult{}, err
	}
	checkedInput, err := a.authorizeSubagentTool(ctx, name, input, job)
	if err != nil {
		return conversation.ToolExecutionResult{}, err
	}
	input = checkedInput
	if job.Isolation == subagentIsolationSharedReadOnly && name == "bash" {
		command, _ := input["command"].(string)
		if !sharedReadOnlyShellCommand(command) {
			return conversation.ToolExecutionResult{}, fmt.Errorf("shared read-only subagent cannot run shell command %q; use read_file or request an isolated write-capable subagent", strings.TrimSpace(command))
		}
	}
	if isDurableSubagentInheritedParentTool(name) {
		return a.handleToolResult(ctx, name, input)
	}
	workspace := strings.TrimSpace(job.WorktreeDir)
	if workspace == "" {
		return a.executeSubagentToolWithScope(ctx, name, input, job.WriteScope)
	}
	tempDir := filepath.Join(workspace, ".godex", ".tmp")
	if a.cfg != nil && strings.TrimSpace(a.cfg.TempDir) != "" {
		tempDir = filepath.Join(a.cfg.TempDir, "subagents", job.ID)
	}
	var execution tooling.ExecutionConfig
	if a.cfg != nil {
		execution = executionConfigFromRuntime(a.cfg.Tools.Execution)
	}
	execution = executionConfigForSubagentRole(execution, job.ToolPolicy)
	return executeSubagentToolWithHandlers(ctx, name, input, workspaceSubagentToolHandlers(workspace, tempDir, execution))
}

func sharedReadOnlyShellCommand(command string) bool {
	segments, err := tooling.DisallowedShellCommands(command)
	if err != nil || len(segments) > 0 {
		return false
	}
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, ">|;&`$") {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "pwd", "ls", "find", "rg", "grep", "sed", "cat", "head", "tail", "wc":
		return true
	case "git":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "status", "diff", "log", "show", "grep", "ls-files":
			return true
		}
	}
	return false
}

const subagentPermissionSenderPrefix = "subagent:"

func (a *Agent) authorizeSubagentTool(ctx context.Context, name string, input map[string]interface{}, job *subagentJob) (map[string]interface{}, error) {
	normalized := cloneStringAnyMap(input)
	if strings.TrimSpace(name) != "bash" {
		return normalized, nil
	}
	if a == nil || a.permissions == nil || job == nil {
		return normalized, nil
	}
	runtimeCtx := tools.SessionContextFromContext(ctx)
	if strings.TrimSpace(runtimeCtx.SessionID) == "" {
		runtimeCtx.SessionID = firstNonEmpty(job.RuntimeContext.SessionID, job.SessionID)
	}
	if strings.TrimSpace(runtimeCtx.Source) == "" {
		runtimeCtx.Source = job.RuntimeContext.Source
	}
	if strings.TrimSpace(runtimeCtx.Source) == "" && strings.HasPrefix(strings.TrimSpace(runtimeCtx.SessionID), "web-") {
		runtimeCtx.Source = string(message.SourceWeb)
	}
	runtimeCtx.Sender = subagentPermissionSenderPrefix + strings.TrimSpace(job.ID)
	call := tools.ToolCall{
		Name:            strings.TrimSpace(name),
		RawInput:        cloneStringAnyMap(input),
		NormalizedInput: normalized,
		SessionContext:  runtimeCtx.Clone(),
	}
	_, err := tools.NewPermissionInterceptorWithReview(a.permissions, a.reviewPermissionRequest)(ctx, &call)
	if err != nil {
		return normalized, err
	}
	return cloneStringAnyMap(call.NormalizedInput), nil
}

func subagentJobAllowsTool(toolNames []string, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, allowed := range toolNames {
		if strings.EqualFold(strings.TrimSpace(allowed), name) {
			return true
		}
	}
	return false
}

func (a *Agent) executeSubagentToolWithScope(ctx context.Context, name string, input map[string]interface{}, writeScope []string) (conversation.ToolExecutionResult, error) {
	if err := enforceSubagentWriteScope(name, input, writeScope); err != nil {
		return conversation.ToolExecutionResult{}, err
	}
	return a.executeSubagentTool(ctx, name, input)
}

type subagentToolHandlers struct {
	runBash   func(context.Context, string, bool) (conversation.ToolExecutionResult, error)
	readFile  func(context.Context, string, int, int, int, int) (conversation.ToolExecutionResult, error)
	writeFile func(context.Context, string, string) (conversation.ToolExecutionResult, error)
	editFile  func(context.Context, string, string, string) (conversation.ToolExecutionResult, error)
}

func executeSubagentToolWithHandlers(ctx context.Context, name string, input map[string]interface{}, handlers subagentToolHandlers) (conversation.ToolExecutionResult, error) {
	switch name {
	case "bash":
		cmd, _ := input["command"].(string)
		allowUnlisted, _ := input["_allow_unlisted_commands"].(bool)
		return handlers.runBash(ctx, cmd, allowUnlisted)
	case "read_file":
		path, _ := input["path"].(string)
		return handlers.readFile(ctx, path, subagentToolLimit(input["limit"]), subagentToolLimit(input["offset"]), subagentToolLimit(input["start_line"]), subagentToolLimit(input["max_lines"]))
	case "write_file":
		path, _ := input["path"].(string)
		content, _ := input["content"].(string)
		return handlers.writeFile(ctx, path, content)
	case "edit_file":
		path, _ := input["path"].(string)
		oldText, _ := input["old_text"].(string)
		newText, _ := input["new_text"].(string)
		return handlers.editFile(ctx, path, oldText, newText)
	default:
		return conversation.ToolExecutionResult{}, fmt.Errorf("unknown tool: %s", name)
	}
}

func workspaceSubagentToolHandlers(workspace, tempDir string, execution tooling.ExecutionConfig) subagentToolHandlers {
	executor := tooling.NewWorkspaceExecutorWithTempDirAndExecution(workspace, tempDir, execution)
	return subagentToolHandlers{
		runBash: func(ctx context.Context, cmd string, allowUnlisted bool) (conversation.ToolExecutionResult, error) {
			options := tools.ShellCommandOptionsForContext(tools.SessionContextFromContext(ctx), tooling.ShellCommandOptions{
				AllowUnlistedCommands: allowUnlisted,
			})
			output, err := executor.RunShellBudgetedWithOptions(ctx, cmd, options)
			return conversation.ToolExecutionResult{
				Output:        output.ModelText(),
				ArtifactPaths: compactNonEmptyStrings(output.FilePath),
			}, err
		},
		readFile: func(_ context.Context, path string, limit, offset, startLine, maxLines int) (conversation.ToolExecutionResult, error) {
			output, err := executor.ReadFileRange(path, limit, offset, startLine, maxLines)
			return conversation.ToolExecutionResult{Output: output}, err
		},
		writeFile: func(_ context.Context, path, content string) (conversation.ToolExecutionResult, error) {
			output, err := executor.WriteFile(path, content)
			return conversation.ToolExecutionResult{Output: output}, err
		},
		editFile: func(_ context.Context, path, oldText, newText string) (conversation.ToolExecutionResult, error) {
			output, err := executor.EditFile(path, oldText, newText)
			return conversation.ToolExecutionResult{Output: output}, err
		},
	}
}

func executionConfigForSubagentRole(base tooling.ExecutionConfig, toolPolicy []string) tooling.ExecutionConfig {
	for _, item := range toolPolicy {
		key, value, ok := splitToolPolicy(item)
		if !ok {
			continue
		}
		switch key {
		case "shell:allow":
			base.ShellAllowPatterns = append(base.ShellAllowPatterns, value)
		case "shell:deny":
			base.ShellDenyPatterns = append(base.ShellDenyPatterns, value)
		}
	}
	return base
}

func splitToolPolicy(item string) (string, string, bool) {
	item = strings.TrimSpace(item)
	if item == "" {
		return "", "", false
	}
	for _, prefix := range []string{"shell:allow:", "shell:deny:"} {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimSuffix(prefix, ":"), strings.TrimSpace(strings.TrimPrefix(item, prefix)), strings.TrimSpace(strings.TrimPrefix(item, prefix)) != ""
		}
	}
	return "", "", false
}

func subagentToolLimit(value interface{}) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	}
	return 0
}

func enforceSubagentWriteScope(name string, input map[string]interface{}, writeScope []string) error {
	if (name != "write_file" && name != "edit_file") || len(writeScope) == 0 {
		return nil
	}
	path, _ := input["path"].(string)
	if !pathAllowedByWriteScope(path, writeScope) {
		return fmt.Errorf("path %q is outside subagent write scope", path)
	}
	return nil
}

func copyWorkspaceSnapshot(src, dst string) error {
	if strings.TrimSpace(src) == "" || strings.TrimSpace(dst) == "" {
		return fmt.Errorf("missing source or destination")
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return copyDirFiltered(src, dst, true)
}

func copyScopeSnapshot(src, dst string, scope []string) error {
	if strings.TrimSpace(src) == "" || strings.TrimSpace(dst) == "" {
		return fmt.Errorf("missing source or destination")
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for _, rel := range normalizeWriteScope(scope) {
		srcPath, err := safeJoinUnderRoot(src, rel)
		if err != nil {
			return err
		}
		dstPath, err := safeJoinUnderRoot(dst, rel)
		if err != nil {
			return err
		}
		info, err := os.Lstat(srcPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := copyDirFiltered(srcPath, dstPath, true); err != nil {
				return err
			}
			continue
		}
		if err := copyFileOrSymlink(srcPath, dstPath, info); err != nil {
			return err
		}
	}
	return nil
}

func copyDirFiltered(src, dst string, skipGenerated bool) error {
	src = filepath.Clean(src)
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0755)
		}
		if entry.IsDir() && skipGenerated && shouldSkipSubagentSnapshotDir(entry.Name()) {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFileOrSymlink(path, target, info)
	})
}

func copyFileOrSymlink(src, dst string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		_ = os.Remove(dst)
		return os.Symlink(target, dst)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func shouldSkipSubagentSnapshotDir(name string) bool {
	switch strings.TrimSpace(name) {
	case ".git", ".godex", "node_modules", ".pnpm-store", ".next", ".nuxt", ".turbo", ".cache", "coverage", "dist", "build":
		return true
	default:
		return false
	}
}
