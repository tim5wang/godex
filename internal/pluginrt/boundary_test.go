package pluginrt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/scope"
)

type boundaryPlugin struct {
	manifest Manifest
	start    func(ctx context.Context, host Host) error
}

func (p *boundaryPlugin) Manifest() Manifest                         { return p.manifest }
func (p *boundaryPlugin) Start(ctx context.Context, host Host) error { return p.start(ctx, host) }
func (p *boundaryPlugin) Stop(ctx context.Context) error             { return nil }

func newBoundaryManifest(id string) Manifest {
	return Manifest{ID: id, Scope: scope.Org("godex")}
}

func TestPluginRoutesRegisterAndRevert(t *testing.T) {
	manager := NewManager(nil)
	plugin := &boundaryPlugin{
		manifest: newBoundaryManifest("routes-plugin"),
		start: func(ctx context.Context, host Host) error {
			return host.RegisterRoutes("/t1", func(mux *http.ServeMux) {
				mux.HandleFunc("GET /t1/ping", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("pong"))
				})
			})
		},
	}
	if _, err := manager.Activate(context.Background(), plugin); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if prefixes := manager.RoutePrefixes(); len(prefixes) != 1 || prefixes[0] != "/t1" {
		t.Fatalf("expected [/t1], got %v", prefixes)
	}

	root := http.NewServeMux()
	manager.MountRoutes(root)
	server := httptest.NewServer(root)
	defer server.Close()

	resp, err := http.Get(server.URL + "/t1/ping")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 while active, got %d", resp.StatusCode)
	}

	if err := manager.Deactivate(context.Background(), "routes-plugin"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	resp, err = http.Get(server.URL + "/t1/ping")
	if err != nil {
		t.Fatalf("get after deactivate: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after deactivate, got %d", resp.StatusCode)
	}
}

func TestPluginRoutesLateActivationMounts(t *testing.T) {
	manager := NewManager(nil)
	root := http.NewServeMux()
	manager.MountRoutes(root)

	plugin := &boundaryPlugin{
		manifest: newBoundaryManifest("late-routes"),
		start: func(ctx context.Context, host Host) error {
			return host.RegisterRoutes("/t2", func(mux *http.ServeMux) {
				mux.HandleFunc("GET /t2/hello", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				})
			})
		},
	}
	if _, err := manager.Activate(context.Background(), plugin); err != nil {
		t.Fatalf("activate: %v", err)
	}

	server := httptest.NewServer(root)
	defer server.Close()
	resp, err := http.Get(server.URL + "/t2/hello")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected late-mounted route to serve 200, got %d", resp.StatusCode)
	}
}

func TestPluginServicesInjection(t *testing.T) {
	workspace := t.TempDir()
	stateDir := filepath.Join(workspace, "state")
	var gotWorkspace atomic.Value
	manager := NewManager(nil, WithServices(Services{
		WorkspaceDir: workspace,
		StateDir:     stateDir,
	}))
	plugin := &boundaryPlugin{
		manifest: newBoundaryManifest("services-plugin"),
		start: func(ctx context.Context, host Host) error {
			gotWorkspace.Store(host.Services().WorkspaceDir)
			return nil
		},
	}
	if _, err := manager.Activate(context.Background(), plugin); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if got := gotWorkspace.Load(); got != workspace {
		t.Fatalf("expected workspace %q, got %v", workspace, got)
	}
}

func TestPluginScheduleTicksAndReverts(t *testing.T) {
	manager := NewManager(nil)
	var ticks atomic.Int64
	plugin := &boundaryPlugin{
		manifest: newBoundaryManifest("schedule-plugin"),
		start: func(ctx context.Context, host Host) error {
			return host.RegisterSchedule("tick", ScheduleSpec{Every: 10 * time.Millisecond}, func(ctx context.Context) {
				ticks.Add(1)
			})
		},
	}
	if _, err := manager.Activate(context.Background(), plugin); err != nil {
		t.Fatalf("activate: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && ticks.Load() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if ticks.Load() < 2 {
		t.Fatalf("expected schedule to tick at least twice, got %d", ticks.Load())
	}

	if err := manager.Deactivate(context.Background(), "schedule-plugin"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	after := ticks.Load()
	time.Sleep(80 * time.Millisecond)
	if got := ticks.Load(); got != after {
		t.Fatalf("expected schedule to stop after deactivation, ticks went %d -> %d", after, got)
	}
}

func TestPluginScheduleValidation(t *testing.T) {
	cases := []ScheduleSpec{
		{},
		{CronExpr: "*/5 * * * *", Every: time.Second},
		{CronExpr: "not a cron"},
	}
	for _, spec := range cases {
		if err := spec.validate(); err == nil {
			t.Fatalf("expected validation error for %+v", spec)
		}
	}
	if err := (ScheduleSpec{CronExpr: "*/5 * * * *"}).validate(); err != nil {
		t.Fatalf("valid cron rejected: %v", err)
	}
	if err := (ScheduleSpec{Every: time.Second}).validate(); err != nil {
		t.Fatalf("valid every rejected: %v", err)
	}
}
