package packages

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePackageManifest(t *testing.T, dir, manifest string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func TestParseRequirementForms(t *testing.T) {
	tests := []struct {
		raw      string
		wantKind string
		wantName string
		wantCons string
		wantErr  bool
	}{
		{raw: "review-kit", wantKind: "package", wantName: "review-kit"},
		{raw: "review-kit@1", wantKind: "package", wantName: "review-kit", wantCons: "1"},
		{raw: "review-kit@>=0.2.0", wantKind: "package", wantName: "review-kit", wantCons: ">=0.2.0"},
		{raw: "review-kit@^1.2.0", wantKind: "package", wantName: "review-kit", wantCons: "^1.2.0"},
		{raw: "godex:log@1", wantKind: "capability", wantName: "godex:log", wantCons: "1"},
		{raw: "tool:read_file", wantKind: "capability", wantName: "tool:read_file"},
		{raw: "  godex:tool-provider@1  ", wantKind: "capability", wantName: "godex:tool-provider", wantCons: "1"},
		{raw: "bad capability@x", wantKind: "capability", wantErr: true},
		{raw: "bad name@1", wantErr: true},
		{raw: "", wantErr: true},
		{raw: "@1", wantErr: true},
	}
	for _, tt := range tests {
		req, err := ParseRequirement(tt.raw)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseRequirement(%q): expected error, got %+v", tt.raw, req)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRequirement(%q): %v", tt.raw, err)
			continue
		}
		if req.Kind != tt.wantKind || req.Name != tt.wantName || req.Constraint != tt.wantCons {
			t.Errorf("ParseRequirement(%q) = %+v, want kind=%s name=%s constraint=%q", tt.raw, req, tt.wantKind, tt.wantName, tt.wantCons)
		}
	}
}

func TestVersionConstraintMatching(t *testing.T) {
	tests := []struct {
		constraint string
		version    string
		want       bool
	}{
		{"", "0.1.0", true},
		{"*", "9.9.9", true},
		{"1", "1.2.3", true},
		{"1", "2.0.0", false},
		{"1.2", "1.2.9", true},
		{"1.2", "1.3.0", false},
		{"1.2.3", "1.2.3", true},
		{"1.2.3", "1.2.4", false},
		{">=1.2.0", "1.2.0", true},
		{">=1.2.0", "1.2.1", true},
		{">=1.2.0", "1.1.9", false},
		{">1.2.0", "1.2.0", false},
		{">1.2.0", "1.2.1", true},
		{"<2.0.0", "1.9.9", true},
		{"<2.0.0", "2.0.0", false},
		{"<=1.2.3", "1.2.3", true},
		{"<=1.2.3", "1.2.4", false},
		{"^1.2.0", "1.9.9", true},
		{"^1.2.0", "2.0.0", false},
		{"^1.2.0", "1.1.9", false},
		{"~1.2.3", "1.2.9", true},
		{"~1.2.3", "1.3.0", false},
		{"1.x", "1.4.0", true},
		{"1.x", "2.0.0", false},
		{"v1.2.0", "1.2.0", true},
	}
	for _, tt := range tests {
		req := Requirement{Kind: "package", Name: "pkg", Constraint: tt.constraint}
		if got := req.satisfiedByVersion(tt.version); got != tt.want {
			t.Errorf("constraint %q vs version %q = %v, want %v", tt.constraint, tt.version, got, tt.want)
		}
	}
}

func TestCapabilitySatisfaction(t *testing.T) {
	tests := []struct {
		req      string
		provided string
		want     bool
	}{
		{"godex:log@1", "godex:log@1", true},
		{"godex:log@1", "godex:log@2", false},
		{"godex:log", "godex:log@2", true},
		{"godex:log@1", "godex:log", false},
		{"godex:log@1", "godex:other@1", false},
	}
	for _, tt := range tests {
		req, err := ParseRequirement(tt.req)
		if err != nil {
			t.Fatalf("parse %q: %v", tt.req, err)
		}
		if got := req.satisfiedByCapability(tt.provided); got != tt.want {
			t.Errorf("req %q vs provided %q = %v, want %v", tt.req, tt.provided, got, tt.want)
		}
	}
}

func TestValidateCandidateDependenciesMissingAndConflict(t *testing.T) {
	installed := []Entry{
		{Name: "base", Version: "0.5.0", Provides: []string{"godex:base@1"}},
	}
	candidate := Entry{
		Name:     "app",
		Version:  "0.1.0",
		Requires: []string{"base@>=1.0.0", "missing@1", "godex:base@1", "godex:nope@1"},
	}
	report := ValidateCandidateDependencies(candidate, installed)
	if report.Empty() {
		t.Fatal("expected dependency issues, got none")
	}
	// missing@1 and godex:nope@1 are missing; base@>=1.0.0 conflicts (0.5.0 installed).
	if len(report.Missing) != 2 {
		t.Fatalf("expected 2 missing (missing@1, godex:nope@1), got %v", report.Missing)
	}
	if len(report.Conflicts) != 1 {
		t.Fatalf("expected 1 version conflict (base@>=1.0.0 vs 0.5.0), got %v", report.Conflicts)
	}
	if !strings.Contains(report.Conflicts[0], "base@>=1.0.0") {
		t.Fatalf("unexpected conflict message: %q", report.Conflicts[0])
	}
}

func TestValidateCandidateDependenciesVersionConflict(t *testing.T) {
	installed := []Entry{{Name: "base", Version: "0.5.0"}}
	candidate := Entry{Name: "app", Version: "0.1.0", Requires: []string{"base@>=1.0.0"}}
	report := ValidateCandidateDependencies(candidate, installed)
	if len(report.Conflicts) != 1 {
		t.Fatalf("expected 1 version conflict, got %v", report.Conflicts)
	}
	if !strings.Contains(report.Conflicts[0], "base@>=1.0.0") || !strings.Contains(report.Conflicts[0], "0.5.0") {
		t.Fatalf("unexpected conflict message: %q", report.Conflicts[0])
	}
}

func TestValidateCandidateDependenciesCapabilityFromPackage(t *testing.T) {
	installed := []Entry{{Name: "provider", Version: "1.0.0", Provides: []string{"godex:tool-provider@1"}}}
	candidate := Entry{Name: "consumer", Version: "0.1.0", Requires: []string{"godex:tool-provider@1"}}
	report := ValidateCandidateDependencies(candidate, installed)
	if !report.Empty() {
		t.Fatalf("expected no issues, got %s", report.Error())
	}
}

func TestValidateCandidateDependenciesSelfProvidesNotCounted(t *testing.T) {
	// A package's own provides must not satisfy its own requires: the
	// requirement must resolve to another installed package or the platform.
	candidate := Entry{Name: "self", Version: "1.0.0", Requires: []string{"godex:self@1"}, Provides: []string{"godex:self@1"}}
	report := ValidateCandidateDependencies(candidate, []Entry{})
	if report.Empty() {
		t.Fatal("expected missing dependency (own provides must not satisfy own requires)")
	}
	if len(report.Missing) != 1 {
		t.Fatalf("expected 1 missing, got %v", report.Missing)
	}
}

func TestValidateCandidateDependenciesCycle(t *testing.T) {
	installed := []Entry{
		{Name: "a", Version: "1.0.0", Requires: []string{"b"}},
		{Name: "b", Version: "1.0.0", Requires: []string{"a"}},
	}
	candidate := Entry{Name: "c", Version: "1.0.0"}
	report := ValidateCandidateDependencies(candidate, installed)
	if len(report.Cycles) == 0 {
		t.Fatalf("expected cycle detection, got none")
	}
	// a <-> b cycle must be present
	found := false
	for _, cycle := range report.Cycles {
		joined := strings.Join(cycle, "->")
		if strings.Contains(joined, "a") && strings.Contains(joined, "b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a<->b cycle, got %v", report.Cycles)
	}
}

func TestDependents(t *testing.T) {
	installed := []Entry{
		{Name: "app", Version: "1.0.0", Requires: []string{"base@1"}},
		{Name: "other", Version: "1.0.0"},
		{Name: "base", Version: "1.0.0"},
	}
	dependents := Dependents("base", installed, "base")
	if len(dependents) != 1 || dependents[0] != "app" {
		t.Fatalf("expected dependents [app], got %v", dependents)
	}
	if len(Dependents("other", installed, "other")) != 0 {
		t.Fatal("expected no dependents for other")
	}
}

func TestInstallRejectsMissingDependency(t *testing.T) {
	source := t.TempDir()
	writePackageManifest(t, source, "name: broken\nversion: 0.1.0\nrequires:\n  - missing-lib@1\n")
	manager := NewManager(t.TempDir(), filepath.Join(t.TempDir(), "skills"))
	if _, err := manager.Install(source); err == nil {
		t.Fatal("expected install to fail on missing dependency")
	} else if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing-dependency error, got %v", err)
	}
	items, err := manager.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no installed packages after failed install, got %v", items)
	}
}

func TestInstallRejectsCycle(t *testing.T) {
	manager := NewManager(t.TempDir(), filepath.Join(t.TempDir(), "skills"))

	// a requires b
	sourceA := t.TempDir()
	writePackageManifest(t, sourceA, "name: a\nversion: 1.0.0\nrequires:\n  - b\n")
	if _, err := manager.Install(sourceA); err == nil {
		t.Fatal("expected install of a to fail (b missing)")
	}

	// b requires a
	sourceB := t.TempDir()
	writePackageManifest(t, sourceB, "name: b\nversion: 1.0.0\nrequires:\n  - a\n")
	if _, err := manager.Install(sourceB); err == nil {
		t.Fatal("expected install of b to fail (a missing)")
	}

	// install a first now that... a still requires b -> fail
	if _, err := manager.Install(sourceA); err == nil {
		t.Fatal("expected install of a to still fail")
	}
}

func TestInstallDependencyChainSatisfied(t *testing.T) {
	manager := NewManager(t.TempDir(), filepath.Join(t.TempDir(), "skills"))

	base := t.TempDir()
	writePackageManifest(t, base, "name: base\nversion: 1.2.0\nprovides:\n  - godex:base@1\n")
	if _, err := manager.Install(base); err != nil {
		t.Fatalf("install base: %v", err)
	}

	consumer := t.TempDir()
	writePackageManifest(t, consumer, "name: app\nversion: 0.1.0\nrequires:\n  - base@>=1.0.0\n  - godex:base@1\n")
	if _, err := manager.Install(consumer); err != nil {
		t.Fatalf("install app: %v", err)
	}

	entry, err := manager.Get("app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if len(entry.Requires) != 2 || entry.Requires[0] != "base@>=1.0.0" || entry.Requires[1] != "godex:base@1" {
		t.Fatalf("unexpected requires persisted: %v", entry.Requires)
	}
}

func TestRemoveGuardBlocksRequiredPackage(t *testing.T) {
	manager := NewManager(t.TempDir(), filepath.Join(t.TempDir(), "skills"))

	base := t.TempDir()
	writePackageManifest(t, base, "name: base\nversion: 1.0.0\n")
	if _, err := manager.Install(base); err != nil {
		t.Fatalf("install base: %v", err)
	}
	app := t.TempDir()
	writePackageManifest(t, app, "name: app\nversion: 1.0.0\nrequires:\n  - base\n")
	if _, err := manager.Install(app); err != nil {
		t.Fatalf("install app: %v", err)
	}

	if _, err := manager.Remove("base"); err == nil {
		t.Fatal("expected remove of base to be blocked")
	} else if !strings.Contains(err.Error(), "required by app") {
		t.Fatalf("unexpected remove error: %v", err)
	}
	// still installed
	if _, err := manager.Get("base"); err != nil {
		t.Fatalf("base should still be installed: %v", err)
	}
	// removing app unblocks base
	if _, err := manager.Remove("app"); err != nil {
		t.Fatalf("remove app: %v", err)
	}
	if _, err := manager.Remove("base"); err != nil {
		t.Fatalf("remove base after app removed: %v", err)
	}
}

func TestReinstallIsTransactionalAndGCsStaleDirs(t *testing.T) {
	state := t.TempDir()
	manager := NewManager(state, filepath.Join(t.TempDir(), "skills"))

	source := t.TempDir()
	writePackageManifest(t, source, "name: kit\nversion: 0.1.0\n")
	first, err := manager.Install(source)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("first install dir missing: %v", err)
	}

	// Reinstall with changed content -> new digest dir, old dir GC'd.
	writePackageManifest(t, source, "name: kit\nversion: 0.2.0\n")
	second, err := manager.Reinstall("kit")
	if err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if second.Version != "0.2.0" {
		t.Fatalf("unexpected version: %+v", second)
	}
	if second.Path == first.Path {
		t.Fatal("expected new digest dir after content change")
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatalf("second install dir missing: %v", err)
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Fatalf("expected old digest dir GC'd, got %v", err)
	}

	// Reinstall with same content -> same digest dir reused, no orphan.
	third, err := manager.Reinstall("kit")
	if err != nil {
		t.Fatalf("reinstall again: %v", err)
	}
	if third.Path != second.Path {
		t.Fatalf("expected same digest dir reused: %v vs %v", third.Path, second.Path)
	}
}

func TestQualityReportSurfacesDependencyIssues(t *testing.T) {
	manager := NewManager(t.TempDir(), filepath.Join(t.TempDir(), "skills"))

	broken := t.TempDir()
	writePackageManifest(t, broken, "name: broken\nversion: 0.1.0\nrequires:\n  - ghost@1\n")
	// Install is blocked, so seed the registry directly via InstallPrepared of a
	// valid copy then mutate? Simpler: use a valid manifest and verify the report
	// shows no dependency issues, then a second scenario with a seeded registry.
	writePackageManifest(t, broken, "name: ok\nversion: 0.1.0\nrequires:\n  - ghost@1\nprovides:\n  - godex:ok@1\n")
	if _, err := manager.Install(broken); err == nil {
		// Install with missing dep is blocked; force a registry entry through a
		// bypass so the quality report can still flag it.
		items, _ := manager.List()
		_ = items
		t.Fatal("expected install to fail on missing dependency")
	}

	// Seed a registry entry manually to simulate an externally-broken install.
	registry := Registry{Packages: []Entry{{Name: "ok", Version: "0.1.0", Requires: []string{"ghost@1"}, Provides: []string{"godex:ok@1"}, Path: t.TempDir(), Trust: "local"}}}
	if err := manager.writeRegistry(registry); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	report, err := manager.BuildQualityReport("now", ToolHealthSummary{}, []string{"core_code"})
	if err != nil {
		t.Fatalf("quality report: %v", err)
	}
	if len(report.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(report.Packages))
	}
	quality := report.Packages[0]
	if len(quality.Requires) != 1 || quality.Requires[0] != "ghost@1" {
		t.Fatalf("unexpected requires: %+v", quality.Requires)
	}
	if len(quality.DependencyIssues) == 0 {
		t.Fatalf("expected dependency issues in quality report, got %+v", quality)
	}
	foundMissing := false
	for _, issue := range quality.DependencyIssues {
		if containsSubstr(issue, "missing") && containsSubstr(issue, "ghost") {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Fatalf("expected missing ghost dependency issue, got %v", quality.DependencyIssues)
	}
	if quality.RiskLevel != "high" {
		t.Fatalf("expected high risk for broken dependency, got %s", quality.RiskLevel)
	}
}

func containsSubstr(value, substr string) bool {
	return strings.Contains(value, substr)
}

func TestReinstallRollsBackOnBrokenSource(t *testing.T) {
	state := t.TempDir()
	manager := NewManager(state, filepath.Join(t.TempDir(), "skills"))

	source := t.TempDir()
	writePackageManifest(t, source, "name: kit\nversion: 0.1.0\n")
	first, err := manager.Install(source)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	// Break the source: manifest now requires something missing.
	writePackageManifest(t, source, "name: kit\nversion: 0.2.0\nrequires:\n  - never-installed\n")
	if _, err := manager.Reinstall("kit"); err == nil {
		t.Fatal("expected reinstall to fail on broken dependency")
	}

	// Old registry entry and dir must be intact.
	entry, err := manager.Get("kit")
	if err != nil {
		t.Fatalf("old entry should remain: %v", err)
	}
	if entry.Version != "0.1.0" {
		t.Fatalf("expected old version kept, got %v", entry.Version)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("old dir should remain: %v", err)
	}
}

func TestReinstallRollsBackOnSkillLinkFailure(t *testing.T) {
	state := t.TempDir()
	skills := filepath.Join(t.TempDir(), "skills")
	manager := NewManager(state, skills)

	source := t.TempDir()
	writePackageManifest(t, source, "name: sk-kit\nversion: 0.1.0\nresources:\n  skills:\n    - skills/demo/SKILL.md\n")
	if err := os.MkdirAll(filepath.Join(source, "skills", "demo"), 0755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "skills", "demo", "SKILL.md"), []byte("# Demo\n"), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	first, err := manager.Install(source)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skills, "demo", "SKILL.md")); err != nil {
		t.Fatalf("expected skill linked: %v", err)
	}

	// Reinstall with a manifest that declares a skill whose file is missing in
	// the staged content -> linkSkills fails -> activation must roll back.
	writePackageManifest(t, source, "name: sk-kit\nversion: 0.2.0\nresources:\n  skills:\n    - skills/missing/SKILL.md\n")
	if _, err := manager.Reinstall("sk-kit"); err == nil {
		t.Fatal("expected reinstall to fail on missing skill file")
	}

	entry, err := manager.Get("sk-kit")
	if err != nil {
		t.Fatalf("old entry should remain: %v", err)
	}
	if entry.Version != "0.1.0" {
		t.Fatalf("expected old version kept after failed activation, got %v", entry.Version)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("old dir should remain: %v", err)
	}
	// Old skill link must still point at the old skill content.
	data, err := os.ReadFile(filepath.Join(skills, "demo", "SKILL.md"))
	if err != nil {
		t.Fatalf("old skill link should be restored: %v", err)
	}
	if strings.TrimSpace(string(data)) != "# Demo" {
		t.Fatalf("unexpected skill content after rollback: %q", string(data))
	}
}
