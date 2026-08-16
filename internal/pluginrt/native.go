package pluginrt

import (
	"context"

	"github.com/tim5wang/godex/internal/toolruntime"
)

// ToolContributor is a builtin Go component that contributes tools to the
// session tool handler when its plugin activates, and reverses the
// registration (via the P1 disposer) when it deactivates.
type ToolContributor interface {
	// Tools returns the tools this plugin contributes. It is called once per
	// activation, so tools may be constructed fresh per generation.
	Tools() []toolruntime.Tool
	// Meta returns the registration metadata applied to every contributed
	// tool (bundle, summary, activation).
	Meta() toolruntime.ToolMeta
}

// NativeToolPlugin adapts a ToolContributor into a NativePlugin: on Start it
// registers every contributed tool on the handler through RegisterOwned
// (recording the plugin id as owner); on Stop the ledger reverses them.
type NativeToolPlugin struct {
	ManifestValue Manifest
	Contributor   ToolContributor
	Handler       *toolruntime.ToolHandler
}

// Manifest returns the declared manifest.
func (p *NativeToolPlugin) Manifest() Manifest { return p.ManifestValue }

// Start registers contributed tools with owner = plugin id.
func (p *NativeToolPlugin) Start(ctx context.Context, host Host) error {
	if p.Contributor == nil || p.Handler == nil {
		return nil
	}
	owner := p.ManifestValue.ID
	for _, tool := range p.Contributor.Tools() {
		tool := tool
		meta := p.Contributor.Meta()
		host.RegisterEffect(func(ctx context.Context) (func() error, error) {
			registration, err := p.Handler.RegisterOwned(owner, tool, meta)
			if err != nil {
				return nil, err
			}
			dispose := registration.Dispose
			return func() error { dispose(); return nil }, nil
		})
	}
	return nil
}

// Stop is a no-op; effect reversal happens in the kernel ledger.
func (p *NativeToolPlugin) Stop(ctx context.Context) error { return nil }
