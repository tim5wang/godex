package app

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
)

// TestRunNodeForwardUsesDefaultNode verifies that when --node is omitted, the
// runner falls back to control.default_node instead of failing fast.
func TestRunNodeForwardUsesDefaultNode(t *testing.T) {
	r := &Runner{
		Cfg:    &config.Config{Control: config.ControlConfig{DefaultNode: "n1"}},
		Stderr: &strings.Builder{},
		Stdout: &strings.Builder{},
	}
	// --node omitted: validation must pass and the flow should reach the
	// center URL check (which fails with a "center" hint), not the --node error.
	err := r.runNodeForward(context.Background(), []string{"--target", "10.0.0.5:3306"})
	if err == nil {
		t.Fatal("expected an error (no center URL configured)")
	}
	if strings.Contains(err.Error(), "--node is required") {
		t.Fatalf("expected default_node fallback to avoid --node error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "center") {
		t.Fatalf("expected center hint after default node fallback, got: %v", err)
	}
}

// TestRunNodeExecUsesDefaultNode verifies the same fallback for node exec.
func TestRunNodeExecUsesDefaultNode(t *testing.T) {
	r := &Runner{
		Cfg:    &config.Config{Control: config.ControlConfig{DefaultNode: "n1"}},
		Stderr: &strings.Builder{},
		Stdout: &strings.Builder{},
	}
	err := r.runNodeExec(context.Background(), []string{"echo hi"})
	if err == nil {
		t.Fatal("expected an error (no center URL configured)")
	}
	if strings.Contains(err.Error(), "--node is required") {
		t.Fatalf("expected default_node fallback to avoid --node error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "center") {
		t.Fatalf("expected center hint after default node fallback, got: %v", err)
	}
}

// TestRunNodeForwardExplicitNodeBeatsDefault verifies that an explicit --node
// flag wins over control.default_node.
func TestRunNodeForwardExplicitNodeBeatsDefault(t *testing.T) {
	r := &Runner{
		Cfg:    &config.Config{Control: config.ControlConfig{DefaultNode: "default-node"}},
		Stderr: &strings.Builder{},
		Stdout: &strings.Builder{},
	}
	err := r.runNodeForward(context.Background(), []string{
		"--node", "explicit-node", "--target", "10.0.0.5:3306",
	})
	if err == nil {
		t.Fatal("expected an error (no center URL configured)")
	}
	if strings.Contains(err.Error(), "default-node") {
		t.Fatalf("expected explicit --node to be used, got: %v", err)
	}
	if !strings.Contains(err.Error(), "center") {
		t.Fatalf("expected center hint, got: %v", err)
	}
}

// TestRunNodeForwardStillRequiresNodeWhenNoDefault verifies that without
// --node and without control.default_node the command still fails fast.
func TestRunNodeForwardStillRequiresNodeWhenNoDefault(t *testing.T) {
	r := &Runner{
		Cfg:    &config.Config{Control: config.ControlConfig{}},
		Stderr: &strings.Builder{},
		Stdout: &strings.Builder{},
	}
	err := r.runNodeForward(context.Background(), []string{"--target", "10.0.0.5:3306"})
	if err == nil {
		t.Fatal("expected error when no --node and no default_node")
	}
	if !strings.Contains(err.Error(), "--node") {
		t.Fatalf("expected --node hint, got: %v", err)
	}
}
