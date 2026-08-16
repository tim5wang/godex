package toolruntime

import (
	"context"
	"strings"
	"testing"
)

func TestRegisterOwnedReturnsDisposerAndRemovesCleanly(t *testing.T) {
	handler := NewToolHandler()
	reg, err := handler.RegisterOwned("plugin-a", fakeTool{name: "p_tool"}, ToolMeta{Bundle: "plugin", AlwaysActive: true})
	if err != nil {
		t.Fatalf("register owned: %v", err)
	}
	if reg == nil || reg.Owner() != "plugin-a" || reg.Generation() == 0 {
		t.Fatalf("unexpected registration handle: %+v", reg)
	}
	if handler.Get("p_tool") == nil || !handler.IsActive("p_tool") {
		t.Fatal("expected tool registered and active")
	}

	reg.Dispose()
	if handler.Get("p_tool") != nil {
		t.Fatal("expected tool removed after dispose")
	}
	// Dispose is idempotent.
	reg.Dispose()
	if handler.OwnerFor("p_tool") != "" {
		t.Fatal("expected owner cleared")
	}
}

func TestRegisterOwnedConflictRejected(t *testing.T) {
	handler := NewToolHandler()
	if _, err := handler.RegisterOwned("plugin-a", fakeTool{name: "shared"}, ToolMeta{AlwaysActive: true}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, err := handler.RegisterOwned("plugin-b", fakeTool{name: "shared"}, ToolMeta{AlwaysActive: true}); err == nil {
		t.Fatal("expected conflict error for second owner")
	} else if !strings.Contains(err.Error(), "already owned by") {
		t.Fatalf("unexpected conflict error: %v", err)
	}
	// Same owner may replace.
	if _, err := handler.RegisterOwned("plugin-a", fakeTool{name: "shared"}, ToolMeta{AlwaysActive: true}); err != nil {
		t.Fatalf("same-owner re-register should replace: %v", err)
	}
	// Anonymous registration may replace any.
	if _, err := handler.RegisterOwned("", fakeTool{name: "shared"}, ToolMeta{AlwaysActive: true}); err != nil {
		t.Fatalf("anonymous re-register should replace: %v", err)
	}
}

func TestUnregisterOwnerRemovesAllOwnedTools(t *testing.T) {
	handler := NewToolHandler()
	for _, name := range []string{"a_tool", "b_tool"} {
		if _, err := handler.RegisterOwned("plugin-x", fakeTool{name: name}, ToolMeta{Bundle: "plugin", AlwaysActive: true}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	if _, err := handler.RegisterOwned("plugin-y", fakeTool{name: "y_tool"}, ToolMeta{AlwaysActive: true}); err != nil {
		t.Fatalf("register y: %v", err)
	}
	removed := handler.UnregisterOwner("plugin-x")
	if len(removed) != 2 {
		t.Fatalf("expected 2 removed, got %v", removed)
	}
	if handler.Get("a_tool") != nil || handler.Get("b_tool") != nil {
		t.Fatal("expected plugin-x tools removed")
	}
	if handler.Get("y_tool") == nil {
		t.Fatal("expected plugin-y tool untouched")
	}
	// Bundle bookkeeping cleaned: plugin bundle should be gone (both tools removed).
	catalog := handler.Catalog()
	for _, bundle := range catalog.Bundles {
		if bundle.Name == "plugin" {
			t.Fatal("expected plugin bundle removed after unregistering all its tools")
		}
	}
}

func TestStaleRegistrationDisposeDoesNotRemoveReplacement(t *testing.T) {
	handler := NewToolHandler()
	first, err := handler.RegisterOwned("plugin-a", fakeTool{name: "evolving"}, ToolMeta{AlwaysActive: true})
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	second, err := handler.RegisterOwned("plugin-a", fakeTool{name: "evolving"}, ToolMeta{AlwaysActive: true})
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	if first.Generation() == second.Generation() {
		t.Fatal("expected distinct generations")
	}
	if handler.CurrentGeneration("evolving") != second.Generation() {
		t.Fatal("expected current generation to match latest registration")
	}
	// Disposing the stale handle must not remove the replacement.
	first.Dispose()
	if handler.Get("evolving") == nil {
		t.Fatal("stale dispose must not remove replacement")
	}
	second.Dispose()
	if handler.Get("evolving") != nil {
		t.Fatal("current dispose should remove tool")
	}
}

func TestMarkDrainingRejectsNewCalls(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool{name: "doomed"}, ToolMeta{AlwaysActive: true})
	handler.MarkDraining("doomed")
	if _, err := handler.HandleResult(t.Context(), "doomed", map[string]interface{}{}); err == nil {
		t.Fatal("expected draining error")
	} else if _, ok := err.(ErrToolDraining); !ok {
		t.Fatalf("expected ErrToolDraining, got %T: %v", err, err)
	}
	// Re-registration clears draining.
	handler.RegisterWithMeta(fakeTool{name: "doomed"}, ToolMeta{AlwaysActive: true})
	if _, err := handler.HandleResult(t.Context(), "doomed", map[string]interface{}{}); err != nil {
		t.Fatalf("expected call to succeed after re-registration, got %v", err)
	}
}

func TestReplaceWithPreservesOwnershipAndGeneration(t *testing.T) {
	handler := NewToolHandler()
	if _, err := handler.RegisterOwned("plugin-a", fakeTool{name: "owned"}, ToolMeta{Bundle: "plugin", AlwaysActive: true}); err != nil {
		t.Fatalf("register: %v", err)
	}
	generation := handler.CurrentGeneration("owned")

	next := NewToolHandler()
	if _, err := next.RegisterOwned("plugin-a", fakeTool{name: "owned"}, ToolMeta{Bundle: "plugin", AlwaysActive: true}); err != nil {
		t.Fatalf("next register: %v", err)
	}
	handler.ReplaceWith(next)

	if handler.OwnerFor("owned") != "plugin-a" {
		t.Fatalf("ownership not preserved across ReplaceWith: %q", handler.OwnerFor("owned"))
	}
	if handler.CurrentGeneration("owned") == generation {
		t.Fatal("expected new generation after ReplaceWith")
	}
	removed := handler.UnregisterOwner("plugin-a")
	if len(removed) != 1 || removed[0] != "owned" {
		t.Fatalf("expected [owned] removed after ReplaceWith, got %v", removed)
	}
}

func TestOwnedInterceptorsRegisteredAndReversed(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool{name: "bash"}, ToolMeta{AlwaysActive: true})

	beforeDispose := handler.AddBeforeInterceptorsOwned("plugin-x", func(ctx context.Context, call *ToolCall) (*ToolResult, error) {
		return nil, nil
	})
	afterDispose := handler.AddAfterInterceptorsOwned("plugin-x", func(ctx context.Context, call *ToolCall, result ToolResult, err error) (ToolResult, error) {
		return result, err
	})
	// Effective slices include the owned interceptors.
	handler.mu.RLock()
	beforeCount := len(handler.before)
	afterCount := len(handler.after)
	handler.mu.RUnlock()
	if beforeCount != 1 || afterCount != 1 {
		t.Fatalf("expected 1 owned before + 1 owned after, got %d/%d", beforeCount, afterCount)
	}

	beforeDispose()
	afterDispose()
	handler.mu.RLock()
	beforeCount = len(handler.before)
	afterCount = len(handler.after)
	handler.mu.RUnlock()
	if beforeCount != 0 || afterCount != 0 {
		t.Fatalf("expected owned interceptors reversed, got %d/%d", beforeCount, afterCount)
	}
	// Idempotent.
	beforeDispose()
}

func TestUnregisterOwnerInterceptors(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool{name: "bash"}, ToolMeta{AlwaysActive: true})
	handler.AddBeforeInterceptorsOwned("plugin-a", func(ctx context.Context, call *ToolCall) (*ToolResult, error) {
		return nil, nil
	})
	handler.AddAfterInterceptorsOwned("plugin-a", func(ctx context.Context, call *ToolCall, result ToolResult, err error) (ToolResult, error) {
		return result, err
	})
	handler.AddBeforeInterceptorsOwned("plugin-b", func(ctx context.Context, call *ToolCall) (*ToolResult, error) {
		return nil, nil
	})
	handler.AddBeforeInterceptors(func(ctx context.Context, call *ToolCall) (*ToolResult, error) {
		return nil, nil
	})

	handler.UnregisterOwnerInterceptors("plugin-a")
	handler.mu.RLock()
	beforeCount := len(handler.before)
	handler.mu.RUnlock()
	// base interceptor + plugin-b owned = 2.
	if beforeCount != 2 {
		t.Fatalf("expected 2 before interceptors after unregistering plugin-a, got %d", beforeCount)
	}
	handler.UnregisterOwnerInterceptors("plugin-b")
	handler.mu.RLock()
	beforeCount = len(handler.before)
	afterCount := len(handler.after)
	handler.mu.RUnlock()
	if beforeCount != 1 || afterCount != 0 {
		t.Fatalf("expected base before only, got %d/%d", beforeCount, afterCount)
	}
}

func TestOwnedInterceptorBeforeShortCircuit(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool{name: "bash"}, ToolMeta{AlwaysActive: true})
	handler.AddBeforeInterceptorsOwned("policy", func(ctx context.Context, call *ToolCall) (*ToolResult, error) {
		if call.Name == "bash" {
			return &ToolResult{Text: "blocked by policy"}, nil
		}
		return nil, nil
	})
	result, err := handler.HandleResult(t.Context(), "bash", map[string]interface{}{})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if result.Text != "blocked by policy" {
		t.Fatalf("expected policy short-circuit, got %q", result.Text)
	}
}
