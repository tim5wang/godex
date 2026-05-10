package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/runtime/channels"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
)

const (
	channelName        = "feishu"
	reconnectDelay     = 2 * time.Second
	maxReplyChunkRunes = 3200
)

var feishuLog = logger.New("feishu")

type apiClient interface {
	FetchWSEndpoint(context.Context) (string, error)
	SendText(context.Context, string, string) error
	SendPost(context.Context, string, string) error
	CreateCard(context.Context, string, string) (string, error)
	PatchCard(context.Context, string, string) error
	SendFile(context.Context, string, string) error
	SendImage(context.Context, string, string) error
	UploadFile(context.Context, string, string) (string, error)
	UploadImage(context.Context, string) (string, error)
	DownloadMessageResource(context.Context, string, string, string) (downloadedResource, error)
}

type socketClient interface {
	Connect(context.Context, string, func(context.Context, []byte) error) error
	Close() error
}

// Factory wires the first Feishu/Lark channel into the runtime.
type Factory struct{}

func (Factory) Name() string { return channelName }

func (Factory) Enabled(cfg *config.Config) bool {
	return cfg != nil && cfg.Feishu.Enabled
}

func (Factory) Build(cfg *config.Config, manager *channels.Manager) (channels.Channel, error) {
	return New(cfg, manager)
}

func (Factory) ConfigSchema() config.SectionSchema {
	return config.SectionSchema{
		ID:          "channels-feishu",
		Label:       "Channel / Feishu",
		Description: "First-party Feishu/Lark runtime configuration.",
		Fields: []config.FieldSchema{
			{Path: "channels.feishu.enabled", Label: "Enabled", Description: "Enable the Feishu/Lark channel.", Type: "bool", LiveApply: true, Env: "FEISHU_ENABLED"},
			{Path: "channels.feishu.app_id", Label: "App ID", Description: "Feishu/Lark app ID.", Type: "string", Secret: true, LiveApply: true, Env: "FEISHU_APP_ID"},
			{Path: "channels.feishu.app_secret", Label: "App Secret", Description: "Feishu/Lark app secret.", Type: "string", Secret: true, LiveApply: true, Env: "FEISHU_APP_SECRET"},
			{Path: "channels.feishu.domain", Label: "Domain", Description: "Use lark or feishu API domain.", Type: "string", LiveApply: true, Env: "FEISHU_DOMAIN", Options: []string{"lark", "feishu"}},
		},
	}
}

// Option overrides internal Feishu channel dependencies for tests.
type Option func(*Channel)

func WithAPIClient(client apiClient) Option {
	return func(ch *Channel) { ch.api = client }
}

func WithSocketClient(client socketClient) Option {
	return func(ch *Channel) { ch.socket = client }
}

// Channel is the first-party Feishu/Lark DM text adapter.
type Channel struct {
	cfg        config.FeishuConfig
	manager    *channels.Manager
	api        apiClient
	socket     socketClient
	dispatcher *larkdispatcher.EventDispatcher

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a Feishu channel using the shared runtime manager.
func New(cfg *config.Config, manager *channels.Manager, opts ...Option) (*Channel, error) {
	if cfg == nil {
		return nil, fmt.Errorf("missing config")
	}
	if manager == nil {
		return nil, fmt.Errorf("missing channel manager")
	}
	if strings.TrimSpace(cfg.Feishu.AppID) == "" || strings.TrimSpace(cfg.Feishu.AppSecret) == "" {
		return nil, fmt.Errorf("missing FEISHU_APP_ID or FEISHU_APP_SECRET")
	}

	ch := &Channel{
		cfg:     cfg.Feishu,
		manager: manager,
		api:     newHTTPClient(cfg.Feishu),
		socket:  newBinarySocket(),
	}
	for _, opt := range opts {
		opt(ch)
	}
	ch.dispatcher = newEventDispatcher(ch)
	return ch, nil
}

func (c *Channel) Name() string { return channelName }

func (c *Channel) Capabilities() channels.ChannelCapabilities {
	return channels.ChannelCapabilities{
		Delivery:     true,
		Media:        true,
		Typing:       false,
		Status:       true,
		SessionModes: []string{channels.SessionModeShared, channels.SessionModePerThread},
	}
}

// Deliver proactively sends one reply plan into a Feishu chat.
func (c *Channel) Deliver(ctx context.Context, target automation.DeliveryTarget, plan channels.ReplyPlan) error {
	chatID := strings.TrimSpace(target.Metadata["chat_id"])
	if chatID == "" {
		chatID = strings.TrimSpace(target.SessionKey)
	}
	if chatID == "" {
		return automation.NewBlockedError("missing feishu chat_id for proactive delivery")
	}
	return (&replySender{api: c.api, chatID: chatID}).SendReplyPlan(ctx, plan)
}

func (c *Channel) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		feishuLog.Infof("start requested while channel is already running")
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	done := make(chan struct{})
	c.done = done
	c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
		Enabled:    boolRef(true),
		Running:    boolRef(true),
		State:      channels.StateStarting,
		Detail:     "launching Feishu websocket loop",
		ClearError: true,
		MarkStart:  true,
	})
	feishuLog.Infof("starting Feishu channel for domain=%s", c.cfg.Domain)

	go func() {
		defer close(done)
		c.run(runCtx)
	}()
	return nil
}

func (c *Channel) Stop(ctx context.Context) error {
	c.mu.Lock()
	cancel := c.cancel
	done := c.done
	c.cancel = nil
	c.done = nil
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if c.socket != nil {
		_ = c.socket.Close()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
		Running:  boolRef(false),
		State:    channels.StateStopped,
		Detail:   "channel stopped",
		MarkStop: true,
	})
	feishuLog.Infof("stopped Feishu channel")
	return nil
}

func (c *Channel) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
			Running:   boolRef(true),
			State:     channels.StateStarting,
			Detail:    "requesting websocket endpoint",
			LastEvent: "fetch_ws_endpoint",
		})
		feishuLog.Debugf("requesting Feishu websocket endpoint")
		endpoint, err := c.api.FetchWSEndpoint(ctx)
		if err != nil {
			c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
				Running:   boolRef(false),
				State:     channels.StateRestarting,
				Detail:    "failed to fetch websocket endpoint",
				LastError: err.Error(),
			})
			feishuLog.Warnf("fetch websocket endpoint failed: %v", err)
			if !sleepContext(ctx, reconnectDelay) {
				return
			}
			continue
		}
		c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
			Running:    boolRef(true),
			State:      channels.StateRunning,
			Detail:     fmt.Sprintf("connected to websocket service_id=%d", parseServiceID(endpoint)),
			LastEvent:  "ws_connected",
			ClearError: true,
		})
		feishuLog.Infof("connecting Feishu websocket service_id=%d", parseServiceID(endpoint))
		err = c.socket.Connect(ctx, endpoint, c.handlePayload)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
				Running:   boolRef(false),
				State:     channels.StateRestarting,
				Detail:    "websocket disconnected",
				LastError: err.Error(),
				LastEvent: "ws_disconnected",
			})
			feishuLog.Warnf("Feishu websocket disconnected: %v", err)
			if !sleepContext(ctx, reconnectDelay) {
				return
			}
			continue
		}
	}
}

func (c *Channel) handlePayload(ctx context.Context, payload []byte) error {
	_, err := c.dispatcher.Do(ctx, payload)
	if err == nil {
		return nil
	}

	var notFound larkdispatcher.NotFoundEventHandlerErr
	if errors.As(err, &notFound) {
		c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
			Running:   boolRef(true),
			State:     channels.StateRunning,
			Detail:    "connected and waiting for message events",
			LastEvent: notFound.Error(),
		})
		feishuLog.Debugf("ignored unsupported Feishu event: %v", err)
		return nil
	}

	var handled eventHandleError
	if errors.As(err, &handled) {
		return handled.Unwrap()
	}

	feishuLog.Debugf("ignored one websocket payload that is not a handled event JSON: %v", err)
	return nil
}

func (c *Channel) handleMessageEvent(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	msg := eventMessage(event)
	if msg == nil {
		feishuLog.Warnf("received Feishu message event without message body")
		return nil
	}

	chatID := strings.TrimSpace(valueOrEmpty(msg.ChatId))
	if chatID == "" {
		feishuLog.Warnf("received Feishu message event without chat_id")
		return nil
	}

	messageType := strings.TrimSpace(valueOrEmpty(msg.MessageType))
	chatType := strings.TrimSpace(valueOrEmpty(msg.ChatType))
	messageID := strings.TrimSpace(valueOrEmpty(msg.MessageId))
	threadID := strings.TrimSpace(valueOrEmpty(msg.ThreadId))
	content := valueOrEmpty(msg.Content)
	senderID := strings.TrimSpace(eventSenderOpenID(event))

	feishuLog.Infof("received Feishu message event chat_id=%s chat_type=%s message_type=%s", chatID, chatType, messageType)
	c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
		Running:     boolRef(true),
		State:       channels.StateRunning,
		Detail:      fmt.Sprintf("handling %s message", messageType),
		LastEvent:   "im.message.receive_v1",
		MarkInbound: true,
		ClearError:  true,
	})

	if chatType != "p2p" && !shouldHandleGroupMessage(msg) {
		feishuLog.Infof("ignoring Feishu group message without @mention chat_id=%s", chatID)
		c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
			Running:   boolRef(true),
			State:     channels.StateRunning,
			Detail:    "ignored group message without mention",
			LastEvent: "group_ignored_no_mention",
		})
		return nil
	}

	inbound := channels.InboundMessage{
		Channel:    channelName,
		SessionKey: chatID,
		Sender:     senderID,
		Source:     message.SourceFeishu,
		Routing: channels.RoutingIdentity{
			ChannelID:   channelName,
			PlatformID:  chatID,
			ThreadID:    threadID,
			SenderID:    senderID,
			SessionMode: channels.SessionModeShared,
		},
		Metadata: map[string]string{
			"message_id": messageID,
			"chat_id":    chatID,
			"sender_id":  senderID,
			"chat_type":  chatType,
			"thread_id":  threadID,
		},
	}

	switch messageType {
	case "text":
		inbound.Text = normalizeIncomingText(content, msg.Mentions)
		if strings.TrimSpace(inbound.Text) == "" {
			if chatType == "p2p" {
				feishuLog.Infof("received empty Feishu text message, sending unsupported notice")
				return c.api.SendText(ctx, chatID, feishuText(textNonTextUnsupported))
			}
			feishuLog.Infof("received group mention without question content chat_id=%s", chatID)
			return c.api.SendText(ctx, chatID, feishuText(textGroupMentionHint))
		}
	case "image", "file", "audio", "media", "video":
		opened, err := c.manager.OpenInboundSession(ctx, inbound)
		if err != nil {
			feishuLog.Errorf("open inbound session for Feishu attachment message failed: %v", err)
			return c.api.SendText(ctx, chatID, feishuText(textOpenSessionFailed, err))
		}
		attachment, err := c.downloadAttachment(ctx, opened.SessionID, messageID, messageType, content)
		if err != nil {
			feishuLog.Errorf("download Feishu attachment failed: %v", err)
			return c.api.SendText(ctx, chatID, feishuText(textDownloadAttachmentFailed, err))
		}
		inbound.Attachments = []message.AttachmentRef{attachment}
	default:
		feishuLog.Infof("received unsupported Feishu message type=%s", messageType)
		return c.api.SendText(ctx, chatID, feishuText(textNonTextUnsupported))
	}

	reply := replySender{
		api:    c.api,
		chatID: chatID,
	}
	if err := c.manager.RouteInbound(ctx, inbound, &reply); err != nil {
		feishuLog.Errorf("route Feishu inbound failed: %v", err)
		return c.api.SendText(ctx, chatID, feishuText(textProcessMessageFailed, err))
	}
	feishuLog.Infof("handled Feishu message successfully chat_id=%s", chatID)
	return nil
}

func (c *Channel) downloadAttachment(ctx context.Context, sessionID, messageID, messageType, rawContent string) (message.AttachmentRef, error) {
	resource, err := parseAttachmentContent(messageType, rawContent)
	if err != nil {
		return message.AttachmentRef{}, err
	}
	downloaded, err := c.api.DownloadMessageResource(ctx, messageID, resource.Key, resource.ResourceType)
	if err != nil {
		return message.AttachmentRef{}, err
	}
	name := resource.Name
	if strings.TrimSpace(name) == "" {
		name = defaultAttachmentName(resource.Key, resource.ResourceType, downloaded.ContentType)
	}
	return c.manager.StoreAttachment(ctx, sessionID, rtbackend.AttachmentUpload{
		Name:     name,
		MIMEType: downloaded.ContentType,
		Reader:   bytes.NewReader(downloaded.Data),
	})
}

type replySender struct {
	mu            sync.Mutex
	api           apiClient
	chatID        string
	pendingCardID string
}

func (r *replySender) SendText(ctx context.Context, text string) error {
	return r.api.SendText(ctx, r.chatID, text)
}

func (r *replySender) SendAck(ctx context.Context) error {
	content, err := renderProcessingCard()
	if err != nil {
		return err
	}
	messageID, err := r.api.CreateCard(ctx, r.chatID, content)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.pendingCardID = messageID
	r.mu.Unlock()
	return nil
}

func (r *replySender) SendReplyPlan(ctx context.Context, plan channels.ReplyPlan) error {
	cardContent, err := renderReplyPlanCard(plan)
	if err != nil {
		return err
	}

	r.mu.Lock()
	pendingCardID := r.pendingCardID
	r.pendingCardID = ""
	r.mu.Unlock()

	if pendingCardID != "" {
		if err := r.api.PatchCard(ctx, pendingCardID, cardContent); err != nil {
			fallbackID, createErr := r.api.CreateCard(ctx, r.chatID, cardContent)
			if createErr != nil {
				return err
			}
			_ = fallbackID
		}
	} else {
		if _, err := r.api.CreateCard(ctx, r.chatID, cardContent); err != nil {
			postPlan := plan
			postPlan.Artifacts = nil
			posts, renderErr := renderReplyPlanPosts(postPlan)
			if renderErr != nil {
				return err
			}
			for _, post := range posts {
				if sendErr := r.api.SendPost(ctx, r.chatID, post); sendErr != nil {
					return err
				}
			}
		}
	}

	failures := make([]string, 0, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		if err := r.sendArtifact(ctx, artifact); err != nil {
			label := artifactLabel(artifact)
			if label == "" {
				label = "artifact"
			}
			failures = append(failures, fmt.Sprintf("%s: %v", label, err))
		}
	}
	if len(failures) > 0 {
		notice := feishuText(textArtifactUploadFailed, strings.Join(failures, "\n- "))
		for _, chunk := range splitReplyChunks(notice, maxReplyChunkRunes) {
			if err := r.api.SendText(ctx, r.chatID, chunk); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *replySender) sendArtifact(ctx context.Context, artifact channels.ReplyArtifact) error {
	path := strings.TrimSpace(artifact.Path)
	if path == "" {
		return fmt.Errorf("missing artifact path")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	kind, err := detectArtifactKind(absolutePath)
	if err != nil {
		return err
	}
	name := artifactLabel(artifact)
	switch kind {
	case "image":
		imageKey, err := r.api.UploadImage(ctx, absolutePath)
		if err != nil {
			return err
		}
		return r.api.SendImage(ctx, r.chatID, imageKey)
	default:
		fileKey, err := r.api.UploadFile(ctx, absolutePath, name)
		if err != nil {
			return err
		}
		return r.api.SendFile(ctx, r.chatID, fileKey)
	}
}

func artifactLabel(artifact channels.ReplyArtifact) string {
	label := strings.TrimSpace(artifact.Name)
	if label != "" {
		return label
	}
	label = filepath.Base(strings.TrimSpace(artifact.Path))
	if label != "" && label != "." {
		return label
	}
	return feishuText(textArtifactFallbackName)
}

func detectArtifactKind(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("artifact path is a directory")
	}
	if mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); strings.HasPrefix(mimeType, "image/") {
		return "image", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var sniff [512]byte
	n, err := file.Read(sniff[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if strings.HasPrefix(http.DetectContentType(sniff[:n]), "image/") {
		return "image", nil
	}
	return "file", nil
}

type eventHandleError struct {
	err error
}

func (e eventHandleError) Error() string {
	return e.err.Error()
}

func (e eventHandleError) Unwrap() error {
	return e.err
}

func newEventDispatcher(ch *Channel) *larkdispatcher.EventDispatcher {
	dispatcher := larkdispatcher.NewEventDispatcher("", "")
	dispatcher.Config.Logger = sdkLogger{base: logger.New("feishu-event")}
	dispatcher.OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
		if err := ch.handleMessageEvent(ctx, event); err != nil {
			return eventHandleError{err: err}
		}
		return nil
	})
	return dispatcher
}

func eventMessage(event *larkim.P2MessageReceiveV1) *larkim.EventMessage {
	if event == nil || event.Event == nil {
		return nil
	}
	return event.Event.Message
}

func eventSenderOpenID(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil || event.Event.Sender == nil || event.Event.Sender.SenderId == nil {
		return ""
	}
	return valueOrEmpty(event.Event.Sender.SenderId.OpenId)
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func boolRef(v bool) *bool {
	return &v
}

func parseTextContent(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err == nil {
		return strings.TrimSpace(payload.Text)
	}
	return strings.TrimSpace(raw)
}

func normalizeIncomingText(raw string, mentions []*larkim.MentionEvent) string {
	text := parseTextContent(raw)
	if len(mentions) == 0 {
		return text
	}
	for _, mention := range mentions {
		if mention == nil {
			continue
		}
		name := strings.TrimSpace(valueOrEmpty(mention.Name))
		if name != "" {
			text = strings.ReplaceAll(text, "@"+name, " ")
		}
		key := strings.TrimSpace(valueOrEmpty(mention.Key))
		if key != "" {
			text = strings.ReplaceAll(text, key, " ")
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

func shouldHandleGroupMessage(msg *larkim.EventMessage) bool {
	if msg == nil {
		return false
	}
	return len(msg.Mentions) > 0
}

type inboundAttachment struct {
	Key          string
	Name         string
	ResourceType string
}

func parseAttachmentContent(messageType, raw string) (inboundAttachment, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return inboundAttachment{}, fmt.Errorf("decode %s content: %w", messageType, err)
	}

	switch messageType {
	case "image":
		key, _ := payload["image_key"].(string)
		key = strings.TrimSpace(key)
		if key == "" {
			return inboundAttachment{}, fmt.Errorf("missing image_key")
		}
		return inboundAttachment{
			Key:          key,
			Name:         strings.TrimSpace(stringValue(payload["file_name"])),
			ResourceType: "image",
		}, nil
	case "file", "audio", "media", "video":
		key, _ := payload["file_key"].(string)
		key = strings.TrimSpace(key)
		if key == "" {
			return inboundAttachment{}, fmt.Errorf("missing file_key")
		}
		return inboundAttachment{
			Key:          key,
			Name:         strings.TrimSpace(stringValue(payload["file_name"])),
			ResourceType: "file",
		}, nil
	default:
		return inboundAttachment{}, fmt.Errorf("unsupported attachment message type %q", messageType)
	}
}

func defaultAttachmentName(key, resourceType, contentType string) string {
	base := resourceType
	if base == "" {
		base = "attachment"
	}
	if strings.TrimSpace(contentType) != "" {
		if extensions, err := mime.ExtensionsByType(strings.TrimSpace(contentType)); err == nil && len(extensions) > 0 {
			return base + "-" + shortKey(key) + extensions[0]
		}
	}
	return base + "-" + shortKey(key)
}

func shortKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 12 {
		return key
	}
	return key[:12]
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func sleepContext(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func splitReplyChunks(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if limit <= 0 {
		return []string{text}
	}

	paragraphs := strings.Split(text, "\n\n")
	chunks := make([]string, 0, len(paragraphs))
	var current strings.Builder

	flush := func() {
		if strings.TrimSpace(current.String()) == "" {
			current.Reset()
			return
		}
		chunks = append(chunks, strings.TrimSpace(current.String()))
		current.Reset()
	}

	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		candidate := paragraph
		if current.Len() > 0 {
			candidate = current.String() + "\n\n" + paragraph
		}
		if len([]rune(candidate)) <= limit {
			if current.Len() > 0 {
				current.WriteString("\n\n")
			}
			current.WriteString(paragraph)
			continue
		}
		flush()
		runes := []rune(paragraph)
		for len(runes) > limit {
			chunks = append(chunks, strings.TrimSpace(string(runes[:limit])))
			runes = runes[limit:]
		}
		if len(runes) > 0 {
			current.WriteString(string(runes))
		}
	}

	flush()
	if len(chunks) == 0 {
		return []string{text}
	}
	return chunks
}
