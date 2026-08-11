package agent

import (
	"context"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/security"
)

// buildScreener constructs the content security screener from config
// (roadmap 6.1). Disabled config or a missing client yields a no-op screener
// that never blocks; enabled config yields an LLM-backed classifier.
func buildScreener(cfg *config.Config, client conversation.Caller) security.Screener {
	if cfg == nil || !cfg.Security.Screener.Enabled || client == nil {
		return security.NoopScreener{}
	}
	return security.NewLLMScreener(security.LLMScreenerOptions{
		Provider:  cfg.Security.Screener.Provider,
		Shadow:    cfg.Security.Screener.Shadow,
		Timeout:   time.Duration(cfg.Security.Screener.TimeoutMS) * time.Millisecond,
		MaxTokens: cfg.Security.Screener.MaxTokens,
		Caller:    client,
	})
}

// screenAuditFn receives every screener verdict for audit recording. It is
// wired by the backend to appendSecurityEvent; nil discards the verdict.
type screenAuditFn func(security.ScreenHook, security.ScreenVerdict)

func (a *Agent) activeScreener() security.Screener {
	if a == nil {
		return security.NoopScreener{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.screener == nil {
		return security.NoopScreener{}
	}
	return a.screener
}

// SetScreener injects a screener (used by tests and by the backend when it
// needs to rebind the classifier after model profile changes).
func (a *Agent) SetScreener(s security.Screener) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.screener = s
	a.mu.Unlock()
}

// SetScreenAudit wires an audit callback that receives every screener
// verdict (roadmap 6.1). The backend uses it to appendSecurityEvent.
func (a *Agent) SetScreenAudit(fn screenAuditFn) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.screenAudit = fn
	a.mu.Unlock()
}

// ScreenUserInput screens an inbound user message before it reaches the
// model (roadmap 6.1 hook: user_input). In shadow mode the classification
// runs fire-and-forget for audit and the call returns immediately with an
// auto verdict so the pipeline is never gated or delayed; in authoritative
// mode (future) it returns the real verdict synchronously.
func (a *Agent) ScreenUserInput(ctx context.Context, text string, metadata map[string]string) security.ScreenVerdict {
	s := a.activeScreener()
	if s.Shadow() {
		go func() {
			child, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			verdict := s.Classify(child, text, security.ScreenHookUserInput, metadata)
			a.emitScreenAudit(security.ScreenHookUserInput, verdict)
		}()
		return security.ScreenVerdict{Decision: security.ScreenDecisionAuto}
	}
	verdict := s.Classify(ctx, text, security.ScreenHookUserInput, metadata)
	a.emitScreenAudit(security.ScreenHookUserInput, verdict)
	return verdict
}

// screenToolResult screens a tool result flowing back to the model (roadmap
// 6.1 hook: tool_response). Called from the tool-result filter. Shadow mode
// runs fire-and-forget for audit without gating or delaying the pipeline.
func (a *Agent) screenToolResult(ctx context.Context, tool conversation.ExecutedTool) security.ScreenVerdict {
	s := a.activeScreener()
	if s.Shadow() {
		go func() {
			child, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			verdict := s.Classify(child, tool.Output, security.ScreenHookToolResponse, map[string]string{
				"tool": tool.Name,
			})
			a.emitScreenAudit(security.ScreenHookToolResponse, verdict)
		}()
		return security.ScreenVerdict{Decision: security.ScreenDecisionAuto}
	}
	verdict := s.Classify(ctx, tool.Output, security.ScreenHookToolResponse, map[string]string{
		"tool": tool.Name,
	})
	a.emitScreenAudit(security.ScreenHookToolResponse, verdict)
	return verdict
}

func (a *Agent) emitScreenAudit(hook security.ScreenHook, verdict security.ScreenVerdict) {
	if a == nil {
		return
	}
	a.mu.Lock()
	fn := a.screenAudit
	a.mu.Unlock()
	if fn != nil {
		fn(hook, verdict)
	}
}
