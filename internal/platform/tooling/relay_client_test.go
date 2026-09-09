package tooling

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRelayClientExecForwardsToNode verifies the A-side RelayClient posts the
// command to the center's node-proxy path with the restricted nk_ credential
// and decodes the bounded output result.
func TestRelayClientExecForwardsToNode(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody ExecRequest
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(CommandOutputResult{
			Text:     "hello from B",
			ExitCode: 0,
			Bytes:    13,
		})
	}))
	defer center.Close()

	client := &RelayClient{CenterURL: center.URL, NodeID: "pod-b", Token: "nk_secret"}
	out, err := client.Exec(context.Background(), ExecRequest{Command: "echo hello"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if gotPath != "/api/control/nodes/pod-b/proxy/control/sandbox/exec" {
		t.Fatalf("path = %q, want node-proxy sandbox exec path", gotPath)
	}
	if gotAuth != "Bearer nk_secret" {
		t.Fatalf("auth = %q, want Bearer nk_secret", gotAuth)
	}
	if gotBody.Command != "echo hello" {
		t.Fatalf("body command = %q", gotBody.Command)
	}
	if out.Text != "hello from B" {
		t.Fatalf("decoded text = %q", out.Text)
	}
}

// TestRelayClientExecPropagatesRemoteError verifies non-200 responses surface
// the remote error message.
func TestRelayClientExecPropagatesRemoteError(t *testing.T) {
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"node offline"}`, http.StatusBadGateway)
	}))
	defer center.Close()

	client := &RelayClient{CenterURL: center.URL, NodeID: "pod-b", Token: "nk_secret"}
	_, err := client.Exec(context.Background(), ExecRequest{Command: "echo x"})
	if err == nil || !strings.Contains(err.Error(), "node offline") {
		t.Fatalf("expected node offline error, got %v", err)
	}
}

// TestRelayClientFSRoundTrip verifies the FS request/response encoding.
func TestRelayClientFSRoundTrip(t *testing.T) {
	var got FSRequest
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(FSResult{Removed: true})
	}))
	defer center.Close()

	client := &RelayClient{CenterURL: center.URL, NodeID: "pod-b", Token: "nk_secret"}
	res, err := client.FS(context.Background(), FSRequest{Op: "rm", Path: "tmp/x"})
	if err != nil {
		t.Fatalf("fs: %v", err)
	}
	if !res.Removed {
		t.Fatal("expected removed=true")
	}
	if got.Op != "rm" || got.Path != "tmp/x" {
		t.Fatalf("fs request = %+v", got)
	}
}

// TestRunShellBudgetedRelayMode verifies the WorkspaceExecutor dispatches to
// the relay backend when Execution.Mode == relay.
func TestRunShellBudgetedRelayMode(t *testing.T) {
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(CommandOutputResult{
			Text:     "relayed output",
			ExitCode: 0,
			Bytes:    14,
		})
	}))
	defer center.Close()

	executor := NewWorkspaceExecutorWithTempDirAndExecution("/workspace", "/workspace/.godex/tmp", ExecutionConfig{
		Mode:        ExecutionModeRelay,
		RelayCenter: center.URL,
		RelayNode:   "pod-b",
		RelayToken:  "nk_secret",
	})
	out, err := executor.RunShellBudgeted(context.Background(), "echo relayed-output")
	if err != nil {
		t.Fatalf("run shell relay: %v", err)
	}
	if out.Text != "relayed output" {
		t.Fatalf("output = %q, want relayed output", out.Text)
	}
	if backend := executor.ExecutionBackend(); backend != ExecutionModeRelay {
		t.Fatalf("execution backend = %q, want relay", backend)
	}
}
