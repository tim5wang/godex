package agent

import (
	"github.com/tim5wang/godex/internal/agent/contextpolicy"
	"github.com/tim5wang/godex/internal/core/compress"
)

// Phase 2.3 — 上下文预算管理.
//
// Completed subtasks must not stay in the active context at full size. The
// durable workflow runtime already keeps only bounded handoff artifacts; this
// file centralizes the token-budget truncation used to assemble those
// artifacts (per-node summaries, dependency handoff text, merge-point
// synthesis).

// workflowHandoffSummaryTokenBudget is the token budget for a single
// subtask summary stored in a handoff artifact. 2000 tokens is roughly a
// quarter of a worker's 100K budget and enough for verdict + changed files +
// key findings.
const workflowHandoffSummaryTokenBudget = 2000

// compressCountTokensForText is a thin indirection over the character-class
// token estimator so call sites read as a budget check.
func compressCountTokensForText(text string) int {
	return compress.CountTokens(text)
}

// truncateTextToTokenBudget keeps a text within a token budget using a
// head-biased strategy: the head (verdict, summary, key findings) is always
// kept; when the text overflows, the tail is appended until the budget is
// exhausted, with a clear truncation marker in between. A marker is emitted
// only when content was actually dropped.
func truncateTextToTokenBudget(text string, budget int) string {
	return contextpolicy.TruncateText(text, budget)
}

// assembleTruncatedHandoffs joins dependency handoff chunks under a byte
// ceiling (the durable handoff_max_bytes limit). It is the shared assembly
// used by dependency handoff injection and merge-point synthesis so the
// truncation behavior is consistent everywhere a child consumes parent
// results.
func assembleTruncatedHandoffs(chunks []string, byteLimit int) string {
	return contextpolicy.AssembleHandoffs(chunks, byteLimit, workflowDefaultHandoffMaxBytes)
}

// Phase 4.6 — 上下文预算按角色分配.
//
// Different roles get different max context token budgets (roadmap 4.6): the
// orchestrator gets the largest window, workers and reviewers a mid budget,
// and researchers the smallest. When a subagent's accumulated history exceeds
// its role budget the run loop compacts it (auto-compaction) instead of
// letting the request grow unbounded.
const (
	roleContextBudgetOrchestrator = contextpolicy.BudgetOrchestrator
	roleContextBudgetWorker       = contextpolicy.BudgetWorker
	roleContextBudgetReviewer     = contextpolicy.BudgetReviewer
	roleContextBudgetResearcher   = contextpolicy.BudgetResearcher
	defaultRoleContextBudget      = contextpolicy.BudgetDefault
)

// roleContextBudgetTokens resolves the max context tokens for a subagent
// role. Unknown / empty roles fall back to the default worker budget.
func roleContextBudgetTokens(roleID, agentType string) int {
	return contextpolicy.RoleBudget(roleID, agentType)
}
