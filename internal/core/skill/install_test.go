package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoaderInstallFromLocalDirectory(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")
	sourceDir := filepath.Join(workspace, "remote-skill")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte(`---
description: Capture screenshots with Playwright
when_to_use: take screenshots
recommended_bundles: web
sections:
  - core
  - workflow
---
## Core
Use Playwright to automate browser screenshots.

## Workflow
Open the page first, then capture evidence.`), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".godex"), 0755); err != nil {
		t.Fatalf("mkdir .godex: %v", err)
	}
	configData, err := json.Marshal(map[string]any{
		"sources": []map[string]any{
			{
				"id":         "remote-skill",
				"name":       "remote-skill",
				"summary":    "Remote browser skill.",
				"source":     sourceDir,
				"skill_name": "remote-skill",
				"categories": []string{"browser", "automation"},
				"trust":      "verified",
				"version":    "v1.2.3",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal source config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".godex", "skill-sources.json"), configData, 0644); err != nil {
		t.Fatalf("write source config: %v", err)
	}

	loader := NewLoader(skillsDir)
	result, err := loader.Install(sourceDir, "")
	if err != nil {
		t.Fatalf("install skill: %v", err)
	}
	if result.Name != "remote-skill" {
		t.Fatalf("expected installed name remote-skill, got %q", result.Name)
	}
	if result.Status != "installed" {
		t.Fatalf("expected installed status, got %q", result.Status)
	}
	if result.Trust != "verified" || result.Version != "v1.2.3" {
		t.Fatalf("expected remembered trust/version, got %#v", result)
	}
	if len(result.Categories) != 2 || result.Categories[0] != "browser" {
		t.Fatalf("expected remembered categories, got %#v", result.Categories)
	}

	skillPath := filepath.Join(skillsDir, "remote-skill", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Fatalf("expected installed skill at %s: %v", skillPath, err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "remote-skill", installMetadataFileName)); err != nil {
		t.Fatalf("expected install metadata file: %v", err)
	}

	catalog, err := loader.Catalog(workspace)
	if err != nil {
		t.Fatalf("catalog after install: %v", err)
	}
	if len(catalog) != 1 || catalog[0].Name != "remote-skill" {
		t.Fatalf("unexpected catalog after install: %#v", catalog)
	}
	if catalog[0].Version != "v1.2.3" || catalog[0].InstallMemory == nil {
		t.Fatalf("expected catalog to expose install memory, got %#v", catalog[0])
	}
}

func TestLoaderRemoveInstalledSkill(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")
	sourceDir := filepath.Join(workspace, "skill-creator")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte(`---
name: Skill Creator
description: Create skills
---
## Core
Create high quality skills.
`), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	loader := NewLoader(skillsDir)
	if _, err := loader.Install(sourceDir, ""); err != nil {
		t.Fatalf("install skill: %v", err)
	}

	result, err := loader.Remove("Skill Creator")
	if err != nil {
		t.Fatalf("remove skill: %v", err)
	}
	if result.ID != "skill-creator" || result.Status != "removed" {
		t.Fatalf("unexpected remove result: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "skill-creator")); !os.IsNotExist(err) {
		t.Fatalf("expected skill directory to be removed, stat err=%v", err)
	}
	catalog, err := loader.Catalog(workspace)
	if err != nil {
		t.Fatalf("catalog after remove: %v", err)
	}
	if len(catalog) != 0 {
		t.Fatalf("expected empty catalog after remove, got %#v", catalog)
	}
}

func TestLoaderInstallRequiresNameWhenSourceContainsMultipleSkills(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")
	sourceRoot := filepath.Join(workspace, "market")
	for _, name := range []string{"alpha", "beta"} {
		skillPath := filepath.Join(sourceRoot, "skills", name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(skillPath, []byte("Skill body"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	loader := NewLoader(skillsDir)
	_, err := loader.Install(sourceRoot, "")
	if err == nil {
		t.Fatal("expected multiple-skill source to require a name")
	}
	if !strings.Contains(err.Error(), "multiple skills") {
		t.Fatalf("expected multiple skills error, got %v", err)
	}

	result, err := loader.Install(sourceRoot, "beta")
	if err != nil {
		t.Fatalf("install named skill: %v", err)
	}
	if result.Name != "beta" {
		t.Fatalf("expected installed name beta, got %q", result.Name)
	}
}

func TestLoaderInstallIgnoresRootReadmeWhenDiscoveringCandidates(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")
	sourceRoot := filepath.Join(workspace, "market")
	if err := os.MkdirAll(filepath.Join(sourceRoot, "alpha"), 0755); err != nil {
		t.Fatalf("mkdir alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "README.md"), []byte("# docs only"), 0644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "alpha", "SKILL.md"), []byte("Skill body"), 0644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}

	loader := NewLoader(skillsDir)
	result, err := loader.Install(sourceRoot, "")
	if err != nil {
		t.Fatalf("install single discoverable skill: %v", err)
	}
	if result.Name != "alpha" {
		t.Fatalf("expected installed name alpha, got %q", result.Name)
	}
}

func TestLoaderInstallMatchesRequestedFrontmatterName(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")
	sourceRoot := filepath.Join(workspace, "market")
	skillPath := filepath.Join(sourceRoot, "cn-stock-sim", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir stock skill: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
name: stock-trading
description: Simulated stock trading helper
---
## Core
Trade carefully.`), 0644); err != nil {
		t.Fatalf("write stock skill: %v", err)
	}

	loader := NewLoader(skillsDir)
	result, err := loader.Install(sourceRoot, "stock-trading")
	if err != nil {
		t.Fatalf("install frontmatter-matched skill: %v", err)
	}
	if result.ID != "stock-trading" || result.Name != "stock-trading" {
		t.Fatalf("expected stable installed stock-trading skill, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "stock-trading", "SKILL.md")); err != nil {
		t.Fatalf("expected installed stock-trading directory: %v", err)
	}
}

func TestLoaderInstallFindsNestedClaudeSkill(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")
	sourceRoot := filepath.Join(workspace, "market")
	skillPath := filepath.Join(sourceRoot, "workflows", "stock-trader-workflow", ".claude", "skills", "hk-stock-analysis", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir nested skill: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
name: hk-stock-analysis
description: Hong Kong stock market analysis helper
---
## Core
Analyze HK stocks.`), 0644); err != nil {
		t.Fatalf("write nested skill: %v", err)
	}

	loader := NewLoader(skillsDir)
	result, err := loader.Install(sourceRoot, "hk-stock-analysis")
	if err != nil {
		t.Fatalf("install nested .claude skill: %v", err)
	}
	if result.ID != "hk-stock-analysis" || result.Name != "hk-stock-analysis" {
		t.Fatalf("expected hk-stock-analysis skill, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "hk-stock-analysis", "SKILL.md")); err != nil {
		t.Fatalf("expected installed nested skill: %v", err)
	}
}

func TestLoaderInstallCopiesInternalSymlinkDirectory(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")
	sourceRoot := filepath.Join(workspace, "market")
	if err := os.MkdirAll(filepath.Join(sourceRoot, "open-gstack-browser"), 0755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "SKILL.md"), []byte(`---
name: gstack
description: Browser testing helper
---
## Core
Use browser automation.`), 0644); err != nil {
		t.Fatalf("write root skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "open-gstack-browser", "SKILL.md"), []byte("Nested browser skill"), 0644); err != nil {
		t.Fatalf("write nested skill: %v", err)
	}
	if err := os.Symlink("open-gstack-browser", filepath.Join(sourceRoot, "connect-chrome")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	loader := NewLoader(skillsDir)
	result, err := loader.Install(sourceRoot, "gstack")
	if err != nil {
		t.Fatalf("install symlinked root skill: %v", err)
	}
	if result.ID != "gstack" || result.Name != "gstack" {
		t.Fatalf("expected gstack root skill, got %#v", result)
	}
	target, err := os.Readlink(filepath.Join(skillsDir, "gstack", "connect-chrome"))
	if err != nil {
		t.Fatalf("expected installed symlink: %v", err)
	}
	if target != "open-gstack-browser" {
		t.Fatalf("expected symlink target open-gstack-browser, got %q", target)
	}
}

func TestLoaderInstallRejectsEscapingSymlink(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")
	sourceRoot := filepath.Join(workspace, "market")
	if err := os.MkdirAll(sourceRoot, 0755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "SKILL.md"), []byte(`---
name: unsafe-link
description: Unsafe link
---
## Core
Do not install external links.`), 0644); err != nil {
		t.Fatalf("write root skill: %v", err)
	}
	if err := os.Symlink("..", filepath.Join(sourceRoot, "outside")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	loader := NewLoader(skillsDir)
	_, err := loader.Install(sourceRoot, "unsafe-link")
	if err == nil {
		t.Fatal("expected escaping symlink to be rejected")
	}
	if !strings.Contains(err.Error(), "outside skill source") {
		t.Fatalf("expected outside skill source error, got %v", err)
	}
}

func TestLoaderInstallFindsExampleSkillsByName(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")
	sourceRoot := filepath.Join(workspace, "repo")
	skillPath := filepath.Join(sourceRoot, "examples", "skills", "browser-assist", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir example skill: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
name: browser-assist
description: Browser handoff helper
---
## Core
Use browser handoff and resume.`), 0644); err != nil {
		t.Fatalf("write example skill: %v", err)
	}

	loader := NewLoader(skillsDir)
	result, err := loader.Install(sourceRoot, "browser-assist")
	if err != nil {
		t.Fatalf("install example skill: %v", err)
	}
	if result.ID != "browser-assist" || result.Name != "browser-assist" {
		t.Fatalf("expected browser-assist skill, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "browser-assist", "SKILL.md")); err != nil {
		t.Fatalf("expected installed browser-assist skill: %v", err)
	}
}

func TestNormalizeInstallRequestSupportsOwnerRepoSkill(t *testing.T) {
	source, name := normalizeInstallRequest("meo9rhsan3492-cell/cn-stock-sim/stock-trading", "")
	if source != "meo9rhsan3492-cell/cn-stock-sim" || name != "stock-trading" {
		t.Fatalf("unexpected normalized request source=%q name=%q", source, name)
	}
}

func TestNormalizeInstallRequestSupportsSkillshID(t *testing.T) {
	source, name := normalizeInstallRequest("skillsh:nicepkg/ai-workflow/hk-stock-analysis", "")
	if source != "nicepkg/ai-workflow" || name != "hk-stock-analysis" {
		t.Fatalf("unexpected normalized skills.sh request source=%q name=%q", source, name)
	}
}

func TestNormalizeInstallRequestPreservesLocalPath(t *testing.T) {
	workspace := t.TempDir()
	sourceDir := filepath.Join(workspace, "market", "stock-trading")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("mkdir local source dir: %v", err)
	}

	source, name := normalizeInstallRequest(sourceDir, "")
	if source != sourceDir || name != "" {
		t.Fatalf("expected local path to stay intact, got source=%q name=%q", source, name)
	}
}
