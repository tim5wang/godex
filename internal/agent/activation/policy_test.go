package activation

import (
	"reflect"
	"testing"

	"github.com/tim5wang/godex/internal/core/templates"
	"github.com/tim5wang/godex/internal/toolruntime"
)

func TestResolveExactUnion(t *testing.T) {
	catalog := toolruntime.ToolCatalog{Bundles: []toolruntime.BundleCatalogItem{
		{Name: "core", Tools: []string{"read_file", "bash"}},
		{Name: "web", Tools: []string{"web_search"}},
	}}
	got := Resolve(templates.AgentTemplate{ID: "worker", Bundles: []string{"CORE"}, Tools: []string{"bash", "edit_file"}}, catalog)
	if got.Mode != Exact {
		t.Fatalf("mode = %v, want exact", got.Mode)
	}
	want := []string{"read_file", "bash", "edit_file"}
	if !reflect.DeepEqual(got.ToolNames, want) {
		t.Fatalf("tools = %v, want %v", got.ToolNames, want)
	}
}

func TestResolveEmptyTemplates(t *testing.T) {
	if got := Resolve(templates.AgentTemplate{ID: templates.BuiltinDefault}, toolruntime.ToolCatalog{}); got.Mode != RegistrationDefaults {
		t.Fatalf("built-in default mode = %v, want registration defaults", got.Mode)
	}
	got := Resolve(templates.AgentTemplate{ID: "empty-custom"}, toolruntime.ToolCatalog{})
	if got.Mode != Exact || len(got.ToolNames) != 0 {
		t.Fatalf("custom empty plan = %#v, want exact empty", got)
	}
}
