package retrieval

import (
	"fmt"
	"strings"
	"testing"
)

func TestHybridExactEntityNormalizationAndAbstention(t *testing.T) {
	docs := []Document{{ID: "correct", Title: "implementation", Text: "ApplyMemoryVerification is an atomic database transaction."}, {ID: "noise", Title: "database failures", Text: strings.Repeat("失败 调用 验证 事务 ", 80)}}
	got := Rank("apply_memory_verification 事务失败", docs, 5)
	if len(got) != 1 || got[0].ID != "correct" || len(got[0].Signals.Entities) == 0 {
		t.Fatal("identifier normalization failed", got)
	}
	if got = Rank("unknown_symbol 事务失败", docs, 5); len(got) != 0 {
		t.Fatal("generic words bypassed explicit identifier", got)
	}
	if got = Rank("请用中文简短回答", docs, 5); len(got) != 0 {
		t.Fatal("response mechanics manufactured a match", got)
	}
}
func TestHybridBM25DoesNotRewardKeywordRepetition(t *testing.T) {
	docs := []Document{{ID: "short", Title: "gopls references", Text: "Fix symbol lookup."}, {ID: "long", Title: "notes", Text: strings.Repeat("gopls ", 300)}}
	got := Rank("gopls references", docs, 2)
	if len(got) == 0 || got[0].ID != "short" {
		t.Fatal("repetition dominated BM25", got)
	}
	if got[0].Signals.Keyword <= 0 || len(got[0].Signals.Terms) == 0 {
		t.Fatal("missing diagnostics")
	}
}
func TestHybridEntitiesCannotComeFromScopeOrOpaqueIDs(t *testing.T) {
	docs := []Document{{ID: "unrelated", Title: "OCR node", Text: "Use Vision", Entities: []string{"session://b22da6b31234567890123456"}}}
	if got := Rank("b22da6b31234567890123456", docs, 5); len(got) != 0 {
		t.Fatal("opaque source id manufactured relevance", got)
	}
	if got := Rank("2026", docs, 5); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestCatalogKeepsUnknownAnchorsAndBasenameSearch(t *testing.T) {
	docs := []Document{
		{ID: "platform", Text: "macOS tools failed"},
		{ID: "a", Text: "pkg/one/config.go"},
		{ID: "b", Text: "pkg/two/config.go"},
	}
	catalog := Catalog{Groups: map[string]string{"platform": "scope", "a": "scope", "b": "scope"}, Entities: []CatalogEntity{
		{ID: "os", Name: "macOS", Kind: "concept", Group: "scope"},
		{ID: "file-a", Name: "pkg/one/config.go", Kind: "file", Group: "scope"},
		{ID: "file-b", Name: "pkg/two/config.go", Kind: "file", Group: "scope"},
	}}
	if got := RankWithCatalog("macOS unknown_symbol failed", docs, 5, catalog); len(got) != 0 {
		t.Fatal("unrelated known entity bypassed an unknown identifier", got)
	}
	if got := RankWithCatalog("config.go", docs, 5, catalog); len(got) != 2 {
		t.Fatal("basename search must retain both paths", got)
	}
	if got := RankWithCatalog("pkg/one/config.go", docs, 5, catalog); len(got) != 1 || got[0].ID != "a" {
		t.Fatal("full path matched another file", got)
	}
}
func BenchmarkHybridRank200(b *testing.B) {
	docs := make([]Document, 200)
	for i := range docs {
		docs[i] = Document{ID: fmt.Sprint(i), Title: "implementation notes", Text: "gopls references fixes symbol navigation and Go source diagnostics"}
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Rank("gopls references navigation", docs, 5)
	}
}
