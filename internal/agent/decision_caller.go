package agent

import (
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/decision"
	"github.com/tim5wang/godex/internal/core/llm"
)

// buildDecisionCaller constructs the decision-model caller used by decision
// nodes. Disabled config or a missing chat client yields nil; decision nodes
// then fail_closed (route to the llm fallback) instead of auto-executing.
// F0 drives the already-configured default provider through the strict JSON
// adapter (same pattern as the screener); native Jev/Laya structured
// endpoints and per-node provider selection plug in behind decision.Caller.
func buildDecisionCaller(cfg *config.Config, client conversation.Caller) decision.Caller {
	if cfg == nil || !cfg.Decision.Enabled {
		return nil
	}
	provider := strings.TrimSpace(cfg.Decision.Provider)
	if provider == "" {
		provider = "llm"
	}
	timeout := time.Duration(cfg.Decision.TimeoutMS) * time.Millisecond

	// Native Jev/Laya structured endpoint: resolve the decision provider in
	// the shared providers registry; when it is a laya_jev type, drive the
	// laya /predict endpoint directly (no chat round-trip).
	if p, ok := cfg.LLMProviders[provider]; ok {
		if llm.NormalizeProviderType(p.Type) == llm.ProviderLaya {
			return decision.NewJevCaller(decision.JevCallerOptions{
				BaseURL: p.BaseURL,
				Timeout: timeout,
			})
		}
	}

	// F0: drive the already-configured default provider through the strict
	// JSON adapter (same pattern as the screener).
	if client == nil {
		return nil
	}
	return decision.NewLLMCaller(decision.LLMCallerOptions{
		Provider:  provider,
		Caller:    client,
		Timeout:   timeout,
		MaxTokens: cfg.Decision.MaxTokens,
	})
}

// SetDecisionCaller injects the decision caller (tests and config rebinds).
func (a *Agent) SetDecisionCaller(c decision.Caller) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.decisionCaller = c
	a.mu.Unlock()
}

func (a *Agent) activeDecisionCaller() decision.Caller {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.decisionCaller
}
