package tools

import "github.com/tim5wang/godex/internal/domain/history"

type HistorySearchRequest = history.SearchRequest
type HistorySearchSnippet = history.SearchSnippet
type HistorySearchResult = history.SearchResult
type HistorySearchCurrent = history.Current
type HistorySearchRuntime = history.Runtime

type HistorySearchPolicy = history.HistorySearchPolicy
type HistorySearchAutoPolicy = history.HistorySearchAutoPolicy
type HistorySearchCuePolicy = history.HistorySearchCuePolicy
type HistorySearchBlockPolicy = history.HistorySearchBlockPolicy
type HistoryRecallEvaluationInput = history.HistoryRecallEvaluationInput
type HistoryRecallEvaluation = history.HistoryRecallEvaluation

const (
	HistorySearchScopeCurrentSession = history.HistorySearchScopeCurrentSession
	HistorySearchScopeSessionArchive = history.HistorySearchScopeSessionArchive
	HistorySearchScopeAllArchives    = history.HistorySearchScopeAllArchives
)

func DefaultHistorySearchPolicy() HistorySearchPolicy {
	return history.DefaultHistorySearchPolicy()
}

func EvaluateHistoryRecall(policy HistorySearchPolicy, input HistoryRecallEvaluationInput) HistoryRecallEvaluation {
	return history.EvaluateHistoryRecall(policy, input)
}
