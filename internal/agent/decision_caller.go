package agent

import (
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/decision"
)

// buildDecisionCaller constructs the decision-model caller used by decision
// nodes. Disabled config or a missing chat client yields nil; decision nodes
// then fail_closed (route to the llm fallback) instead of auto-executing.
// F0 drives the already-configured default provider through the strict JSON
// adapter (same pattern as the screener); native Jev/Laya structured
// endpoints and per-node provider selection plug in behind decision.Caller.
func buildDecisionCaller(cfg *config.Config, client conversation.Caller) decision.Caller {
	if cfg == nil || !cfg.Decision.Enabled || client == nil {
		return nil
	}
	provider := cfg.Decision.Provider
	if provider == "" {
		provider = "llm"
	}
	timeout := time.Duration(cfg.Decision.TimeoutMS) * time.Millisecond
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
