package workcontext

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/retrieval"
)

type recallSnapshot struct {
	stamp   string
	facts   []Fact
	catalog retrieval.Catalog
	until   time.Time
}

// Persisted, desktop-local revisions also cover Dream's fact changes, metadata
// edits and other connections/processes. Ownership is checked BEFORE cache use.
func (s *Store) recallStamp(ctx context.Context, tenant, id string) (string, error) {
	var source, facts, metadata int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT source_seq FROM wc_memory_state WHERE tenant_id=? AND context_id=?),0),COALESCE((SELECT SUM(version) FROM wc_facts WHERE tenant_id=? AND context_id=?),0),COALESCE((SELECT SUM(version) FROM wc_memory_entities WHERE tenant_id=? AND context_id=?),0)+COALESCE((SELECT SUM(version) FROM wc_entity_relations WHERE tenant_id=? AND context_id=?),0)`, tenant, id, tenant, id, tenant, id, tenant, id).Scan(&source, &facts, &metadata)
	return fmt.Sprintf("%d:%d:%d", source, facts, metadata), err
}

func (s *Store) RecallCurrentWithDiagnostics(ctx context.Context, scope Scope, id, query string) ([]Fact, retrieval.Diagnostics, error) {
	started := time.Now()
	trace := retrieval.Diagnostics{Query: query, Intent: retrieval.QueryIntent(query), Layer: "desktop", Cache: "not_run"}
	tenant, owner := scopeArgs(scope)
	if _, err := s.getContext(ctx, tenant, owner, id); err != nil {
		return nil, trace, err
	}
	if strings.TrimSpace(query) == "" {
		return nil, trace, nil
	}
	facts, err := s.recallCurrentSnapshot(ctx, scope, id, query, &trace)
	trace.ElapsedMS = float64(time.Since(started).Microseconds()) / 1000
	return facts, trace, err
}

func (s *Store) recallCurrentSnapshot(ctx context.Context, scope Scope, id, query string, diagnostics ...*retrieval.Diagnostics) ([]Fact, error) {
	var trace *retrieval.Diagnostics
	if len(diagnostics) > 0 {
		trace = diagnostics[0]
	}
	tenant, _ := scopeArgs(scope)
	stamp, err := s.recallStamp(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	key := tenant + "\x00" + id
	s.recallMu.Lock()
	cached, ok := s.recallSnapshots[key]
	s.recallMu.Unlock()
	if ok && cached.stamp == stamp && (cached.until.IsZero() || time.Now().Before(cached.until)) {
		if trace != nil {
			trace.Cache = "hit"
		}
		return rankCurrentSnapshot(query, cached, trace), nil
	}
	if trace != nil {
		trace.Cache = "miss"
	}
	facts, err := s.listFacts(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	catalog, err := s.memoryEntityCatalogWithFacts(ctx, scope, id, facts)
	if err != nil {
		return nil, err
	}
	snapshot := recallSnapshot{stamp: stamp, facts: facts, catalog: catalog}
	for _, f := range facts {
		until, e := time.Parse(time.RFC3339, f.ValidUntil)
		if e == nil && time.Now().Before(until) && (snapshot.until.IsZero() || until.Before(snapshot.until)) {
			snapshot.until = until
		}
	}
	after, err := s.recallStamp(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	// A concurrent update or first-time mention rebuild must not label an old
	// snapshot with a new revision. Such a read is simply not cached.
	if after == stamp {
		s.recallMu.Lock()
		if s.recallSnapshots == nil || len(s.recallSnapshots) >= 32 {
			s.recallSnapshots = map[string]recallSnapshot{}
		}
		s.recallSnapshots[key] = snapshot
		s.recallMu.Unlock()
	}
	return rankCurrentSnapshot(query, snapshot, trace), nil
}

func rankCurrentSnapshot(query string, snapshot recallSnapshot, trace *retrieval.Diagnostics) []Fact {
	facts := make([]Fact, 0, len(snapshot.facts))
	excluded := map[string]int{}
	for _, f := range snapshot.facts {
		if f.Current(time.Now()) {
			facts = append(facts, f)
		} else if trace != nil {
			reason := f.Status
			if f.Status == "confirmed" {
				reason = "expired"
			}
			excluded[reason]++
		}
	}
	if query == "" {
		return facts
	}
	out := rankFacts(query, facts, snapshot.catalog, trace)
	if trace != nil {
		trace.Eligibility = excluded
	}
	return out
}
