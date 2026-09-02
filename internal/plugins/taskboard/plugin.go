package taskboard

import (
	"context"
	"fmt"
	"net/http"

	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/pluginrt"
	"github.com/tim5wang/godex/internal/toolruntime"
)

// ManifestID is the plugin identity used for tool ownership and activation.
const ManifestID = "taskboard"

// Executor runs one card in an isolated session (the durable-subagent
// adapter lives in the backend assembly; M1-d). Implementations must:
// record the execution via Ledger.StartExecution, launch the isolated
// session, and finalize via Ledger.FinishExecution when it ends. The
// optional observability/recovery methods let a PJM see where a run is stuck
// and nudge it back, without opening the execution session.
type Executor interface {
	Execute(ctx context.Context, card Card) (executionID, sessionID string, err error)
}

// ObservedExecutor extends Executor with execution observability + recovery
// (taskboard tool action=observe/reconcile/recover/retry). Backend executor
// implements it; tool-only tests may use a stub.
type ObservedExecutor interface {
	Executor
	// Observe inspects the running execution and returns where the run is
	// (stage), how it last failed (error type / message), and what it last did.
	// The bool reports whether the run is still live.
	Observe(ctx context.Context, cardID, executionID string) (ExecutionObservation, bool, error)
	// Reconcile walks all running executions, finalizes ones whose session has
	// concretely failed, and writes observation snapshots for the rest.
	Reconcile(ctx context.Context) (ReconcileReport, error)
	// Recover appends a message into the card's execution session to nudge a
	// running/stalled task. Returns the session id delivered to.
	Recover(ctx context.Context, cardID, executionID, text string) (string, error)
	// Retry replays the last retryable turn of the execution session.
	Retry(ctx context.Context, cardID, executionID string) (string, error)
}

// Plugin is the taskboard NativePlugin: it contributes the taskboard_*
// agent tools and the /v1/taskboard HTTP surface. Both registrations are
// reversible through the pluginrt effect ledger.
type Plugin struct {
	manifest pluginrt.Manifest
	ledger   *Ledger
	executor Executor
	handler  *toolruntime.ToolHandler
}

// NewPlugin wires the plugin. handler may be nil in route-only tests.
func NewPlugin(ledger *Ledger, executor Executor, handler *toolruntime.ToolHandler) *Plugin {
	return &Plugin{
		manifest: pluginrt.Manifest{
			ID:       ManifestID,
			Version:  "0.1.0",
			Scope:    scope.Org("godex"),
			Requires: nil,
			Provides: []string{"godex:tool@1"},
		},
		ledger:   ledger,
		executor: executor,
		handler:  handler,
	}
}

// Manifest returns the plugin manifest.
func (p *Plugin) Manifest() pluginrt.Manifest { return p.manifest }

// Ledger exposes the ledger for assembly-side consumers (executor, HTTP tests).
func (p *Plugin) Ledger() *Ledger { return p.ledger }

// Start registers the agent tools and HTTP routes via the host; both are
// reverted on deactivation by the effect ledger.
func (p *Plugin) Start(ctx context.Context, host pluginrt.Host) error {
	if p.handler != nil {
		for _, tool := range NewTaskboardTools(p.ledger) {
			tool := tool
			meta := toolruntime.ToolMeta{
				Bundle:        "taskboard",
				Summary:       "tools contributed by the taskboard plugin",
				AlwaysActive:  false,
				DefaultActive: true,
			}
			host.RegisterEffect(func(ctx context.Context) (func() error, error) {
				registration, err := p.handler.RegisterOwned(ManifestID, tool, meta)
				if err != nil {
					return nil, fmt.Errorf("taskboard: register tool %s: %w", tool.Name(), err)
				}
				dispose := registration.Dispose
				return func() error { dispose(); return nil }, nil
			})
		}
	}
	if err := host.RegisterRoutes("/v1/taskboard", p.registerRoutes); err != nil {
		return fmt.Errorf("taskboard: register routes: %w", err)
	}
	return nil
}

// Stop is a no-op; effect reversal happens in the kernel ledger.
func (p *Plugin) Stop(ctx context.Context) error { return nil }

// registerRoutes mounts the HTTP surface on the plugin mux (P-A consumer).
// Patterns use the full request path, matching the httpapi mux style.
func (p *Plugin) registerRoutes(mux *http.ServeMux) {
	json := func(handler func(w http.ResponseWriter, r *http.Request) (any, error)) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload, err := handler(w, r)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, payload)
		})
	}

	mux.Handle("GET /v1/taskboard/projects", json(p.handleListProjects))
	mux.Handle("POST /v1/taskboard/projects", json(p.handleCreateProject))
	mux.Handle("PATCH /v1/taskboard/projects/{id}", json(p.handleUpdateProject))
	mux.Handle("DELETE /v1/taskboard/projects/{id}", json(p.handleDeleteProject))
	mux.Handle("GET /v1/taskboard/cards", json(p.handleListCards))
	mux.Handle("POST /v1/taskboard/cards", json(p.handleCreateCard))
	mux.Handle("GET /v1/taskboard/cards/{id}", json(p.handleGetCard))
	mux.Handle("PATCH /v1/taskboard/cards/{id}", json(p.handlePatchCard))
	mux.Handle("DELETE /v1/taskboard/cards/{id}", json(p.handleDeleteCard))
	mux.Handle("POST /v1/taskboard/cards/{id}/execute", json(p.handleExecuteCard))
	mux.Handle("POST /v1/taskboard/cards/{id}/executions/{executionID}/observe", json(p.handleObserveExecution))
	mux.Handle("POST /v1/taskboard/cards/{id}/executions/{executionID}/recover", json(p.handleRecoverExecution))
	mux.Handle("POST /v1/taskboard/cards/{id}/executions/{executionID}/retry", json(p.handleRetryExecution))
	mux.Handle("POST /v1/taskboard/reconcile", json(p.handleReconcile))
	mux.Handle("GET /v1/taskboard/status", json(p.handleStatus))
}
