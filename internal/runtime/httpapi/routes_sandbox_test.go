package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSandboxExecEndpoint verifies the B-side exec endpoint runs a shell
// command in the workspace and returns the bounded output result.
func TestSandboxExecEndpoint(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	mux := http.NewServeMux()
	registerSandboxRoutes(mux, manager, func(h http.Handler) http.Handler { return h })
	server := httptest.NewServer(mux)
	defer server.Close()

	body := `{"command":"echo remote-sandbox-ok","workspace":"` + cfg.WorkspaceDir + `"}`
	resp, err := http.Post(server.URL+"/control/sandbox/exec", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data := make([]byte, 4096)
		n, _ := resp.Body.Read(data)
		t.Fatalf("exec status %d: %s", resp.StatusCode, strings.TrimSpace(string(data[:n])))
	}
	var out struct {
		Text     string `json:"text"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode exec response: %v", err)
	}
	if !strings.Contains(out.Text, "remote-sandbox-ok") {
		t.Fatalf("exec output = %q, want echo of remote-sandbox-ok", out.Text)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0", out.ExitCode)
	}
}

// TestSandboxExecRejectsMissingCommand checks the endpoint requires a command.
func TestSandboxExecRejectsMissingCommand(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	mux := http.NewServeMux()
	registerSandboxRoutes(mux, manager, func(h http.Handler) http.Handler { return h })
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Post(server.URL+"/control/sandbox/exec", "application/json", strings.NewReader(`{"command":""}`))
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	_ = resp.Body.Close()
	// An empty command should be rejected (missing command argument) before
	// execution; the handler surfaces validation as a 400.
	if resp.StatusCode != http.StatusBadRequest {
		data := make([]byte, 4096)
		n, _ := resp.Body.Read(data)
		t.Fatalf("expected 400 for empty command, got %d: %s", resp.StatusCode, strings.TrimSpace(string(data[:n])))
	}
}

// TestSandboxFSEndpointRoundTrip verifies write then read through the B-side
// fs endpoint round-trips file contents (base64 payloads).
func TestSandboxFSEndpointRoundTrip(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	mux := http.NewServeMux()
	registerSandboxRoutes(mux, manager, func(h http.Handler) http.Handler { return h })
	server := httptest.NewServer(mux)
	defer server.Close()

	path := "remote-sandbox-test.txt"
	content := base64.StdEncoding.EncodeToString([]byte("hello remote fs"))
	writeBody := `{"op":"write","path":"` + path + `","data":"` + content + `","workspace":"` + cfg.WorkspaceDir + `"}`
	resp, err := http.Post(server.URL+"/control/sandbox/fs", "application/json", bytes.NewReader([]byte(writeBody)))
	if err != nil {
		t.Fatalf("fs write: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fs write status %d", resp.StatusCode)
	}

	readBody := `{"op":"read","path":"` + path + `","workspace":"` + cfg.WorkspaceDir + `"}`
	resp, err = http.Post(server.URL+"/control/sandbox/fs", "application/json", bytes.NewReader([]byte(readBody)))
	if err != nil {
		t.Fatalf("fs read: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fs read status %d", resp.StatusCode)
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode fs read: %v", err)
	}
	got, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		t.Fatalf("decode read data: %v", err)
	}
	if string(got) != "hello remote fs" {
		t.Fatalf("round-trip content = %q, want %q", string(got), "hello remote fs")
	}
}

// TestSandboxFSEndpointRejectsBadOp checks unsupported ops fail cleanly.
func TestSandboxFSEndpointRejectsBadOp(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	mux := http.NewServeMux()
	registerSandboxRoutes(mux, manager, func(h http.Handler) http.Handler { return h })
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Post(server.URL+"/control/sandbox/fs", "application/json", strings.NewReader(`{"op":"nope","path":"x"}`))
	if err != nil {
		t.Fatalf("fs: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad op, got %d", resp.StatusCode)
	}
}
