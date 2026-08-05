package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func openTestStream(t *testing.T, hub *Hub, nodeID, connID, target string) io.ReadWriteCloser {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := hub.OpenTCPStream(ctx, nodeID, connID, target)
	if err != nil {
		t.Fatalf("OpenTCPStream: %v", err)
	}
	return stream
}

func decodeTCPOpen(t *testing.T, frame Frame) TCPOpenPayload {
	t.Helper()
	var payload TCPOpenPayload
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	return payload
}

func TestHubOpenTCPStreamSendsOpenToNode(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool {
		return nodeID == "node-a" && credential == "ck_secret"
	})
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "ck_secret", Version: "v1.2.0"})
	if ok := client.recv(t); ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}

	stream := openTestStream(t, hub, "node-a", "c_1", "10.0.0.5:3306")
	defer stream.Close()

	open := client.recv(t)
	if open.Type != FrameTCPOpen {
		t.Fatalf("expected tcp_open, got %#v", open)
	}
	payload := decodeTCPOpen(t, open)
	if payload.ConnID != "c_1" || payload.Target != "10.0.0.5:3306" {
		t.Fatalf("tcp_open payload mismatch: %#v", payload)
	}
}

func TestHubOpenTCPStreamOfflineNode(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return true })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := hub.OpenTCPStream(ctx, "node-missing", "c_1", "127.0.0.1:1"); !errors.Is(err, ErrNodeOffline) {
		t.Fatalf("expected ErrNodeOffline, got %v", err)
	}
}

func TestHubTCPStreamWriteForwardsDataToNode(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return true })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "x", Version: "v1.2.0"})
	if ok := client.recv(t); ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}

	stream := openTestStream(t, hub, "node-a", "c_2", "127.0.0.1:3306")
	defer stream.Close()
	if open := client.recv(t); open.Type != FrameTCPOpen {
		t.Fatalf("expected tcp_open, got %#v", open)
	}

	if _, err := stream.Write([]byte("hello-node")); err != nil {
		t.Fatalf("write: %v", err)
	}
	data := client.recv(t)
	if data.Type != FrameTCPData {
		t.Fatalf("expected tcp_data, got %#v", data)
	}
	var dp TCPDataPayload
	if err := json.Unmarshal(data.Payload, &dp); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if dp.ConnID != "c_2" {
		t.Fatalf("tcp_data conn_id mismatch: %#v", dp)
	}
	chunk, err := decodedTCPChunk(data)
	if err != nil {
		t.Fatalf("chunk decode: %v", err)
	}
	if string(chunk) != "hello-node" {
		t.Fatalf("chunk mismatch: %q", chunk)
	}
}

func TestHubTCPStreamReadReceivesNodeData(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return true })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "x", Version: "v1.2.0"})
	if ok := client.recv(t); ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}

	stream := openTestStream(t, hub, "node-a", "c_3", "127.0.0.1:3306")
	defer stream.Close()
	if open := client.recv(t); open.Type != FrameTCPOpen {
		t.Fatalf("expected tcp_open, got %#v", open)
	}

	client.send(t, Frame{Type: FrameTCPData, Payload: tcpDataPayloadJSON("c_3"), BodyB64: base64.StdEncoding.EncodeToString([]byte("from-node"))})

	buf := make([]byte, 64)
	n, err := stream.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != "from-node" {
		t.Fatalf("read mismatch: %q", buf[:n])
	}
}

func TestHubTCPStreamCloseSendsCloseToNode(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return true })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "x", Version: "v1.2.0"})
	if ok := client.recv(t); ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}

	stream := openTestStream(t, hub, "node-a", "c_4", "127.0.0.1:3306")
	if open := client.recv(t); open.Type != FrameTCPOpen {
		t.Fatalf("expected tcp_open, got %#v", open)
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	closeFrame := client.recv(t)
	if closeFrame.Type != FrameTCPClose {
		t.Fatalf("expected tcp_close, got %#v", closeFrame)
	}
	var cp TCPClosePayload
	if err := json.Unmarshal(closeFrame.Payload, &cp); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if cp.ConnID != "c_4" {
		t.Fatalf("tcp_close conn_id mismatch: %#v", cp)
	}
}

func TestHubTCPStreamReadEOFOnNodeClose(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return true })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "x", Version: "v1.2.0"})
	if ok := client.recv(t); ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}

	stream := openTestStream(t, hub, "node-a", "c_5", "127.0.0.1:3306")
	defer stream.Close()
	if open := client.recv(t); open.Type != FrameTCPOpen {
		t.Fatalf("expected tcp_open, got %#v", open)
	}

	client.send(t, Frame{Type: FrameTCPClose, Payload: tcpClosePayloadJSON("c_5", "node done")})

	buf := make([]byte, 64)
	if _, err := stream.Read(buf); err != io.EOF {
		t.Fatalf("expected io.EOF on node close, got %v", err)
	}
}
