package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/pluginrt"
)

type packageRuntimeTestPlugin struct {
	manifest pluginrt.Manifest
}

func (p *packageRuntimeTestPlugin) Manifest() pluginrt.Manifest                { return p.manifest }
func (p *packageRuntimeTestPlugin) Start(context.Context, pluginrt.Host) error { return nil }
func (p *packageRuntimeTestPlugin) Stop(context.Context) error                 { return nil }

func TestPackageRuntimeReconcilePreservesNonPackagePlugin(t *testing.T) {
	a := newTestAgent(t, 4096)
	builtin := &packageRuntimeTestPlugin{manifest: pluginrt.Manifest{
		ID:      "builtin",
		Version: "1.0.0",
		Scope:   scope.Org("godex"),
	}}
	if _, err := a.pluginMgr.Activate(context.Background(), builtin); err != nil {
		t.Fatalf("activate builtin plugin: %v", err)
	}
	if err := a.ActivateInstalledPackageRuntimes(context.Background()); err != nil {
		t.Fatalf("reconcile package runtimes: %v", err)
	}
	if a.pluginMgr.Get("builtin") == nil {
		t.Fatal("package reconciliation deactivated a non-package plugin")
	}
}

func TestInstalledWasmPackageActivatesAndRemoves(t *testing.T) {
	a := newTestAgent(t, 4096)
	source := t.TempDir()
	binary, err := os.ReadFile(filepath.Join("..", "wasmrt", "testdata", "plugin.wasm"))
	if err != nil {
		t.Fatalf("read wasm fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.wasm"), binary, 0644); err != nil {
		t.Fatal(err)
	}
	manifest := "name: runtime-demo\nversion: 0.1.0\nprovides:\n  - godex:runtime-demo@1\nruntime:\n  kind: wasm\n  module: plugin.wasm\n"
	if err := os.WriteFile(filepath.Join(source, pkgregistry.ManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := a.InstallPackage(source); err != nil {
		t.Fatalf("install runtime package: %v", err)
	}
	if a.toolHandler.Get("wasm_echo") == nil {
		t.Fatal("installed WASM package did not register its tool")
	}
	if got := a.pluginMgr.Get("runtime-demo"); got == nil {
		t.Fatal("installed WASM package is not active in plugin manager")
	}
	result, err := a.toolHandler.HandleResult(context.Background(), "wasm_echo", map[string]interface{}{"message": "production"})
	if err != nil || result.Text == "" {
		t.Fatalf("execute activated tool: result=%+v err=%v", result, err)
	}

	if _, err := a.RemovePackage("runtime-demo"); err != nil {
		t.Fatalf("remove runtime package: %v", err)
	}
	if a.toolHandler.Get("wasm_echo") != nil || a.pluginMgr.Get("runtime-demo") != nil {
		t.Fatal("removed WASM package remained active")
	}
}

func TestInstalledWasmPackageReloadsWhenDigestChanges(t *testing.T) {
	a := newTestAgent(t, 4096)
	source := t.TempDir()
	binary, err := os.ReadFile(filepath.Join("..", "wasmrt", "testdata", "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.wasm"), binary, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, pkgregistry.ManifestFileName), []byte("name: reload-demo\nversion: 0.1.0\nruntime:\n  kind: wasm\n  module: plugin.wasm\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.InstallPackage(source); err != nil {
		t.Fatal(err)
	}
	first := a.pluginMgr.Get("reload-demo")
	if first == nil {
		t.Fatal("initial runtime missing")
	}
	if err := os.WriteFile(filepath.Join(source, "build.txt"), []byte("second build"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.InstallPackage(source); err != nil {
		t.Fatalf("reload runtime package: %v", err)
	}
	second := a.pluginMgr.Get("reload-demo")
	if second == nil || second.Generation() <= first.Generation() {
		t.Fatalf("runtime generation did not advance: first=%v second=%v", first.Generation(), second)
	}
	if a.toolHandler.Get("wasm_echo") == nil {
		t.Fatal("reloaded runtime lost its tool registration")
	}
}

func TestAgentStartupActivatesInstalledWasmPackages(t *testing.T) {
	a := newTestAgent(t, 4096)
	source := t.TempDir()
	binary, err := os.ReadFile(filepath.Join("..", "wasmrt", "testdata", "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.wasm"), binary, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, pkgregistry.ManifestFileName), []byte("name: boot-demo\nversion: 0.1.0\nruntime:\n  kind: wasm\n  module: plugin.wasm\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).Install(source); err != nil {
		t.Fatal(err)
	}

	restarted := New(a.cfg)
	restarted.RegisterTools()
	if restarted.toolHandler.Get("wasm_echo") == nil || restarted.pluginMgr.Get("boot-demo") == nil {
		t.Fatal("agent startup did not activate installed WASM package")
	}
}
