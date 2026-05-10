package app

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

type recordedService struct {
	name   string
	events *[]string
	mu     *sync.Mutex
}

func (s recordedService) Start(context.Context) error {
	s.mu.Lock()
	*s.events = append(*s.events, "start:"+s.name)
	s.mu.Unlock()
	return nil
}

func (s recordedService) Stop(context.Context) error {
	s.mu.Lock()
	*s.events = append(*s.events, "stop:"+s.name)
	s.mu.Unlock()
	return nil
}

type recordedHTTPServer struct {
	events      *[]string
	mu          *sync.Mutex
	listening   chan struct{}
	shutdownNow chan struct{}
}

func (s recordedHTTPServer) ListenAndServe() error {
	close(s.listening)
	<-s.shutdownNow
	return http.ErrServerClosed
}

func (s recordedHTTPServer) Shutdown(context.Context) error {
	s.mu.Lock()
	*s.events = append(*s.events, "shutdown:server")
	s.mu.Unlock()
	select {
	case <-s.shutdownNow:
	default:
		close(s.shutdownNow)
	}
	return nil
}

func TestServeRuntimeCancelsGracefully(t *testing.T) {
	var (
		mu     sync.Mutex
		events []string
	)
	server := recordedHTTPServer{
		events:      &events,
		mu:          &mu,
		listening:   make(chan struct{}),
		shutdownNow: make(chan struct{}),
	}
	runtime := ServeRuntime{
		Server: server,
		Services: []LifecycleService{
			recordedService{name: "channels", events: &events, mu: &mu},
			recordedService{name: "cron", events: &events, mu: &mu},
			recordedService{name: "heartbeat", events: &events, mu: &mu},
		},
		ShutdownTimeout: time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- runtime.Run(ctx)
	}()

	select {
	case <-server.listening:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for server start")
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime shutdown")
	}

	want := []string{
		"start:channels",
		"start:cron",
		"start:heartbeat",
		"shutdown:server",
		"stop:heartbeat",
		"stop:cron",
		"stop:channels",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != len(want) {
		t.Fatalf("unexpected event count: got=%v want=%v", events, want)
	}
	for idx := range want {
		if events[idx] != want[idx] {
			t.Fatalf("unexpected shutdown order: got=%v want=%v", events, want)
		}
	}
}
