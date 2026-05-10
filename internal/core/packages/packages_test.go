package packages

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagerInstallListsPromptsAndRemovesPackage(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "skills", "review"), 0755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(source, "prompts"), 0755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(source, "commands"), 0755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(source, "roles"), 0755); err != nil {
		t.Fatalf("mkdir roles: %v", err)
	}
	manifest := `name: review-kit
version: 0.1.0
description: Review helpers
resources:
  skills:
    - skills/review/SKILL.md
  prompts:
    - prompts/review.md
  commands:
    - commands/review.yaml
  roles:
    - roles/reviewer.yaml
app:
  kind: builtin
  id: notes
  label: Notes
  config:
    default_role: assistant
permissions:
  - read_file
capabilities:
  - tool:read_file
tool_policy:
  - tool:allow:read_file
recommended_bundles:
  - core_code
smoke_tests:
  - name: quick
    command: printf ok
    timeout_seconds: 5
    required_permissions:
      - shell
`
	if err := os.WriteFile(filepath.Join(source, ManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "skills", "review", "SKILL.md"), []byte("# Review\n"), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "prompts", "review.md"), []byte("review this code"), 0644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	commandSpec := `name: review
namespace: review-kit
description: Review current changes
mode: agent_turn
prompt_path: prompts/review.md
roles:
  - review-kit:reviewer
recommended_bundles:
  - core_code
capabilities:
  - tool:read_file
tool_policy:
  - tool:allow:read_file
`
	if err := os.WriteFile(filepath.Join(source, "commands", "review.yaml"), []byte(commandSpec), 0644); err != nil {
		t.Fatalf("write command: %v", err)
	}
	roleSpec := `id: review-kit:reviewer
name: Reviewer
description: Reviews code changes
default_bundles:
  - core_code
tools:
  - read_file
capabilities:
  - tool:read_file
tool_policy:
  - tool:allow:read_file
`
	if err := os.WriteFile(filepath.Join(source, "roles", "reviewer.yaml"), []byte(roleSpec), 0644); err != nil {
		t.Fatalf("write role: %v", err)
	}

	state := t.TempDir()
	skills := filepath.Join(t.TempDir(), "skills")
	manager := NewManager(state, skills)
	entry, err := manager.Install(source)
	if err != nil {
		t.Fatalf("install package: %v", err)
	}
	if entry.Name != "review-kit" || entry.Trust != "local" {
		t.Fatalf("unexpected installed entry: %+v", entry)
	}
	if len(entry.Capabilities) != 1 || entry.Capabilities[0] != "tool:read_file" || len(entry.ToolPolicy) != 1 || entry.ToolPolicy[0] != "tool:allow:read_file" {
		t.Fatalf("unexpected contract fields: %+v", entry)
	}
	if len(entry.SmokeTests) != 1 || entry.SmokeTests[0].Name != "quick" {
		t.Fatalf("unexpected smoke tests: %+v", entry.SmokeTests)
	}
	if entry.App.Kind != "builtin" || entry.App.ID != "notes" || entry.App.Config["default_role"] != "assistant" {
		t.Fatalf("unexpected package app manifest: %+v", entry.App)
	}
	if _, err := os.Stat(filepath.Join(skills, "review", "SKILL.md")); err != nil {
		t.Fatalf("expected linked skill: %v", err)
	}

	prompts, err := manager.ListPrompts(true)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	if len(prompts) != 1 || prompts[0].Name != "review" || prompts[0].Content != "review this code" {
		t.Fatalf("unexpected prompts: %+v", prompts)
	}
	commands, err := manager.ListCommands(true)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(commands) != 1 || commands[0].Name != "review" || commands[0].Mode != "agent_turn" || commands[0].Prompt != "review this code" {
		t.Fatalf("unexpected commands: %+v", commands)
	}
	if len(commands[0].Capabilities) != 1 || commands[0].Capabilities[0] != "tool:read_file" || len(commands[0].ToolPolicy) != 1 {
		t.Fatalf("unexpected command contract fields: %+v", commands[0])
	}
	roles, err := manager.ListRoles(true)
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	if len(roles) != 1 || roles[0].ID != "review-kit:reviewer" || roles[0].Name != "Reviewer" {
		t.Fatalf("unexpected roles: %+v", roles)
	}
	if len(roles[0].Capabilities) != 1 || roles[0].Capabilities[0] != "tool:read_file" || len(roles[0].ToolPolicy) != 1 {
		t.Fatalf("unexpected role contract fields: %+v", roles[0])
	}

	removed, err := manager.Remove("review-kit")
	if err != nil {
		t.Fatalf("remove package: %v", err)
	}
	if removed.Name != "review-kit" {
		t.Fatalf("unexpected removed entry: %+v", removed)
	}
	if _, err := os.Stat(filepath.Join(skills, "review")); !os.IsNotExist(err) {
		t.Fatalf("expected linked skill removed, got %v", err)
	}
}

func TestManagerBuildQualityReportFlagsResourceAndPermissionIssues(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "skills", "ops"), 0755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	manifest := `name: ops-kit
version: 0.2.0
resources:
  skills:
    - skills/ops/SKILL.md
  prompts:
    - prompts/missing.md
permissions:
  - shell
recommended_bundles:
  - mystery
capabilities:
  - unknown:value
tool_policy:
  - invalid
smoke_tests:
  - name: bad
    command: curl https://example.com
    working_dir: ../outside
    required_permissions:
      - mystery
`
	if err := os.WriteFile(filepath.Join(source, ManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "skills", "ops", "SKILL.md"), []byte("# Ops\n"), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	state := t.TempDir()
	skills := filepath.Join(t.TempDir(), "skills")
	manager := NewManager(state, skills)
	if _, err := manager.Install(source); err != nil {
		t.Fatalf("install package: %v", err)
	}
	report, err := manager.BuildQualityReport("now", ToolHealthSummary{
		InspectedSessions: 1,
		TotalRuns:         2,
		SuccessRuns:       1,
		FailureRuns:       1,
		SuccessRate:       50,
		ByTool: []ToolStat{{
			Name:        "bash",
			Total:       2,
			Success:     1,
			Failure:     1,
			SuccessRate: 50,
			LastFailure: "command not allowed",
		}},
	}, []string{"core_code"})
	if err != nil {
		t.Fatalf("quality report: %v", err)
	}
	if report.PackageCount != 1 || report.SkillCount != 1 || report.PromptCount != 1 || report.HighRiskPackages != 1 {
		t.Fatalf("unexpected quality summary: %+v", report)
	}
	item := report.Packages[0]
	if item.RiskLevel != "high" || len(item.ResourceIssues) == 0 || len(item.PermissionIssues) == 0 || len(item.UnknownBundles) != 1 {
		t.Fatalf("expected package issues, got %+v", item)
	}
	if len(item.CapabilityIssues) == 0 || len(item.ToolPolicyIssues) == 0 || len(item.SmokeChecks) != 1 || item.SmokeChecks[0].Status != "invalid" {
		t.Fatalf("expected contract and smoke diagnostics, got %+v", item)
	}
	if len(item.AppIssues) != 0 {
		t.Fatalf("did not expect app issues for package without app: %+v", item.AppIssues)
	}
	if len(report.FailureReasons) != 1 || report.FailureReasons[0].Reason != "command not allowed" {
		t.Fatalf("unexpected failure reasons: %+v", report.FailureReasons)
	}
}

func TestManagerBuildQualityReportFlagsPackageAppIssues(t *testing.T) {
	source := t.TempDir()
	manifest := `name: app-kit
version: 0.1.0
app:
  kind: iframe
  id: custom-ui
`
	if err := os.WriteFile(filepath.Join(source, ManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manager := NewManager(t.TempDir(), filepath.Join(t.TempDir(), "skills"))
	if _, err := manager.Install(source); err != nil {
		t.Fatalf("install package: %v", err)
	}
	report, err := manager.BuildQualityReport("now", ToolHealthSummary{}, []string{"core_code"})
	if err != nil {
		t.Fatalf("quality report: %v", err)
	}
	if len(report.Packages) != 1 || len(report.Packages[0].AppIssues) == 0 {
		t.Fatalf("expected app issues, got %+v", report.Packages)
	}
}

func TestManagerReinstallAndSmokeRunMetadata(t *testing.T) {
	source := t.TempDir()
	manifest := `name: smoke-kit
version: 0.1.0
smoke_tests:
  - name: quick
    command: printf ok
`
	if err := os.WriteFile(filepath.Join(source, ManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	state := t.TempDir()
	manager := NewManager(state, filepath.Join(t.TempDir(), "skills"))
	entry, err := manager.Install(source)
	if err != nil {
		t.Fatalf("install package: %v", err)
	}
	run := SmokeRun{
		RunID:       NewSmokeRunID(entry.Name, "quick", entry.InstalledAt),
		PackageName: entry.Name,
		SmokeName:   "quick",
		Status:      "passed",
		Output:      "ok",
		StartedAt:   entry.InstalledAt,
		CompletedAt: entry.InstalledAt,
	}
	if err := manager.RecordSmokeRun(run); err != nil {
		t.Fatalf("record smoke run: %v", err)
	}
	runs, err := manager.ListSmokeRuns(entry.Name)
	if err != nil {
		t.Fatalf("list smoke runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "passed" {
		t.Fatalf("unexpected smoke runs: %+v", runs)
	}

	manifest = `name: smoke-kit
version: 0.2.0
smoke_tests:
  - name: quick
    command: printf ok
`
	if err := os.WriteFile(filepath.Join(source, ManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}
	reinstalled, err := manager.Reinstall("smoke-kit")
	if err != nil {
		t.Fatalf("reinstall package: %v", err)
	}
	if reinstalled.Version != "0.2.0" || reinstalled.Source != source {
		t.Fatalf("unexpected reinstalled entry: %+v", reinstalled)
	}
}
