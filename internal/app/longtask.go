package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/services/backend"
)

func (r *Runner) runLongTaskCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing longtask subcommand: list, create, run, status, cancel, finalize")
	}
	switch args[0] {
	case "list":
		return r.runLongTaskList(ctx, args[1:])
	case "create":
		return r.runLongTaskCreate(ctx, args[1:])
	case "run":
		return r.runLongTaskRun(ctx, args[1:])
	case "status":
		return r.runLongTaskStatus(ctx, args[1:])
	case "cancel":
		return r.runLongTaskCancel(ctx, args[1:])
	case "finalize":
		return r.runLongTaskFinalize(ctx, args[1:])
	default:
		return fmt.Errorf("unknown longtask subcommand %q", args[0])
	}
}

func (r *Runner) runLongTaskList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("longtask list", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var sessionSpec string
	fs.StringVar(&sessionSpec, "session", "", "session key or channel:key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opened, err := r.openLongTaskSession(ctx, sessionSpec)
	if err != nil {
		return err
	}
	items, err := r.Backend.ListLongTasks(ctx, opened.SessionID)
	if err != nil {
		return err
	}
	return r.printLongTaskJSON(items)
}

func (r *Runner) runLongTaskCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("longtask create", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var sessionSpec, file string
	fs.StringVar(&sessionSpec, "session", "", "session key or channel:key")
	fs.StringVar(&file, "file", "", "JSON PRD/LongTask spec file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(file) == "" {
		return fmt.Errorf("missing --file")
	}
	var input agent.LongTaskArgs
	if err := readLongTaskArgsFile(file, &input); err != nil {
		return err
	}
	opened, err := r.openLongTaskSession(ctx, sessionSpec)
	if err != nil {
		return err
	}
	view, err := r.Backend.CreateLongTask(ctx, opened.SessionID, input)
	if err != nil {
		return err
	}
	return r.printLongTaskJSON(view)
}

func (r *Runner) runLongTaskRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("longtask run", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var sessionSpec string
	var autoRepair bool
	var maxRepairAttempts, maxIterations, waitTimeoutMS int
	fs.StringVar(&sessionSpec, "session", "", "session key or channel:key")
	fs.BoolVar(&autoRepair, "auto-repair", false, "append repair nodes after failed validations")
	fs.IntVar(&maxRepairAttempts, "max-repair-attempts", 0, "maximum repair attempts per story")
	fs.IntVar(&maxIterations, "max-iterations", 0, "maximum LongTask run loop iterations")
	fs.IntVar(&waitTimeoutMS, "wait-timeout-ms", 0, "subagent wait timeout in milliseconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	workflowID := strings.TrimSpace(fs.Arg(0))
	if workflowID == "" {
		return fmt.Errorf("missing longtask id")
	}
	opened, err := r.openLongTaskSession(ctx, sessionSpec)
	if err != nil {
		return err
	}
	view, err := r.Backend.RunLongTask(ctx, opened.SessionID, workflowID, agent.LongTaskArgs{
		AutoRepair:        autoRepair,
		MaxRepairAttempts: maxRepairAttempts,
		MaxIterations:     maxIterations,
		WaitTimeoutMS:     waitTimeoutMS,
	})
	if err != nil {
		return err
	}
	return r.printLongTaskJSON(view)
}

func (r *Runner) runLongTaskStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("longtask status", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var sessionSpec string
	fs.StringVar(&sessionSpec, "session", "", "session key or channel:key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	workflowID := strings.TrimSpace(fs.Arg(0))
	if workflowID == "" {
		return fmt.Errorf("missing longtask id")
	}
	opened, err := r.openLongTaskSession(ctx, sessionSpec)
	if err != nil {
		return err
	}
	view, err := r.Backend.GetLongTask(ctx, opened.SessionID, workflowID)
	if err != nil {
		return err
	}
	return r.printLongTaskJSON(view)
}

func (r *Runner) runLongTaskCancel(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("longtask cancel", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var sessionSpec, nodeID string
	fs.StringVar(&sessionSpec, "session", "", "session key or channel:key")
	fs.StringVar(&nodeID, "node", "", "workflow node id to cancel")
	if err := fs.Parse(args); err != nil {
		return err
	}
	workflowID := strings.TrimSpace(fs.Arg(0))
	if workflowID == "" || strings.TrimSpace(nodeID) == "" {
		return fmt.Errorf("usage: godex longtask cancel <id> --node <node_id>")
	}
	opened, err := r.openLongTaskSession(ctx, sessionSpec)
	if err != nil {
		return err
	}
	view, err := r.Backend.CancelLongTask(ctx, opened.SessionID, workflowID, nodeID)
	if err != nil {
		return err
	}
	return r.printLongTaskJSON(view)
}

func (r *Runner) runLongTaskFinalize(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("longtask finalize", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var sessionSpec, nodeID string
	fs.StringVar(&sessionSpec, "session", "", "session key or channel:key")
	fs.StringVar(&nodeID, "node", "", "completed story node id to finalize")
	if err := fs.Parse(args); err != nil {
		return err
	}
	workflowID := strings.TrimSpace(fs.Arg(0))
	if workflowID == "" || strings.TrimSpace(nodeID) == "" {
		return fmt.Errorf("usage: godex longtask finalize <id> --node <node_id>")
	}
	opened, err := r.openLongTaskSession(ctx, sessionSpec)
	if err != nil {
		return err
	}
	view, err := r.Backend.FinalizeLongTaskStory(ctx, opened.SessionID, workflowID, nodeID)
	if err != nil {
		return err
	}
	return r.printLongTaskJSON(view)
}

func (r *Runner) openLongTaskSession(ctx context.Context, sessionSpec string) (*backend.OpenedSession, error) {
	locator := parseSessionSpecifier(sessionSpec, "local", "default")
	return r.Backend.OpenSession(ctx, locator)
}

func readLongTaskArgsFile(path string, out *agent.LongTaskArgs) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read longtask file: %w", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse longtask file: %w", err)
	}
	return nil
}

func (r *Runner) printLongTaskJSON(value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(r.Stdout, string(data))
	return err
}
