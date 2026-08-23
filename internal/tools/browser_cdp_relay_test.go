package tools

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/cdp"
	"github.com/gorilla/websocket"
	"github.com/tim5wang/godex/internal/core/config"
)

// tcpPair returns a connected TCP loopback pair, simulating a real relay
// stream (which has TCP buffering semantics; net.Pipe would deadlock the
// websocket handshake because both sides write before reading).
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	acceptCh := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			acceptCh <- conn
		}
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server := <-acceptCh
	_ = ln.Close()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client, server
}

// TestSetCDPDialerStoresAndStartRelayCDPRequiresDialer verifies the dialer
// setter works and that starting in relay mode without an installed dialer
// fails loudly.
func TestSetCDPDialerStoresAndStartRelayCDPRequiresDialer(t *testing.T) {
	service := NewBrowserService(config.BrowserConfig{
		Enabled:        true,
		CDPRelayNode:   "node-a",
		CDPRelayTarget: "127.0.0.1:9222",
	}, t.TempDir())

	// No dialer installed -> relay start must fail with a clear error.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := service.startRelayCDP(ctx); err == nil {
		t.Fatal("expected startRelayCDP to fail without a dialer")
	} else if !strings.Contains(err.Error(), "relay CDP dialer") {
		t.Fatalf("expected dialer-missing error, got %v", err)
	}

	// Installing a dialer that errors should surface the dial error.
	service.SetCDPDialer(func(ctx context.Context, nodeID, target string) (net.Conn, error) {
		if nodeID != "node-a" {
			t.Errorf("expected node node-a, got %q", nodeID)
		}
		if target != "127.0.0.1:9222" {
			t.Errorf("expected default target 127.0.0.1:9222, got %q", target)
		}
		return nil, errors.New("dial boom")
	})
	if _, err := service.startRelayCDP(ctx); err == nil || !strings.Contains(err.Error(), "dial boom") {
		t.Fatalf("expected dial error to surface, got %v", err)
	}
}

// TestStartRelayCDPDefaultTarget verifies cdp_relay_target defaults to
// 127.0.0.1:9222 and a ws:// prefix is tolerated.
func TestStartRelayCDPDefaultTarget(t *testing.T) {
	service := NewBrowserService(config.BrowserConfig{
		Enabled:      true,
		CDPRelayNode: "node-a",
	}, t.TempDir())
	service.SetCDPDialer(func(ctx context.Context, nodeID, target string) (net.Conn, error) {
		if target != "127.0.0.1:9222" {
			t.Errorf("expected default target, got %q", target)
		}
		return nil, errors.New("stop-here")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = service.startRelayCDP(ctx)

	// ws:// prefix on target is stripped before dialing.
	service2 := NewBrowserService(config.BrowserConfig{
		Enabled:        true,
		CDPRelayNode:   "node-a",
		CDPRelayTarget: "ws://127.0.0.1:9333",
	}, t.TempDir())
	service2.SetCDPDialer(func(ctx context.Context, nodeID, target string) (net.Conn, error) {
		if target != "127.0.0.1:9333" {
			t.Errorf("expected ws:// prefix stripped, got %q", target)
		}
		return nil, errors.New("stop-here")
	})
	_, _ = service2.startRelayCDP(ctx)
}

// TestRelayNetConnAdapterContract verifies the adapter satisfies net.Conn and
// delegates Read/Write/Close to the underlying stream.
func TestRelayNetConnAdapterContract(t *testing.T) {
	client, server := tcpPair(t)
	adapter := &relayNetConn{ReadWriteCloser: server, local: server.LocalAddr(), remote: server.RemoteAddr()}

	if adapter.LocalAddr() == nil || adapter.RemoteAddr() == nil {
		t.Fatal("expected non-nil addresses")
	}
	if err := adapter.SetDeadline(time.Time{}); err != nil {
		t.Fatalf("SetDeadline should be a no-op: %v", err)
	}
	if err := adapter.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("SetReadDeadline should be a no-op: %v", err)
	}
	if err := adapter.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatalf("SetWriteDeadline should be a no-op: %v", err)
	}

	go func() {
		_, _ = client.Write([]byte("ping"))
	}()
	buf := make([]byte, 4)
	if n, err := adapter.Read(buf); err != nil || string(buf[:n]) != "ping" {
		t.Fatalf("expected relay adapter to read 'ping', got %q err %v", buf[:n], err)
	}
	// net.Pipe Write blocks until the peer Read consumes the bytes, so the
	// peer read must run in a goroutine first.
	readCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		got := make([]byte, 4)
		if _, err := io.ReadFull(client, got); err != nil {
			errCh <- err
			return
		}
		readCh <- string(got)
	}()
	if _, err := adapter.Write([]byte("pong")); err != nil {
		t.Fatalf("relay adapter write: %v", err)
	}
	select {
	case got := <-readCh:
		if got != "pong" {
			t.Fatalf("expected client to read 'pong', got %q", got)
		}
	case err := <-errCh:
		t.Fatalf("client read: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for client read")
	}
}

// TestGorillaWSAdapterSendReadRoundTrip verifies the rod WebSocketable adapter
// round-trips text messages over a gorilla WebSocket, using a manually dialed
// TCP connection + NewClient (exactly what startRelayCDP does over the relay
// stream) against a real websocket server.
func TestGorillaWSAdapterSendReadRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.WriteMessage(websocket.TextMessage, append([]byte("echo:"), data...))
		}
	}))
	defer server.Close()

	u, err := url.Parse("ws" + strings.TrimPrefix(server.URL, "http") + "/ws")
	if err != nil {
		t.Fatalf("parse ws url: %v", err)
	}
	raw, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("dial server: %v", err)
	}
	defer raw.Close()
	ws, _, err := websocket.NewClient(raw, u, nil, 0, 0)
	if err != nil {
		t.Fatalf("client websocket handshake: %v", err)
	}
	adapter := &gorillaWSAdapter{conn: ws}
	if err := adapter.Send([]byte("hello-cdp")); err != nil {
		t.Fatalf("adapter send: %v", err)
	}
	data, err := adapter.Read()
	if err != nil {
		t.Fatalf("adapter read: %v", err)
	}
	if string(data) != "echo:hello-cdp" {
		t.Fatalf("expected echo:hello-cdp, got %q", data)
	}

	// The adapter must satisfy rod's cdp transport contract.
	var _ cdp.WebSocketable = adapter
}

// TestCDPListenLauncherPinsPort verifies cdp_listen pins the remote debugging
// port on the launcher flags.
func TestCDPListenLauncherPinsPort(t *testing.T) {
	launch := newLocalLauncher(context.Background(), true, "", t.TempDir(), t.TempDir(), "127.0.0.1:9222")
	if launch == nil {
		t.Fatal("expected launcher")
	}
	flags := launch.Get("remote-debugging-port")
	if flags != "9222" {
		t.Fatalf("expected remote-debugging-port 9222, got %q", flags)
	}
	if addr := launch.Get("remote-debugging-address"); addr != "127.0.0.1" {
		t.Fatalf("expected remote-debugging-address 127.0.0.1, got %q", addr)
	}

	// Empty cdp_listen leaves the launcher's default (random port = "0").
	plain := newLocalLauncher(context.Background(), true, "", t.TempDir(), t.TempDir(), "")
	if port := plain.Get("remote-debugging-port"); port != "0" {
		t.Fatalf("expected default random port 0, got %q", port)
	}
}
