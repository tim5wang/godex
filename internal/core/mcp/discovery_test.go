package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCachedToolDiscoveryNonBlockingAndCoalesced(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []map[string]any{{"name": "echo"}}}})
	}))
	defer srv.Close()
	m := NewManager(filepath.Join(t.TempDir(), "mcp.json"), "", "")
	defer m.Close()
	if err := m.UpsertTransientServer(ServerConfig{Name: "slow", Type: ServerTypeHTTP, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 50; i++ {
		items, status := m.CachedServerTools("slow")
		if len(items) != 0 || status.State != "discovering" {
			t.Fatalf("unexpected snapshot: %+v, %+v", items, status)
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("cache-only registration blocked")
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := m.ListServerTools(ctx, "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter cancellation: %v", err)
	}
	close(release)
	items, err := m.ListServerTools(context.Background(), "slow")
	if err != nil || len(items) != 1 || requests.Load() != 2 {
		t.Fatalf("discovery not shared: items=%v err=%v requests=%d", items, err, requests.Load())
	}
	_, status := m.CachedServerTools("slow")
	if !status.Online || status.State != "ready" {
		t.Fatalf("status: %+v", status)
	}
}

func TestToolDiscoveryFailureCooldownAndConfigInvalidation(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unavailable", http.StatusBadRequest)
	}))
	defer srv.Close()
	m := NewManager(filepath.Join(t.TempDir(), "mcp.json"), "", "")
	defer m.Close()
	bad := ServerConfig{Name: "test", Type: ServerTypeHTTP, URL: srv.URL}
	if err := m.UpsertTransientServer(bad); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := m.ListServerTools(context.Background(), "test"); err == nil {
			t.Fatal("expected failure")
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("failure was reprobed %d times", requests.Load())
	}
	_, status := m.CachedServerTools("test")
	if status.State != "unavailable" || status.Error == "" {
		t.Fatalf("missing failure state: %+v", status)
	}
	good := fakeHTTPServer(t, false)
	bad.URL = good.URL
	if err := m.UpsertTransientServer(bad); err != nil {
		t.Fatal(err)
	}
	if items, err := m.ListServerTools(context.Background(), "test"); err != nil || len(items) != 2 {
		t.Fatalf("config change did not clear failure: %v %v", items, err)
	}
}

func TestToolDiscoveryInvalidationDiscardsInflightResult(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	m := NewManager(filepath.Join(t.TempDir(), "mcp.json"), "", "")
	defer m.Close()
	server := ServerConfig{Name: "test", Type: ServerTypeHTTP, URL: srv.URL}
	if err := m.UpsertTransientServer(server); err != nil {
		t.Fatal(err)
	}
	m.CachedServerTools("test")
	<-started
	m.mu.RLock()
	old := m.toolDiscoveries["test"]
	m.mu.RUnlock()
	good := fakeHTTPServer(t, false)
	server.URL = good.URL
	if err := m.UpsertTransientServer(server); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ListServerTools(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	<-old.done
	items, status := m.CachedServerTools("test")
	if len(items) != 2 || !status.Online {
		t.Fatalf("old discovery overwrote cache: %v %+v", items, status)
	}
}

func TestMCPClientBusyWaitHonorsContext(t *testing.T) {
	server := ServerConfig{Name: "busy", Type: ServerTypeHTTP, URL: "http://localhost"}
	entry := &clientEntry{sig: serverSignature(server), gate: make(chan struct{}, 1)}
	entry.lastUsed.Store(time.Now().UnixNano())
	entry.gate <- struct{}{}
	m := &Manager{clients: map[string]*clientEntry{"busy": entry}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := withClient(m, ctx, server, func(rpcClient) (bool, error) { t.Error("entered busy client"); return false, nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}
