package relay

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// testNodeClient is a minimal node-side WebSocket peer used to exercise the hub.
type testNodeClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func dialTestNode(t *testing.T, serverURL string) *testNodeClient {
	t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.Dial(serverURL, nil)
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	return &testNodeClient{conn: conn}
}

func (c *testNodeClient) send(t *testing.T, frame Frame) {
	t.Helper()
	if err := c.sendErr(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// sendErr is the goroutine-safe variant used inside node reader loops where a
// closed connection is expected during teardown and must not fail the test.
func (c *testNodeClient) sendErr(frame Frame) error {
	data, err := EncodeFrame(frame)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

func (c *testNodeClient) recv(t *testing.T) Frame {
	t.Helper()
	frame, err := c.recvErr()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return frame
}

// recvErr is the goroutine-safe variant used inside node reader loops where a
// closed connection is expected during teardown and must not fail the test.
func (c *testNodeClient) recvErr() (Frame, error) {
	c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		return Frame{}, err
	}
	return DecodeFrame(data)
}

func (c *testNodeClient) close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func newTestHub(validate func(nodeID, credential string) bool) (*Hub, *httptest.Server) {
	hub := NewHub(validate)
	server := httptest.NewServer(hub)
	return hub, server
}

func TestHubAcceptsHelloAndRegistersNode(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool {
		return nodeID == "node-a" && credential == "ck_secret"
	})
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()

	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "ck_secret", Version: "v1.2.0", Caps: []string{"chat"}})
	ok := client.recv(t)
	if ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}

	if !hub.IsOnline("node-a") {
		t.Fatal("expected node-a to be online after hello")
	}
}

func TestHubRejectsBadCredential(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()

	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "wrong"})
	rejected := client.recv(t)
	if rejected.Type != FrameHelloError {
		t.Fatalf("expected hello_err, got %#v", rejected)
	}
	if hub.IsOnline("node-a") {
		t.Fatal("expected node-a to stay offline after rejected hello")
	}
}

func TestHubForwardRoundTrip(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "ck_secret"})
	if got := client.recv(t); got.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", got)
	}

	// Node read loop: answer every request with a mirrored response.
	go func() {
		for {
			frame, err := client.recvErr()
			if err != nil {
				return
			}
			if frame.Type != FrameRequest {
				continue
			}
			_ = client.sendErr(Frame{
				Type:    FrameResponse,
				ReqID:   frame.ReqID,
				Status:  200,
				BodyB64: base64.StdEncoding.EncodeToString([]byte("pong:" + frame.Path)),
			})
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := hub.Forward(ctx, "node-a", ForwardRequest{Method: "GET", Path: "/meta"})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("expected status 200, got %d", resp.Status)
	}
	if string(resp.Body) != "pong:/meta" {
		t.Fatalf("unexpected body %q", resp.Body)
	}
}

func TestHubForwardOfflineNode(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return true })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := hub.Forward(ctx, "node-ghost", ForwardRequest{Method: "GET", Path: "/meta"})
	if err == nil {
		t.Fatal("expected error forwarding to offline node")
	}
	if !errors.Is(err, ErrNodeOffline) {
		t.Fatalf("expected ErrNodeOffline, got %v", err)
	}
}

func TestHubForwardMultiplexesByReqID(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return credential == "ck_secret" })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "ck_secret"})
	if got := client.recv(t); got.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", got)
	}

	// Node echoes each request's path as the response body, delayed per request.
	go func() {
		for {
			frame, err := client.recvErr()
			if err != nil {
				return
			}
			if frame.Type != FrameRequest {
				continue
			}
			delay := 20 * time.Millisecond
			if strings.Contains(frame.Path, "slow") {
				delay = 100 * time.Millisecond
			}
			time.Sleep(delay)
			_ = client.sendErr(Frame{
				Type:    FrameResponse,
				ReqID:   frame.ReqID,
				Status:  200,
				BodyB64: base64.StdEncoding.EncodeToString([]byte(frame.Path)),
			})
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fastCh := make(chan *ForwardResponse, 1)
	slowCh := make(chan *ForwardResponse, 1)
	errCh := make(chan error, 2)
	go func() {
		resp, err := hub.Forward(ctx, "node-a", ForwardRequest{Method: "GET", Path: "/fast"})
		if err != nil {
			errCh <- err
			return
		}
		fastCh <- resp
	}()
	go func() {
		resp, err := hub.Forward(ctx, "node-a", ForwardRequest{Method: "GET", Path: "/slow"})
		if err != nil {
			errCh <- err
			return
		}
		slowCh <- resp
	}()

	select {
	case err := <-errCh:
		t.Fatalf("forward error: %v", err)
	case resp := <-fastCh:
		if string(resp.Body) != "/fast" {
			t.Fatalf("fast response mismatch: %q", resp.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fast response")
	}
	select {
	case err := <-errCh:
		t.Fatalf("forward error: %v", err)
	case resp := <-slowCh:
		if string(resp.Body) != "/slow" {
			t.Fatalf("slow response mismatch: %q", resp.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for slow response")
	}
}

func TestHubForwardTimesOut(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return credential == "ck_secret" })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "ck_secret"})
	if got := client.recv(t); got.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", got)
	}

	// Node never answers.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := hub.Forward(ctx, "node-a", ForwardRequest{Method: "GET", Path: "/hang"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestHubPingPongKeepalive(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return credential == "ck_secret" })
	hub.SetPingInterval(50 * time.Millisecond)
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "ck_secret"})
	if got := client.recv(t); got.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", got)
	}

	// Node answers pings with pongs.
	go func() {
		for {
			frame, err := client.recvErr()
			if err != nil {
				return
			}
			switch frame.Type {
			case FramePing:
				_ = client.sendErr(Frame{Type: FramePong, Seq: frame.Seq})
			case FrameRequest:
				_ = client.sendErr(Frame{Type: FrameResponse, ReqID: frame.ReqID, Status: 200})
			}
		}
	}()

	time.Sleep(200 * time.Millisecond)
	if !hub.IsOnline("node-a") {
		t.Fatal("expected node-a to stay online across pings")
	}
	lastPong := hub.LastPong("node-a")
	if lastPong.IsZero() {
		t.Fatal("expected hub to record at least one pong")
	}
}

func TestHubRemovesNodeOnDisconnect(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return credential == "ck_secret" })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "ck_secret"})
	if got := client.recv(t); got.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", got)
	}
	client.close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !hub.IsOnline("node-a") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected node-a to be removed after disconnect")
}

// ensure Hub implements http.Handler at compile time.
var _ http.Handler = (*Hub)(nil)
