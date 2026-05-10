package weixin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/runtime/channels"
)

func (c *Channel) run(ctx context.Context, done chan struct{}) {
	defer func() {
		c.setTransport(nil)
		c.mu.Lock()
		c.cancel = nil
		c.done = nil
		c.mu.Unlock()
		if done != nil {
			close(done)
		}
	}()

	accountID := normalizeAccountID(c.cfg.AccountID)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		account, transport, err := c.ensureAccountTransport()
		if err != nil {
			c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
				Enabled:    boolRef(true),
				Running:    boolRef(false),
				State:      channels.StateError,
				Detail:     "failed to initialize Weixin channel",
				LastError:  err.Error(),
				LastEvent:  "transport_error",
				ClearError: false,
			})
			weixinLog.Warnf("initialize Weixin transport failed account=%s: %v", accountID, err)
			if !sleepContext(ctx, pollRetryDelay) {
				return
			}
			continue
		}
		if account == nil || transport == nil {
			c.enterReauth("weixin login state missing", nil)
			if !sleepContext(ctx, reauthRetryDelay) {
				return
			}
			continue
		}
		if err := c.pollAccount(ctx, accountID, account, transport); err != nil {
			if isSessionExpired(err) {
				_ = c.store.ClearAccount()
				_ = c.store.ClearCursor()
				_ = c.store.ClearContextTokens()
				c.setTransport(nil)
				c.enterReauth("weixin session expired", nil)
				weixinLog.Warnf("weixin session expired for account=%s", accountID)
				if !sleepContext(ctx, reauthRetryDelay) {
					return
				}
				continue
			}
			c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
				Enabled:    boolRef(true),
				Running:    boolRef(true),
				State:      channels.StateError,
				Detail:     "poll failed, retrying for account=" + accountID,
				LastError:  err.Error(),
				LastEvent:  "poll_error",
				ClearError: false,
			})
			weixinLog.Warnf("weixin poll failed account=%s: %v", accountID, err)
			if !sleepContext(ctx, pollRetryDelay) {
				return
			}
		}
	}
}

func (c *Channel) ensureAccountTransport() (*accountState, transport, error) {
	account, err := c.store.LoadAccount()
	if err != nil {
		return nil, nil, err
	}
	if account == nil || strings.TrimSpace(account.BotToken) == "" {
		return nil, nil, nil
	}
	current := c.currentTransport()
	if current != nil {
		return account, current, nil
	}
	transport, err := newTransport(configFromAccount(c.cfg, account), account.BotToken)
	if err != nil {
		return nil, nil, err
	}
	c.setTransport(transport)
	return account, transport, nil
}

func configFromAccount(base config.WeixinConfig, account *accountState) config.WeixinConfig {
	base.BaseURL = defaultIfEmpty(account.BaseURL, base.BaseURL)
	base.CDNBaseURL = defaultIfEmpty(account.CDNBaseURL, base.CDNBaseURL)
	return base
}

func (c *Channel) pollAccount(ctx context.Context, accountID string, account *accountState, transport transport) error {
	c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
		Enabled:    boolRef(true),
		Running:    boolRef(true),
		State:      channels.StateRunning,
		Detail:     "starting long-poll for account=" + accountID,
		ClearError: true,
		MarkStart:  true,
		LastEvent:  "poll_start",
	})
	weixinLog.Infof("starting Weixin poll loop account=%s base_url=%s", accountID, defaultIfEmpty(account.BaseURL, c.cfg.BaseURL))

	cursor, err := c.store.LoadCursor()
	if err != nil {
		c.enterReauth("failed to load Weixin cursor", err)
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		resp, err := transport.GetUpdates(ctx, cursor, c.cfg.LongPollTimeoutMs)
		if err != nil {
			return err
		}

		if next := strings.TrimSpace(resp.GetUpdatesBuf); next != "" && next != cursor {
			cursor = next
			if err := c.store.SaveCursor(cursor); err != nil {
				c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
					Enabled:   boolRef(true),
					Running:   boolRef(true),
					State:     channels.StateError,
					Detail:    "failed to persist Weixin cursor",
					LastError: err.Error(),
					LastEvent: "cursor_save_error",
				})
				weixinLog.Warnf("save Weixin cursor failed account=%s: %v", accountID, err)
			}
		}

		c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
			Enabled:    boolRef(true),
			Running:    boolRef(true),
			State:      channels.StateRunning,
			Detail:     fmt.Sprintf("polling account=%s", accountID),
			LastEvent:  "poll_ok",
			ClearError: true,
			MarkPoll:   true,
		})

		for _, msg := range resp.Msgs {
			if err := c.handleInboundMessage(ctx, msg); err != nil {
				weixinLog.Warnf("handle Weixin inbound failed account=%s: %v", accountID, err)
				c.manager.SetStatus(channelName, channels.ChannelStatusUpdate{
					Enabled:   boolRef(true),
					Running:   boolRef(true),
					State:     channels.StateError,
					Detail:    "handle inbound failed",
					LastError: err.Error(),
					LastEvent: "inbound_error",
				})
			}
		}
	}
}

func (c *Channel) handleInboundMessage(ctx context.Context, msg weixinMessage) error {
	if msg.MessageType != 0 && msg.MessageType != weixinMessageTypeUser {
		return nil
	}
	fromUserID := strings.TrimSpace(msg.FromUserID)
	_ = c.store.SaveContextToken(fromUserID, msg.ContextToken)

	reply := &replySender{
		transport:    c.currentTransport(),
		store:        c.store,
		cdnBaseURL:   c.cfg.CDNBaseURL,
		toUserID:     fromUserID,
		contextToken: strings.TrimSpace(msg.ContextToken),
	}

	sessionID := ""
	if containsMediaItems(msg.ItemList) {
		opened, err := c.manager.OpenInboundSession(ctx, channels.InboundMessage{
			Channel:    channelName,
			SessionKey: normalizeAccountID(c.cfg.AccountID) + ":" + fromUserID,
			Sender:     fromUserID,
			Source:     message.SourceWeixin,
			Routing: channels.RoutingIdentity{
				ChannelID:   channelName,
				PlatformID:  normalizeAccountID(c.cfg.AccountID) + ":" + fromUserID,
				SenderID:    fromUserID,
				SessionMode: channels.SessionModeShared,
			},
			Metadata: map[string]string{
				"context_token": strings.TrimSpace(msg.ContextToken),
				"message_id":    inboundMessageID(msg),
				"from_user_id":  fromUserID,
				"to_user_id":    strings.TrimSpace(msg.ToUserID),
				"account_id":    normalizeAccountID(c.cfg.AccountID),
				"message_type":  fmt.Sprintf("%d", msg.MessageType),
			},
		})
		if err != nil {
			weixinLog.Errorf("open Weixin inbound session for media message failed: %v", err)
			return reply.SendText(ctx, "打开会话失败："+err.Error())
		}
		sessionID = opened.SessionID
	}
	content, err := c.collectInboundContent(ctx, sessionID, msg)
	if err != nil {
		weixinLog.Errorf("collect Weixin inbound content failed: %v", err)
		return reply.SendText(ctx, "处理消息附件失败："+err.Error())
	}
	if strings.TrimSpace(content.text) == "" && len(content.attachments) == 0 {
		weixinLog.Infof("received unsupported Weixin message from %s", fromUserID)
		return reply.SendText(ctx, "当前版本仅支持文本、图片、语音、视频和文件消息。")
	}

	inbound := channels.InboundMessage{
		Channel:     channelName,
		SessionKey:  normalizeAccountID(c.cfg.AccountID) + ":" + fromUserID,
		Sender:      fromUserID,
		Text:        content.text,
		Attachments: append([]message.AttachmentRef{}, content.attachments...),
		Source:      message.SourceWeixin,
		Routing: channels.RoutingIdentity{
			ChannelID:   channelName,
			PlatformID:  normalizeAccountID(c.cfg.AccountID) + ":" + fromUserID,
			SenderID:    fromUserID,
			SessionMode: channels.SessionModeShared,
		},
		Metadata: map[string]string{
			"context_token": strings.TrimSpace(msg.ContextToken),
			"message_id":    inboundMessageID(msg),
			"from_user_id":  fromUserID,
			"to_user_id":    strings.TrimSpace(msg.ToUserID),
			"account_id":    normalizeAccountID(c.cfg.AccountID),
			"message_type":  fmt.Sprintf("%d", msg.MessageType),
		},
	}
	if tag := strings.TrimSpace(c.cfg.RouteTag); tag != "" {
		inbound.Metadata["route_tag"] = tag
	}

	weixinLog.Infof("routing Weixin inbound user=%s message_id=%s text=%t attachments=%d", fromUserID, inbound.Metadata["message_id"], strings.TrimSpace(content.text) != "", len(content.attachments))
	return c.manager.RouteInbound(ctx, inbound, reply)
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func containsMediaItems(items []messageItem) bool {
	for _, item := range items {
		switch item.Type {
		case weixinItemTypeImage, weixinItemTypeVoice, weixinItemTypeFile, weixinItemTypeVideo:
			return true
		}
	}
	return false
}
