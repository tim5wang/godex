package app

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/claudeimport"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
)

func (r *Runner) runImport(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing import subcommand\n\n%s", importHelpText())
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprintln(r.Stdout, importHelpText())
		return nil
	case "claude":
		return r.runImportClaude(ctx, args[1:])
	default:
		return fmt.Errorf("unknown import subcommand %q\n\n%s", args[0], importHelpText())
	}
}

func (r *Runner) runImportClaude(ctx context.Context, args []string) error {
	_ = ctx
	fs := flag.NewFlagSet("import claude", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var source string
	var packageName string
	var dryRun bool
	fs.StringVar(&source, "source", ".claude", "Claude Code directory to import")
	fs.StringVar(&packageName, "package", claudeimport.DefaultPackageName, "GoDex package name to create")
	fs.BoolVar(&dryRun, "dry-run", false, "scan and report without installing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected import claude arguments: %s", strings.Join(fs.Args(), " "))
	}
	if r.Cfg == nil {
		return fmt.Errorf("config unavailable")
	}
	plan, err := claudeimport.NewPlan(claudeimport.Options{Source: source, PackageName: packageName})
	if err != nil {
		return err
	}
	if dryRun {
		r.printClaudeImportPlan(plan, true, nil)
		return nil
	}
	tmp, err := os.MkdirTemp("", "godex-claude-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := claudeimport.BuildPackage(plan, tmp); err != nil {
		return err
	}
	manager := pkgregistry.NewManager(r.Cfg.StateDir, r.Cfg.SkillsDir)
	entry, err := manager.InstallPrepared(tmp, "claude:"+plan.Source)
	if err != nil {
		return err
	}
	r.printClaudeImportPlan(plan, false, &entry)
	return nil
}

func (r *Runner) printClaudeImportPlan(plan claudeimport.Plan, dryRun bool, entry *pkgregistry.Entry) {
	if dryRun {
		fmt.Fprintln(r.Stdout, "Claude import dry run")
	} else {
		fmt.Fprintln(r.Stdout, "Claude import installed")
	}
	fmt.Fprintf(r.Stdout, "Source: %s\n", plan.Source)
	fmt.Fprintf(r.Stdout, "Package: %s\n", plan.PackageName)
	if entry != nil {
		fmt.Fprintf(r.Stdout, "Installed path: %s\n", entry.Path)
	}
	fmt.Fprintf(r.Stdout, "Skills: %d\n", len(plan.Skills))
	for _, item := range plan.Skills {
		fmt.Fprintf(r.Stdout, "  - %s -> %s\n", item.Name, filepath.ToSlash(item.TargetPath))
	}
	fmt.Fprintf(r.Stdout, "Commands: %d\n", len(plan.Commands))
	for _, item := range plan.Commands {
		fmt.Fprintf(r.Stdout, "  - %s -> %s\n", item.Name, filepath.ToSlash(item.TargetPath))
	}
	fmt.Fprintf(r.Stdout, "Roles: %d\n", len(plan.Roles))
	for _, item := range plan.Roles {
		fmt.Fprintf(r.Stdout, "  - %s -> %s\n", item.Name, filepath.ToSlash(item.TargetPath))
	}
	if len(plan.Settings) > 0 {
		fmt.Fprintf(r.Stdout, "Settings diagnostics: %d file(s)\n", len(plan.Settings))
	}
	if len(plan.Warnings) > 0 {
		fmt.Fprintln(r.Stdout, "Warnings:")
		for _, warning := range plan.Warnings {
			fmt.Fprintf(r.Stdout, "  - %s\n", warning)
		}
	}
	if dryRun {
		fmt.Fprintln(r.Stdout, "Run without --dry-run to install the generated GoDex package.")
	}
}

func importHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex import claude [--source .claude] [--package claude-import] [--dry-run]",
		"",
		"Import external agent ecosystem resources into GoDex packages.",
		"",
		"Claude import mapping:",
		"  .claude/skills/*/SKILL.md  -> GoDex skill",
		"  .claude/commands/**/*.md   -> GoDex package command",
		"  .claude/agents/**/*.md     -> GoDex package role",
		"  .claude/settings*.json     -> diagnostics only",
		"",
		"Examples:",
		"  godex import claude --source .claude --dry-run",
		"  godex import claude --source .claude",
		"  godex import claude --source ~/.claude --package claude-user",
	}, "\n")
}
