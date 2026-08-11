package memory

import (
	"regexp"
	"strings"

	"github.com/tim5wang/godex/internal/core/compress"
)

// memoryDatePrefixRE matches a leading "(YYYY-MM-DD)" bullet date prefix.
var memoryDatePrefixRE = regexp.MustCompile(`^\(\d{4}-\d\d-\d\d\)\s*`)

// normalizeMemoryLine normalizes one line of memory content for dedup
// comparison: strips bullet markers, strips a leading (YYYY-MM-DD) date
// prefix, collapses whitespace, and lowercases. Empty lines normalize to "".
func normalizeMemoryLine(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "- ")
	line = strings.TrimPrefix(line, "* ")
	line = memoryDatePrefixRE.ReplaceAllString(line, "")
	line = strings.Join(strings.Fields(line), " ")
	return strings.ToLower(line)
}

// foldCapture merges incoming content lines into existing content with
// normalize-based dedup: lines whose normalized form already exists are
// skipped; genuinely new lines are appended to the end (newest last).
// It returns the merged body and the number of added lines.
//
// This mirrors the reference foldCapture in temp/qm/src/memory/memory-service.ts
// for GoDex's per-entry memory model: Remember updates keep prior content and
// only append new facts, so repeated remembers of the same facts do not
// accumulate duplicates. Explicit Update still replaces content wholesale.
func foldCapture(existing, incoming string) (string, int) {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)
	if incoming == "" {
		return existing, 0
	}

	seen := map[string]struct{}{}
	for _, line := range strings.Split(existing, "\n") {
		if key := normalizeMemoryLine(line); key != "" {
			seen[key] = struct{}{}
		}
	}

	var added []string
	for _, line := range strings.Split(incoming, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key := normalizeMemoryLine(line)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		added = append(added, line)
	}
	if len(added) == 0 {
		return existing, 0
	}
	body := existing
	if body == "" {
		body = strings.Join(added, "\n")
	} else {
		body = body + "\n" + strings.Join(added, "\n")
	}
	return body, len(added)
}

// truncateTextTailToTokenBudget keeps the TAIL (newest) portion of text within
// a token budget, prefixing an ellipsis when content is dropped. Memory content
// is append-oriented (newest facts last), so when trimming for context budget
// the newest tail should survive; truncateTextToTokenBudget keeps the head and
// is used for summaries/identifiers where the beginning matters most.
func truncateTextTailToTokenBudget(text string, maxTokens int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxTokens <= 0 {
		return ""
	}
	if compress.CountTokens(text) <= maxTokens {
		return text
	}

	runes := []rune(text)
	low, high := 0, len(runes)
	best := ""
	for low <= high {
		mid := (low + high) / 2
		candidate := strings.TrimSpace(string(runes[len(runes)-mid:]))
		if candidate == "" {
			low = mid + 1
			continue
		}
		withEllipsis := "..." + candidate
		if compress.CountTokens(withEllipsis) <= maxTokens {
			best = withEllipsis
			low = mid + 1
			continue
		}
		high = mid - 1
	}
	if best != "" {
		return best
	}
	if compress.CountTokens("...") <= maxTokens {
		return "..."
	}
	return ""
}
