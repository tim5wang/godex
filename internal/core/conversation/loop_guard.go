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
	// MaxNoMutationRounds caps consecutive tool rounds that mutate no file
	// (no edit_file/write_file). Research spirals trip it; real implementation
	// writes files within a few rounds. <= 0 disables the detector.
	MaxNoMutationRounds int
	// Mode controls abort behavior: strict may abort after exhausting
	// recoveries; balanced never aborts (infinite recoveries); warn
	// always recovers with a stronger warning but never aborts.
	Mode LoopGuardMode
	// StaleTodoThreshold is the number of turns an in_progress todo item is
	// allowed to stay unchanged while the agent keeps executing non-todo tools
	// before the loop guard emits a stale_todo_in_progress recovery feedback.
	// <= 0 disables the stale-todo detector.
	StaleTodoThreshold int
}

// todoStalenessProvider reports in_progress todo items that have been
// sitting in the same state across multiple turns. The loop guard queries
// it after every executed-tool batch and emits a recovery feedback when
// an item has gone stale. Implementations live in the agent package so the
// conversation package can stay free of domain/todo imports.
type todoStalenessProvider interface {
	StaleInProgress(maxTurns int) (itemID int, content string, activeForm string, ok bool)
}

type loopGuard struct {
	config        loopGuardConfig
	repeated      map[string]int
	polling       map[string]pollingToolRepeatState
	recent        []string
	recovered     map[string]int
	totalRecovery int
	// noMutationRounds counts consecutive tool batches that mutated no file.
	noMutationRounds int
	// staleRecovered tracks how many times the loop guard has already issued
	// a stale_todo_in_progress recovery for a given todo itemID. It is a
	// separate counter from `recovered` because the fingerprint space is
	// disjoint (itemID vs tool fingerprint) and a single todo item should
	// be re-prompted up to MaxRecoveries times before the guard aborts.
	staleRecovered map[int]int
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
		config:        config,
		repeated:      make(map[string]int),
		polling:       make(map[string]pollingToolRepeatState),
		recent:        make([]string, 0, repeatedToolCycleWindow),
		recovered:     make(map[string]int),
		staleRecovered: make(map[int]int),
	}
}

func (g *loopGuard) Observe(executed []ExecutedTool, staleProvider todoStalenessProvider) loopGuardDecision {
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
	// Stale-todo detection runs after the other detectors so a tool that is
	// already triggering repeated-tool or stalled-polling abort paths takes
	// priority. The detector only fires when at least one non-todo_write tool
	// executed in this turn: if the model is actively calling todo_write, the
	// in_progress state may be in the middle of being updated, so we do not
	// surface a stale feedback.
	if g.config.StaleTodoThreshold > 0 && staleProvider != nil && executedContainsNonTodoWrite(executed) {
		if itemID, content, activeForm, ok := staleProvider.StaleInProgress(g.config.StaleTodoThreshold); ok {
			return g.decideStaleTodo(loopGuardDecision{
				Reason:      "stale_todo_in_progress",
				Fingerprint: "stale_todo:" + itoa(itemID),
				Count:       g.config.StaleTodoThreshold,
			}, itemID, content, activeForm)
		}
	}
	// No-mutation spiral: many consecutive tool rounds without touching a file
	// means the model is researching/looping instead of implementing. Count
	// rounds (one Observe call = one tool batch), reset on any mutation.
	if g.config.MaxNoMutationRounds > 0 {
		if executedContainsMutation(executed) {
			g.noMutationRounds = 0
		} else {
			g.noMutationRounds++
			if g.noMutationRounds >= g.config.MaxNoMutationRounds {
				g.noMutationRounds = 0
				last := executed[len(executed)-1]
				return g.decide(loopGuardDecision{
					Reason:      "no_mutation_spiral",
					Fingerprint: "no_mutation",
					Tool:        last,
					Count:       g.config.MaxNoMutationRounds,
				})
			}
		}
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
	// warn mode: always recover, never abort. The model gets feedback but the
	// runner continues indefinitely. Useful for long-running tasks where the
	// user prefers the agent decide when to stop.
	if g.config.Mode == LoopGuardModeWarn {
		g.totalRecovery++
		decision.Action = loopGuardRecover
		decision.Feedback = loopGuardFeedback(decision, g.totalRecovery, -1)
		decision.RecoveryHint = fmt.Sprintf("Loop guard recovery %d (warn-only mode) asked the model to change strategy.", g.totalRecovery)
		return decision
	}
	// balanced mode: recover infinitely but never abort. Differs from warn
	// by still tracking per-fingerprint exhaustion for better diagnostics.
	if g.config.Mode == LoopGuardModeBalanced {
		g.recovered[decision.Fingerprint]++
		g.totalRecovery++
		decision.Action = loopGuardRecover
		decision.Feedback = loopGuardFeedback(decision, g.totalRecovery, -1)
		decision.RecoveryHint = fmt.Sprintf("Loop guard recovery %d (balanced mode) asked the model to change strategy.", g.totalRecovery)
		return decision
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
	switch d.Reason {
	case "tool_cycle":
		tool := strings.TrimSpace(d.Tool.Name)
		if tool == "" {
			tool = "unknown_tool"
		}
		return fmt.Sprintf("%s detected involving %s; cycle length %d repeated", d.Reason, tool, d.CycleLength)
	case "stale_todo_in_progress":
		// Stale-todo decisions are keyed by todo itemID rather than by a tool
		// name, so the summary omits `Tool.Name` and the in_progress item
		// itself is the diagnostic anchor.
		return fmt.Sprintf("%s detected; an in_progress todo item has not been updated for %d turns", d.Reason, d.Count)
	default:
		tool := strings.TrimSpace(d.Tool.Name)
		if tool == "" {
			tool = "unknown_tool"
		}
		return fmt.Sprintf("%s detected for %s after %d repeat(s)", d.Reason, tool, d.Count)
	}
}

func (d loopGuardDecision) AbortMessage() string {
	reason := strings.TrimSpace(d.AbortReason)
	if reason == "" {
		reason = "loop guard determined there was no progress"
	}
	if d.Reason == "stale_todo_in_progress" {
		// Stale-todo aborts do not have a meaningful last-tool input/output,
		// so the abort message focuses on the in_progress item itself.
		return fmt.Sprintf("%s; %s; stale_count=%d", d.Summary(), reason, d.Count)
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
	budget := fmt.Sprintf("Recovery budget: %d/%d.", recovery, maxRecovery)
	if maxRecovery < 0 {
		budget = fmt.Sprintf("Recovery #%d (unlimited).", recovery)
	}
	return strings.Join([]string{
		"Runtime feedback: loop_guard_recovery.",
		fmt.Sprintf("Reason: %s.", decision.Summary()),
		budget,
		"Do not repeat the same tool call, query, polling request, or tool sequence again.",
		"Use the tool result/error already in context as evidence, change strategy, try a meaningfully different input/tool, or provide a concise diagnostic handoff to the user.",
		"Last tool input: " + truncateLoopGuardText(marshalLoopGuardValue(decision.Tool.Input), 500),
		"Last tool output/error: " + truncateLoopGuardText(strings.TrimSpace(decision.Tool.Output+"\n"+decision.Tool.Error), 700),
	}, "\n")
}

func loopGuardAbortHint(decision loopGuardDecision) string {
	return "Loop guard already attempted runtime feedback or exhausted recovery budget; review the latest tool result/error and avoid repeating the same strategy before retrying."
}

// decideStaleTodo is the stale-todo counterpart to decide. It reuses the same
// global MaxRecoveries budget so that a session cannot be rescued forever by
// alternating stale-todo and identical-tool recoveries, but it tracks per
// itemID recovery counts via `staleRecovered` so that a single in_progress
// item that the model refuses to mark completed is escalated to abort after
// MaxRecoveries observations.
func (g *loopGuard) decideStaleTodo(decision loopGuardDecision, itemID int, content, activeForm string) loopGuardDecision {
	if g.config.Mode == LoopGuardModeWarn || g.config.Mode == LoopGuardModeBalanced {
		g.staleRecovered[itemID]++
		g.totalRecovery++
		decision.Action = loopGuardRecover
		decision.Feedback = staleTodoFeedback(itemID, content, activeForm, g.staleRecovered[itemID], -1)
		decision.RecoveryHint = fmt.Sprintf("Loop guard recovery %d (%s mode) reminded the model to update the stale in_progress todo item.", g.totalRecovery, g.config.Mode)
		return decision
	}
	if g.config.MaxRecoveries <= 0 {
		decision.Action = loopGuardAbort
		decision.AbortReason = "loop guard recovery is disabled"
		decision.RecoveryHint = loopGuardAbortHint(decision)
		return decision
	}
	if g.staleRecovered[itemID] >= g.config.MaxRecoveries {
		decision.Action = loopGuardAbort
		decision.AbortReason = "same in_progress todo item stayed stale after runtime feedback"
		decision.RecoveryHint = loopGuardAbortHint(decision)
		return decision
	}
	if g.totalRecovery >= g.config.MaxRecoveries {
		decision.Action = loopGuardAbort
		decision.AbortReason = "loop guard recovery budget exhausted"
		decision.RecoveryHint = loopGuardAbortHint(decision)
		return decision
	}
	g.staleRecovered[itemID]++
	g.totalRecovery++
	decision.Action = loopGuardRecover
	decision.Feedback = staleTodoFeedback(itemID, content, activeForm, g.staleRecovered[itemID], g.config.MaxRecoveries)
	decision.RecoveryHint = fmt.Sprintf("Loop guard recovery %d/%d reminded the model to update the stale in_progress todo item.", g.totalRecovery, g.config.MaxRecoveries)
	return decision
}

// staleTodoFeedback produces the user-visible runtime feedback that asks the
// model to bring an in_progress todo item back in sync with the work it has
// just performed. It is intentionally different from loopGuardFeedback
// (which is tool-result oriented) because the failing condition here is a
// stale todo state, not a repeated tool call.
func staleTodoFeedback(itemID int, content, activeForm string, recovery, maxRecovery int) string {
	heading := strings.TrimSpace(content)
	if heading == "" {
		heading = fmt.Sprintf("item %d", itemID)
	}
	form := strings.TrimSpace(activeForm)
	lines := []string{
		"Runtime feedback: stale_todo_in_progress.",
		fmt.Sprintf("Reason: in_progress todo %q has not been updated for multiple turns while non-todo tools keep executing.", heading),
	}
	if form != "" {
		lines = append(lines, fmt.Sprintf("Active form on file: %q.", form))
	}
	budget := fmt.Sprintf("Recovery budget: %d/%d.", recovery, maxRecovery)
	if maxRecovery < 0 {
		budget = fmt.Sprintf("Recovery #%d (unlimited).", recovery)
	}
	lines = append(lines,
		budget,
		"Call todo_write now to: (1) mark this item completed if the work is done, (2) flip it to the next pending item, or (3) split it into smaller steps and update the plan.",
		"Do not advance to the next sub-step without first reflecting the current state in the todo list.",
	)
	return strings.Join(lines, "\n")
}

// executedContainsNonTodoWrite reports whether at least one of the executed
// tools is something other than todo_write. Stale-todo recovery should only
// fire when the model is busy with non-todo work; if the model is actively
// calling todo_write, the in_progress state may be transitioning and we
// should not surface a stale feedback.
// executedContainsNonTodoWrite reports whether any executed tool is a
// file-mutating tool (edit_file/write_file). bash is deliberately excluded:
// research spirals run read-only bash commands, and counting bash as a
// mutation would mask the spiral. Real implementation work in godex goes
// through edit_file/write_file.
func executedContainsMutation(executed []ExecutedTool) bool {
	for _, tool := range executed {
		switch tool.Name {
		case "edit_file", "write_file":
			return true
		}
	}
	return false
}

func executedContainsNonTodoWrite(executed []ExecutedTool) bool {
	for _, tool := range executed {
		if strings.TrimSpace(tool.Name) != "todo_write" {
			return true
		}
	}
	return false
}

// itoa converts a non-negative int to its decimal string representation.
// It is used in the loop guard only for fingerprint suffixes where the
// int is always small and non-negative, and avoids pulling in strconv
// just for this single call site.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := false
	if value < 0 {
		negative = true
		value = -value
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
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
