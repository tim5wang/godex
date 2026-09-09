package agent

import (
	"testing"

	"github.com/tim5wang/godex/internal/platform/tooling"
)

// TestEnsureExecutionSandboxRebuildsForRelay verifies that a session created
// with a per-session relay exec_mode actually binds its tools to a remote
// sandbox instead of the shared local one. Regression for: picking a relay
// node in the new-chat picker still ran bash/file tools locally because
// ensureWorkspaceSandbox only reacted to workspace changes, never exec_mode.
func TestEnsureExecutionSandboxRebuildsForRelay(t *testing.T) {
	a := newTestAgent(t, 4096)
	// Simulate the shared sandbox (global config => local) already being set.
	baseCfg := a.cfg
	a.sandbox = sandboxFromConfig(baseCfg)

	// Session config carries exec_mode=relay:<node> (ConfigWithExecutionMode).
	sessionCfg := ConfigWithExecutionMode(baseCfg, "relay:pod-b")
	if sessionCfg == baseCfg {
		t.Fatal("expected a cloned session config")
	}
	a.ensureExecutionSandbox(sessionCfg)

	binding := a.SandboxBinding()
	if binding.Execution.Mode != tooling.ExecutionModeRelay {
		t.Fatalf("binding mode = %q, want relay", binding.Execution.Mode)
	}
	if binding.Execution.RelayNode != "pod-b" {
		t.Fatalf("relay node = %q, want pod-b", binding.Execution.RelayNode)
	}
	if binding.SandboxID == "" {
		t.Fatal("expected a sandbox id")
	}
}

// TestEnsureExecutionSandboxKeepsLocal verifies the default local mode leaves
// the shared sandbox untouched.
func TestEnsureExecutionSandboxKeepsLocal(t *testing.T) {
	a := newTestAgent(t, 4096)
	before := a.sandbox
	a.ensureExecutionSandbox(a.cfg)
	if a.sandbox != before {
		t.Fatal("local mode must not rebuild the sandbox")
	}
}
