package agent

import (
	"strings"
	"testing"
)

// Phase 2.3 — 上下文预算管理 tests: completed-subtask summaries and
// dependency handoff text must respect token budgets (not just char limits),
// and historical subtasks must never be injected into the active context at
// full size.

func TestTruncateTextToTokenBudgetKeepsUnderBudget(t *testing.T) {
	longText := strings.Repeat("alpha beta gamma delta ", 500) // ~10k chars ASCII
	budget := 400
	out := truncateTextToTokenBudget(longText, budget)
	if out == "" {
		t.Fatal("expected non-empty truncation")
	}
	if compressCountTokensForText(out) > budget {
		t.Fatalf("truncation exceeded budget: %d > %d", compressCountTokensForText(out), budget)
	}
	// head-biased: the head of the original text is preserved verbatim
	if !strings.HasPrefix(out, "alpha beta gamma delta ") {
		t.Fatalf("expected head preserved, got %q", out[:minInt(len(out), 40)])
	}
	// marker indicates dropped content
	if !strings.Contains(out, "[truncated]") {
		t.Fatal("expected truncation marker")
	}
	// tail is retained when budget allows (original ends with "delta" after TrimSpace)
	if !strings.HasSuffix(out, "delta") {
		t.Fatalf("expected tail preserved, got %q", out)
	}
}

func TestTruncateTextToTokenBudgetShortTextUntouched(t *testing.T) {
	text := "Verdict: pass\nshort result"
	out := truncateTextToTokenBudget(text, 2000)
	if out != text {
		t.Fatalf("expected short text untouched, got %q", out)
	}
}

func TestWorkflowHandoffSummaryCapsCJKUnderTokenBudget(t *testing.T) {
	// A CJK-heavy result: ~2 chars per token, so 20k CJK chars ≈ 10k tokens
	// far above the 2000-token summary budget.
	cjk := strings.Repeat("这是一个非常长的中文子任务结果摘要内容", 800)
	summary := workflowHandoffSummary("Verdict: pass\n"+cjk, "")
	if compressCountTokensForText(summary) > workflowHandoffSummaryTokenBudget {
		t.Fatalf("handoff summary exceeded token budget: %d > %d", compressCountTokensForText(summary), workflowHandoffSummaryTokenBudget)
	}
	// verdict stays in the summary head
	if !strings.Contains(summary, "Verdict: pass") {
		t.Fatalf("expected verdict preserved in summary head, got %q", summary[:minInt(len(summary), 60)])
	}
}

func TestWorkflowHandoffSummaryPreservesShortASCII(t *testing.T) {
	text := "Verdict: pass\nimplemented feature A and B"
	summary := workflowHandoffSummary(text, "")
	if summary != text {
		t.Fatalf("expected short summary untouched, got %q", summary)
	}
}

func TestAssembleTruncatedHandoffsRespectsByteCeiling(t *testing.T) {
	chunks := []string{
		"- node: w1\n  summary: " + strings.Repeat("x", 4000) + "\n",
		"- node: w2\n  summary: " + strings.Repeat("y", 4000) + "\n",
	}
	out := assembleTruncatedHandoffs(chunks, 8000)
	if len(out) > 8000 {
		t.Fatalf("assembled handoffs exceeded byte ceiling: %d", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Fatal("expected truncation marker when byte ceiling hit")
	}
	// first chunk is fully kept (fits), second is cut
	if !strings.Contains(out, "node: w1") {
		t.Fatal("expected first chunk kept")
	}
}

func TestAssembleTruncatedHandoffsEmptyInput(t *testing.T) {
	if out := assembleTruncatedHandoffs(nil, 8000); out != "" {
		t.Fatalf("expected empty output, got %q", out)
	}
}
