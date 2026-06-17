package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/background"
	"github.com/tim5wang/godex/internal/platform/tooling"
)

func TestBackgroundToolRunActionExecutesArgvCommandInWorkspace(t *testing.T) {
	workspace := t.TempDir()
	manager := background.NewManager()
	tool := NewBackgroundTool(manager, workspace, "", tooling.ExecutionConfig{})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "run",
		"command": `sh -c 'pwd'`,
		"timeout": float64(1),
	})
	if err != nil {
		t.Fatalf("background run: %v", err)
	}

	var parsed struct {
		TaskID string   `json:"task_id"`
		Argv   []string `json:"argv"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(parsed.Argv) != 3 || parsed.Argv[0] != "sh" {
		t.Fatalf("expected argv payload, got %+v", parsed.Argv)
	}

	task, err := manager.Wait(parsed.TaskID)
	if err != nil {
		t.Fatalf("wait task: %v", err)
	}
	if got := strings.TrimSpace(task.Output); got != workspace {
		t.Fatalf("expected background command to run in %q, got %q", workspace, got)
	}
}

func TestBackgroundToolRunActionIgnoresCanceledRequestContext(t *testing.T) {
	workspace := t.TempDir()
	manager := background.NewManager()
	tool := NewBackgroundTool(manager, workspace, "", tooling.ExecutionConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":  "run",
		"command": `sh -c 'printf ok'`,
	})
	if err != nil {
		t.Fatalf("background run with canceled context: %v", err)
	}

	var parsed struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	task, err := manager.Wait(parsed.TaskID)
	if err != nil {
		t.Fatalf("wait task: %v", err)
	}
	if got := strings.TrimSpace(task.Output); got != "ok" {
		t.Fatalf("expected output %q, got %q", "ok", got)
	}
}

func TestBackgroundToolRunActionExpandsHomeDirectoryArguments(t *testing.T) {
	workspace := t.TempDir()
	manager := background.NewManager()
	tool := NewBackgroundTool(manager, workspace, "", tooling.ExecutionConfig{})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "run",
		"command": `sh -c 'printf "%s" "$1"' sh ~/.openclaw/test-script.sh`,
	})
	if err != nil {
		t.Fatalf("background run with home dir arg: %v", err)
	}

	var parsed struct {
		TaskID string   `json:"task_id"`
		Argv   []string `json:"argv"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("user home dir: %v", err)
	}
	expectedPath := filepath.Join(homeDir, ".openclaw", "test-script.sh")
	if len(parsed.Argv) != 5 || parsed.Argv[4] != expectedPath {
		t.Fatalf("expected expanded argv path %q, got %+v", expectedPath, parsed.Argv)
	}

	task, err := manager.Wait(parsed.TaskID)
	if err != nil {
		t.Fatalf("wait task: %v", err)
	}
	if got := strings.TrimSpace(task.Output); got != expectedPath {
		t.Fatalf("expected output %q, got %q", expectedPath, got)
	}
}

func TestBackgroundToolRunActionReturnsExecutionBackendError(t *testing.T) {
	workspace := t.TempDir()
	manager := background.NewManager()
	tool := NewBackgroundTool(manager, workspace, "", tooling.ExecutionConfig{Mode: tooling.ExecutionModeSSH})

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "run",
		"command": `pwd`,
	})
	if err == nil || !strings.Contains(err.Error(), "ssh_target") {
		t.Fatalf("expected ssh config error, got %v", err)
	}
}

func TestBackgroundToolCheckActionReturnsStringError(t *testing.T) {
	manager := background.NewManager()
	task, err := manager.Start("task-error", exec.Command("sh", "-c", "exit 3"), 0)
	if err != nil {
		t.Fatalf("start failing task: %v", err)
	}
	<-task.Done

	tool := NewBackgroundTool(manager, "", "", tooling.ExecutionConfig{})
	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "check", "task_id": task.ID})
	if err != nil {
		t.Fatalf("check background: %v", err)
	}

	var parsed struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if parsed.Error == "" {
		t.Fatalf("expected string error in check background result, got %s", result)
	}
}

func TestBackgroundToolCheckActionReturnsRerunHintForInterruptedStoredTask(t *testing.T) {
	storeDir := t.TempDir()
	manager := background.NewManagerWithStore(storeDir)
	task, err := manager.StartWithOptions("task-interrupted", exec.Command("sh", "-c", "sleep 2"), 0, background.OutputOptions{
		Command: "sleep 2",
	})
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	defer func() {
		task.Cancel()
		_, _ = manager.Wait(task.ID)
	}()

	restored := background.NewManagerWithStore(storeDir)
	tool := NewBackgroundTool(restored, "", "", tooling.ExecutionConfig{})
	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "check", "task_id": "task-interrupted"})
	if err != nil {
		t.Fatalf("check interrupted background: %v", err)
	}

	var parsed struct {
		Status    string `json:"status"`
		RerunHint string `json:"rerun_hint"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if parsed.Status != string(background.StatusInterrupted) || !strings.Contains(parsed.RerunHint, "sleep 2") {
		t.Fatalf("expected interrupted rerun hint, got %+v", parsed)
	}
}

func TestBackgroundToolCheckActionReturnsSpillMetadata(t *testing.T) {
	workspace := t.TempDir()
	tempDir := filepath.Join(workspace, ".godex", ".tmp")
	manager := background.NewManager()
	tool := NewBackgroundTool(manager, workspace, tempDir, tooling.ExecutionConfig{})

	runResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "run",
		"command": `sh -c 'printf "%070000d" 0'`,
	})
	if err != nil {
		t.Fatalf("background run: %v", err)
	}
	var runParsed struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(runResult), &runParsed); err != nil {
		t.Fatalf("unmarshal run result: %v", err)
	}
	if _, err := manager.Wait(runParsed.TaskID); err != nil {
		t.Fatalf("wait task: %v", err)
	}

	checkResult, err := tool.Execute(context.Background(), map[string]interface{}{"action": "check", "task_id": runParsed.TaskID})
	if err != nil {
		t.Fatalf("check background: %v", err)
	}
	var checked struct {
		Output          string `json:"output"`
		OutputPath      string `json:"output_path"`
		OutputTruncated bool   `json:"output_truncated"`
		OutputBytes     int64  `json:"output_bytes"`
	}
	if err := json.Unmarshal([]byte(checkResult), &checked); err != nil {
		t.Fatalf("unmarshal check result: %v", err)
	}
	if !checked.OutputTruncated || checked.OutputPath == "" || checked.OutputBytes == 0 {
		t.Fatalf("expected spill metadata, got %+v", checked)
	}
	if _, err := os.Stat(checked.OutputPath); err != nil {
		t.Fatalf("expected spill file to exist: %v", err)
	}
	if !strings.Contains(checked.Output, "captured output saved") {
		t.Fatalf("expected output to mention spill file, got %q", checked.Output)
	}
}
