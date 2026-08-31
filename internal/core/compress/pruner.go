package compress

import (
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// prunerMarker is the stable middle marker substituted for the removed span of
// an oversized tool result. It keeps the "tool_result_truncated" convention
// used by the provider-facing stubs and references the transcript when known.
func prunerMarker(prunedChars, totalChars int, transcript string) string {
	ref := ""
	if strings.TrimSpace(transcript) != "" {
		ref = "; full output in transcript " + transcript
	}
	return fmt.Sprintf("\n\n[tool_result_truncated: pruned %d of %d characters%s]\n\n", prunedChars, totalChars, ref)
}

// PruneToolResultText trims an over-budget tool-result text to its head and
// tail around a stable marker (model-free, deterministic). Slicing is by
// Unicode code point so a retained boundary never splits a surrogate pair.
// Returns the pruned text and whether it changed.
func PruneToolResultText(text string, thresholdChars, headChars, tailChars int, transcript string) (string, bool) {
	runes := []rune(text)
	if len(runes) <= thresholdChars {
		return text, false
	}
	if headChars < 0 {
		headChars = 0
	}
	if tailChars < 0 {
		tailChars = 0
	}
	if headChars > len(runes) {
		headChars = len(runes)
	}
	if tailChars > len(runes) {
		tailChars = len(runes)
	}
	head := string(runes[:headChars])
	tail := string(runes[len(runes)-tailChars:])
	pruned := len(runes) - headChars - tailChars
	if pruned < 0 {
		pruned = 0
	}
	return head + prunerMarker(pruned, len(runes), transcript) + tail, true
}

// PruneOversizedToolResults clones messages and prunes tool-result text blocks
// above thresholdChars to head + marker + tail. Applied to the region fed to
// the LLM summarizer so oversized outputs shrink without dropping the head and
// tail (Phase 4.1, model-free). Deterministic and safe to reuse.
func PruneOversizedToolResults(messages []protocol.Message, thresholdChars, headChars, tailChars int, transcript string) []protocol.Message {
	if thresholdChars <= 0 {
		return protocol.CloneMessages(messages)
	}
	cloned := protocol.CloneMessages(messages)
	for msgIdx := range cloned {
		for blockIdx, block := range cloned[msgIdx].Content {
			if block.Type != protocol.BlockToolResult {
				continue
			}
			pruned, changed := PruneToolResultText(block.Content, thresholdChars, headChars, tailChars, transcript)
			if changed {
				cloned[msgIdx].Content[blockIdx].Content = pruned
			}
		}
	}
	return cloned
}
