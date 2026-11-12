package memory

import (
	"strings"

	"github.com/tim5wang/godex/internal/platform/retrieval"
)

func rankStoredMemories(query string, records []StoredMemory, limit int) []StoredMemory {
	if strings.TrimSpace(query) == "" {
		if limit > 0 && len(records) > limit {
			return records[:limit]
		}
		return records
	}
	docs := make([]retrieval.Document, 0, len(records))
	byID := make(map[string]StoredMemory, len(records))
	for _, r := range records {
		docs = append(docs, retrieval.Document{ID: r.ID, Title: r.Title, Text: r.Summary + "\n" + r.Content, Entities: []string{sourceRelevanceText(r.Source)}})
		byID[r.ID] = r
	}
	matches := retrieval.Rank(query, docs, limit)
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
