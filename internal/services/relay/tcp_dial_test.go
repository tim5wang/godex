package relay

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// echoTCPServer starts a local TCP server that echoes back everything it
// receives, simulating an internal-network service behind the node.
func echoTCPServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func startDialAgent(t *testing.T, hub *Hub, hubURL string, allow []string) *Agent {
	t.Helper()
	agent := NewAgent(AgentConfig{
		CenterURL:    hubURL,
		NodeID:       "node-a",
		Credential:   "ck_secret",
		Version:      "v1.2.0",
		ForwardAllow: allow,
		Handler:      fakeLocalHandler(t),
	})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	t.Cleanup(func() { _ = agent.Stop(context.Background()) })

	// Wait until the agent's relay connection is registered with the hub.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub.IsOnline("node-a") {
			return agent
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("agent did not come online before deadline")
	return nil
}

func TestAgentServesTCPOpenAndBridgesEcho(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return true })
	echoAddr := echoTCPServer(t)
	startDialAgent(t, hub, wsURL, []string{"127.0.0.1:*"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := hub.OpenTCPStream(ctx, "node-a", "c_echo", echoAddr)
	if err != nil {
		t.Fatalf("OpenTCPStream: %v", err)
	}
	defer stream.Close()

	// Local write → node dials echo server → echo server replies → we read it.
	if _, err := stream.Write([]byte("hello-echo")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 128)
	n, err := stream.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != "hello-echo" {
		t.Fatalf("echo mismatch: %q", buf[:n])
	}
}

func TestAgentRejectsForwardNotInAllowlist(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return true })
	echoAddr := echoTCPServer(t)
	// Empty allowlist → default deny.
	startDialAgent(t, hub, wsURL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := hub.OpenTCPStream(ctx, "node-a", "c_deny", echoAddr)
	if err == nil {
		t.Fatal("expected OpenTCPStream to fail for a denied target")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not allowed") {
		t.Fatalf("expected 'not allowed' error, got %v", err)
	}
}

func TestAgentRejectsForwardHostNotAllowed(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return true })
	echoAddr := echoTCPServer(t)
	// Only a different host is allowed; the echo server's host must be denied.
	startDialAgent(t, hub, wsURL, []string{"127.0.0.2:*"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := hub.OpenTCPStream(ctx, "node-a", "c_host", echoAddr)
	if err == nil {
		t.Fatal("expected OpenTCPStream to fail for a denied host")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not allowed") {
		t.Fatalf("expected 'not allowed' error, got %v", err)
	}
}

func TestAgentTCPCloseClosesLocalConn(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return true })
	echoAddr := echoTCPServer(t)
	startDialAgent(t, hub, wsURL, []string{"127.0.0.1:*"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := hub.OpenTCPStream(ctx, "node-a", "c_close", echoAddr)
	if err != nil {
		t.Fatalf("OpenTCPStream: %v", err)
	}

	// Closing the center-side stream must propagate a tcp_close to the node.
	if err := stream.Close(); err != nil {
		t.Fatalf("stream close: %v", err)
	}
	// The stream's channel is released on close, so Read returns io.EOF.
	buf := make([]byte, 128)
	if _, err := stream.Read(buf); err != io.EOF {
		t.Fatalf("expected io.EOF after close, got %v", err)
	}
}
