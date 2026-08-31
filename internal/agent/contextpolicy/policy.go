// Package contextpolicy owns pure context-budget and handoff truncation rules.
package contextpolicy

import (
	"strings"
	"unicode/utf8"

	"github.com/tim5wang/godex/internal/core/compress"
)

const (
	BudgetOrchestrator = 200_000
	BudgetWorker       = 100_000
	BudgetReviewer     = 100_000
	BudgetResearcher   = 50_000
	BudgetDefault      = 100_000
)

// TruncateText keeps text within a token budget while retaining its head and tail.
func TruncateText(text string, budget int) string {
	text = strings.TrimSpace(text)
	if text == "" || budget <= 0 {
		return ""
	}
	if compress.CountTokens(text) <= budget {
		return text
	}
	const marker = "\n...[truncated]\n"
	runes := []rune(text)
	headEnd := runesWithinTokenBudget(runes, budget/2)
	head := string(runes[:headEnd])
	tailBudget := budget - compress.CountTokens(head) - compress.CountTokens(marker)
	if tailBudget <= 0 {
		return head + marker
	}
	tailStart := len(runes)
	used := 0
	for i := len(runes) - 1; i >= headEnd; i-- {
		used += compress.CountTokens(string(runes[i]))
		if used > tailBudget {
			break
		}
		tailStart = i
	}
	return head + marker + string(runes[tailStart:])
}

// AssembleHandoffs joins chunks under a byte ceiling without splitting UTF-8.
func AssembleHandoffs(chunks []string, byteLimit, defaultByteLimit int) string {
	if byteLimit <= 0 {
		byteLimit = defaultByteLimit
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
				builder.WriteString(truncateBytes(chunk, remaining-len(marker)))
			}
			builder.WriteString(marker)
			break
		}
		builder.WriteString(chunk)
	}
	return builder.String()
}

// RoleBudget returns the maximum context tokens for a subagent role.
func RoleBudget(roleID, agentType string) int {
	id := strings.ToLower(strings.TrimSpace(firstNonEmpty(roleID, agentType)))
	switch {
	case strings.Contains(id, "orchestrator"):
		return BudgetOrchestrator
	case strings.Contains(id, "reviewer"):
		return BudgetReviewer
	case strings.Contains(id, "researcher"), strings.Contains(id, "research"):
		return BudgetResearcher
	case strings.Contains(id, "worker"):
		return BudgetWorker
	default:
		return BudgetDefault
	}
}

func runesWithinTokenBudget(runes []rune, budget int) int {
	used, end := 0, 0
	for budget > 0 && end < len(runes) {
		used += compress.CountTokens(string(runes[end]))
		if used > budget {
			break
		}
		end++
	}
	return end
}

func truncateBytes(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	end := 0
	for end < len(text) {
		_, size := utf8.DecodeRuneInString(text[end:])
		if end+size > maxBytes {
			break
		}
		end += size
	}
	return text[:end]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
