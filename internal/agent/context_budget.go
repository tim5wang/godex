package agent

import (
	"strings"
	"unicode/utf8"

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
	text = strings.TrimSpace(text)
	if text == "" || budget <= 0 {
		return ""
	}
	if compressCountTokensForText(text) <= budget {
		return text
	}
	const truncationMarker = "\n...[truncated]\n"
	runes := []rune(text)
	headBudget := budget / 2
	headEnd := runesWithinTokenBudget(runes, headBudget)
	head := string(runes[:headEnd])
	markerTokens := compressCountTokensForText(truncationMarker)
	tailBudget := budget - compressCountTokensForText(head) - markerTokens
	if tailBudget <= 0 {
		return head + truncationMarker
	}
	// Scan backwards from the end, keeping as much of the tail as fits.
	tailStart := len(runes)
	used := 0
	for i := len(runes) - 1; i >= headEnd; i-- {
		used += compressCountTokensForText(string(runes[i]))
		if used > tailBudget {
			break
		}
		tailStart = i
	}
	return head + truncationMarker + string(runes[tailStart:])
}

// runesWithinTokenBudget returns the number of leading runes that fit within
// the token budget (greedy).
func runesWithinTokenBudget(runes []rune, budget int) int {
	if budget <= 0 {
		return 0
	}
	used := 0
	end := 0
	for end < len(runes) {
		used += compressCountTokensForText(string(runes[end]))
		if used > budget {
			break
		}
		end++
	}
	return end
}

// truncateChunkToByteLimit returns the longest valid-UTF-8 prefix of chunk
// whose byte length does not exceed maxBytes. It never splits a multi-byte
// rune, so CJK-heavy handoff content stays well-formed when a chunk is cut
// at the byte ceiling.
func truncateChunkToByteLimit(chunk string, maxBytes int) string {
	if maxBytes <= 0 || len(chunk) <= maxBytes {
		return chunk
	}
	used := 0
	end := 0
	for end < len(chunk) {
		_, size := utf8.DecodeRuneInString(chunk[end:])
		if used+size > maxBytes {
			break
		}
		used += size
		end += size
	}
	return chunk[:end]
}

// assembleTruncatedHandoffs joins dependency handoff chunks under a byte
// ceiling (the durable handoff_max_bytes limit). It is the shared assembly
// used by dependency handoff injection and merge-point synthesis so the
// truncation behavior is consistent everywhere a child consumes parent
// results.
func assembleTruncatedHandoffs(chunks []string, byteLimit int) string {
	if byteLimit <= 0 {
		byteLimit = workflowDefaultHandoffMaxBytes
	}
	var builder strings.Builder
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		if builder.Len()+len(chunk) > byteLimit {
			remaining := byteLimit - builder.Len()
			const marker = "\n[dependency handoffs truncated]\n"
			if remaining-len(marker) > 0 {
				builder.WriteString(truncateChunkToByteLimit(chunk, remaining-len(marker)))
			}
			builder.WriteString(marker)
			break
		}
		builder.WriteString(chunk)
	}
	return builder.String()
}

// Phase 4.6 — 上下文预算按角色分配.
//
// Different roles get different max context token budgets (roadmap 4.6): the
// orchestrator gets the largest window, workers and reviewers a mid budget,
// and researchers the smallest. When a subagent's accumulated history exceeds
// its role budget the run loop compacts it (auto-compaction) instead of
// letting the request grow unbounded.
const (
	roleContextBudgetOrchestrator = 200_000
	roleContextBudgetWorker       = 100_000
	roleContextBudgetReviewer     = 100_000
	roleContextBudgetResearcher   = 50_000
	defaultRoleContextBudget      = 100_000
)

// roleContextBudgetTokens resolves the max context tokens for a subagent
// role. Unknown / empty roles fall back to the default worker budget.
func roleContextBudgetTokens(roleID, agentType string) int {
	id := strings.ToLower(strings.TrimSpace(firstNonEmpty(roleID, agentType)))
	switch {
	case strings.Contains(id, "orchestrator"):
		return roleContextBudgetOrchestrator
	case strings.Contains(id, "reviewer"):
		return roleContextBudgetReviewer
	case strings.Contains(id, "researcher"), strings.Contains(id, "research"):
		return roleContextBudgetResearcher
	case strings.Contains(id, "worker"):
		return roleContextBudgetWorker
	default:
		return defaultRoleContextBudget
	}
}
