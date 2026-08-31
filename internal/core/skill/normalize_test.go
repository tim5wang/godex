package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

type fakeFallbackNormalizer struct {
	document *NormalizedDocument
	core     string
}

func (f fakeFallbackNormalizer) Normalize(ctx context.Context, input NormalizationInput) (*NormalizedDocument, string, error) {
	_ = ctx
	_ = input
	return f.document, f.core, nil
}

type countingFallbackNormalizer struct {
	document *NormalizedDocument
	core     string
	calls    int
}

func (f *countingFallbackNormalizer) Normalize(ctx context.Context, input NormalizationInput) (*NormalizedDocument, string, error) {
	_ = ctx
	_ = input
	f.calls++
	return f.document, f.core, nil
}

type stubModelCaller struct {
	response string
}

func (s stubModelCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	_ = req
	return &protocol.Response{
		Content: []protocol.Block{protocol.TextBlock(s.response)},
	}, nil
}

func TestLoaderWritesNormalizedArtifactsForLegacyThirdPartySkill(t *testing.T) {
	skillsDir := t.TempDir()
	skillPath := filepath.Join(skillsDir, "round-table", "SKILL.md")
	raw := `/round-table <topic>

Use bash tools and smoke_test.sh before continuing.
Preferred search path: mcp__MiniMax__web_search.
Coordinate with rt-tech and rt-risk subagent roles.
`
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(raw), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	loader := NewLoader(skillsDir)
	skillDef, err := loader.Load("round-table")
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}

	if skillDef.Compatibility.Status != CompatibilityDegradedSupported {
		t.Fatalf("expected degraded support for third-party skill, got %+v", skillDef.Compatibility)
	}
	for _, want := range []string{"core_code", "background", "subagent", "mcp"} {
		if !contains(skillDef.RecommendedBundles, want) {
			t.Fatalf("expected recommended bundle %q, got %+v", want, skillDef.RecommendedBundles)
		}
	}

	docPath, corePath := normalizedArtifactPaths(skillPath)
	docData, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read normalized json: %v", err)
	}
	if !strings.Contains(string(docData), `"status": "degraded_supported"`) {
		t.Fatalf("expected compatibility status in normalized json, got %q", docData)
	}
	coreData, err := os.ReadFile(corePath)
	if err != nil {
		t.Fatalf("read normalized core: %v", err)
	}
	if got := strings.TrimSpace(string(coreData)); got == "" {
		t.Fatal("expected normalized core markdown to be written")
	}
}

func TestLoaderRunsConfiguredFallbackNormalizerOnlyWhenExplicit(t *testing.T) {
	skillsDir := t.TempDir()
	skillPath := filepath.Join(skillsDir, "market-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte("Legacy third-party skill body."), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	loader := NewLoader(skillsDir)
	normalizer := &countingFallbackNormalizer{
		document: &NormalizedDocument{
			Name:               "market-skill",
			Summary:            "Normalized summary",
			RecommendedBundles: []string{"background"},
			Sections:           []string{"core", "workflow"},
			Compatibility: Compatibility{
				Status: CompatibilityDegradedSupported,
			},
		},
		core: "Normalized core instructions.",
	}
	loader.SetFallbackNormalizer(normalizer)

	loaded, err := loader.Load("market-skill")
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	if normalizer.calls != 0 {
		t.Fatalf("expected ordinary load to avoid fallback normalizer, got %d calls", normalizer.calls)
	}
	if loaded.Description == "Normalized summary" {
		t.Fatalf("ordinary load should not use fallback-normalized description")
	}
	entry := loader.CatalogEntryFor(loaded)
	if !entry.CanNormalize || !entry.NeedsNormalization || entry.Normalized || entry.NormalizationStatus != "suggested" {
		t.Fatalf("expected legacy skill to suggest normalization, got %+v", entry)
	}

	skillDef, err := loader.NormalizeSkill(context.Background(), "market-skill")
	if err != nil {
		t.Fatalf("normalize skill: %v", err)
	}
	if normalizer.calls != 1 {
		t.Fatalf("expected explicit normalize to call fallback normalizer once, got %d", normalizer.calls)
	}
	if skillDef.Description != "Normalized summary" {
		t.Fatalf("expected fallback-normalized description, got %q", skillDef.Description)
	}
	if skillDef.Core != "Normalized core instructions." {
		t.Fatalf("expected fallback-normalized core, got %q", skillDef.Core)
	}
	if !contains(skillDef.RecommendedBundles, "background") {
		t.Fatalf("expected fallback-recommended background bundle, got %+v", skillDef.RecommendedBundles)
	}
	entry = loader.CatalogEntryFor(skillDef)
	if !entry.Normalized || entry.NeedsNormalization || entry.NormalizationStatus != "normalized" || entry.NormalizationSource != "llm" {
		t.Fatalf("expected normalized skill status after explicit normalize, got %+v", entry)
	}
}

func TestCatalogAvoidsFallbackNormalization(t *testing.T) {
	skillsDir := t.TempDir()
	workspaceDir := t.TempDir()
	skillPath := filepath.Join(skillsDir, "market-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte("Legacy third-party skill body."), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	normalizer := &countingFallbackNormalizer{
		document: &NormalizedDocument{
			Name:               "market-skill",
			Summary:            "Normalized summary",
			RecommendedBundles: []string{"background"},
			Sections:           []string{"core"},
			Compatibility: Compatibility{
				Status: CompatibilityDegradedSupported,
			},
		},
		core: "Normalized core instructions.",
	}

	loader := NewLoader(skillsDir)
	loader.SetFallbackNormalizer(normalizer)

	if _, err := loader.Catalog(workspaceDir); err != nil {
		t.Fatalf("first catalog: %v", err)
	}
	if _, err := loader.Catalog(workspaceDir); err != nil {
		t.Fatalf("second catalog: %v", err)
	}
	if normalizer.calls != 0 {
		t.Fatalf("expected catalog to avoid fallback normalizer, got %d calls", normalizer.calls)
	}
}

func TestLoaderDoesNotFallbackNormalizeStructuredSkillMissingOptionalMetadata(t *testing.T) {
	skillsDir := t.TempDir()
	skillPath := filepath.Join(skillsDir, "gstack", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
name: gstack
description: Fast headless browser for QA testing and site dogfooding.
---
## Preamble
Run setup.

## Workflow
Use browser automation.`), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	normalizer := &countingFallbackNormalizer{
		document: &NormalizedDocument{
			Name:    "gstack",
			Summary: "Should not be used",
		},
		core: "Should not be used.",
	}
	loader := NewLoader(skillsDir)
	loader.SetFallbackNormalizer(normalizer)

	skillDef, err := loader.Load("gstack")
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	if normalizer.calls != 0 {
		t.Fatalf("expected structured skill to avoid fallback normalizer, got %d calls", normalizer.calls)
	}
	if skillDef.Description != "Fast headless browser for QA testing and site dogfooding." {
		t.Fatalf("expected frontmatter description, got %q", skillDef.Description)
	}
}

func TestLoaderAcceptsLegacyNormalizedArtifactsWithoutSchemaVersion(t *testing.T) {
	skillsDir := t.TempDir()
	workspaceDir := t.TempDir()
	skillPath := filepath.Join(skillsDir, "playwright-cli", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	raw := `---
name: playwright-cli
description: Browser automation helper.
---
## Core
Use Playwright.`
	if err := os.WriteFile(skillPath, []byte(raw), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	docPath, corePath := normalizedArtifactPaths(skillPath)
	legacyDoc, err := json.MarshalIndent(map[string]any{
		"source_hash": sourceHash(raw),
		"name":        "playwright-cli",
		"summary":     "Legacy normalized summary",
		"sections":    []string{"core"},
		"compatibility": map[string]any{
			"status": "native_supported",
		},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy doc: %v", err)
	}
	if err := os.WriteFile(docPath, append(legacyDoc, '\n'), 0644); err != nil {
		t.Fatalf("write legacy normalized doc: %v", err)
	}
	if err := os.WriteFile(corePath, []byte("Legacy core.\n"), 0644); err != nil {
		t.Fatalf("write legacy core: %v", err)
	}

	normalizer := &countingFallbackNormalizer{
		document: &NormalizedDocument{
			Name:    "playwright-cli",
			Summary: "Should not be used",
		},
		core: "Should not be used.",
	}
	loader := NewLoader(skillsDir)
	loader.SetFallbackNormalizer(normalizer)

	items, err := loader.Catalog(workspaceDir)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one catalog item, got %+v", items)
	}
	if items[0].Description != "Legacy normalized summary" {
		t.Fatalf("expected legacy normalized summary, got %+v", items[0])
	}
	if normalizer.calls != 0 {
		t.Fatalf("expected legacy normalized artifacts to bypass fallback normalizer, got %d calls", normalizer.calls)
	}
}

func TestLoaderOverridesFallbackSourceHashBeforePersisting(t *testing.T) {
	skillsDir := t.TempDir()
	skillPath := filepath.Join(skillsDir, "market-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	raw := "Legacy third-party skill body."
	if err := os.WriteFile(skillPath, []byte(raw), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	loader := NewLoader(skillsDir)
	loader.SetFallbackNormalizer(fakeFallbackNormalizer{
		document: &NormalizedDocument{
			SourceHash:         "sha256:not-real",
			Name:               "market-skill",
			Summary:            "Normalized summary",
			RecommendedBundles: []string{"background"},
			Sections:           []string{"core"},
			Compatibility: Compatibility{
				Status: CompatibilityDegradedSupported,
			},
		},
		core: "Normalized core instructions.",
	})

	if _, err := loader.NormalizeSkill(context.Background(), "market-skill"); err != nil {
		t.Fatalf("normalize skill: %v", err)
	}

	docPath, _ := normalizedArtifactPaths(skillPath)
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read normalized doc: %v", err)
	}
	var doc NormalizedDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode normalized doc: %v", err)
	}
	if doc.SourceHash != sourceHash(raw) {
		t.Fatalf("expected persisted source hash %q, got %q", sourceHash(raw), doc.SourceHash)
	}
	if doc.GeneratedBy != "llm" {
		t.Fatalf("expected persisted LLM normalization source, got %q", doc.GeneratedBy)
	}
}

func TestLLMNormalizerAcceptsStringOrArrayFields(t *testing.T) {
	normalizer := NewLLMNormalizer(stubModelCaller{
		response: `{
  "source_hash": "sha256:test",
  "name": "playwright-cli",
  "summary": "Browser automation helper",
  "when_to_use": "Use when the user needs browser automation",
  "argument_hint": "optional target URL",
  "paths": "web/**",
  "recommended_bundles": "core_code",
  "sections": "core",
  "compatibility": {"status":"native_supported"},
  "warnings": "Generated by fallback",
  "core": "Core instructions"
}`,
	}, "test-model", 256)

	doc, core, err := normalizer.Normalize(context.Background(), NormalizationInput{
		Name: "playwright-cli",
		Path: "skills/playwright-cli/SKILL.md",
		Raw:  "raw skill",
	})
	if err != nil {
		t.Fatalf("normalize skill: %v", err)
	}
	if got := strings.Join(doc.WhenToUse, ","); got != "Use when the user needs browser automation" {
		t.Fatalf("unexpected when_to_use: %q", got)
	}
	if got := strings.Join(doc.Paths, ","); got != "web/**" {
		t.Fatalf("unexpected paths: %q", got)
	}
	if got := strings.Join(doc.RecommendedBundles, ","); got != "core_code" {
		t.Fatalf("unexpected bundles: %q", got)
	}
	if got := strings.Join(doc.Sections, ","); got != "core" {
		t.Fatalf("unexpected sections: %q", got)
	}
	if got := strings.Join(doc.Warnings, ","); got != "Generated by fallback" {
		t.Fatalf("unexpected warnings: %q", got)
	}
	if core != "Core instructions" {
		t.Fatalf("unexpected core: %q", core)
	}
}

func TestAnalyzeRequirementsExtractsExecutableHintsFromAllowedTools(t *testing.T) {
	parsed := &Skill{
		Name: "playwright-cli",
		Content: `---
name: playwright-cli
allowed-tools: Bash(playwright-cli:*) Bash(npx:*) Bash(npm:*)
---
## Core
Use Playwright.`,
	}

	req, _ := analyzeRequirements(parsed)
	for _, want := range []string{"Bash(playwright-cli:*)", "Bash(npx:*)", "Bash(npm:*)"} {
		if !contains(req.AllowedTools, want) {
			t.Fatalf("expected allowed tool %q, got %+v", want, req.AllowedTools)
		}
	}
	for _, want := range []string{"playwright-cli", "npx", "npm"} {
		if !contains(req.Executables, want) {
			t.Fatalf("expected executable hint %q, got %+v", want, req.Executables)
		}
	}
}

func TestAllowedToolsCanRecommendDesktopBundle(t *testing.T) {
	got := mapAllowedToolsToBundles([]string{"Desktop(click:*)"})
	if !contains(got, "desktop") {
		t.Fatalf("expected desktop bundle recommendation, got %+v", got)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
