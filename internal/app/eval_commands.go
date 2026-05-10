package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/services/evalharness"
)

func (r *Runner) runEval(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing eval subcommand\n\n%s", evalHelpText())
	}
	switch args[0] {
	case "run":
		return r.runEvalRun(ctx, args[1:])
	case "list":
		return r.runEvalList(args[1:])
	case "show":
		return r.runEvalShow(args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(r.Stdout, evalHelpText())
		return nil
	default:
		return fmt.Errorf("unknown eval subcommand %q\n\n%s", args[0], evalHelpText())
	}
}

func (r *Runner) runEvalRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("eval run", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var suitePath, outDir, modelProfileID string
	fs.StringVar(&suitePath, "suite", "", "path to godex.eval.yaml")
	fs.StringVar(&outDir, "out", defaultEvalRunsDir(), "directory for eval run outputs")
	fs.StringVar(&modelProfileID, "model-profile", "", "override model profile id for every case")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected eval run arguments: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(suitePath) == "" {
		return fmt.Errorf("missing --suite")
	}
	if r.Eval == nil {
		return fmt.Errorf("eval harness unavailable")
	}
	report, err := r.Eval.RunSuite(ctx, evalharness.RunOptions{
		SuitePath:      suitePath,
		OutDir:         outDir,
		ModelProfileID: modelProfileID,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Eval run %s: %d/%d passed\n", report.RunID, report.PassedCases, report.TotalCases)
	if !report.Passed {
		return fmt.Errorf("eval run failed: %d case(s) failed", report.FailedCases)
	}
	return nil
}

func (r *Runner) runEvalList(args []string) error {
	fs := flag.NewFlagSet("eval list", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var dir string
	fs.StringVar(&dir, "dir", defaultEvalRunsDir(), "directory containing eval runs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected eval list arguments: %s", strings.Join(fs.Args(), " "))
	}
	reports, err := evalharness.ListReports(dir)
	if err != nil {
		return err
	}
	for _, report := range reports {
		status := "failed"
		if report.Passed {
			status = "passed"
		}
		fmt.Fprintf(r.Stdout, "%s\t%s\t%d/%d\t%s\n", report.RunID, status, report.PassedCases, report.TotalCases, report.StartedAt.Format("2006-01-02 15:04:05"))
	}
	return nil
}

func (r *Runner) runEvalShow(args []string) error {
	fs := flag.NewFlagSet("eval show", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var runPath string
	fs.StringVar(&runPath, "run", "", "path to eval run directory or report.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected eval show arguments: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(runPath) == "" {
		return fmt.Errorf("missing --run")
	}
	report, err := evalharness.ReadReport(runPath)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(r.Stdout, string(data))
	return nil
}

func evalHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex eval run --suite godex.eval.yaml [--out ~/.godex/evals/runs] [--model-profile id]",
		"  godex eval list [--dir ~/.godex/evals/runs]",
		"  godex eval show --run <run-dir-or-report.json>",
	}, "\n")
}

func defaultEvalRunsDir() string {
	if home := strings.TrimSpace(os.Getenv("GODEX_HOME")); home != "" {
		return filepath.Join(home, "evals", "runs")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".godex", "evals", "runs")
	}
	return filepath.Join("godex-evals", "runs")
}
