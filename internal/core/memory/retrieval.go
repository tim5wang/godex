package memory

import (
	"strings"

	"github.com/tim5wang/godex/internal/platform/retrieval"
)

func rankStoredMemories(query string, records []StoredMemory, limit int, diagnostics ...*retrieval.Diagnostics) []StoredMemory {
	if strings.TrimSpace(query) == "" {
		if limit > 0 && len(records) > limit {
			return records[:limit]
		}
		return records
	}
	docs := make([]retrieval.Document, 0, len(records))
	byID := make(map[string]StoredMemory, len(records))
	for _, r := range records {
		kind, group := string(r.Type), "personal"
		version := 0
		for _, tag := range r.Tags {
			if tag == "decision" {
				kind = "decision"
			}
		}
		if r.Origin != nil {
			group = r.Origin.ContextID + "\x00" + r.Origin.Applicability
			version = r.Origin.FactVersion
		}
		docs = append(docs, retrieval.Document{ID: r.ID, Title: r.Title, Text: r.Summary + "\n" + r.Content, Entities: []string{sourceRelevanceText(r.Source)}, Kind: kind, Group: group, Source: r.Source, Version: version})
		byID[r.ID] = r
	}
	var trace *retrieval.Diagnostics
	if len(diagnostics) > 0 {
		trace = diagnostics[0]
	}
	matches := retrieval.RankWithDiagnostics(query, docs, limit, retrieval.Catalog{}, trace)
	out := make([]StoredMemory, 0, len(matches))
	for _, match := range matches {
		r := byID[match.ID]
		signals := match.Signals
		r.Retrieval = &signals
		r.RecallScore = match.Score
		out = append(out, r)
	}
	return out
}
