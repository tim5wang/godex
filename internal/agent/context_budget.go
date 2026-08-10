package agent

import (
	"strings"

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
				builder.WriteString(chunk[:remaining-len(marker)])
			}
			builder.WriteString(marker)
			break
		}
		builder.WriteString(chunk)
	}
	return builder.String()
}
