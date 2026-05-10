package conversation

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/platform/logger"
)

type fallbackCaller struct {
	profiles []config.ModelProfileConfig
	callers  []Caller
	strategy string
	next     uint64
}

// NewFallbackCallerForProfiles creates a caller that tries profiles in order.
func NewFallbackCallerForProfiles(profiles []config.ModelProfileConfig) Caller {
	return NewStrategyCallerForProfiles(llm.StrategyFallback, profiles)
}

// NewStrategyCallerForProfiles creates a caller that applies the configured model selection strategy.
func NewStrategyCallerForProfiles(strategy string, profiles []config.ModelProfileConfig) Caller {
	compact := make([]config.ModelProfileConfig, 0, len(profiles))
	seen := map[string]struct{}{}
	for _, profile := range profiles {
		profile.ID = strings.TrimSpace(profile.ID)
		if profile.ID == "" {
			profile.ID = profile.Model
		}
		key := profile.ID
		if key == "" {
			key = profile.Provider + "|" + profile.Model + "|" + profile.BaseURL
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		compact = append(compact, profile)
	}
	if len(compact) == 0 {
		return NewCallerForProfile(config.ModelProfileConfig{})
	}
	if len(compact) == 1 {
		return NewCallerForProfile(compact[0])
	}
	callers := make([]Caller, 0, len(compact))
	for _, profile := range compact {
		callers = append(callers, NewCallerForProfile(profile))
	}
	strategy = llm.NormalizeStrategy(llm.StrategyConfig{Type: strategy}).Type
	return &fallbackCaller{profiles: compact, callers: callers, strategy: strategy}
}

func (c *fallbackCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	var lastErr error
	order := c.callOrder()
	for pos, i := range order {
		caller := c.callers[i]
		nextReq := requestForProfile(req, c.profiles[i])
		resp, err := caller.Call(ctx, nextReq)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if pos == len(order)-1 || c.strategy == llm.StrategyPrimary || !isRetryableFallbackError(err) {
			return nil, err
		}
		logger.Warnf("model profile %q failed, falling back to %q: %v", c.profiles[i].ID, c.profiles[order[pos+1]].ID, err)
	}
	return nil, lastErr
}

func (c *fallbackCaller) Stream(ctx context.Context, req protocol.Request, handler StreamHandler) (*protocol.Response, error) {
	var lastErr error
	order := c.callOrder()
	for pos, i := range order {
		caller := c.callers[i]
		streamer, ok := caller.(StreamCaller)
		if !ok {
			resp, err := caller.Call(ctx, requestForProfile(req, c.profiles[i]))
			if err == nil {
				return resp, nil
			}
			lastErr = err
		} else {
			resp, err := streamer.Stream(ctx, requestForProfile(req, c.profiles[i]), handler)
			if err == nil {
				return resp, nil
			}
			lastErr = err
		}
		if pos == len(order)-1 || c.strategy == llm.StrategyPrimary || !isRetryableFallbackError(lastErr) {
			return nil, lastErr
		}
		logger.Warnf("model profile %q stream failed before completion, falling back to %q: %v", c.profiles[i].ID, c.profiles[order[pos+1]].ID, lastErr)
	}
	return nil, lastErr
}

func (c *fallbackCaller) callOrder() []int {
	count := len(c.callers)
	if count == 0 {
		return nil
	}
	if c.strategy == llm.StrategyPrimary || count == 1 {
		return []int{0}
	}
	order := make([]int, 0, count)
	start := 0
	if c.strategy == llm.StrategyRoundRobin {
		start = int(atomic.AddUint64(&c.next, 1)-1) % count
	}
	for offset := 0; offset < count; offset++ {
		order = append(order, (start+offset)%count)
	}
	return order
}

func requestForProfile(req protocol.Request, profile config.ModelProfileConfig) protocol.Request {
	req.Model = profile.Model
	if profile.MaxTokens > 0 {
		req.MaxTokens = profile.MaxTokens
	}
	if effort := strings.TrimSpace(profile.ReasoningEffort); effort != "" {
		req.ReasoningEffort = effort
	}
	return req
}

func isRetryableFallbackError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "context canceled") || strings.Contains(text, "cancelled") {
		return false
	}
	for _, token := range []string{
		"timeout", "deadline", "temporarily", "temporary", "connection refused", "connection reset",
		"no such host", "eof", "429", "500", "502", "503", "504", "rate limit", "overloaded",
	} {
		if strings.Contains(text, token) {
			return true
		}
	}
	return strings.TrimSpace(err.Error()) == ""
}
