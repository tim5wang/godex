package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
	"github.com/tim5wang/godex/internal/pluginrt"
	"github.com/tim5wang/godex/internal/tools"
	"github.com/tim5wang/godex/internal/wasmrt"
)

// DeactivateInstalledPackageRuntimes removes only plugins backed by the package
// registry. It is used before replacing the ToolHandler during config reload.
// A failure to stop one runtime does not block the others; the tracking entry
// is dropped either way so the subsequent activation reconcile re-activates any
// still-active instance onto the current handler (same-id replacement).
func (a *Agent) DeactivateInstalledPackageRuntimes(ctx context.Context) error {
	if a == nil || a.cfg == nil || a.pluginMgr == nil {
		return nil
	}
	a.pluginRuntimeMu.Lock()
	defer a.pluginRuntimeMu.Unlock()
	var errs []error
	for id := range a.packageRuntimeIDs {
		if err := a.pluginMgr.Deactivate(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("deactivate package runtime %s: %w", id, err))
		}
		delete(a.packageRuntimeIDs, id)
	}
	return errors.Join(errs...)
}

// ActivateInstalledPackageRuntimes reconciles installed runtime declarations
// into this agent's plugin manager. It is safe to call at startup and after a
// package mutation; runtime-free packages are ignored.
func (a *Agent) ActivateInstalledPackageRuntimes(ctx context.Context) error {
	if a == nil || a.cfg == nil || a.pluginMgr == nil || a.toolHandler == nil {
		return nil
	}
	a.pluginRuntimeMu.Lock()
	defer a.pluginRuntimeMu.Unlock()

	packages := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir)
	items, err := packages.List()
	if err != nil {
		return err
	}
	wanted := make(map[string]pkgregistry.Entry)
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.Runtime.Kind), "wasm") {
			wanted[item.Name] = item
		}
	}
	for id := range a.packageRuntimeIDs {
		if _, ok := wanted[id]; !ok {
			if err := a.pluginMgr.Deactivate(ctx, id); err != nil {
				return err
			}
			delete(a.packageRuntimeIDs, id)
		}
	}

	// Dependency providers may appear after consumers in the registry. Retry
	// deferred activations until a full pass makes no progress; the package
	// registry has already validated the graph, so a remaining error is a real
	// load/start failure rather than an ordering issue.
	pending := make(map[string]pkgregistry.Entry)
	for name, item := range wanted {
		if current := a.pluginMgr.Get(name); current == nil || current.Manifest().Version != packageRuntimeVersion(item) {
			pending[name] = item
		}
	}
	var lastErr error
	for len(pending) > 0 {
		progress := false
		for name, item := range pending {
			if err := a.activatePackageRuntime(ctx, packages, item); err != nil {
				lastErr = fmt.Errorf("activate package runtime %s: %w", item.Name, err)
				continue
			}
			delete(pending, name)
			a.packageRuntimeIDs[name] = struct{}{}
			progress = true
		}
		if !progress {
			return lastErr
		}
	}
	return nil
}

func (a *Agent) activatePackageRuntime(ctx context.Context, packages *pkgregistry.Manager, item pkgregistry.Entry) error {
	modulePath := packages.RuntimeModulePath(item)
	if modulePath == "" {
		return fmt.Errorf("invalid runtime module path")
	}
	binary, err := os.ReadFile(modulePath)
	if err != nil {
		return err
	}
	manifest := pluginrt.Manifest{
		ID:       item.Name,
		Version:  packageRuntimeVersion(item),
		Scope:    scope.Org("godex"),
		Requires: packageRuntimeRequires(item),
		Provides: packageRuntimeProvides(item),
	}
	credentials := pluginrt.NewCredentialBroker(os.LookupEnv, map[string][]string{
		item.Name: packageCredentialPermissions(item.Permissions),
	})
	config := wasmrt.Config{Host: wasmrt.HostCallbacks{
		Log: func(message string) {
			fmt.Fprintf(os.Stderr, "[%s] %s\n", item.Name, message)
		},
	}}
	if hasPackagePermission(item.Permissions, "network") {
		config.Host.HTTPGet = a.pluginHTTPGet()
	}
	if hasPackagePermission(item.Permissions, "filesystem") || hasPackagePermission(item.Permissions, "read_file") {
		config.Host.WorkspaceRead = func(relative string) (string, error) {
			workspace, err := workspacefs.New(a.SandboxBinding().WorkspaceDir)
			if err != nil {
				return "", err
			}
			defer workspace.Close()
			data, err := workspace.ReadFile(relative)
			return string(data), err
		}
	}
	var kv *pluginrt.PluginKVBroker
	if hasPackagePermission(item.Permissions, "memory") {
		kv = a.pluginKV
	}
	_, err = a.pluginMgr.Activate(ctx, &pluginrt.WasmToolPlugin{
		ManifestValue: manifest,
		Binary:        binary,
		Handler:       a.toolHandler,
		Meta: tools.ToolMeta{
			Bundle:        "wasm",
			Summary:       "tools supplied by installed WASM packages",
			AlwaysActive:  true,
			DefaultActive: true,
		},
		WasmConfig:  config,
		KV:          kv,
		Credentials: credentials,
	})
	return err
}

func packageRuntimeVersion(item pkgregistry.Entry) string {
	if strings.TrimSpace(item.Digest) == "" {
		return item.Version
	}
	return item.Version + "+" + item.Digest
}

func packageRuntimeRequires(item pkgregistry.Entry) []string {
	var required []string
	for _, raw := range item.Requires {
		requirement, err := pkgregistry.ParseRequirement(raw)
		if err == nil && requirement.Kind == "capability" {
			required = append(required, requirement.Raw)
		}
	}
	return required
}

func packageRuntimeProvides(item pkgregistry.Entry) []string {
	provided := append([]string{}, item.Provides...)
	if len(provided) == 0 {
		provided = append(provided, "package:"+item.Name+"@1")
	}
	return provided
}

func packageCredentialPermissions(permissions []string) []string {
	var out []string
	for _, permission := range permissions {
		if name, ok := strings.CutPrefix(strings.TrimSpace(permission), "credential:"); ok && strings.TrimSpace(name) != "" {
			out = append(out, strings.TrimSpace(name))
		}
	}
	return out
}

func hasPackagePermission(permissions []string, name string) bool {
	for _, permission := range permissions {
		if strings.EqualFold(strings.TrimSpace(permission), name) {
			return true
		}
	}
	return false
}
