package mintui

import (
	"context"

	rtbackend "github.com/tim5wang/godex/internal/services/backend"
)

// BackendAdapter wraps an *rtbackend.Service and satisfies the
// mintui.Backend interface for the longtask surface.
//
// *rtbackend.Service already exposes the agent-shaped
// ListLongTasks / GetLongTask / CancelLongTask methods, but
// those return []agent.LongTaskView (and a nodeID-typed
// cancel) and would force mintui to import internal/agent.
// Instead the service exposes mintui-shaped projections
// (MintuiListLongTasks / MintuiGetLongTask / MintuiCancelLongTask)
// that the adapter forwards to.
//
// The other 16 Backend methods (OpenSession, Snapshot, Submit,
// …) are inherited from the embedded *rtbackend.Service so we
// do not have to repeat them here.
//
// One-line wire-up in cmd/godex/main.go:
//
//	mintui.New(cfg, mintui.NewBackendAdapter(svc), out, err)
type BackendAdapter struct {
	*rtbackend.Service
}

// NewBackendAdapter wraps an existing *rtbackend.Service and
// returns a value that satisfies mintui.Backend.
func NewBackendAdapter(s *rtbackend.Service) *BackendAdapter {
	return &BackendAdapter{Service: s}
}

// ListLongTasks implements Backend by delegating to the
// service's mintui-shaped projection.
func (a *BackendAdapter) ListLongTasks(ctx context.Context, sessionID string) ([]rtbackend.LongTaskRow, error) {
	return a.Service.MintuiListLongTasks(ctx, sessionID)
}

// GetLongTask implements Backend.
func (a *BackendAdapter) GetLongTask(ctx context.Context, sessionID, workflowID string) (rtbackend.LongTaskDetail, error) {
	return a.Service.MintuiGetLongTask(ctx, sessionID, workflowID)
}

// CancelLongTask implements Backend.
func (a *BackendAdapter) CancelLongTask(ctx context.Context, sessionID, workflowID string) error {
	return a.Service.MintuiCancelLongTask(ctx, sessionID, workflowID)
}

// ListSubagents implements Backend.
func (a *BackendAdapter) ListSubagents(ctx context.Context, sessionID string) ([]rtbackend.SubagentRow, error) {
	return a.Service.MintuiListSubagents(ctx, sessionID)
}

// LookupLongTask implements Backend.
func (a *BackendAdapter) LookupLongTask(ctx context.Context, sessionID, commit, longtaskID string) (rtbackend.LongTaskLookupResult, error) {
	return a.Service.MintuiLookupLongTask(ctx, sessionID, commit, longtaskID)
}

// RollbackLongTaskStory implements Backend.
func (a *BackendAdapter) RollbackLongTaskStory(ctx context.Context, sessionID, workflowID, nodeID, reason string) (rtbackend.LongTaskRollbackResult, error) {
	return a.Service.MintuiRollbackLongTaskStory(ctx, sessionID, workflowID, nodeID, reason)
}

// GCLongTaskArtifacts implements Backend.
func (a *BackendAdapter) GCLongTaskArtifacts(ctx context.Context, sessionID, workflowID string, olderThanSeconds int, apply bool) (rtbackend.LongTaskGCSweepResult, error) {
	return a.Service.MintuiGCLongTaskArtifacts(ctx, sessionID, workflowID, olderThanSeconds, apply)
}
