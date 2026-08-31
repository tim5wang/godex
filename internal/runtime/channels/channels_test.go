package channels

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/app"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/platform/logger"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/sessionadmin"
)

type stubCaller struct {
	mu        sync.Mutex
	responses []protocol.Response
	calls     int
	delay     time.Duration
}

func (c *stubCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	_ = req
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	resp := c.responses[c.calls]
	c.calls++
	return &resp, nil
}

type captureReply struct {
	mu    sync.Mutex
	text  string
	ack   string
	final []string
}

func (c *captureReply) SendText(ctx context.Context, text string) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	c.text = text
	c.final = append(c.final, text)
	return nil
}

func (c *captureReply) SendAck(ctx context.Context) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ack = "ack"
	return nil
}

func (c *captureReply) snapshot() (string, string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text, c.ack, len(c.final)
}

type capturePlanReply struct {
	captureReply
	plan ReplyPlan
}

func (c *capturePlanReply) SendReplyPlan(ctx context.Context, plan ReplyPlan) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	c.plan = plan
	c.text = plan.Text
	c.final = append(c.final, plan.RenderText())
	return nil
}

type stubFactory struct {
	name    string
	enabled bool
	channel Channel
}

func (f stubFactory) Name() string                                    { return f.name }
func (f stubFactory) Enabled(*config.Config) bool                     { return f.enabled }
func (f stubFactory) Build(*config.Config, *Manager) (Channel, error) { return f.channel, nil }

type stubChannel struct {
	name       string
	started    bool
	stopped    bool
	startErr   error
	stopErr    error
	startCalls int
	stopCalls  int
}

func (c *stubChannel) Name() string { return c.name }
func (c *stubChannel) Start(context.Context) error {
	c.startCalls++
	if c.startErr != nil {
		return c.startErr
	}
	c.started = true
	return nil
}
func (c *stubChannel) Stop(context.Context) error {
	c.stopCalls++
	if c.stopErr != nil {
		return c.stopErr
	}
	c.stopped = true
	return nil
}

type buildFuncFactory struct {
	name    string
	enabled bool
	build   func(*config.Config, *Manager) (Channel, error)
}

func (f buildFuncFactory) Name() string                { return f.name }
func (f buildFuncFactory) Enabled(*config.Config) bool { return f.enabled }
func (f buildFuncFactory) Build(cfg *config.Config, manager *Manager) (Channel, error) {
	return f.build(cfg, manager)
}

type deliverStubChannel struct {
	stubChannel
	target   automation.DeliveryTarget
	plan     ReplyPlan
	calls    int
	failures int
	err      error
}

func (c *deliverStubChannel) Deliver(ctx context.Context, target automation.DeliveryTarget, plan ReplyPlan) error {
	_ = ctx
	c.calls++
	if c.calls <= c.failures {
		if c.err != nil {
			return c.err
		}
		return errors.New("temporary delivery failure")
	}
	c.target = target.Clone()
	c.plan = plan
	return nil
}

func TestManagerRouteInboundSendsAssistantReply(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := NewManager(cfg, service)

	reply := &captureReply{}
	err := manager.RouteInbound(context.Background(), InboundMessage{
		Channel:    "feishu",
		SessionKey: "oc_chat_1",
		Sender:     "ou_user_1",
		Text:       "hello",
	}, reply)
	if err != nil {
		t.Fatalf("route inbound: %v", err)
	}
	if reply.text != "assistant reply" {
		t.Fatalf("expected assistant reply, got %q", reply.text)
	}
}

func TestManagerRouteInboundDerivesSessionKeyFromRouting(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := NewManager(cfg, service)

	reply := &captureReply{}
	err := manager.RouteInbound(context.Background(), InboundMessage{
		Channel: "feishu",
		Sender:  "ou_user_1",
		Text:    "hello",
		Routing: RoutingIdentity{
			ChannelID:   "feishu",
			PlatformID:  "oc_chat_1",
			ThreadID:    "thread_1",
			SenderID:    "ou_user_1",
			SessionMode: SessionModePerThread,
		},
	}, reply)
	if err != nil {
		t.Fatalf("route inbound: %v", err)
	}
	opened, err := service.OpenSession(context.Background(), rtbackend.SessionLocator{
		Channel: "feishu",
		Key:     "oc_chat_1:thread_1",
		UserID:  "ou_user_1",
	})
	if err != nil {
		t.Fatalf("reopen routed session: %v", err)
	}
	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Messages) == 0 {
		t.Fatalf("expected routed message in session")
	}
	meta := snapshot.Locator.Metadata
	if meta == nil {
		t.Fatalf("expected routing metadata on locator, got %#v", snapshot.Locator)
	}
	if got := meta[MetadataThreadID]; got != "thread_1" {
		t.Fatalf("expected routing thread metadata, got %q", got)
	}
	if got := meta[MetadataSessionMode]; got != SessionModePerThread {
		t.Fatalf("expected routing session mode, got %q", got)
	}
}

func TestRoutingMetadataPreservesAdapterValues(t *testing.T) {
	meta := metadataWithRouting(map[string]string{
		MetadataPlatformID: "adapter-platform",
		"message_id":       "msg-1",
	}, RoutingIdentity{
		ChannelID:   "feishu",
		PlatformID:  "derived-platform",
		ThreadID:    "thread-1",
		SenderID:    "sender-1",
		SessionMode: SessionModePerThread,
	})
	if got := meta[MetadataPlatformID]; got != "adapter-platform" {
		t.Fatalf("expected adapter platform metadata to win, got %q", got)
	}
	if got := meta[MetadataThreadID]; got != "thread-1" {
		t.Fatalf("expected derived thread metadata, got %q", got)
	}
	if got := meta["message_id"]; got != "msg-1" {
		t.Fatalf("expected existing metadata preserved, got %q", got)
	}
}

func TestManagerRouteInboundDeniesSenderOutsideAccessGate(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Weixin.AllowFrom = []string{"allowed-user"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := NewManager(cfg, service)

	err := manager.RouteInbound(context.Background(), InboundMessage{
		Channel:    "weixin",
		SessionKey: "wx-chat-1",
		Sender:     "blocked-user",
		Text:       "hello",
		Routing: RoutingIdentity{
			ChannelID:   "weixin",
			PlatformID:  "wx-chat-1",
			SenderID:    "blocked-user",
			SessionMode: SessionModeShared,
		},
	}, &captureReply{})
	if err == nil || !automation.IsBlockedError(err) {
		t.Fatalf("expected blocked access error, got %v", err)
	}
	sessions, listErr := service.ListSessions(context.Background(), rtbackend.SessionListFilter{Channel: "weixin"})
	if listErr != nil {
		t.Fatalf("list sessions: %v", listErr)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected denied inbound to avoid session creation, got %#v", sessions)
	}
	report := manager.StatusReport()
	if len(report.Channels) != 1 || report.Channels[0].LastAccess == nil || report.Channels[0].LastAccess.Action != AccessActionDeny {
		t.Fatalf("expected denied access status, got %#v", report.Channels)
	}
	audit, auditErr := service.SecurityAudit(context.Background(), 10)
	if auditErr != nil {
		t.Fatalf("security audit: %v", auditErr)
	}
	if len(audit) == 0 || audit[0].Category != "channel_access" || audit[0].Action != AccessActionDeny {
		t.Fatalf("expected channel access audit entry, got %#v", audit)
	}
}

func TestManagerRouteInboundIgnoresDuplicateMessage(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}}
	service := newTestService(cfg, caller)
	manager := NewManager(cfg, service)

	firstReply := &captureReply{}
	inbound := InboundMessage{
		Channel:    "feishu",
		SessionKey: "oc_chat_1",
		Sender:     "ou_user_1",
		Text:       "hello",
		Metadata:   map[string]string{"message_id": "om_message_1"},
	}
	if err := manager.RouteInbound(context.Background(), inbound, firstReply); err != nil {
		t.Fatalf("route first inbound: %v", err)
	}
	secondReply := &captureReply{}
	if err := manager.RouteInbound(context.Background(), inbound, secondReply); err != nil {
		t.Fatalf("route duplicate inbound: %v", err)
	}

	firstText, _, _ := firstReply.snapshot()
	secondText, secondAck, secondFinals := secondReply.snapshot()
	if firstText != "assistant reply" {
		t.Fatalf("expected first inbound reply, got %q", firstText)
	}
	if secondText != "" || secondAck != "" || secondFinals != 0 {
		t.Fatalf("expected duplicate inbound to be ignored, got text=%q ack=%q finals=%d", secondText, secondAck, secondFinals)
	}
	if caller.calls != 1 {
		t.Fatalf("expected one model call after duplicate filtering, got %d", caller.calls)
	}
}

func TestManagerRouteInboundSendsSlowAck(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		delay:     40 * time.Millisecond,
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}},
	}
	service := newTestService(cfg, caller)
	manager := NewManager(cfg, service)
	manager.slowAckDelay = 5 * time.Millisecond

	reply := &captureReply{}
	if err := manager.RouteInbound(context.Background(), InboundMessage{
		Channel:    "feishu",
		SessionKey: "oc_chat_1",
		Sender:     "ou_user_1",
		Text:       "hello",
	}, reply); err != nil {
		t.Fatalf("route inbound: %v", err)
	}

	text, ack, finals := reply.snapshot()
	if ack == "" {
		t.Fatalf("expected slow ack to be sent before final reply")
	}
	if text != "assistant reply" {
		t.Fatalf("expected final assistant reply, got %q", text)
	}
	if finals != 1 {
		t.Fatalf("expected exactly one final reply, got %d", finals)
	}
}

func TestManagerRouteInboundCommandsUseCommandService(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := NewManager(cfg, service)

	reply := &captureReply{}
	err := manager.RouteInbound(context.Background(), InboundMessage{
		Channel:    "feishu",
		SessionKey: "oc_chat_1",
		Sender:     "ou_user_1",
		Text:       "/help",
	}, reply)
	if err != nil {
		t.Fatalf("route inbound command: %v", err)
	}
	if reply.text == "" {
		t.Fatal("expected command output")
	}
}

func TestManagerRouteInboundPendingApprovalSendsApproveInstructions(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.TextBlock("I need approval to continue."),
			protocol.ToolUseBlock("tool-1", "write_file", map[string]interface{}{"path": "notes/todo.txt", "content": "hello"}),
		}},
	}})
	manager := NewManager(cfg, service)

	reply := &captureReply{}
	err := manager.RouteInbound(context.Background(), InboundMessage{
		Channel:    "feishu",
		SessionKey: "oc_chat_pending",
		Sender:     "ou_user_1",
		Text:       "write todo",
	}, reply)
	if err != nil {
		t.Fatalf("route inbound pending approval: %v", err)
	}
	if !strings.Contains(reply.text, "/approve") || !strings.Contains(reply.text, "approval") {
		t.Fatalf("expected approve instructions in reply, got %q", reply.text)
	}
	for _, want := range []string{"write_file", "notes/todo.txt", "content: hello"} {
		if !strings.Contains(reply.text, want) {
			t.Fatalf("expected approval detail %q in reply, got %q", want, reply.text)
		}
	}
	if got := strings.Count(reply.text, "Approval is required before GoDex can continue."); got != 1 {
		t.Fatalf("expected a single approval notice, got %d in %q", got, reply.text)
	}
}

func TestReplyPlanRenderTextRedactsApprovalSensitiveInput(t *testing.T) {
	plan := ReplyPlan{
		Text: "This action is waiting for approval.",
		Approvals: []ReplyApproval{{
			RequestID: "abc123",
			ToolName:  "web_fetch",
			Action:    "fetch",
			Reason:    "remote channel requires review",
			InputPreview: []ReplyInputPreview{
				{Key: "url", Value: "https://example.com/private"},
				{Key: "api_key", Value: "[redacted]"},
			},
		}},
		Notices: []string{pendingApprovalNotice("abc123")},
	}

	text := plan.RenderText()
	for _, want := range []string{"Approval request:", "abc123", "web_fetch", "url: https://example.com/private", "api_key: [redacted]", "/approve"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected rendered approval to contain %q, got %q", want, text)
		}
	}
	if strings.Contains(text, "secret-value") {
		t.Fatalf("expected sensitive value to be redacted, got %q", text)
	}
}

func TestReplyPlanRenderTextIncludesTodoSummary(t *testing.T) {
	plan := ReplyPlan{
		Text: "Working on it.",
		Todos: &ReplyTodoList{
			Total:     2,
			Completed: 1,
			Pending:   1,
			Items: []ReplyTodoItem{
				{Content: "Inspect changes", Status: "completed", ActiveForm: "Inspecting changes"},
				{Content: "Run tests", Status: "pending", ActiveForm: "Running tests"},
			},
		},
	}

	text := plan.RenderText()
	for _, want := range []string{"Working on it.", "Todo list (1/2 completed)", "[x] Inspect changes", "[ ] Run tests"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected rendered reply to contain %q, got %q", want, text)
		}
	}
}

func TestManagerRouteInboundApproveCommandResumesPendingTurn(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "command -v sh"})}},
		{Content: []protocol.Block{protocol.TextBlock("command output")}},
	}})
	manager := NewManager(cfg, service)

	inbound := InboundMessage{
		Channel:    "weixin",
		SessionKey: "wx-chat-approval",
		Sender:     "wx-user-1",
		Text:       "run command -v sh",
	}
	firstReply := &captureReply{}
	if err := manager.RouteInbound(context.Background(), inbound, firstReply); err != nil {
		t.Fatalf("route inbound pending approval: %v", err)
	}
	matches := regexp.MustCompile(`/deny\s+([a-f0-9]+)`).FindStringSubmatch(firstReply.text)
	if len(matches) != 2 {
		t.Fatalf("expected deny instructions with request id, got %q", firstReply.text)
	}

	secondReply := &captureReply{}
	if err := manager.RouteInbound(context.Background(), InboundMessage{
		Channel:    inbound.Channel,
		SessionKey: inbound.SessionKey,
		Sender:     inbound.Sender,
		Text:       "/approve session",
	}, secondReply); err != nil {
		t.Fatalf("route inbound approve command: %v", err)
	}
	if !strings.Contains(secondReply.text, "Permission approved") || !strings.Contains(secondReply.text, "Resume: completed") || !strings.Contains(secondReply.text, "command output") {
		t.Fatalf("unexpected approve command output: %q", secondReply.text)
	}

	opened, err := manager.OpenInboundSession(context.Background(), inbound)
	if err != nil {
		t.Fatalf("open inbound session: %v", err)
	}
	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.PendingPermissions) != 0 {
		t.Fatalf("expected no pending permissions after approve, got %+v", snapshot.PendingPermissions)
	}
	if got := protocol.MessageText(snapshot.Messages[len(snapshot.Messages)-1]); got != "command output" {
		t.Fatalf("expected resumed assistant output in snapshot, got %q", got)
	}
}

func TestManagerRouteInboundHistorySearchCommandSearchesCurrentSession(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("Logged the rollback checklist.")}},
	}})
	manager := NewManager(cfg, service)

	inbound := InboundMessage{
		Channel:    "feishu",
		SessionKey: "history-chat",
		Sender:     "ou-user-1",
		Text:       "Aurora rollback checklist is ready.",
	}
	if err := manager.RouteInbound(context.Background(), inbound, &captureReply{}); err != nil {
		t.Fatalf("route inbound user message: %v", err)
	}

	reply := &captureReply{}
	if err := manager.RouteInbound(context.Background(), InboundMessage{
		Channel:    inbound.Channel,
		SessionKey: inbound.SessionKey,
		Sender:     inbound.Sender,
		Text:       "/history search aurora role=user",
	}, reply); err != nil {
		t.Fatalf("route inbound history search command: %v", err)
	}
	lower := strings.ToLower(reply.text)
	if !strings.Contains(reply.text, "History search:") || !strings.Contains(lower, "aurora") || !strings.Contains(reply.text, "source=current_session") {
		t.Fatalf("unexpected history search reply: %q", reply.text)
	}
}

func TestCurrentTurnAssistantSnapshotExtraction(t *testing.T) {
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "打开网站截图"),
		protocol.NewTextMessage(protocol.RoleAssistant, "这是截图说明。"),
		{
			Role: protocol.RoleAssistant,
			Content: []protocol.Block{
				protocol.TextBlock(""),
			},
			Metadata: &protocol.Metadata{
				Attachments: []protocol.Attachment{{
					Name: "capture.png",
					Path: ".godex/.sessions/demo/attachments/capture.png",
				}},
			},
		},
	}

	if got := currentTurnAssistantText(messages); got != "这是截图说明。" {
		t.Fatalf("unexpected current turn assistant text %q", got)
	}
	artifacts := currentTurnAssistantArtifacts(messages)
	if len(artifacts) != 1 {
		t.Fatalf("expected one current-turn artifact, got %#v", artifacts)
	}
	if got := artifacts[0].Path; got != ".godex/.sessions/demo/attachments/capture.png" {
		t.Fatalf("unexpected artifact path %q", got)
	}
	if got := artifacts[0].Name; got != "capture.png" {
		t.Fatalf("unexpected artifact name %q", got)
	}
}

func TestApplyCurrentTurnSnapshotPrefersPersistedArtifacts(t *testing.T) {
	plan := ReplyPlan{
		Text: "streamed text",
		Artifacts: []ReplyArtifact{{
			Path: "/tmp/browser/session/page-1.png",
			Name: "page-1.png",
		}},
	}
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "打开网站截图"),
		protocol.NewTextMessage(protocol.RoleAssistant, "这是截图说明。"),
		{
			Role: protocol.RoleAssistant,
			Content: []protocol.Block{
				protocol.TextBlock(""),
			},
			Metadata: &protocol.Metadata{
				Attachments: []protocol.Attachment{{
					Name: "page-1.png",
					Path: ".godex/.sessions/demo/attachments/page-1.png",
				}},
			},
		},
	}

	applyCurrentTurnSnapshot(&plan, messages)
	if plan.Text != "这是截图说明。" {
		t.Fatalf("unexpected plan text %q", plan.Text)
	}
	if len(plan.Artifacts) != 1 {
		t.Fatalf("expected one persisted artifact, got %#v", plan.Artifacts)
	}
	if got := plan.Artifacts[0].Path; got != ".godex/.sessions/demo/attachments/page-1.png" {
		t.Fatalf("unexpected artifact path %q", got)
	}
}

func TestReplyCollectorBuildsStructuredPlan(t *testing.T) {
	collector := &replyCollector{}
	collector.Emit(events.Event{
		Type:    events.EventToolCallStarted,
		Payload: events.ToolCallPayload{ID: "tool-1", Name: "web_search"},
	})
	collector.Emit(events.Event{
		Type: events.EventToolCallFinished,
		Payload: events.ToolCallPayload{
			ID:            "tool-1",
			Name:          "web_search",
			Output:        "result line 1\nresult line 2",
			ArtifactPaths: []string{"/tmp/result.png"},
		},
	})
	collector.Emit(events.Event{
		Type:    events.EventWarningRaised,
		Payload: events.NoticePayload{Message: "warning text"},
	})
	collector.Emit(events.Event{
		Type:    events.EventAssistantTextDelta,
		Payload: events.TextPayload{Text: "assistant reply"},
	})

	plan := collector.Plan()
	if plan.Text != "assistant reply" {
		t.Fatalf("expected assistant text in plan, got %q", plan.Text)
	}
	if len(plan.Notices) != 1 || plan.Notices[0] != "warning text" {
		t.Fatalf("unexpected notices: %#v", plan.Notices)
	}
	if len(plan.Tools) != 1 || plan.Tools[0].Name != "web_search" || plan.Tools[0].Status != "completed" {
		t.Fatalf("unexpected tools: %#v", plan.Tools)
	}
	if len(plan.Artifacts) != 1 || plan.Artifacts[0].Path != "/tmp/result.png" {
		t.Fatalf("unexpected artifacts: %#v", plan.Artifacts)
	}
}

func TestReplyPlanRenderTextIncludesArtifactsAndNotes(t *testing.T) {
	plan := ReplyPlan{
		Text:    "assistant reply",
		Notices: []string{"note one"},
		Artifacts: []ReplyArtifact{{
			Path: "/tmp/insights-latest.md",
			Name: "insights-latest.md",
		}},
	}
	rendered := plan.RenderText()
	if !strings.Contains(rendered, "assistant reply") {
		t.Fatalf("missing main text: %q", rendered)
	}
	if !strings.Contains(rendered, "Artifacts:") || !strings.Contains(rendered, "insights-latest.md") {
		t.Fatalf("missing artifact section: %q", rendered)
	}
	if !strings.Contains(rendered, "Notes:") || !strings.Contains(rendered, "note one") {
		t.Fatalf("missing notes section: %q", rendered)
	}
}

func TestReplyPlanRenderTextListsLargeFileArtifactWithoutInliningContent(t *testing.T) {
	plan := ReplyPlan{
		Text: "generated file",
		Artifacts: []ReplyArtifact{{
			Path: "/tmp/The.Go.Programming.Language.2015.11.pdf",
			Name: "The.Go.Programming.Language.2015.11.pdf",
		}},
	}
	rendered := plan.RenderText()
	if !strings.Contains(rendered, "Artifacts:") || !strings.Contains(rendered, "The.Go.Programming.Language.2015.11.pdf") {
		t.Fatalf("missing artifact listing: %q", rendered)
	}
	if strings.Contains(rendered, "%PDF-") {
		t.Fatalf("expected large file render to avoid inlining content, got %q", rendered)
	}
}

func TestMergeReplyArtifactsDeduplicatesByPath(t *testing.T) {
	plan := &ReplyPlan{
		Artifacts: []ReplyArtifact{{
			Path: ".godex/.sessions/demo/attachments/page-1.png",
			Name: "page-1.png",
		}},
	}
	mergeReplyArtifacts(plan, []ReplyArtifact{
		{Path: ".godex/.sessions/demo/attachments/page-1.png", Name: "page-1.png"},
		{Path: ".godex/.sessions/demo/attachments/report.pdf", Name: "report.pdf"},
	})
	if len(plan.Artifacts) != 2 {
		t.Fatalf("expected deduplicated artifacts, got %#v", plan.Artifacts)
	}
	if got := plan.Artifacts[1].Path; got != ".godex/.sessions/demo/attachments/report.pdf" {
		t.Fatalf("unexpected second artifact path %q", got)
	}
}

func TestManagerStartAndStopAll(t *testing.T) {
	if err := logger.InitWithConfig(logger.Config{Level: "debug", Output: os.Stderr}); err != nil {
		t.Fatalf("init logger: %v", err)
	}
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := NewManager(cfg, service)
	channel := &stubChannel{name: "stub"}
	manager.RegisterFactory(stubFactory{name: "stub", enabled: true, channel: channel})

	if err := manager.StartAll(context.Background()); err != nil {
		t.Fatalf("start all: %v", err)
	}
	if !channel.started {
		t.Fatalf("expected channel to start")
	}
	if len(manager.RunningNames()) != 1 {
		t.Fatalf("expected one running channel, got %#v", manager.RunningNames())
	}

	if err := manager.StopAll(context.Background()); err != nil {
		t.Fatalf("stop all: %v", err)
	}
	if !channel.stopped {
		t.Fatalf("expected channel to stop")
	}
}

func TestManagerStatusTextAndDoctorAugment(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := NewManager(cfg, service)
	channel := &stubChannel{name: "feishu"}
	manager.RegisterFactory(stubFactory{name: "feishu", enabled: true, channel: channel})

	if err := manager.StartAll(context.Background()); err != nil {
		t.Fatalf("start all: %v", err)
	}
	manager.SetStatus("feishu", ChannelStatusUpdate{
		State:       StateRunning,
		Detail:      "connected to websocket",
		MarkInbound: true,
		LastEvent:   "im.message.receive_v1",
	})

	text := manager.StatusText()
	if !strings.Contains(text, "feishu") || !strings.Contains(text, StateRunning) {
		t.Fatalf("unexpected status text: %q", text)
	}

	report := manager.AugmentDoctor(config.DoctorReport{})
	var sawRunning bool
	for _, check := range report.Checks {
		if check.Code == "channel_running_feishu" {
			sawRunning = true
			break
		}
	}
	if !sawRunning {
		t.Fatalf("expected channel_running_feishu doctor check, got %#v", report.Checks)
	}
}

func TestManagerDeliverToSessionAppendsBackgroundReply(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := NewManager(cfg, service)

	opened, err := service.OpenSession(context.Background(), rtbackend.SessionLocator{Channel: "web", Key: "default"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if err := manager.Deliver(context.Background(), automation.DeliveryTarget{
		Kind:      automation.DeliveryKindSession,
		SessionID: opened.SessionID,
	}, ReplyPlan{Text: "scheduled background note"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	found := false
	for _, msg := range snapshot.Messages {
		if msg.Role == protocol.RoleAssistant && strings.Contains(protocol.MessageText(msg), "scheduled background note") {
			found = true
			if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindBackground {
				t.Fatalf("expected background kind metadata, got %#v", msg.Metadata)
			}
		}
	}
	if !found {
		t.Fatalf("expected background reply in snapshot: %#v", snapshot.Messages)
	}
}

func TestManagerDeliverToSessionAppendsArtifacts(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := NewManager(cfg, service)

	opened, err := service.OpenSession(context.Background(), rtbackend.SessionLocator{Channel: "web", Key: "artifacts"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	artifactPath := filepath.Join(t.TempDir(), "capture.png")
	if err := os.WriteFile(artifactPath, []byte("png"), 0644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := manager.Deliver(context.Background(), automation.DeliveryTarget{
		Kind:      automation.DeliveryKindSession,
		SessionID: opened.SessionID,
	}, ReplyPlan{
		Text: "scheduled background note",
		Artifacts: []ReplyArtifact{{
			Path: artifactPath,
			Name: "capture.png",
		}},
	}); err != nil {
		t.Fatalf("deliver with artifacts: %v", err)
	}
	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	found := false
	for _, msg := range snapshot.Messages {
		if msg.Role == protocol.RoleAssistant && msg.Metadata != nil && len(msg.Metadata.Attachments) > 0 {
			found = true
			if msg.Metadata.Kind != protocol.KindBackground {
				t.Fatalf("expected background kind metadata, got %#v", msg.Metadata)
			}
			if got := msg.Metadata.Attachments[0].Name; got != "capture.png" {
				t.Fatalf("expected attachment name capture.png, got %q", got)
			}
		}
	}
	if !found {
		t.Fatalf("expected attachment reply in snapshot: %#v", snapshot.Messages)
	}
}

func TestManagerReconcileRollsBackFailedRestartWithoutStoppingOtherChannels(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := NewManager(cfg, service)

	oldCfg := cfg
	newCfg := cfg.Clone()

	primaryOld := &stubChannel{name: "primary"}
	primaryRestored := &stubChannel{name: "primary"}
	primaryCandidate := &stubChannel{name: "primary", startErr: errors.New("start failed")}
	stableChannel := &stubChannel{name: "stable"}

	manager.RegisterFactory(buildFuncFactory{
		name:    "primary",
		enabled: true,
		build: func(buildCfg *config.Config, _ *Manager) (Channel, error) {
			switch buildCfg {
			case oldCfg:
				if primaryOld.started {
					return primaryRestored, nil
				}
				return primaryOld, nil
			case newCfg:
				return primaryCandidate, nil
			default:
				return nil, fmt.Errorf("unexpected config pointer")
			}
		},
	})
	manager.RegisterFactory(buildFuncFactory{
		name:    "stable",
		enabled: true,
		build: func(*config.Config, *Manager) (Channel, error) {
			return stableChannel, nil
		},
	})

	if err := manager.StartAll(context.Background()); err != nil {
		t.Fatalf("start all: %v", err)
	}

	err := manager.Reconcile(context.Background(), newCfg)
	if err == nil || !strings.Contains(err.Error(), "start failed") {
		t.Fatalf("expected restart failure, got %v", err)
	}

	report := manager.StatusReport()
	statusByName := make(map[string]ChannelStatus, len(report.Channels))
	for _, status := range report.Channels {
		statusByName[status.Name] = status
	}
	if got := statusByName["primary"].State; got != StateRunning {
		t.Fatalf("expected primary to roll back to running, got %#v", statusByName["primary"])
	}
	if got := statusByName["primary"].LastEvent; got != "reconcile_rollback" {
		t.Fatalf("expected rollback event, got %#v", statusByName["primary"])
	}
	if got := statusByName["stable"].State; got != StateRunning {
		t.Fatalf("expected stable channel to remain running, got %#v", statusByName["stable"])
	}
	if primaryOld.stopCalls != 1 {
		t.Fatalf("expected old primary to stop once, got %d", primaryOld.stopCalls)
	}
	if primaryRestored.startCalls != 1 {
		t.Fatalf("expected restored primary to restart once, got %d", primaryRestored.startCalls)
	}
	if stableChannel.stopCalls != 0 {
		t.Fatalf("expected stable channel not to be stopped, got %d", stableChannel.stopCalls)
	}
}

func TestManagerDeliverToRunningChannelUsesDeliverer(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := NewManager(cfg, service)
	channel := &deliverStubChannel{stubChannel: stubChannel{name: "feishu"}}
	manager.RegisterFactory(stubFactory{name: "feishu", enabled: true, channel: channel})
	if err := manager.StartAll(context.Background()); err != nil {
		t.Fatalf("start all: %v", err)
	}
	target := automation.DeliveryTarget{
		Kind:     automation.DeliveryKindChannel,
		Channel:  "feishu",
		Metadata: map[string]string{"chat_id": "oc_1"},
	}
	plan := ReplyPlan{Text: "scheduled ping"}
	if err := manager.Deliver(context.Background(), target, plan); err != nil {
		t.Fatalf("deliver to channel: %v", err)
	}
	if channel.target.Channel != "feishu" || channel.target.Metadata["chat_id"] != "oc_1" {
		t.Fatalf("unexpected target: %#v", channel.target)
	}
	if channel.plan.Text != "scheduled ping" {
		t.Fatalf("unexpected plan: %#v", channel.plan)
	}
}

func TestManagerDeliverRetriesAndRecordsStatus(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := NewManager(cfg, service)
	manager.deliveryDelay = time.Millisecond
	channel := &deliverStubChannel{
		stubChannel: stubChannel{name: "feishu"},
		failures:    2,
		err:         errors.New("platform temporary failure"),
	}
	manager.RegisterFactory(stubFactory{name: "feishu", enabled: true, channel: channel})
	if err := manager.StartAll(context.Background()); err != nil {
		t.Fatalf("start all: %v", err)
	}

	target := automation.DeliveryTarget{
		Kind:     automation.DeliveryKindChannel,
		Channel:  "feishu",
		Metadata: map[string]string{"chat_id": "oc_1"},
	}
	if err := manager.Deliver(context.Background(), target, ReplyPlan{Text: "scheduled ping"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if channel.calls != defaultDeliveryRetries {
		t.Fatalf("expected retry attempts, got %d", channel.calls)
	}
	report := manager.StatusReport()
	if len(report.Deliveries) != 1 {
		t.Fatalf("expected one delivery record, got %#v", report.Deliveries)
	}
	record := report.Deliveries[0]
	if record.Status != DeliveryStatusDelivered || record.Attempts != defaultDeliveryRetries || !record.FailedAt.IsZero() {
		t.Fatalf("unexpected delivery record: %#v", record)
	}
	if len(report.Channels) != 1 || report.Channels[0].LastDelivery == nil || report.Channels[0].LastDelivery.Status != DeliveryStatusDelivered {
		t.Fatalf("expected last delivery on channel status, got %#v", report.Channels)
	}
}

func TestManagerDeliverRecordsFailedStatus(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := NewManager(cfg, service)
	manager.deliveryDelay = time.Millisecond
	channel := &deliverStubChannel{
		stubChannel: stubChannel{name: "feishu"},
		failures:    defaultDeliveryRetries,
		err:         errors.New("platform down"),
	}
	manager.RegisterFactory(stubFactory{name: "feishu", enabled: true, channel: channel})
	if err := manager.StartAll(context.Background()); err != nil {
		t.Fatalf("start all: %v", err)
	}

	err := manager.Deliver(context.Background(), automation.DeliveryTarget{
		Kind:     automation.DeliveryKindChannel,
		Channel:  "feishu",
		Metadata: map[string]string{"chat_id": "oc_1"},
	}, ReplyPlan{Text: "scheduled ping"})
	if err == nil || !strings.Contains(err.Error(), "platform down") {
		t.Fatalf("expected platform failure, got %v", err)
	}
	report := manager.StatusReport()
	if len(report.Deliveries) != 1 {
		t.Fatalf("expected one delivery record, got %#v", report.Deliveries)
	}
	record := report.Deliveries[0]
	if record.Status != DeliveryStatusFailed || record.Attempts != defaultDeliveryRetries || record.LastError != "platform down" || record.FailedAt.IsZero() {
		t.Fatalf("unexpected failed delivery record: %#v", record)
	}
	if len(report.Channels) != 1 || report.Channels[0].LastDelivery == nil || report.Channels[0].LastDelivery.Status != DeliveryStatusFailed {
		t.Fatalf("expected failed last delivery on channel status, got %#v", report.Channels)
	}
}

func newTestService(cfg *config.Config, caller *stubCaller) *rtbackend.Service {
	commandService := commands.NewService(cfg)
	shared := agent.NewSharedDependenciesWithCaller(cfg, caller)
	service := rtbackend.NewService(cfg, shared, commandService)
	sessionAdmin := sessionadmin.NewService(func() *config.Config { return cfg }, service, nil, nil)
	commandService.SetSession(app.NewSessionCommandHandler(service, sessionAdmin))
	commandService.SetClear(app.NewClearCommandHandler(sessionAdmin))
	commandService.SetApprove(app.NewApproveCommandHandler(sessionAdmin))
	commandService.SetDeny(app.NewDenyCommandHandler(sessionAdmin))
	return service
}

func newTestConfig(t *testing.T) *config.Config {
	t.Helper()
	workspace := t.TempDir()
	cfg := &config.Config{
		Model:             "test-model",
		BaseURL:           "http://127.0.0.1",
		MaxTokens:         1024,
		WorkspaceDir:      workspace,
		StateDir:          filepath.Join(workspace, ".godex"),
		TeamDir:           filepath.Join(workspace, ".godex", ".team"),
		TasksDir:          filepath.Join(workspace, ".godex", ".tasks"),
		TodosDir:          filepath.Join(workspace, ".godex", ".todos"),
		MemoryDir:         filepath.Join(workspace, ".godex", "memory"),
		RulesDir:          filepath.Join(workspace, ".godex", "rules"),
		SkillsDir:         filepath.Join(workspace, ".godex", "skills"),
		MCPConfigPath:     filepath.Join(workspace, ".godex", "mcp.json"),
		TempDir:           filepath.Join(workspace, ".godex", ".tmp"),
		TranscriptsDir:    filepath.Join(workspace, ".godex", ".transcripts"),
		SessionsDir:       filepath.Join(workspace, ".godex", ".sessions"),
		CompressThreshold: 100000,
		LeadName:          "lead",
		TeamName:          "default",
	}
	for _, dir := range []string{
		filepath.Join(cfg.TeamDir, "inbox"),
		cfg.TasksDir,
		cfg.TodosDir,
		cfg.MemoryDir,
		cfg.RulesDir,
		cfg.SkillsDir,
		cfg.TempDir,
		cfg.TranscriptsDir,
		cfg.SessionsDir,
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return cfg
}
