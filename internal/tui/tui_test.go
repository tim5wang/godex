package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/tools"
)

type fakeBackend struct {
	snapshot       rtbackend.Snapshot
	contextSummary tools.ContextInspection
	longTasks      []agent.LongTaskView
	subagents      []agent.DurableSubagentJobView
	submitted      []message.Envelope
	executed       []commands.Command
	approved       []approvedPermission
	denied         []deniedPermission
}

type approvedPermission struct {
	SessionID string
	RequestID string
	Scope     tools.PermissionGrantScope
}

type deniedPermission struct {
	SessionID string
	RequestID string
	Reason    string
}

func runImmediateCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}

	done := make(chan tea.Msg, 1)
	go func() {
		done <- cmd()
	}()

	select {
	case msg := <-done:
		switch msg := msg.(type) {
		case tea.BatchMsg:
			for _, nested := range msg {
				runImmediateCmd(nested)
			}
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func pumpModelCmd(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}

	done := make(chan tea.Msg, 1)
	go func() {
		done <- cmd()
	}()

	select {
	case msg := <-done:
		switch msg := msg.(type) {
		case tea.BatchMsg:
			for _, nested := range msg {
				pumpModelCmd(m, nested)
			}
		default:
			_, next := m.Update(msg)
			pumpModelCmd(m, next)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func (f *fakeBackend) OpenSession(ctx context.Context, locator rtbackend.SessionLocator) (*rtbackend.OpenedSession, error) {
	_ = ctx
	return &rtbackend.OpenedSession{SessionID: "session-1", Locator: locator}, nil
}

func (f *fakeBackend) Snapshot(ctx context.Context, sessionID string) (rtbackend.Snapshot, error) {
	_ = ctx
	_ = sessionID
	return f.snapshot, nil
}

func (f *fakeBackend) ContextSummary(ctx context.Context, sessionID string) (tools.ContextInspection, error) {
	_ = ctx
	_ = sessionID
	return f.contextSummary, nil
}

func (f *fakeBackend) ListLongTasks(ctx context.Context, sessionID string) ([]agent.LongTaskView, error) {
	_ = ctx
	_ = sessionID
	return f.longTasks, nil
}

func (f *fakeBackend) ListSubagents(ctx context.Context, sessionID string) ([]agent.DurableSubagentJobView, error) {
	_ = ctx
	_ = sessionID
	return f.subagents, nil
}

func (f *fakeBackend) Submit(ctx context.Context, sessionID string, envelope message.Envelope) (*rtbackend.SubmitResult, error) {
	_ = ctx
	_ = sessionID
	f.submitted = append(f.submitted, envelope)
	return &rtbackend.SubmitResult{SessionID: sessionID}, nil
}

func (f *fakeBackend) ExecuteCommand(ctx context.Context, sessionID string, cmd commands.Command) (commands.Result, error) {
	_ = ctx
	_ = sessionID
	f.executed = append(f.executed, cmd)
	return commands.Result{Name: cmd.Name, Output: "ok"}, nil
}

func (f *fakeBackend) ApprovePermission(ctx context.Context, sessionID, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	_ = ctx
	f.approved = append(f.approved, approvedPermission{SessionID: sessionID, RequestID: requestID, Scope: scope})
	pending := f.removePendingPermission(requestID)
	return tools.PermissionResolution{
		RequestID: requestID,
		Decision:  tools.PermissionAllow,
		Scope:     scope,
		Request:   pending.Request,
	}, nil
}

func (f *fakeBackend) DenyPermission(ctx context.Context, sessionID, requestID, reason string) (tools.PermissionResolution, error) {
	_ = ctx
	f.denied = append(f.denied, deniedPermission{SessionID: sessionID, RequestID: requestID, Reason: reason})
	pending := f.removePendingPermission(requestID)
	return tools.PermissionResolution{
		RequestID: requestID,
		Decision:  tools.PermissionDeny,
		Reason:    reason,
		Request:   pending.Request,
	}, nil
}

func (f *fakeBackend) AttachSink(sessionID string, sink events.Sink) (func(), error) {
	_ = sessionID
	_ = sink
	return func() {}, nil
}

func (f *fakeBackend) removePendingPermission(requestID string) tools.PendingPermission {
	for i, pending := range f.snapshot.PendingPermissions {
		if pending.ID != requestID {
			continue
		}
		f.snapshot.PendingPermissions = append(f.snapshot.PendingPermissions[:i], f.snapshot.PendingPermissions[i+1:]...)
		return pending
	}
	return tools.PendingPermission{ID: requestID}
}

func TestSnapshotToItemsBuildsFoldedToolBlocks(t *testing.T) {
	items := snapshotToItems([]protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "hello"),
		protocol.NewMessage(protocol.RoleAssistant,
			protocol.TextBlock("working"),
			protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "pwd"}),
		),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-1", "/workspace")),
	}, nil, nil)

	if len(items) != 3 {
		t.Fatalf("expected 3 feed items, got %d", len(items))
	}
	if items[2].Kind != feedTool {
		t.Fatalf("expected tool item, got %+v", items[2])
	}
	if !items[2].Foldable || items[2].Expanded {
		t.Fatalf("expected folded tool item, got %+v", items[2])
	}
	if items[2].Status != "finished" {
		t.Fatalf("expected finished tool, got %+v", items[2])
	}
	if items[2].Summary != "/workspace" {
		t.Fatalf("expected tool summary from output, got %q", items[2].Summary)
	}
}

func TestSnapshotToItemsIncludesPendingPermissions(t *testing.T) {
	pending := tools.PendingPermission{
		ID:     "perm-1",
		Reason: "Need approval before editing project files.",
		Request: tools.PermissionRequest{
			ToolName: "edit_file",
			Paths:    []string{"README.md"},
		},
	}

	items := snapshotToItems(nil, []tools.PendingPermission{pending}, nil, "session-1")
	if len(items) != 1 {
		t.Fatalf("expected one permission item, got %d", len(items))
	}
	if items[0].Kind != feedPermission {
		t.Fatalf("expected permission item, got %+v", items[0])
	}
	if !items[0].Foldable || items[0].Permission == nil {
		t.Fatalf("expected foldable permission item, got %+v", items[0])
	}
	if items[0].Summary != "Need approval before editing project files." {
		t.Fatalf("unexpected permission summary: %q", items[0].Summary)
	}
}

func TestPermissionCollapsedLineShowsRequestAndSessionDetails(t *testing.T) {
	pending := tools.PendingPermission{
		ID:     "perm-1",
		Reason: "Need approval before running shell commands.",
		Request: tools.PermissionRequest{
			ToolName: "bash",
			Action:   "execute",
			Command:  "git status --short",
			Source:   string(message.SourceTUI),
		},
	}
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator:            rtbackend.SessionLocator{Channel: "local", Key: "default"},
		PendingPermissions: []tools.PendingPermission{pending},
	})
	m.activeWorkbenchTab = workbenchTabLogs
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	view := m.viewport.View()
	for _, want := range []string{"perm-1", "session-1", "bash", "execute", "git status --sh", "Agent wants to run shell command", "medium risk", "a once", "p pattern", "t timebox", "s session", "x deny"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected permission line to contain %q, got %q", want, view)
		}
	}
}

func TestPermissionCopyIncludesDetailsAndRedactsSensitiveInput(t *testing.T) {
	item := newPermissionItem(tools.PendingPermission{
		ID:     "perm-1",
		Reason: "Need approval before fetching protected URL.",
		Request: tools.PermissionRequest{
			ToolName: "web_fetch",
			Action:   "fetch",
			Input: map[string]interface{}{
				"url":       "https://example.com/private",
				"api_token": "secret-value",
			},
		},
	}, false, "session-1")

	text := feedItemCopyText(item)
	for _, want := range []string{"perm-1", "session-1", "web_fetch", "https://example.com/private", "Need approval"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected copied permission text to contain %q, got %q", want, text)
		}
	}
	if strings.Contains(text, "secret-value") {
		t.Fatalf("expected sensitive input to be redacted, got %q", text)
	}
	if !strings.Contains(text, "[redacted]") {
		t.Fatalf("expected redacted marker, got %q", text)
	}
}

func TestWorkbenchSummaryIncludesEmptySessionGuidance(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})

	summary := m.buildWorkbenchSummary()
	if !containsLine(summary.Plan, "No active plan. Start with a request or run a longtask.") {
		t.Fatalf("expected empty plan guidance, got %+v", summary.Plan)
	}
	if !containsLine(summary.Active, "Idle") {
		t.Fatalf("expected idle active state, got %+v", summary.Active)
	}
	if !containsLine(summary.Review, "Nothing waiting for review") {
		t.Fatalf("expected empty review guidance, got %+v", summary.Review)
	}
}

func TestWorkbenchSummarySurfacesLongTasksSubagentsAndReviewState(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator:      rtbackend.SessionLocator{Channel: "local", Key: "default"},
		Running:      true,
		ActiveTurnID: "turn-1",
		ActivePhase:  "tool:bash",
		QueuedTurns: []rtbackend.QueuedTurn{{
			ID:      "queued-1",
			Status:  "queued",
			Summary: "follow-up request",
		}},
		PendingPermissions: []tools.PendingPermission{{
			ID: "perm-1",
			Request: tools.PermissionRequest{
				ToolName: "bash",
				Action:   "execute",
			},
		}},
	})
	m.longTasks = []agent.LongTaskView{{
		WorkflowID:  "workflow-1",
		Project:     "GoDex TUI",
		Status:      "running",
		Total:       3,
		Pending:     1,
		Running:     1,
		Completed:   1,
		Failed:      0,
		Description: "Build task center",
	}}
	m.subagents = []agent.DurableSubagentJobView{{
		JobID:          "job-1",
		DisplayTitle:   "Implement Task Center",
		Status:         "completed",
		MergeStatus:    "ready",
		WorkerID:       "worker:godex:local",
		SandboxID:      "sandbox:local:repo",
		WorkerBranchID: "branch:worker-1",
		SourceBranchID: "main",
		LastPhase:      "finished",
		LastMessage:    "patch ready",
	}, {
		JobID:        "job-2",
		DisplayTitle: "Run tests",
		Status:       "running",
		WorkerID:     "worker:godex:local",
		SandboxID:    "sandbox:local:repo",
		LastPhase:    "testing",
	}}

	summary := m.buildWorkbenchSummary()
	for _, want := range []string{"GoDex TUI", "running 1/3", "queued-1"} {
		if !summaryContains(summary.Plan, want) {
			t.Fatalf("expected plan summary to contain %q, got %+v", want, summary.Plan)
		}
	}
	for _, want := range []string{"turn-1", "tool:bash", "job-2", "sandbox:local:repo"} {
		if !summaryContains(summary.Active, want) {
			t.Fatalf("expected active summary to contain %q, got %+v", want, summary.Active)
		}
	}
	for _, want := range []string{"perm-1", "job-1", "ready"} {
		if !summaryContains(summary.Review, want) {
			t.Fatalf("expected review summary to contain %q, got %+v", want, summary.Review)
		}
	}
}

func TestWorkbenchSummaryReconcilesRecoveredLongTaskWithMergedWorker(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})
	m.longTasks = []agent.LongTaskView{longTaskViewFromJSON(t, `{
		"longtask_id": "lt_demo",
		"workflow_id": "lt_demo",
		"project": "godex",
		"description": "Create docs/superpowers/tmp/tui-mvp-demo.md for the TUI MVP.",
		"status": "error",
		"total": 1,
		"failed": 1,
		"stories": [{
			"id": "tui-mvp-demo-doc",
			"title": "Create TUI MVP demo doc",
			"description": "Write docs/superpowers/tmp/tui-mvp-demo.md",
			"status": "error",
			"verdict": "fail",
			"error": "subagent_capability_required"
		}]
	}`)}
	m.subagents = []agent.DurableSubagentJobView{{
		JobID:        "subagent_demo",
		DisplayTitle: "Create TUI MVP demo doc",
		Objective:    "Create docs/superpowers/tmp/tui-mvp-demo.md as a short product-value document.",
		Status:       "completed",
		MergeStatus:  "merged",
		Result:       "Created docs/superpowers/tmp/tui-mvp-demo.md.",
		WriteScope:   []string{"docs/superpowers/tmp"},
	}}

	summary := m.buildWorkbenchSummary()
	if !summaryContains(summary.Plan, "Merged") || !summaryContains(summary.Plan, "recovered from failed longtask lt_demo") {
		t.Fatalf("expected recovered merged outcome, got %+v", summary.Plan)
	}
	if summaryContains(summary.Plan, "failed 1") {
		t.Fatalf("expected recovered longtask failure to be hidden from unresolved plan failures, got %+v", summary.Plan)
	}
	if !summaryContains(summary.Review, "Merged") || !summaryContains(summary.Review, "subagent_demo") {
		t.Fatalf("expected merged worker in review summary, got %+v", summary.Review)
	}
}

func TestWorkbenchSummaryMatchesLongTaskStoryJobIDDirectly(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{})
	m.longTasks = []agent.LongTaskView{longTaskViewFromJSON(t, `{
		"longtask_id": "lt_direct",
		"workflow_id": "lt_direct",
		"project": "Direct Match",
		"status": "error",
		"failed": 1,
		"stories": [{
			"id": "story-1",
			"title": "Story one",
			"status": "error",
			"job_id": "subagent_direct"
		}]
	}`)}
	m.subagents = []agent.DurableSubagentJobView{{
		JobID:       "subagent_direct",
		Status:      "completed",
		MergeStatus: "merged",
	}}

	summary := m.buildWorkbenchSummary()
	if !summaryContains(summary.Plan, "Merged") || !summaryContains(summary.Plan, "lt_direct") {
		t.Fatalf("expected direct job match to reconcile outcome, got %+v", summary.Plan)
	}
}

func TestWorkbenchSummaryDoesNotMergeUnrelatedFailuresByGenericText(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{})
	m.longTasks = []agent.LongTaskView{longTaskViewFromJSON(t, `{
		"longtask_id": "lt_generic",
		"workflow_id": "lt_generic",
		"project": "Task Center",
		"description": "Improve Task Center",
		"status": "error",
		"failed": 1,
		"stories": [{"id": "story-1", "title": "Task Center", "status": "error"}]
	}`)}
	m.subagents = []agent.DurableSubagentJobView{{
		JobID:        "subagent_generic",
		DisplayTitle: "Task Center",
		Objective:    "Improve Task Center",
		Status:       "completed",
		MergeStatus:  "merged",
	}}

	summary := m.buildWorkbenchSummary()
	if !summaryContains(summary.Plan, "Failed") || !summaryContains(summary.Plan, "lt_generic") {
		t.Fatalf("expected unrelated longtask failure to remain visible, got %+v", summary.Plan)
	}
	if summaryContains(summary.Plan, "recovered from failed longtask lt_generic") {
		t.Fatalf("expected generic text match not to reconcile failure, got %+v", summary.Plan)
	}
}

func TestWorkbenchSummaryClassifiesReviewAndBlockedOutcomes(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{})
	m.subagents = []agent.DurableSubagentJobView{{
		JobID:        "subagent_ready",
		DisplayTitle: "Ready patch",
		Status:       "completed",
		MergeStatus:  "pending",
	}, {
		JobID:        "subagent_blocked",
		DisplayTitle: "Blocked patch",
		Status:       "pending_approval",
		LastMessage:  "waiting for tool approval",
	}}

	summary := m.buildWorkbenchSummary()
	if !summaryContains(summary.Review, "Ready for review") || !summaryContains(summary.Review, "subagent_ready") {
		t.Fatalf("expected ready worker in review lines, got %+v", summary.Review)
	}
	if !summaryContains(summary.Active, "Blocked") || !summaryContains(summary.Active, "subagent_blocked") {
		t.Fatalf("expected blocked worker in active lines, got %+v", summary.Active)
	}
}

func TestWorkbenchOutcomeSmallTerminalRenderKeepsStatusVisible(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})
	m.longTasks = []agent.LongTaskView{longTaskViewFromJSON(t, `{
		"longtask_id": "lt_demo",
		"workflow_id": "lt_demo",
		"description": "Create docs/superpowers/tmp/tui-mvp-demo.md",
		"status": "error",
		"failed": 1
	}`)}
	m.subagents = []agent.DurableSubagentJobView{{
		JobID:       "subagent_demo",
		Objective:   "Create docs/superpowers/tmp/tui-mvp-demo.md",
		Status:      "completed",
		MergeStatus: "merged",
		WriteScope:  []string{"docs/superpowers/tmp"},
	}}
	m.activeWorkbenchTab = workbenchTabTask
	m.Update(tea.WindowSizeMsg{Width: 48, Height: 18})

	view := m.View()
	for _, want := range []string{"Merged", "recovered"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected small terminal view to contain %q, got %q", want, view)
		}
	}
}

func TestViewDefaultsToLogsAndTaskTabShowsTaskCenter(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model", WorkspaceDir: "/workspace"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
		Messages: []protocol.Message{
			protocol.NewTextMessage(protocol.RoleAssistant, "conversation log line"),
		},
	})
	m.longTasks = []agent.LongTaskView{{
		WorkflowID: "workflow-1",
		Project:    "Task Center",
		Status:     "running",
		Total:      2,
		Running:    1,
	}}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	view := m.View()
	for _, want := range []string{"conversation log line", "Focus: Input"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected default Logs view to contain %q, got %q", want, view)
		}
	}
	for _, hidden := range []string{"Plan", "Active Execution", "Review & Merge", "Task Center"} {
		if strings.Contains(view, hidden) {
			t.Fatalf("expected Task Center content to stay in Task tab, got %q", view)
		}
	}

	m.activeWorkbenchTab = workbenchTabTask
	m.refreshViewport(false)
	view = m.View()
	for _, want := range []string{"1 Task", "2 Workers", "3 Graph", "4 Diff", "5 Logs", "Plan", "Active Execution", "Review & Merge", "Task Center"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected Task tab to contain %q, got %q", want, view)
		}
	}
}

func TestWorkbenchInitLoadsLongTasksAndSubagents(t *testing.T) {
	backend := &fakeBackend{
		longTasks: []agent.LongTaskView{{
			WorkflowID: "workflow-1",
			Project:    "Loaded Work",
			Status:     "running",
			Total:      1,
			Running:    1,
		}},
		subagents: []agent.DurableSubagentJobView{{
			JobID:        "job-1",
			DisplayTitle: "Loaded Worker",
			Status:       "running",
			WorkerID:     "worker:godex:local",
		}},
	}
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, backend, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})

	pumpModelCmd(m, m.Init())

	if len(m.longTasks) != 1 || m.longTasks[0].Project != "Loaded Work" {
		t.Fatalf("expected init to load long tasks, got %+v", m.longTasks)
	}
	if len(m.subagents) != 1 || m.subagents[0].DisplayTitle != "Loaded Worker" {
		t.Fatalf("expected init to load subagents, got %+v", m.subagents)
	}
}

func TestNumberKeysSwitchWorkbenchTabsWhenWorkbenchFocused(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.focus = focusFeed
	m.composer.Blur()

	for _, tc := range []struct {
		key string
		tab workbenchTab
	}{
		{"2", workbenchTabWorkers},
		{"3", workbenchTabGraph},
		{"4", workbenchTabDiff},
		{"5", workbenchTabLogs},
		{"1", workbenchTabTask},
	} {
		_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)})
		if m.activeWorkbenchTab != tc.tab {
			t.Fatalf("%s selected tab %v, want %v", tc.key, m.activeWorkbenchTab, tc.tab)
		}
	}
}

func TestNumberKeysReachComposerWhenInputFocused(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})

	if m.activeWorkbenchTab != workbenchTabLogs {
		t.Fatalf("expected bare number to keep active tab at logs, got %v", m.activeWorkbenchTab)
	}
	if m.composer.Value() != "2" {
		t.Fatalf("expected bare number to reach composer, got %q", m.composer.Value())
	}
}

func TestFocusStatusLineShowsActiveFocus(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	if got := m.renderRuntimeStatus(); !strings.Contains(got, "Focus: Input") || !strings.Contains(got, "Tab workbench") {
		t.Fatalf("expected input focus hint, got %q", got)
	}

	_ = m.toggleFocus()
	if got := m.renderRuntimeStatus(); !strings.Contains(got, "Focus: Workbench") || !strings.Contains(got, "1-5 tabs") {
		t.Fatalf("expected workbench focus hint, got %q", got)
	}
}

func TestStatusLineShowsActivePermissionBlocker(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead", Model: "test-model"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "web", Key: "default"},
		ActivePermissionBlocker: &rtbackend.PermissionBlocker{
			RequestID: "perm-1",
			Status:    tools.PermissionStatusPending,
			ToolName:  "bash",
			Action:    "exec",
			Expiry:    "expires in 4m",
		},
	})

	got := m.renderRuntimeStatus()
	for _, want := range []string{"Blocked by approval", "perm-1", "bash exec", "expires in 4m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected status line to contain %q, got %q", want, got)
		}
	}
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

func summaryContains(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

func longTaskViewFromJSON(t *testing.T, raw string) agent.LongTaskView {
	t.Helper()
	var view agent.LongTaskView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("unmarshal longtask view: %v", err)
	}
	return view
}

func TestRenderHeaderShowsBotModelAndWorkspace(t *testing.T) {
	cfg := &config.Config{Model: "test-model", WorkspaceDir: "/workspace"}
	m := newModel(context.Background(), cfg, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})
	m.width = 60

	header := m.renderHeader()
	if !strings.Contains(header, "GoDex") {
		t.Fatalf("expected header to contain bot name, got %q", header)
	}
	if !strings.Contains(header, "test-model") {
		t.Fatalf("expected header to contain model, got %q", header)
	}
	if !strings.Contains(header, "/workspace") {
		t.Fatalf("expected header to contain workspace, got %q", header)
	}
}

func TestRenderHeaderShowsSessionModelProfile(t *testing.T) {
	cfg := &config.Config{
		Model:            "claude-sonnet-test",
		WorkspaceDir:     "/workspace",
		DefaultProfileID: "anthropic.sonnet",
		ModelProfiles: map[string]config.ModelProfileConfig{
			"anthropic.sonnet": {
				ID:       "anthropic.sonnet",
				Name:     "Claude Sonnet",
				Provider: config.ProviderAnthropicCompatible,
				Model:    "claude-sonnet-test",
			},
			"openai-codex": {
				ID:       "openai-codex",
				Name:     "OpenAI Codex / Codex",
				Provider: config.ProviderOpenAICodex,
				Model:    "gpt-5.5",
			},
		},
	}
	m := newModel(context.Background(), cfg, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator:        rtbackend.SessionLocator{Channel: "local", Key: "default"},
		ModelProfileID: "openai-codex",
	})
	m.width = 80

	header := m.renderHeader()
	if !strings.Contains(header, "OpenAI Codex / Codex") || !strings.Contains(header, "gpt-5.5") || !strings.Contains(header, "session") {
		t.Fatalf("expected header to contain selected session profile, got %q", header)
	}
}

func TestViewUsesSingleColumnFeedAndWrapsLongAssistantText(t *testing.T) {
	backend := &fakeBackend{}
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, backend, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
		Messages: []protocol.Message{
			protocol.NewTextMessage(protocol.RoleAssistant, "This is a very long assistant response that should wrap across multiple lines in the viewport without relying on a sidebar layout."),
		},
	})
	m.Update(tea.WindowSizeMsg{Width: 32, Height: 18})

	view := m.View()
	if strings.Contains(view, "Session") || strings.Contains(view, "Skills") || strings.Contains(view, "Bundles") {
		t.Fatalf("expected redesigned TUI without legacy sidebar labels, got %q", view)
	}
	if m.viewport.TotalLineCount() <= 1 {
		t.Fatalf("expected wrapped assistant text, total lines %d", m.viewport.TotalLineCount())
	}
}

func TestToolLifecycleMergesByIDAndCanToggleExpansion(t *testing.T) {
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m.setWorkbenchTab(workbenchTabLogs)

	m.handleEvent(events.Event{
		TurnID: "turn-1",
		Type:   events.EventToolCallStarted,
		Payload: events.ToolCallPayload{
			ID:    "tool-1",
			Name:  "bash",
			Input: map[string]interface{}{"command": "pwd"},
		},
	})
	m.handleEvent(events.Event{
		TurnID: "turn-1",
		Type:   events.EventToolCallFinished,
		Payload: events.ToolCallPayload{
			ID:     "tool-1",
			Name:   "bash",
			Input:  map[string]interface{}{"command": "pwd"},
			Output: "/workspace",
		},
	})

	if len(m.overlayItems) != 1 {
		t.Fatalf("expected one merged overlay tool item, got %d", len(m.overlayItems))
	}
	if m.overlayItems[0].Status != "finished" {
		t.Fatalf("expected finished tool item, got %+v", m.overlayItems[0])
	}
	m.focus = focusFeed
	m.selectedItemID = "tool:tool-1"
	m.toggleSelectedItem()
	if !m.overlayItems[0].Expanded {
		t.Fatalf("expected selected tool item to expand, got %+v", m.overlayItems[0])
	}
}

func TestTodoListUpdatedCreatesDedicatedFeedItem(t *testing.T) {
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})

	m.handleEvent(events.Event{
		TurnID: "turn-1",
		Type:   events.EventTodoListUpdated,
		Payload: events.TodoListPayload{
			Total:     2,
			Completed: 1,
			Pending:   1,
			Items: []events.TodoItemPayload{
				{Content: "Inspect changes", Status: "completed", ActiveForm: "Inspecting changes"},
				{Content: "Run tests", Status: "pending", ActiveForm: "Running tests"},
			},
		},
	})

	if len(m.overlayItems) != 1 {
		t.Fatalf("expected one todo overlay item, got %d", len(m.overlayItems))
	}
	item := m.overlayItems[0]
	if item.Kind != feedTodo || item.Title != "Todo list" {
		t.Fatalf("unexpected todo item: %+v", item)
	}
	if !strings.Contains(item.Body, "[x] Inspect changes") || !strings.Contains(item.Body, "[ ] Run tests") {
		t.Fatalf("expected rendered todo body, got %q", item.Body)
	}
	if !strings.Contains(m.status, "1/2") {
		t.Fatalf("expected todo progress in status, got %q", m.status)
	}
}

func TestFeedCopyShortcutCopiesSelectedItem(t *testing.T) {
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})
	m.width = 80
	m.focus = focusFeed
	item := newToolItem("tool:1", "turn-1", "bash", map[string]interface{}{"command": "pwd"}, "/workspace", "", false, true)
	m.overlayItems = []feedItem{item}
	m.selectedItemID = item.ID

	var copied string
	m.clipboardWrite = func(text string) error {
		copied = text
		return nil
	}

	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd != nil {
		t.Fatal("expected copy shortcut to finish synchronously")
	}
	if !strings.Contains(copied, "bash") || !strings.Contains(copied, "pwd") || !strings.Contains(copied, "/workspace") {
		t.Fatalf("expected selected item details to be copied, got %q", copied)
	}
	if !strings.Contains(m.status, "Copied") {
		t.Fatalf("expected copied status, got %q", m.status)
	}
}

func TestFeedCopyShortcutHandlesMissingSelection(t *testing.T) {
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})
	m.focus = focusFeed

	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd != nil {
		t.Fatal("expected copy shortcut to finish synchronously")
	}
	if !strings.Contains(m.status, "No feed item") {
		t.Fatalf("expected missing selection status, got %q", m.status)
	}
}

func TestFeedSelectionCanCopyVisibleAssistantMessage(t *testing.T) {
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
		Messages: []protocol.Message{
			protocol.NewTextMessage(protocol.RoleAssistant, "copy this answer"),
		},
	})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.setWorkbenchTab(workbenchTabLogs)
	m.focus = focusFeed

	var copied string
	m.clipboardWrite = func(text string) error {
		copied = text
		return nil
	}

	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !strings.Contains(copied, "copy this answer") {
		t.Fatalf("expected visible assistant message to be selectable and copied, got %q with selected %q", copied, m.selectedItemID)
	}
}

func TestCommandOverlayClearsWhenAssistantSnapshotAdvances(t *testing.T) {
	userAt := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	commandAt := userAt.Add(time.Second)
	assistantAt := userAt.Add(2 * time.Second)

	userMsg := protocol.NewTextMessage(protocol.RoleUser, "hello")
	assistantMsg := protocol.NewTextMessage(protocol.RoleAssistant, "done")

	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator:  rtbackend.SessionLocator{Channel: "local", Key: "default"},
		Messages: []protocol.Message{userMsg},
	})

	m.handleEvent(events.Event{
		TurnID:    "cmd-1",
		Type:      events.EventCommandCompleted,
		Timestamp: commandAt,
		Payload: events.CommandPayload{
			Name:   "help",
			Output: "help output",
		},
	})

	m.Update(snapshotLoadedMsg{Snapshot: rtbackend.Snapshot{
		Locator:   rtbackend.SessionLocator{Channel: "local", Key: "default"},
		UpdatedAt: assistantAt,
		Messages:  []protocol.Message{userMsg, assistantMsg},
	}})

	items := m.allItems()
	if len(items) != 2 {
		t.Fatalf("expected command overlay to clear once assistant snapshot advances, got %+v", items)
	}
	got := []feedItemKind{items[0].Kind, items[1].Kind}
	want := []feedItemKind{feedUser, feedAssistant}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected item order: got %v want %v; items=%+v", got, want, items)
		}
	}
}

func TestCommandOverlayPersistsAcrossSnapshotWithoutNewAssistant(t *testing.T) {
	at := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	userMsg := protocol.NewTextMessage(protocol.RoleUser, "hello")
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator:  rtbackend.SessionLocator{Channel: "local", Key: "default"},
		Messages: []protocol.Message{userMsg},
	})
	m.handleEvent(events.Event{
		TurnID:    "cmd-1",
		Type:      events.EventCommandCompleted,
		Timestamp: at,
		Payload: events.CommandPayload{
			Name:   "help",
			Output: "help output",
		},
	})

	m.Update(snapshotLoadedMsg{Snapshot: rtbackend.Snapshot{
		Locator:   rtbackend.SessionLocator{Channel: "local", Key: "default"},
		UpdatedAt: at,
		Messages:  []protocol.Message{userMsg},
	}})

	items := m.allItems()
	if len(items) != 2 || items[1].Kind != feedCommand {
		t.Fatalf("expected command overlay to remain visible without new assistant message, got %+v", items)
	}
}

func TestCommandOverlayUpsertsDuplicateRuntimeEvents(t *testing.T) {
	at := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})

	event := events.Event{
		TurnID:    "cmd-1",
		Type:      events.EventCommandCompleted,
		Timestamp: at,
		Payload: events.CommandPayload{
			Name:   "help",
			Output: "help output",
		},
	}
	m.handleEvent(event)
	m.handleEvent(event)

	count := 0
	for _, item := range m.overlayItems {
		if item.ID == "command:cmd-1:help" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected duplicate command event to upsert one overlay item, got %d items: %+v", count, m.overlayItems)
	}
}

func TestTUIMouseCaptureDisabledForNativeSelection(t *testing.T) {
	if captureMouseForTUI {
		t.Fatal("expected TUI mouse capture to be disabled so terminal native text selection works")
	}
}

func TestAutoFollowPausesWhenUserScrollsUp(t *testing.T) {
	messages := make([]protocol.Message, 0, 8)
	for i := 0; i < 8; i++ {
		messages = append(messages, protocol.NewTextMessage(protocol.RoleAssistant, strings.Repeat("long line ", 8)))
	}

	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator:  rtbackend.SessionLocator{Channel: "local", Key: "default"},
		Messages: messages,
	})
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 12})

	if !m.viewport.AtBottom() {
		t.Fatal("expected initial auto-follow at bottom")
	}

	m.viewport.LineUp(4)
	m.syncAutoFollow()
	m.reconcileSelectedItem()
	if m.autoFollow {
		t.Fatal("expected auto-follow to pause after scrolling up")
	}

	oldOffset := m.viewport.YOffset
	m.appendAssistantDelta("turn-1", strings.Repeat("new content ", 6), time.Now())
	m.refreshViewport(false)
	if m.viewport.YOffset != oldOffset {
		t.Fatalf("expected offset to stay put when auto-follow is paused, got %d want %d", m.viewport.YOffset, oldOffset)
	}

	m.viewport.GotoBottom()
	m.syncAutoFollow()
	m.appendAssistantDelta("turn-1", strings.Repeat("more content ", 6), time.Now())
	m.refreshViewport(false)
	if !m.viewport.AtBottom() {
		t.Fatal("expected auto-follow to resume once viewport returns to bottom")
	}
}

func TestHeartbeatStartsOnSubmitAndStopsOnTurnComplete(t *testing.T) {
	now := time.Unix(100, 0)
	backend := &fakeBackend{}
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, backend, func() time.Time { return now }, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})

	m.composer.SetValue("hello")
	if cmd := m.submitComposer(); cmd == nil {
		t.Fatal("expected submit command")
	}
	if !m.working {
		t.Fatal("expected heartbeat to start on submit")
	}

	now = now.Add(12 * time.Second)
	m.Update(heartbeatTickMsg{})
	if !strings.Contains(m.renderHeartbeatLine(), "12s") {
		t.Fatalf("expected heartbeat line to show elapsed time, got %q", m.renderHeartbeatLine())
	}

	m.handleEvent(events.Event{
		TurnID: "turn-1",
		Type:   events.EventTurnCompleted,
		Payload: events.TurnPayload{
			Status: "completed",
		},
	})
	if m.working {
		t.Fatal("expected heartbeat to stop after turn completion")
	}
}

func TestInputHistoryRecallsSubmittedTextAndCommands(t *testing.T) {
	backend := &fakeBackend{}
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, backend, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})

	m.composer.SetValue("hello")
	runImmediateCmd(m.submitComposer())
	m.submitting = false
	m.stopWorking()
	m.composer.SetValue("/tasks")
	runImmediateCmd(m.submitComposer())
	m.submitting = false
	m.stopWorking()

	m.composer.SetValue("")
	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlP})
	if got := m.composer.Value(); got != "/tasks" {
		t.Fatalf("expected most recent input, got %q", got)
	}
	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlP})
	if got := m.composer.Value(); got != "hello" {
		t.Fatalf("expected previous input, got %q", got)
	}
	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlN})
	if got := m.composer.Value(); got != "/tasks" {
		t.Fatalf("expected next input, got %q", got)
	}
}

func TestUpDownOnEmptyComposerScrollsFeedInsteadOfHistory(t *testing.T) {
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})
	m.inputHistory = []string{"previous prompt"}
	m.composer.SetValue("")
	m.viewport.Height = 5
	m.viewport.SetContent(strings.Repeat("line\n", 40))
	m.viewport.GotoBottom()
	before := m.viewport.YOffset

	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyUp})

	if got := m.composer.Value(); got != "" {
		t.Fatalf("expected empty composer to stay unchanged on plain up, got %q", got)
	}
	if m.viewport.YOffset >= before {
		t.Fatalf("expected plain up to scroll feed, before=%d after=%d", before, m.viewport.YOffset)
	}
}

func TestInputHistoryDedupesConsecutiveEntriesAndPreservesMultilineEditing(t *testing.T) {
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})

	m.composer.SetValue("same")
	runImmediateCmd(m.submitComposer())
	m.submitting = false
	m.stopWorking()
	m.composer.SetValue("same")
	runImmediateCmd(m.submitComposer())
	m.submitting = false
	m.stopWorking()

	if len(m.inputHistory) != 1 {
		t.Fatalf("expected consecutive duplicate to be stored once, got %+v", m.inputHistory)
	}

	m.composer.SetValue("line one\nline two")
	_, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.composer.Value(); got != "line one\nline two" {
		t.Fatalf("expected multiline input to stay under textarea control, got %q", got)
	}
}

func TestStatusLineShowsContextAndModelCalls(t *testing.T) {
	m := newModel(context.Background(), &config.Config{
		Model:             "test-model",
		WorkspaceDir:      "/workspace",
		LeadName:          "lead",
		CompressThreshold: 1000,
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
		Timeline: []events.Event{
			{
				TurnID:    "turn-1",
				Type:      events.EventRunnerPhaseChanged,
				Timestamp: time.Unix(1, 0),
				Payload: events.RunnerPhasePayload{
					Phase: "model_request",
				},
			},
		},
	})
	m.width = 120
	m.contextSummary = tools.ContextInspection{
		MessageCount:       18,
		TokenEstimate:      240,
		CompressThreshold:  1000,
		TotalTokenEstimate: 240,
		TokenBreakdown:     tools.ContextTokenBreakdown{Total: 240},
	}
	m.rebuildModelCallStats()

	line := m.renderHeartbeatLine()
	for _, want := range []string{"Ready", "ctx", "240/1.0k", "24%", "calls 1", "msgs 18"} {
		if !strings.Contains(line, want) {
			t.Fatalf("expected status line to contain %q, got %q", want, line)
		}
	}
}

func TestRuntimeModelRequestPhaseIncrementsCallCountOnce(t *testing.T) {
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})

	event := events.Event{
		TurnID:    "turn-1",
		Type:      events.EventRunnerPhaseChanged,
		Timestamp: time.Unix(1, 0),
		Payload:   events.RunnerPhasePayload{Phase: "model_request"},
	}
	m.handleEvent(event)
	m.handleEvent(event)

	if m.modelCallCount != 1 {
		t.Fatalf("expected duplicate model_request event to count once, got %d", m.modelCallCount)
	}
}

func TestPermissionShortcutApprovesAndRefreshesSnapshot(t *testing.T) {
	backend := &fakeBackend{
		snapshot: rtbackend.Snapshot{
			Locator: rtbackend.SessionLocator{Channel: "web", Key: "default"},
			PendingPermissions: []tools.PendingPermission{
				{
					ID:     "perm-1",
					Reason: "Need approval before running shell commands.",
					Request: tools.PermissionRequest{
						ToolName: "bash",
						Command:  "pwd",
						Source:   string(message.SourceWeb),
					},
				},
			},
		},
	}
	m := newModel(context.Background(), &config.Config{
		Model:        "test-model",
		WorkspaceDir: "/workspace",
		LeadName:     "lead",
	}, backend, time.Now, "session-1", backend.snapshot)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m.focus = focusFeed
	m.selectedItemID = "permission:perm-1"

	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if cmd == nil {
		t.Fatal("expected approval command")
	}
	pumpModelCmd(m, cmd)

	if len(backend.approved) != 1 {
		t.Fatalf("expected one approval call, got %+v", backend.approved)
	}
	if backend.approved[0].Scope != tools.PermissionGrantOnce {
		t.Fatalf("unexpected approval scope: %+v", backend.approved[0])
	}
	if len(m.snapshot.PendingPermissions) != 0 {
		t.Fatalf("expected refreshed snapshot to clear pending permissions, got %+v", m.snapshot.PendingPermissions)
	}
	if strings.Contains(m.viewport.View(), "pending approval") {
		t.Fatalf("expected pending permission to disappear after approval, got %q", m.viewport.View())
	}
}

func TestSubmitComposerRoutesTextAndCommands(t *testing.T) {
	backend := &fakeBackend{}
	cfg := &config.Config{LeadName: "lead", Model: "test-model", WorkspaceDir: "/workspace"}
	m := newModel(context.Background(), cfg, backend, func() time.Time { return time.Unix(123, 0) }, "session-1", rtbackend.Snapshot{
		Locator: rtbackend.SessionLocator{Channel: "local", Key: "default"},
	})

	m.composer.SetValue("hello")
	cmd := m.submitComposer()
	if cmd == nil {
		t.Fatal("expected submit command for text input")
	}
	runImmediateCmd(cmd)
	if len(backend.submitted) != 1 {
		t.Fatalf("expected one submitted envelope, got %d", len(backend.submitted))
	}
	if backend.submitted[0].Source != message.SourceTUI || backend.submitted[0].Text != "hello" {
		t.Fatalf("unexpected submitted envelope: %+v", backend.submitted[0])
	}

	m.submitting = false
	m.stopWorking()
	m.composer.SetValue("/tasks")
	cmd = m.submitComposer()
	if cmd == nil {
		t.Fatal("expected submit command for slash command input")
	}
	runImmediateCmd(cmd)
	if len(backend.executed) != 1 || backend.executed[0].Name != "tasks" {
		t.Fatalf("unexpected executed commands: %+v", backend.executed)
	}
}

func TestQDoesNotQuitTUI(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{})

	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("expected q not to quit the TUI")
		}
	}
}

func TestCtrlCStillQuitsTUI(t *testing.T) {
	m := newModel(context.Background(), &config.Config{LeadName: "lead"}, &fakeBackend{}, time.Now, "session-1", rtbackend.Snapshot{})

	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected ctrl+c to quit the TUI")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected ctrl+c command to be tea.Quit")
	}
}
