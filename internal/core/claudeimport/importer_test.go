package claudeimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
)

func TestClaudeImportBuildsInstallablePackage(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "skills", "reviewer", "SKILL.md"), `---
name: reviewer
description: Review code.
---
# Reviewer

## Core
Review code carefully.
`)
	writeTestFile(t, filepath.Join(source, "commands", "git", "review.md"), `---
description: Review current git changes.
allowed-tools: Read, Grep, Bash(git diff:*)
---
Review the current git diff and summarize risks.
`)
	writeTestFile(t, filepath.Join(source, "agents", "planner.md"), `---
name: planner
description: Planning specialist.
tools: Read, Write, Task, UnknownTool
model: sonnet
---
Plan the work and call out dependencies.
`)
	writeTestFile(t, filepath.Join(source, "settings.json"), `{"hooks": []}`)

	plan, err := NewPlan(Options{Source: source, PackageName: "Claude Kit"})
	if err != nil {
		t.Fatalf("plan import: %v", err)
	}
	if plan.PackageName != "claude-kit" {
		t.Fatalf("unexpected package name %q", plan.PackageName)
	}
	if len(plan.Skills) != 1 || len(plan.Commands) != 1 || len(plan.Roles) != 1 || len(plan.Settings) != 1 {
		t.Fatalf("unexpected plan counts: %+v", plan)
	}
	if !strings.Contains(strings.Join(plan.Warnings, "\n"), "UnknownTool") {
		t.Fatalf("expected unsupported tool warning, got %#v", plan.Warnings)
	}

	generated := filepath.Join(t.TempDir(), "package")
	if err := BuildPackage(plan, generated); err != nil {
		t.Fatalf("build package: %v", err)
	}
	manager := pkgregistry.NewManager(t.TempDir(), filepath.Join(t.TempDir(), "skills"))
	entry, err := manager.InstallPrepared(generated, "claude:"+source)
	if err != nil {
		t.Fatalf("install generated package: %v", err)
	}
	if entry.Name != "claude-kit" || entry.Source != "claude:"+source {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if len(entry.Resources.Skills) != 1 || len(entry.Resources.Commands) != 1 || len(entry.Resources.Roles) != 1 || len(entry.Resources.Prompts) != 1 {
		t.Fatalf("unexpected resources: %+v", entry.Resources)
	}
	commands, err := manager.ListCommands(true)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(commands) != 1 || commands[0].Namespace != "git" || commands[0].Mode != "agent_turn" {
		t.Fatalf("unexpected command: %+v", commands)
	}
	if !strings.Contains(commands[0].Prompt, "git diff") {
		t.Fatalf("expected command prompt content, got %q", commands[0].Prompt)
	}
	roles, err := manager.ListRoles(true)
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	if len(roles) != 1 || roles[0].ID != "claude-kit:planner" || !roles[0].WriteEnabled || roles[0].ModelHint != "sonnet" {
		t.Fatalf("unexpected role: %+v", roles)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
