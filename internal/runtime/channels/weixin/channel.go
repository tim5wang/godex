package weixin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/runtime/channels"
)

var weixinLog = logger.New("weixin")

// Factory wires the first-party Weixin/iLink channel into the runtime.
type Factory struct{}

func (Factory) Name() string { return channelName }

func (Factory) Enabled(cfg *config.Config) bool {
	return cfg != nil && cfg.Weixin.Enabled
}

func (Factory) Build(cfg *config.Config, manager *channels.Manager) (channels.Channel, error) {
	return New(cfg, manager)
}

func (Factory) ConfigSchema() config.SectionSchema {
	return config.SectionSchema{
		ID:          "channels-weixin",
		Label:       "Channel / Weixin",
		Description: "First-party Weixin/iLink runtime configuration.",
		Fields: []config.FieldSchema{
			{Path: "channels.weixin.enabled", Label: "Enabled", Description: "Enable the Weixin/iLink channel.", Type: "bool", LiveApply: true, Env: "WEIXIN_ENABLED"},
			{Path: "channels.weixin.base_url", Label: "Base URL", Description: "Weixin iLink HTTP API base URL.", Type: "string", LiveApply: true, Env: "WEIXIN_BASE_URL"},
			{Path: "channels.weixin.cdn_base_url", Label: "CDN Base URL", Description: "Weixin CDN base URL used for media upload and download.", Type: "string", LiveApply: true, Env: "WEIXIN_CDN_BASE_URL"},
			{Path: "channels.weixin.account_id", Label: "Account ID", Description: "Logical local account bucket used for persisted iLink credentials.", Type: "string", LiveApply: true, Env: "WEIXIN_ACCOUNT_ID"},
			{Path: "channels.weixin.allow_from", Label: "Allow From", Description: "Optional allowlist of iLink user IDs. Empty or * allows all senders.", Type: "string_list", LiveApply: true, Env: "WEIXIN_ALLOW_FROM"},
			{Path: "channels.weixin.route_tag", Label: "Route Tag", Description: "Optional metadata tag attached to inbound Weixin sessions.", Type: "string", LiveApply: true, Env: "WEIXIN_ROUTE_TAG"},
			{Path: "channels.weixin.long_poll_timeout_ms", Label: "Long Poll Timeout Ms", Description: "Requested long-poll timeout in milliseconds.", Type: "int", LiveApply: true, Env: "WEIXIN_LONG_POLL_TIMEOUT_MS"},
			{Path: "channels.weixin.proxy", Label: "Proxy", Description: "Optional HTTP(S) proxy used for iLink requests.", Type: "string", LiveApply: true, Env: "WEIXIN_PROXY"},
		},
	}
}

// Option overrides internal dependencies for tests.
type Option func(*Channel)

func WithTransport(t transport) Option {
	return func(ch *Channel) {
		ch.transport = t
	}
}

// Channel is the first-party Weixin/iLink DM text adapter.
type Channel struct {
	cfg       config.WeixinConfig
	stateDir  string
	manager   *channels.Manager
	store     *stateStore
	transport transport

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}

	inboundSem chan struct{}
	inboundWG  sync.WaitGroup
}

func New(cfg *config.Config, manager *channels.Manager, opts ...Option) (*Channel, error) {
	if cfg == nil {
		return nil, fmt.Errorf("missing config")
	}
	if manager == nil {
		return nil, fmt.Errorf("missing channel manager")
	}
	weixinCfg := cfg.Weixin
	weixinCfg.AccountID = normalizeAccountID(weixinCfg.AccountID)
	if strings.TrimSpace(weixinCfg.BaseURL) == "" {
		weixinCfg.BaseURL = defaultBaseURL
	}
	if weixinCfg.LongPollTimeoutMs <= 0 {
		weixinCfg.LongPollTimeoutMs = defaultPollTimeoutMs
	}
	ch := &Channel{
		cfg:        weixinCfg,
		stateDir:   cfg.StateDir,
		manager:    manager,
		store:      newStateStore(cfg.StateDir, weixinCfg.AccountID),
		inboundSem: make(chan struct{}, maxInboundHandlers),
	}
	for _, opt := range opts {
		opt(ch)
	}
	return ch, nil
}

func (c *Channel) Name() string { return channelName }

func (c *Channel) Capabilities() channels.ChannelCapabilities {
	return channels.ChannelCapabilities{
		Delivery:     true,
		AuthLogin:    true,
		Media:        true,
		Status:       true,
		AllowFrom:    true,
		SessionModes: []string{channels.SessionModeShared},
	}
}

// Deliver proactively sends one reply plan into a Weixin chat when enough reply context exists.
func (c *Channel) Deliver(ctx context.Context, target automation.DeliveryTarget, plan channels.ReplyPlan) error {
	transport := c.currentTransport()
	if transport == nil {
		account, nextTransport, err := c.ensureAccountTransport()
		if err != nil {
			return automation.NewBlockedError("weixin delivery is not authenticated")
		}
		_ = account
		transport = nextTransport
	}

	toUserID := strings.TrimSpace(target.Recipient)
	if toUserID == "" {
		toUserID = strings.TrimSpace(target.Metadata["from_user_id"])
	}
	if toUserID == "" {
		return automation.NewBlockedError("missing weixin recipient for proactive delivery")
	}

	token := strings.TrimSpace(target.Metadata["context_token"])
	if token == "" && c.store != nil {
		cached, _ := c.store.LookupContextToken(toUserID)
		token = strings.TrimSpace(cached)
	}
	if token == "" {
		return automation.NewBlockedError("missing weixin context token for proactive delivery")
	}

	sender := &replySender{
		transport:    transport,
		store:        c.store,
		cdnBaseURL:   strings.TrimSpace(firstNonEmpty(target.Metadata["cdn_base_url"], c.cfg.CDNBaseURL)),
		toUserID:     toUserID,
		contextToken: token,
	}
	return sender.SendReplyPlan(ctx, plan)
}

func (c *Channel) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	done := make(chan struct{})
	c.done = done
	go c.run(runCtx, done)
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
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := c.waitInboundHandlers(ctx); err != nil {
		return err
	}
	c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
		Running:    boolRef(false),
		State:      channels.StateStopped,
		Detail:     "channel stopped",
		LastEvent:  "stopped",
		ClearError: true,
		MarkStop:   true,
	})
	return nil
}

func (c *Channel) enterReauth(detail string, err error) {
	update := channels.ChannelStatusUpdate{
		Enabled:    boolRef(true),
		Running:    boolRef(false),
		State:      channels.StateError,
		Detail:     detail + " for account=" + normalizeAccountID(c.cfg.AccountID),
		ClearError: err == nil,
		LastEvent:  "reauth_required",
	}
	if err != nil {
		update.LastError = err.Error()
	}
	c.manager.SetStatus(channelName, update)
}

func (c *Channel) currentTransport() transport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transport
}

func (c *Channel) setTransport(t transport) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transport = t
}

type replySender struct {
	transport    transport
	store        *stateStore
	cdnBaseURL   string
	toUserID     string
	contextToken string
	typingTicket string
}

func (r *replySender) SendText(ctx context.Context, text string) error {
	return r.SendReplyPlan(ctx, channels.ReplyPlan{Text: text, Status: "completed"})
}

func (r *replySender) SendAck(ctx context.Context) error {
	if strings.TrimSpace(r.toUserID) == "" {
		return fmt.Errorf("missing weixin target user")
	}
	cfg, err := r.transport.GetConfig(ctx, r.toUserID, r.resolveContextToken())
	if err != nil {
		return err
	}
	r.typingTicket = strings.TrimSpace(cfg.TypingTicket)
	if r.typingTicket == "" {
		return fmt.Errorf("weixin typing ticket is empty")
	}
	return r.transport.SendTyping(ctx, r.toUserID, r.typingTicket, 1)
}

func (r *replySender) SendReplyPlan(ctx context.Context, plan channels.ReplyPlan) error {
	contextToken := r.resolveContextToken()
	if strings.TrimSpace(contextToken) == "" {
		return fmt.Errorf("missing weixin context token")
	}
	textPlan := plan
	if len(textPlan.Artifacts) > 0 {
		textPlan.Artifacts = nil
	}
	text := strings.TrimSpace(textPlan.RenderText())
	if text == "" && len(plan.Artifacts) == 0 {
		text = "Completed."
	}
	if text != "" {
		for _, chunk := range splitReplyChunks(text, maxReplyChunkRunes) {
			if err := r.transport.SendMessage(ctx, weixinMessage{
				ClientID:     newClientID(),
				ToUserID:     r.toUserID,
				MessageType:  weixinMessageTypeBot,
				MessageState: weixinMessageStateFinish,
				ContextToken: contextToken,
				ItemList: []messageItem{{
					Type:     weixinItemTypeText,
					TextItem: &textItem{Text: chunk},
				}},
			}); err != nil {
				_ = r.stopTyping(ctx)
				return err
			}
		}
	}
	failures := make([]string, 0, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		if err := r.sendArtifact(ctx, artifact); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		for _, chunk := range splitReplyChunks("部分附件发送失败：\n- "+strings.Join(failures, "\n- "), maxReplyChunkRunes) {
			if err := r.transport.SendMessage(ctx, weixinMessage{
				ClientID:     newClientID(),
				ToUserID:     r.toUserID,
				MessageType:  weixinMessageTypeBot,
				MessageState: weixinMessageStateFinish,
				ContextToken: contextToken,
				ItemList: []messageItem{{
					Type:     weixinItemTypeText,
					TextItem: &textItem{Text: chunk},
				}},
			}); err != nil {
				_ = r.stopTyping(ctx)
				return err
			}
		}
	}
	return r.stopTyping(ctx)
}

func (r *replySender) resolveContextToken() string {
	if token := strings.TrimSpace(r.contextToken); token != "" {
		return token
	}
	if r.store == nil {
		return ""
	}
	token, _ := r.store.LookupContextToken(r.toUserID)
	return token
}

func (r *replySender) stopTyping(ctx context.Context) error {
	if strings.TrimSpace(r.toUserID) == "" {
		return nil
	}
	ticket := strings.TrimSpace(r.typingTicket)
	if ticket == "" {
		cfg, err := r.transport.GetConfig(ctx, r.toUserID, r.resolveContextToken())
		if err != nil {
			return err
		}
		ticket = strings.TrimSpace(cfg.TypingTicket)
	}
	if ticket == "" {
		return nil
	}
	return r.transport.SendTyping(ctx, r.toUserID, ticket, 2)
}

func splitReplyChunks(text string, maxRunes int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxRunes <= 0 {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return []string{text}
	}
	chunks := make([]string, 0, len(runes)/maxRunes+1)
	start := 0
	for start < len(runes) {
		end := start + maxRunes
		if end >= len(runes) {
			chunks = append(chunks, strings.TrimSpace(string(runes[start:])))
			break
		}
		split := findChunkBoundary(runes, start, end)
		chunks = append(chunks, strings.TrimSpace(string(runes[start:split])))
		start = split
	}
	out := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) != "" {
			out = append(out, chunk)
		}
	}
	return out
}

func findChunkBoundary(runes []rune, start, end int) int {
	limit := end - start
	for i := end; i > start+limit/3; i-- {
		switch runes[i] {
		case '\n', '。', '！', '？', '.', '!', '?', '，', ',', ';', '；', ' ':
			return i + 1
		}
	}
	return end
}

func senderAllowed(allow []string, sender string) bool {
	sender = strings.TrimSpace(sender)
	if sender == "" {
		return false
	}
	if len(allow) == 0 {
		return true
	}
	for _, item := range allow {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if item == "*" || item == sender {
			return true
		}
	}
	return false
}

func boolRef(v bool) *bool {
	return &v
}

func inboundMessageID(msg weixinMessage) string {
	if msg.MessageID != 0 {
		return fmt.Sprintf("%d", msg.MessageID)
	}
	if msg.Seq != 0 {
		return fmt.Sprintf("seq-%d", msg.Seq)
	}
	return ""
}

func newClientID() string {
	return fmt.Sprintf("godex-%d", time.Now().UnixNano())
}

func isSessionExpired(err error) bool {
	return errors.Is(err, errSessionExpired)
}
