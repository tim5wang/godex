package retrieval

import "strings"

func QueryIntent(query string) string {
	q := strings.ToLower(query)
	for _, rule := range []struct {
		kind string
		cues []string
	}{
		{"decision", []string{"为什么选择", "为何选择", "决定", "决策", "约定", "why choose", "decision"}},
		{"history", []string{"当时", "之前", "历史", "曾经", "以前", "previously", "history"}},
		{"current", []string{"目前", "现在", "当前", "最新", "还支持", "current status"}},
		{"method", []string{"怎么", "如何", "步骤", "命令", "操作", "修复", "how to", "how do"}},
	} {
		for _, cue := range rule.cues {
			if strings.Contains(q, cue) {
				return rule.kind
			}
		}
	}
	return "fact"
}
func normalizedKind(kind string) string {
	switch kind {
	case "work_method", "workflow", "method":
		return "method"
	case "work_fact", "project", "fact":
		return "fact"
	case "user", "identity", "preference":
		return "preference"
	case "warning", "constraint":
		return "warning"
	default:
		return kind
	}
}

func refineCandidates(query string, docs []Document, scored []scoredDocument, trace *Diagnostics) []scoredDocument {
	maxAnchors, bestKeyword, bestCoverage := 0, 0.0, 0.0
	for _, s := range scored {
		maxAnchors = max(maxAnchors, s.match.Signals.Anchors)
		bestKeyword = max(bestKeyword, s.match.Signals.Keyword)
		bestCoverage = max(bestCoverage, s.match.Signals.Coverage)
	}
	intent := QueryIntent(query)
	out := scored[:0]
	for _, s := range scored {
		signals := &s.match.Signals
		if signals.Relation == 0 && maxAnchors >= 2 && signals.Anchors < maxAnchors {
			trace.reject(docs[s.order], "missing_identifier_facet", "")
			continue
		}
		if signals.Relation == 0 && maxAnchors > 0 && signals.Keyword < bestKeyword*.35 && signals.Coverage < bestCoverage*.8 {
			trace.reject(docs[s.order], "weak_query_facets", "")
			continue
		}
		signals.Intent = intent
		if intent == normalizedKind(docs[s.order].Kind) {
			signals.TypeMatch = 1
		}
		out = append(out, s)
	}
	return out
}
