package feishu

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/runtime/channels"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

type stubCaller struct {
	mu        sync.Mutex
	responses []protocol.Response
	calls     int
}

func (c *stubCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	_ = req
	c.mu.Lock()
	defer c.mu.Unlock()
	resp := c.responses[c.calls]
	c.calls++
	return &resp, nil
}

type fakeAPIClient struct {
	mu        sync.Mutex
	endpoint  string
	sent      []sentMessage
	resources map[string]downloadedResource
	uploads   []uploadedFile
	patched   []patchedCard
}

type sentMessage struct {
	chatID string
	kind   string
	text   string
	key    string
	body   string
}

type uploadedFile struct {
	kind string
	path string
	name string
	key  string
}

type patchedCard struct {
	messageID string
	content   string
}

func (c *fakeAPIClient) FetchWSEndpoint(context.Context) (string, error) {
	return c.endpoint, nil
}

func (c *fakeAPIClient) SendText(ctx context.Context, chatID, text string) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, sentMessage{chatID: chatID, kind: "text", text: text})
	return nil
}

func (c *fakeAPIClient) SendPost(ctx context.Context, chatID, content string) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, sentMessage{chatID: chatID, kind: "post", body: content})
	return nil
}

func (c *fakeAPIClient) CreateCard(ctx context.Context, chatID, content string) (string, error) {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	messageID := "om_card_" + strconv.Itoa(len(c.sent)+1)
	c.sent = append(c.sent, sentMessage{chatID: chatID, kind: "card", body: content, key: messageID})
	return messageID, nil
}

func (c *fakeAPIClient) PatchCard(ctx context.Context, messageID, content string) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	c.patched = append(c.patched, patchedCard{messageID: messageID, content: content})
	return nil
}

func (c *fakeAPIClient) SendFile(ctx context.Context, chatID, fileKey string) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, sentMessage{chatID: chatID, kind: "file", key: fileKey})
	return nil
}

func (c *fakeAPIClient) SendImage(ctx context.Context, chatID, imageKey string) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, sentMessage{chatID: chatID, kind: "image", key: imageKey})
	return nil
}

func (c *fakeAPIClient) UploadFile(ctx context.Context, path, name string) (string, error) {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	key := "file_" + filepath.Base(path)
	c.uploads = append(c.uploads, uploadedFile{kind: "file", path: path, name: name, key: key})
	return key, nil
}

func (c *fakeAPIClient) UploadImage(ctx context.Context, path string) (string, error) {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	key := "image_" + filepath.Base(path)
	c.uploads = append(c.uploads, uploadedFile{kind: "image", path: path, name: filepath.Base(path), key: key})
	return key, nil
}

func (c *fakeAPIClient) DownloadMessageResource(ctx context.Context, messageID, fileKey, resourceType string) (downloadedResource, error) {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	resource, ok := c.resources[messageID+":"+fileKey+":"+resourceType]
	if !ok {
		return downloadedResource{}, os.ErrNotExist
	}
	return resource, nil
}

type fakeSocket struct {
	mu           sync.Mutex
	connectCalls int
	closed       bool
	block        chan struct{}
}

func (s *fakeSocket) Connect(ctx context.Context, endpoint string, handler func(context.Context, []byte) error) error {
	_ = ctx
	_ = endpoint
	_ = handler
	s.mu.Lock()
	s.connectCalls++
	block := s.block
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	return nil
}

func (s *fakeSocket) Close() error {
	s.mu.Lock()
	s.closed = true
	if s.block != nil {
		close(s.block)
		s.block = nil
	}
	s.mu.Unlock()
	return nil
}

func (s *fakeSocket) stats() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectCalls, s.closed
}

func TestChannelRoutesDirectMessages(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Feishu = config.FeishuConfig{Enabled: true, AppID: "cli_app", AppSecret: "secret", Domain: "lark"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := channels.NewManager(cfg, service)
	api := &fakeAPIClient{}
	socket := &fakeSocket{}
	channel, err := New(cfg, manager, WithAPIClient(api), WithSocketClient(socket))
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}

	err = channel.handleMessageEvent(context.Background(), textEvent("p2p", "oc_chat_1", "ou_user_1", "hello"))
	if err != nil {
		t.Fatalf("handle direct message: %v", err)
	}
	if len(api.sent) != 1 || api.sent[0].kind != "card" || !strings.Contains(api.sent[0].body, "assistant reply") {
		t.Fatalf("unexpected direct-message replies: %#v", api.sent)
	}
}

func TestChannelRoutesSlashCommands(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Feishu = config.FeishuConfig{Enabled: true, AppID: "cli_app", AppSecret: "secret", Domain: "lark"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := channels.NewManager(cfg, service)
	api := &fakeAPIClient{}
	socket := &fakeSocket{}
	channel, err := New(cfg, manager, WithAPIClient(api), WithSocketClient(socket))
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}

	err = channel.handleMessageEvent(context.Background(), textEvent("p2p", "oc_chat_1", "ou_user_1", "/help"))
	if err != nil {
		t.Fatalf("handle slash command: %v", err)
	}
	if len(api.sent) != 1 || api.sent[0].kind != "card" {
		t.Fatalf("expected command output reply, got %#v", api.sent)
	}
}

func TestChannelIgnoresGroupMessagesWithoutMention(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Feishu = config.FeishuConfig{Enabled: true, AppID: "cli_app", AppSecret: "secret", Domain: "lark"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := channels.NewManager(cfg, service)
	api := &fakeAPIClient{}
	socket := &fakeSocket{}
	channel, err := New(cfg, manager, WithAPIClient(api), WithSocketClient(socket))
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}

	err = channel.handleMessageEvent(context.Background(), textEvent("group", "oc_chat_1", "ou_user_1", "hello"))
	if err != nil {
		t.Fatalf("handle group message: %v", err)
	}
	if len(api.sent) != 0 {
		t.Fatalf("expected unmentioned group message to be ignored, got %#v", api.sent)
	}
}

func TestChannelRoutesMentionedGroupMessages(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Feishu = config.FeishuConfig{Enabled: true, AppID: "cli_app", AppSecret: "secret", Domain: "lark"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant group reply")}}}})
	manager := channels.NewManager(cfg, service)
	api := &fakeAPIClient{}
	socket := &fakeSocket{}
	channel, err := New(cfg, manager, WithAPIClient(api), WithSocketClient(socket))
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}

	event := textEvent("group", "oc_chat_1", "ou_user_1", "@GoDex 今天深圳天气怎么样")
	event.Event.Message.Mentions = []*larkim.MentionEvent{
		{Name: stringPtr("GoDex"), Key: stringPtr("@_bot_1")},
	}
	if err := channel.handleMessageEvent(context.Background(), event); err != nil {
		t.Fatalf("handle mentioned group message: %v", err)
	}
	if len(api.sent) != 1 || api.sent[0].kind != "card" || !strings.Contains(api.sent[0].body, "assistant group reply") {
		t.Fatalf("unexpected mentioned group reply: %#v", api.sent)
	}
}

func TestChannelPromptsWhenGroupMentionHasNoQuestion(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Feishu = config.FeishuConfig{Enabled: true, AppID: "cli_app", AppSecret: "secret", Domain: "lark"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})
	manager := channels.NewManager(cfg, service)
	api := &fakeAPIClient{}
	socket := &fakeSocket{}
	channel, err := New(cfg, manager, WithAPIClient(api), WithSocketClient(socket))
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}

	event := textEvent("group", "oc_chat_1", "ou_user_1", "@GoDex")
	event.Event.Message.Mentions = []*larkim.MentionEvent{
		{Name: stringPtr("GoDex")},
	}
	if err := channel.handleMessageEvent(context.Background(), event); err != nil {
		t.Fatalf("handle empty group mention: %v", err)
	}
	if len(api.sent) != 1 || api.sent[0].kind != "text" || api.sent[0].text != feishuText(textGroupMentionHint) {
		t.Fatalf("unexpected group mention hint: %#v", api.sent)
	}
}

func TestChannelRoutesImageMessagesAsAttachments(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Feishu = config.FeishuConfig{Enabled: true, AppID: "cli_app", AppSecret: "secret", Domain: "lark"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := channels.NewManager(cfg, service)
	api := &fakeAPIClient{
		resources: map[string]downloadedResource{
			"om_message_1:img_key:image": {
				ContentType: "image/png",
				Data:        bytes.Repeat([]byte{1, 2, 3, 4}, 4),
			},
		},
	}
	socket := &fakeSocket{}
	channel, err := New(cfg, manager, WithAPIClient(api), WithSocketClient(socket))
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}

	event := textEvent("p2p", "oc_chat_1", "ou_user_1", "")
	event.Event.Message.MessageType = stringPtr("image")
	event.Event.Message.Content = stringPtr(`{"image_key":"img_key"}`)
	if err := channel.handleMessageEvent(context.Background(), event); err != nil {
		t.Fatalf("handle image message: %v", err)
	}
	if len(api.sent) != 1 || api.sent[0].kind != "card" || !strings.Contains(api.sent[0].body, "assistant reply") {
		t.Fatalf("unexpected reply after image message: %#v", api.sent)
	}

	opened, err := service.OpenSession(context.Background(), rtbackend.SessionLocator{
		Channel: "feishu",
		Key:     "oc_chat_1",
		UserID:  "ou_user_1",
	})
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Messages) == 0 || snapshot.Messages[0].Metadata == nil || len(snapshot.Messages[0].Metadata.Attachments) != 1 {
		t.Fatalf("expected stored attachment metadata, got %#v", snapshot.Messages)
	}
	if got := snapshot.Messages[0].Metadata.Attachments[0].MIMEType; got != "image/png" {
		t.Fatalf("expected image attachment mime type, got %q", got)
	}
}

func TestChannelStartAndStop(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Feishu = config.FeishuConfig{Enabled: true, AppID: "cli_app", AppSecret: "secret", Domain: "lark"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := channels.NewManager(cfg, service)
	api := &fakeAPIClient{endpoint: "wss://example.com?service_id=1"}
	socket := &fakeSocket{block: make(chan struct{})}
	channel, err := New(cfg, manager, WithAPIClient(api), WithSocketClient(socket))
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}

	if err := channel.Start(context.Background()); err != nil {
		t.Fatalf("start channel: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	connectCalls, _ := socket.stats()
	if connectCalls == 0 {
		t.Fatalf("expected socket connect on start")
	}

	if err := channel.Stop(context.Background()); err != nil {
		t.Fatalf("stop channel: %v", err)
	}
	_, closed := socket.stats()
	if !closed {
		t.Fatalf("expected socket to close on stop")
	}
}

func TestReplySenderSplitsLongReplyPlan(t *testing.T) {
	api := &fakeAPIClient{}
	sender := replySender{api: api, chatID: "oc_chat_1"}
	plan := channels.ReplyPlan{Text: strings.Repeat("这是一段很长的回复。", 600)}

	if err := sender.SendReplyPlan(context.Background(), plan); err != nil {
		t.Fatalf("send reply plan: %v", err)
	}
	if len(api.sent) != 1 || api.sent[0].kind != "card" {
		t.Fatalf("expected long reply to render as one markdown card, got %#v", api.sent)
	}
}

func TestReplySenderUploadsArtifacts(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "chart.png")
	filePath := filepath.Join(dir, "report.md")
	if err := os.WriteFile(imagePath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("# report\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	api := &fakeAPIClient{}
	sender := replySender{api: api, chatID: "oc_chat_1"}
	plan := channels.ReplyPlan{
		Text: "assistant reply",
		Artifacts: []channels.ReplyArtifact{
			{Path: imagePath, Name: "chart.png"},
			{Path: filePath, Name: "report.md"},
		},
	}

	if err := sender.SendReplyPlan(context.Background(), plan); err != nil {
		t.Fatalf("send reply plan with artifacts: %v", err)
	}

	if len(api.uploads) != 2 {
		t.Fatalf("expected two uploads, got %#v", api.uploads)
	}
	if len(api.sent) != 3 {
		t.Fatalf("expected card + two artifact messages, got %#v", api.sent)
	}
	if api.sent[0].kind != "card" {
		t.Fatalf("expected first reply to be card summary, got %#v", api.sent)
	}
	if api.sent[1].kind != "image" || api.sent[2].kind != "file" {
		t.Fatalf("expected image then file messages, got %#v", api.sent)
	}
}

func TestReplySenderUploadsPDFArtifactAsFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "manual.pdf")
	if err := os.WriteFile(filePath, []byte("%PDF-1.7 fake bytes"), 0644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	api := &fakeAPIClient{}
	sender := replySender{api: api, chatID: "oc_chat_1"}
	plan := channels.ReplyPlan{
		Text: "assistant reply",
		Artifacts: []channels.ReplyArtifact{
			{Path: filePath, Name: "manual.pdf"},
		},
	}

	if err := sender.SendReplyPlan(context.Background(), plan); err != nil {
		t.Fatalf("send reply plan with pdf artifact: %v", err)
	}

	if len(api.uploads) != 1 {
		t.Fatalf("expected one upload, got %#v", api.uploads)
	}
	if api.uploads[0].kind != "file" || api.uploads[0].name != "manual.pdf" {
		t.Fatalf("expected pdf to upload as file, got %#v", api.uploads)
	}
	if len(api.sent) != 2 || api.sent[1].kind != "file" {
		t.Fatalf("expected card then file message, got %#v", api.sent)
	}
}

func TestRenderReplyPlanIncludesApprovalSummary(t *testing.T) {
	plan := channels.ReplyPlan{
		Text:   "This action is waiting for approval.",
		Status: "pending_approval",
		Approvals: []channels.ReplyApproval{{
			RequestID: "req-1",
			ToolName:  "bash",
			Action:    "run",
			Command:   "node -e 'console.log(1)'",
			Reason:    "shell command requires review",
			InputPreview: []channels.ReplyInputPreview{
				{Key: "command", Value: "node -e 'console.log(1)'"},
				{Key: "token", Value: "[redacted]"},
			},
		}},
		Notices: []string{"Reply `/approve` to allow once, `/approve session` to allow this session, or `/deny req-1` to reject."},
	}

	post := renderPostBody(plan)
	card := renderCardBody(plan)
	for _, body := range []string{post, card} {
		for _, want := range []string{"req-1", "bash", "node -e", "token: [redacted]", "/approve"} {
			if !strings.Contains(body, want) {
				t.Fatalf("expected approval body to contain %q, got %q", want, body)
			}
		}
	}
}

func TestRenderReplyPlanIncludesTodoSummary(t *testing.T) {
	plan := channels.ReplyPlan{
		Text: "Working on it.",
		Todos: &channels.ReplyTodoList{
			Total:     2,
			Completed: 1,
			Pending:   1,
			Items: []channels.ReplyTodoItem{
				{Content: "Inspect changes", Status: "completed", ActiveForm: "Inspecting changes"},
				{Content: "Run tests", Status: "pending", ActiveForm: "Running tests"},
			},
		},
	}

	post := renderPostBody(plan)
	card := renderCardBody(plan)
	for _, body := range []string{post, card} {
		for _, want := range []string{"Working on it.", "Todo list (1/2 completed)", "[x] Inspect changes", "[ ] Run tests"} {
			if !strings.Contains(body, want) {
				t.Fatalf("expected todo body to contain %q, got %q", want, body)
			}
		}
	}
}

func TestReplySenderAckCreatesAndPatchesSameCard(t *testing.T) {
	api := &fakeAPIClient{}
	sender := &replySender{api: api, chatID: "oc_chat_1"}

	if err := sender.SendAck(context.Background()); err != nil {
		t.Fatalf("send ack: %v", err)
	}
	if len(api.sent) != 1 || api.sent[0].kind != "card" {
		t.Fatalf("expected ack to create one card, got %#v", api.sent)
	}

	plan := channels.ReplyPlan{Text: "final markdown body", Status: "completed"}
	if err := sender.SendReplyPlan(context.Background(), plan); err != nil {
		t.Fatalf("send reply plan after ack: %v", err)
	}
	if len(api.sent) != 1 {
		t.Fatalf("expected final reply to patch existing card instead of sending a new one, got %#v", api.sent)
	}
	if len(api.patched) != 1 || api.patched[0].messageID != api.sent[0].key || !strings.Contains(api.patched[0].content, "final markdown body") {
		t.Fatalf("expected patched final card, got sent=%#v patched=%#v", api.sent, api.patched)
	}
}

func textEvent(chatType, chatID, senderID, text string) *larkim.P2MessageReceiveV1 {
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId: &larkim.UserId{OpenId: stringPtr(senderID)},
			},
			Message: &larkim.EventMessage{
				MessageId:   stringPtr("om_message_1"),
				ChatId:      stringPtr(chatID),
				ChatType:    stringPtr(chatType),
				MessageType: stringPtr("text"),
				Content:     stringPtr(`{"text":"` + text + `"}`),
			},
		},
	}
}

func stringPtr(value string) *string {
	return &value
}

func newTestService(cfg *config.Config, caller *stubCaller) *rtbackend.Service {
	shared := agent.NewSharedDependenciesWithCaller(cfg, caller)
	return rtbackend.NewService(cfg, shared, commands.NewService(cfg))
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
