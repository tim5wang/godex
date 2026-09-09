package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/platform/tooling"
)

// TestRemoteSandboxToolBindingForcesRelay verifies ToolBinding always reports
// ExecutionModeRelay with the configured relay target/credential.
func TestRemoteSandboxToolBindingForcesRelay(t *testing.T) {
	s := NewRemote(RemoteOptions{
		CenterURL: "https://center.example",
		NodeID:    "pod-b",
		Token:     "nk_secret",
		WorkspaceDir: "/ws",
		Execution:    tooling.ExecutionConfig{Mode: tooling.ExecutionModeLocal}, // overridden
	})
	binding := s.ToolBinding()
	if binding.Execution.Mode != tooling.ExecutionModeRelay {
		t.Fatalf("mode = %q, want relay", binding.Execution.Mode)
	}
	if binding.Execution.RelayNode != "pod-b" || binding.Execution.RelayCenter != "https://center.example" || binding.Execution.RelayToken != "nk_secret" {
		t.Fatalf("relay binding = %+v", binding.Execution)
	}
	if s.ID() == "" || s.Lifecycle() == "" {
		t.Fatalf("empty id/lifecycle")
	}
	if s.WorkspaceDir() != "/ws" {
		t.Fatalf("workspace = %q", s.WorkspaceDir())
	}
}

// fakeSandboxNode simulates B's /control/sandbox/fs endpoint over the relay
// proxy path so RemoteFS can be tested without a live relay channel.
func fakeSandboxNode(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/control/nodes/pod-b/proxy/control/sandbox/fs" {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
}

// TestRemoteFSReadFile verifies ReadFile goes through the relay and decodes
// the base64 payload.
func TestRemoteFSReadFile(t *testing.T) {
	center := fakeSandboxNode(t, func(w http.ResponseWriter, r *http.Request) {
		var req tooling.FSRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Op != "read" || req.Path != "notes.txt" {
			t.Errorf("unexpected fs request: %+v", req)
		}
		_ = json.NewEncoder(w).Encode(tooling.FSResult{Data: base64.StdEncoding.EncodeToString([]byte("remote content"))})
	})
	defer center.Close()

	s := NewRemote(RemoteOptions{CenterURL: center.URL, NodeID: "pod-b", Token: "nk_secret", WorkspaceDir: "/ws"})
	fs, err := s.FileSystem()
	if err != nil {
		t.Fatalf("file system: %v", err)
	}
	data, err := fs.ReadFile("notes.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "remote content" {
		t.Fatalf("content = %q", string(data))
	}
	if fs.Dir() != "/ws" {
		t.Fatalf("dir = %q", fs.Dir())
	}
}

// TestRemoteFSWriteFile verifies WriteFile encodes the payload to base64 and
// sends the expected op.
func TestRemoteFSWriteFile(t *testing.T) {
	center := fakeSandboxNode(t, func(w http.ResponseWriter, r *http.Request) {
		var req tooling.FSRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Op != "write" || req.Path != "out.txt" {
			t.Errorf("unexpected fs request: %+v", req)
		}
		decoded, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil || string(decoded) != "payload" {
			t.Errorf("bad write payload: %q err=%v", req.Data, err)
		}
		_ = json.NewEncoder(w).Encode(tooling.FSResult{})
	})
	defer center.Close()

	s := NewRemote(RemoteOptions{CenterURL: center.URL, NodeID: "pod-b", Token: "nk_secret", WorkspaceDir: "/ws"})
	fs, err := s.FileSystem()
	if err != nil {
		t.Fatalf("file system: %v", err)
	}
	if err := fs.WriteFile("out.txt", []byte("payload"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestRemoteFSReadDirStatMkdir verifies the remaining FS surface maps to ops.
func TestRemoteFSReadDirStatMkdir(t *testing.T) {
	center := fakeSandboxNode(t, func(w http.ResponseWriter, r *http.Request) {
		var req tooling.FSRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Op {
		case "readdir":
			_ = json.NewEncoder(w).Encode(tooling.FSResult{Entries: []tooling.FSDirItem{{Name: "a.txt", IsDir: false}}})
		case "stat":
			_ = json.NewEncoder(w).Encode(tooling.FSResult{Info: &tooling.FSFileInfo{Name: "dir", Size: 0, IsDir: true}})
		case "mkdir":
			_ = json.NewEncoder(w).Encode(tooling.FSResult{})
		default:
			t.Errorf("unexpected op %q", req.Op)
		}
	})
	defer center.Close()

	s := NewRemote(RemoteOptions{CenterURL: center.URL, NodeID: "pod-b", Token: "nk_secret", WorkspaceDir: "/ws"})
	fs, err := s.FileSystem()
	if err != nil {
		t.Fatalf("file system: %v", err)
	}
	entries, err := fs.ReadDir(".")
	if err != nil || len(entries) != 1 || entries[0].Name() != "a.txt" {
		t.Fatalf("readdir = %+v err=%v", entries, err)
	}
	info, err := fs.Stat("dir")
	if err != nil || !info.IsDir() {
		t.Fatalf("stat = %+v err=%v", info, err)
	}
	if err := fs.MkdirAll("newdir", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got, _ := fs.Abs("rel.txt"); !strings.HasSuffix(got, filepath.FromSlash("/ws/rel.txt")) {
		t.Fatalf("abs = %q", got)
	}
}

// TestRemoteSandboxRebuildKeepsTarget verifies Rebuild preserves identity and
// the relay target.
func TestRemoteSandboxRebuildKeepsTarget(t *testing.T) {
	s := NewRemote(RemoteOptions{CenterURL: "https://c", NodeID: "pod-b", Token: "nk", WorkspaceDir: "/ws"})
	reb := s.Rebuild()
	if reb == nil || reb.ID() != s.ID() {
		t.Fatalf("rebuild identity mismatch")
	}
	b := reb.ToolBinding().Execution
	if b.RelayNode != "pod-b" || b.Mode != tooling.ExecutionModeRelay {
		t.Fatalf("rebuild relay = %+v", b)
	}
}

var _ = context.Background
