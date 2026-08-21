package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// bigBody returns a body large enough to cross the gzip threshold.
func bigBody(prefix string) []byte {
	return []byte(strings.Repeat(prefix, 300)) // > 1KB
}

// base64Plain returns the plain base64 encoding of data (no compression).
func base64Plain(t *testing.T, data []byte) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(data)
}

func TestRelayGzipLargeRequestIsCompressed(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return true })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "x", Version: "v1.2.0", Caps: []string{CapGzip}})
	ok := client.recv(t)
	if ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}
	if !hasCap(ok.Caps, CapGzip) {
		t.Fatalf("expected hub to advertise gzip, got caps %v", ok.Caps)
	}

	body := bigBody("request-payload-")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, err := hub.startRequest(ctx, "node-a", ForwardRequest{Method: "POST", Path: "/x", Body: body})
	if err != nil {
		t.Fatalf("startRequest: %v", err)
	}
	_ = ch // pending channel is released when the hub shuts down

	frame := client.recv(t)
	if frame.Type != FrameRequest {
		t.Fatalf("expected request frame, got %#v", frame)
	}
	if !frame.Compressed {
		t.Fatal("expected large request body to be compressed for a gzip node")
	}
	// The compressed wire body must not equal the plain base64 of the original.
	if frame.BodyB64 == base64Plain(t, body) {
		t.Fatal("compressed body should differ from plain base64")
	}
	// Decoding must round-trip the original bytes.
	got := decodeBodyB64(frame.BodyB64, frame.Compressed)
	if !bytes.Equal(got, body) {
		t.Fatalf("decoded body mismatch: got %d bytes want %d", len(got), len(body))
	}
	// The compressed representation must actually be smaller.
	if len(frame.BodyB64) >= len(base64Plain(t, body)) {
		t.Fatal("expected gzip to shrink the repeated payload")
	}
}

func TestRelayGzipSmallRequestStaysPlain(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return true })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "x", Version: "v1.2.0", Caps: []string{CapGzip}})
	if ok := client.recv(t); ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ch, err := hub.startRequest(ctx, "node-a", ForwardRequest{Method: "GET", Path: "/x", Body: []byte("small")})
	if err != nil {
		t.Fatalf("startRequest: %v", err)
	}
	_ = ch

	frame := client.recv(t)
	if frame.Compressed {
		t.Fatal("small body must not be compressed")
	}
	if got := decodeBodyB64(frame.BodyB64, false); string(got) != "small" {
		t.Fatalf("expected plain small body, got %q", got)
	}
}

func TestRelayGzipOldNodeStaysPlain(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return true })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	// Old node: hello without the gzip capability.
	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "x", Version: "v1.2.0"})
	if ok := client.recv(t); ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ch, err := hub.startRequest(ctx, "node-a", ForwardRequest{Method: "POST", Path: "/x", Body: bigBody("old-node-")})
	if err != nil {
		t.Fatalf("startRequest: %v", err)
	}
	_ = ch

	frame := client.recv(t)
	if frame.Compressed {
		t.Fatal("old node must never receive compressed bodies")
	}
	if got := decodeBodyB64(frame.BodyB64, false); !bytes.Equal(got, bigBody("old-node-")) {
		t.Fatal("plain body mismatch")
	}
}

func TestRelayGzipResponseRoundTrip(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return true })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "x", Version: "v1.2.0", Caps: []string{CapGzip}})
	if ok := client.recv(t); ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}

	// Mock node: wait for the request, then reply with a compressed large body.
	respBody := bigBody("response-payload-")
	go func() {
		req := client.recv(t)
		if req.Type != FrameRequest {
			t.Errorf("expected request frame, got %#v", req)
			return
		}
		b64, compressed := encodeBodyB64(respBody, true)
		client.send(t, Frame{Type: FrameResponse, ReqID: req.ReqID, Status: 200, BodyB64: b64, Compressed: compressed})
	}()

	resp, err := hub.Forward(context.Background(), "node-a", ForwardRequest{Method: "GET", Path: "/y"})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if !bytes.Equal(resp.Body, respBody) {
		t.Fatalf("response body mismatch: got %d bytes want %d", len(resp.Body), len(respBody))
	}
}

// TestRelayGzipAgentToOldHubPlain verifies a gzip-capable agent talking to an
// old hub (hello_ok without caps) keeps sending plain bodies.
func TestRelayGzipAgentToOldHubPlain(t *testing.T) {
	// Build a minimal "old hub": a websocket server that answers hello with a
	// hello_ok carrying no caps, then issues a large request.
	oldHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// hello
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		hello, err := DecodeFrame(data)
		if err != nil || hello.Type != FrameHello {
			return
		}
		if err := writeFrame(conn, Frame{Type: FrameHelloOK, Version: hello.Version}); err != nil {
			return
		}
		// issue a large request (plain, as an old hub would)
		body := bigBody("old-hub-request-")
		req := Frame{
			Type:    FrameRequest,
			ReqID:   "r_old",
			Method:  "POST",
			Path:    "/big",
			BodyB64: base64.StdEncoding.EncodeToString(body),
		}
		if err := writeFrame(conn, req); err != nil {
			return
		}
		// read the response and assert the agent did not compress it.
		deadline := time.Now().Add(5 * time.Second)
		_ = conn.SetReadDeadline(deadline)
		_, rdata, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read agent response: %v", err)
			return
		}
		resp, err := DecodeFrame(rdata)
		if err != nil {
			t.Errorf("decode agent response: %v", err)
			return
		}
		if resp.Compressed {
			t.Error("agent must not compress responses to an old hub")
		}
		if got := decodeBodyB64(resp.BodyB64, false); !bytes.Equal(got, body) {
			t.Error("agent response body mismatch")
		}
	}))
	defer oldHub.Close()

	agent := NewAgent(AgentConfig{
		CenterURL:  "ws" + strings.TrimPrefix(oldHub.URL, "http") + "/relay",
		NodeID:     "node-new",
		Credential: "x",
		Version:    "v1.2.0",
		Caps:       []string{CapGzip},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			_, _ = w.Write(data) // echo the body back
		}),
	})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())

	// Wait for the old hub to finish its exchange (it fails the test on mismatch).
	time.Sleep(500 * time.Millisecond)
}

// TestRelayGzipAgentRoundTrip exercises the full real-agent path: hub
// compresses the large request, the agent decompresses it, echoes a large
// response, and the hub decompresses that.
func TestRelayGzipAgentRoundTrip(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return true })
	agent := NewAgent(AgentConfig{
		CenterURL:  wsURL,
		NodeID:     "node-gzip",
		Credential: "x",
		Version:    "v1.2.0",
		Caps:       []string{CapGzip},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			_, _ = w.Write(append([]byte("echo:"), data...))
		}),
	})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-gzip")

	body := bigBody("agent-roundtrip-")
	resp, err := hub.Forward(context.Background(), "node-gzip", ForwardRequest{
		Method: "POST", Path: "/echo", Body: body,
	})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	want := append([]byte("echo:"), body...)
	if !bytes.Equal(resp.Body, want) {
		t.Fatalf("round-trip body mismatch: got %d bytes want %d", len(resp.Body), len(want))
	}
}
