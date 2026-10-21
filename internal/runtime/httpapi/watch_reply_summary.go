// godex-feature: watch-reply-summary
// 腕上精简回复（B4）：把 agent 的整段回复收敛成腕上能读的一小段。
// 入口：GET /watch/sessions/{id} 的 reply_summary 字段
// 文档：docs/wearos-watch-implementation-plan.md §12.2 B4
package httpapi

import (
	"regexp"
	"strings"
)

const (
	// maxWatchReplyRunes bounds the summary. A 454px watch renders this with
	// maxLines=2, so the UI enforces the two-line contract visually; this bound
	// exists so the endpoint never ships a whole paragraph to a wrist, and so the
	// cut lands somewhere predictable (a sentence boundary) rather than wherever
	// the screen happens to overflow.
	maxWatchReplyRunes = 120

	// watchReplyCodeOnlyNotice stands in for a reply that is entirely code.
	//
	// Returning an empty summary would leave the detail screen looking as if the
	// agent had said nothing, which is worse than saying where to look: the reply
	// did happen, it just cannot be read on a wrist.
	watchReplyCodeOnlyNotice = "（内容为代码/命令，请在电脑端查看）"
)

// markdownLink keeps the label and drops the target: a URL is not actionable on a
// wrist, and the label is the human-readable part.
var markdownLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// markdownNoise are the inline markers that only mean something to a renderer.
var markdownNoise = strings.NewReplacer(
	"**", "", "__", "", "~~", "", "`", "", "*", "",
)

// watchReplySummary condenses an agent reply into what a wrist can usefully show.
//
// Deliberately deterministic: it is pure text shaping, with no model call. A
// summary endpoint that cost an LLM round-trip would make every watch poll slow
// and expensive, and would produce a different answer each time the screen was
// opened.
func watchReplySummary(text string) string {
	stripped := stripFencedCode(text)
	// Tighten BEFORE collapsing whitespace, not after.
	//
	// At this stage paragraph breaks are still newlines, so the only spaces in play
	// are the artifacts left behind by removed inline markers ("已 **重构**" ->
	// "已 重构"). Once whitespace is collapsed, a paragraph boundary is
	// indistinguishable from such an artifact, and removing it runs two paragraphs
	// together: "已完成" + "已重构" fused into "已完成已重构". That is a real bug this
	// ordering fixes; the alternative reads as a typo on the wrist.
	cleaned := collapseWhitespace(tightenCJKSpace(stripMarkdownNoise(stripped)))
	if cleaned == "" {
		// The reply was nothing but code/markup. Say so instead of returning an
		// empty string that reads as silence.
		if collapseWhitespace(text) != "" {
			return watchReplyCodeOnlyNotice
		}
		return ""
	}
	return cutAtSentence(cleaned, maxWatchReplyRunes)
}

// tightenCJKSpace removes a space that sits between two CJK runes.
//
// Dropping an inline marker can leave a space with CJK on both sides: "已 **重构**"
// becomes "已 重构", which reads as a typo in Chinese. A space at a Latin/CJK
// boundary ("auth 中间件") is legitimate and is kept.
func tightenCJKSpace(text string) string {
	runes := []rune(text)
	out := make([]rune, 0, len(runes))
	for i, r := range runes {
		if r == ' ' && i > 0 && i < len(runes)-1 && isCJK(runes[i-1]) && isCJK(runes[i+1]) {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// isCJK reports whether a rune is a CJK ideograph or CJK punctuation. Mapped from
// the same ranges the wrist font renders.
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // Extension A
		(r >= 0x3000 && r <= 0x303F) || // CJK punctuation
		(r >= 0xFF00 && r <= 0xFFEF) // Fullwidth forms
}

// stripFencedCode removes ``` / ~~~ blocks. A wrist cannot show code, and a long
// script would otherwise consume the entire summary budget and hide the sentence
// that explains it.
func stripFencedCode(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// stripMarkdownNoise drops block and inline markers, keeping the words.
func stripMarkdownNoise(text string) string {
	text = markdownLink.ReplaceAllString(text, "$1")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "#")
		line = strings.TrimPrefix(line, "> ")
		if isListMarker(line) {
			line = strings.TrimSpace(line[2:])
		}
		lines[i] = strings.TrimSpace(markdownNoise.Replace(line))
	}
	return strings.Join(lines, "\n")
}

// isListMarker reports whether a line starts with "- " / "* " / "+ " / "1. ".
func isListMarker(line string) bool {
	if len(line) >= 2 && (line[1] == ' ') {
		switch line[0] {
		case '-', '*', '+':
			return true
		}
	}
	// Ordered item: digits followed by ". ".
	dot := strings.Index(line, ". ")
	if dot > 0 && dot <= 3 {
		for _, r := range line[:dot] {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// collapseWhitespace turns the multi-line reply into one paragraph, so the
// watch's two lines are filled with words rather than indentation.
func collapseWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// cutAtSentence trims to a sentence boundary when one is available in the second
// half of the budget; otherwise it cuts at the budget. Either way the result reads
// as a deliberate sentence, which is what makes two lines tolerable.
func cutAtSentence(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	for i := limit - 1; i >= limit/2; i-- {
		switch runes[i] {
		case '.', '!', '?', '。', '！', '？':
			return strings.TrimSpace(string(runes[:i+1]))
		}
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
