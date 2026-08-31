package agent

import (
	"context"
	"strings"
	"sync"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
)

// HarnessProfile describes a harness's static identity and capabilities.
//
// Mirrors the `HarnessProfile` surface of the QM reference
// (`temp/qm/src/harness/harness.ts`): each engine advertises which models and
// tools it can serve so the router can pick an engine per turn.
type HarnessProfile struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Models []string `json:"models"`
	Tools  []string `json:"tools"`
}

// HarnessTurnInput carries everything a harness needs to run one turn.
//
// It is the Go analogue of `HarnessTurnInput` in the QM reference, trimmed to
// the fields the current agent loop actually consumes (see RunOptions).
//
// P2 item 1: instead of reaching into the host Agent's internals, an engine
// receives a stable access surface via Messages (a snapshot provider) and
// WorkspaceDir. External engines must build their turn from these inputs.
type HarnessTurnInput struct {
	SessionID          string
	TurnID             string
	ActorID            string
	ActorKind          string
	EmitRunnerPhases   bool
	Sink               events.Sink
	RuntimeContext     automation.SessionContext
	Checkpoint         func()
	DrainInjections    func(context.Context, int) (conversation.InjectionDrain, error)
	OnInjectionDrained func(conversation.InjectionDrain)
	Model              string // optional per-turn model override
	// Harness is the engine requested for this turn (roadmap 6.4). Empty
	// means the default godex engine.
	Harness string
	// Messages returns a snapshot of the session transcript. The host sets it
	// so engines do not depend on the host's internal message store.
	Messages func() []protocol.Message
	// WorkspaceDir is the session workspace an engine may operate in.
	WorkspaceDir string
	// Scope is the sandbox scope the session is bound to (roadmap 6.2; empty
	// means the shared org layer). External engines may use it to scope their
	// own state or gate permissions (P2 #5).
	Scope scope.Id
	// UsageContext is the per-session usage ledger (optional; engines that do
	// not consume tokens may ignore it).
	UsageContext func(runtimeCtx automation.SessionContext, sessionID, turnID, jobID string) conversation.UsageContext
}

// HarnessTurnResult reports the outcome of one turn.
type HarnessTurnResult struct {
	Reply         string
	Completed     bool
	Stopped       bool
	HadInjections bool
	RecoveryHint  string
}

// Harness abstracts a runnable agent engine (turn loop + model utilities).
//
// The roadmap's Phase 5.1 asks for exactly this seam: `runTurn`,
// `resetSession`, `close`, `profile`, `models`, `tools`. The current
// godex engine is the default implementation; a harness router can switch
// between engines per session/turn.
type Harness interface {
	// Profile returns the engine's static capabilities.
	Profile() HarnessProfile
	// Models lists model IDs this engine can serve.
	Models() []string
	// Tools lists tool names this engine exposes.
	Tools() []string
	// RunTurn executes one turn against the given input.
	RunTurn(ctx context.Context, input HarnessTurnInput) (HarnessTurnResult, error)
	// ResetSession drops engine-side state for a session (called on switch).
	ResetSession(ctx context.Context, sessionID string) error
	// Close releases engine resources.
	Close() error
}

// godexHarness is the default Harness implementation: it wraps the existing
// Agent loop (RunWithOptions) so nothing about the current engine changes
// while the abstraction layer is introduced.
type godexHarness struct {
	agent *Agent
}

// NewGodexHarness wraps an Agent as the default godex Harness.
func NewGodexHarness(agent *Agent) Harness {
	return &godexHarness{agent: agent}
}

func (h *godexHarness) Profile() HarnessProfile {
	return HarnessProfile{
		ID:     "godex",
		Name:   "godex",
		Models: h.Models(),
		Tools:  h.Tools(),
	}
}

func (h *godexHarness) Models() []string {
	if h.agent == nil {
		return nil
	}
	model := h.agent.CurrentModel()
	if model == "" {
		return nil
	}
	return []string{model}
}

func (h *godexHarness) Tools() []string {
	if h.agent == nil {
		return nil
	}
	schemas := h.agent.activeToolSchemas("")
	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		if schema.Name != "" {
			names = append(names, schema.Name)
		}
	}
	return names
}

func (h *godexHarness) RunTurn(ctx context.Context, input HarnessTurnInput) (HarnessTurnResult, error) {
	if h.agent == nil {
		return HarnessTurnResult{}, nil
	}
	err := h.agent.RunWithOptions(ctx, RunOptions{
		SessionID:          input.SessionID,
		TurnID:             input.TurnID,
		ActorID:            input.ActorID,
		ActorKind:          input.ActorKind,
		EmitRunnerPhases:   input.EmitRunnerPhases,
		Sink:               input.Sink,
		RuntimeContext:     input.RuntimeContext,
		Checkpoint:         input.Checkpoint,
		DrainInjections:    input.DrainInjections,
		OnInjectionDrained: input.OnInjectionDrained,
	})
	if err != nil {
		return HarnessTurnResult{RecoveryHint: err.Error()}, err
	}
	return HarnessTurnResult{Completed: true}, nil
}

func (h *godexHarness) ResetSession(ctx context.Context, sessionID string) error {
	// The godex engine keeps no engine-side session state beyond the message
	// transcript, which is owned by the caller; nothing to drop here.
	return nil
}

func (h *godexHarness) Close() error { return nil }

// HarnessResolver picks a harness id for a turn input.
type HarnessResolver func(ctx context.Context, input HarnessTurnInput) (string, error)

// NewDefaultHarnessResolver always routes to the given harness id (used when
// no engine-switching configuration exists).
func NewDefaultHarnessResolver(id string) HarnessResolver {
	return func(context.Context, HarnessTurnInput) (string, error) { return id, nil }
}

// NewRequestedHarnessResolver routes to the harness named by input.Harness,
// falling back to the given default id when the request does not name one.
// It is the resolver behind per-turn engine switching (roadmap 6.4).
func NewRequestedHarnessResolver(defaultID string) HarnessResolver {
	return func(_ context.Context, input HarnessTurnInput) (string, error) {
		if strings.TrimSpace(input.Harness) == "" {
			return defaultID, nil
		}
		return strings.TrimSpace(input.Harness), nil
	}
}

// harnessRouter routes turns to registered harnesses and resets engine state
// when a session switches engines. Mirrors `createHarnessRouter` in the QM
// reference (`temp/qm/src/harness/harness-router.ts`).
//
// Unlike the QM reference and the original GoDex lazy build (which snapshotted
// adapters once via sync.Once), the registry is dynamic: engines can be
// registered after first use (research doc P2 item 3).
type harnessRouter struct {
	mu        sync.RWMutex
	adapters  map[string]Harness
	resolve   HarnessResolver
	sessionMu sync.Mutex
	last      map[string]string // sessionID -> harnessID
}

// NewHarnessRouter builds a router over the given adapters.
func NewHarnessRouter(adapters map[string]Harness, resolve HarnessResolver) Harness {
	if resolve == nil {
		resolve = NewDefaultHarnessResolver("godex")
	}
	clone := make(map[string]Harness, len(adapters))
	for id, adapter := range adapters {
		clone[id] = adapter
	}
	return &harnessRouter{adapters: clone, resolve: resolve, last: map[string]string{}}
}

// Register adds or replaces an engine at runtime. It is safe to call after
// the router is already serving turns; a new engine becomes available to the
// next RunTurn without rebuilding the router.
func (r *harnessRouter) Register(id string, harness Harness) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.adapters == nil {
		r.adapters = map[string]Harness{}
	}
	r.adapters[id] = harness
}

func (r *harnessRouter) adapter(id string) Harness {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.adapters == nil {
		return nil
	}
	return r.adapters[id]
}

func (r *harnessRouter) adaptersSnapshot() map[string]Harness {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Harness, len(r.adapters))
	for id, adapter := range r.adapters {
		out[id] = adapter
	}
	return out
}

func (r *harnessRouter) Profile() HarnessProfile {
	if adapter := r.adapter("godex"); adapter != nil {
		return adapter.Profile()
	}
	for _, adapter := range r.adaptersSnapshot() {
		return adapter.Profile()
	}
	return HarnessProfile{ID: "godex", Name: "godex"}
}

func (r *harnessRouter) Models() []string {
	if adapter := r.adapter("godex"); adapter != nil {
		return adapter.Models()
	}
	for _, adapter := range r.adaptersSnapshot() {
		return adapter.Models()
	}
	return nil
}

func (r *harnessRouter) Tools() []string {
	if adapter := r.adapter("godex"); adapter != nil {
		return adapter.Tools()
	}
	for _, adapter := range r.adaptersSnapshot() {
		return adapter.Tools()
	}
	return nil
}

func (r *harnessRouter) RunTurn(ctx context.Context, input HarnessTurnInput) (HarnessTurnResult, error) {
	choice, err := r.resolve(ctx, input)
	if err != nil {
		return HarnessTurnResult{}, err
	}
	adapter := r.adapter(choice)
	if adapter == nil {
		return HarnessTurnResult{}, conversation.NewNonRetryableTurnError("harness " + choice + " is unavailable")
	}
	r.sessionMu.Lock()
	prior := r.last[input.SessionID]
	r.last[input.SessionID] = choice
	r.sessionMu.Unlock()
	if prior != "" && prior != choice {
		if old := r.adapter(prior); old != nil {
			_ = old.ResetSession(ctx, input.SessionID)
		}
		_ = adapter.ResetSession(ctx, input.SessionID)
	}
	return adapter.RunTurn(ctx, input)
}

func (r *harnessRouter) ResetSession(ctx context.Context, sessionID string) error {
	r.sessionMu.Lock()
	delete(r.last, sessionID)
	r.sessionMu.Unlock()
	for _, adapter := range r.adaptersSnapshot() {
		if adapter != nil {
			_ = adapter.ResetSession(ctx, sessionID)
		}
	}
	return nil
}

func (r *harnessRouter) Close() error {
	seen := map[Harness]struct{}{}
	for _, adapter := range r.adaptersSnapshot() {
		if adapter == nil {
			continue
		}
		if _, ok := seen[adapter]; ok {
			continue
		}
		seen[adapter] = struct{}{}
		_ = adapter.Close()
	}
	return nil
}
