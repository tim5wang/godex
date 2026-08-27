package pluginrt

import (
	"github.com/tim5wang/godex/internal/core/config"
)

// Services is the platform service face injected into the plugin kernel at
// assembly time (P-C, the godex analog of Cordis ctx.inject). Plugins consume
// it via Host.Services; each field is granted implicitly by the platform —
// sensitive surfaces (credentials, exec) stay out until a permission model
// exists in the manifest.
type Services struct {
	// WorkspaceDir is the workspace root (project root for the default project).
	WorkspaceDir string
	// StateDir holds durable plugin state (ledgers, side files).
	StateDir string
	// TempDir is the scratch directory.
	TempDir string
	// Config returns a config snapshot; the getter must be safe for
	// concurrent use (config.Manager.Current style).
	Config func() *config.Config
}
