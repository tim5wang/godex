package retrieval

import (
	"time"
	"unicode/utf8"
)

// Diagnostics contains bounded metadata, never copies of memory bodies. It is
// an explanation of selection, not evidence that a memory is objectively true.
type Diagnostics struct {
	Query            string              `json:"query"`
	Intent           string              `json:"intent"`
	Layer            string              `json:"layer,omitempty"`
	Cache            string              `json:"cache,omitempty"`
	Candidates       int                 `json:"candidates"`
	Returned         int                 `json:"returned"`
	SelectedIDs      []string            `json:"selected_ids,omitempty"`
	EstimatedTokens  int                 `json:"estimated_result_tokens"`
	ElapsedMS        float64             `json:"elapsed_ms"`
	Rejected         map[string]int      `json:"rejected"`
	Eligibility      map[string]int      `json:"eligibility,omitempty"`
	Decisions        []SelectionDecision `json:"decisions,omitempty"`
	DetailsTruncated bool                `json:"details_truncated,omitempty"`
}
type SelectionDecision struct {
	ID           string   `json:"id"`
	Title        string   `json:"title,omitempty"`
	Kind         string   `json:"kind,omitempty"`
	Source       string   `json:"source,omitempty"`
	Version      int      `json:"version,omitempty"`
	Reason       string   `json:"reason"`
	DuplicateOf  string   `json:"duplicate_of,omitempty"`
	MatchedTerms []string `json:"matched_terms,omitempty"`
	TypeMatch    bool     `json:"type_match,omitempty"`
}

func RankWithDiagnostics(query string, docs []Document, limit int, catalog Catalog, trace *Diagnostics) []Match {
	if trace == nil {
		return RankWithCatalog(query, docs, limit, catalog)
	}
	started := time.Now()
	cache, layer := trace.Cache, trace.Layer
	*trace = Diagnostics{Query: boundedRecallText(query, 384), Intent: QueryIntent(query), Candidates: len(docs), Rejected: map[string]int{}, Cache: cache, Layer: layer}
	matches := rankWithCatalog(query, docs, limit, catalog, trace)
	trace.Returned = len(matches)
	byID := map[string]Document{}
	for _, d := range docs {
		byID[d.ID] = d
	}
	for _, m := range matches {
		d := byID[m.ID]
		trace.EstimatedTokens += utf8.RuneCountInString(d.Title+d.Text) / 2
		if len(trace.SelectedIDs) < 24 {
			trace.SelectedIDs = append(trace.SelectedIDs, m.ID)
		}
	}
	rejected := trace.Decisions
	trace.Decisions = nil
	for i, m := range matches {
		if i >= 8 {
			trace.DetailsTruncated = true
			break
		}
		trace.add(byID[m.ID], "selected", "")
		at := len(trace.Decisions) - 1
		trace.Decisions[at].MatchedTerms = append([]string{}, m.Signals.Terms[:min(12, len(m.Signals.Terms))]...)
		trace.Decisions[at].TypeMatch = m.Signals.TypeMatch > 0
	}
	for _, decision := range rejected {
		if len(trace.Decisions) >= 24 {
			trace.DetailsTruncated = true
			break
		}
		trace.Decisions = append(trace.Decisions, decision)
	}
	trace.ElapsedMS = float64(time.Since(started).Microseconds()) / 1000
	return matches
}

// FinalizeSelection accounts for downstream prompt budgets without reranking.
func FinalizeSelection(d *Diagnostics, ids []string, estimatedTokens int) {
	if d == nil {
		return
	}
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	if dropped := d.Returned - len(selected); dropped > 0 {
		if d.Rejected == nil {
			d.Rejected = map[string]int{}
		}
		d.Rejected["context_budget_or_core"] += dropped
	}
	for i := range d.Decisions {
		if d.Decisions[i].Reason == "selected" && !selected[d.Decisions[i].ID] {
			d.Decisions[i].Reason = "context_budget_or_core"
		}
	}
	d.Returned = len(selected)
	d.SelectedIDs = append([]string{}, ids[:min(24, len(ids))]...)
	d.EstimatedTokens = estimatedTokens
}
func (d *Diagnostics) reject(doc Document, reason, duplicate string) {
	if d == nil {
		return
	}
	if d.Rejected == nil {
		d.Rejected = map[string]int{}
	}
	d.Rejected[reason]++
	d.add(doc, reason, duplicate)
}
func (d *Diagnostics) add(doc Document, reason, duplicate string) {
	if d == nil {
		return
	}
	if len(d.Decisions) >= 24 {
		d.DetailsTruncated = true
		return
	}
	d.Decisions = append(d.Decisions, SelectionDecision{ID: doc.ID, Title: diagnosticLabel(doc.Title, 80), Kind: doc.Kind, Source: diagnosticLabel(doc.Source, 180), Version: doc.Version, Reason: reason, DuplicateOf: duplicate})
}

func diagnosticLabel(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}
