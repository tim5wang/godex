package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExecURL(t *testing.T) {
	cases := []struct {
		center string
		nodeID string
		want   string
	}{
		{"https://godex.claw.carc.top", "node_a", "https://godex.claw.carc.top/api/control/nodes/node_a/proxy/v1/exec"},
		{"http://127.0.0.1:3921", "n1", "http://127.0.0.1:3921/api/control/nodes/n1/proxy/v1/exec"},
		{"http://127.0.0.1:3921/", "n1", "http://127.0.0.1:3921/api/control/nodes/n1/proxy/v1/exec"},
	}
	for _, tc := range cases {
		got, err := execURL(tc.center, tc.nodeID)
		if err != nil {
			t.Fatalf("execURL(%q): %v", tc.center, err)
		}
		if got != tc.want {
			t.Fatalf("execURL(%q) = %q, want %q", tc.center, got, tc.want)
		}
	}
	if _, err := execURL("", "n1"); err == nil {
		t.Fatal("expected error for empty center")
	}
	if _, err := execURL("ftp://x", "n1"); err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}

func TestRunNodeExecValidatesFlags(t *testing.T) {
	r := &Runner{Stderr: &strings.Builder{}, Stdout: &strings.Builder{}}
	// Missing command (no positional arg) must fail fast.
	err := r.runNodeExec(context.Background(), []string{"--node", "n1"})
	if err == nil {
		t.Fatal("expected error when command is missing")
	}
	// Missing --node must fail fast.
	err = r.runNodeExec(context.Background(), []string{"echo hi"})
	if err == nil {
		t.Fatal("expected error when --node is missing")
	}
}

func TestRunNodeExecStreamsSSEThroughCenterProxy(t *testing.T) {
	var mu sync.Mutex
	var gotNode, gotCommand, gotAuth string
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		// Path layout from execURL: /api/control/nodes/{id}/proxy/v1/exec
		if !strings.HasSuffix(r.URL.Path, "/api/control/nodes/node-a/proxy/v1/exec") {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		var body struct {
			Command      string `json:"command"`
			WorkspaceDir string `json:"workspace_dir"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		gotNode = strings.Split(strings.TrimPrefix(r.URL.Path, "/api/control/nodes/"), "/")[0]
		gotCommand = body.Command
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		// The node emits cumulative output snapshots; the CLI prints only the
		// delta since the previous event.
		_, _ = fmt.Fprint(w, "data: {\"output\":\"line1\\n\",\"final\":false,\"exit_code\":0}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: {\"output\":\"line1\\nline2\\n\",\"final\":true,\"exit_code\":7}\n\n")
		flusher.Flush()
	}))
	defer center.Close()

	var out strings.Builder
	r := &Runner{Stderr: &strings.Builder{}, Stdout: &out}
	err := r.runNodeExec(context.Background(), []string{
		"--node", "node-a",
		"--center", center.URL,
		"--token", "secret",
		"--dir", "/tmp/ws",
		"echo hi",
	})
	if err == nil {
		t.Fatal("expected non-zero exit code error")
	}
	if !strings.Contains(err.Error(), "7") {
		t.Fatalf("expected exit code 7 in error, got: %v", err)
	}
	if out.String() != "line1\nline2\n" {
		t.Fatalf("unexpected output: %q", out.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if gotNode != "node-a" {
		t.Fatalf("unexpected node: %q", gotNode)
	}
	if gotCommand != "echo hi" {
		t.Fatalf("unexpected command: %q", gotCommand)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("unexpected auth header: %q", gotAuth)
	}
}

// TestRunNodeExecZeroExit verifies a successful command does not error.
func TestRunNodeExecZeroExit(t *testing.T) {
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"output\":\"done\\n\",\"final\":true,\"exit_code\":0}\n\n")
	}))
	defer center.Close()

	var out strings.Builder
	r := &Runner{Stderr: &strings.Builder{}, Stdout: &out}
	err := r.runNodeExec(context.Background(), []string{
		"--node", "node-a",
		"--center", center.URL,
		"echo done",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if out.String() != "done\n" {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

var _ = time.Second
