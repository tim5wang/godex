package pluginrt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/toolruntime"
)

type simplePlugin struct {
	manifest Manifest
	started  atomic.Int32
	stopped  atomic.Int32
	startErr error
}

func (p *simplePlugin) Manifest() Manifest { return p.manifest }
func (p *simplePlugin) Start(ctx context.Context, host Host) error {
	p.started.Add(1)
	return p.startErr
}
func (p *simplePlugin) Stop(ctx context.Context) error { p.stopped.Add(1); return nil }

func TestManifestValidation(t *testing.T) {
	tests := []struct {
		name    string
		manifest Manifest
		wantErr bool
	}{
		{name: "ok", manifest: Manifest{ID: "a", Scope: scope.Org("godex"), Requires: []string{"godex:log@1"}, Provides: []string{"godex:tool@1"}}},
		{name: "missing id", manifest: Manifest{Scope: scope.Org("godex")}, wantErr: true},
		{name: "missing scope", manifest: Manifest{ID: "a"}, wantErr: true},
		{name: "bad scope", manifest: Manifest{ID: "a", Scope: "team:x"}, wantErr: true},
		{name: "bad capability", manifest: Manifest{ID: "a", Scope: scope.Org("godex"), Requires: []string{"nocolon"}}, wantErr: true},
		{name: "bad major", manifest: Manifest{ID: "a", Scope: scope.Org("godex"), Requires: []string{"godex:log@x"}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.manifest.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGraphMissingAndConflict(t *testing.T) {
	graph := NewGraph(nil)
	candidate := Manifest{ID: "app", Scope: scope.Org("godex"), Requires: []string{"godex:log@1", "godex:missing@1"}}
	installed := []Manifest{{ID: "logger", Scope: scope.Org("godex"), Provides: []string{"godex:log@1"}}}
	report := graph.Validate(candidate, installed)
	if len(report.Missing) != 1 || !strings.Contains(report.Missing[0], "godex:missing@1") {
		t.Fatalf("expected 1 missing (godex:missing@1), got %v", report.Missing)
	}
	if len(report.Conflicts) != 0 {
		t.Fatalf("did not expect conflicts, got %v", report.Conflicts)
	}

	// Version conflict: requires @1 but only @2 provided.
	conflict := graph.Validate(
		Manifest{ID: "app", Scope: scope.Org("godex"), Requires: []string{"godex:log@1"}},
		[]Manifest{{ID: "logger", Scope: scope.Org("godex"), Provides: []string{"godex:log@2"}}},
	)
	if len(conflict.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %v", conflict.Conflicts)
	}
	if len(conflict.Missing) != 0 {
		t.Fatalf("did not expect missing, got %v", conflict.Missing)
	}
}

func TestGraphPlatformCapabilities(t *testing.T) {
	platform := func(name string) bool { return strings.HasPrefix(name, "tool:") }
	graph := NewGraph(platform)
	candidate := Manifest{ID: "app", Scope: scope.Org("godex"), Requires: []string{"tool:read_file", "godex:custom@1"}}
	report := graph.Validate(candidate, []Manifest{{ID: "custom", Scope: scope.Org("godex"), Provides: []string{"godex:custom@1"}}})
	if !report.Empty() {
		t.Fatalf("expected empty report (platform tool + plugin custom), got %s", report.Error())
	}
}

func TestGraphCycleDetection(t *testing.T) {
	graph := NewGraph(nil)
	// a requires godex:a-cap, provided by b; b requires godex:b-cap, provided by a.
	installed := []Manifest{
		{ID: "a", Scope: scope.Org("godex"), Provides: []string{"godex:b-cap@1"}, Requires: []string{"godex:a-cap@1"}},
		{ID: "b", Scope: scope.Org("godex"), Provides: []string{"godex:a-cap@1"}, Requires: []string{"godex:b-cap@1"}},
	}
	report := graph.Validate(Manifest{ID: "c", Scope: scope.Org("godex")}, installed)
	if len(report.Cycles) == 0 {
		t.Fatalf("expected cycle detection, got none")
	}
	found := false
	for _, cycle := range report.Cycles {
		joined := strings.Join(cycle, "->")
		if strings.Contains(joined, "a") && strings.Contains(joined, "b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a<->b cycle, got %v", report.Cycles)
	}
}

func TestManagerActivateAndDeactivate(t *testing.T) {
	manager := NewManager(nil)
	plugin := &simplePlugin{manifest: Manifest{ID: "hello", Scope: scope.Org("godex"), Provides: []string{"godex:hello@1"}}}

	instance, err := manager.Activate(context.Background(), plugin)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if instance.State() != StateActive || instance.Generation() != 1 {
		t.Fatalf("unexpected instance state/gen: %s/%d", instance.State(), instance.Generation())
	}
	if plugin.started.Load() != 1 {
		t.Fatal("expected Start hook called once")
	}
	if !manager.Registry().Provided("org:godex", "godex:hello@1") {
		t.Fatal("expected capability recorded in registry")
	}
	if got := manager.Get("hello"); got == nil || got.ID() != "hello" {
		t.Fatal("expected instance retrievable by id")
	}

	if err := manager.Deactivate(context.Background(), "hello"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if plugin.stopped.Load() != 1 {
		t.Fatal("expected Stop hook called once")
	}
	if manager.Get("hello") != nil {
		t.Fatal("expected instance removed")
	}
	if manager.Registry().Provided("org:godex", "godex:hello@1") {
		t.Fatal("expected capability revoked")
	}
	// Idempotent.
	if err := manager.Deactivate(context.Background(), "hello"); err != nil {
		t.Fatalf("second deactivate should be no-op: %v", err)
	}
}

func TestManagerRejectsMissingDependency(t *testing.T) {
	manager := NewManager(nil)
	plugin := &simplePlugin{manifest: Manifest{ID: "app", Scope: scope.Org("godex"), Requires: []string{"godex:nope@1"}}}
	if _, err := manager.Activate(context.Background(), plugin); err == nil {
		t.Fatal("expected activation failure on missing dependency")
	} else if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(manager.List()) != 0 {
		t.Fatal("failed activation must not register an instance")
	}
}

func TestManagerFailedStartLeavesRegistryUntouched(t *testing.T) {
	manager := NewManager(nil)
	good := &simplePlugin{manifest: Manifest{ID: "good", Scope: scope.Org("godex"), Provides: []string{"godex:good@1"}}}
	if _, err := manager.Activate(context.Background(), good); err != nil {
		t.Fatalf("activate good: %v", err)
	}
	bad := &simplePlugin{manifest: Manifest{ID: "bad", Scope: scope.Org("godex")}, startErr: errBoom}
	if _, err := manager.Activate(context.Background(), bad); err == nil {
		t.Fatal("expected activation failure")
	}
	if manager.Get("bad") != nil {
		t.Fatal("failed plugin must not be registered")
	}
	if !manager.Registry().Provided("org:godex", "godex:good@1") {
		t.Fatal("good plugin capability must remain")
	}
}

func TestManagerReloadReplacesSameID(t *testing.T) {
	manager := NewManager(nil)
	first := &simplePlugin{manifest: Manifest{ID: "svc", Scope: scope.Org("godex"), Provides: []string{"godex:svc@1"}}}
	firstInstance, err := manager.Activate(context.Background(), first)
	if err != nil {
		t.Fatalf("activate first: %v", err)
	}
	second := &simplePlugin{manifest: Manifest{ID: "svc", Scope: scope.Org("godex"), Provides: []string{"godex:svc@2"}}}
	secondInstance, err := manager.Activate(context.Background(), second)
	if err != nil {
		t.Fatalf("activate second: %v", err)
	}
	if secondInstance.Generation() <= firstInstance.Generation() {
		t.Fatalf("expected generation bump: %d -> %d", firstInstance.Generation(), secondInstance.Generation())
	}
	if first.stopped.Load() != 1 {
		t.Fatal("expected prior instance stopped on reload")
	}
	if !manager.Registry().Provided("org:godex", "godex:svc@2") {
		t.Fatal("expected new capability recorded")
	}
	if manager.Registry().Provided("org:godex", "godex:svc@1") {
		t.Fatal("expected old capability revoked")
	}
}

func TestTransactionalPrepareRollback(t *testing.T) {
	manager := NewManager(nil)
	stable := &simplePlugin{manifest: Manifest{ID: "stable", Scope: scope.Org("godex"), Provides: []string{"godex:stable@1"}}}
	if _, err := manager.Activate(context.Background(), stable); err != nil {
		t.Fatalf("activate stable: %v", err)
	}

	// Prepare a broken candidate: must fail validation without touching live state.
	broken := &simplePlugin{manifest: Manifest{ID: "broken", Scope: scope.Org("godex"), Requires: []string{"godex:none@1"}}}
	if _, err := manager.Prepare(context.Background(), broken); err == nil {
		t.Fatal("expected prepare failure for broken candidate")
	}
	if manager.Get("broken") != nil {
		t.Fatal("broken prepare must not register anything")
	}
	if !manager.Registry().Provided("org:godex", "godex:stable@1") {
		t.Fatal("stable registry must be untouched after failed prepare")
	}

	// Prepare a valid replacement of stable, then roll back: nothing changes.
	tx, err := manager.Prepare(context.Background(), &simplePlugin{manifest: Manifest{ID: "stable", Scope: scope.Org("godex"), Provides: []string{"godex:stable@2"}}})
	if err != nil {
		t.Fatalf("prepare valid replacement: %v", err)
	}
	tx.Rollback()
	if manager.Get("stable").Manifest().Provides[0] != "godex:stable@1" {
		t.Fatal("rollback must leave original instance in place")
	}
	if manager.Registry().Provided("org:godex", "godex:stable@1") {
		// Both versions recorded under the same scope; original still provided.
		if manager.Registry().MatchCapability("org:godex", "godex:stable@1") == "" {
			t.Fatal("original capability should still match after rollback")
		}
	}
}

func TestTransactionCommitAfterRollbackIsNoOp(t *testing.T) {
	manager := NewManager(nil)
	stable := &simplePlugin{manifest: Manifest{ID: "stable", Scope: scope.Org("godex"), Provides: []string{"godex:stable@1"}}}
	if _, err := manager.Activate(context.Background(), stable); err != nil {
		t.Fatalf("activate: %v", err)
	}
	tx, err := manager.Prepare(context.Background(), &simplePlugin{manifest: Manifest{ID: "stable", Scope: scope.Org("godex"), Provides: []string{"godex:stable@9"}}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	tx.Rollback()
	if _, err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit after rollback must be no-op: %v", err)
	}
	if got := manager.Get("stable").Manifest().Provides[0]; got != "godex:stable@1" {
		t.Fatalf("expected original after rolled-back commit, got %v", got)
	}
}

func TestNativeToolPluginRegistersAndUnregistersTools(t *testing.T) {
	handler := toolruntime.NewToolHandler()
	contributor := &fakeContributor{tools: []toolruntime.Tool{newFakeTool("plugin_tool")}}
	plugin := &NativeToolPlugin{
		ManifestValue: Manifest{ID: "tools", Scope: scope.Org("godex"), Provides: []string{"godex:tools@1"}},
		Contributor:   contributor,
		Handler:       handler,
	}
	manager := NewManager(nil)
	if _, err := manager.Activate(context.Background(), plugin); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if handler.Get("plugin_tool") == nil {
		t.Fatal("expected plugin tool registered")
	}
	if handler.OwnerFor("plugin_tool") != "tools" {
		t.Fatalf("expected owner recorded as plugin id, got %q", handler.OwnerFor("plugin_tool"))
	}

	if err := manager.Deactivate(context.Background(), "tools"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if handler.Get("plugin_tool") != nil {
		t.Fatal("expected plugin tool unregistered on deactivate")
	}
	if handler.OwnerFor("plugin_tool") != "" {
		t.Fatal("expected owner cleared")
	}
}

func TestNativeToolPluginRejectsConflictingOwner(t *testing.T) {
	handler := toolruntime.NewToolHandler()
	contributorA := &fakeContributor{tools: []toolruntime.Tool{newFakeTool("clash")}}
	contributorB := &fakeContributor{tools: []toolruntime.Tool{newFakeTool("clash")}}
	manager := NewManager(nil)
	if _, err := manager.Activate(context.Background(), &NativeToolPlugin{
		ManifestValue: Manifest{ID: "a", Scope: scope.Org("godex")},
		Contributor:   contributorA,
		Handler:       handler,
	}); err != nil {
		t.Fatalf("activate a: %v", err)
	}
	if _, err := manager.Activate(context.Background(), &NativeToolPlugin{
		ManifestValue: Manifest{ID: "b", Scope: scope.Org("godex")},
		Contributor:   contributorB,
		Handler:       handler,
	}); err == nil {
		t.Fatal("expected conflict error when two plugins own the same tool")
	}
	// a's registration survives the failed b activation.
	if handler.OwnerFor("clash") != "a" {
		t.Fatalf("expected owner a preserved, got %q", handler.OwnerFor("clash"))
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (e *boomError) Error() string { return "boom" }

type fakeContributor struct {
	tools []toolruntime.Tool
}

func (c *fakeContributor) Tools() []toolruntime.Tool { return c.tools }
func (c *fakeContributor) Meta() toolruntime.ToolMeta {
	return toolruntime.ToolMeta{Bundle: "plugin", AlwaysActive: true}
}

func newFakeTool(name string) toolruntime.Tool {
	return toolruntime.NewTypedTool(toolruntime.NewToolSpec(name, "fake", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args map[string]interface{}) (toolruntime.ToolResult, error) {
		return toolruntime.ToolResult{Text: name + " ok"}, nil
	})
}

func TestWasmToolPluginRegistersAndUnregisters(t *testing.T) {
	binary, err := os.ReadFile(filepath.Join("..", "wasmrt", "testdata", "plugin.wasm"))
	if err != nil {
		t.Fatalf("read wasm plugin: %v", err)
	}
	handler := toolruntime.NewToolHandler()
	plugin := &WasmToolPlugin{
		ManifestValue: Manifest{ID: "wasm-demo", Scope: scope.Org("godex"), Provides: []string{"godex:wasm-demo@1"}},
		Binary:        binary,
		Handler:       handler,
		Meta:          toolruntime.ToolMeta{Bundle: "wasm", AlwaysActive: true},
	}
	manager := NewManager(nil)
	if _, err := manager.Activate(context.Background(), plugin); err != nil {
		t.Fatalf("activate wasm plugin: %v", err)
	}
	if handler.Get("wasm_echo") == nil {
		t.Fatal("expected wasm tool registered")
	}
	if handler.OwnerFor("wasm_echo") != "wasm-demo" {
		t.Fatalf("expected wasm owner, got %q", handler.OwnerFor("wasm_echo"))
	}
	// The registered tool actually executes the wasm guest.
	result, err := handler.HandleResult(context.Background(), "wasm_echo", map[string]interface{}{"message": "via handler"})
	if err != nil {
		t.Fatalf("handle wasm tool: %v", err)
	}
	if !strings.Contains(result.Text, "wasm echo: via handler") {
		t.Fatalf("unexpected result: %q", result.Text)
	}

	if err := manager.Deactivate(context.Background(), "wasm-demo"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if handler.Get("wasm_echo") != nil {
		t.Fatal("expected wasm tool unregistered")
	}
}

func TestManagerPromptSectionsAggregatesWasmPlugins(t *testing.T) {
	binary, err := os.ReadFile(filepath.Join("..", "wasmrt", "testdata", "plugin.wasm"))
	if err != nil {
		t.Fatalf("read wasm plugin: %v", err)
	}
	handler := toolruntime.NewToolHandler()
	manager := NewManager(nil)
	if _, err := manager.Activate(context.Background(), &WasmToolPlugin{
		ManifestValue: Manifest{ID: "prompt-demo", Scope: scope.Org("godex"), Provides: []string{"godex:prompt-demo@1"}},
		Binary:        binary,
		Handler:       handler,
		Meta:          toolruntime.ToolMeta{Bundle: "wasm", AlwaysActive: true},
	}); err != nil {
		t.Fatalf("activate: %v", err)
	}

	sections := manager.PromptSections()
	if len(sections) != 1 {
		t.Fatalf("expected 1 prompt section, got %+v", sections)
	}
	if sections[0].PluginID != "prompt-demo" || sections[0].Key != "wasm_plugin_note" {
		t.Fatalf("unexpected section: %+v", sections[0])
	}
	if sections[0].Kind != "background" {
		t.Fatalf("expected background kind, got %q", sections[0].Kind)
	}

	// Deactivation clears the contributions.
	if err := manager.Deactivate(context.Background(), "prompt-demo"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if got := manager.PromptSections(); len(got) != 0 {
		t.Fatalf("expected no sections after deactivate, got %+v", got)
	}
}

func TestWasmToolPluginPolicyInterceptor(t *testing.T) {
	binary, err := os.ReadFile(filepath.Join("..", "wasmrt", "testdata", "plugin.wasm"))
	if err != nil {
		t.Fatalf("read wasm plugin: %v", err)
	}
	handler := toolruntime.NewToolHandler()
	plugin := &WasmToolPlugin{
		ManifestValue: Manifest{ID: "policy-demo", Scope: scope.Org("godex"), Provides: []string{"godex:policy-demo@1"}},
		Binary:        binary,
		Handler:       handler,
		Meta:          toolruntime.ToolMeta{Bundle: "wasm", AlwaysActive: true},
	}
	manager := NewManager(nil)
	if _, err := manager.Activate(context.Background(), plugin); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// Allowed tool executes normally.
	result, err := handler.HandleResult(context.Background(), "wasm_echo", map[string]interface{}{"message": "hello"})
	if err != nil {
		t.Fatalf("handle wasm_echo: %v", err)
	}
	if !strings.Contains(result.Text, "wasm echo: hello") {
		t.Fatalf("unexpected echo result: %q", result.Text)
	}

	// Denied tool is blocked by the plugin policy before execution.
	if _, err := handler.HandleResult(context.Background(), "wasm_secret", map[string]interface{}{}); err == nil {
		t.Fatal("expected wasm_secret to be denied by policy")
	} else if !strings.Contains(err.Error(), "wasm_secret is denied by plugin policy") {
		t.Fatalf("unexpected deny error: %v", err)
	}

	// Deactivation reverses the policy interceptor.
	if err := manager.Deactivate(context.Background(), "policy-demo"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if handler.Get("wasm_secret") != nil {
		t.Fatal("expected tools unregistered")
	}
}
