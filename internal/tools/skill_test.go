package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/skill"
)

type fakeSkillRuntime struct {
	active             map[string]struct{}
	installHadDeadline bool
}

func (f *fakeSkillRuntime) ActivateSkill(name string) (SkillActivation, error) {
	if name == "missing" {
		return SkillActivation{}, context.Canceled
	}
	if f.active == nil {
		f.active = make(map[string]struct{})
	}
	status := "already_active"
	if _, ok := f.active[name]; !ok {
		f.active[name] = struct{}{}
		status = "activated"
	}
	return SkillActivation{
		Name:               name,
		Status:             status,
		Description:        "Example skill",
		LoadedSections:     []string{"core"},
		AvailableSections:  []string{"core", "workflow"},
		RecommendedBundles: []string{"background"},
		Compatibility:      skill.Compatibility{Status: skill.CompatibilityNativeSupported},
	}, nil
}

func (f *fakeSkillRuntime) ExpandSkill(name string, sections []string) (SkillExpansion, error) {
	if _, ok := f.active[name]; !ok {
		return SkillExpansion{}, context.Canceled
	}
	return SkillExpansion{
		Name:              name,
		Status:            "expanded",
		ExpandedSections:  sections,
		LoadedSections:    append([]string{"core"}, sections...),
		AvailableSections: []string{"core", "workflow"},
		Compatibility:     skill.Compatibility{Status: skill.CompatibilityNativeSupported},
	}, nil
}

func (f *fakeSkillRuntime) ListSkills() ([]skill.CatalogEntry, error) {
	return []skill.CatalogEntry{
		{
			ID:                 "example",
			Name:               "example",
			Description:        "Example skill",
			RecommendedBundles: []string{"background"},
			Sections:           []string{"core", "workflow"},
			Compatibility:      skill.Compatibility{Status: skill.CompatibilityNativeSupported},
		},
		{
			ID:                 "gstack",
			Name:               "gstack",
			Description:        "Fast headless browser with nested specialist skills",
			RecommendedBundles: []string{"team"},
			Compatibility:      skill.Compatibility{Status: skill.CompatibilityNativeSupported},
		},
		{
			ID:            "gstack/plan-eng-review",
			Name:          "plan-eng-review",
			Description:   "Engineering manager architecture review",
			Compatibility: skill.Compatibility{Status: skill.CompatibilityNativeSupported},
		},
		{
			ID:            "gstack/qa",
			Name:          "qa",
			Description:   "Browser QA testing",
			Compatibility: skill.Compatibility{Status: skill.CompatibilityNativeSupported},
		},
	}, nil
}

func (f *fakeSkillRuntime) GetSkill(name string) (skill.CatalogEntry, error) {
	if name == "missing" {
		return skill.CatalogEntry{}, context.Canceled
	}
	return skill.CatalogEntry{
		Name:               name,
		Description:        "Example skill",
		RecommendedBundles: []string{"background"},
		Sections:           []string{"core", "workflow"},
		Compatibility:      skill.Compatibility{Status: skill.CompatibilityNativeSupported},
	}, nil
}

func (f *fakeSkillRuntime) ListSkillSources() ([]SkillSourceEntry, error) {
	return []SkillSourceEntry{
		{
			ID:               "playwright-cli",
			Name:             "playwright-cli",
			Summary:          "Browser automation helpers",
			Source:           "owner/repo",
			SkillName:        "playwright-cli",
			Tags:             []string{"browser"},
			Origin:           "curated",
			InstallSupported: true,
			Installed:        false,
		},
	}, nil
}

func (f *fakeSkillRuntime) SearchSkillSources(query string) ([]SkillSourceEntry, error) {
	return []SkillSourceEntry{
		{
			ID:               "skillsh:vercel-labs/agent-skills/react",
			Name:             "react",
			Summary:          "Search result for " + query,
			Source:           "vercel-labs/agent-skills",
			SkillName:        "react",
			Origin:           "skillsh",
			Trust:            "community",
			InstallSupported: true,
		},
	}, nil
}

func (f *fakeSkillRuntime) ActiveSkills() ([]SkillActivation, error) {
	items := make([]SkillActivation, 0, len(f.active))
	for name := range f.active {
		items = append(items, SkillActivation{
			Name:              name,
			Status:            "active",
			Description:       "Example skill",
			LoadedSections:    []string{"core"},
			AvailableSections: []string{"core", "workflow"},
			Compatibility:     skill.Compatibility{Status: skill.CompatibilityNativeSupported},
		})
	}
	return items, nil
}

func (f *fakeSkillRuntime) UnloadSkill(name string) (SkillActivation, error) {
	if _, ok := f.active[name]; !ok {
		return SkillActivation{}, context.Canceled
	}
	delete(f.active, name)
	return SkillActivation{
		Name:              name,
		Status:            "unloaded",
		Description:       "Example skill",
		LoadedSections:    nil,
		AvailableSections: []string{"core", "workflow"},
		Compatibility:     skill.Compatibility{Status: skill.CompatibilityNativeSupported},
	}, nil
}

func (f *fakeSkillRuntime) InstallSkill(source, name string) (SkillInstallResult, error) {
	if source == "missing" {
		return SkillInstallResult{}, context.Canceled
	}
	if name == "" {
		name = "example"
	}
	return SkillInstallResult{
		Name:               name,
		Status:             "installed",
		Source:             source,
		InstalledPath:      "/tmp/" + name,
		Description:        "Installed skill",
		Sections:           []string{"core", "workflow"},
		RecommendedBundles: []string{"background"},
		Compatibility:      skill.Compatibility{Status: skill.CompatibilityNativeSupported},
	}, nil
}

func (f *fakeSkillRuntime) InstallSkillContext(ctx context.Context, source, name string) (SkillInstallResult, error) {
	_, f.installHadDeadline = ctx.Deadline()
	return f.InstallSkill(source, name)
}

func (f *fakeSkillRuntime) RemoveSkill(name string) (SkillRemoveResult, error) {
	if name == "missing" {
		return SkillRemoveResult{}, context.Canceled
	}
	delete(f.active, name)
	return SkillRemoveResult{
		ID:          name,
		Name:        name,
		Status:      "removed",
		RemovedPath: "/tmp/" + name,
	}, nil
}

func TestSkillToolLoadActionActivatesSkillsAndReportsStatus(t *testing.T) {
	runtime := &fakeSkillRuntime{}
	tool := NewSkillTool(runtime)

	if _, err := tool.Execute(context.Background(), map[string]interface{}{"action": "load", "name": "missing"}); err == nil {
		t.Fatal("expected missing skill to fail")
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "load", "name": "example"})
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	for _, want := range []string{`"status":"activated"`, `"loaded_sections":["core"]`, `"recommended_bundles":["background"]`} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected load result to contain %q, got %q", want, result)
		}
	}

	result, err = tool.Execute(context.Background(), map[string]interface{}{"action": "load", "name": "example"})
	if err != nil {
		t.Fatalf("reload skill: %v", err)
	}
	if !strings.Contains(result, `"status":"already_active"`) {
		t.Fatalf("expected already_active status, got %q", result)
	}
}

func TestSkillToolListActionReturnsCatalog(t *testing.T) {
	runtime := &fakeSkillRuntime{}
	tool := NewSkillTool(runtime)

	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "list"})
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	for _, want := range []string{`"mode":"catalog"`, `"skills"`, `"suites"`, `"id":"example"`, `"id":"gstack"`, `"description":"Fast headless browser with nested specialist skills"`, `"child_skill_count":2`, `"child_skill_ids":["gstack/plan-eng-review","gstack/qa"]`, `"child_skill_hint"`, `"list_hint"`} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected list result to contain %q, got %q", want, result)
		}
	}
	if strings.Contains(result, `"description":"Engineering manager architecture review"`) {
		t.Fatalf("expected default catalog to omit child skill descriptions, got %q", result)
	}
}

func TestSkillToolListActionIncludeDetailsReturnsFullRootEntries(t *testing.T) {
	runtime := &fakeSkillRuntime{}
	tool := NewSkillTool(runtime)

	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "list", "include_details": true})
	if err != nil {
		t.Fatalf("list detailed skills: %v", err)
	}
	for _, want := range []string{`"mode":"catalog"`, `"description":"Fast headless browser with nested specialist skills"`, `"recommended_bundles":["team"]`, `"child_skill_count":2`, `"child_skill_ids":["gstack/plan-eng-review","gstack/qa"]`} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected detailed catalog to contain %q, got %q", want, result)
		}
	}
	if strings.Contains(result, `"description":"Engineering manager architecture review"`) {
		t.Fatalf("expected detailed catalog to omit child skill descriptions, got %q", result)
	}
}

func TestSkillToolListActionPaginatesSuiteChildren(t *testing.T) {
	runtime := &fakeSkillRuntime{}
	tool := NewSkillTool(runtime)

	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "list", "suite": "gstack", "limit": 1})
	if err != nil {
		t.Fatalf("list suite skills: %v", err)
	}
	if !strings.Contains(result, `"id":"gstack/plan-eng-review"`) {
		t.Fatalf("expected first suite page to include gstack/plan-eng-review, got %q", result)
	}
	if !strings.Contains(result, `"has_more":true`) || !strings.Contains(result, `"next_offset":1`) {
		t.Fatalf("expected suite result to expose pagination, got %q", result)
	}

	result, err = tool.Execute(context.Background(), map[string]interface{}{"action": "list", "suite": "gstack", "offset": 1, "limit": 1})
	if err != nil {
		t.Fatalf("list second suite page: %v", err)
	}
	if !strings.Contains(result, `"id":"gstack/qa"`) || strings.Contains(result, `"id":"gstack/plan-eng-review"`) {
		t.Fatalf("expected second suite page to include only gstack/qa, got %q", result)
	}
}

func TestSkillToolListActionSearchesGenerically(t *testing.T) {
	runtime := &fakeSkillRuntime{}
	tool := NewSkillTool(runtime)

	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "list", "query": "engineering", "limit": 1})
	if err != nil {
		t.Fatalf("list skills query: %v", err)
	}
	if !strings.Contains(result, `"mode":"search"`) || !strings.Contains(result, `"id":"gstack/plan-eng-review"`) {
		t.Fatalf("expected generic text query to find matching skill, got %q", result)
	}
}

func TestSkillToolExpandActionLoadsAdditionalSections(t *testing.T) {
	runtime := &fakeSkillRuntime{}
	if _, err := runtime.ActivateSkill("example"); err != nil {
		t.Fatalf("seed active skill: %v", err)
	}
	tool := NewSkillTool(runtime)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "expand",
		"name":     "example",
		"sections": []interface{}{"workflow"},
	})
	if err != nil {
		t.Fatalf("expand skill: %v", err)
	}
	for _, want := range []string{`"status":"expanded"`, `"expanded_sections":["workflow"]`} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected expand result to contain %q, got %q", want, result)
		}
	}
}

func TestSkillToolUnloadActionRemovesActiveSkill(t *testing.T) {
	runtime := &fakeSkillRuntime{}
	if _, err := runtime.ActivateSkill("example"); err != nil {
		t.Fatalf("seed active skill: %v", err)
	}
	tool := NewSkillTool(runtime)

	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "unload", "name": "example"})
	if err != nil {
		t.Fatalf("unload skill: %v", err)
	}
	for _, want := range []string{`"status":"unloaded"`, `"name":"example"`} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected unload result to contain %q, got %q", want, result)
		}
	}
}

func TestSkillToolInstallActionInstallsFromSource(t *testing.T) {
	runtime := &fakeSkillRuntime{}
	tool := NewSkillTool(runtime)

	if _, err := tool.Execute(context.Background(), map[string]interface{}{"action": "install", "source": "missing"}); err == nil {
		t.Fatal("expected missing source to fail")
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":          "install",
		"source":          "owner/repo",
		"name":            "playwright-cli",
		"timeout_seconds": 1,
	})
	if err != nil {
		t.Fatalf("install skill: %v", err)
	}
	for _, want := range []string{`"status":"installed"`, `"name":"playwright-cli"`, `"source":"owner/repo"`} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected install result to contain %q, got %q", want, result)
		}
	}
	if !runtime.installHadDeadline {
		t.Fatal("expected install context to have a deadline")
	}
}

func TestSkillToolSourcesActionReturnsCuratedSources(t *testing.T) {
	runtime := &fakeSkillRuntime{}
	tool := NewSkillTool(runtime)

	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "sources"})
	if err != nil {
		t.Fatalf("list skill sources: %v", err)
	}
	for _, want := range []string{`"sources"`, `"name":"playwright-cli"`, `"source":"owner/repo"`} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected source list result to contain %q, got %q", want, result)
		}
	}
}

func TestSkillToolSourcesActionSupportsSearchQuery(t *testing.T) {
	runtime := &fakeSkillRuntime{}
	tool := NewSkillTool(runtime)

	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "sources", "query": "react"})
	if err != nil {
		t.Fatalf("search skill sources: %v", err)
	}
	for _, want := range []string{`"name":"react"`, `"origin":"skillsh"`, `"summary":"Search result for react"`} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected queried source list to contain %q, got %q", want, result)
		}
	}
}
