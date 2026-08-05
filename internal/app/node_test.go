package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestForwardWSURL(t *testing.T) {
	cases := []struct {
		center string
		nodeID string
		want   string
	}{
		{"https://godex.claw.carc.top", "node_a", "wss://godex.claw.carc.top/api/control/nodes/node_a/forward"},
		{"http://127.0.0.1:3921", "n1", "ws://127.0.0.1:3921/api/control/nodes/n1/forward"},
		{"http://127.0.0.1:3921/", "n1", "ws://127.0.0.1:3921/api/control/nodes/n1/forward"},
		{"wss://hub.example.com/base", "n2", "wss://hub.example.com/base/api/control/nodes/n2/forward"},
	}
	for _, tc := range cases {
		got, err := forwardWSURL(tc.center, tc.nodeID)
		if err != nil {
			t.Fatalf("forwardWSURL(%q): %v", tc.center, err)
		}
		if got != tc.want {
			t.Fatalf("forwardWSURL(%q) = %q, want %q", tc.center, got, tc.want)
		}
	}
}

func TestForwardWSURLRejectsBadInput(t *testing.T) {
	if _, err := forwardWSURL("", "n1"); err == nil {
		t.Fatal("expected error for empty center")
	}
	if _, err := forwardWSURL("ftp://x", "n1"); err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
	if _, err := forwardWSURL("https://x", ""); err == nil {
		t.Fatal("expected error for empty node id")
	}
}

func TestRunNodeForwardValidatesRequiredFlags(t *testing.T) {
	r := &Runner{Stderr: &strings.Builder{}, Stdout: &strings.Builder{}}
	// Missing --target must fail fast before any dial attempt.
	err := r.runNodeForward(context.Background(), []string{"--node", "n1"})
	if err == nil {
		t.Fatal("expected error when --target is missing")
	}
	if !strings.Contains(err.Error(), "--target") {
		t.Fatalf("expected --target hint in error, got: %v", err)
	}
	// Missing --node must fail fast too.
	err = r.runNodeForward(context.Background(), []string{"--target", "10.0.0.5:3306"})
	if err == nil {
		t.Fatal("expected error when --node is missing")
	}
}

func TestRunNodeForwardRequiresCenterURL(t *testing.T) {
	r := &Runner{Stderr: &strings.Builder{}, Stdout: &strings.Builder{}}
	err := r.runNodeForward(context.Background(), []string{
		"--node", "n1", "--target", "10.0.0.5:3306",
	})
	if err == nil {
		t.Fatal("expected error when no center URL is configured")
	}
	if !strings.Contains(err.Error(), "center") {
		t.Fatalf("expected center hint in error, got: %v", err)
	}
}

// TestRunNodeForwardUnreachableCenter verifies that a bad center URL produces a
// dial error rather than hanging forever.
func TestRunNodeForwardUnreachableCenter(t *testing.T) {
	r := &Runner{Stderr: &strings.Builder{}, Stdout: &strings.Builder{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := r.runNodeForward(ctx, []string{
		"--node", "n1",
		"--target", "127.0.0.1:1",
		"--center", "http://127.0.0.1:1",
	})
	if err == nil {
		t.Fatal("expected dial error for unreachable center")
	}
}
