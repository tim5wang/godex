package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
)

// TestLongTaskCardRendersHeaderAndProgress verifies that the
// longtask card surfaces id, status, and the X/Y progress count.
// T15 acceptance: a user opening the workbench task tab sees a
// one-glance card per longtask; the story progress is the
// "where do I stand" the operator asks for.
func TestLongTaskCardRendersHeaderAndProgress(t *testing.T) {
	view := agent.LongTaskView{
		LongTaskID: "lt_card",
		WorkflowID: "lt_card",
		Run: &agent.LongTaskRunSummary{Status: "running"},
		Stories: []agent.LongTaskStoryView{
			{ID: "US-001", Status: "completed"},
			{ID: "US-002", Status: "completed"},
			{ID: "US-003", Status: "running"},
		},
	}
	got := longTaskCard(view, 80)
	if !strings.Contains(got, "lt_card") {
		t.Fatalf("expected longtask id in card, got %q", got)
	}
	if !strings.Contains(got, "running") {
		t.Fatalf("expected status in card, got %q", got)
	}
	if !strings.Contains(got, "2/3 completed") {
		t.Fatalf("expected progress count, got %q", got)
	}
}

// TestLongTaskCardShowsRevertedCount verifies that a longtask
// with rolled-back stories is annotated. T15 acceptance: a
// story that was rolled back via T12 is visibly tagged so the
// operator can spot the audit trail.
func TestLongTaskCardShowsRevertedCount(t *testing.T) {
	view := agent.LongTaskView{
		LongTaskID: "lt_reverted",
		Run:        &agent.LongTaskRunSummary{Status: "completed"},
		Stories: []agent.LongTaskStoryView{
			{ID: "US-001", Status: "completed", Reverted: true},
			{ID: "US-002", Status: "completed"},
		},
	}
	got := longTaskCard(view, 80)
	if !strings.Contains(got, "1 reverted") {
		t.Fatalf("expected reverted annotation, got %q", got)
	}
}

// TestLongTaskStoryListSortsActiveFirst verifies that the story
// list orders active stories before completed ones. T15
// acceptance: the operator sees the work-in-progress at the top
// so they do not have to scroll to find out what is blocked.
func TestLongTaskStoryListSortsActiveFirst(t *testing.T) {
	stories := []longTaskStoryForList{
		{StoryID: "US-002", Status: "completed"},
		{StoryID: "US-001", Status: "running"},
		{StoryID: "US-003", Status: "pending"},
	}
	got := longTaskStoryList(stories, 80)
	runningIdx := strings.Index(got, "US-001")
	completedIdx := strings.Index(got, "US-002")
	pendingIdx := strings.Index(got, "US-003")
	if runningIdx < 0 || completedIdx < 0 || pendingIdx < 0 {
		t.Fatalf("expected all three stories, got %q", got)
	}
	// Pending (no in-flight) should come before running, and
	// both should come before completed.
	if !(pendingIdx < runningIdx && runningIdx < completedIdx) {
		t.Fatalf("expected order pending < running < completed, got %q", got)
	}
}

// TestLongTaskStoryListShowsRevertedTag verifies that a rolled-
// back story is rendered with a [reverted] marker. T15 acceptance:
// the TUI surfaces the same audit trail the T12 agent layer
// persists on the story view.
func TestLongTaskStoryListShowsRevertedTag(t *testing.T) {
	stories := []longTaskStoryForList{
		{StoryID: "US-001", Status: "completed", Reverted: true, CommitHash: "abc12345"},
	}
	got := longTaskStoryList(stories, 80)
	if !strings.Contains(got, "[reverted]") {
		t.Fatalf("expected [reverted] tag, got %q", got)
	}
	if !strings.Contains(got, "abc12345") {
		t.Fatalf("expected commit prefix, got %q", got)
	}
}

// TestLongTaskRollbackModalCapsReasonAt1024Bytes verifies the
// local TUI cap. T15 acceptance: the modal refuses to accept
// 1025 bytes, matching the agent-side 1024-byte cap (T12), and
// keeps accepting up to and including 1024 bytes.
func TestLongTaskRollbackModalCapsReasonAt1024Bytes(t *testing.T) {
	s := longTaskRollbackReasonState{Visible: true, NodeID: "US-001"}
	// 1024 bytes: exactly at the cap, must succeed.
	s = longTaskRollbackReasonAppend(s, strings.Repeat("a", 1024))
	if s.Err != "" {
		t.Fatalf("expected 1024-byte append to succeed, got %q", s.Err)
	}
	if s.ByteSize != 1024 {
		t.Fatalf("expected byte size 1024, got %d", s.ByteSize)
	}
	// 1025 bytes: must fail.
	s = longTaskRollbackReasonAppend(s, "b")
	if s.Err == "" {
		t.Fatalf("expected 1025-byte append to error, got nil")
	}
	// Backspace: must drop the byte and clear the error.
	s = longTaskRollbackReasonBackspace(s)
	if s.Err != "" {
		t.Fatalf("expected backspace to clear error, got %q", s.Err)
	}
	if s.ByteSize != 1023 {
		t.Fatalf("expected byte size 1023 after backspace, got %d", s.ByteSize)
	}
}

// TestLongTaskRollbackModalRendersByteCounter verifies the live
// byte counter in the rendered modal. T15 acceptance: the
// user sees "1023 / 1024 bytes" and knows they have one byte
// left before the modal refuses input.
func TestLongTaskRollbackModalRendersByteCounter(t *testing.T) {
	s := longTaskRollbackReasonState{Visible: true, NodeID: "US-001", Text: strings.Repeat("x", 100), ByteSize: 100}
	got := longTaskRollbackReasonView(s, 80)
	if !strings.Contains(got, "100 / 1024 bytes") {
		t.Fatalf("expected byte counter in modal, got %q", got)
	}
	if !strings.Contains(got, "US-001") {
		t.Fatalf("expected story id in modal header, got %q", got)
	}
}

// TestLongTaskLookupModalHonorsMode verifies the modal header
// changes with the mode (commit vs story). T15 acceptance: the
// same modal chrome works for both lookup paths.
func TestLongTaskLookupModalHonorsMode(t *testing.T) {
	commit := longTaskLookupState{Visible: true, Mode: longTaskLookupByCommit, Query: "abc"}
	commitView := longTaskLookupView(commit, 80)
	if !strings.Contains(commitView, "commit hash") {
		t.Fatalf("expected commit hint, got %q", commitView)
	}
	story := longTaskLookupState{Visible: true, Mode: longTaskLookupByStory, Query: "US-001"}
	storyView := longTaskLookupView(story, 80)
	if !strings.Contains(storyView, "story id") {
		t.Fatalf("expected story hint, got %q", storyView)
	}
}

// TestLongTaskLookupResultRendersEmptyHit verifies the no-match
// path. T15 acceptance: a lookup that found nothing still
// produces a useful "no matching stories" line, not a blank
// pane.
func TestLongTaskLookupResultRendersEmptyHit(t *testing.T) {
	got := longTaskLookupResult(nil, 80)
	if !strings.Contains(got, "no matching stories") {
		t.Fatalf("expected empty-hit message, got %q", got)
	}
}

// TestLongTaskDetailKeyMapsAllShortcuts verifies the keyboard
// mapping table. T15 acceptance: r/w/c/f/R/l/g each dispatch
// to the matching action; the test is the contract for the
// model layer's update logic.
func TestLongTaskDetailKeyMapsAllShortcuts(t *testing.T) {
	cases := map[string]longTaskDetailAction{
		"r": longTaskActionRun,
		"w": longTaskActionWait,
		"c": longTaskActionCancel,
		"f": longTaskActionFinalize,
		"R": longTaskActionRollback,
		"l": longTaskActionLookup,
		"g": longTaskActionGC,
		"esc": longTaskActionBack,
		"x": longTaskActionNone,
	}
	for k, want := range cases {
		got := longTaskDetailKey(k)
		if got != want {
			t.Fatalf("key %q: expected %s, got %s", k, longTaskDetailActionLabel(want), longTaskDetailActionLabel(got))
		}
	}
}

// TestLongTaskRefluxBubbleHasHeader verifies the TUI-side bubble
// renderer emits a [LongTask] header. T15 acceptance: reflux
// messages in the chat list are visually distinct from regular
// assistant messages so the user can tell at a glance.
func TestLongTaskRefluxBubbleHasHeader(t *testing.T) {
	got := longTaskRefluxBubble("LongTask lt_x: completed", "lt_x", "completed")
	if !strings.HasPrefix(got, "[LongTask] lt_x  completed") {
		t.Fatalf("expected header prefix, got %q", got)
	}
	if !strings.Contains(got, "LongTask lt_x: completed") {
		t.Fatalf("expected body preserved, got %q", got)
	}
}

// TestLongTaskRefluxBubbleSniffTest verifies the loose content
// sniff that the TUI chat list uses. T15 acceptance: a message
// starting with 'LongTask ' is treated as a reflux even if the
// metadata is missing; the real authority is the metadata kind,
// but the loose sniff keeps the chat list robust.
func TestLongTaskRefluxBubbleSniffTest(t *testing.T) {
	if !isLongTaskRefluxMessage("LongTask lt_x: completed") {
		t.Fatalf("expected sniff to accept 'LongTask ...' content")
	}
	if isLongTaskRefluxMessage("hello world") {
		t.Fatalf("expected sniff to reject non-reflux content")
	}
}

// TestModelRenderLongTaskDetailDelegatesToRenderer verifies the
// model-level integration: when longTaskDetailVisible is set and
// the cache has a matching view, renderLongTaskDetail returns
// the same string the pure renderer would produce. T15
// acceptance: the integration does not double-render or drop
// the modal state.
func TestModelRenderLongTaskDetailDelegatesToRenderer(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{longTasks: []agent.LongTaskView{
		{
			LongTaskID: "lt_detail",
			WorkflowID: "lt_detail",
			Run:        &agent.LongTaskRunSummary{Status: "running"},
			Stories: []agent.LongTaskStoryView{
				{ID: "US-001", Status: "running"},
			},
		},
	}}, time.Now, "session-1", rtbackend.Snapshot{Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"}})
	m.longTaskDetailVisible = true
	m.longTaskDetailID = "lt_detail"
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := m.renderLongTaskDetail()
	if !strings.Contains(got, "lt_detail") {
		t.Fatalf("expected id in detail render, got %q", got)
	}
	if !strings.Contains(got, "keys: [r] run") {
		t.Fatalf("expected keyboard hint, got %q", got)
	}
}
