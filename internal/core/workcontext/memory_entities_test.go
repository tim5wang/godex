package workcontext

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func saveEntityTest(t *testing.T, s *Store, id string, f Fact, kind, name string, aliases ...string) MemoryEntity {
	t.Helper()
	input := EntityInput{Kind: kind, Name: name, Aliases: aliases, FactID: f.ID, FactVersion: f.Version, Reason: "explicitly curated identity from the cited source"}
	page, err := s.ListMemoryEntities(context.Background(), testScope(), id, name, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range page.Items {
		if e.Kind == kind && e.Name == name {
			input.ID = e.ID
			input.IfVersion = e.Version
			break
		}
	}
	e, err := s.SaveMemoryEntity(context.Background(), testScope(), id, input)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestMemoryEntityAliasesProofLifecycleAndStableID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "catalog")
	f := memoryFact(t, s, c.ID, "目录解析模块")
	before, _ := s.MemoryState(ctx, testScope(), c.ID)
	e := saveEntityTest(t, s, c.ID, f, "module", "目录解析模块", "repo_map")
	after, _ := s.MemoryState(ctx, testScope(), c.ID)
	if before.SourceSeq != after.SourceSeq {
		t.Fatal("entity metadata dirtied Dream")
	}
	hits, err := s.RecallCurrent(ctx, testScope(), c.ID, "repo_map")
	if err != nil || len(hits) != 1 || hits[0].ID != f.ID || len(hits[0].Recall.Direct) != 1 {
		t.Fatal("alias did not retrieve original fact", hits, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListMemoryEntities(ctx, testScope(), c.ID, "repo_map", 0, 10)
	if err != nil || page.Total != 1 || page.Items[0].ID != e.ID || len(page.Items[0].Evidence) == 0 {
		t.Fatal("identity/proof did not persist", page, err)
	}
	if _, err = s.ReviewFact(ctx, testScope(), c.ID, f.ID, FactReview{Status: "stale", IfVersion: f.Version}); err != nil {
		t.Fatal(err)
	}
	hits, err = s.RecallCurrent(ctx, testScope(), c.ID, "repo_map")
	if err != nil || len(hits) != 0 {
		t.Fatal("stale proof still expanded an alias", hits, err)
	}
	page, _ = s.ListMemoryEntities(ctx, testScope(), c.ID, "repo_map", 0, 10)
	if page.Items[0].Active || page.Items[0].Evidence[0].Current {
		t.Fatal("expired evidence presented as current", page)
	}
	history, err := s.EntityAuditHistory(ctx, testScope(), c.ID)
	if err != nil || len(history) != 1 {
		t.Fatal("missing metadata audit", history, err)
	}
}
func TestMemoryEntityScopeIsolationAndCAS(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, s, testScope(), "A")
	b := mustCreate(t, s, testScope(), "B")
	fa := memoryFact(t, s, a.ID, "模块甲")
	fb := memoryFact(t, s, b.ID, "模块甲")
	e := saveEntityTest(t, s, a.ID, fa, "module", "模块甲", "private_alias")
	if got, err := s.RecallCurrent(ctx, testScope(), b.ID, "private_alias"); err != nil || len(got) != 0 {
		t.Fatal("alias crossed desktop", got, err)
	}
	if _, err := s.ListMemoryEntities(ctx, Scope{TenantID: testScope().TenantID, OwnerID: "other"}, a.ID, "", 0, 10); !errors.Is(err, ErrNotFound) {
		t.Fatal("directory crossed owners", err)
	}
	input := EntityInput{ID: e.ID, Kind: e.Kind, Name: e.Name, Aliases: []string{"new_alias"}, FactID: fa.ID, FactVersion: fa.Version, IfVersion: e.Version - 1, Reason: "stale edit"}
	if _, err := s.SaveMemoryEntity(ctx, testScope(), a.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatal("stale metadata edit accepted", err)
	}
	input.IfVersion = e.Version
	input.FactID = fb.ID
	if _, err := s.SaveMemoryEntity(ctx, testScope(), a.ID, input); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign proof used", err)
	}
	revision := s.MemoryRevision(ctx, testScope())
	input.FactID = fa.ID
	input.Archived = true
	if _, err := s.SaveMemoryEntity(ctx, testScope(), a.ID, input); err != nil {
		t.Fatal(err)
	}
	if s.MemoryRevision(ctx, testScope()) == revision {
		t.Fatal("metadata did not invalidate recall cache")
	}
	if hits, _ := s.RecallCurrent(ctx, testScope(), a.ID, "private_alias"); len(hits) != 0 {
		t.Fatal("archived alias remained searchable")
	}
}
func TestMemoryEntityTypedIdentityAvoidsSeparatorAndFilenameCollisions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "identity")
	a := memoryFact(t, s, c.ID, "a_b")
	b := memoryFact(t, s, c.ID, "ab")
	ea := saveEntityTest(t, s, c.ID, a, "symbol", "a_b")
	eb := saveEntityTest(t, s, c.ID, b, "symbol", "ab")
	if ea.ID == eb.ID {
		t.Fatal("distinct symbols merged")
	}
	hits, err := s.RecallCurrent(ctx, testScope(), c.ID, "a_b")
	if err != nil || len(hits) != 1 || hits[0].ID != a.ID {
		t.Fatal("normalization collision leaked", hits, err)
	}
	fileA := memoryFact(t, s, c.ID, "pkg/one/config.go")
	memoryFact(t, s, c.ID, "pkg/two/config.go")
	hits, err = s.RecallCurrent(ctx, testScope(), c.ID, "pkg/one/config.go")
	if err != nil || len(hits) != 1 || hits[0].ID != fileA.ID {
		t.Fatal("basename collision leaked", hits, err)
	}
}
func TestMemoryEntityPartOfIsOneHopAndRequiresLiveProof(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "relations")
	root := memoryFact(t, s, c.ID, "AE2 wc_attention_actions snooze")
	child := memoryFact(t, s, c.ID, "AE2.1 snooze 条目级动作优先")
	grandchild := memoryFact(t, s, c.ID, "AE2.1.1 snooze 历史细节")
	a := saveEntityTest(t, s, c.ID, root, "module", "AE2")
	b := saveEntityTest(t, s, c.ID, child, "module", "AE2.1")
	d := saveEntityTest(t, s, c.ID, grandchild, "module", "AE2.1.1")
	r, err := s.SaveEntityRelation(ctx, testScope(), c.ID, EntityRelationInput{FromID: b.ID, ToID: a.ID, Kind: "part_of", FactID: child.ID, FactVersion: child.Version, Reason: "AE2.1 refines AE2's snooze behavior"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveEntityRelation(ctx, testScope(), c.ID, EntityRelationInput{FromID: d.ID, ToID: b.ID, Kind: "part_of", FactID: grandchild.ID, FactVersion: grandchild.Version, Reason: "explicit second level"}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.RecallCurrent(ctx, testScope(), c.ID, "AE2 wc_attention_actions snooze")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]Fact{}
	for _, f := range hits {
		seen[f.ID] = f
	}
	if seen[child.ID].Recall == nil {
		catalog, _ := s.MemoryEntityCatalog(ctx, testScope(), c.ID)
		t.Fatalf("child missing: hits=%+v catalog=%+v", hits, catalog)
	}
	if len(seen[child.ID].Recall.Related) != 1 || seen[child.ID].Recall.Related[0].RelationID != r.ID {
		t.Fatal("missing one-hop evidence", hits)
	}
	if _, ok := seen[grandchild.ID]; ok {
		t.Fatal("relationship expanded transitively")
	}
	if _, err = s.SaveEntityRelation(ctx, testScope(), c.ID, EntityRelationInput{FromID: a.ID, ToID: b.ID, Kind: "part_of", FactID: root.ID, FactVersion: root.Version, Reason: "bad cycle"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("cycle accepted", err)
	}
	if _, err = s.ReviewFact(ctx, testScope(), c.ID, child.ID, FactReview{Status: "stale", IfVersion: child.Version}); err != nil {
		t.Fatal(err)
	}
	hits, _ = s.RecallCurrent(ctx, testScope(), c.ID, "AE2 wc_attention_actions snooze")
	for _, f := range hits {
		if f.ID == child.ID {
			t.Fatal("stale relation proof recalled stale fact")
		}
	}
}
func TestMemoryEntityCannotActivateFromObservation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "unverified")
	f, err := s.CreateFact(ctx, testScope(), c.ID, FactInput{Title: "unverified_tool", Content: "unverified_tool may exist", Kind: "fact", Scope: `{"env":"test"}`})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListMemoryEntities(ctx, testScope(), c.ID, "unverified_tool", 0, 10)
	if err != nil || page.Total != 1 || page.Items[0].Active {
		t.Fatal(page, err)
	}
	if _, err = s.SaveMemoryEntity(ctx, testScope(), c.ID, EntityInput{Kind: "module", Name: "guess", Aliases: []string{"guess_alias"}, FactID: f.ID, FactVersion: f.Version, Reason: "not confirmed"}); !errors.Is(err, ErrConflict) {
		t.Fatal("unverified source activated alias", err)
	}
	if got, _ := s.RecallCurrent(ctx, testScope(), c.ID, "unverified_tool"); len(got) != 0 {
		t.Fatal("directory elevated truth")
	}
}

func TestMemoryEntityProofVersionAndExpiryDisableMetadata(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed-version", true: "expired"}[expire], func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			c := mustCreate(t, s, testScope(), "proof")
			root := memoryFact(t, s, c.ID, "RootModule")
			child := memoryFact(t, s, c.ID, "ChildModule")
			a := saveEntityTest(t, s, c.ID, root, "symbol", "RootModule", "root_alias")
			b := saveEntityTest(t, s, c.ID, child, "symbol", "ChildModule")
			r, err := s.SaveEntityRelation(ctx, testScope(), c.ID, EntityRelationInput{FromID: b.ID, ToID: a.ID, Kind: "part_of", FactID: root.ID, FactVersion: root.Version, Reason: "source identifies the membership"})
			if err != nil {
				t.Fatal(err)
			}
			if expire {
				// Simulate a previously future deadline passing without a fact version change.
				_, err = s.db.ExecContext(ctx, `UPDATE wc_facts SET valid_until=? WHERE id=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), root.ID)
			} else {
				root, err = s.ReviewFact(ctx, testScope(), c.ID, root.ID, FactReview{Status: "confirmed", IfVersion: root.Version})
			}
			if err != nil {
				t.Fatal(err)
			}
			if hits, err := s.RecallCurrent(ctx, testScope(), c.ID, "root_alias"); err != nil || len(hits) != 0 {
				t.Fatal("invalid proof retained an alias", hits, err)
			}
			page, err := s.ListMemoryEntities(ctx, testScope(), c.ID, "root_alias", 0, 10)
			if err != nil || page.Total != 1 || page.Items[0].AliasesActive || page.Items[0].Origin != "curated" {
				t.Fatal("metadata validity is not visible", page, err)
			}
			if !expire && !page.Items[0].Active {
				t.Fatal("current literal mention should retain the canonical identity")
			}
			catalog, err := s.MemoryEntityCatalog(ctx, testScope(), c.ID)
			if err != nil || len(catalog.Relations) != 0 {
				t.Fatal("invalid proof retained a relation", catalog, err)
			}
			if _, err = s.SaveEntityRelation(ctx, testScope(), c.ID, EntityRelationInput{FromID: b.ID, ToID: a.ID, Kind: "part_of", FactID: root.ID, FactVersion: root.Version, IfVersion: r.Version, Archived: true, Reason: "retire outdated metadata"}); err != nil {
				t.Fatal("outdated relation cannot be archived", err)
			}
		})
	}
}

func TestMemoryEntityApplicabilityDoesNotWiden(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "scope")
	f := memoryFact(t, s, c.ID, "shared_module")
	other, err := s.CreateFact(ctx, testScope(), c.ID, FactInput{Title: "shared_module", Content: "shared_module production", Kind: "fact", Scope: `{"env":"prod"}`, Source: "report://fixture/prod", ObservedAt: "2026-11-12T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	other, err = s.ReviewFact(ctx, testScope(), c.ID, other.ID, FactReview{Status: "confirmed", IfVersion: other.Version})
	if err != nil {
		t.Fatal(err)
	}
	e := saveEntityTest(t, s, c.ID, f, "module", "shared_module", "test_alias")
	if hits, err := s.RecallCurrent(ctx, testScope(), c.ID, "test_alias"); err != nil || len(hits) != 1 || hits[0].ID != f.ID {
		t.Fatal("alias widened applicability", hits, err)
	}
	prod, err := s.SaveMemoryEntity(ctx, testScope(), c.ID, EntityInput{Kind: "module", Name: "prod_module", FactID: other.ID, FactVersion: other.Version, Reason: "production source"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveEntityRelation(ctx, testScope(), c.ID, EntityRelationInput{FromID: prod.ID, ToID: e.ID, Kind: "part_of", FactID: f.ID, FactVersion: f.Version, Reason: "invalid cross-scope relation"}); !errors.Is(err, ErrConflict) {
		t.Fatal("relation widened applicability", err)
	}
}

func TestEntityRelationInactiveWhenEndpointArchived(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "archived endpoint")
	fa := memoryFact(t, s, c.ID, "Parent")
	fb := memoryFact(t, s, c.ID, "Child")
	a := saveEntityTest(t, s, c.ID, fa, "module", "Parent")
	b := saveEntityTest(t, s, c.ID, fb, "module", "Child")
	if _, err := s.SaveEntityRelation(ctx, testScope(), c.ID, EntityRelationInput{FromID: b.ID, ToID: a.ID, Kind: "part_of", FactID: fb.ID, FactVersion: fb.Version, Reason: "current membership"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveMemoryEntity(ctx, testScope(), c.ID, EntityInput{ID: a.ID, Kind: a.Kind, Name: a.Name, FactID: fa.ID, FactVersion: fa.Version, IfVersion: a.Version, Archived: true, Reason: "retire parent identity"}); err != nil {
		t.Fatal(err)
	}
	relations, err := s.ListEntityRelations(ctx, testScope(), c.ID)
	if err != nil || len(relations) != 1 || relations[0].Active {
		t.Fatal("archived endpoint presented as an active relation", relations, err)
	}
}

func TestEntityRelationRejectsLongCycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "deep hierarchy")
	var first, previous MemoryEntity
	var firstProof Fact
	for i := 0; i < 35; i++ {
		name := fmt.Sprintf("Layer%02d", i)
		f := memoryFact(t, s, c.ID, name)
		e := saveEntityTest(t, s, c.ID, f, "module", name)
		if i == 0 {
			first, firstProof = e, f
		} else if _, err := s.SaveEntityRelation(ctx, testScope(), c.ID, EntityRelationInput{FromID: e.ID, ToID: previous.ID, Kind: "part_of", FactID: f.ID, FactVersion: f.Version, Reason: "explicit hierarchy"}); err != nil {
			t.Fatal(err)
		}
		previous = e
	}
	if _, err := s.SaveEntityRelation(ctx, testScope(), c.ID, EntityRelationInput{FromID: first.ID, ToID: previous.ID, Kind: "part_of", FactID: firstProof.ID, FactVersion: firstProof.Version, Reason: "would close a long cycle"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("long hierarchy bypassed cycle detection", err)
	}
}
