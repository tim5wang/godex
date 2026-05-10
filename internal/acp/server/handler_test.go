package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/tools"
)

type recordingUpdater struct {
	updates []acp.SessionUpdate
}

func (u *recordingUpdater) Update(ctx context.Context, update acp.SessionUpdate) error {
	_ = ctx
	u.updates = append(u.updates, update)
	return nil
}

type recordingPermissionRequester struct {
	requests []acp.RequestPermissionRequest
	response acp.RequestPermissionResponse
	err      error
}

func (r *recordingPermissionRequester) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	_ = ctx
	r.requests = append(r.requests, req)
	if r.err != nil {
		return acp.RequestPermissionResponse{}, r.err
	}
	return r.response, nil
}

type fakeHandlerBackend struct {
	sessionID string

	submitCalled  bool
	commandCalled bool
	pendingCalled bool
	approveCalled bool
	denyCalled    bool

	submitResult  *backend.SubmitResult
	commandResult commands.Result
	pending       []tools.PendingPermission
	resolution    tools.PermissionResolution
	approvedScope tools.PermissionGrantScope
	deniedReason  string
	sink          events.Sink
}

func (f *fakeHandlerBackend) OpenSession(context.Context, backend.SessionLocator) (*backend.OpenedSession, error) {
	sessionID := strings.TrimSpace(f.sessionID)
	if sessionID == "" {
		sessionID = "sess-1"
	}
	return &backend.OpenedSession{SessionID: sessionID}, nil
}

func (f *fakeHandlerBackend) SubmitAsync(context.Context, string, message.Envelope, ...backend.SubmitOptions) (*backend.SubmitResult, error) {
	f.submitCalled = true
	if f.submitResult != nil {
		return f.submitResult, nil
	}
	return &backend.SubmitResult{SessionID: "sess-1", TurnID: "turn-1", Status: "running"}, nil
}

func (f *fakeHandlerBackend) AttachSink(_ string, sink events.Sink) (func(), error) {
	f.sink = sink
	return func() { f.sink = nil }, nil
}

func (f *fakeHandlerBackend) ExecuteCommand(context.Context, string, commands.Command) (commands.Result, error) {
	f.commandCalled = true
	return f.commandResult, nil
}

func (f *fakeHandlerBackend) ApprovePermission(_ context.Context, _, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	f.approveCalled = true
	f.approvedScope = scope
	resolution := f.resolution
	resolution.RequestID = requestID
	resolution.Scope = scope
	resolution.Decision = tools.PermissionAllow
	return resolution, nil
}

func (f *fakeHandlerBackend) DenyPermission(_ context.Context, _, requestID, reason string) (tools.PermissionResolution, error) {
	f.denyCalled = true
	f.deniedReason = reason
	resolution := f.resolution
	resolution.RequestID = requestID
	resolution.Decision = tools.PermissionDeny
	resolution.Reason = reason
	return resolution, nil
}

func TestBackendPromptHandlerWaitsForTurnCompletedAfterToolLoop(t *testing.T) {
	fake := &fakeHandlerBackend{
		submitResult: &backend.SubmitResult{SessionID: "sess-1", TurnID: "turn-1", Status: "running"},
	}
	handler := BackendPromptHandler(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	var result PromptResult
	var err error
	go func() {
		defer wg.Done()
		result, err = handler(ctx, PromptTurn{SessionID: "acp-1", Prompt: "review code"})
	}()

	waitForSink(t, fake)
	emit := func(eventType events.EventType, payload any) {
		fake.sink.Emit(events.Event{
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Type:      eventType,
			Timestamp: time.Now(),
			Payload:   payload,
		})
	}
	emit(events.EventAssistantTextDelta, events.TextPayload{Text: "Let me inspect the diff."})
	emit(events.EventAssistantMessageComplete, events.TextPayload{Text: "Let me inspect the diff."})
	emit(events.EventToolCallStarted, events.ToolCallPayload{Name: "read_file"})
	emit(events.EventToolCallFinished, events.ToolCallPayload{Name: "read_file", Output: "ok"})
	emit(events.EventAssistantTextDelta, events.TextPayload{Text: "Final review: no issues found."})
	emit(events.EventAssistantMessageComplete, events.TextPayload{Text: "Final review: no issues found."})
	emit(events.EventTurnCompleted, events.TurnPayload{Status: "completed"})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("handler did not complete")
	}
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if !strings.Contains(result.FinalText, "Let me inspect the diff.") || !strings.Contains(result.FinalText, "Final review: no issues found.") {
		t.Fatalf("FinalText = %q, want both intermediate and final assistant text", result.FinalText)
	}
}

func TestBackendPromptHandlerStreamsTodoListUpdateToToolCall(t *testing.T) {
	fake := &fakeHandlerBackend{
		submitResult: &backend.SubmitResult{SessionID: "sess-1", TurnID: "turn-1", Status: "running"},
	}
	handler := BackendPromptHandler(fake)
	updater := &recordingUpdater{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	var err error
	go func() {
		defer wg.Done()
		_, err = handler(ctx, PromptTurn{SessionID: "acp-1", Prompt: "plan", Updater: updater})
	}()

	waitForSink(t, fake)
	emit := func(eventType events.EventType, payload any) {
		fake.sink.Emit(events.Event{
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Type:      eventType,
			Timestamp: time.Now(),
			Payload:   payload,
		})
	}
	emit(events.EventToolCallStarted, events.ToolCallPayload{ID: "todo-tool-1", Name: "todo_write"})
	emit(events.EventToolCallFinished, events.ToolCallPayload{ID: "todo-tool-1", Name: "todo_write", Output: "plain todo tool output"})
	emit(events.EventTodoListUpdated, events.TodoListPayload{
		SourceToolCallID: "todo-tool-1",
		SourceToolName:   "todo_write",
		Total:            2,
		Completed:        1,
		Pending:          1,
		Items: []events.TodoItemPayload{
			{Content: "Inspect changes", Status: "completed", ActiveForm: "Inspecting changes"},
			{Content: "Run tests", Status: "pending", ActiveForm: "Running tests"},
		},
	})
	emit(events.EventTurnCompleted, events.TurnPayload{Status: "completed"})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("handler did not complete")
	}
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	var sawTodoUpdate bool
	var sawInitialPlan bool
	var sawFinalPlan bool
	updateCount := 0
	for _, update := range updater.updates {
		if update.Plan != nil {
			if len(update.Plan.Entries) != 2 {
				t.Fatalf("expected two plan entries, got %+v", update.Plan.Entries)
			}
			if update.Plan.Entries[0].Status == acp.PlanEntryStatusCompleted && update.Plan.Entries[1].Status == acp.PlanEntryStatusPending {
				sawInitialPlan = true
			}
			if update.Plan.Entries[0].Status == acp.PlanEntryStatusCompleted && update.Plan.Entries[1].Status == acp.PlanEntryStatusCompleted {
				sawFinalPlan = true
			}
		}
		if update.ToolCallUpdate == nil {
			continue
		}
		if update.ToolCallUpdate.ToolCallId == acp.ToolCallId("todo-tool-1") && update.ToolCallUpdate.RawOutput != nil {
			updateCount++
			sawTodoUpdate = true
			if _, ok := update.ToolCallUpdate.RawOutput.(map[string]interface{}); !ok {
				t.Fatalf("expected todo raw output map, got %#v", update.ToolCallUpdate.RawOutput)
			}
		}
	}
	if !sawTodoUpdate {
		t.Fatalf("expected todo tool call update with raw output, got %+v", updater.updates)
	}
	if !sawInitialPlan {
		t.Fatalf("expected initial ACP plan update, got %+v", updater.updates)
	}
	if !sawFinalPlan {
		t.Fatalf("expected final completed ACP plan update, got %+v", updater.updates)
	}
	if updateCount != 1 {
		t.Fatalf("expected exactly one todo tool call output update, got %d updates: %+v", updateCount, updater.updates)
	}
}

func TestBackendPromptHandlerCompletesInProgressPlanOnCompletedTurn(t *testing.T) {
	fake := &fakeHandlerBackend{
		submitResult: &backend.SubmitResult{SessionID: "sess-1", TurnID: "turn-1", Status: "running"},
	}
	handler := BackendPromptHandler(fake)
	updater := &recordingUpdater{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	var err error
	go func() {
		defer wg.Done()
		_, err = handler(ctx, PromptTurn{SessionID: "acp-1", Prompt: "plan", Updater: updater})
	}()

	waitForSink(t, fake)
	emit := func(eventType events.EventType, payload any) {
		fake.sink.Emit(events.Event{
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Type:      eventType,
			Timestamp: time.Now(),
			Payload:   payload,
		})
	}
	emit(events.EventTodoListUpdated, events.TodoListPayload{
		Total:      2,
		Completed:  1,
		InProgress: 1,
		Items: []events.TodoItemPayload{
			{Content: "Inspect changes", Status: "completed"},
			{Content: "Summarize findings", Status: "in_progress"},
		},
	})
	emit(events.EventTurnCompleted, events.TurnPayload{Status: "completed"})
	wg.Wait()
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}

	var finalPlan *acp.SessionUpdatePlan
	for idx := range updater.updates {
		if updater.updates[idx].Plan != nil {
			finalPlan = updater.updates[idx].Plan
		}
	}
	if finalPlan == nil {
		t.Fatalf("expected final plan update, got %+v", updater.updates)
	}
	if len(finalPlan.Entries) != 2 {
		t.Fatalf("expected two final plan entries, got %+v", finalPlan.Entries)
	}
	for _, entry := range finalPlan.Entries {
		if entry.Status != acp.PlanEntryStatusCompleted {
			t.Fatalf("expected all final plan entries completed, got %+v", finalPlan.Entries)
		}
	}
}

func TestBackendPromptHandlerClearsPlanForEmptyTodoList(t *testing.T) {
	fake := &fakeHandlerBackend{
		submitResult: &backend.SubmitResult{SessionID: "sess-1", TurnID: "turn-1", Status: "running"},
	}
	handler := BackendPromptHandler(fake)
	updater := &recordingUpdater{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	var err error
	go func() {
		defer wg.Done()
		_, err = handler(ctx, PromptTurn{SessionID: "acp-1", Prompt: "clear plan", Updater: updater})
	}()
	waitForSink(t, fake)
	fake.sink.Emit(events.Event{SessionID: "sess-1", TurnID: "turn-1", Type: events.EventTodoListUpdated, Timestamp: time.Now(), Payload: events.TodoListPayload{}})
	fake.sink.Emit(events.Event{SessionID: "sess-1", TurnID: "turn-1", Type: events.EventTurnCompleted, Timestamp: time.Now(), Payload: events.TurnPayload{Status: "completed"}})
	wg.Wait()
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	for _, update := range updater.updates {
		if update.Plan != nil {
			if len(update.Plan.Entries) != 0 {
				t.Fatalf("expected empty plan update, got %+v", update.Plan.Entries)
			}
			return
		}
	}
	t.Fatalf("expected empty plan update, got %+v", updater.updates)
}

func waitForSink(t *testing.T, fake *fakeHandlerBackend) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if fake.sink != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for sink")
}

func waitForApprove(t *testing.T, fake *fakeHandlerBackend) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if fake.approveCalled {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for approval")
}

func (f *fakeHandlerBackend) PendingPermissions(context.Context, string) ([]tools.PendingPermission, error) {
	f.pendingCalled = true
	return f.pending, nil
}

func TestBackendPromptHandlerRunsSlashCommand(t *testing.T) {
	fake := &fakeHandlerBackend{
		commandResult: commands.Result{Name: "help", Output: "command output"},
	}
	handler := BackendPromptHandler(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := handler(ctx, PromptTurn{SessionID: "acp-1", Prompt: "/help"})
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if !fake.commandCalled {
		t.Fatal("expected ExecuteCommand to be called")
	}
	if fake.submitCalled {
		t.Fatal("expected slash command not to submit an agent turn")
	}
	if result.FinalText != "command output" {
		t.Fatalf("FinalText = %q, want command output", result.FinalText)
	}
}

func TestBackendPromptHandlerReturnsPendingApprovalSummary(t *testing.T) {
	fake := &fakeHandlerBackend{
		submitResult: &backend.SubmitResult{
			SessionID:        "sess-1",
			TurnID:           "turn-1",
			Status:           "pending_approval",
			PendingApproval:  true,
			PendingRequestID: "perm-1",
		},
		pending: []tools.PendingPermission{{
			ID: "perm-1",
			Request: tools.PermissionRequest{
				ToolName: "bash",
				Action:   "execute",
				Command:  "rm -rf build",
				Source:   "acp",
				Sender:   "acp_user",
			},
			Reason: "High-risk shell command",
		}},
	}
	handler := BackendPromptHandler(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := handler(ctx, PromptTurn{SessionID: "acp-1", Prompt: "run cleanup"})
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if !fake.pendingCalled {
		t.Fatal("expected PendingPermissions to be called")
	}
	for _, want := range []string{
		"Pending approval",
		"perm-1",
		"bash",
		"rm -rf build",
		"/approve",
		"/deny perm-1",
	} {
		if !strings.Contains(result.FinalText, want) {
			t.Fatalf("FinalText = %q, want substring %q", result.FinalText, want)
		}
	}
}

func TestBackendPromptHandlerRequestsNativeApprovalAllowSession(t *testing.T) {
	fake := &fakeHandlerBackend{
		submitResult: &backend.SubmitResult{
			SessionID:        "sess-1",
			TurnID:           "turn-1",
			Status:           "pending_approval",
			PendingApproval:  true,
			PendingRequestID: "perm-1",
		},
		pending: []tools.PendingPermission{{
			ID: "perm-1",
			Request: tools.PermissionRequest{
				ToolName: "bash",
				Action:   "execute",
				Command:  "npm test",
			},
			Reason: "Needs shell access",
		}},
		resolution: tools.PermissionResolution{ResumeTurnID: "turn-2", ResumeOutput: "tests passed"},
	}
	requester := &recordingPermissionRequester{
		response: acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(acp.PermissionOptionId("allow_session"))},
	}
	handler := BackendPromptHandler(fake)
	updater := &recordingUpdater{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	var result PromptResult
	var err error
	go func() {
		defer wg.Done()
		result, err = handler(ctx, PromptTurn{SessionID: "acp-1", Prompt: "run tests", Updater: updater, PermissionRequester: requester})
	}()
	waitForSink(t, fake)
	waitForApprove(t, fake)
	fake.sink.Emit(events.Event{
		SessionID: "sess-1",
		TurnID:    "turn-2",
		Type:      events.EventToolCallStarted,
		Timestamp: time.Now(),
		Payload:   events.ToolCallPayload{ID: "tool-1", Name: "bash", Input: map[string]interface{}{"command": "npm test"}},
	})
	fake.sink.Emit(events.Event{
		SessionID: "sess-1",
		TurnID:    "turn-2",
		Type:      events.EventToolCallFinished,
		Timestamp: time.Now(),
		Payload:   events.ToolCallPayload{ID: "tool-1", Name: "bash", Output: "ok"},
	})
	fake.sink.Emit(events.Event{
		SessionID: "sess-1",
		TurnID:    "turn-2",
		Type:      events.EventAssistantTextDelta,
		Timestamp: time.Now(),
		Payload:   events.TextPayload{Text: "tests passed"},
	})
	fake.sink.Emit(events.Event{
		SessionID: "sess-1",
		TurnID:    "turn-2",
		Type:      events.EventTurnCompleted,
		Timestamp: time.Now(),
		Payload:   events.TurnPayload{Status: "completed"},
	})
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("handler did not complete after approval resume")
	}
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if len(requester.requests) != 1 {
		t.Fatalf("expected one native permission request, got %+v", requester.requests)
	}
	if !fake.approveCalled || fake.approvedScope != tools.PermissionGrantSession {
		t.Fatalf("expected session approval, called=%v scope=%q", fake.approveCalled, fake.approvedScope)
	}
	if result.FinalText != "tests passed" {
		t.Fatalf("expected resume output, got %q", result.FinalText)
	}
	var sawToolUpdate bool
	for _, update := range updater.updates {
		if update.ToolCallUpdate != nil && update.ToolCallUpdate.ToolCallId == acp.ToolCallId("tool-1") {
			sawToolUpdate = true
			break
		}
	}
	if !sawToolUpdate {
		t.Fatalf("expected resumed turn tool call updates, got %+v", updater.updates)
	}
}

func TestBackendPromptHandlerRequestsNativeApprovalDeny(t *testing.T) {
	fake := &fakeHandlerBackend{
		submitResult: &backend.SubmitResult{SessionID: "sess-1", TurnID: "turn-1", Status: "pending_approval", PendingApproval: true, PendingRequestID: "perm-1"},
		pending:      []tools.PendingPermission{{ID: "perm-1", Request: tools.PermissionRequest{ToolName: "bash"}}},
	}
	requester := &recordingPermissionRequester{
		response: acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(acp.PermissionOptionId("deny"))},
	}
	handler := BackendPromptHandler(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := handler(ctx, PromptTurn{SessionID: "acp-1", Prompt: "run tests", PermissionRequester: requester})
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if !fake.denyCalled {
		t.Fatal("expected DenyPermission to be called")
	}
	if !strings.Contains(result.FinalText, "Denied permission perm-1") {
		t.Fatalf("unexpected denial text: %q", result.FinalText)
	}
}

func TestBackendPromptHandlerFallsBackWhenNativeApprovalFails(t *testing.T) {
	fake := &fakeHandlerBackend{
		submitResult: &backend.SubmitResult{SessionID: "sess-1", TurnID: "turn-1", Status: "pending_approval", PendingApproval: true, PendingRequestID: "perm-1"},
		pending:      []tools.PendingPermission{{ID: "perm-1", Request: tools.PermissionRequest{ToolName: "bash", Command: "npm test"}}},
	}
	requester := &recordingPermissionRequester{err: context.Canceled}
	handler := BackendPromptHandler(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := handler(ctx, PromptTurn{SessionID: "acp-1", Prompt: "run tests", PermissionRequester: requester})
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if fake.approveCalled || fake.denyCalled {
		t.Fatal("expected no backend approval when native request fails")
	}
	if !strings.Contains(result.FinalText, "/approve") || !strings.Contains(result.FinalText, "/deny perm-1") {
		t.Fatalf("expected text fallback, got %q", result.FinalText)
	}
}
