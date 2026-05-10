package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
)

// RunSetupCommand initializes a workspace without requiring an existing config.
func RunSetupCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runSetupCommandNamed(ctx, "setup", args, stdout, stderr)
}

func runSetupCommandNamed(ctx context.Context, commandName string, args []string, stdout, stderr io.Writer) error {
	_ = ctx
	fs := flag.NewFlagSet(commandName, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := "."
	force := false
	fs.StringVar(&dir, "dir", dir, "workspace directory to initialize")
	fs.BoolVar(&force, "force", false, "overwrite godex.yaml with default config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected %s arguments: %s", commandName, strings.Join(fs.Args(), " "))
	}
	workspace, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(workspace, 0755); err != nil {
		return err
	}
	configPath := filepath.Join(workspace, "godex.yaml")
	if err := config.WriteDefaultConfigFile(configPath, force); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	manager, err := config.NewManager(config.Options{WorkspaceDir: workspace, ConfigPath: configPath})
	if err != nil {
		return err
	}
	cfg := manager.Current()
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	if err := ensureFile(filepath.Join(workspace, ".env.example"), "# Optional local secrets. Copy to .env and fill values as needed.\nANTHROPIC_API_KEY=\n"); err != nil {
		return err
	}
	if err := ensureFile(filepath.Join(workspace, "AGENT.md"), "# Project Agent Notes\n\nAdd project-specific goals, constraints, and operating notes for GoDex here.\n"); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Initialized GoDex workspace at %s\n", workspace)
	fmt.Fprintf(stdout, "Config: %s\n", configPath)
	fmt.Fprintf(stdout, "State: %s\n", cfg.StateDir)
	report := manager.Doctor()
	fmt.Fprintf(stdout, "Doctor: %d error(s), %d warning(s)\n", report.Errors, report.Warnings)
	if report.Errors > 0 || report.Warnings > 0 {
		fmt.Fprintf(stdout, "Next: cd %s && godex doctor\n", workspace)
	}
	return nil
}

func ensureFile(path, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(content)
	return err
}
