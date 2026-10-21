package httpapi

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/services/backend"
)

// snapshotWithMessages builds the minimal snapshot the projection needs. Keeping it
// here (rather than driving a whole session) keeps these tests about the shaping,
// which is where the behaviour lives.
func snapshotWithMessages(t *testing.T, messages ...protocol.Message) backend.Snapshot {
	t.Helper()
	return backend.Snapshot{SessionID: "sess-test", Messages: messages}
}

// TestWatchReplySummaryCondensesMarkdown covers the shaping that makes a reply
// readable on a wrist: markers dropped, code removed, one paragraph, sentence cut.
func TestWatchReplySummaryCondensesMarkdown(t *testing.T) {
	reply := "# 完成\n\n已 **重构** `auth` 中间件，并跑通了测试。\n\n" +
		"```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```\n\n" +
		"## 细节\n\n- 修改了 middleware.go\n- 更新了 README\n"

	got := watchReplySummary(reply)
	if got == "" {
		t.Fatal("summary must not be empty for a real reply")
	}
	// Code must not appear: a wrist cannot show it, and a script would eat the
	// whole budget while hiding the sentence that explains it.
	if strings.Contains(got, "fmt.Println") || strings.Contains(got, "func main") {
		t.Fatalf("code leaked into the summary: %q", got)
	}
	if strings.Contains(got, "**") || strings.Contains(got, "`") || strings.Contains(got, "#") {
		t.Fatalf("markdown markers leaked into the summary: %q", got)
	}
	// It must be the prose, in one paragraph (no newlines: the watch's two lines
	// should hold words, not indentation).
	if !strings.Contains(got, "重构") {
		t.Fatalf("summary lost the actual content: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("summary must be collapsed to one paragraph: %q", got)
	}
}

// TestWatchReplySummaryIsBounded pins the size contract: the endpoint must never
// ship a paragraph to a 454px screen.
func TestWatchReplySummaryIsBounded(t *testing.T) {
	long := strings.Repeat("这是一段很长的说明文字，用来测试截断行为。", 30)
	got := watchReplySummary(long)
	if n := len([]rune(got)); n > maxWatchReplyRunes+1 {
		t.Fatalf("summary too long: %d runes (%q)", n, got)
	}
	if got == "" {
		t.Fatal("long input must still produce a summary")
	}
}

// TestWatchReplySummaryCutsAtSentenceWhenPossible keeps the cut readable: the
// point of a shortened reply is that it reads as a finished thought.
func TestWatchReplySummaryCutsAtSentenceWhenPossible(t *testing.T) {
	reply := strings.Repeat("第一句话已经完成了。", 4) + strings.Repeat("后面的补充说明", 20)
	got := watchReplySummary(reply)
	if strings.HasSuffix(got, "后面的补充说明") {
		t.Fatalf("expected a cut at a sentence boundary, got %q", got)
	}
	if !strings.HasSuffix(got, "。") && !strings.HasSuffix(got, "…") {
		t.Fatalf("summary should end at a sentence or the ellipsis cut, got %q", got)
	}
}

// TestWatchReplySummaryHandlesCodeOnlyReply covers the case that would otherwise
// look like the agent said nothing: the reply was real, it just cannot be read on
// a wrist, so the summary says so instead of being empty.
func TestWatchReplySummaryHandlesCodeOnlyReply(t *testing.T) {
	code := "```bash\ngo build ./...\ngo test ./...\n```"
	got := watchReplySummary(code)
	if got == "" {
		t.Fatal("a code-only reply must not produce an empty summary")
	}
	if got != watchReplyCodeOnlyNotice {
		t.Fatalf("summary = %q, want the code-only notice", got)
	}

	// A genuinely empty reply stays empty: there is nothing to point at.
	if got := watchReplySummary(""); got != "" {
		t.Fatalf("empty reply should stay empty, got %q", got)
	}
	if got := watchReplySummary("   \n\n  "); got != "" {
		t.Fatalf("whitespace-only reply should stay empty, got %q", got)
	}
}

// TestWatchReplySummaryKeepsLinkLabels drops targets but keeps labels: a URL is not
// actionable on a wrist, the label is the readable part.
func TestWatchReplySummaryKeepsLinkLabels(t *testing.T) {
	got := watchReplySummary("详见 [部署文档](https://example.com/deploy/very/long/path) 的说明。")
	if strings.Contains(got, "https://") {
		t.Fatalf("URL should be dropped, got %q", got)
	}
	if !strings.Contains(got, "部署文档") {
		t.Fatalf("link label should survive, got %q", got)
	}
}

// TestWatchReplySummaryTightensSpaceLeftByInlineMarkers covers a defect the real
// end-to-end run exposed: dropping an inline marker leaves a space with CJK on both
// sides, so "已 **重构** auth" shaped to "已 重构 auth", which reads as a typo.
// The space at the Latin/CJK boundary ("auth 中间件") is legitimate and stays.
func TestWatchReplySummaryTightensSpaceLeftByInlineMarkers(t *testing.T) {
	got := watchReplySummary("已 **重构** `auth` 中间件，并跑通了测试。")
	if strings.Contains(got, "已 重构") {
		t.Fatalf("space left between CJK runes: %q", got)
	}
	if !strings.Contains(got, "已重构") {
		t.Fatalf("expected the CJK runes to be joined: %q", got)
	}
	// A separator between Latin and CJK is real and must survive.
	if !strings.Contains(got, "auth 中间件") {
		t.Fatalf("Latin/CJK separator was removed: %q", got)
	}
}

// TestWatchReplySummaryKeepsParagraphBoundaries covers a defect found in the real
// end-to-end run: tightening CJK space AFTER collapsing whitespace made a
// paragraph break indistinguishable from a marker artifact, so "已完成" and
// "已重构…" fused into "已完成已重构…" and "后续建议" ran into "建议补充…".
// Paragraph breaks must stay separated; only spaces left by removing an inline
// marker may be tightened.
func TestWatchReplySummaryKeepsParagraphBoundaries(t *testing.T) {
	reply := "# 已完成\n\n已 **重构** `auth` 中间件。\n\n## 后续建议\n\n- 补充并发测试\n"
	got := watchReplySummary(reply)
	if strings.Contains(got, "已完成已重构") {
		t.Fatalf("heading fused with the next paragraph: %q", got)
	}
	if strings.Contains(got, "后续建议建议") {
		t.Fatalf("heading fused with its list: %q", got)
	}
	// The inline artifact still has to be tightened in the same pass.
	if strings.Contains(got, "已 重构") {
		t.Fatalf("inline marker space was not tightened: %q", got)
	}
}

// TestWatchSessionDetailIncludesReplySummary checks the field is actually wired
// into the projection, which is what the watch reads.
func TestWatchSessionDetailIncludesReplySummary(t *testing.T) {
	detail := watchSessionDetail(snapshotWithMessages(t,
		protocol.Message{Role: protocol.RoleUser, Content: []protocol.Block{protocol.TextBlock("帮我重构")}},
		protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{protocol.TextBlock("已重构 auth 中间件，测试通过。")}},
	))
	summary, _ := detail["reply_summary"].(string)
	if summary == "" {
		t.Fatalf("reply_summary missing from the detail payload: %+v", detail)
	}
	if !strings.Contains(summary, "已重构") {
		t.Fatalf("summary should describe the assistant reply, got %q", summary)
	}
}

// TestWatchSessionDetailSummaryPrefersAssistantText guards against summarising the
// user's instruction: the newest message is often the user's own, and echoing it
// back would tell the user nothing.
func TestWatchSessionDetailSummaryPrefersAssistantText(t *testing.T) {
	detail := watchSessionDetail(snapshotWithMessages(t,
		protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{protocol.TextBlock("第一步已经完成。")}},
		protocol.Message{Role: protocol.RoleUser, Content: []protocol.Block{protocol.TextBlock("那接着做第二步")}},
	))
	summary, _ := detail["reply_summary"].(string)
	if strings.Contains(summary, "接着做第二步") {
		t.Fatalf("summary repeated the user's instruction: %q", summary)
	}
	if !strings.Contains(summary, "第一步") {
		t.Fatalf("summary should carry the assistant's reply, got %q", summary)
	}
}

// TestWatchSessionDetailOmitsSummaryWhenNoAssistantReply keeps the field honest:
// absent means "nothing to summarise", which the watch renders by falling back to
// its existing preview rather than showing an empty block.
func TestWatchSessionDetailOmitsSummaryWhenNoAssistantReply(t *testing.T) {
	detail := watchSessionDetail(snapshotWithMessages(t,
		protocol.Message{Role: protocol.RoleUser, Content: []protocol.Block{protocol.TextBlock("只有我的问题")}},
	))
	if _, ok := detail["reply_summary"]; ok {
		t.Fatalf("no assistant reply should mean no summary, got %+v", detail["reply_summary"])
	}
}
