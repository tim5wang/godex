package skill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoaderLoadsDirectorySkills(t *testing.T) {
	skillsDir := t.TempDir()

	dirSkillPath := filepath.Join(skillsDir, "dir-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(dirSkillPath), 0755); err != nil {
		t.Fatalf("mkdir dir skill: %v", err)
	}
	if err := os.WriteFile(dirSkillPath, []byte("directory content"), 0644); err != nil {
		t.Fatalf("write dir skill: %v", err)
	}

	loader := NewLoader(skillsDir)

	dirSkill, err := loader.Load("dir-skill")
	if err != nil {
		t.Fatalf("load directory skill: %v", err)
	}
	if dirSkill.Content != "directory content" {
		t.Fatalf("expected directory content, got %q", dirSkill.Content)
	}
}

func TestLoaderParsesFrontmatterAndSections(t *testing.T) {
	skillsDir := t.TempDir()
	skillPath := filepath.Join(skillsDir, "review-helper", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}

	content := `---
name: review-helper
description: Review code changes with a structured checklist
when_to_use:
  - when the user asks for review
paths:
  - "docs/**"
recommended_bundles:
  - core_code
  - background
sections:
  - core
  - workflow
  - references
---
## Core
Focus on correctness, regressions, and missing tests.

## Workflow
1. Read the diff.
2. Check risky behavior changes.

## References
See docs/review.md for the report template.
`
	if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	loader := NewLoader(skillsDir)
	skill, err := loader.Load("review-helper")
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}

	if skill.Name != "review-helper" {
		t.Fatalf("expected parsed name, got %q", skill.Name)
	}
	if skill.ID != "review-helper" {
		t.Fatalf("expected stable id review-helper, got %q", skill.ID)
	}
	if skill.Description != "Review code changes with a structured checklist" {
		t.Fatalf("unexpected description %q", skill.Description)
	}
	if skill.Core != "Focus on correctness, regressions, and missing tests." {
		t.Fatalf("unexpected core content %q", skill.Core)
	}
	if got := skill.SectionOrder; len(got) != 3 || got[0] != "core" || got[1] != "workflow" || got[2] != "references" {
		t.Fatalf("unexpected section order %#v", got)
	}
	if skill.Sections["workflow"] == "" || skill.Sections["references"] == "" {
		t.Fatalf("expected parsed sections, got %+v", skill.Sections)
	}
}

func TestExamplePackageDeveloperSkillIsLoadable(t *testing.T) {
	examplesDir := filepath.Join("..", "..", "..", "examples", "skills")
	skillPath := filepath.Join(examplesDir, "package-developer", "SKILL.md")
	docPath, corePath := normalizedArtifactPaths(skillPath)
	t.Cleanup(func() {
		_ = os.Remove(docPath)
		_ = os.Remove(corePath)
	})
	loader := NewLoader(examplesDir)
	skill, err := loader.Load("package-developer")
	if err != nil {
		t.Fatalf("load package-developer example skill: %v", err)
	}
	if skill.Description == "" || !strings.Contains(strings.ToLower(skill.Description), "package") {
		t.Fatalf("expected package-developer skill to describe package workflows, got %q", skill.Description)
	}
	if !strings.Contains(skill.Core, "godex.package.yaml") {
		t.Fatalf("expected package-developer core workflow to mention godex.package.yaml, got %q", skill.Core)
	}
}

func TestCatalogFiltersSkillsByWorkspacePaths(t *testing.T) {
	skillsDir := t.TempDir()
	workspaceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceDir, "README.md"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceDir, "docs"), 0755); err != nil {
		t.Fatalf("mkdir docs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "docs", "review.md"), []byte("guide"), 0644); err != nil {
		t.Fatalf("write docs file: %v", err)
	}

	matchingSkillPath := filepath.Join(skillsDir, "matching", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(matchingSkillPath), 0755); err != nil {
		t.Fatalf("mkdir matching skill dir: %v", err)
	}
	if err := os.WriteFile(matchingSkillPath, []byte(`---
description: Matches docs
paths:
  - "docs/**"
---
## Core
Use when docs files are relevant.`), 0644); err != nil {
		t.Fatalf("write matching skill: %v", err)
	}

	otherSkillPath := filepath.Join(skillsDir, "non-matching", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(otherSkillPath), 0755); err != nil {
		t.Fatalf("mkdir non-matching skill dir: %v", err)
	}
	if err := os.WriteFile(otherSkillPath, []byte(`---
description: Matches frontend files
paths:
  - "web/**"
---
## Core
Use when frontend files are relevant.`), 0644); err != nil {
		t.Fatalf("write non-matching skill: %v", err)
	}

	loader := NewLoader(skillsDir)
	items, err := loader.Catalog(workspaceDir)
	if err != nil {
		t.Fatalf("catalog skills: %v", err)
	}
	if len(items) != 1 || items[0].Name != "matching" {
		t.Fatalf("expected only matching skill, got %+v", items)
	}
}

func TestCatalogContinuesWhenOneSkillIsBrokenAndDoesNotWriteArtifacts(t *testing.T) {
	skillsDir := t.TempDir()
	workspaceDir := t.TempDir()

	goodSkillPath := filepath.Join(skillsDir, "good-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(goodSkillPath), 0755); err != nil {
		t.Fatalf("mkdir good skill dir: %v", err)
	}
	if err := os.WriteFile(goodSkillPath, []byte("Legacy skill body."), 0644); err != nil {
		t.Fatalf("write good skill: %v", err)
	}

	badSkillPath := filepath.Join(skillsDir, "broken-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(badSkillPath), 0755); err != nil {
		t.Fatalf("mkdir bad skill dir: %v", err)
	}
	if err := os.WriteFile(badSkillPath, []byte("---\ndescription: [oops\n---\n## Core\nBroken"), 0644); err != nil {
		t.Fatalf("write bad skill: %v", err)
	}

	loader := NewLoader(skillsDir)
	items, err := loader.Catalog(workspaceDir)
	if err != nil {
		t.Fatalf("catalog skills: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected catalog to include good and broken skills, got %+v", items)
	}

	var good CatalogEntry
	var broken CatalogEntry
	for _, item := range items {
		switch item.Name {
		case "good-skill":
			good = item
		case "broken-skill":
			broken = item
		}
	}
	if good.Name != "good-skill" {
		t.Fatalf("expected good skill entry, got %+v", items)
	}
	if broken.Name != "broken-skill" {
		t.Fatalf("expected broken skill placeholder, got %+v", items)
	}
	if broken.Compatibility.Status != CompatibilityUnsupported {
		t.Fatalf("expected broken skill to be unsupported, got %+v", broken.Compatibility)
	}
	if len(broken.Warnings) == 0 {
		t.Fatalf("expected broken skill warning, got %+v", broken)
	}

	docPath, corePath := normalizedArtifactPaths(goodSkillPath)
	if _, err := os.Stat(docPath); !os.IsNotExist(err) {
		t.Fatalf("expected catalog to avoid writing normalized json, got err=%v", err)
	}
	if _, err := os.Stat(corePath); !os.IsNotExist(err) {
		t.Fatalf("expected catalog to avoid writing normalized core, got err=%v", err)
	}
}

func TestGetSectionsRejectsUnknownSection(t *testing.T) {
	skillsDir := t.TempDir()
	skillPath := filepath.Join(skillsDir, "example", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`## Core
Core text.

## Workflow
Workflow text.`), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	loader := NewLoader(skillsDir)
	if _, err := loader.GetSections("example", []string{"workflow", "templates"}); err == nil {
		t.Fatal("expected missing templates section to fail")
	} else if !errors.Is(err, ErrSkillInvalidRequest) {
		t.Fatalf("expected invalid skill request, got %v", err)
	}
}

func TestLoaderKeepsStableIDWhenFrontmatterNameDiffers(t *testing.T) {
	skillsDir := t.TempDir()
	skillPath := filepath.Join(skillsDir, "review-helper", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
name: Review Helper Deluxe
description: Review code changes with a polished checklist
---
## Core
Review carefully.`), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	loader := NewLoader(skillsDir)
	skillDef, err := loader.Load("review-helper")
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	if skillDef.ID != "review-helper" {
		t.Fatalf("expected stable id review-helper, got %q", skillDef.ID)
	}
	if skillDef.Name != "Review Helper Deluxe" {
		t.Fatalf("expected display name, got %q", skillDef.Name)
	}

	entry := loader.CatalogEntryFor(skillDef)
	if entry.ID != "review-helper" || entry.Name != "Review Helper Deluxe" {
		t.Fatalf("unexpected catalog entry %+v", entry)
	}
	if entry.NeedsNormalization || entry.Normalized || entry.NormalizationStatus != "not_needed" {
		t.Fatalf("expected structured skill to avoid normalization prompt, got %+v", entry)
	}

	resolvedID, err := loader.ResolveID("Review Helper Deluxe")
	if err != nil {
		t.Fatalf("resolve display name: %v", err)
	}
	if resolvedID != "review-helper" {
		t.Fatalf("expected resolved id review-helper, got %q", resolvedID)
	}
}

func TestLoaderDiscoverIgnoresNonSkillFiles(t *testing.T) {
	skillsDir := t.TempDir()
	validDirSkillPath := filepath.Join(skillsDir, "review-helper", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(validDirSkillPath), 0755); err != nil {
		t.Fatalf("mkdir dir skill: %v", err)
	}
	if err := os.WriteFile(validDirSkillPath, []byte("dir skill"), 0644); err != nil {
		t.Fatalf("write dir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "writer.md"), []byte("stray markdown"), 0644); err != nil {
		t.Fatalf("write markdown file: %v", err)
	}
	for _, name := range []string{"README", ".DS_Store", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(skillsDir, name), []byte("ignore me"), 0644); err != nil {
			t.Fatalf("write stray file %s: %v", name, err)
		}
	}

	loader := NewLoader(skillsDir)
	names, err := loader.Discover()
	if err != nil {
		t.Fatalf("discover skills: %v", err)
	}
	if len(names) != 1 || names[0] != "review-helper" {
		t.Fatalf("expected only discoverable skills, got %#v", names)
	}
}

func TestLoaderDiscoversNestedSkillsInsideInstalledSuite(t *testing.T) {
	skillsDir := t.TempDir()

	rootSkillPath := filepath.Join(skillsDir, "gstack", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(rootSkillPath), 0755); err != nil {
		t.Fatalf("mkdir root skill: %v", err)
	}
	if err := os.WriteFile(rootSkillPath, []byte(`---
name: gstack
description: Browser testing helper
---
## Core
Use browser automation.`), 0644); err != nil {
		t.Fatalf("write root skill: %v", err)
	}

	nestedSkillPath := filepath.Join(skillsDir, "gstack", "plan-eng-review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(nestedSkillPath), 0755); err != nil {
		t.Fatalf("mkdir nested skill: %v", err)
	}
	if err := os.WriteFile(nestedSkillPath, []byte(`---
name: plan-eng-review
description: Engineering manager architecture review
---
## Core
Review architecture, edge cases, and tests.`), 0644); err != nil {
		t.Fatalf("write nested skill: %v", err)
	}

	loader := NewLoader(skillsDir)
	names, err := loader.Discover()
	if err != nil {
		t.Fatalf("discover skills: %v", err)
	}
	want := []string{"gstack", "gstack/plan-eng-review"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("expected nested suite skills %#v, got %#v", want, names)
	}

	skillDef, err := loader.Load("gstack/plan-eng-review")
	if err != nil {
		t.Fatalf("load nested skill: %v", err)
	}
	if skillDef.ID != "gstack/plan-eng-review" || skillDef.Name != "plan-eng-review" {
		t.Fatalf("unexpected nested skill identity: %#v", skillDef)
	}
	if !strings.Contains(skillDef.Description, "Engineering manager") {
		t.Fatalf("expected nested skill description, got %q", skillDef.Description)
	}
}

func TestLoaderAddsRuntimeDependencyDiagnostics(t *testing.T) {
	skillsDir := t.TempDir()
	skillPath := filepath.Join(skillsDir, "playwright-cli", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
name: playwright-cli
description: Browser automation helper
allowed-tools: Bash(playwright-cli:*) Bash(npx:*) Bash(npm:*)
---
## Core
Use Playwright CLI to automate browser work.`), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	t.Setenv("PATH", "")

	loader := NewLoader(skillsDir)
	skillDef, err := loader.Load("playwright-cli")
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}

	for _, want := range []string{"playwright-cli", "npx", "npm"} {
		if !contains(skillDef.Requires.Executables, want) {
			t.Fatalf("expected executable hint %q, got %+v", want, skillDef.Requires.Executables)
		}
		if !contains(skillDef.Compatibility.MissingDependencies, want) {
			t.Fatalf("expected missing dependency %q, got %+v", want, skillDef.Compatibility.MissingDependencies)
		}
	}
	if skillDef.Compatibility.Status != CompatibilityDegradedSupported {
		t.Fatalf("expected degraded support, got %+v", skillDef.Compatibility)
	}
	warningText := strings.Join(skillDef.Warnings, " ")
	if !strings.Contains(warningText, "Missing local executables") {
		t.Fatalf("expected dependency warning, got %+v", skillDef.Warnings)
	}
}
