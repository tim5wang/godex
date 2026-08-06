package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
)

func newJoinRunner(t *testing.T) (*Runner, *config.Manager, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	manager, err := config.NewManager(config.Options{HomeDir: home, WorkspaceDir: workspace})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	r := &Runner{
		Cfg:           manager.Current(),
		ConfigManager: manager,
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
	}
	return r, manager, home
}

// TestRunNodeJoinSyncsNodeIDFile verifies that join writes the chosen node id
// into state/node.json so the node registers under the operator-specified id
// (not a stale auto-generated one).
func TestRunNodeJoinSyncsNodeIDFile(t *testing.T) {
	r, manager, _ := newJoinRunner(t)
	stateDir := manager.Current().StateDir
	if err := r.runNodeJoin(context.Background(), []string{
		"https://godex.claw.carc.top",
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
	}); err != nil {
		t.Fatalf("node join: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "node.json"))
	if err != nil {
		t.Fatalf("read state/node.json: %v", err)
	}
	if !strings.Contains(string(data), "my-laptop") {
		t.Fatalf("expected node id synced to node.json, got:\n%s", data)
	}
}

func TestRunNodeJoinWritesControlConfig(t *testing.T) {
	r, manager, home := newJoinRunner(t)
	err := r.runNodeJoin(context.Background(), []string{
		"https://godex.claw.carc.top",
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
		"--trust", "guarded-remote",
		"--name", "dev box",
	})
	if err != nil {
		t.Fatalf("node join: %v", err)
	}
	cfg := manager.Current()
	if cfg.Control.CenterURL != "https://godex.claw.carc.top" {
		t.Fatalf("expected center_url %q, got %q", "https://godex.claw.carc.top", cfg.Control.CenterURL)
	}
	if cfg.Control.Credential != "ck_test_abc123" {
		t.Fatalf("expected credential %q, got %q", "ck_test_abc123", cfg.Control.Credential)
	}
	if cfg.Control.NodeID != "my-laptop" {
		t.Fatalf("expected node_id %q, got %q", "my-laptop", cfg.Control.NodeID)
	}
	if cfg.Control.TrustLevel != "guarded-remote" {
		t.Fatalf("expected trust_level %q, got %q", "guarded-remote", cfg.Control.TrustLevel)
	}
	if cfg.Control.NodeName != "dev box" {
		t.Fatalf("expected node_name %q, got %q", "dev box", cfg.Control.NodeName)
	}

	data, err := os.ReadFile(filepath.Join(home, "godex.yaml"))
	if err != nil {
		t.Fatalf("read home godex.yaml: %v", err)
	}
	for _, want := range []string{"center_url: https://godex.claw.carc.top", "node_id: my-laptop", "trust_level: guarded-remote"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("expected %q in godex.yaml, got:\n%s", want, data)
		}
	}
	// The credential is a secret: it must live in the home .env, not godex.yaml.
	envData, err := os.ReadFile(filepath.Join(home, ".env"))
	if err != nil {
		t.Fatalf("read home .env: %v", err)
	}
	if !strings.Contains(string(envData), "GODEX_CONTROL_CREDENTIAL=ck_test_abc123") {
		t.Fatalf("expected credential in .env, got:\n%s", envData)
	}
}

func TestRunNodeJoinDefaultsTrustToTrusted(t *testing.T) {
	r, manager, _ := newJoinRunner(t)
	err := r.runNodeJoin(context.Background(), []string{
		"https://godex.claw.carc.top",
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
	})
	if err != nil {
		t.Fatalf("node join: %v", err)
	}
	if got := manager.Current().Control.TrustLevel; got != "trusted" {
		t.Fatalf("expected default trust_level trusted, got %q", got)
	}
}

func TestRunNodeJoinDoesNotOverwriteUnrelatedConfig(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "godex.yaml"), []byte(strings.Join([]string{
		"agent:",
		"  profile: coding",
		"control:",
		"  node_name: existing-name",
	}, "\n")), 0600); err != nil {
		t.Fatalf("write home godex.yaml: %v", err)
	}
	manager, err := config.NewManager(config.Options{HomeDir: home, WorkspaceDir: workspace})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	r := &Runner{
		Cfg:           manager.Current(),
		ConfigManager: manager,
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
	}
	if err := r.runNodeJoin(context.Background(), []string{
		"https://godex.claw.carc.top",
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
	}); err != nil {
		t.Fatalf("node join: %v", err)
	}
	cfg := manager.Current()
	if cfg.AgentProfile != "coding" {
		t.Fatalf("expected agent.profile preserved, got %q", cfg.AgentProfile)
	}
	if cfg.Control.NodeName != "existing-name" {
		t.Fatalf("expected existing node_name preserved, got %q", cfg.Control.NodeName)
	}
}

func TestRunNodeJoinValidatesArguments(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing id",
			args: []string{"https://godex.claw.carc.top", "--credential", "ck_x"},
			want: "--id",
		},
		{
			name: "missing credential",
			args: []string{"https://godex.claw.carc.top", "--id", "n1"},
			want: "--credential",
		},
		{
			name: "bad credential prefix",
			args: []string{"https://godex.claw.carc.top", "--id", "n1", "--credential", "secret"},
			want: "ck_",
		},
		{
			name: "invalid id characters",
			args: []string{"https://godex.claw.carc.top", "--id", "bad id!", "--credential", "ck_x"},
			want: "node id",
		},
		{
			name: "invalid center url",
			args: []string{"not-a-url", "--id", "n1", "--credential", "ck_x"},
			want: "center",
		},
		{
			name: "missing center url",
			args: []string{"--id", "n1", "--credential", "ck_x"},
			want: "center",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _ := newJoinRunner(t)
			err := r.runNodeJoin(context.Background(), tc.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("expected error containing %q, got: %v", tc.want, err)
			}
		})
	}
}
