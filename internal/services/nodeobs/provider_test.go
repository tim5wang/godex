package nodeobs

import (
	"context"
	"errors"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/toolruntime"
	"github.com/tim5wang/godex/internal/tools"
)

// fakeSource implements SnapshotSource with canned backend data.
type fakeSource struct {
	sessions []backend.ListedSession
	long     map[string][]agent.LongTaskView
	pending  map[string][]tools.PendingPermission
	err      error
}

func (f *fakeSource) ListSessions(_ context.Context, _ backend.SessionListFilter) ([]backend.ListedSession, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.sessions, nil
}

func (f *fakeSource) ListLongTasks(_ context.Context, sessionID string) ([]agent.LongTaskView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.long[sessionID], nil
}

func (f *fakeSource) PendingPermissions(_ context.Context, sessionID string) ([]tools.PendingPermission, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.pending[sessionID], nil
}

func sampleSession(id, title string, running bool) backend.ListedSession {
	return backend.ListedSession{SessionID: id, Title: title, Running: running}
}

func TestProviderSnapshotMapsSessionsJobsApprovals(t *testing.T) {
	src := &fakeSource{
		sessions: []backend.ListedSession{
			sampleSession("s1", "fix bug", true),
			sampleSession("s2", "done", false),
		},
		long: map[string][]agent.LongTaskView{
			"s1": {{
				LongTaskID:  "j1",
				Description: "deploy",
				Status:      "running",
				Total:       5,
				Pending:     2,
				Completed:   2,
			}},
		},
		pending: map[string][]tools.PendingPermission{
			"s1": {{
				ID:     "ap1",
				Status: toolruntime.PermissionStatusPending,
				Request: tools.PermissionRequest{
					SessionID: "s1",
					ToolName:  "bash",
					Command:   "go test",
					Mutation:  true,
				},
			}},
		},
	}
	provider := NewProvider(src, "v1.2.0", []string{"chat", "terminal"})

	snap, err := provider.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Version != "v1.2.0" {
		t.Errorf("version = %q", snap.Version)
	}
	if len(snap.Capabilities) != 2 {
		t.Errorf("capabilities = %v", snap.Capabilities)
	}
	if len(snap.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want 2", snap.Sessions)
	}
	if !snap.Sessions[0].Running || snap.Sessions[0].ID != "s1" {
		t.Errorf("session[0] = %+v", snap.Sessions[0])
	}
	if len(snap.Jobs) != 1 {
		t.Fatalf("jobs = %+v, want 1", snap.Jobs)
	}
	job := snap.Jobs[0]
	if job.ID != "j1" || job.Status != "running" || job.Phase != "running" || job.Turn != 2 || job.Total != 5 {
		t.Errorf("job = %+v", job)
	}
	if len(snap.Approvals) != 1 {
		t.Fatalf("approvals = %+v, want 1", snap.Approvals)
	}
	ap := snap.Approvals[0]
	if ap.ID != "ap1" || ap.SessionID != "s1" || ap.Status != "pending" {
		t.Errorf("approval = %+v", ap)
	}
	if ap.Intent == "" {
		t.Error("expected non-empty approval intent")
	}
}

func TestProviderSnapshotErrorsPropagate(t *testing.T) {
	provider := NewProvider(&fakeSource{err: errors.New("boom")}, "v1.2.0", nil)
	if _, err := provider.Snapshot(context.Background()); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestProviderSnapshotSkipsBrokenSessions(t *testing.T) {
	// A session without longtask data must not fail the whole snapshot.
	src := &fakeSource{
		sessions: []backend.ListedSession{sampleSession("s1", "fix bug", true)},
		long:     map[string][]agent.LongTaskView{},
		pending:  map[string][]tools.PendingPermission{},
	}
	provider := NewProvider(src, "v1.2.0", nil)
	snap, err := provider.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap.Jobs) != 0 || len(snap.Approvals) != 0 {
		t.Errorf("expected empty jobs/approvals, got %+v / %+v", snap.Jobs, snap.Approvals)
	}
}
