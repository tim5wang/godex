package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/tools"
)

// newTestScopeWriteHandler builds a ToolHandler with write_file registered and
// the scope write interceptor installed against root.
func newTestScopeWriteHandler(t *testing.T, root string) *tools.ToolHandler {
	t.Helper()
	executor := tooling.NewWorkspaceExecutor(root)
	handler := tools.NewToolHandler()
	handler.RegisterWithMeta(tools.NewWriteFileToolWithExecutor(executor), tools.ToolMeta{
		Bundle:        "core_code",
		DefaultActive: true,
	})
	handler.AddBeforeInterceptorsForTools(
		[]string{"write_file", "edit_file", "attach_file"},
		NewScopeWriteInterceptor(root),
	)
	handler.ActivateDefaults()
	return handler
}

func TestScopeWriteInterceptorRejectsEscape(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	handler := newTestScopeWriteHandler(t, root)

	out, err := handler.Handle(context.Background(), "write_file", map[string]interface{}{
		"path":    "../../etc/passwd",
		"content": "x",
	})
	if err != nil {
		t.Fatalf("interceptor short-circuits with nil error, got %v", err)
	}
	if !strings.Contains(out, "scope write guard") {
		t.Fatalf("output should mention scope write guard, got %q", out)
	}
}

func TestScopeWriteInterceptorAllowsInside(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	handler := newTestScopeWriteHandler(t, root)

	out, err := handler.Handle(context.Background(), "write_file", map[string]interface{}{
		"path":    "docs/plan.md",
		"content": "hello",
	})
	if err != nil {
		t.Fatalf("in-scope write should succeed: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("unexpected output %q", out)
	}
}

func TestScopeWriteInterceptorAllowsAbsoluteInsideRoot(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	handler := newTestScopeWriteHandler(t, root)

	inside := filepath.Join(root, "sub", "f.txt")
	out, err := handler.Handle(context.Background(), "write_file", map[string]interface{}{
		"path":    inside,
		"content": "hi",
	})
	if err != nil {
		t.Fatalf("absolute path inside root should succeed: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("unexpected output %q", out)
	}
}

func TestScopeWriteInterceptorSkipsOtherTools(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	handler := newTestScopeWriteHandler(t, root)

	// read_file is not guarded; it is not registered here, so expect "tool not
	// found" rather than a scope guard error — proves the interceptor does not
	// fire for non-write tools.
	_, err := handler.Handle(context.Background(), "read_file", map[string]interface{}{
		"path": "../../etc/passwd",
	})
	if err == nil || strings.Contains(err.Error(), "scope write guard") {
		t.Fatalf("non-write tool should not hit the scope guard, got %v", err)
	}
}
