package history

import "testing"

func TestEvaluateHistoryRecallExplicitCueAllowsTool(t *testing.T) {
	policy := DefaultHistorySearchPolicy()
	got := EvaluateHistoryRecall(policy, HistoryRecallEvaluationInput{
		Query:                    "你回顾一下刚才我们说过什么",
		CurrentContextSufficient: false,
	})
	if !got.AllowTool {
		t.Fatalf("expected explicit cue to allow tool, got %+v", got)
	}
	if !got.ExplicitRequest {
		t.Fatalf("expected explicit request, got %+v", got)
	}
	if got.Score < policy.Auto.MinScore {
		t.Fatalf("expected score >= min score, got %+v", got)
	}
}

func TestEvaluateHistoryRecallDoesNotAutoExposeAllArchives(t *testing.T) {
	policy := DefaultHistorySearchPolicy()
	policy.Auto.DefaultScope = HistorySearchScopeAllArchives
	got := EvaluateHistoryRecall(policy, HistoryRecallEvaluationInput{
		Query:                    "remember the constraint we settled on",
		CurrentContextSufficient: false,
	})
	if !got.Automatic {
		t.Fatalf("expected automatic history recall, got %+v", got)
	}
	if got.RecommendedScope == HistorySearchScopeAllArchives {
		t.Fatalf("expected automatic recall not to recommend all_archives, got %+v", got)
	}
}

func TestEvaluateHistoryRecallBlockedSessionSourcePreventsAutomatic(t *testing.T) {
	policy := DefaultHistorySearchPolicy()
	got := EvaluateHistoryRecall(policy, HistoryRecallEvaluationInput{
		Query:                    "remember the constraint we settled on",
		SessionSource:            "cron",
		CurrentContextSufficient: false,
	})
	if got.Automatic {
		t.Fatalf("expected blocked session source to prevent automatic recall, got %+v", got)
	}
	if got.AllowTool {
		t.Fatalf("expected non-explicit blocked automatic recall not to allow tool, got %+v", got)
	}
}

func TestEvaluateHistoryRecallRespectsPerTurnLimit(t *testing.T) {
	policy := DefaultHistorySearchPolicy()
	got := EvaluateHistoryRecall(policy, HistoryRecallEvaluationInput{
		Query:                    "remember the constraint we settled on",
		CurrentContextSufficient: false,
		AlreadyUsedThisTurn:      1,
	})
	if got.Automatic {
		t.Fatalf("expected turn limit to block automatic recall, got %+v", got)
	}
	if got.AllowTool {
		t.Fatalf("expected non-explicit recall at turn limit not to allow tool, got %+v", got)
	}
}
