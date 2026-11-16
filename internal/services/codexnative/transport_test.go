package codexnative

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newUnixWebSocketServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	// macOS limits Unix socket paths to 104 bytes. Go's per-test temporary
	// directory includes the long test name, so use a short private directory.
	dir, err := os.MkdirTemp("", "gc-ws-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return socket
}

func TestReadinessUsesUnixWebSocketAndPreservesMessageBoundaries(t *testing.T) {
	methods := make(chan string, 8)
	socket := newUnixWebSocketServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		for {
			var request frame
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			methods <- request.Method
			if len(request.ID) == 0 {
				continue
			}
			if request.Method != "initialize" && request.Method != "thread/loaded/list" {
				t.Errorf("readiness must not resume or mutate a thread: %s", request.Method)
				return
			}
			// JSON whitespace/newlines inside one WS frame must not turn into
			// separate protocol messages (the old stdio Scanner cannot do this).
			response, _ := json.MarshalIndent(map[string]any{"id": request.ID, "result": map[string]any{"data": []string{}}}, "", "  ")
			if err := conn.WriteMessage(websocket.TextMessage, response); err != nil {
				return
			}
		}
	})
	alias := filepath.Join(t.TempDir(), "daemon.sock")
	if err := os.Symlink(socket, alias); err != nil {
		t.Fatal(err)
	}
	// Connecting an existing daemon must not need or execute any CLI binary.
	s := New(Options{Command: "must-not-be-executed", Socket: alias})
	defer s.Close()
	ctx := testContext(t)
	for range 2 {
		if status := s.Status(ctx); !status.Ready {
			t.Fatal(status.Reason)
		}
	}
	for _, want := range []string{"initialize", "initialized", "thread/loaded/list", "thread/loaded/list"} {
		select {
		case got := <-methods:
			if got != want {
				t.Fatalf("got %s; want %s", got, want)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestReadinessTimeoutClosesConnectionAndCanReconnect(t *testing.T) {
	for _, stage := range []string{"initialize", "thread/loaded/list"} {
		t.Run(stage, func(t *testing.T) {
			var connections atomic.Int32
			closed := make(chan struct{}, 1)
			socket := newUnixWebSocketServer(t, func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				first := connections.Add(1) == 1
				defer conn.Close()
				if first {
					defer func() { closed <- struct{}{} }()
				}
				for {
					var request frame
					if err := conn.ReadJSON(&request); err != nil {
						return
					}
					if len(request.ID) == 0 || first && request.Method == stage {
						continue
					}
					if err := conn.WriteJSON(map[string]any{"id": request.ID, "result": map[string]any{}}); err != nil {
						return
					}
				}
			})
			s := New(Options{Socket: socket})
			defer s.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			status := s.Status(ctx)
			cancel()
			if status.Ready || !strings.Contains(status.Reason, stage) || !strings.Contains(status.Reason, "deadline exceeded") {
				t.Fatalf("missing stage-specific timeout: %+v", status)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("timed-out readiness connection leaked")
			}
			if status = s.Status(testContext(t)); !status.Ready || connections.Load() != 2 {
				t.Fatalf("failed to reconnect: %+v; connections=%d", status, connections.Load())
			}
		})
	}
}

func TestReadinessReportsWebSocketHandshakeFailure(t *testing.T) {
	socket := newUnixWebSocketServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not a WebSocket daemon", http.StatusBadRequest)
	})
	s := New(Options{Socket: socket})
	defer s.Close()
	status := s.Status(testContext(t))
	if status.Ready || !strings.Contains(status.Reason, "WebSocket handshake") {
		t.Fatalf("missing handshake diagnostic: %+v", status)
	}
}

func TestWebSocketTransportRejectsUnexpectedBinaryFrames(t *testing.T) {
	socket := newUnixWebSocketServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("not negotiated"))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	transport, err := dialSocket(testContext(t), socket)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if _, err := transport.ReadMessage(); err == nil || err == io.EOF || !strings.Contains(err.Error(), "expected uncompressed JSON text") {
		t.Fatalf("unexpected binary frame accepted: %v", err)
	}
}

// Opt-in: only initialize, initialized and thread/loaded/list are sent. Never
// start/restart a daemon, resume a thread, submit a prompt, or invoke a model.
func TestExistingDaemonReadinessReadOnly(t *testing.T) {
	if os.Getenv("GODEX_CODEX_READONLY_SMOKE") != "1" {
		t.Skip("set GODEX_CODEX_READONLY_SMOKE=1 to probe an existing daemon")
	}
	s := New(Options{})
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	status := s.Status(ctx)
	if !status.Ready {
		t.Fatal(status.Reason)
	}
	t.Logf("existing daemon: ready=true; initialize + thread/loaded/list completed in %s", time.Since(started).Round(time.Millisecond))
	if id := os.Getenv("GODEX_CODEX_READONLY_THREAD"); id != "" {
		c, err := s.connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		params := map[string]any{"limit": 200}
		for {
			var page struct {
				Data       []string `json:"data"`
				NextCursor *string  `json:"nextCursor"`
			}
			if _, err := c.call(ctx, "thread/loaded/list", params, &page); err != nil {
				t.Fatal(err)
			}
			for _, loaded := range page.Data {
				if loaded == id {
					t.Logf("target thread %s: loaded in this daemon (read-only check; no resume)", id)
					return
				}
			}
			if page.NextCursor == nil || *page.NextCursor == "" {
				t.Logf("target thread %s: NOT loaded in this daemon (read-only check; no resume)", id)
				return
			}
			params["cursor"] = *page.NextCursor
		}
	}
}

func TestExistingDaemonAccessReadOnly(t *testing.T) {
	id := os.Getenv("GODEX_CODEX_READONLY_THREAD")
	if os.Getenv("GODEX_CODEX_READONLY_SMOKE") != "1" || id == "" || os.Getenv("GODEX_CODEX_WORKSPACE_ROOTS") == "" {
		t.Skip("opt in with a target thread and explicit authorized workspace roots")
	}
	s := New(Options{})
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// This production preflight only initializes, reads metadata and lists
	// loaded IDs. It never resumes, subscribes, prompts or invokes a model.
	access, err := s.Access(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("target thread %s: loadedInRuntime=%t (metadata-only; no resume)", id, access.LoadedInRuntime)
}

// Opt-in latency probes never attach a viewer, resume a thread or invoke a
// model. Log only duration and payload sizes, not private conversation content.
func TestExistingDaemonLatencyReadOnly(t *testing.T) {
	id := os.Getenv("GODEX_CODEX_READONLY_THREAD")
	if os.Getenv("GODEX_CODEX_READONLY_SMOKE") != "1" || id == "" || os.Getenv("GODEX_CODEX_WORKSPACE_ROOTS") == "" {
		t.Skip("opt in with a target thread and explicit authorized workspace roots")
	}
	s := New(Options{})
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	started := time.Now()
	c, err := s.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("connect: %s", time.Since(started).Round(time.Millisecond))
	started = time.Now()
	if _, err := s.Access(ctx, id); err != nil {
		t.Fatal(err)
	}
	t.Logf("access: %s", time.Since(started).Round(time.Millisecond))
	for _, limit := range []int{1, 5, 20} {
		started = time.Now()
		var page json.RawMessage
		_, err := c.call(ctx, "thread/turns/list", map[string]any{"threadId": id, "limit": limit, "sortDirection": "desc", "itemsView": "full"}, &page)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("turns/list limit=%d: %s, %d bytes", limit, time.Since(started).Round(time.Millisecond), len(page))
	}
	started = time.Now()
	snapshot, err := s.History(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("production first history page: %s, %d turns", time.Since(started).Round(time.Millisecond), len(snapshot.Turns.Data))
	if len(snapshot.Turns.Data) > 5 {
		t.Fatal("native runtime ignored initial history limit")
	}
	started = time.Now()
	if _, err := s.Queue(ctx, id); err != nil {
		t.Fatal(err)
	}
	t.Logf("queue: %s", time.Since(started).Round(time.Millisecond))
}
