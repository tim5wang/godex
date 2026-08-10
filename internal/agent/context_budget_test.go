package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tim5wang/godex/internal/core/protocol"
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

func TestAssembleTruncatedHandoffsCJKRuneSafe(t *testing.T) {
	// A chunk of multi-byte CJK runes cut at a byte ceiling must never
	// split a rune in half (which would produce invalid UTF-8).
	cjk := strings.Repeat("这是一个非常长的中文子任务结果摘要内容", 400) // ~2000 runes, 3 bytes each
	out := assembleTruncatedHandoffs([]string{cjk}, 8000)
	if len(out) > 8000 {
		t.Fatalf("assembled handoffs exceeded byte ceiling: %d", len(out))
	}
	if !utf8.ValidString(out) {
		t.Fatal("assembled handoffs produced invalid UTF-8 (rune split)")
	}
	if !strings.Contains(out, "truncated") {
		t.Fatal("expected truncation marker when byte ceiling hit")
	}
}

func TestRoleContextBudgetTokens(t *testing.T) {
	cases := []struct {
		roleID, agentType string
		want              int
	}{
		{"orchestrator", "", roleContextBudgetOrchestrator},
		{"worker", "", roleContextBudgetWorker},
		{"reviewer", "", roleContextBudgetReviewer},
		{"researcher", "", roleContextBudgetResearcher},
		{"", "research-agent", roleContextBudgetResearcher},
		{"", "general-purpose", defaultRoleContextBudget},
		{"", "", defaultRoleContextBudget},
	}
	for _, tc := range cases {
		if got := roleContextBudgetTokens(tc.roleID, tc.agentType); got != tc.want {
			t.Fatalf("roleContextBudgetTokens(%q, %q) = %d, want %d", tc.roleID, tc.agentType, got, tc.want)
		}
	}
}

func TestSubagentStartWithOptionsResolvesContextBudget(t *testing.T) {
	store := newSubagentJobStore(filepath.Join(t.TempDir(), "subagents"))
	job, err := store.StartWithOptions(subagentStartOptions{
		AgentType:   "general-purpose",
		RoleID:      "researcher",
		RoleName:    "researcher",
		Prompt:      "research the topic",
		ToolNames:   []string{"web_search"},
		MaxTurns:    1,
		WorkerID:    localGoDexWorkerID,
		SandboxID:   "sandbox:local:test",
		BasePrompt:  "base",
		ParentID:    "turn-1",
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	if job.ContextBudget != roleContextBudgetResearcher {
		t.Fatalf("expected researcher context budget %d, got %d", roleContextBudgetResearcher, job.ContextBudget)
	}
}

func TestSubagentStartWithOptionsHonorsExplicitContextBudget(t *testing.T) {
	store := newSubagentJobStore(filepath.Join(t.TempDir(), "subagents"))
	job, err := store.StartWithOptions(subagentStartOptions{
		AgentType:     "general-purpose",
		Prompt:        "do the work",
		ToolNames:     []string{"todo_read"},
		MaxTurns:      1,
		WorkerID:      localGoDexWorkerID,
		SandboxID:     "sandbox:local:test",
		BasePrompt:    "base",
		ParentID:      "turn-1",
		ContextBudget: 42,
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	if job.ContextBudget != 42 {
		t.Fatalf("expected explicit context budget 42, got %d", job.ContextBudget)
	}
}

func TestMaybeCompactSubagentMessagesOverBudget(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	store := newSubagentJobStore(filepath.Join(t.TempDir(), "subagents"))
	job, err := store.StartWithOptions(subagentStartOptions{
		AgentType:     "general-purpose",
		Prompt:        "work",
		ToolNames:     []string{"todo_read"},
		MaxTurns:      1,
		WorkerID:      localGoDexWorkerID,
		SandboxID:     "sandbox:local:test",
		BasePrompt:    "base",
		ParentID:      "turn-1",
		ContextBudget: 1000,
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	// Build a message history well over the budget.
	big := strings.Repeat("tool output line\n", 3000) // ~45k chars
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "start"),
		protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-1", big)),
		protocol.NewTextMessage(protocol.RoleUser, "continue"),
	}
	if estimateMessages(messages) <= 1000 {
		t.Fatalf("test messages too small: %d tokens", estimateMessages(messages))
	}
	out := a.maybeCompactSubagentMessages(context.Background(), job, messages, subagentEventTarget{})
	if len(out) == 0 {
		t.Fatal("expected non-empty compacted messages")
	}
	if estimateMessages(out) >= estimateMessages(messages) {
		t.Fatalf("expected compaction to reduce tokens: before=%d after=%d", estimateMessages(messages), estimateMessages(out))
	}
	// The compacted history is checkpointed and progress recorded.
	persisted, err := store.Get(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if estimateMessages(persisted.Messages) >= estimateMessages(messages) {
		t.Fatalf("expected persisted messages compacted, got %d tokens", estimateMessages(persisted.Messages))
	}
}

func TestMaybeCompactSubagentMessagesUnderBudgetUnchanged(t *testing.T) {
	a := newTestAgent(t, 4096)
	store := newSubagentJobStore(filepath.Join(t.TempDir(), "subagents"))
	job, err := store.StartWithOptions(subagentStartOptions{
		AgentType:     "general-purpose",
		Prompt:        "work",
		ToolNames:     []string{"todo_read"},
		MaxTurns:      1,
		WorkerID:      localGoDexWorkerID,
		SandboxID:     "sandbox:local:test",
		BasePrompt:    "base",
		ParentID:      "turn-1",
		ContextBudget: 100000,
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "short prompt"),
	}
	out := a.maybeCompactSubagentMessages(context.Background(), job, messages, subagentEventTarget{})
	if len(out) != len(messages) || protocol.MessageText(out[0]) != "short prompt" {
		t.Fatalf("expected messages unchanged under budget, got %+v", out)
	}
}
