package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/sandbox"
	"github.com/tim5wang/godex/internal/tools"
)

func TestRegisteredWorkspaceToolsUseAgentSandboxBinding(t *testing.T) {
	a := newTestAgent(t, 4096)
	cfgWorkspace := a.cfg.WorkspaceDir
	sandboxWorkspace := t.TempDir()

	if err := os.WriteFile(filepath.Join(cfgWorkspace, "marker.txt"), []byte("from config"), 0644); err != nil {
		t.Fatalf("write config marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxWorkspace, "marker.txt"), []byte("from sandbox"), 0644); err != nil {
		t.Fatalf("write sandbox marker: %v", err)
	}

	a.sandbox = sandbox.NewLocal(sandbox.LocalOptions{
		WorkspaceDir: sandboxWorkspace,
		TempDir:      filepath.Join(sandboxWorkspace, ".godex", ".tmp"),
		Execution:    executionConfigFromRuntime(a.cfg.Tools.Execution),
	})
	a.toolHandler = tools.NewToolHandler()
	a.RegisterTools()

	output, err := a.handleTool(context.Background(), "read_file", map[string]interface{}{"path": "marker.txt"})
	if err != nil {
		t.Fatalf("read sandbox marker: %v", err)
	}
	if !strings.Contains(output, "from sandbox") {
		t.Fatalf("expected read_file to use sandbox workspace, got %q", output)
	}
	if strings.Contains(output, "from config") {
		t.Fatalf("read_file used config workspace instead of sandbox workspace: %q", output)
	}
}

func TestRegisteredFileToolsCanReadOnlyAccessCurrentSessionAttachments(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.sessionID = "web-current"
	a.cfg.SessionsDir = filepath.Join(t.TempDir(), "sessions")
	attachmentDir := filepath.Join(a.cfg.SessionsDir, a.sessionID, "attachments")
	if err := os.MkdirAll(attachmentDir, 0755); err != nil {
		t.Fatalf("mkdir attachment dir: %v", err)
	}
	attachmentPath := filepath.Join(attachmentDir, "upload.tsv")
	if err := os.WriteFile(attachmentPath, []byte("url\tsegments\nexample.aac\t[]\n"), 0644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	otherDir := filepath.Join(a.cfg.SessionsDir, "web-other", "attachments")
	if err := os.MkdirAll(otherDir, 0755); err != nil {
		t.Fatalf("mkdir other attachment dir: %v", err)
	}
	otherPath := filepath.Join(otherDir, "secret.txt")
	if err := os.WriteFile(otherPath, []byte("secret"), 0644); err != nil {
		t.Fatalf("write other attachment: %v", err)
	}

	a.toolHandler = tools.NewToolHandler()
	a.RegisterTools()
	output, err := a.handleTool(context.Background(), "read_file", map[string]interface{}{"path": attachmentPath})
	if err != nil || !strings.Contains(output, "example.aac") {
		t.Fatalf("read current session attachment: output=%q err=%v", output, err)
	}
	if _, err := a.handleTool(context.Background(), "attach_file", map[string]interface{}{"path": attachmentPath}); err != nil {
		t.Fatalf("attach current session attachment: %v", err)
	}
	if _, err := a.handleTool(context.Background(), "read_file", map[string]interface{}{"path": otherPath}); err == nil {
		t.Fatal("expected another session's attachment to remain outside the read allowlist")
	}
}

func TestToolExecutionContextIncludesSandboxID(t *testing.T) {
	a := newTestAgent(t, 4096)
	var captured string
	a.toolHandler = tools.NewToolHandler()
	a.toolHandler.RegisterWithMeta(
		tools.NewTypedTool[struct{}](tools.NewToolSpec("capture_sandbox", "capture sandbox", map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}, nil), func(ctx context.Context, _ struct{}) (tools.ToolResult, error) {
			captured = tools.SandboxIDFromContext(ctx)
			return tools.ToolResult{Text: "ok"}, nil
		}),
		tools.ToolMeta{AlwaysActive: true},
	)

	if _, err := a.handleToolResult(context.Background(), "capture_sandbox", map[string]interface{}{}); err != nil {
		t.Fatalf("handle tool: %v", err)
	}
	if captured != a.SandboxID() {
		t.Fatalf("captured sandbox id %q, want %q", captured, a.SandboxID())
	}
}

func TestSubagentInheritedParentToolKeepsWorkerSandboxID(t *testing.T) {
	a := newTestAgent(t, 4096)
	var captured string
	a.toolHandler = tools.NewToolHandler()
	a.toolHandler.RegisterWithMeta(
		tools.NewTypedTool[struct{}](tools.NewToolSpec("web_fetch", "capture inherited parent tool sandbox", map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}, nil), func(ctx context.Context, _ struct{}) (tools.ToolResult, error) {
			captured = tools.SandboxIDFromContext(ctx)
			return tools.ToolResult{Text: "ok"}, nil
		}),
		tools.ToolMeta{AlwaysActive: true},
	)
	job := &subagentJob{
		ID:        "job-worker-sandbox",
		ToolNames: []string{"web_fetch"},
		SandboxID: "sandbox:local:worker",
	}

	if _, err := a.executeSubagentToolForJob(context.Background(), "web_fetch", map[string]interface{}{}, job); err != nil {
		t.Fatalf("execute inherited parent tool: %v", err)
	}
	if captured != job.SandboxID {
		t.Fatalf("captured sandbox id %q, want %q", captured, job.SandboxID)
	}
}
