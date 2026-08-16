package compress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tim5wang/godex/internal/core/protocol"
)

type summaryCaller struct {
	resp     *protocol.Response
	err      error
	requests []protocol.Request
}

func (c *summaryCaller) Call(_ context.Context, req protocol.Request) (*protocol.Response, error) {
	c.requests = append(c.requests, req)
	if c.err != nil {
		return nil, c.err
	}
	if c.resp == nil {
		return &protocol.Response{}, nil
	}
	return c.resp, nil
}

func TestCompactEmitsStructuredSummaryAndTranscript(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "first"),
		protocol.NewTextMessage(protocol.RoleAssistant, "second"),
	}

	compact, err := compressor.Compact(messages, "system prompt")
	if err != nil {
		t.Fatalf("compact messages: %v", err)
	}
	if len(compact) == 0 {
		t.Fatal("expected compacted messages")
	}
	if compact[0].Role != protocol.RoleUser {
		t.Fatalf("expected summary message role user, got %s", compact[0].Role)
	}
	if compact[0].Metadata == nil || compact[0].Metadata.Kind != protocol.KindSummary {
		t.Fatalf("expected structured summary metadata, got %+v", compact[0].Metadata)
	}
	if compact[0].Metadata.Transcript == "" {
		t.Fatal("expected transcript filename in summary metadata")
	}
	if text := protocol.MessageText(compact[0]); !strings.Contains(text, "## Session Compaction Summary") || !strings.Contains(text, "## Goal") {
		t.Fatalf("expected structured summary sections, got %q", text)
	}
	if _, err := os.Stat(filepath.Join(dir, compact[0].Metadata.Transcript)); err != nil {
		t.Fatalf("expected transcript file to exist: %v", err)
	}
}

func TestCompactPreservesRecentAssistantOutputs(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "实现一个分页组件"),
		protocol.NewTextMessage(protocol.RoleAssistant, "已完成分页组件：支持上一页/下一页/页码跳转，代码在 ui/web/src/components/Pager.tsx，并补充了单测。"),
		protocol.NewTextMessage(protocol.RoleUser, "再补充键盘左右键翻页"),
		protocol.NewTextMessage(protocol.RoleAssistant, "已为 Pager 接入键盘左右键翻页：监听 keydown，ArrowLeft/ArrowRight 触发页码切换，同时阻止默认滚动行为。"),
	}

	compact, err := compressor.Compact(messages, "system prompt")
	if err != nil {
		t.Fatalf("compact messages: %v", err)
	}
	text := protocol.MessageText(compact[0])
	if !strings.Contains(text, "### Recent Assistant Messages") {
		t.Fatalf("expected dedicated recent-assistant section, got:\n%s", text)
	}
	if !strings.Contains(text, "Pager.tsx") || !strings.Contains(text, "ArrowLeft/ArrowRight") {
		t.Fatalf("expected recent assistant output preserved verbatim, got:\n%s", text)
	}
}

func TestCompressorSetKeepRecentControlsRetainedRawMessages(t *testing.T) {
	dir := t.TempDir()
	messages := make([]protocol.Message, 0, 26)
	for i := 0; i < 26; i++ {
		messages = append(messages, protocol.NewTextMessage(protocol.RoleUser, "msg-"+fmt.Sprint(i)))
	}

	compressor := NewCompressor(dir)
	compact, err := compressor.Compact(messages, "system prompt")
	if err != nil {
		t.Fatalf("compact messages: %v", err)
	}
	if len(compact) != 1+defaultKeepRecent {
		t.Fatalf("expected summary + %d recent raw messages, got %d", defaultKeepRecent, len(compact))
	}

	compressor2 := NewCompressor(dir)
	compressor2.SetKeepRecent(5)
	compact2, err := compressor2.Compact(messages, "system prompt")
	if err != nil {
		t.Fatalf("compact messages: %v", err)
	}
	if len(compact2) != 6 {
		t.Fatalf("expected summary + 5 recent raw messages after SetKeepRecent(5), got %d", len(compact2))
	}
}

func TestCompactPreservesRecentUserInputsWithDedicatedBudget(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	formatted := "请保留这个格式：\n\n- 第一项\n- 第二项\n\n```go\nfmt.Println(\"ok\")\n```"
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, formatted),
		protocol.NewTextMessage(protocol.RoleAssistant, "收到，我会保留格式。"),
		protocol.NewTextMessage(protocol.RoleUser, "继续，但不要丢掉上面的列表和代码块。"),
	}

	compact, err := compressor.Compact(messages, "system prompt")
	if err != nil {
		t.Fatalf("compact messages: %v", err)
	}
	text := protocol.MessageText(compact[0])
	if !strings.Contains(text, "### Recent User Messages") {
		t.Fatalf("expected dedicated recent-user section, got:\n%s", text)
	}
	if !strings.Contains(text, "第一项") || !strings.Contains(text, "fmt.Println(\"ok\")") {
		t.Fatalf("expected recent user formatting to be preserved, got:\n%s", text)
	}
}

func TestRuleBasedSessionSummarizerWrapsCompressor(t *testing.T) {
	dir := t.TempDir()
	summarizer := NewRuleBasedSessionSummarizer(NewCompressor(dir))
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "please keep context compact"),
		protocol.NewTextMessage(protocol.RoleAssistant, "working on it"),
	}

	result, err := summarizer.SummarizeSession(context.Background(), SessionSummaryRequest{
		System:               "system prompt",
		History:              messages,
		TokenBreakdown:       map[string]int{"history": 20, "total": 40},
		RecentUserMessages:   []string{"please keep context compact"},
		ContinuationSnapshot: "Current blocker: pending LongTask validation for US-001.",
	})
	if err != nil {
		t.Fatalf("summarize session: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Fatal("expected summary messages")
	}
	if len(result.TranscriptRefs) != 1 {
		t.Fatalf("expected transcript refs, got %+v", result.TranscriptRefs)
	}
	if result.Messages[0].Metadata == nil || result.Messages[0].Metadata.Kind != protocol.KindSummary {
		t.Fatalf("expected summary message metadata, got %+v", result.Messages[0].Metadata)
	}
	if text := protocol.MessageText(result.Messages[0]); !strings.Contains(text, "Pinned continuation state") || !strings.Contains(text, "pending LongTask validation") {
		t.Fatalf("expected pinned continuation state in rule-based summary, got:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(dir, result.TranscriptRefs[0])); err != nil {
		t.Fatalf("expected transcript file to exist: %v", err)
	}
}

func TestLLMSessionSummarizerUsesModelSummary(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	caller := &summaryCaller{
		resp: &protocol.Response{Content: []protocol.Block{
			protocol.TextBlock("Current goal: finish P2.1 model-backed compaction.\nOpen item: run focused tests."),
		}},
	}
	summarizer := NewLLMSessionSummarizer(caller, "summary-model", 4096, compressor, NewRuleBasedSessionSummarizer(compressor))
	history := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Implement P2.1 with model summarization."),
		protocol.NewTextMessage(protocol.RoleAssistant, "I will add the summarizer and tests."),
	}

	result, err := summarizer.SummarizeSession(context.Background(), SessionSummaryRequest{
		System:               "system prompt",
		History:              history,
		TokenBreakdown:       map[string]int{"history": 100, "total": 160},
		RecentUserMessages:   []string{"Implement P2.1 with model summarization."},
		ContinuationSnapshot: "Current validation command: go test ./internal/core/compress.",
	})
	if err != nil {
		t.Fatalf("summarize session: %v", err)
	}
	if len(caller.requests) != 1 {
		t.Fatalf("expected one model request, got %d", len(caller.requests))
	}
	req := caller.requests[0]
	if req.Model != "summary-model" || req.MaxTokens != 4096 {
		t.Fatalf("unexpected model request: model=%q max_tokens=%d", req.Model, req.MaxTokens)
	}
	if len(req.Tools) != 0 {
		t.Fatalf("expected no tools for summary request, got %+v", req.Tools)
	}
	// Prefix-aligned request shape: the conversation's own system prompt, the
	// history verbatim, and ONE trailing summary-instruction user message.
	if req.System != "system prompt" {
		t.Fatalf("expected the conversation's own system prompt, got %q", req.System)
	}
	if len(req.Messages) != len(history)+1 {
		t.Fatalf("expected history + instruction messages, got %d", len(req.Messages))
	}
	instruction := protocol.MessageText(protocol.Message{Role: req.Messages[len(req.Messages)-1].Role, Content: req.Messages[len(req.Messages)-1].Content})
	for _, want := range []string{
		"session compaction engine",
		"<continuation-state>",
		"go test ./internal/core/compress",
		"<recent-user-messages>",
		"Implement P2.1 with model summarization.",
	} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("expected summary instruction to contain %q, got:\n%s", want, instruction)
		}
	}
	if len(result.Messages) == 0 || result.Messages[0].Metadata == nil || result.Messages[0].Metadata.Kind != protocol.KindSummary {
		t.Fatalf("expected summary message first, got %+v", result.Messages)
	}
	text := protocol.MessageText(result.Messages[0])
	if !strings.Contains(text, "Model-assisted compaction summary") || !strings.Contains(text, "finish P2.1 model-backed compaction") {
		t.Fatalf("expected model summary text, got:\n%s", text)
	}
	if !strings.Contains(text, "Pinned continuation state") || !strings.Contains(text, "go test ./internal/core/compress") {
		t.Fatalf("expected pinned continuation state in model summary message, got:\n%s", text)
	}
	if len(result.TranscriptRefs) != 1 {
		t.Fatalf("expected transcript refs, got %+v", result.TranscriptRefs)
	}
	if _, err := os.Stat(filepath.Join(dir, result.TranscriptRefs[0])); err != nil {
		t.Fatalf("expected transcript file to exist: %v", err)
	}
}

func TestLLMSessionSummarizerFallsBackOnModelFailure(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	caller := &summaryCaller{err: errors.New("provider unavailable")}
	summarizer := NewLLMSessionSummarizer(caller, "summary-model", 512, compressor, NewRuleBasedSessionSummarizer(compressor))
	history := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Keep the roadmap context."),
		protocol.NewTextMessage(protocol.RoleAssistant, "P2.0 baseline is done."),
	}

	result, err := summarizer.SummarizeSession(context.Background(), SessionSummaryRequest{
		System:  "system prompt",
		History: history,
	})
	if err != nil {
		t.Fatalf("summarize session: %v", err)
	}
	if len(caller.requests) != llmSummaryAttempts {
		t.Fatalf("expected retry attempts, got %d", len(caller.requests))
	}
	if len(result.Diagnostics) != llmSummaryAttempts {
		t.Fatalf("expected diagnostics for failed attempts, got %+v", result.Diagnostics)
	}
	if result.RecoveryHint != llmSummaryFailureRecovery {
		t.Fatalf("expected fallback recovery hint, got %q", result.RecoveryHint)
	}
	text := protocol.MessageText(result.Messages[0])
	if !strings.Contains(text, "## Session Compaction Summary") {
		t.Fatalf("expected rule-based fallback summary, got:\n%s", text)
	}
}

func TestLLMSessionSummarizerOmitsLargeToolResultFromModelInput(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	caller := &summaryCaller{
		resp: &protocol.Response{Content: []protocol.Block{
			protocol.TextBlock("Tool result was large; continue from transcript reference."),
		}},
	}
	summarizer := NewLLMSessionSummarizer(caller, "summary-model", 1024, compressor, NewRuleBasedSessionSummarizer(compressor))
	summarizer.SetPruneConfig(8192, 4096, 1024)
	secretMarker := "SECRET_RAW_TOOL_RESULT_SHOULD_NOT_REACH_SUMMARY_MODEL"
	large := strings.Repeat("head data\n", 5000) + secretMarker + strings.Repeat("\ntail data", 5000)
	history := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Inspect this long tool output."),
		protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"cmd": "cat huge.log"})),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-1", large)),
	}

	result, err := summarizer.SummarizeSession(context.Background(), SessionSummaryRequest{
		System:  "system prompt",
		History: history,
	})
	if err != nil {
		t.Fatalf("summarize session: %v", err)
	}
	if len(caller.requests) != 1 {
		t.Fatalf("expected one model request, got %d", len(caller.requests))
	}
	data, err := json.Marshal(caller.requests[0].Messages)
	if err != nil {
		t.Fatalf("marshal request messages: %v", err)
	}
	requestText := string(data)
	if strings.Contains(requestText, secretMarker) {
		t.Fatalf("expected large raw tool result marker to be omitted from model request")
	}
	if !strings.Contains(requestText, "tool_result_truncated") || !strings.Contains(requestText, "transcript_") {
		t.Fatalf("expected large tool result reference in model request, got %s", requestText)
	}
	if !strings.Contains(protocol.MessageText(result.Messages[0]), "Tool result was large") {
		t.Fatalf("expected model summary result, got:\n%s", protocol.MessageText(result.Messages[0]))
	}
}

func TestCompactSemanticSummaryPreservesLongTaskState(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Rewrite the Web UI with Ant Design, keep React 19, preserve backend JSON schemas, and fix the API prefix routing bug."),
		protocol.NewTextMessage(protocol.RoleAssistant, "Implemented async turn runtime in internal/services/backend/backend.go and updated ui/web/src/features/chat/ChatPage.tsx. Remaining: semantic compaction and history indexing."),
		protocol.NewMessage(protocol.RoleAssistant,
			protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{
				"cmd": "go test ./internal/runtime/webui ./internal/runtime/httpapi && cd ui/web && pnpm build",
			}),
		),
		protocol.NewMessage(protocol.RoleUser,
			protocol.ToolResultBlock("tool-1", "PASS go test ./internal/runtime/webui ./internal/runtime/httpapi\npnpm build completed"),
		),
		protocol.NewTextMessage(protocol.RoleUser, "好，执行下一步"),
	}

	compact, err := compressor.Compact(messages, "system prompt")
	if err != nil {
		t.Fatalf("compact messages: %v", err)
	}
	if len(compact) == 0 {
		t.Fatal("expected compacted messages")
	}
	text := protocol.MessageText(compact[0])
	for _, want := range []string{
		"Rewrite the Web UI with Ant Design",
		"keep React 19",
		"internal/services/backend/backend.go",
		"ui/web/src/features/chat/ChatPage.tsx",
		"go test ./internal/runtime/webui ./internal/runtime/httpapi",
		"semantic compaction",
		"## Session Compaction Summary",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected summary to contain %q, got:\n%s", want, text)
		}
	}
	if strings.Contains(text, "好，执行下一步") && strings.Contains(text, "### Recent User Messages") {
		// low-signal ack should appear in Recent User Messages (verbatim section)
		// but NOT in Goal or Next Steps
	}
}

// TestCompactKeepsLargeRecentToolResultsVerbatim verifies the retained tail is
// byte-identical after compaction: large tool results in the recent span are no
// longer stubbed into a transcript reference (the DSH-aligned retention
// design), and the full raw history is still available in the transcript.
func TestCompactKeepsLargeRecentToolResultsVerbatim(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	large := strings.Repeat("status payload with transcript noise\n", 2000)
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "start"),
		protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("tool-1", "task", map[string]interface{}{
			"action": "status",
			"job_id": "subagent_1",
		})),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-1", large)),
	}

	compact, err := compressor.Compact(messages, "system prompt")
	if err != nil {
		t.Fatalf("compact messages: %v", err)
	}
	if len(compact) < 2 {
		t.Fatalf("expected summary plus recent messages, got %+v", compact)
	}
	last := compact[len(compact)-1]
	if len(last.Content) != 1 || last.Content[0].Type != protocol.BlockToolResult {
		t.Fatalf("expected recent tool result block, got %+v", last.Content)
	}
	if last.Content[0].Content != large {
		t.Fatalf("expected retained tool result verbatim (no truncation stub)")
	}
	data, err := os.ReadFile(filepath.Join(dir, compact[0].Metadata.Transcript))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if !strings.Contains(string(data), "status payload with transcript noise") {
		t.Fatalf("expected full transcript to keep raw tool result")
	}
}

func TestCountTokensTreatsUnicodeMoreConservatively(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "empty", text: "", want: 0},
		{name: "ascii", text: "abcdefgh", want: 2},
		{name: "single ascii", text: "a", want: 1},
		{name: "cjk", text: "你好世界", want: 2},
		{name: "mixed", text: "hello世界", want: 3},
	}

	for _, tc := range tests {
		if got := CountTokens(tc.text); got != tc.want {
			t.Fatalf("%s: expected %d tokens, got %d", tc.name, tc.want, got)
		}
	}
}

// TestCompactKeepsAgentOutputsBeyondMetadataBudget guards against the old
// behaviour where the whole summary (metadata + verbatim user/assistant
// sections) was truncated to 6500 runes, silently dropping the tail — which is
// where recent assistant outputs lived.
func TestCompactKeepsAgentOutputsBeyondMetadataBudget(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)

	var messages []protocol.Message
	for i := 0; i < 12; i++ {
		// Each round's assistant output is large enough that the old 6500-rune
		// summary cap would have truncated the whole verbatim section away.
		userText := fmt.Sprintf("user instruction round %d with some detail to retain %s", i, strings.Repeat("user detail text. ", 12))
		assistantText := fmt.Sprintf("assistant output round %d summarizing what was done and why %s", i, strings.Repeat("assistant detail text. ", 62))
		messages = append(messages, protocol.NewTextMessage(protocol.RoleUser, userText))
		messages = append(messages, protocol.NewTextMessage(protocol.RoleAssistant, assistantText))
	}

	compact, err := compressor.Compact(messages, "system prompt")
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(compact) == 0 {
		t.Fatalf("expected compacted messages")
	}
	summary := protocol.MessageText(compact[0])
	// The newest assistant output must survive verbatim (not truncated to the
	// tiny per-item summary cap, not cut by a total summary cap).
	if !strings.Contains(summary, "assistant output round 11") {
		t.Fatalf("expected newest assistant output to survive compaction, summary head: %q", truncateForTest(summary, 400))
	}
	if !strings.Contains(summary, "user instruction round 11") {
		t.Fatalf("expected newest user instruction to survive compaction, summary head: %q", truncateForTest(summary, 400))
	}
	// Several older rounds' outputs should be preserved too (the old code kept
	// at most 6 assistant outputs and then truncated the tail away entirely).
	olderRounds := 0
	for i := 9; i >= 6; i-- {
		if strings.Contains(summary, fmt.Sprintf("assistant output round %d", i)) {
			olderRounds++
		}
	}
	if olderRounds < 2 {
		t.Fatalf("expected at least 2 of rounds 6-9 to survive, got %d", olderRounds)
	}
	if utf8.RuneCountInString(summary) < 10000 {
		t.Fatalf("expected a substantially larger summary that preserves round-by-round input/output, got %d runes", utf8.RuneCountInString(summary))
	}
}

// TestCompactKeepsMediumRecentToolResultsVerbatim verifies tool results in the
// retained span survive compaction byte-for-byte: the DSH-aligned retention
// design no longer trims them to previews, so the model keeps the real tool
// output instead of having to re-run the tool.
func TestCompactKeepsMediumRecentToolResultsVerbatim(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	medium := strings.Repeat("grep output line with file content\n", 400) // ~15KB
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "search for x"),
		protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("tool-1", "grep", map[string]interface{}{"pattern": "x"})),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-1", medium)),
		protocol.NewTextMessage(protocol.RoleAssistant, "found 400 lines"),
	}

	compact, err := compressor.Compact(messages, "system prompt")
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	for _, msg := range compact {
		for _, block := range msg.Content {
			if block.Type == protocol.BlockToolResult {
				if strings.Contains(block.Content, "tool_result_truncated") {
					t.Fatalf("expected tool result retained verbatim, found truncation stub")
				}
				if block.Content != medium {
					t.Fatalf("expected tool result bytes intact")
				}
			}
		}
	}
}

func truncateForTest(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func TestRetentionBoundaryRespectsTokenBudget(t *testing.T) {
	messages := make([]protocol.Message, 0, 8)
	for i := 0; i < 8; i++ {
		// Each message is ~10 tokens of ASCII text.
		messages = append(messages, protocol.NewTextMessage(protocol.RoleUser, strings.Repeat("payload ", 10)))
	}
	// Budget 30 tokens: accumulate from the end (~18 tokens/message) → the
	// boundary lands at the first message that crosses the budget.
	cutoff := retentionBoundary(messages, 30, 20)
	if cutoff != 6 {
		t.Fatalf("expected boundary at 6 (tail ≈ 36 tokens ≥ 30), got %d", cutoff)
	}
	// A budget beyond the whole history still compacts at least one message.
	if got := retentionBoundary(messages, 1<<30, 20); got != 1 {
		t.Fatalf("expected minimal boundary 1 for oversized budget, got %d", got)
	}
	// Message-count fallback when retain tokens are unset.
	if got := retentionBoundary(messages, 0, 4); got != len(messages)-4 {
		t.Fatalf("expected fallback boundary len-4, got %d", got)
	}
}

func TestRetentionBoundaryNeverSplitsToolPair(t *testing.T) {
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, strings.Repeat("old context ", 60)), // ~180 tokens
		protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "x"})),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-1", strings.Repeat("result payload ", 40))),
		protocol.NewTextMessage(protocol.RoleUser, "final"),
	}
	// Small budget would naturally cut after message 2 (tool_use) — the pair
	// must be pulled into the verbatim tail instead.
	cutoff := retentionBoundary(messages, 20, 20)
	if cutoff > 1 {
		t.Fatalf("expected boundary pulled before the tool_use message, got %d", cutoff)
	}
	tail := messages[cutoff:]
	if len(tail) != 3 {
		t.Fatalf("expected tail to hold tool_use+result+final, got %d messages", len(tail))
	}
}

func TestCompactRetentionTailVerbatim(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	compressor.SetRetainTokens(1000)
	var messages []protocol.Message
	for i := 0; i < 10; i++ {
		messages = append(messages, protocol.NewTextMessage(protocol.RoleUser, fmt.Sprintf("user message %d", i)))
		messages = append(messages, protocol.NewTextMessage(protocol.RoleAssistant, fmt.Sprintf("assistant reply %d", i)))
	}

	compact, err := compressor.Compact(messages, "system prompt")
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(compact) < 2 {
		t.Fatalf("expected summary + retained tail, got %d", len(compact))
	}
	// Every retained message must be byte-identical to its source message.
	recent := messages[len(messages)-(len(compact)-1):]
	for i, msg := range compact[1:] {
		original := recent[i]
		if msg.Role != original.Role || protocol.MessageText(msg) != protocol.MessageText(original) {
			t.Fatalf("retained message %d changed: got %q want %q", i, protocol.MessageText(msg), protocol.MessageText(original))
		}
	}
}

func TestCompactSummaryHasNoTimestamp(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "first"),
		protocol.NewTextMessage(protocol.RoleAssistant, "second"),
	}
	compact, err := compressor.Compact(messages, "system prompt")
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	text := protocol.MessageText(compact[0])
	if strings.Contains(text, "Compressed at") {
		t.Fatalf("expected deterministic summary without timestamp, got %q", text)
	}
}

func TestLLMSessionSummarizerPrefixAlignedWithQuasiStablePrefix(t *testing.T) {
	dir := t.TempDir()
	compressor := NewCompressor(dir)
	caller := &summaryCaller{
		resp: &protocol.Response{Content: []protocol.Block{
			protocol.TextBlock("Goal: keep working."),
		}},
	}
	summarizer := NewLLMSessionSummarizer(caller, "summary-model", 1024, compressor, NewRuleBasedSessionSummarizer(compressor))
	history := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "first"),
		protocol.NewTextMessage(protocol.RoleAssistant, "second"),
	}
	prefix := []protocol.Message{
		protocol.NewEphemeralTextMessage(protocol.KindMemory, "memory index"),
	}

	if _, err := summarizer.SummarizeSession(context.Background(), SessionSummaryRequest{
		System:  "system prompt",
		Prefix:  prefix,
		History: history,
	}); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(caller.requests) != 1 {
		t.Fatalf("expected one request, got %d", len(caller.requests))
	}
	req := caller.requests[0]
	// [prefix..., history..., instruction] — the prefix must sit verbatim at
	// the head so the call reuses the conversation's warm prefix cache.
	if len(req.Messages) != len(prefix)+len(history)+1 {
		t.Fatalf("expected prefix+history+instruction messages, got %d", len(req.Messages))
	}
	head := protocol.MessageText(protocol.Message{Role: req.Messages[0].Role, Content: req.Messages[0].Content})
	if !strings.Contains(head, "memory index") {
		t.Fatalf("expected quasi-stable prefix first, got %q", head)
	}
	second := protocol.MessageText(protocol.Message{Role: req.Messages[1].Role, Content: req.Messages[1].Content})
	if !strings.Contains(second, "first") {
		t.Fatalf("expected history to follow the prefix verbatim, got %q", second)
	}
}
