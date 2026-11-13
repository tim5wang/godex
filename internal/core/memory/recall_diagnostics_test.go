package memory

import "testing"

func TestNoResultDiagnosticsSeparateEligibilityFromRanking(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.Remember(SaveInput{Title: "cache_reload", Summary: "缓存刷新", Content: "cache_reload 读取配置", Type: TypeWorkFact}); err != nil {
		t.Fatal(err)
	}
	layers, err := m.PreviewContextLayers("unknown_symbol")
	if err != nil || len(layers.Diagnostics) != 1 {
		t.Fatal(layers, err)
	}
	d := layers.Diagnostics[0]
	if d.Candidates != 1 || d.Returned != 0 || d.Rejected["identifier_mismatch"] != 1 {
		t.Fatal(d)
	}
	if d.Eligibility["not_selected_by_relevance_scope_or_budget"] != 0 {
		t.Fatal("aggregate selection count duplicated an exclusion reason", d)
	}
}
