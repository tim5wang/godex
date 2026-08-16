package pluginrt

import (
	"context"

	"github.com/tim5wang/godex/internal/tools"
	"github.com/tim5wang/godex/internal/toolruntime"
	"github.com/tim5wang/godex/internal/wasmrt"
)

// WasmToolPlugin is a pluginrt native adapter for a WASM tool plugin: on Start
// it loads the .wasm module (wazero), discovers its tools, and registers each
// as an owned toolruntime.Tool on the handler; on Stop the ledger reverses the
// registrations and the module is closed. This is the 阶段 B MVP wiring —
// WASM plugins become first-class pluginrt instances sharing the capability
// registry and permission/scope model.
type WasmToolPlugin struct {
	// ManifestValue is the plugin declaration (id/scope/requires/provides).
	ManifestValue Manifest
	// Binary is the compiled .wasm module.
	Binary []byte
	// Handler receives the discovered tools.
	Handler *toolruntime.ToolHandler
	// Runner maps plugin ids to loaded wasm runtimes; when nil, this plugin
	// registers tools bound to itself (single-plugin holder).
	Runner tools.WasmPluginRunner
	// Meta is the registration metadata applied to every discovered tool.
	Meta toolruntime.ToolMeta
	// WasmConfig optionally overrides wasmrt defaults (timeout, memory, host
	// callbacks). Callbacks are wired per activation.
	WasmConfig wasmrt.Config

	runtime     *wasmrt.Plugin
	promptSects []wasmrt.PromptSection
}

// Manifest returns the declared manifest.
func (p *WasmToolPlugin) Manifest() Manifest { return p.ManifestValue }

// Start loads the module, discovers tools, and registers each with owner =
// plugin id through reversible effects. Prompt/context contributions from the
// module are cached for the host to inject (P4 prompt contributor).
func (p *WasmToolPlugin) Start(ctx context.Context, host Host) error {
	if p.Handler == nil || len(p.Binary) == 0 {
		return nil
	}
	config := p.WasmConfig
	config.Binary = p.Binary
	loaded, err := wasmrt.NewPlugin(ctx, config)
	if err != nil {
		return err
	}
	p.runtime = loaded

	decls, err := loaded.ToolsList(ctx)
	if err != nil {
		_ = loaded.Close(ctx)
		p.runtime = nil
		return err
	}
	if sections, sectionsErr := loaded.PromptSections(ctx); sectionsErr == nil {
		p.promptSects = sections
	}

	owner := p.ManifestValue.ID
	runner := p.Runner
	if runner == nil {
		runner = &singleWasmRunner{plugin: loaded, id: owner}
	}
	meta := p.Meta
	closed := false
	for _, decl := range decls {
		decl := decl
		host.RegisterEffect(func(ctx context.Context) (func() error, error) {
			tool, err := tools.NewWasmCallTool(runner, decl, tools.WasmToolOptions{PluginID: owner})
			if err != nil {
				return nil, err
			}
			registration, err := p.Handler.RegisterOwned(owner, tool, meta)
			if err != nil {
				return nil, err
			}
			dispose := registration.Dispose
			return func() error {
				dispose()
				if !closed {
					closed = true
					return loaded.Close(ctx)
				}
				return nil
			}, nil
		})
	}
	return nil
}

// PromptSections returns the cached context contributions of this plugin
// (P4 prompt/context contributor). Empty when the module has none.
func (p *WasmToolPlugin) PromptSections() []PromptSection {
	out := make([]PromptSection, 0, len(p.promptSects))
	for _, section := range p.promptSects {
		out = append(out, PromptSection{
			Key:  section.Key,
			Kind: section.Kind,
			Text: section.Text,
		})
	}
	return out
}

// Stop closes the loaded module (registration reversal happens in the ledger).
func (p *WasmToolPlugin) Stop(ctx context.Context) error {
	if p.runtime != nil {
		err := p.runtime.Close(ctx)
		p.runtime = nil
		p.promptSects = nil
		return err
	}
	return nil
}

// singleWasmRunner serves tools bound to one plugin instance.
type singleWasmRunner struct {
	plugin *wasmrt.Plugin
	id     string
}

func (r *singleWasmRunner) WasmPlugin(id string) *wasmrt.Plugin {
	if id == r.id {
		return r.plugin
	}
	return nil
}
