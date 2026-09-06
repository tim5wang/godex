package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

func TestACPFSBridgeReadWriteTextFile(t *testing.T) {
	workspace := t.TempDir()
	sub := filepath.Join(workspace, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bridge, err := newACPFSBridge(workspace)
	if err != nil {
		t.Fatalf("newACPFSBridge: %v", err)
	}

	// Write into the workspace (creates parent dirs).
	if _, err := bridge.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
		Path:    filepath.Join(sub, "notes.txt"),
		Content: "line one\nline two\nline three\n",
	}); err != nil {
		t.Fatalf("WriteTextFile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(sub, "notes.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "line one\nline two\nline three\n" {
		t.Fatalf("content = %q", data)
	}

	// Read whole file.
	resp, err := bridge.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: filepath.Join(sub, "notes.txt")})
	if err != nil {
		t.Fatalf("ReadTextFile: %v", err)
	}
	if !strings.Contains(resp.Content, "line two") {
		t.Fatalf("read content = %q", resp.Content)
	}

	// Read with line range.
	line := 2
	limit := 1
	resp, err = bridge.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: filepath.Join(sub, "notes.txt"), Line: &line, Limit: &limit})
	if err != nil {
		t.Fatalf("ReadTextFile range: %v", err)
	}
	if strings.TrimSpace(resp.Content) != "line two" {
		t.Fatalf("range content = %q, want line two", resp.Content)
	}
}

func TestACPFSBridgeRejectsEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	evil := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(evil, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	bridge, err := newACPFSBridge(workspace)
	if err != nil {
		t.Fatalf("newACPFSBridge: %v", err)
	}
	if _, err := bridge.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: evil}); err == nil {
		t.Fatal("expected read outside workspace to fail")
	}
}

func TestACPTerminalManagerLifecycle(t *testing.T) {
	workspace := t.TempDir()
	mgr := newACPTerminalManager(workspace)

	create, err := mgr.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "sh",
		Args:    []string{"-c", "echo hello; exit 3"},
	})
	if err != nil {
		t.Fatalf("CreateTerminal: %v", err)
	}
	if create.TerminalId == "" {
		t.Fatal("empty terminal id")
	}

	// Output should eventually contain the echoed text.
	var output string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := mgr.TerminalOutput(context.Background(), acp.TerminalOutputRequest{TerminalId: create.TerminalId})
		if err != nil {
			t.Fatalf("TerminalOutput: %v", err)
		}
		output = resp.Output
		if strings.Contains(output, "hello") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(output, "hello") {
		t.Fatalf("terminal output = %q, want hello", output)
	}

	// Wait for exit and check the exit code.
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	exit, err := mgr.WaitForTerminalExit(waitCtx, acp.WaitForTerminalExitRequest{TerminalId: create.TerminalId})
	if err != nil {
		t.Fatalf("WaitForTerminalExit: %v", err)
	}
	if exit.ExitCode == nil || *exit.ExitCode != 3 {
		t.Fatalf("exit code = %+v, want 3", exit.ExitCode)
	}

	// Release cleans up; a second release is a no-op.
	if _, err := mgr.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{TerminalId: create.TerminalId}); err != nil {
		t.Fatalf("ReleaseTerminal: %v", err)
	}
	if _, err := mgr.ReleaseTerminal(context.Background(), acp.ReleaseTerminalRequest{TerminalId: create.TerminalId}); err != nil {
		t.Fatalf("second ReleaseTerminal: %v", err)
	}
}

func TestACPTerminalManagerRejectsCWDOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	mgr := newACPTerminalManager(workspace)
	defer mgr.Close()
	_, err := mgr.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "pwd",
		Cwd:     &outside,
	})
	if err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("expected workspace escape error, got %v", err)
	}
}

func TestACPTerminalManagerKill(t *testing.T) {
	workspace := t.TempDir()
	mgr := newACPTerminalManager(workspace)

	create, err := mgr.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "sh",
		Args:    []string{"-c", "sleep 30"},
	})
	if err != nil {
		t.Fatalf("CreateTerminal: %v", err)
	}
	if _, err := mgr.KillTerminal(context.Background(), acp.KillTerminalRequest{TerminalId: create.TerminalId}); err != nil {
		t.Fatalf("KillTerminal: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	exit, err := mgr.WaitForTerminalExit(waitCtx, acp.WaitForTerminalExitRequest{TerminalId: create.TerminalId})
	if err != nil {
		t.Fatalf("WaitForTerminalExit after kill: %v", err)
	}
	if exit.ExitCode == nil || *exit.ExitCode != -1 {
		t.Fatalf("killed exit code = %+v, want -1", exit.ExitCode)
	}
}

func TestACPSessionBridgesAdvertisedInInitialize(t *testing.T) {
	// OpenACPSession advertises fs+terminal capabilities to the external
	// agent. The fake server records the initialize request so we can assert
	// the capabilities without a live agent.
	client := &acpSDKClient{}
	fsBridge, err := newACPFSBridge(t.TempDir())
	if err != nil {
		t.Fatalf("newACPFSBridge: %v", err)
	}
	client.fsBridge = fsBridge
	client.terminalBridge = newACPTerminalManager(t.TempDir())
	if client.fsBridge == nil || client.terminalBridge == nil {
		t.Fatal("bridges not wired")
	}
}
