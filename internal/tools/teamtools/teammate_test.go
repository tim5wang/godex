package teamtools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/teammate"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/task"
)

func TestBroadcastToolTargetsKnownTeammates(t *testing.T) {
	workspace := t.TempDir()
	teamDir := filepath.Join(workspace, ".team")
	tasksDir := filepath.Join(workspace, ".tasks")
	inboxDir := filepath.Join(teamDir, "inbox")

	if err := os.MkdirAll(inboxDir, 0755); err != nil {
		t.Fatalf("mkdir inbox dir: %v", err)
	}
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("mkdir tasks dir: %v", err)
	}

	teammates := map[string]*teammate.Teammate{
		"alice": {
			Name:       "alice",
			Role:       "researcher",
			Status:     teammate.StatusIdle,
			CreatedAt:  time.Now(),
			LastActive: time.Now(),
		},
		"bob": {
			Name:       "bob",
			Role:       "builder",
			Status:     teammate.StatusIdle,
			CreatedAt:  time.Now(),
			LastActive: time.Now(),
		},
	}
	data, err := json.Marshal(teammates)
	if err != nil {
		t.Fatalf("marshal teammates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(teamDir, "config.json"), data, 0644); err != nil {
		t.Fatalf("write teammate config: %v", err)
	}

	bus := message.NewBus(inboxDir)
	manager := teammate.NewManager(workspace, teamDir, task.NewManager(tasksDir), bus, "", nil)
	tool := NewBroadcastTool(bus, manager, "captain")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"content": "hello team"})
	if err != nil {
		t.Fatalf("broadcast execute: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty broadcast result")
	}

	for _, name := range []string{"alice", "bob"} {
		msgs := bus.PeekInbox(name)
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message for %s, got %d", name, len(msgs))
		}
		if msgs[0].To != name {
			t.Fatalf("expected message recipient %q, got %q", name, msgs[0].To)
		}
		if msgs[0].From != "captain" {
			t.Fatalf("expected broadcast sender %q, got %q", "captain", msgs[0].From)
		}
	}

	if msgs := bus.PeekInbox("all"); len(msgs) != 0 {
		t.Fatalf("expected no messages in synthetic 'all' inbox, got %d", len(msgs))
	}
}

func TestPlanApprovalTargetsRequestedTeammateOnly(t *testing.T) {
	workspace := t.TempDir()
	teamDir := filepath.Join(workspace, ".team")
	tasksDir := filepath.Join(workspace, ".tasks")
	inboxDir := filepath.Join(teamDir, "inbox")

	if err := os.MkdirAll(inboxDir, 0755); err != nil {
		t.Fatalf("mkdir inbox dir: %v", err)
	}
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("mkdir tasks dir: %v", err)
	}

	teammates := map[string]*teammate.Teammate{
		"alice": {Name: "alice", Role: "researcher", Status: teammate.StatusIdle},
		"bob":   {Name: "bob", Role: "builder", Status: teammate.StatusIdle},
	}
	data, err := json.Marshal(teammates)
	if err != nil {
		t.Fatalf("marshal teammates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(teamDir, "config.json"), data, 0644); err != nil {
		t.Fatalf("write teammate config: %v", err)
	}

	bus := message.NewBus(inboxDir)
	manager := teammate.NewManager(workspace, teamDir, task.NewManager(tasksDir), bus, "", nil)
	tool := NewPlanApprovalTool(bus, manager, "captain")

	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"request_id": "req-1",
		"teammate":   "alice",
		"approve":    true,
	}); err != nil {
		t.Fatalf("plan approval execute: %v", err)
	}

	if got := bus.PeekInbox("alice"); len(got) != 1 || got[0].Type != message.MsgTypePlanApprovalResponse {
		t.Fatalf("expected one approval response for alice, got %+v", got)
	}
	if got := bus.PeekInbox("bob"); len(got) != 0 {
		t.Fatalf("expected bob to receive no approval response, got %+v", got)
	}
}
