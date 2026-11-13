package agent

import (
	"strings"
	"testing"
)

// The reply-style prompt is a product surface: it is what makes answers lead with
// the outcome, separate verified layers, and stay silent when nothing changed.
// Pin the guarantees a rewording must not silently drop.
func TestResponseStylePromptKeepsComprehensionGuarantees(t *testing.T) {
	for _, want := range []struct {
		phrase string
		why    string
	}{
		{"Answer first", "结论先于过程"},
		{"Match the request", "不因简洁吞掉用户要求的细节"},
		{"Never present a passing unit test or build as proof that a UI flow works", "不把单测/build 外推成界面可用"},
		{"Report progress only when something changed", "无变化不重复长报"},
		{"On failure, say what did not work", "失败必须说清影响与责任人"},
		{"Do not stop at \"please verify\"", "已授权时不要把工作推回用户"},
	} {
		if !strings.Contains(conciseDefaultResponsePrompt, want.phrase) {
			t.Errorf("response style prompt lost %q (%s): %s", want.phrase, want.why, conciseDefaultResponsePrompt)
		}
	}
}

// Recording calls belong before the visible answer: if the final text is followed
// by checkpoint/ack, the real answer is what gets folded away in the feed.
func TestRuntimePromptOrdersLedgerRecordingBeforeFinalReply(t *testing.T) {
	a := newTestAgent(t, 4096)
	system, err := a.buildRuntimeSystemPrompt()
	if err != nil {
		t.Fatalf("buildRuntimeSystemPrompt: %v", err)
	}
	for _, want := range []string{
		"finish any required work_context recording",
		"If saving fails, say the state was not saved",
	} {
		if !strings.Contains(system, want) {
			t.Errorf("runtime prompt missing %q", want)
		}
	}
}
