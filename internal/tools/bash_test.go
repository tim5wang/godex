package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/platform/tooling"
)

func TestBashToolSpillsLargeOutput(t *testing.T) {
	workspace := t.TempDir()
	tempDir := filepath.Join(workspace, ".godex", ".tmp")
	tool := NewBashTool(workspace, tempDir)
	handler := NewToolHandler()
	handler.Register(tool)

	result, err := handler.HandleResult(context.Background(), tool.Name(), map[string]interface{}{
		"command": `printf '%70000s\n' x`,
	})
	if err != nil {
		t.Fatalf("bash tool: %v", err)
	}
	if len(result.ArtifactPaths) != 1 {
		t.Fatalf("expected spill artifact path, got %+v", result.ArtifactPaths)
	}
	if _, err := os.Stat(result.ArtifactPaths[0]); err != nil {
		t.Fatalf("expected spill file to exist: %v", err)
	}
	output, err := result.OutputString()
	if err != nil {
		t.Fatalf("serialize output: %v", err)
	}
	if !strings.Contains(output, "captured output saved") {
		t.Fatalf("expected truncated model output to mention spill file, got %q", output)
	}
}

func TestBashToolReturnsExecutionBackendError(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBashToolWithExecution(workspace, "", tooling.ExecutionConfig{Mode: tooling.ExecutionModeSSH})
	handler := NewToolHandler()
	handler.Register(tool)

	_, err := handler.HandleResult(context.Background(), tool.Name(), map[string]interface{}{
		"command": `pwd`,
	})
	if err == nil || !strings.Contains(err.Error(), "ssh_target") {
		t.Fatalf("expected ssh config error, got %v", err)
	}
}

func TestBashToolAllowsDevRepairDiagnosticCommands(t *testing.T) {
	workspace := t.TempDir()
	tool := NewBashTool(workspace)
	handler := NewToolHandler()
	handler.Register(tool)

	_, err := handler.HandleResult(context.Background(), tool.Name(), map[string]interface{}{
		"command": `command -v sh`,
	})
	if err == nil || !strings.Contains(err.Error(), "command not allowed: command") {
		t.Fatalf("expected command to be rejected without dev/repair profile, got %v", err)
	}

	ctx := WithSessionContext(context.Background(), automation.SessionContext{SecurityProfile: SecurityProfileDevRepair})
	result, err := handler.HandleResult(ctx, tool.Name(), map[string]interface{}{
		"command": `command -v sh`,
	})
	if err != nil {
		t.Fatalf("expected command to run under dev/repair profile: %v", err)
	}
	output, err := result.OutputString()
	if err != nil {
		t.Fatalf("serialize output: %v", err)
	}
	if strings.TrimSpace(output) == "" {
		t.Fatalf("expected non-empty output")
	}
}

func TestBashToolHonorsTimeoutSeconds(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	ctx := WithSessionContext(context.Background(), automation.SessionContext{SecurityProfile: SecurityProfileDevRepair})
	_, err := tool.Execute(ctx, map[string]interface{}{
		"command":                  "sleep 5",
		"timeout_seconds":          1,
		"_allow_unlisted_commands": true,
	})
	if err == nil {
		t.Fatal("expected bash timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("expected deadline or killed process, got %v", err)
	}
}
