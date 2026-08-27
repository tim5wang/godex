package pluginrt

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
)

// Host is what the kernel hands to a plugin so it can register capabilities
// and effects (tools, interceptors, services, listeners). It is the GoDex
// analog of a DSH Context.
type Host interface {
	// RegisterEffect records a reversible effect for the current instance.
	RegisterEffect(effect Effect)
	// Provide records a capability this instance provides (already declared
	// in its manifest; kept here for runtime confirmation/audit).
	Provide(capabilityName string)
	// Logger returns an optional per-instance logger prefix hook (no-op by
	// default).
	Logger(pluginID string) func(format string, args ...any)
	// RegisterRoutes mounts HTTP routes under prefix for this plugin. The
	// registration is reversible: deactivation unmounts the prefix (requests
	// fall through to 404). The register callback receives a dedicated mux;
	// patterns use the full request path (same style as the httpapi mux).
	RegisterRoutes(prefix string, register func(mux *http.ServeMux)) error
	// Services returns the platform services injected at manager assembly
	// (workspace/state/temp dirs and a config snapshot getter).
	Services() Services
	// RegisterSchedule registers a recurring callback (cron expression or
	// fixed interval, exactly one of the two). Reversible: deactivation
	// removes the schedule.
	RegisterSchedule(name string, spec ScheduleSpec, fn func(ctx context.Context)) error
}

// NativePlugin is a builtin Go plugin: it declares its manifest and provides
// start/stop hooks that run within the kernel lifecycle.
type NativePlugin interface {
	Manifest() Manifest
	// Start runs once during activation; it typically registers effects via
	// the Host and returns an optional long-running cleanup.
	Start(ctx context.Context, host Host) error
	// Stop runs during teardown before effects are reverted.
	Stop(ctx context.Context) error
}

// Instance is one activated plugin instance.
type Instance struct {
	manifest   Manifest
	plugin     NativePlugin
	state      atomic.Int32 // State
	generation uint64
	ledger     *Ledger
	mu         sync.Mutex
	host       Host
	started    bool
	// manager is the owning kernel; it hosts the route table and scheduler
	// shared by all instances. Injected at Activate.
	manager *Manager
}

// ID returns the plugin id.
func (i *Instance) ID() string { return i.manifest.ID }

// Scope returns the instance scope.
func (i *Instance) Scope() string { return string(i.manifest.Scope) }

// Manifest returns the declared manifest.
func (i *Instance) Manifest() Manifest { return i.manifest }

// State returns the current lifecycle phase.
func (i *Instance) State() State { return State(i.state.Load()) }

// Generation returns the registration generation (bumped on each re-activation).
func (i *Instance) Generation() uint64 { return i.generation }

// Requires returns the declared capability requirements.
func (i *Instance) Requires() []string { return append([]string{}, i.manifest.Requires...) }

// Provides returns the declared capabilities.
func (i *Instance) Provides() []string { return append([]string{}, i.manifest.Provides...) }

// Start activates the instance: PENDING -> LOADING -> ACTIVE. Effects are run
// through the ledger, so a later Stop reverts them in reverse order.
func (i *Instance) Start(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.started {
		return nil
	}
	i.state.Store(int32(StateLoading))
	host := newHost(i)
	i.host = host
	if i.plugin != nil {
		if err := i.plugin.Start(ctx, host); err != nil {
			i.state.Store(int32(StateFailed))
			return err
		}
	}
	if err := i.ledger.Run(ctx); err != nil {
		i.state.Store(int32(StateFailed))
		return err
	}
	i.started = true
	i.state.Store(int32(StateActive))
	return nil
}

// Stop tears down the instance: ACTIVE -> UNLOADING -> DISPOSED. It runs the
// plugin Stop hook and then reverses all effects in reverse order.
func (i *Instance) Stop(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.started {
		return nil
	}
	i.state.Store(int32(StateUnloading))
	if i.plugin != nil {
		_ = i.plugin.Stop(ctx)
	}
	err := i.ledger.Revert()
	i.started = false
	i.host = nil
	i.state.Store(int32(StateDisposed))
	return err
}

// IsActive reports whether the instance is currently serving.
func (i *Instance) IsActive() bool {
	return i.State() == StateActive
}

// host is the concrete Host handed to a plugin instance.
type host struct {
	instance *Instance
}

func newHost(instance *Instance) Host { return &host{instance: instance} }

func (h *host) RegisterEffect(effect Effect) { h.instance.ledger.Add(effect) }

func (h *host) Provide(capabilityName string) {
	// The capability is already declared in the manifest; runtime
	// confirmation is a no-op ledger entry kept for audit symmetry.
	_ = capabilityName
}

func (h *host) Logger(pluginID string) func(format string, args ...any) {
	return func(format string, args ...any) {}
}

func (h *host) RegisterRoutes(prefix string, register func(mux *http.ServeMux)) error {
	m := h.instance.manager
	if err := m.registerPluginRoutes(h.instance.manifest.ID, prefix, register); err != nil {
		return err
	}
	// Reversal rides the standard effect ledger: unmount on deactivation.
	h.instance.ledger.Add(func(ctx context.Context) (func() error, error) {
		return func() error { m.removePluginRoutes(h.instance.manifest.ID, prefix); return nil }, nil
	})
	return nil
}

func (h *host) Services() Services { return h.instance.manager.services }

func (h *host) RegisterSchedule(name string, spec ScheduleSpec, fn func(ctx context.Context)) error {
	m := h.instance.manager
	if err := m.registerSchedule(h.instance.manifest.ID, name, spec, fn); err != nil {
		return err
	}
	h.instance.ledger.Add(func(ctx context.Context) (func() error, error) {
		return func() error { m.removeSchedules(h.instance.manifest.ID); return nil }, nil
	})
	return nil
}
