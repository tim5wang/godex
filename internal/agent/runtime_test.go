package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/modelcontext"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/tools"
)

type capturingCaller struct {
	req  protocol.Request
	resp protocol.Response
}

type sequenceCaller struct {
	responses []protocol.Response
	index     int
}

type repeatingCaller struct {
	response protocol.Response
	calls    int
}

type streamingCaller struct {
	response protocol.Response
	deltas   []string
}

type fakeCronManager struct{}

func (fakeCronManager) ListJobs() ([]automation.CronJob, error) { return nil, nil }
func (fakeCronManager) GetJob(jobID string) (automation.CronJob, error) {
	return automation.CronJob{ID: jobID}, nil
}
func (fakeCronManager) CreateJob(input automation.CronCreateInput) (automation.CronJob, error) {
	return automation.CronJob{ID: "job-1", Message: input.Message}, nil
}
func (fakeCronManager) UpdateJob(input automation.CronUpdateInput) (automation.CronJob, error) {
	return automation.CronJob{ID: input.ID}, nil
}
func (fakeCronManager) ToggleJob(jobID string, enabled bool) (automation.CronJob, error) {
	return automation.CronJob{ID: jobID, Enabled: enabled}, nil
}
func (fakeCronManager) DeleteJob(jobID string) error { return nil }
func (fakeCronManager) RunNow(ctx context.Context, jobID string) (automation.CronRunLog, error) {
	_ = ctx
	return automation.CronRunLog{ID: "run-1", JobID: jobID}, nil
}
func (fakeCronManager) ListRunLogs(jobID string, limit int) ([]automation.CronRunLog, error) {
	_ = limit
	return []automation.CronRunLog{{ID: "run-1", JobID: jobID}}, nil
}

func (c *capturingCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	c.req = req
	resp := c.resp
	return &resp, nil
}

func (c *sequenceCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	_ = req
	if c.index >= len(c.responses) {
		return &protocol.Response{}, nil
	}
	resp := c.responses[c.index]
	c.index++
	return &resp, nil
}

func (c *repeatingCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	_ = req
	c.calls++
	resp := c.response
	return &resp, nil
}

func (c *streamingCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	_ = req
	resp := c.response
	return &resp, nil
}

func (c *streamingCaller) Stream(ctx context.Context, req protocol.Request, handler conversation.StreamHandler) (*protocol.Response, error) {
	_ = ctx
	_ = req
	for _, delta := range c.deltas {
		if handler.OnTextDelta != nil {
			handler.OnTextDelta(delta)
		}
	}
	resp := c.response
	return &resp, nil
}

func TestRunWithOptionsEmitsLifecycleEvents(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = fakeCaller{resp: protocol.Response{
		Content: []protocol.Block{
			protocol.TextBlock("done"),
		},
	}}
	got := make([]events.EventType, 0, 3)

	err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID: "session-1",
		TurnID:    "turn-1",
		Sink: events.SinkFunc(func(event events.Event) {
			got = append(got, event.Type)
		}),
	})
	if err != nil {
		t.Fatalf("run with options: %v", err)
	}

	want := []events.EventType{
		events.EventModelRequestCompleted,
		events.EventAssistantTextDelta,
		events.EventAssistantMessageComplete,
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected emitted events: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected emitted events: got %v want %v", got, want)
		}
	}
}

func TestAPIRequestTimeoutUsesConfigWithDefaultFallback(t *testing.T) {
	if got := apiRequestTimeout(&config.Config{APITimeoutSeconds: 42}); got != 42*time.Second {
		t.Fatalf("expected configured timeout, got %s", got)
	}
	if got := apiRequestTimeout(&config.Config{}); got != 600*time.Second {
		t.Fatalf("expected default timeout fallback, got %s", got)
	}
}

func TestRunWithOptionsCheckpointsTranscriptAppends(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = &sequenceCaller{responses: []protocol.Response{
		{
			Content: []protocol.Block{
				protocol.ToolUseBlock("tool-1", "memory", map[string]interface{}{"action": "list"}),
			},
		},
		{
			Content: []protocol.Block{
				protocol.TextBlock("done"),
			},
		},
	}}

	var checkpoints []int
	err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID: "session-checkpoint",
		TurnID:    "turn-checkpoint",
		Checkpoint: func() {
			checkpoints = append(checkpoints, len(a.GetMessages()))
		},
	})
	if err != nil {
		t.Fatalf("run with options: %v", err)
	}
	want := []int{1, 2, 3}
	if len(checkpoints) != len(want) {
		t.Fatalf("expected %d checkpoints, got %v", len(want), checkpoints)
	}
	for i := range want {
		if checkpoints[i] != want[i] {
			t.Fatalf("unexpected checkpoint message counts: got %v want %v", checkpoints, want)
		}
	}
}

func TestRunWithOptionsAppendsLoopGuardFeedbackAndCheckpoint(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	repeated := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-1", "memory", map[string]interface{}{"action": "list"}),
	}}
	a.client = &sequenceCaller{responses: []protocol.Response{
		repeated,
		repeated,
		repeated,
		repeated,
		{Content: []protocol.Block{protocol.TextBlock("changed strategy")}},
	}}

	var checkpoints []int
	var recoveryPhase bool
	err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID: "session-loop-guard",
		TurnID:    "turn-loop-guard",
		Checkpoint: func() {
			checkpoints = append(checkpoints, len(a.GetMessages()))
		},
		EmitRunnerPhases: true,
		Sink: events.SinkFunc(func(event events.Event) {
			if event.Type != events.EventRunnerPhaseChanged {
				return
			}
			payload, _ := event.Payload.(events.RunnerPhasePayload)
			if payload.Phase == conversation.PhaseRecoveryAttempt && strings.Contains(payload.Message, "loop_guard_recovery") {
				recoveryPhase = true
			}
		}),
	})
	if err != nil {
		t.Fatalf("run with options: %v", err)
	}
	if !recoveryPhase {
		t.Fatal("expected loop guard recovery phase")
	}
	foundFeedback := false
	for _, msg := range a.GetMessages() {
		if strings.Contains(protocol.MessageText(msg), "loop_guard_recovery") {
			foundFeedback = true
			break
		}
	}
	if !foundFeedback {
		t.Fatalf("expected runtime feedback in transcript, got %+v", a.GetMessages())
	}
	if len(checkpoints) < 6 || checkpoints[len(checkpoints)-2] < 8 {
		t.Fatalf("expected checkpoints to include loop guard feedback and final answer, got %v", checkpoints)
	}
}

func TestRunWithOptionsUsesConfiguredMaxTurns(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cfg.MaxTurns = 2
	a.RegisterTools()
	caller := &repeatingCaller{response: protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-1", "memory", map[string]interface{}{"action": "list"}),
	}}}
	a.client = caller
	var maxTurnPayload events.NoticePayload

	err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID: "session-max-turns",
		TurnID:    "turn-max-turns",
		ActorKind: "main",
		ActorID:   "agent-main",
		Sink: events.SinkFunc(func(event events.Event) {
			if event.Type != events.EventErrorRaised {
				return
			}
			payload, _ := event.Payload.(events.NoticePayload)
			if payload.Code == "max_turns_reached" {
				maxTurnPayload = payload
			}
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "after 2 turns") {
		t.Fatalf("expected configured max turns error after 2 turns, got %v", err)
	}
	if maxTurnPayload.Code != "max_turns_reached" || maxTurnPayload.MaxTurns != 2 || maxTurnPayload.Iteration != 2 || maxTurnPayload.ActorKind != "agent" || maxTurnPayload.ActorID != "agent-main" {
		t.Fatalf("expected max-turn diagnostic payload, got %+v", maxTurnPayload)
	}
	if caller.calls != 2 {
		t.Fatalf("expected two model calls, got %d", caller.calls)
	}
}

func TestRunWithOptionsEmitsRealStreamingDeltas(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = &streamingCaller{
		response: protocol.Response{Content: []protocol.Block{protocol.TextBlock("streamed")}},
		deltas:   []string{"str", "eam", "ed"},
	}
	var deltas []string
	var completed string

	err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID: "session-stream",
		TurnID:    "turn-stream",
		Sink: events.SinkFunc(func(event events.Event) {
			payload, _ := event.Payload.(events.TextPayload)
			switch event.Type {
			case events.EventAssistantTextDelta:
				deltas = append(deltas, payload.Text)
			case events.EventAssistantMessageComplete:
				completed = payload.Text
			}
		}),
	})
	if err != nil {
		t.Fatalf("run with options: %v", err)
	}
	if got := strings.Join(deltas, ""); got != "streamed" {
		t.Fatalf("expected streamed deltas %q, got %q", "streamed", got)
	}
	if completed != "streamed" {
		t.Fatalf("expected completion payload %q, got %q", "streamed", completed)
	}
}

func TestRunWithOptionsEmitsHistoryRecallDecisionEvent(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.AddMessage("刚才我们说过哪个方案？")
	a.client = fakeCaller{resp: protocol.Response{
		Content: []protocol.Block{
			protocol.TextBlock("我来回顾一下。"),
		},
	}}

	var payload events.HistoryRecallPayload
	var saw bool
	err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID: "session-history",
		TurnID:    "turn-history",
		Sink: events.SinkFunc(func(event events.Event) {
			if event.Type != events.EventHistoryRecallDecision {
				return
			}
			got, ok := event.Payload.(events.HistoryRecallPayload)
			if !ok {
				t.Fatalf("unexpected history recall payload: %#v", event.Payload)
			}
			payload = got
			saw = true
		}),
	})
	if err != nil {
		t.Fatalf("run with options: %v", err)
	}
	if !saw {
		t.Fatal("expected history recall decision event")
	}
	if !payload.AllowTool || !payload.ExplicitRequest {
		t.Fatalf("unexpected history recall payload: %+v", payload)
	}
}

func TestRunWithOptionsEmitsToolLifecycleEventsFromRunner(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = &sequenceCaller{responses: []protocol.Response{
		{
			Content: []protocol.Block{
				protocol.ToolUseBlock("tool-1", "memory", map[string]interface{}{"action": "list"}),
			},
		},
		{
			Content: []protocol.Block{
				protocol.TextBlock("done"),
			},
		},
	}}

	var started events.ToolCallPayload
	var finished events.ToolCallPayload
	var sawStarted bool
	var sawFinished bool
	err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID: "session-tool",
		TurnID:    "turn-tool",
		Sink: events.SinkFunc(func(event events.Event) {
			switch event.Type {
			case events.EventToolCallStarted:
				payload, ok := event.Payload.(events.ToolCallPayload)
				if !ok {
					t.Fatalf("unexpected tool started payload: %#v", event.Payload)
				}
				started = payload
				sawStarted = true
			case events.EventToolCallFinished:
				payload, ok := event.Payload.(events.ToolCallPayload)
				if !ok {
					t.Fatalf("unexpected tool finished payload: %#v", event.Payload)
				}
				finished = payload
				sawFinished = true
			}
		}),
	})
	if err != nil {
		t.Fatalf("run with options: %v", err)
	}
	if !sawStarted || !sawFinished {
		t.Fatalf("expected tool lifecycle events, started=%v finished=%v", sawStarted, sawFinished)
	}
	if started.ID != "tool-1" || started.Name != "memory" {
		t.Fatalf("unexpected started payload: %+v", started)
	}
	if finished.ID != "tool-1" || finished.Name != "memory" {
		t.Fatalf("unexpected finished payload: %+v", finished)
	}
	if finished.Error != "" {
		t.Fatalf("expected successful tool finish, got %+v", finished)
	}
}

func TestRunWithOptionsEmitsTodoListUpdatedAfterTodoWrite(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = &sequenceCaller{responses: []protocol.Response{
		{
			Content: []protocol.Block{
				protocol.ToolUseBlock("todo-tool-1", "todo_write", map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{"content": "Inspect changes", "status": "completed", "active_form": "Inspecting changes"},
						map[string]interface{}{"content": "Run tests", "status": "in_progress", "active_form": "Running tests"},
					},
				}),
			},
		},
		{
			Content: []protocol.Block{protocol.TextBlock("done")},
		},
	}}

	var payload events.TodoListPayload
	saw := false
	err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID: "session-todo",
		TurnID:    "turn-todo",
		Sink: events.SinkFunc(func(event events.Event) {
			if event.Type != events.EventTodoListUpdated {
				return
			}
			got, ok := event.Payload.(events.TodoListPayload)
			if !ok {
				t.Fatalf("unexpected todo payload: %#v", event.Payload)
			}
			payload = got
			saw = true
		}),
	})
	if err != nil {
		t.Fatalf("run with options: %v", err)
	}
	if !saw {
		t.Fatal("expected todo_list_updated event")
	}
	if payload.SourceToolCallID != "todo-tool-1" || payload.SourceToolName != "todo_write" {
		t.Fatalf("unexpected source tool fields: %+v", payload)
	}
	if payload.Total != 2 || payload.Completed != 1 || payload.InProgress != 1 || payload.Pending != 0 {
		t.Fatalf("unexpected todo counts: %+v", payload)
	}
	if len(payload.Items) != 2 || payload.Items[1].Content != "Run tests" {
		t.Fatalf("unexpected todo items: %+v", payload.Items)
	}
}

func TestRunWithOptionsEmitsExplicitArtifactPathsFromToolResult(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.registerTool(tools.NewTypedTool(tools.NewToolSpec("emit_artifact", "Emit one explicit artifact.", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args struct{}) (tools.ToolResult, error) {
		_ = ctx
		return tools.ToolResult{
			Structured:    map[string]interface{}{"status": "ok"},
			ArtifactPaths: []string{"/tmp/generated.png"},
		}, nil
	}), tools.ToolMeta{AlwaysActive: true})
	a.client = &sequenceCaller{responses: []protocol.Response{
		{
			Content: []protocol.Block{
				protocol.ToolUseBlock("tool-1", "emit_artifact", map[string]interface{}{}),
			},
		},
		{
			Content: []protocol.Block{
				protocol.TextBlock("done"),
			},
		},
	}}

	var finished events.ToolCallPayload
	err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID: "session-artifact",
		TurnID:    "turn-artifact",
		Sink: events.SinkFunc(func(event events.Event) {
			if event.Type != events.EventToolCallFinished {
				return
			}
			payload, ok := event.Payload.(events.ToolCallPayload)
			if !ok {
				t.Fatalf("unexpected tool finished payload: %#v", event.Payload)
			}
			finished = payload
		}),
	})
	if err != nil {
		t.Fatalf("run with options: %v", err)
	}
	if len(finished.ArtifactPaths) != 1 || finished.ArtifactPaths[0] != "/tmp/generated.png" {
		t.Fatalf("unexpected artifact paths: %+v", finished.ArtifactPaths)
	}
}

func TestRunWithOptionsStubsOversizedToolResult(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	largeOutput := strings.Repeat("large-output\n", 4000)
	a.registerTool(tools.NewTypedTool(tools.NewToolSpec("large_result", "Emit a large result.", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args struct{}) (tools.ToolResult, error) {
		_ = ctx
		return tools.ToolResult{Text: largeOutput}, nil
	}), tools.ToolMeta{AlwaysActive: true})
	a.client = &sequenceCaller{responses: []protocol.Response{
		{
			Content: []protocol.Block{
				protocol.ToolUseBlock("tool-large", "large_result", map[string]interface{}{}),
			},
		},
		{
			Content: []protocol.Block{
				protocol.TextBlock("done"),
			},
		},
	}}

	var finished events.ToolCallPayload
	if err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID: "session-large",
		TurnID:    "turn-large",
		Sink: events.SinkFunc(func(event events.Event) {
			if event.Type != events.EventToolCallFinished {
				return
			}
			payload, ok := event.Payload.(events.ToolCallPayload)
			if !ok {
				t.Fatalf("unexpected tool finished payload: %#v", event.Payload)
			}
			finished = payload
		}),
	}); err != nil {
		t.Fatalf("run with options: %v", err)
	}
	if !strings.Contains(finished.Output, "tool_result_truncated") {
		t.Fatalf("expected truncated tool result stub, got %q", finished.Output)
	}
	if len([]byte(finished.Output)) > modelcontext.MaxVisibleToolResultBytes {
		t.Fatalf("expected model-visible output below cap, got %d bytes", len([]byte(finished.Output)))
	}
	if len([]byte(finished.Output)) > 6*1024 {
		t.Fatalf("expected compact large-result preview, got %d bytes", len([]byte(finished.Output)))
	}
	if len(finished.ArtifactPaths) != 1 {
		t.Fatalf("expected one large-result artifact path, got %+v", finished.ArtifactPaths)
	}
	data, err := os.ReadFile(finished.ArtifactPaths[0])
	if err != nil {
		t.Fatalf("read stored large-result artifact: %v", err)
	}
	if !strings.Contains(string(data), "large-output") {
		t.Fatalf("expected stored artifact to contain raw output")
	}
	if gotRel, wantRel := a.modelToolResultReferencePath(finished.ArtifactPaths[0]), ".godex/.tool-results/session-large/tool-large.json"; gotRel != wantRel {
		t.Fatalf("expected reference path %q, got %q", wantRel, gotRel)
	}
}

func TestRunWithOptionsCapturesMemoryCandidatesOnCompletedTurn(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = fakeCaller{resp: protocol.Response{
		Content: []protocol.Block{
			protocol.TextBlock("好的，我之后会用中文回复。"),
		},
	}}
	a.AddMessage("请用中文回复")

	if err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID: "session-memory",
		TurnID:    "turn-memory",
	}); err != nil {
		t.Fatalf("run with options: %v", err)
	}

	candidates, err := a.memoryMgr.ListCandidates()
	if err != nil {
		t.Fatalf("list memory candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one memory candidate, got %d", len(candidates))
	}
	if candidates[0].Title != "User Preference: Reply in Chinese" {
		t.Fatalf("unexpected candidate: %+v", candidates[0])
	}
}

func TestExportStateAndRestoreStateRoundTrip(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	skillPath := filepath.Join(a.cfg.SkillsDir, "review-helper", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
description: Review code changes with a structured checklist
sections:
  - core
  - workflow
---
## Core
Focus on regressions first.

## Workflow
Read the diff, then run tests.
`), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	a.AddMessage("hello")
	if _, err := a.ActivateSkill("review-helper"); err != nil {
		t.Fatalf("activate skill: %v", err)
	}
	if _, err := a.ExpandSkill("review-helper", []string{"workflow"}); err != nil {
		t.Fatalf("expand skill: %v", err)
	}
	a.toolHandler.ActivateBundles(bundleBackground)

	state := a.ExportState()

	restored := newTestAgent(t, 4096)
	restored.RegisterTools()
	restored.RestoreState(state)

	if got := protocol.MessageText(restored.GetMessages()[0]); got != "hello" {
		t.Fatalf("expected restored messages, got %q", got)
	}
	if got := restored.ActiveSkillNames(); len(got) != 1 || got[0] != "review-helper" {
		t.Fatalf("expected restored active skills, got %v", got)
	}
	catalog := restored.ToolCatalog()
	active := false
	for _, name := range catalog.ActiveBundles {
		if name == bundleBackground {
			active = true
			break
		}
	}
	if !active {
		t.Fatalf("expected background bundle to be restored, got %+v", catalog.ActiveBundles)
	}
	exported := restored.ExportState()
	if exported.HistoryVersion != state.HistoryVersion || exported.LastCompactedVersion != state.LastCompactedVersion {
		t.Fatalf("expected history versions to round-trip, got %+v want %+v", exported, state)
	}
}

func TestClearMessagesResetsTransientPromptState(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.RestoreState(SessionState{
		Messages:       []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "old question")},
		TranscriptRefs: []string{"transcripts/session-1.jsonl"},
		ActiveBundles:  []string{bundleBackground, bundleWeb},
	})

	a.ClearMessages()

	state := a.ExportState()
	if len(state.Messages) != 0 {
		t.Fatalf("expected messages to be cleared, got %d", len(state.Messages))
	}
	if len(state.TranscriptRefs) != 0 {
		t.Fatalf("expected transcript refs to be cleared, got %v", state.TranscriptRefs)
	}
	for _, name := range state.ActiveBundles {
		if name == bundleBackground {
			t.Fatalf("expected transient bundles to reset, got %v", state.ActiveBundles)
		}
	}
	if !containsString(state.ActiveBundles, bundleCoreCode) || !containsString(state.ActiveBundles, bundlePlanning) || !containsString(state.ActiveBundles, bundleWeb) {
		t.Fatalf("expected default bundles to remain active, got %v", state.ActiveBundles)
	}
}

func TestLoadDefaultSkillsReportsMissingSkillsWithoutFailing(t *testing.T) {
	a := newTestAgent(t, 4096)
	skillPath := filepath.Join(a.cfg.SkillsDir, "present", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
name: Present
description: Present default skill
---
## Core
Use the present skill.
`), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	a.cfg.DefaultSkills = []string{"present", "missing"}

	result := a.LoadDefaultSkills()
	if !containsString(result.Loaded, "present") {
		t.Fatalf("expected present skill to load, got %#v", result)
	}
	if !containsString(result.Missing, "missing") {
		t.Fatalf("expected missing skill to be reported, got %#v", result)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("expected no failed default skills, got %#v", result.Failed)
	}
}

func TestRemoveSkillDeletesInstalledSkillAndActiveState(t *testing.T) {
	a := newTestAgent(t, 4096)
	sourceDir := filepath.Join(a.cfg.WorkspaceDir, "skill-creator")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("mkdir skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte(`---
name: Skill Creator
description: Create skills
---
## Core
Create useful skills.
`), 0644); err != nil {
		t.Fatalf("write skill source: %v", err)
	}

	installed, err := a.InstallSkill(sourceDir, "")
	if err != nil {
		t.Fatalf("install skill: %v", err)
	}
	if _, err := a.ActivateSkill(installed.ID); err != nil {
		t.Fatalf("activate skill: %v", err)
	}
	removed, err := a.RemoveSkill("Skill Creator")
	if err != nil {
		t.Fatalf("remove skill: %v", err)
	}
	if removed.ID != "skill-creator" || !removed.WasActive {
		t.Fatalf("unexpected remove result: %+v", removed)
	}
	if active, err := a.ActiveSkills(); err != nil || len(active) != 0 {
		t.Fatalf("expected active skill to be removed, active=%+v err=%v", active, err)
	}
	if _, err := os.Stat(filepath.Join(a.cfg.SkillsDir, "skill-creator")); !os.IsNotExist(err) {
		t.Fatalf("expected installed skill directory removed, stat err=%v", err)
	}
}

func TestRunWithOptionsBuildsVisionInputForImageAttachments(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	imagePath := filepath.Join(a.cfg.StateDir, "sample.jpg")
	if err := os.WriteFile(imagePath, []byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43}, 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	caller := &capturingCaller{resp: protocol.Response{
		Content: []protocol.Block{protocol.TextBlock("done")},
	}}
	a.client = caller
	a.AddEnvelope(message.Envelope{
		Source:    message.SourceFeishu,
		SessionID: "session-vision",
		Sender:    "lead",
		Text:      "这张图是做什么用的？",
		Attachments: []message.AttachmentRef{{
			ID:       "att-vision",
			Name:     "sample.jpg",
			MIMEType: "image/jpeg",
			Path:     imagePath,
		}},
	})

	if err := a.RunWithOptions(context.Background(), RunOptions{SessionID: "session-vision", TurnID: "turn-vision"}); err != nil {
		t.Fatalf("run with options: %v", err)
	}

	foundImage := false
	for _, msg := range caller.req.Messages {
		for _, block := range msg.Content {
			if block.Type == protocol.BlockImage {
				foundImage = true
			}
		}
	}
	if !foundImage {
		t.Fatalf("expected request to include image input, got %#v", caller.req.Messages)
	}
}

func TestApplyConfigPreservesCronToolFromSharedDependencies(t *testing.T) {
	a := newTestAgent(t, 4096)
	cfg := a.cfg
	shared := NewSharedDependencies(cfg)
	shared.SetCronService(fakeCronManager{})

	a = NewWithSharedDependencies(cfg, shared, "")
	a.RegisterTools()
	before := a.ToolCatalog()
	foundBefore := false
	for _, name := range before.AlwaysActiveTools {
		if name == "cron" {
			foundBefore = true
			break
		}
	}
	if !foundBefore {
		t.Fatalf("expected cron tool before config apply, got %+v", before.AlwaysActiveTools)
	}

	nextCfg := *cfg
	nextCfg.MaxTokens = cfg.MaxTokens + 1
	shared.ApplyConfig(&nextCfg)
	a.ApplyConfig(&nextCfg, shared)

	after := a.ToolCatalog()
	foundAfter := false
	for _, name := range after.AlwaysActiveTools {
		if name == "cron" {
			foundAfter = true
			break
		}
	}
	if !foundAfter {
		t.Fatalf("expected cron tool after config apply, got %+v", after.AlwaysActiveTools)
	}
}

func TestApplyConfigKeepsToolHandlerInstanceStable(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	original := a.toolHandler
	nextCfg := a.cfg.Clone()
	nextCfg.MaxTokens = a.cfg.MaxTokens + 1
	shared := NewSharedDependenciesWithCaller(nextCfg, a.client)

	a.ApplyConfig(nextCfg, shared)

	if a.toolHandler != original {
		t.Fatal("expected ApplyConfig to rebuild tool registrations without replacing the handler instance")
	}
	if original.Get("memory") == nil {
		t.Fatal("expected rebuilt handler to retain registered tools")
	}
}

func TestApplyConfigRefreshesSandboxBinding(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	nextWorkspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(nextWorkspace, "marker.txt"), []byte("from refreshed sandbox"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	nextCfg := a.cfg.Clone()
	nextCfg.WorkspaceDir = nextWorkspace
	nextCfg.TempDir = filepath.Join(nextWorkspace, ".godex", ".tmp")
	shared := NewSharedDependenciesWithCaller(nextCfg, a.client)

	a.ApplyConfig(nextCfg, shared)

	if got, want := a.SandboxBinding().WorkspaceDir, filepath.Clean(nextWorkspace); got != want {
		t.Fatalf("sandbox workspace %q, want %q", got, want)
	}
	output, err := a.handleTool(context.Background(), "read_file", map[string]interface{}{"path": "marker.txt"})
	if err != nil {
		t.Fatalf("read marker through rebound tool: %v", err)
	}
	if !strings.Contains(output, "from refreshed sandbox") {
		t.Fatalf("expected read_file to use refreshed sandbox, got %q", output)
	}
}

func TestApplyConfigRebindsToolExchangeToStableHandler(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	nextCfg := a.cfg.Clone()
	nextCfg.MaxTokens = a.cfg.MaxTokens + 1
	shared := NewSharedDependenciesWithCaller(nextCfg, a.client)

	a.ApplyConfig(nextCfg, shared)

	if _, err := a.handleTool(context.Background(), "tool_exchange", map[string]interface{}{
		"enable_bundles": []interface{}{bundleBackground},
	}); err != nil {
		t.Fatalf("enable background bundle after ApplyConfig: %v", err)
	}
	if !a.toolHandler.IsActive("background") {
		t.Fatal("expected tool_exchange to activate background on the stable handler")
	}
	if _, err := a.handleTool(context.Background(), "background", map[string]interface{}{
		"action":  "run",
		"command": `sh -c 'printf ok'`,
	}); err != nil {
		t.Fatalf("expected background to execute after tool_exchange activation: %v", err)
	}
}

func TestApplyConfigRefreshesPermissionPolicyWhilePreservingManager(t *testing.T) {
	a := newTestAgent(t, 4096)
	cfg := a.cfg
	shared := NewSharedDependencies(cfg)

	beforeManager := shared.snapshot().permissions
	before := beforeManager.Evaluate(tools.PermissionRequest{
		SessionID: "web-session-before",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/a.txt"},
		Mutation:  true,
	})
	if before.Decision != tools.PermissionPending {
		t.Fatalf("expected default policy to require approval, got %+v", before)
	}

	nextCfg := cfg.Clone()
	nextCfg.Tools.Permissions.InteractiveApprovalEnabled = false
	shared.ApplyConfig(nextCfg)

	afterManager := shared.snapshot().permissions
	if beforeManager != afterManager {
		t.Fatal("expected permission manager instance to be preserved across config apply")
	}

	after := afterManager.Evaluate(tools.PermissionRequest{
		SessionID: "web-session-after",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/b.txt"},
		Mutation:  true,
	})
	if after.Decision != tools.PermissionAbstain {
		t.Fatalf("expected updated policy to disable interactive approval, got %+v", after)
	}
}

func TestApplyConfigRefreshesTrustedPermissionPrefixes(t *testing.T) {
	a := newTestAgent(t, 4096)
	cfg := a.cfg
	shared := NewSharedDependencies(cfg)

	nextCfg := cfg.Clone()
	nextCfg.Tools.Permissions.TrustedPathPrefixes = []string{"notes"}
	shared.ApplyConfig(nextCfg)

	manager := shared.snapshot().permissions
	allowed := manager.Evaluate(tools.PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/todo.txt"},
		Mutation:  true,
	})
	if allowed.Decision != tools.PermissionAllow {
		t.Fatalf("expected trusted path prefix to allow after config apply, got %+v", allowed)
	}
}

func TestApplyConfigRefreshesPermissionApprovalMode(t *testing.T) {
	a := newTestAgent(t, 4096)
	cfg := a.cfg
	shared := NewSharedDependencies(cfg)

	nextCfg := cfg.Clone()
	nextCfg.Tools.Permissions.InteractiveApprovalMode = tools.InteractiveApprovalModeYOLO
	shared.ApplyConfig(nextCfg)

	manager := shared.snapshot().permissions
	result := manager.Evaluate(tools.PermissionRequest{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/todo.txt"},
		Mutation:  true,
	})
	if result.Decision != tools.PermissionAllow {
		t.Fatalf("expected yolo mode to auto-approve after config apply, got %+v", result)
	}
}

func TestHandleToolReviewModeUsesSubagentRunnerBeforeAllowing(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.permissions.ApplyPolicy(tools.PermissionPolicy{
		BlockAutomationMutations: true,
		InteractiveApproval: tools.InteractiveApprovalPolicy{
			Mode:    tools.InteractiveApprovalModeReview,
			Enabled: true,
			Sources: []string{string(message.SourceWeb)},
			Tools:   []string{"write_file"},
		},
	})

	notesDir := filepath.Join(a.cfg.WorkspaceDir, "notes")
	if err := os.MkdirAll(notesDir, 0755); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "context.txt"), []byte("workspace notes are safe to update"), 0644); err != nil {
		t.Fatalf("seed context file: %v", err)
	}

	caller := &sequenceCaller{responses: []protocol.Response{
		{
			Content: []protocol.Block{
				protocol.ToolUseBlock("tool-read", "read_file", map[string]interface{}{"path": "notes/context.txt"}),
			},
		},
		{
			Content: []protocol.Block{
				protocol.TextBlock("ALLOW: safe workspace note update"),
			},
		},
	}}
	a.client = caller

	ctx := tools.WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "web-session",
		Source:    string(message.SourceWeb),
		Sender:    "lead",
	})
	result, err := a.handleTool(ctx, "write_file", map[string]interface{}{
		"path":    "notes/todo.txt",
		"content": "hello",
	})
	if err != nil {
		t.Fatalf("expected review mode to allow write after reviewer approval, got %v", err)
	}
	if caller.index != 2 {
		t.Fatalf("expected reviewer subagent to make 2 model calls, got %d", caller.index)
	}
	if len(a.permissions.ListPending("web-session")) != 0 {
		t.Fatalf("expected no pending approvals after reviewer allow, got %+v", a.permissions.ListPending("web-session"))
	}
	if _, readErr := os.ReadFile(filepath.Join(notesDir, "todo.txt")); readErr != nil {
		t.Fatalf("expected target file to be written, got %v", readErr)
	}
	if result == "" {
		t.Fatal("expected write_file result text")
	}
}

func TestExportAndRestoreStateForSessionPersistsPermissions(t *testing.T) {
	a := newTestAgent(t, 4096)
	req := tools.PermissionRequest{
		SessionID: "session-permissions",
		Source:    string(message.SourceWeb),
		ToolName:  "write_file",
		Action:    "write",
		Paths:     []string{"notes/todo.txt"},
		Mutation:  true,
	}

	first := a.permissions.Evaluate(req)
	if first.Decision != tools.PermissionPending {
		t.Fatalf("expected pending approval, got %+v", first)
	}
	if _, err := a.permissions.ApprovePending("session-permissions", first.RequestID, tools.PermissionGrantSession); err != nil {
		t.Fatalf("approve pending session: %v", err)
	}

	state := a.ExportStateForSession("session-permissions")
	if len(state.PermissionState.Overrides) != 1 {
		t.Fatalf("expected one persisted override, got %+v", state.PermissionState)
	}

	restored := newTestAgent(t, 4096)
	restored.RestoreStateForSession("session-permissions", state)

	result := restored.permissions.Evaluate(req)
	if result.Decision != tools.PermissionAllow {
		t.Fatalf("expected restored session approval to allow request, got %+v", result)
	}
}

func TestExportRestoreStatePersistsCacheAndUsage(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cacheStatsMu.Lock()
	a.cacheStats = sessionCacheStats{Calls: 3, InputTokens: 1000, CacheReadTokens: 9000, CacheWriteTokens: 100}
	a.cacheStatsMu.Unlock()
	a.usageMu.Lock()
	a.usage = sessionUsage{InputTokens: 10100, OutputTokens: 130}
	a.usageMu.Unlock()

	state := a.ExportStateForSession("session-cache-persist")
	if state.CacheUsage == nil || state.CacheUsage.Calls != 3 {
		t.Fatalf("expected cache usage in exported state, got %+v", state.CacheUsage)
	}
	if state.UsageTotals == nil || state.UsageTotals.InputTokens != 10100 || state.UsageTotals.OutputTokens != 130 {
		t.Fatalf("expected usage totals in exported state, got %+v", state.UsageTotals)
	}

	// Round-trip through JSON to mirror the persisted session file, then
	// restore into a fresh agent (as loadSession does on reopen).
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	var decoded SessionState
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}

	restored := newTestAgent(t, 4096)
	restored.RestoreStateForSession("session-cache-persist", decoded)

	snap := restored.cacheUsageSnapshot()
	if snap.Calls != 3 || snap.CacheReadTokens != 9000 {
		t.Fatalf("expected restored cache stats, got %+v", snap)
	}
	if snap.HitRatePercent < 89.9 || snap.HitRatePercent > 90.1 {
		t.Fatalf("expected restored hit rate ~90%%, got %.2f", snap.HitRatePercent)
	}
	in, out := restored.cumulativeTokenUsage()
	if in != 10100 || out != 130 {
		t.Fatalf("expected restored usage totals (%d/%d), got (%d/%d)", 10100, 130, in, out)
	}
}
