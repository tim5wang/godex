package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
)

// OrganizeMemory uses the configured caller without entering any user session
// or exposing tools. Scope, evidence validation and scheduling remain host-owned.
func (s *SharedDependencies) OrganizeMemory(ctx context.Context, cfg *config.Config, prompt, input string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("memory organizer unavailable")
	}
	if cfg == nil || strings.TrimSpace(cfg.Model) == "" || cfg.MaxTokens <= 0 {
		return "", fmt.Errorf("memory organizer model unavailable")
	}
	caller := s.snapshot().client
	if caller == nil {
		return "", fmt.Errorf("memory organizer model unavailable")
	}
	// Reasoning providers share the output budget between thinking and JSON.
	// A diverse batch can exhaust 4096 tokens before JSON is complete. Keep
	// Dream bounded while respecting the configured user limit and effort.
	budget := min(cfg.MaxTokens, 8192)
	request := conversation.NewRequest(cfg.Model, budget, cfg.ReasoningEffort, "", []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, prompt+"\n\n"+input)}, nil)
	response, err := caller.Call(ctx, request)
	if err != nil {
		return "", err
	}
	if response == nil {
		return "", fmt.Errorf("memory organizer returned no response")
	}
	if response.StopReason == "length" || response.StopReason == "max_tokens" {
		return "", fmt.Errorf("memory organizer exhausted output budget (%d tokens); no result was applied", budget)
	}
	output := strings.TrimSpace(protocol.BlocksText(response.Content))
	if output == "" {
		return "", fmt.Errorf("memory organizer returned empty JSON (stop_reason=%s)", response.StopReason)
	}
	return output, nil
}
