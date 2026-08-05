package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// startForwardCenter boots a hub, attaches a fake node, and serves the forward
// endpoint. It returns the forward WS URL and the fake node client.
func startForwardCenter(t *testing.T) (string, *testNodeClient, *Hub, *httptest.Server) {
	t.Helper()
	hub, hubServer := newTestHub(func(nodeID, credential string) bool { return true })
	t.Cleanup(func() { hubServer.Close(); _ = hub.Shutdown(context.Background()) })

	node := dialTestNode(t, "ws"+strings.TrimPrefix(hubServer.URL, "http"))
	t.Cleanup(node.close)
	node.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "x", Version: "v1.2.0"})
	if ok := node.recv(t); ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}

	forwardServer := httptest.NewServer(NewForwardHandler(hub, nil))
	t.Cleanup(forwardServer.Close)
	wsURL := "ws" + strings.TrimPrefix(forwardServer.URL, "http") + "/control/nodes/node-a/forward"
	return wsURL, node, hub, forwardServer
}

func TestForwardClientOpensNodeStreamBidirectionally(t *testing.T) {
	wsURL, node, _, _ := startForwardCenter(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialForward(ctx, wsURL, "")
	if err != nil {
		t.Fatalf("DialForward: %v", err)
	}
	defer client.Close()

	stream, err := client.Open("127.0.0.1:3306")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer stream.Close()

	// Node sees the tcp_open with the requested target.
	open := node.recv(t)
	if open.Type != FrameTCPOpen {
		t.Fatalf("expected tcp_open, got %#v", open)
	}
	var op TCPOpenPayload
	if err := json.Unmarshal(open.Payload, &op); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if op.Target != "127.0.0.1:3306" || op.ConnID == "" {
		t.Fatalf("tcp_open payload mismatch: %#v", op)
	}

	// CLI → node: stream.Write reaches the node as tcp_data.
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	data := node.recv(t)
	if data.Type != FrameTCPData {
		t.Fatalf("expected tcp_data, got %#v", data)
	}
	chunk, err := decodedTCPChunk(data)
	if err != nil {
		t.Fatalf("chunk decode: %v", err)
	}
	if string(chunk) != "ping" {
		t.Fatalf("chunk mismatch: %q", chunk)
	}

	// Node → CLI: node tcp_data reaches stream.Read.
	node.send(t, Frame{Type: FrameTCPData, Payload: tcpDataPayloadJSON(op.ConnID), BodyB64: base64.StdEncoding.EncodeToString([]byte("pong"))})
	buf := make([]byte, 64)
	n, err := stream.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != "pong" {
		t.Fatalf("read mismatch: %q", buf[:n])
	}
}

func TestForwardClientReadEOFOnNodeClose(t *testing.T) {
	wsURL, node, _, _ := startForwardCenter(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialForward(ctx, wsURL, "")
	if err != nil {
		t.Fatalf("DialForward: %v", err)
	}
	defer client.Close()

	stream, err := client.Open("127.0.0.1:3306")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer stream.Close()

	open := node.recv(t)
	var op TCPOpenPayload
	if err := json.Unmarshal(open.Payload, &op); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}

	// Node closes the stream; the CLI side must observe io.EOF.
	node.send(t, Frame{Type: FrameTCPClose, Payload: tcpClosePayloadJSON(op.ConnID, "node done")})
	buf := make([]byte, 64)
	if _, err := stream.Read(buf); err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestForwardClientCloseSendsCloseToNode(t *testing.T) {
	wsURL, node, _, _ := startForwardCenter(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialForward(ctx, wsURL, "")
	if err != nil {
		t.Fatalf("DialForward: %v", err)
	}
	defer client.Close()

	stream, err := client.Open("127.0.0.1:3306")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	open := node.recv(t)
	var op TCPOpenPayload
	if err := json.Unmarshal(open.Payload, &op); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	closeFrame := node.recv(t)
	if closeFrame.Type != FrameTCPClose {
		t.Fatalf("expected tcp_close, got %#v", closeFrame)
	}
}

// TestForwardHandlerRejectsOfflineNode verifies the endpoint returns a tcp_close
// reply (rather than hanging) when the target node is offline.
func TestForwardHandlerRejectsOfflineNode(t *testing.T) {
	hub, hubServer := newTestHub(func(nodeID, credential string) bool { return true })
	defer hubServer.Close()
	defer hub.Shutdown(context.Background())

	forwardServer := httptest.NewServer(NewForwardHandler(hub, nil))
	defer forwardServer.Close()
	wsURL := "ws" + strings.TrimPrefix(forwardServer.URL, "http") + "/control/nodes/missing/forward"

	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial forward: %v", err)
	}
	defer conn.Close()

	payload, _ := json.Marshal(TCPOpenPayload{ConnID: "c_1", Target: "127.0.0.1:3306"})
	if err := writeFrame(conn, Frame{Type: FrameTCPOpen, Payload: payload}); err != nil {
		t.Fatalf("send tcp_open: %v", err)
	}
	reply, err := readFrameTimeout(conn, 3*time.Second)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if reply.Type != FrameTCPClose {
		t.Fatalf("expected tcp_close error reply, got %#v", reply)
	}
}

func readFrameTimeout(conn *websocket.Conn, d time.Duration) (Frame, error) {
	_ = conn.SetReadDeadline(time.Now().Add(d))
	_, data, err := conn.ReadMessage()
	if err != nil {
		return Frame{}, err
	}
	return DecodeFrame(data)
}
