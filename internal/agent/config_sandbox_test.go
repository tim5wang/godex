package agent

import (
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/tooling"
)

// TestConfigWithSandboxNodeForcesRelay verifies that pinning a sandbox node on
// a per-session config switches tool execution to relay mode with the target
// node id, without mutating the original config.
func TestConfigWithSandboxNodeForcesRelay(t *testing.T) {
	cfg := &config.Config{
		WorkspaceDir: "/base",
		TempDir:      "/base/.godex/tmp",
		Tools: config.ToolsConfig{
			Execution: config.ToolExecutionConfig{Mode: "local"},
		},
	}
	got := ConfigWithSandboxNode(cfg, "pod-b")
	if got == cfg {
		t.Fatal("expected a cloned config, got the same pointer")
	}
	if got.Tools.Execution.Mode != tooling.ExecutionModeRelay {
		t.Fatalf("mode = %q, want relay", got.Tools.Execution.Mode)
	}
	if got.Tools.Execution.RelayNode != "pod-b" {
		t.Fatalf("relay_node = %q, want pod-b", got.Tools.Execution.RelayNode)
	}
	// The original config must be untouched.
	if cfg.Tools.Execution.Mode != "local" {
		t.Fatalf("original mode mutated: %q", cfg.Tools.Execution.Mode)
	}
}

// TestConfigWithSandboxNodeEmptyKeepsLocal verifies an empty node id returns
// the original config unchanged.
func TestConfigWithSandboxNodeEmptyKeepsLocal(t *testing.T) {
	cfg := &config.Config{
		Tools: config.ToolsConfig{
			Execution: config.ToolExecutionConfig{Mode: "local"},
		},
	}
	if got := ConfigWithSandboxNode(cfg, ""); got != cfg {
		t.Fatal("expected original config when node id is empty")
	}
	if got := ConfigWithSandboxNode(nil, "pod-b"); got != nil {
		t.Fatal("expected nil when cfg is nil")
	}
}

// TestConfigWithExecutionModeUnifiedParser covers the unified new-chat picker
// values: local (unchanged), docker/ssh (session backend override), and
// relay:<node_id> (RemoteSandbox).
func TestConfigWithExecutionModeUnifiedParser(t *testing.T) {
	base := &config.Config{
		Tools: config.ToolsConfig{
			Execution: config.ToolExecutionConfig{Mode: "local"},
		},
	}

	// Empty / local keep the original config (no clone, no mutation).
	if got := ConfigWithExecutionMode(base, ""); got != base {
		t.Fatal("empty mode must return original config")
	}
	if got := ConfigWithExecutionMode(base, "local"); got != base {
		t.Fatal("local mode must return original config")
	}

	// docker / ssh clone and pin the execution backend.
	docker := ConfigWithExecutionMode(base, "docker")
	if docker == base || docker.Tools.Execution.Mode != tooling.ExecutionModeDocker {
		t.Fatalf("docker mode = %q", docker.Tools.Execution.Mode)
	}
	ssh := ConfigWithExecutionMode(base, "ssh")
	if ssh == base || ssh.Tools.Execution.Mode != tooling.ExecutionModeSSH {
		t.Fatalf("ssh mode = %q", ssh.Tools.Execution.Mode)
	}

	// relay:<id> forces RemoteSandbox relay mode with the target node id.
	relay := ConfigWithExecutionMode(base, "relay:pod-b")
	if relay == base || relay.Tools.Execution.Mode != tooling.ExecutionModeRelay || relay.Tools.Execution.RelayNode != "pod-b" {
		t.Fatalf("relay mode = %+v", relay.Tools.Execution)
	}
	// Original stays untouched.
	if base.Tools.Execution.Mode != "local" || base.Tools.Execution.RelayNode != "" {
		t.Fatal("original config mutated")
	}
}

// TestConfigWithSandboxNodeKeepsExplicitRelayFields verifies explicit
// relay_center / relay_token survive the clone.
func TestConfigWithSandboxNodeKeepsExplicitRelayFields(t *testing.T) {
	cfg := &config.Config{
		Tools: config.ToolsConfig{
			Execution: config.ToolExecutionConfig{
				Mode:        "local",
				RelayCenter: "https://center.example",
				RelayToken:  "nk_override",
			},
		},
	}
	got := ConfigWithSandboxNode(cfg, "pod-b")
	if got.Tools.Execution.RelayCenter != "https://center.example" {
		t.Fatalf("relay_center = %q", got.Tools.Execution.RelayCenter)
	}
	if got.Tools.Execution.RelayToken != "nk_override" {
		t.Fatalf("relay_token = %q", got.Tools.Execution.RelayToken)
	}
}
