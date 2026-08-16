package compress

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/protocol"
)

func TestPruneToolResultTextWithinThresholdUnchanged(t *testing.T) {
	text := strings.Repeat("small output ", 10)
	pruned, changed := PruneToolResultText(text, 8192, 4096, 1024, "t.json")
	if changed || pruned != text {
		t.Fatalf("expected unchanged text below threshold, changed=%v", changed)
	}
}

func TestPruneToolResultTextKeepsHeadMarkerTail(t *testing.T) {
	head := strings.Repeat("HEAD-", 2000) // 10000 chars
	middle := "SECRET_MIDDLE_MARKER"
	tail := strings.Repeat("-TAIL", 1000)
	text := head + middle + tail

	pruned, changed := PruneToolResultText(text, 4096, 2048, 512, "transcript_1.json")
	if !changed {
		t.Fatal("expected oversized text to be pruned")
	}
	if strings.Contains(pruned, middle) {
		t.Fatalf("expected middle removed, got tail of %q", pruned)
	}
	if !strings.HasPrefix(pruned, head[:2048]) {
		t.Fatal("expected head prefix retained")
	}
	if !strings.HasSuffix(pruned, tail[len(tail)-512:]) {
		t.Fatal("expected tail suffix retained")
	}
	if !strings.Contains(pruned, "[tool_result_truncated") || !strings.Contains(pruned, "transcript_1.json") {
		t.Fatalf("expected pruner marker with transcript ref, got %q", pruned)
	}
}

func TestPruneToolResultTextNeverSplitsSurrogatePairs(t *testing.T) {
	// 3000 emoji (surrogate pairs) exceeds the small threshold; slicing by code
	// point must not split a pair (each emoji is one code point).
	text := strings.Repeat("😀", 3000)
	pruned, changed := PruneToolResultText(text, 100, 50, 50, "")
	if !changed {
		t.Fatal("expected prune")
	}
	runes := []rune(pruned)
	if len(runes) <= 100 {
		t.Fatalf("expected head + marker + tail, got %d runes", len(runes))
	}
	// Head and tail boundaries must not split a surrogate pair: the first 50
	// and last 50 runes are full emoji, and the middle is marker text.
	for _, r := range runes[:50] {
		if r != '😀' {
			t.Fatalf("head boundary split a rune: %q", r)
		}
	}
	for _, r := range runes[len(runes)-50:] {
		if r != '😀' {
			t.Fatalf("tail boundary split a rune: %q", r)
		}
	}
}

func TestPruneOversizedToolResultsPrunesOnlyToolResults(t *testing.T) {
	big := strings.Repeat("big output ", 2000)
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "instruction "+strings.Repeat("x", 100000)),
		protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"cmd": "run"})),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-1", big)),
	}
	out := PruneOversizedToolResults(messages, 4096, 2048, 512, "transcript_2.json")
	// The oversized user TEXT stays verbatim (pruner only touches tool results).
	if len(out[0].Content) != 1 || !strings.Contains(out[0].Content[0].Text, "instruction") {
		t.Fatalf("expected non-tool-result text untouched")
	}
	// The tool result is pruned.
	if !strings.Contains(out[2].Content[0].Content, "[tool_result_truncated") {
		t.Fatalf("expected tool result pruned, got %q", out[2].Content[0].Content)
	}
	// Disabled pruner (threshold 0) keeps everything.
	raw := PruneOversizedToolResults(messages, 0, 0, 0, "")
	if raw[2].Content[0].Content != big {
		t.Fatal("expected disabled pruner to keep the raw result")
	}
}
