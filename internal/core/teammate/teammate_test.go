package teammate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/platform/localstore"
	"github.com/tim5wang/godex/internal/toolruntime"
)

func TestWorkspaceBoundToolHelpers(t *testing.T) {
	workspace := t.TempDir()
	teamDir := filepath.Join(workspace, ".team")
	tasksDir := filepath.Join(workspace, ".tasks")

	if err := os.MkdirAll(teamDir, 0755); err != nil {
		t.Fatalf("mkdir team dir: %v", err)
	}
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("mkdir tasks dir: %v", err)
	}

	bus := localstore.NewMessageBus(filepath.Join(teamDir, "inbox"))
	manager := NewManager(workspace, teamDir, localstore.NewTaskManager(tasksDir), bus, "", nil)

	out, err := manager.tooling.RunShell(context.Background(), "pwd")
	if err != nil {
		t.Fatalf("run bash: %v", err)
	}
	if strings.TrimSpace(out) != workspace {
		t.Fatalf("expected bash to run in %q, got %q", workspace, strings.TrimSpace(out))
	}

	if _, err := manager.tooling.WriteFile("notes/example.txt", "hello world"); err != nil {
		t.Fatalf("write file: %v", err)
	}

	content, err := manager.tooling.ReadFile("notes/example.txt", 0)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if content != "hello world" {
		t.Fatalf("expected file content %q, got %q", "hello world", content)
	}

	if _, err := manager.tooling.EditFile("notes/example.txt", "world", "godex"); err != nil {
		t.Fatalf("edit file: %v", err)
	}

	content, err = manager.tooling.ReadFile("notes/example.txt", 0)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if content != "hello godex" {
		t.Fatalf("expected edited content %q, got %q", "hello godex", content)
	}

	content, err = manager.tooling.ReadFile(filepath.Join(filepath.Base(workspace), "notes", "example.txt"), 0)
	if err != nil {
		t.Fatalf("read duplicated basename path: %v", err)
	}
	if content != "hello godex" {
		t.Fatalf("expected duplicated basename read content %q, got %q", "hello godex", content)
	}

	if _, err := manager.tooling.ReadFile("../outside.txt", 0); err == nil {
		t.Fatal("expected reading outside workspace to fail")
	}
	if _, err := manager.tooling.WriteFile("../outside.txt", "nope"); err == nil {
		t.Fatal("expected writing outside workspace to fail")
	}
}

func TestConsumeInboxMessagesResumesTeammateWork(t *testing.T) {
	workspace := t.TempDir()
	teamDir := filepath.Join(workspace, ".team")
	tasksDir := filepath.Join(workspace, ".tasks")

	if err := os.MkdirAll(filepath.Join(teamDir, "inbox"), 0755); err != nil {
		t.Fatalf("mkdir inbox dir: %v", err)
	}
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("mkdir tasks dir: %v", err)
	}

	bus := localstore.NewMessageBus(filepath.Join(teamDir, "inbox"))
	manager := NewManager(workspace, teamDir, localstore.NewTaskManager(tasksDir), bus, "", nil)
	manager.teammates["worker"] = &Teammate{Name: "worker", Role: "builder", Status: StatusIdle, generation: 1}
	manager.ensureWakeChannelLocked("worker")

	if err := bus.Send(message.Message{
		Type:    message.MsgTypeMessage,
		From:    "lead",
		To:      "worker",
		Content: "please continue",
	}); err != nil {
		t.Fatalf("send inbox message: %v", err)
	}

	msgs, ack, resumed := manager.consumeInboxMessages("worker", 1)
	if !resumed {
		t.Fatal("expected inbox message to resume teammate work")
	}
	got, err := manager.Get("worker")
	if err != nil {
		t.Fatalf("get teammate: %v", err)
	}
	if got.Status != StatusWorking {
		t.Fatalf("expected teammate status %s, got %s", StatusWorking, got.Status)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected one runtime inbox message, got %d entries", len(msgs))
	}
	if msgs[0].Metadata == nil || msgs[0].Metadata.Kind != protocol.KindInbox {
		t.Fatalf("expected inbox runtime metadata, got %+v", msgs[0].Metadata)
	}
	if got := protocol.MessageText(msgs[0]); !strings.Contains(got, "Inbox updates") {
		t.Fatalf("expected readable inbox summary, got %q", got)
	}
	if got := bus.PeekInbox("worker"); len(got) != 1 {
		t.Fatalf("expected inbox preview to remain until ack, got %d messages", len(got))
	}
	ack()
	if got := bus.PeekInbox("worker"); len(got) != 0 {
		t.Fatalf("expected inbox to be acked away, got %d messages", len(got))
	}
}

func TestGetAndListReturnTeammateSnapshots(t *testing.T) {
	workspace := t.TempDir()
	teamDir := filepath.Join(workspace, ".team")
	tasksDir := filepath.Join(workspace, ".tasks")
	bus := localstore.NewMessageBus(filepath.Join(teamDir, "inbox"))
	manager := NewManager(workspace, teamDir, localstore.NewTaskManager(tasksDir), bus, "", nil)

	manager.teammates["worker"] = &Teammate{Name: "worker", Role: "builder", Status: StatusIdle, generation: 1}

	got, err := manager.Get("worker")
	if err != nil {
		t.Fatalf("get teammate: %v", err)
	}
	got.Status = StatusShutdown

	list := manager.List()
	list[0].Status = StatusShutdown

	again, err := manager.Get("worker")
	if err != nil {
		t.Fatalf("get teammate again: %v", err)
	}
	if again.Status != StatusIdle {
		t.Fatalf("expected snapshot mutation not to leak, got %s", again.Status)
	}
}

func TestManagerLoopToolFactoriesAreConfigurable(t *testing.T) {
	workspace := t.TempDir()
	teamDir := filepath.Join(workspace, ".team")
	tasksDir := filepath.Join(workspace, ".tasks")
	bus := localstore.NewMessageBus(filepath.Join(teamDir, "inbox"))
	client := fakeCaller{}
	manager := NewManager(workspace, teamDir, localstore.NewTaskManager(tasksDir), bus, "test-model", client, testLoopToolFactories()...)

	if manager.client != client {
		t.Fatal("expected manager to keep injected conversation caller")
	}

	factories := []LoopToolFactory{
		func(LoopToolContext) toolruntime.Tool { return newFakeLoopTool("custom") },
	}
	manager.SetLoopToolFactories(factories)
	factories[0] = func(LoopToolContext) toolruntime.Tool { return newFakeLoopTool("mutated") }

	if got := manager.newLoopToolHandler("worker", 1).List(); !reflect.DeepEqual(got, []string{"custom"}) {
		t.Fatalf("expected custom loop tools, got %v", got)
	}

	manager.SetLoopToolFactories(nil)
	names := manager.newLoopToolHandler("worker", 1).List()
	if !reflect.DeepEqual(names, []string{"bash", "edit_file", "idle", "read_file", "task", "write_file"}) {
		t.Fatalf("expected default loop tools after reset, got %v", names)
	}
}

func TestIdleWaitWakesOnInboxAndShutdown(t *testing.T) {
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

	bus := localstore.NewMessageBus(inboxDir)
	manager := NewManager(workspace, teamDir, localstore.NewTaskManager(tasksDir), bus, "", nil)
	manager.teammates["worker"] = &Teammate{Name: "worker", Role: "builder", Status: StatusIdle, generation: 1}
	manager.ensureWakeChannelLocked("worker")

	start := time.Now()
	done := make(chan struct{})
	go func() {
		manager.waitForIdleCheck(context.Background(), "worker", 5*time.Second)
		close(done)
	}()

	if err := bus.Send(message.Message{
		Type:    message.MsgTypeMessage,
		From:    "lead",
		To:      "worker",
		Content: "wake up",
	}); err != nil {
		t.Fatalf("send inbox message: %v", err)
	}

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected inbox notification to wake idle wait promptly")
	}
	if time.Since(start) >= time.Second {
		t.Fatalf("expected inbox wake to be prompt, took %s", time.Since(start))
	}

	start = time.Now()
	done = make(chan struct{})
	go func() {
		manager.waitForIdleCheck(context.Background(), "worker", 5*time.Second)
		close(done)
	}()

	if err := manager.ShutdownTeammate("worker"); err != nil {
		t.Fatalf("shutdown teammate: %v", err)
	}

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected shutdown request to wake idle wait promptly")
	}
	if time.Since(start) >= time.Second {
		t.Fatalf("expected shutdown wake to be prompt, took %s", time.Since(start))
	}
}

func TestLoadAllNormalizesLegacyStatusesWithoutRuntimeResume(t *testing.T) {
	workspace := t.TempDir()
	teamDir := filepath.Join(workspace, ".team")
	tasksDir := filepath.Join(workspace, ".tasks")
	if err := os.MkdirAll(filepath.Join(teamDir, "inbox"), 0755); err != nil {
		t.Fatalf("mkdir inbox dir: %v", err)
	}
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("mkdir tasks dir: %v", err)
	}

	manager := NewManager(workspace, teamDir, localstore.NewTaskManager(tasksDir), localstore.NewMessageBus(filepath.Join(teamDir, "inbox")), "", nil)
	payload, err := json.Marshal(map[string]*Teammate{
		"worker":   {Name: "worker", Role: "builder", Prompt: "build", Status: StatusWorking},
		"stopping": {Name: "stopping", Role: "builder", Prompt: "build", Status: StatusShuttingDown},
		"legacy":   {Name: "legacy", Role: "builder", Prompt: "build", Status: Status("shutdowning")},
	})
	if err != nil {
		t.Fatalf("marshal teammates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(teamDir, "config.json"), payload, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	manager.loadAll()

	if got := manager.teammates["worker"]; got == nil || got.Status != StatusIdle || got.ctx != nil || got.cancel != nil {
		t.Fatalf("expected working teammate to normalize to idle without runtime resume, got %+v", got)
	}
	if got := manager.teammates["stopping"]; got == nil || got.Status != StatusShutdown {
		t.Fatalf("expected shutting_down teammate to normalize to shutdown, got %+v", got)
	}
	if got := manager.teammates["legacy"]; got == nil || got.Status != StatusShutdown {
		t.Fatalf("expected legacy shutdowning teammate to normalize to shutdown, got %+v", got)
	}
}

func TestLoadAllPreservesInboxWakeBehaviorForLoadedTeammates(t *testing.T) {
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

	bus := localstore.NewMessageBus(inboxDir)
	manager := NewManager(workspace, teamDir, localstore.NewTaskManager(tasksDir), bus, "", nil)
	payload, err := json.Marshal(map[string]*Teammate{
		"worker": {Name: "worker", Role: "builder", Prompt: "build", Status: StatusIdle},
	})
	if err != nil {
		t.Fatalf("marshal teammates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(teamDir, "config.json"), payload, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	manager.loadAll()

	done := make(chan struct{})
	go func() {
		manager.waitForIdleCheck(context.Background(), "worker", 5*time.Second)
		close(done)
	}()

	if err := bus.Send(message.Message{
		Type:    message.MsgTypeMessage,
		From:    "lead",
		To:      "worker",
		Content: "wake up",
	}); err != nil {
		t.Fatalf("send inbox message: %v", err)
	}

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected loaded teammate inbox notification to wake idle wait")
	}
}

type fakeCaller struct{}

func (fakeCaller) Call(context.Context, protocol.Request) (*protocol.Response, error) {
	return &protocol.Response{}, nil
}

func newFakeLoopTool(name string) toolruntime.Tool {
	return toolruntime.NewTypedTool(toolruntime.NewToolSpec(name, "fake", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(context.Context, struct{}) (toolruntime.ToolResult, error) {
		return toolruntime.ToolResult{Text: "ok"}, nil
	})
}

func testLoopToolFactories() []LoopToolFactory {
	names := []string{"bash", "edit_file", "idle", "read_file", "task", "write_file"}
	factories := make([]LoopToolFactory, 0, len(names))
	for _, name := range names {
		name := name
		factories = append(factories, func(LoopToolContext) toolruntime.Tool {
			return newFakeLoopTool(name)
		})
	}
	return factories
}
