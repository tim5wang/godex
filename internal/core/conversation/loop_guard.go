package conversation

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

type loopGuardAction string

const (
	loopGuardAllow   loopGuardAction = "allow"
	loopGuardRecover loopGuardAction = "recover"
	loopGuardAbort   loopGuardAction = "abort"
)

type loopGuardConfig struct {
	MaxRepeatedTools           int
	MaxRepeatedPollingTools    int
	MaxStalledTaskPollingTools int
	MaxRecoveries              int
}

type loopGuard struct {
	config        loopGuardConfig
	repeated      map[string]int
	polling       map[string]pollingToolRepeatState
	recent        []string
	recovered     map[string]int
	totalRecovery int
}

type loopGuardDecision struct {
	Action       loopGuardAction
	Reason       string
	Fingerprint  string
	Tool         ExecutedTool
	Count        int
	CycleLength  int
	Feedback     string
	RecoveryHint string
	AbortReason  string
}

func newLoopGuard(config loopGuardConfig) *loopGuard {
	if config.MaxRecoveries < 0 {
		config.MaxRecoveries = 0
	}
	return &loopGuard{
		config:    config,
		repeated:  make(map[string]int),
		polling:   make(map[string]pollingToolRepeatState),
		recent:    make([]string, 0, repeatedToolCycleWindow),
		recovered: make(map[string]int),
	}
}

func (g *loopGuard) Observe(executed []ExecutedTool) loopGuardDecision {
	if g == nil || len(executed) == 0 {
		return loopGuardDecision{Action: loopGuardAllow}
	}
	if tool, count, fingerprint, ok := g.repeatedToolExecution(executed); ok {
		return g.decide(loopGuardDecision{
			Reason:      "identical_tool_result",
			Fingerprint: "identical:" + fingerprint,
			Tool:        tool,
			Count:       count,
		})
	}
	if tool, count, fingerprint, ok := g.repeatedPollingToolExecution(executed); ok {
		return g.decide(loopGuardDecision{
			Reason:      "stalled_polling",
			Fingerprint: "polling:" + fingerprint,
			Tool:        tool,
			Count:       count,
		})
	}
	g.recent = appendRecentToolFingerprints(g.recent, executed)
	if cycleLen, fingerprint, ok := repeatedToolCycleV2(g.recent); ok {
		last := executed[len(executed)-1]
		return g.decide(loopGuardDecision{
			Reason:      "tool_cycle",
			Fingerprint: "cycle:" + fingerprint,
			Tool:        last,
			CycleLength: cycleLen,
			Count:       3,
		})
	}
	return loopGuardDecision{Action: loopGuardAllow}
}

func (g *loopGuard) repeatedToolExecution(executed []ExecutedTool) (ExecutedTool, int, string, bool) {
	limit := g.config.MaxRepeatedTools
	if limit <= 0 {
		return ExecutedTool{}, 0, "", false
	}
	for _, tool := range executed {
		if isNoProgressPollingToolCall(tool) {
			continue
		}
		if isBenignRepeatableToolCall(tool) {
			fingerprint := benignRepeatableToolCountPrefix + executedToolFingerprint(tool)
			g.repeated[fingerprint]++
			if g.repeated[fingerprint] >= limit {
				return tool, g.repeated[fingerprint], fingerprint, true
			}
			continue
		}
		clearBenignRepeatableToolCounts(g.repeated)
		fingerprint := executedToolFingerprint(tool)
		g.repeated[fingerprint]++
		if g.repeated[fingerprint] >= limit {
			return tool, g.repeated[fingerprint], fingerprint, true
		}
	}
	return ExecutedTool{}, 0, "", false
}

func (g *loopGuard) repeatedPollingToolExecution(executed []ExecutedTool) (ExecutedTool, int, string, bool) {
	inputLimit := g.config.MaxRepeatedPollingTools
	stalledLimit := g.config.MaxStalledTaskPollingTools
	if inputLimit <= 0 && stalledLimit <= 0 {
		return ExecutedTool{}, 0, "", false
	}
	for _, tool := range executed {
		fingerprint := pollingToolInputFingerprint(tool)
		if fingerprint == "" {
			continue
		}
		if isNoProgressPollingToolCall(tool) {
			if stalledLimit <= 0 {
				continue
			}
			semantic, terminal := pollingToolSemanticFingerprint(tool)
			if terminal {
				delete(g.polling, fingerprint)
				continue
			}
			state := g.polling[fingerprint]
			if state.Semantic == semantic {
				state.Count++
			} else {
				state.Count = 1
				state.Semantic = semantic
			}
			g.polling[fingerprint] = state
			if state.Count >= stalledLimit {
				return tool, state.Count, fingerprint + ":" + semantic, true
			}
			continue
		}
		if inputLimit <= 0 {
			continue
		}
		state := g.polling[fingerprint]
		state.Count++
		g.polling[fingerprint] = state
		if state.Count >= inputLimit {
			return tool, state.Count, fingerprint, true
		}
	}
	return ExecutedTool{}, 0, "", false
}

func (g *loopGuard) decide(decision loopGuardDecision) loopGuardDecision {
	if decision.Fingerprint == "" {
		decision.Fingerprint = decision.Reason
	}
	if g.config.MaxRecoveries <= 0 {
		decision.Action = loopGuardAbort
		decision.AbortReason = "loop guard recovery is disabled"
		decision.RecoveryHint = loopGuardAbortHint(decision)
		return decision
	}
	if g.recovered[decision.Fingerprint] > 0 {
		decision.Action = loopGuardAbort
		decision.AbortReason = "same loop pattern repeated after runtime feedback"
		decision.RecoveryHint = loopGuardAbortHint(decision)
		return decision
	}
	if g.totalRecovery >= g.config.MaxRecoveries {
		decision.Action = loopGuardAbort
		decision.AbortReason = "loop guard recovery budget exhausted"
		decision.RecoveryHint = loopGuardAbortHint(decision)
		return decision
	}
	g.recovered[decision.Fingerprint]++
	g.totalRecovery++
	decision.Action = loopGuardRecover
	decision.Feedback = loopGuardFeedback(decision, g.totalRecovery, g.config.MaxRecoveries)
	decision.RecoveryHint = fmt.Sprintf("Loop guard recovery %d/%d asked the model to change strategy before aborting.", g.totalRecovery, g.config.MaxRecoveries)
	return decision
}

func (d loopGuardDecision) Summary() string {
	tool := strings.TrimSpace(d.Tool.Name)
	if tool == "" {
		tool = "unknown_tool"
	}
	switch d.Reason {
	case "tool_cycle":
		return fmt.Sprintf("%s detected involving %s; cycle length %d repeated", d.Reason, tool, d.CycleLength)
	default:
		return fmt.Sprintf("%s detected for %s after %d repeat(s)", d.Reason, tool, d.Count)
	}
}

func (d loopGuardDecision) AbortMessage() string {
	reason := strings.TrimSpace(d.AbortReason)
	if reason == "" {
		reason = "loop guard determined there was no progress"
	}
	return fmt.Sprintf("%s; %s; last tool=%s; count=%d; input=%s; output=%s; error=%s",
		d.Summary(),
		reason,
		firstNonEmpty(strings.TrimSpace(d.Tool.Name), "unknown_tool"),
		d.Count,
		truncateLoopGuardText(marshalLoopGuardValue(d.Tool.Input), 240),
		truncateLoopGuardText(d.Tool.Output, 240),
		truncateLoopGuardText(d.Tool.Error, 180),
	)
}

func loopGuardFeedback(decision loopGuardDecision, recovery, maxRecovery int) string {
	return strings.Join([]string{
		"Runtime feedback: loop_guard_recovery.",
		fmt.Sprintf("Reason: %s.", decision.Summary()),
		fmt.Sprintf("Recovery budget: %d/%d.", recovery, maxRecovery),
		"Do not repeat the same tool call, query, polling request, or tool sequence again.",
		"Use the tool result/error already in context as evidence, change strategy, try a meaningfully different input/tool, or provide a concise diagnostic handoff to the user.",
		"Last tool input: " + truncateLoopGuardText(marshalLoopGuardValue(decision.Tool.Input), 500),
		"Last tool output/error: " + truncateLoopGuardText(strings.TrimSpace(decision.Tool.Output+"\n"+decision.Tool.Error), 700),
	}, "\n")
}

func loopGuardAbortHint(decision loopGuardDecision) string {
	return "Loop guard already attempted runtime feedback or exhausted recovery budget; review the latest tool result/error and avoid repeating the same strategy before retrying."
}

func repeatedToolCycleV2(recent []string) (int, string, bool) {
	n := len(recent)
	for cycleLen := 2; cycleLen <= 3; cycleLen++ {
		needed := cycleLen * 3
		if n < needed {
			continue
		}
		tail := recent[n-needed:]
		matches := true
		for i := cycleLen; i < needed; i++ {
			if tail[i] != tail[i%cycleLen] {
				matches = false
				break
			}
		}
		if matches {
			return cycleLen, hashLoopGuardStrings(tail...), true
		}
	}
	return 0, "", false
}

func marshalLoopGuardValue(value interface{}) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

func hashLoopGuardStrings(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return fmt.Sprintf("%x", sum[:])
}

func truncateLoopGuardText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
