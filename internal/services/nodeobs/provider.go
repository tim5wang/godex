package nodeobs

import (
	"context"
	"strings"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/relay"
	"github.com/tim5wang/godex/internal/toolruntime"
	"github.com/tim5wang/godex/internal/tools"
)

// SnapshotSource is the minimal backend surface the node observer needs. The
// backend.Service satisfies it directly; tests use a fake.
type SnapshotSource interface {
	ListSessions(ctx context.Context, filter backend.SessionListFilter) ([]backend.ListedSession, error)
	ListLongTasks(ctx context.Context, sessionID string) ([]agent.LongTaskView, error)
	PendingPermissions(ctx context.Context, sessionID string) ([]tools.PendingPermission, error)
}

// Provider adapts the local backend service into a relay.NodeSnapshot so the
// relay observer can push it to the center. It implements relay.StateProvider.
type Provider struct {
	source  SnapshotSource
	version string
	caps    []string
}

// NewProvider creates a snapshot provider backed by the given source.
func NewProvider(source SnapshotSource, version string, caps []string) *Provider {
	return &Provider{source: source, version: version, caps: caps}
}

// Snapshot collects the node's running sessions, longtasks, and pending
// approvals. A broken session never fails the whole snapshot; per-session
// lookups are best effort.
func (p *Provider) Snapshot(ctx context.Context) (relay.NodeSnapshot, error) {
	sessions, err := p.source.ListSessions(ctx, backend.SessionListFilter{})
	if err != nil {
		return relay.NodeSnapshot{}, err
	}
	snap := relay.NodeSnapshot{
		Version:      p.version,
		Capabilities: append([]string(nil), p.caps...),
	}
	for _, session := range sessions {
		snap.Sessions = append(snap.Sessions, relay.SessionInfo{
			ID:        session.SessionID,
			Title:     session.Title,
			Running:   session.Running,
			UpdatedAt: session.UpdatedAt,
		})
		jobs, err := p.source.ListLongTasks(ctx, session.SessionID)
		if err != nil {
			continue
		}
		for _, job := range jobs {
			phase := job.Status
			if job.Workflow.Status != "" {
				phase = job.Workflow.Status
			}
			snap.Jobs = append(snap.Jobs, relay.JobInfo{
				ID:     job.LongTaskID,
				Name:   job.Description,
				Status: job.Status,
				Phase:  phase,
				Turn:   job.Completed,
				Total:  job.Total,
			})
		}
		permissions, err := p.source.PendingPermissions(ctx, session.SessionID)
		if err != nil {
			continue
		}
		for _, permission := range permissions {
			if permission.Status != toolruntime.PermissionStatusPending {
				continue
			}
			snap.Approvals = append(snap.Approvals, relay.ApprovalInfo{
				ID:        permission.ID,
				SessionID: session.SessionID,
				Intent:    permissionIntent(permission.Request),
				Status:    string(permission.Status),
			})
		}
	}
	return snap, nil
}

// permissionIntent renders a human-readable summary of one approval request.
func permissionIntent(request tools.PermissionRequest) string {
	var parts []string
	if request.ToolName != "" {
		parts = append(parts, request.ToolName)
	}
	if request.Command != "" {
		parts = append(parts, request.Command)
	}
	if len(request.Paths) > 0 {
		parts = append(parts, strings.Join(request.Paths, ", "))
	}
	if len(parts) == 0 {
		return request.Action
	}
	return strings.Join(parts, " ")
}
