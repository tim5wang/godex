package backend

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/platform/textutil"
)

var workResourceURL = regexp.MustCompile(`https?://[^\s<>\x60"'\x{3000}-\x{303f}\x{ff00}-\x{ffef}]+`)

// ponytail: literal HTTP(S) mentions only; Agents maintain semantic references
// and directories with resource_put instead of guessing with a bigger parser.
// Only literal URLs from an actual user envelope are captured automatically.
// No crawling, directory guessing, or parsing tool/runtime outputs as user intent.
func (s *Service) captureWorkResources(session *sessionState, envelope message.Envelope, turnID string) {
	switch envelope.Source {
	case message.SourceCLI, message.SourceTUI, message.SourceWeb, message.SourceVoice, message.SourceACP, message.SourceGateway, message.SourceFeishu, message.SourceWeixin:
	default:
		return
	}
	session.mu.RLock()
	scope, id := session.workContextScope, session.workContextID
	session.mu.RUnlock()
	if id == "" {
		return
	}
	store, err := s.WorkContexts()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, raw := range workResourceURL.FindAllString(envelope.BodyText(), 8) {
		locator := strings.TrimRight(raw, ".,;:!?)]}")
		u, err := url.Parse(locator)
		if err != nil || u.Hostname() == "" || u.User != nil {
			continue
		}
		_, err = store.AddResource(ctx, scope, id, workcontext.ResourceInput{
			Title: textutil.TruncateRunes(u.Host+u.EscapedPath(), 160), Kind: "web", Locator: locator,
			Purpose: "用户在对话中提供的参考链接（尚未核验内容）", Source: "session://" + session.id + "/turns/" + turnID,
		})
		if err != nil {
			// Unsafe/credential-bearing URLs are rejected by the shared locator gate.
			// Never log their contents or interrupt the user's turn for an invalid URL.
			logger.Debugf("work desktop resource capture skipped for session %s", session.id)
		}
	}
}
