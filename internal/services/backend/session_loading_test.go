package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/mcp"
	"github.com/tim5wang/godex/internal/sessionstore"
)

func TestOpenSessionDoesNotWaitForMCP(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	cfg := newTestConfig(t)
	if err := mcp.SaveConfig(cfg.MCPConfigPath, mcp.Config{Servers: []mcp.ServerConfig{{Name: "hung", Type: mcp.ServerTypeHTTP, URL: srv.URL}}}); err != nil {
		t.Fatal(err)
	}
	s := newTestService(cfg, &stubCaller{})
	defer s.shared.MCPManager().Close()
	start := time.Now()
	for _, key := range []string{"initial", "resume"} {
		if _, err := s.OpenSession(context.Background(), SessionLocator{Channel: "local", Key: key}); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("opens blocked on MCP: %s", elapsed)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("discovery was not scheduled")
	}
}

type blockingSessionStore struct {
	sessionstore.Store
	id      string
	started chan struct{}
	release chan struct{}
	loads   atomic.Int32
}

func (s *blockingSessionStore) Load(ctx context.Context, id string) (sessionstore.SessionData, bool, error) {
	if id == s.id {
		if s.loads.Add(1) == 1 {
			close(s.started)
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return sessionstore.SessionData{}, false, ctx.Err()
		}
	}
	return s.Store.Load(ctx, id)
}

func TestOpenSessionCoalescesWithoutHoldingGlobalLock(t *testing.T) {
	cfg := newTestConfig(t)
	s := newTestService(cfg, &stubCaller{})
	locator := s.withDefaultLocatorMetadata(SessionLocator{Channel: "local", Key: "blocked"})
	id := stableSessionID(locator)
	if err := os.MkdirAll(s.sessionDir(id), 0755); err != nil {
		t.Fatal(err)
	}
	store := &blockingSessionStore{Store: s.store, id: id, started: make(chan struct{}), release: make(chan struct{})}
	s.store = store
	cfg.Storage.SessionBackend = "sqlite" // Exercise the store seam without external I/O.
	first := make(chan error, 1)
	go func() { _, err := s.OpenSession(context.Background(), locator); first <- err }()
	<-store.started
	if err := s.DeleteSession(context.Background(), id); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("delete must not race an in-flight load: %v", err)
	}
	other := make(chan error, 1)
	go func() {
		_, err := s.OpenSession(context.Background(), SessionLocator{Channel: "local", Key: "other"})
		other <- err
	}()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated session blocked by global lock")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.OpenSession(ctx, locator); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter cancellation: %v", err)
	}
	second := make(chan error, 1)
	go func() { _, err := s.OpenSession(context.Background(), locator); second <- err }()
	close(store.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if store.loads.Load() != 1 {
		t.Fatalf("loaded same session %d times", store.loads.Load())
	}
}

func TestStartupDoesNotRepairUnopenedSessions(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Runtime.Recovery.AutoRepairSessions = true
	dir := filepath.Join(cfg.SessionsDir, "unopened")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// Deliberately corrupt history: neither startup queue scanning nor repair
	// should touch it until this particular session is opened.
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte("broken"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, turnQueueFileName), []byte("[]"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = newTestService(cfg, &stubCaller{})
	if _, err := os.Stat(filepath.Join(dir, eventJournalFileName)); !os.IsNotExist(err) {
		t.Fatalf("startup audited unrelated session: %v", err)
	}
}

func TestSessionOpenSnapshotLoadsStoreOnce(t *testing.T) {
	cfg := newTestConfig(t)
	s := newTestService(cfg, &stubCaller{})
	state := json.RawMessage(`{"messages":[]}`)
	manifest, _ := json.Marshal(SessionManifest{SessionID: "stored", StateDigest: stateDigest(state)})
	if err := s.store.Save(context.Background(), sessionstore.SessionData{SessionID: "stored", Manifest: manifest, State: state, Timeline: json.RawMessage(`[]`), Queue: json.RawMessage(`[]`)}); err != nil {
		t.Fatal(err)
	}
	store := &blockingSessionStore{Store: s.store, id: "stored", started: make(chan struct{}), release: make(chan struct{})}
	close(store.release)
	s.store = store
	cfg.Storage.SessionBackend = "sqlite"
	if _, err := s.readSessionForOpen("stored"); err != nil {
		t.Fatal(err)
	}
	if store.loads.Load() != 1 {
		t.Fatalf("store loaded %d times", store.loads.Load())
	}
}

func TestOpenSessionRepairsOnlyRequestedSession(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Runtime.Recovery.AutoRepairSessions = true
	locator := SessionLocator{Channel: "web", Key: "targeted-repair"}
	id := stableSessionID(locator)
	state := mustJSON(t, agent.SessionState{Messages: []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "keep this history")}})
	manifest := mustJSON(t, SessionManifest{SessionID: id, Locator: locator, Title: "kept", StateDigest: "wrong"})
	store := sessionstore.NewJSONStore(cfg.SessionsDir)
	for _, sessionID := range []string{id, "unopened"} {
		if err := store.Save(context.Background(), sessionstore.SessionData{SessionID: sessionID, Manifest: manifest, State: state}); err != nil {
			t.Fatal(err)
		}
	}
	s := newTestService(cfg, &stubCaller{})
	for _, sessionID := range []string{id, "unopened"} {
		data, err := os.ReadFile(filepath.Join(cfg.SessionsDir, sessionID, manifestFileName))
		if err != nil || string(data) != string(manifest) {
			t.Fatalf("startup changed %s: %v", sessionID, err)
		}
	}
	opened, err := s.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(context.Background(), opened.SessionID)
	if err != nil || len(snapshot.Messages) != 1 || protocol.MessageText(snapshot.Messages[0]) != "keep this history" {
		t.Fatalf("targeted repair lost history: %+v %v", snapshot.Messages, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionsDir, id, ".repair-backups")); err != nil {
		t.Fatalf("expected recoverable repair backup: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.SessionsDir, "unopened", manifestFileName))
	if err != nil || string(data) != string(manifest) {
		t.Fatalf("opening one session repaired unrelated history: %v", err)
	}
}

func TestSessionOpenSnapshotRecoversInjectedQueue(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := newTestConfig(t)
		cfg.Runtime.Recovery.AutoRepairSessions = enabled
		s := newTestService(cfg, &stubCaller{})
		id := "queued-session"
		state := json.RawMessage(`{"messages":[]}`)
		manifest := mustJSON(t, SessionManifest{SessionID: id, Title: "kept", StateDigest: stateDigest(state)})
		queue := json.RawMessage(`[{"id":"steer","status":"injected","mode":"steering","envelope":{"text":"continue"}}]`)
		if err := s.store.Save(context.Background(), sessionstore.SessionData{SessionID: id, Manifest: manifest, State: state, Queue: queue}); err != nil {
			t.Fatal(err)
		}
		snapshot, err := s.readSessionForOpen(id)
		if err != nil || len(snapshot.Queue) != 1 || snapshot.QueueRecovered != enabled {
			t.Fatalf("recovery=%v snapshot=%+v err=%v", enabled, snapshot, err)
		}
		if !enabled {
			if snapshot.Queue[0].Status != "injected" {
				t.Fatal("recovered queue despite disabled auto repair")
			}
			continue
		}
		if q := snapshot.Queue[0]; q.Status != "queued" || q.Mode != QueueModeFollowUp || q.Envelope.Metadata["queue_mode"] != string(QueueModeFollowUp) {
			t.Fatalf("orphan injection not converted to follow-up: %+v", q)
		}
		// Load but do not start the queued turn: verify its repair is durable
		// without making any model requests.
		if _, err := s.loadSession(id, SessionLocator{Channel: "web", Key: "queued"}); err != nil {
			t.Fatal(err)
		}
		if queue := s.readSessionQueue(id); len(queue) != 1 || queue[0].Status != "queued" || queue[0].Mode != QueueModeFollowUp {
			t.Fatalf("queue recovery was not persisted: %+v", queue)
		}
	}
}

func TestSessionOpenSnapshotHonorsNewerQueueCancellation(t *testing.T) {
	cfg := newTestConfig(t)
	s := newTestService(cfg, &stubCaller{})
	id := "checkpoint-queue"
	state := json.RawMessage(`{"messages":[]}`)
	manifest := SessionManifest{SessionID: id, StateDigest: stateDigest(state)}
	if _, err := s.writeSessionCheckpoint(id, manifest, state, nil, nil, []QueuedTurn{{ID: "old", Status: "queued"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.readSessionForOpen(id)
	if err != nil || snapshot.Manifest == nil || len(snapshot.Queue) != 1 {
		t.Fatalf("checkpoint-only session not recovered: %+v %v", snapshot, err)
	}
	queuePath := filepath.Join(s.sessionDir(id), turnQueueFileName)
	if err := os.WriteFile(queuePath, []byte(`[]`), 0644); err != nil {
		t.Fatal(err)
	}
	newer := time.Now().Add(time.Second)
	if err := os.Chtimes(queuePath, newer, newer); err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.readSessionForOpen(id)
	if err != nil || len(snapshot.Queue) != 0 {
		t.Fatalf("old checkpoint resurrected cancelled queue: %+v %v", snapshot, err)
	}
}

func TestOpenSessionSkipLatestSessionLookupIsTransient(t *testing.T) {
	cfg := newTestConfig(t)
	s := newTestService(cfg, &stubCaller{})
	if err := s.writeLatestSessionKey("global-latest"); err != nil {
		t.Fatal(err)
	}

	global, err := s.OpenSession(context.Background(), SessionLocator{Channel: "local", Key: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if global.Locator.Key != "global-latest" {
		t.Fatalf("legacy default lookup changed: %+v", global.Locator)
	}

	locator := SessionLocator{
		Channel:                 "local",
		Key:                     "default",
		Metadata:                map[string]string{"project_dir": cfg.WorkspaceDir},
		SkipLatestSessionLookup: true,
	}
	opened, err := s.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Locator.Key != "default" {
		t.Fatalf("skip flag did not preserve exact key: %+v", opened.Locator)
	}
	if opened.Locator.SkipLatestSessionLookup {
		t.Fatal("transient lookup flag leaked into the opened session")
	}
	session, err := s.requireSession(opened.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.persistSession(session, s.now()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.sessionDir(opened.SessionID), manifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest SessionManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Locator.SkipLatestSessionLookup {
		t.Fatal("transient lookup flag was persisted")
	}
}
