package backend

import (
	"context"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/tim5wang/godex/internal/domain/events"
)

func TestMcpBridgeSpawnFailureEmitsWarningAndReturns(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{})
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "acp", Key: "bridge-failure"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	eventCh := make(chan events.Event, 1)
	unsubscribe, err := service.AttachSink(opened.SessionID, events.SinkFunc(func(event events.Event) {
		if event.Type == events.EventWarningRaised {
			select {
			case eventCh <- event:
			default:
			}
		}
	}))
	if err != nil {
		t.Fatalf("attach sink: %v", err)
	}
	defer unsubscribe()
	service.BridgeACPMCPServers(context.Background(), opened.SessionID, []acp.McpServer{{
		Stdio: &acp.McpServerStdio{Name: "bad", Command: "/definitely/missing/godex-mcp-server"},
	}})
	select {
	case event := <-eventCh:
		payload, ok := event.Payload.(map[string]any)
		if !ok || !strings.Contains(payload["message"].(string), "ACP MCP bridge failed") {
			t.Fatalf("unexpected warning payload: %#v", event.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("expected bridge warning")
	}
}
