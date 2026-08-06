package relay

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHubDisconnectForcesNodeOffline(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return credential == "ck_secret" })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	client := dialTestNode(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()

	client.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "ck_secret"})
	if got := client.recv(t); got.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", got)
	}
	if !hub.IsOnline("node-a") {
		t.Fatal("expected node-a to be online before disconnect")
	}

	hub.Disconnect("node-a")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !hub.IsOnline("node-a") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected node-a to be removed by Disconnect")
}

func TestHubDisconnectUnknownNodeIsNoop(t *testing.T) {
	hub, server := newTestHub(func(nodeID, credential string) bool { return true })
	defer server.Close()
	defer hub.Shutdown(context.Background())

	// Must not panic and must not affect other nodes.
	hub.Disconnect("ghost")
	if hub.IsOnline("ghost") {
		t.Fatal("expected ghost to stay offline")
	}
	_ = context.Background()
}
