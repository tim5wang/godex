package toolruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/platform/stringutil"
)

type fakeTool struct {
	name string
}

func (t fakeTool) Name() string { return t.name }

func (t fakeTool) Description() string { return t.name + " description" }

func (t fakeTool) Spec() ToolSpec {
	return NewToolSpec(t.name, t.Description(), map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil)
}

func (t fakeTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	return executeToolRuntime(ctx, t, args, nil, nil)
}

func (t fakeTool) prepare(raw map[string]interface{}, sessionCtx automation.SessionContext) (ToolCall, error) {
	return ToolCall{
		Name:            t.name,
		RawInput:        cloneStringAnyMap(raw),
		NormalizedInput: cloneStringAnyMap(raw),
		SessionContext:  sessionCtx.Clone(),
	}, nil
}

func (t fakeTool) refresh(call *ToolCall) error {
	_ = call
	return nil
}

func (t fakeTool) invoke(ctx context.Context, call *ToolCall) (ToolResult, error) {
	_ = ctx
	_ = call
	return ToolResult{Text: t.name + " ok"}, nil
}

func TestToolHandlerActivateDefaults(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool{name: "bash"}, ToolMeta{
		Bundle:        "core_code",
		Summary:       "core tools",
		DefaultActive: true,
	})
	handler.RegisterWithMeta(fakeTool{name: "background_run"}, ToolMeta{
		Bundle:  "background",
		Summary: "background tools",
	})
	handler.RegisterWithMeta(fakeTool{name: "compress"}, ToolMeta{AlwaysActive: true})

	handler.ActivateDefaults()

	if !handler.IsActive("bash") {
		t.Fatal("expected default-active tool to be active")
	}
	if handler.IsActive("background_run") {
		t.Fatal("expected non-default tool to remain inactive")
	}
	if !handler.IsActive("compress") {
		t.Fatal("expected always-active tool to be active")
	}
}

func TestToolHandlerResetActiveToolsToDefaultsDropsTransientBundles(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool{name: "bash"}, ToolMeta{
		Bundle:        "core_code",
		Summary:       "core tools",
		DefaultActive: true,
	})
	handler.RegisterWithMeta(fakeTool{name: "background_run"}, ToolMeta{
		Bundle:  "background",
		Summary: "background tools",
	})
	handler.RegisterWithMeta(fakeTool{name: "compress"}, ToolMeta{AlwaysActive: true})
	handler.ActivateDefaults()
	handler.ActivateBundles("background")

	handler.ResetActiveToolsToDefaults()

	if !handler.IsActive("bash") {
		t.Fatal("expected default-active tool to be active after reset")
	}
	if !handler.IsActive("compress") {
		t.Fatal("expected always-active tool to be active after reset")
	}
	if handler.IsActive("background_run") {
		t.Fatal("expected transient bundle tool to be inactive after reset")
	}
}

func TestToolHandlerRejectsInactiveTool(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool{name: "background_run"}, ToolMeta{
		Bundle:  "background",
		Summary: "background tools",
	})

	_, err := handler.Handle(context.Background(), "background_run", map[string]interface{}{})
	var inactive ErrToolInactive
	if !errors.As(err, &inactive) {
		t.Fatalf("expected inactive tool error, got %v", err)
	}
	if inactive.Bundle != "background" {
		t.Fatalf("expected inactive bundle %q, got %q", "background", inactive.Bundle)
	}
}

func TestToolHandlerCannotDeactivateAlwaysActiveBundles(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool{name: "compress"}, ToolMeta{
		Bundle:       "ops",
		Summary:      "ops tools",
		AlwaysActive: true,
	})
	handler.ActivateDefaults()

	changed, blocked := handler.DeactivateBundles("ops")
	if len(changed) != 0 {
		t.Fatalf("expected no changed bundles, got %v", changed)
	}
	if len(blocked) != 1 || blocked[0] != "ops" {
		t.Fatalf("expected blocked bundle %q, got %v", "ops", blocked)
	}
	if !handler.IsActive("compress") {
		t.Fatal("expected always-active tool to remain active")
	}
}

func TestRemoveStringDoesNotMutateInputSlice(t *testing.T) {
	items := []string{"core", "background", "team"}
	alias := append([]string{}, items...)

	filtered := stringutil.Remove(items, "background")

	if !reflect.DeepEqual(items, alias) {
		t.Fatalf("expected original slice to stay unchanged, got %v", items)
	}
	expected := []string{"core", "team"}
	if !reflect.DeepEqual(filtered, expected) {
		t.Fatalf("expected filtered slice %v, got %v", expected, filtered)
	}
}
