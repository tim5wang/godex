// Command memory-recall-eval runs the real desktop and personal retrieval paths
// against a fixed, labeled Chinese/English corpus in throwaway stores. It never
// reads or writes the operator's memory and never calls a model.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/platform/retrieval"
)

//go:embed testdata/corpus.json
var corpusJSON []byte

type record struct {
	ID, Title, Content, Status, Desktop string
	Expired                             bool
}
type query struct {
	ID, Text, Split string
	Relevant        []string
}
type evalEntity struct {
	ID, Kind, Name, FactID string
	Aliases                []string
}
type evalRelation struct{ From, To, FactID string }
type corpus struct {
	Engine    string
	Entities  []evalEntity
	Relations []evalRelation
	Records   []record
	Queries   []query
}
type result struct {
	Signals   []any    `json:"signals,omitempty"`
	Query     string   `json:"query"`
	Split     string   `json:"split"`
	Expected  []string `json:"expected"`
	Retrieved []string `json:"retrieved"`
	MS        float64  `json:"ms"`
	Tokens    int      `json:"estimated_tokens"`
}
type metrics struct {
	Queries          int     `json:"queries"`
	Precision        float64 `json:"precision_returned_at_5"`
	Recall           float64 `json:"recall_at_5"`
	MRR              float64 `json:"mrr"`
	NoResultAccuracy float64 `json:"no_result_accuracy"`
	Forbidden        int     `json:"forbidden_hits"`
	MeanTokens       float64 `json:"mean_estimated_tokens"`
	P50              float64 `json:"p50_ms"`
	P95              float64 `json:"p95_ms"`
}
type report struct {
	Label     string              `json:"label"`
	CorpusSHA string              `json:"corpus_sha256"`
	Records   int                 `json:"records"`
	Metrics   map[string]metrics  `json:"metrics"`
	Results   map[string][]result `json:"results"`
}

func main() {
	out := flag.String("out", "", "JSON output file; stdout if empty")
	label := flag.String("label", "current", "report label")
	noCatalog := flag.Bool("without-catalog", false, "ablate the scoped catalog while keeping the same eligible facts and ranking kernel")
	corpusPath := flag.String("corpus", "", "optional labeled JSON corpus; defaults to embedded fixture")
	flag.Parse()
	rawCorpus := corpusJSON
	var err error
	if *corpusPath != "" {
		rawCorpus, err = os.ReadFile(*corpusPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	r, err := evaluateWithCatalog(*label, rawCorpus, !*noCatalog)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	raw, _ := json.MarshalIndent(r, "", "  ")
	raw = append(raw, '\n')
	if *out == "" {
		_, err = os.Stdout.Write(raw)
	} else {
		err = os.WriteFile(*out, raw, 0644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func evaluate(label string, rawCorpus []byte) (report, error) {
	return evaluateWithCatalog(label, rawCorpus, true)
}

func evaluateWithCatalog(label string, rawCorpus []byte, withCatalog bool) (report, error) {
	var data corpus
	err := json.Unmarshal(rawCorpus, &data)
	if err != nil {
		return report{}, err
	}
	dir, err := os.MkdirTemp("", "godex-recall-eval-")
	if err != nil {
		return report{}, err
	}
	defer os.RemoveAll(dir)
	store, err := workcontext.Open(filepath.Join(dir, "desktop.sqlite"))
	if err != nil {
		return report{}, err
	}
	defer store.Close()
	ctx := context.Background()
	scope := workcontext.Scope{TenantID: "eval", OwnerID: "owner"}
	c, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "primary"})
	if err != nil {
		return report{}, err
	}
	other, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "other"})
	if err != nil {
		return report{}, err
	}
	m := memory.NewManager(filepath.Join(dir, "personal"))
	sourceFacts := map[string]workcontext.Fact{}
	desktopIDs := map[string]string{}
	personalIDs := map[string]string{}
	forbidden := map[string]bool{}
	for _, r := range data.Records {
		id := c.ID
		if r.Desktop == "other" {
			id = other.ID
		}
		deadline := ""
		var expiry time.Time
		if r.Expired {
			deadline = "2020-01-01T00:00:00Z"
			expiry, _ = time.Parse(time.RFC3339, deadline)
		}
		f, e := store.CreateFact(ctx, scope, id, workcontext.FactInput{Title: r.Title, Content: r.Content, Kind: "fact", Scope: `{"env":"eval"}`, Source: "fixture://" + r.ID, ObservedAt: "2026-01-01T00:00:00Z", ValidUntil: deadline})
		if e != nil {
			return report{}, e
		}
		f, e = store.ReviewFact(ctx, scope, id, f.ID, workcontext.FactReview{Status: r.Status, IfVersion: f.Version})
		if e != nil {
			return report{}, e
		}
		desktopIDs[f.ID] = r.ID
		sourceFacts[r.ID] = f
		if r.Desktop != "primary" || r.Status != "confirmed" || r.Expired {
			forbidden[r.ID] = true
		}
		if r.Desktop != "primary" || r.Status == "observed" || r.Status == "verification_blocked" {
			continue
		}
		entry, e := m.Remember(memory.SaveInput{Title: r.Title, Summary: r.Content, Content: r.Content, Type: memory.TypeWorkFact, Source: "fixture://" + r.ID, ExpiresAt: expiry})
		if e != nil {
			return report{}, e
		}
		personalIDs[entry.ID] = r.ID
		if r.Status == "archived" || r.Status == "superseded" || r.Status == "rejected" {
			if _, e = m.Archive(memory.ForgetInput{File: entry.File}); e != nil {
				return report{}, e
			}
		}
	}
	if err = seedEntityCatalog(ctx, store, scope, data, sourceFacts); err != nil {
		return report{}, err
	}
	sum := sha256.Sum256(rawCorpus)
	out := report{Label: label, CorpusSHA: hex.EncodeToString(sum[:]), Records: len(data.Records), Metrics: map[string]metrics{}, Results: map[string][]result{}}
	engines := []string{"desktop", "personal"}
	if data.Engine == "desktop" {
		engines = []string{"desktop"}
	}
	for _, engine := range engines {
		for _, q := range data.Queries {
			one := result{Query: q.Text, Split: q.Split, Expected: q.Relevant, Retrieved: []string{}}
			// Warm the cache once; timed reads still include eligibility and ranking.
			if engine == "personal" {
				_, _ = m.FindRelevant(q.Text, 5)
			}
			start := time.Now()
			if engine == "desktop" {
				facts, e := recallForEvaluation(ctx, store, scope, c.ID, q.Text, withCatalog)
				if e != nil {
					return out, e
				}
				for _, f := range facts {
					if !f.Current(time.Now()) {
						continue
					}
					one.Retrieved = append(one.Retrieved, desktopIDs[f.ID])
					one.Signals = append(one.Signals, f.Recall)
					one.Tokens += utf8.RuneCountInString(f.Title+f.Content) / 2
					if len(one.Retrieved) == 5 {
						break
					}
				}
			} else {
				facts, e := m.FindRelevant(q.Text, 5)
				if e != nil {
					return out, e
				}
				for _, f := range facts {
					one.Retrieved = append(one.Retrieved, personalIDs[f.ID])
					one.Signals = append(one.Signals, f.Retrieval)
					one.Tokens += utf8.RuneCountInString(f.Title+f.Content) / 2
				}
			}
			one.MS = float64(time.Since(start).Microseconds()) / 1000
			out.Results[engine] = append(out.Results[engine], one)
		}
		for _, split := range []string{"development", "holdout", "all"} {
			out.Metrics[engine+"/"+split] = summarize(out.Results[engine], split, forbidden)
		}
	}
	return out, nil
}
func summarize(rows []result, split string, forbidden map[string]bool) metrics {
	var m metrics
	var times []float64
	positive, negative, empty := 0, 0, 0
	returned, correct := 0, 0
	for _, row := range rows {
		if split != "all" && row.Split != split {
			continue
		}
		m.Queries++
		times = append(times, row.MS)
		m.MeanTokens += float64(row.Tokens)
		expected := map[string]bool{}
		for _, id := range row.Expected {
			expected[id] = true
		}
		if len(expected) == 0 {
			negative++
			if len(row.Retrieved) == 0 {
				empty++
			}
		}
		hits := 0
		first := 0
		for i, id := range row.Retrieved {
			if forbidden[id] {
				m.Forbidden++
			}
			if expected[id] {
				hits++
				if first == 0 {
					first = i + 1
				}
			}
		}
		if len(expected) > 0 {
			positive++
			m.Recall += float64(hits) / float64(len(expected))
			if first > 0 {
				m.MRR += 1 / float64(first)
			}
		}
		returned += len(row.Retrieved)
		correct += hits
	}
	if m.Queries > 0 {
		if returned > 0 {
			m.Precision = float64(correct) / float64(returned)
		}
		m.MeanTokens /= float64(m.Queries)
	}
	if positive > 0 {
		m.Recall /= float64(positive)
		m.MRR /= float64(positive)
	}
	if negative > 0 {
		m.NoResultAccuracy = float64(empty) / float64(negative)
	}
	sort.Float64s(times)
	if len(times) > 0 {
		m.P50 = times[len(times)/2]
		m.P95 = times[min(len(times)-1, int(math.Ceil(float64(len(times))*.95))-1)]
	}
	return m
}

func seedEntityCatalog(ctx context.Context, store *workcontext.Store, scope workcontext.Scope, data corpus, facts map[string]workcontext.Fact) error {
	entities := map[string]workcontext.MemoryEntity{}
	for _, spec := range data.Entities {
		fact, ok := facts[spec.FactID]
		if !ok {
			return fmt.Errorf("unknown entity proof %s", spec.FactID)
		}
		input := workcontext.EntityInput{Kind: spec.Kind, Name: spec.Name, Aliases: spec.Aliases, FactID: fact.ID, FactVersion: fact.Version, Reason: "evaluation: explicit curated identity from this source"}
		page, err := store.ListMemoryEntities(ctx, scope, fact.ContextID, spec.Name, 0, 100)
		if err != nil {
			return err
		}
		for _, existing := range page.Items {
			if existing.Kind == spec.Kind && existing.Name == spec.Name {
				input.ID = existing.ID
				input.IfVersion = existing.Version
				break
			}
		}
		entity, err := store.SaveMemoryEntity(ctx, scope, fact.ContextID, input)
		if err != nil {
			return err
		}
		entities[spec.ID] = entity
	}
	for _, spec := range data.Relations {
		fact, ok := facts[spec.FactID]
		if !ok {
			return fmt.Errorf("unknown relation proof %s", spec.FactID)
		}
		_, err := store.SaveEntityRelation(ctx, scope, fact.ContextID, workcontext.EntityRelationInput{FromID: entities[spec.From].ID, ToID: entities[spec.To].ID, Kind: "part_of", FactID: fact.ID, FactVersion: fact.Version, Reason: "evaluation: explicitly curated one-hop membership"})
		if err != nil {
			return err
		}
	}
	return nil
}
func recallForEvaluation(ctx context.Context, store *workcontext.Store, scope workcontext.Scope, id, query string, withCatalog bool) ([]workcontext.Fact, error) {
	if withCatalog {
		return store.RecallCurrent(ctx, scope, id, query)
	}
	facts, err := store.Facts(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	docs := []retrieval.Document{}
	byID := map[string]workcontext.Fact{}
	for _, fact := range facts {
		if fact.Current(time.Now()) {
			docs = append(docs, retrieval.Document{ID: fact.ID, Title: fact.Title, Text: fact.Content, Entities: []string{fact.Subject, fact.Predicate}})
			byID[fact.ID] = fact
		}
	}
	out := []workcontext.Fact{}
	for _, match := range retrieval.Rank(query, docs, 0) {
		f := byID[match.ID]
		signals := match.Signals
		f.Recall = &signals
		out = append(out, f)
	}
	return out, nil
}
