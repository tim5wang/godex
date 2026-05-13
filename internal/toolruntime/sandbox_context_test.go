package toolruntime

import (
	"context"
	"testing"
)

func TestSandboxIDContext(t *testing.T) {
	ctx := WithSandboxID(context.Background(), "sandbox:local:abc123")

	if got := SandboxIDFromContext(ctx); got != "sandbox:local:abc123" {
		t.Fatalf("sandbox id %q", got)
	}
	if got := SandboxIDFromContext(WithSandboxID(context.Background(), "")); got != "" {
		t.Fatalf("empty sandbox id should not annotate context, got %q", got)
	}
}

func TestSandboxIDContextDoesNotReplaceSessionID(t *testing.T) {
	ctx := WithSessionID(context.Background(), "session-1")
	ctx = WithSandboxID(ctx, "sandbox:local:abc123")

	if got := SessionIDFromContext(ctx); got != "session-1" {
		t.Fatalf("session id %q", got)
	}
	if got := SandboxIDFromContext(ctx); got != "sandbox:local:abc123" {
		t.Fatalf("sandbox id %q", got)
	}
}
