package retrieval

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestIntentPrefersDeclaredMethodWithoutCreatingRelevance(t *testing.T) {
	docs := []Document{
		{ID: "fact", Title: "cache_reload", Text: "cache_reload 操作说明", Kind: "fact", Group: "scope"},
		{ID: "method", Title: "cache_reload", Text: "cache_reload 操作说明", Kind: "method", Group: "scope", Source: "file://recipe", Version: 3},
		{ID: "irrelevant", Title: "other_tool", Text: "other_tool 操作说明", Kind: "method", Group: "scope"},
	}
	var trace Diagnostics
	got := RankWithDiagnostics("如何操作 cache_reload", docs, 5, Catalog{}, &trace)
	if len(got) != 2 || got[0].ID != "method" || got[0].Signals.TypeMatch != 1 || trace.Intent != "method" {
		t.Fatal(got, trace)
	}
	if trace.Decisions[0].Version != 3 || trace.Decisions[0].Source != "file://recipe" || !trace.Decisions[0].TypeMatch {
		t.Fatal("missing selection evidence", trace)
	}
	if !reflect.DeepEqual(got, Rank("如何操作 cache_reload", docs, 5)) {
		t.Fatal("diagnostics changed selection")
	}
}

func TestFacetRefinementRejectsCommonObjectOnly(t *testing.T) {
	docs := []Document{
		{ID: "specific", Title: "work_context if_version", Text: "work_context if_version 传递当前版本", Kind: "method", Group: "scope"},
		{ID: "broad", Title: "work_context", Text: "work_context checkpoint 使用说明", Kind: "method", Group: "scope"},
	}
	var trace Diagnostics
	got := RankWithDiagnostics("work_context if_version 怎么传", docs, 5, Catalog{}, &trace)
	if len(got) != 1 || got[0].ID != "specific" || trace.Rejected["missing_identifier_facet"] != 1 {
		t.Fatal(got, trace)
	}
}

func TestQueryDedupPreservesScopeTypesLiteralsAndOpposingClaims(t *testing.T) {
	base := Document{ID: "a", Title: "cache_reload 配置", Text: "cache_reload 配置生效，读取配置文件。", Kind: "fact", Group: "scope"}
	for _, tt := range []struct {
		name  string
		other Document
		want  int
	}{
		{"identical", base, 1},
		{"formatting", Document{Title: base.Title, Text: "cache_reload 配置生效；\n读取配置文件。", Kind: base.Kind, Group: base.Group}, 1},
		{"scope", Document{Title: base.Title, Text: base.Text, Kind: base.Kind, Group: "other"}, 2},
		{"type", Document{Title: base.Title, Text: base.Text, Kind: "method", Group: base.Group}, 2},
		{"parameter", Document{Title: base.Title, Text: "cache_reload 超时 10 秒", Kind: base.Kind, Group: base.Group}, 2},
		{"opposing", Document{Title: base.Title, Text: "cache_reload 配置不会生效，读取配置文件。", Kind: base.Kind, Group: base.Group}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			other := tt.other
			other.ID = "b"
			var trace Diagnostics
			got := RankWithDiagnostics("cache_reload", []Document{base, other}, 5, Catalog{}, &trace)
			if len(got) != tt.want {
				t.Fatal(got, trace)
			}
			if tt.want == 1 && trace.Rejected["duplicate_result"] != 1 {
				t.Fatal(trace)
			}
		})
	}
	for _, pair := range [][2]string{{"cache_reload `a b`", "cache_reload `a  b`"}, {"cache_reload 支持 A，不支持 B", "cache_reload 不支持 A，支持 B"}, {"cache_reload 超时 5 秒", "cache_reload 超时 10 秒"}} {
		a, b := base, base
		a.ID, b.ID = "a", "b"
		a.Text = pair[0]
		b.Text = pair[1]
		if len(Rank("cache_reload", []Document{a, b}, 5)) != 2 {
			t.Fatal("distinct claims collapsed", pair)
		}
	}
	a, b := base, base
	a.ID, b.ID = "a", "b"
	a.Title = "模块甲"
	b.Title = "模块乙"
	if len(Rank("cache_reload", []Document{a, b}, 5)) != 2 {
		t.Fatal("different Chinese subjects collapsed")
	}
	a.Title, b.Title = "a.b", "ab"
	if len(Rank("cache_reload", []Document{a, b}, 5)) != 2 {
		t.Fatal("title identifiers collapsed")
	}
}

func TestRecallDiagnosticsAreBoundedAndContainNoBodies(t *testing.T) {
	docs := []Document{}
	for i := 0; i < 100; i++ {
		docs = append(docs, Document{ID: fmt.Sprint(i), Title: "Unrelated", Text: "private body never copied"})
	}
	docs = append(docs, Document{ID: "selected", Title: "cache_reload", Text: "cache_reload private body never copied", Source: "file://source", Version: 2})
	var trace Diagnostics
	got := RankWithDiagnostics("cache_reload", docs, 5, Catalog{}, &trace)
	if len(got) != 1 || len(trace.Decisions) > 24 || !trace.DetailsTruncated || trace.Decisions[0].ID != "selected" || trace.Returned != 1 || trace.Candidates != 101 {
		t.Fatal(got, trace)
	}
	raw, _ := json.Marshal(trace)
	if strings.Contains(string(raw), "private body never copied") {
		t.Fatal("memory body copied into diagnostic")
	}
}
