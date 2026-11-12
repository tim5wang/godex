package retrieval

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Document contains searchable evidence only. Scope and lifecycle filtering are
// the caller's responsibility and must happen BEFORE ranking. Source IDs, type
// labels, confidence and timestamps cannot create a relevance match.
type Document struct {
	ID, Title, Text string
	Entities        []string
}

// Signals explains a match; scores are ranking signals, never truth confidence.
type Signals struct {
	Direct    []EntityHit `json:"catalog_entities,omitempty"`
	Related   []EntityHit `json:"related_entities,omitempty"`
	Ambiguous []string    `json:"ambiguous_entities,omitempty"`
	Relation  float64     `json:"relation,omitempty"`
	Keyword   float64     `json:"bm25"`
	Entity    float64     `json:"entity"`
	Terms     []string    `json:"matched_terms,omitempty"`
	Entities  []string    `json:"matched_entities,omitempty"`
}
type Match struct {
	ID      string
	Score   int
	Signals Signals
}
type scoredDocument struct {
	match  Match
	order  int
	fusion float64
}

var entityToken = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]*(?:[./:-][A-Za-z0-9_]+)*`)

// Rank combines BM25 and exact entity rankings using weighted reciprocal rank
// fusion. It uses no LLM, embeddings, external service or persistent index.
func Rank(query string, documents []Document, limit int) []Match {
	return RankWithCatalog(query, documents, limit, Catalog{})
}

func RankWithCatalog(query string, documents []Document, limit int, catalog Catalog) []Match {
	plan := planCatalog(query, catalog)
	terms := rankingTerms(query)
	entities, anchors := queryEntities(query)
	if len(terms) == 0 && len(entities) == 0 {
		return nil
	}
	counts := make([][]float64, len(documents))
	lengths := make([]float64, len(documents))
	df := make([]int, len(terms))
	average := 0.0
	for i, d := range documents {
		title, body := strings.ToLower(d.Title), strings.ToLower(d.Text)
		counts[i] = make([]float64, len(terms))
		lengths[i] = textLength(title + " " + body)
		average += lengths[i]
		for j, term := range terms {
			counts[i][j] = 3*termFrequency(title, term) + termFrequency(body, term)
			if counts[i][j] > 0 {
				df[j]++
			}
		}
	}
	if len(documents) == 0 {
		return nil
	}
	average /= float64(len(documents))
	scored := []scoredDocument{}
	maxKeyword := 0.0
	for i, d := range documents {
		signals := Signals{Ambiguous: plan.ambiguous}
		signals.Direct, signals.Related = plan.match(d)
		signals.Entity = 2 * float64(len(signals.Direct))
		signals.Relation = float64(len(signals.Related))
		catalogAnchor := false
		for _, hit := range signals.Direct {
			catalogAnchor = catalogAnchor || plan.anchored[hit.ID]
		}
		matchedAnchors := 0
		for j, term := range terms {
			tf := counts[i][j]
			if tf == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(documents)-df[j])+.5)/(float64(df[j])+.5))
			signals.Keyword += idf * tf * 2.2 / (tf + 1.2*(.25+.75*lengths[i]/average))
			signals.Terms = append(signals.Terms, term)
		}
		var candidates map[string]bool
		if len(entities) > 0 {
			candidates = documentEntities(d)
		}
		for _, e := range entities {
			if candidates[e] {
				signals.Entity++
				signals.Entities = append(signals.Entities, e)
				if anchors[e] {
					matchedAnchors++
				}
			}
		}
		// Explicit identifiers are strong query constraints. A generic word such as
		// "failed" must not surface unrelated tools when the named symbol is absent.
		if len(anchors) > 0 && len(plan.anchored) > 0 && !catalogAnchor && len(signals.Related) == 0 {
			continue
		}
		if len(anchors) > 0 && matchedAnchors == 0 && !catalogAnchor && !(len(signals.Related) > 0 && signals.Keyword > 0) {
			continue
		}
		if signals.Keyword == 0 && signals.Entity == 0 {
			continue
		}
		maxKeyword = max(maxKeyword, signals.Keyword)
		scored = append(scored, scoredDocument{match: Match{ID: d.ID, Signals: signals}, order: i})
	}
	eligible := scored[:0]
	for _, s := range scored {
		if s.match.Signals.Entity > 0 || s.match.Signals.Relation > 0 && s.match.Signals.Keyword > 0 || s.match.Signals.Keyword >= maxKeyword*.4 {
			eligible = append(eligible, s)
		}
	}
	scored = eligible
	fuse(scored, func(s scoredDocument) float64 { return s.match.Signals.Keyword }, 1)
	fuse(scored, func(s scoredDocument) float64 { return s.match.Signals.Entity }, .8)
	fuse(scored, func(s scoredDocument) float64 {
		if s.match.Signals.Keyword > 0 {
			return s.match.Signals.Relation
		}
		return 0
	}, .2)
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].fusion == scored[j].fusion {
			return scored[i].order < scored[j].order
		}
		return scored[i].fusion > scored[j].fusion
	})
	if limit > 0 && len(scored) > limit {
		scored = scored[:limit]
	}
	out := make([]Match, 0, len(scored))
	for _, s := range scored {
		s.match.Score = int(math.Round(s.fusion * 10000))
		out = append(out, s.match)
	}
	return out
}

func rankingTerms(query string) []string {
	terms := Terms(query)
	literal := strings.ToLower(strings.TrimSpace(query))
	if len(terms) == 0 && len([]rune(literal)) >= 2 && len([]rune(literal)) <= 3 && strings.ContainsFunc(literal, func(r rune) bool { return unicode.Is(unicode.Han, r) }) && queryBoilerplate.Replace(literal) == literal {
		return []string{literal}
	}
	return terms
}
func fuse(scored []scoredDocument, signal func(scoredDocument) float64, weight float64) {
	order := make([]int, len(scored))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return signal(scored[order[i]]) > signal(scored[order[j]]) })
	previous := -1.0
	sharedRank := 1
	for rank, i := range order {
		if value := signal(scored[i]); value != previous {
			sharedRank = rank + 1
			previous = value
		}
		if signal(scored[i]) > 0 {
			scored[i].fusion += weight / (60 + float64(sharedRank))
		}
	}
}
func termFrequency(text, term string) float64 {
	if strings.ContainsFunc(term, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
		return float64(strings.Count(text, term))
	}
	count := 0
	for offset := 0; offset < len(text); {
		at := strings.Index(text[offset:], term)
		if at < 0 {
			break
		}
		at += offset
		end := at + len(term)
		if (at == 0 || !identifierByte(text[at-1])) && (end == len(text) || !identifierByte(text[end])) {
			count++
		}
		offset = end
	}
	return float64(count)
}
func identifierByte(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' }
func textLength(text string) float64 {
	n := float64(len(strings.Fields(text)))
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			n += .5
		}
	}
	return max(n, 1)
}
func queryEntities(query string) ([]string, map[string]bool) {
	seen := map[string]bool{}
	anchors := map[string]bool{}
	out := []string{}
	for _, token := range entityToken.FindAllString(query, -1) {
		strong := strings.ContainsAny(token, "_./:")
		distinctive := strong || strings.ToUpper(token) == token && len(token) >= 2 || mixedCase(token)
		if !distinctive {
			continue
		}
		for _, key := range entityKeys(token) {
			if !seen[key] {
				out = append(out, key)
				seen[key] = true
			}
			if strong {
				anchors[key] = true
			}
		}
		if len(out) >= 12 {
			break
		}
	}
	return out, anchors
}
func mixedCase(token string) bool {
	for i := 1; i < len(token); i++ {
		if token[i] >= 'A' && token[i] <= 'Z' {
			return true
		}
	}
	return false
}
func entityKeys(token string) []string {
	key := canonicalEntity(token)
	if key == "" {
		return nil
	}
	out := []string{key}
	if i := strings.LastIndexByte(token, '/'); i >= 0 && strings.Contains(token[i+1:], ".") {
		if base := canonicalEntity(token[i+1:]); base != "" && base != key {
			out = append(out, base)
		}
	}
	return out
}
func canonicalEntity(token string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(token) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	key := b.String()
	if len(key) < 2 || genericTerms[key] || len(key) >= 12 && allHex(key) {
		return ""
	}
	return key
}
func allHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func documentEntities(d Document) map[string]bool {
	out := map[string]bool{}
	text := d.Title + " " + d.Text + " " + strings.Join(d.Entities, " ")
	for _, token := range entityToken.FindAllString(text, -1) {
		for _, key := range entityKeys(token) {
			out[key] = true
		}
	}
	return out
}
