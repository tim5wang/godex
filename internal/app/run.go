package app

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
	acpserver "github.com/tim5wang/godex/internal/acp/server"
	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/auth"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/core/providers"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/platform/storagegc"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/evalharness"
	"github.com/tim5wang/godex/internal/services/sessionrepair"
	"github.com/tim5wang/godex/internal/sessionstore"
	"github.com/tim5wang/godex/internal/tools"
	"github.com/tim5wang/godex/internal/version"
)

// Backend defines the shared backend surface needed by CLI entrypoints.
type Backend interface {
	OpenSession(context.Context, backend.SessionLocator) (*backend.OpenedSession, error)
	Submit(context.Context, string, message.Envelope) (*backend.SubmitResult, error)
	SubmitAsync(context.Context, string, message.Envelope, ...backend.SubmitOptions) (*backend.SubmitResult, error)
	ExecuteCommand(context.Context, string, commands.Command) (commands.Result, error)
	AttachSink(string, events.Sink) (func(), error)
	PendingPermissions(context.Context, string) ([]tools.PendingPermission, error)
	ApprovePermission(context.Context, string, string, tools.PermissionGrantScope) (tools.PermissionResolution, error)
	DenyPermission(context.Context, string, string, string) (tools.PermissionResolution, error)
	Models(context.Context, string) (backend.ModelsView, error)
	SetSessionModelProfile(context.Context, string, string) (backend.ModelsView, error)
	ListLongTasks(context.Context, string) ([]agent.LongTaskView, error)
	GetLongTask(context.Context, string, string) (agent.LongTaskView, error)
	CreateLongTask(context.Context, string, agent.LongTaskArgs) (agent.LongTaskView, error)
	RunLongTask(context.Context, string, string, agent.LongTaskArgs) (agent.LongTaskView, error)
	CancelLongTask(context.Context, string, string, string) (agent.LongTaskView, error)
	CancelLongTaskAll(context.Context, string, string) (agent.LongTaskView, error)
	FinalizeLongTaskStory(context.Context, string, string, string) (agent.LongTaskView, error)
}

// Runner dispatches top-level process modes onto the shared backend.
type Runner struct {
	Cfg           *config.Config
	ConfigManager *config.Manager
	Backend       Backend
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer

	Now          func() time.Time
	RunREPL      func(context.Context) error
	RunTUI       func(context.Context, backend.SessionLocator) error
	Serve        func(context.Context, string) error
	Doctor       func(context.Context) (string, error)
	WeixinSetup  func(context.Context) error
	WeixinLogout func(context.Context) error
	OpenBrowser  func(string) error
	Eval         *evalharness.Service
}

// Run executes the selected top-level mode.
func (r *Runner) Run(ctx context.Context, args []string) error {
	if r.Now == nil {
		r.Now = time.Now
	}
	if len(args) == 0 {
		if r.RunTUI == nil {
			return fmt.Errorf("tui mode unavailable")
		}
		locator := backend.SessionLocator{Channel: "local", Key: "default"}
		profile := ""
		if r.Cfg != nil {
			profile = r.Cfg.DefaultAgentProfileForChannel("tui")
		}
		applyLocatorAgentProfile(&locator, profile)
		return r.RunTUI(ctx, locator)
	}

	switch args[0] {
	case "ask":
		return r.runAsk(ctx, args[1:])
	case "command":
		return r.runCommand(ctx, args[1:])
	case "doctor":
		return r.runDoctor(ctx, args[1:])
	case "eval":
		return r.runEval(ctx, args[1:])
	case "login":
		return r.runLogin(ctx, args[1:])
	case "logout":
		return r.runLogout(ctx, args[1:])
	case "config":
		return RunConfigWizard(ctx, args[1:], r.Stdin, r.Stdout, r.Stderr)
	case "providers":
		return r.runProviders(ctx, args[1:])
	case "migrate":
		return r.runMigrate(ctx, args[1:])
	case "repair":
		return r.runRepair(ctx, args[1:])
	case "gc":
		return r.runGC(ctx, args[1:])
	case "acp-server":
		return r.runACPServer(ctx, args[1:])
	case "import":
		return r.runImport(ctx, args[1:])
	case "longtask":
		if containsHelpArg(args[1:]) {
			fmt.Fprintln(r.Stdout, longtaskHelpText())
			return nil
		}
		return r.runLongTaskCommand(ctx, args[1:])
	case "setup":
		return RunSetupCommand(ctx, args[1:], r.Stdout, r.Stderr)
	case "init":
		return runSetupCommandNamed(ctx, "init", args[1:], r.Stdout, r.Stderr)
	case "repl":
		if containsHelpArg(args[1:]) {
			fmt.Fprintln(r.Stdout, replHelpText())
			return nil
		}
		if r.RunREPL == nil {
			return fmt.Errorf("repl mode unavailable")
		}
		return r.RunREPL(ctx)
	case "serve":
		return r.runServe(ctx, args[1:])
	case "service":
		return r.runService(ctx, args[1:])
	case "weixin":
		return r.runWeixin(ctx, args[1:])
	case "version", "--version":
		return r.runVersion(ctx, args[1:])
	case "help", "-h", "--help":
		r.printRootHelp()
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q\n\n%s", args[0], rootHelpText())
	}
}

func (r *Runner) runVersion(ctx context.Context, args []string) error {
	_ = ctx
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var jsonOutput bool
	fs.BoolVar(&jsonOutput, "json", false, "print version information as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected version arguments: %s", strings.Join(fs.Args(), " "))
	}
	info := version.Current()
	if jsonOutput {
		data, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(r.Stdout, string(data))
		return nil
	}
	fmt.Fprintln(r.Stdout, version.Summary())
	return nil
}

func (r *Runner) runAsk(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)

	var sessionSpec string
	var useStdin bool
	var profile string
	fs.StringVar(&sessionSpec, "session", "", "existing session key or channel:key")
	fs.BoolVar(&useStdin, "stdin", false, "read the prompt from stdin")
	fs.StringVar(&profile, "profile", "", "agent profile for this prompt: general or coding")

	if err := fs.Parse(args); err != nil {
		return err
	}

	var prompt string
	switch {
	case useStdin && len(fs.Args()) > 0:
		return fmt.Errorf("ask does not allow prompt arguments together with --stdin")
	case useStdin:
		data, err := io.ReadAll(r.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		prompt = strings.TrimSpace(string(data))
	default:
		prompt = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
	if prompt == "" {
		return fmt.Errorf("missing prompt: pass text arguments or --stdin")
	}

	locator := parseSessionSpecifier(sessionSpec, "cli", oneShotKey(r.Now()))
	applyLocatorAgentProfile(&locator, profile)
	opened, err := r.Backend.OpenSession(ctx, locator)
	if err != nil {
		return err
	}

	printer := newConsolePrinter(r.Stdout, r.Stderr, false)
	unsubscribe, err := r.Backend.AttachSink(opened.SessionID, events.SinkFunc(printer.HandleEvent))
	if err != nil {
		return err
	}
	defer unsubscribe()

	envelope := message.NewCLIEnvelope(opened.SessionID, r.Cfg.LeadName, prompt, r.Now())
	applyEnvelopeAgentProfile(&envelope, profile)
	_, err = r.Backend.Submit(ctx, opened.SessionID, envelope)
	if err != nil {
		return err
	}
	printer.Finish()
	return nil
}

func (r *Runner) runCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("command", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)

	var sessionSpec string
	fs.StringVar(&sessionSpec, "session", "", "session key or channel:key")

	if err := fs.Parse(args); err != nil {
		return err
	}

	raw := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if raw == "" {
		return fmt.Errorf("missing command: pass a slash command such as /tasks")
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	cmd, ok := commands.Parse(raw)
	if !ok {
		return fmt.Errorf("invalid command: %s", raw)
	}

	locator := parseSessionSpecifier(sessionSpec, "local", "default")
	opened, err := r.Backend.OpenSession(ctx, locator)
	if err != nil {
		return err
	}

	result, err := r.Backend.ExecuteCommand(ctx, opened.SessionID, cmd)
	if result.Output != "" {
		fmt.Fprintln(r.Stdout, result.Output)
	}
	return err
}

func (r *Runner) runACPServer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("acp-server", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var profile string
	fs.StringVar(&profile, "profile", "", "agent profile for ACP sessions: general or coding")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected acp-server arguments: %s", strings.Join(fs.Args(), " "))
	}

	// ACP stdio mode: stdout is the JSON-RPC protocol channel.
	// Re-configure logger so all diagnostic output goes to stderr + file,
	// never to stdout which would corrupt the ACP wire format.
	cfg := r.currentConfig()
	if cfg.Logging.FilePath != "" {
		_ = logger.InitWithConfig(logger.Config{
			Level:      cfg.Logging.Level,
			FilePath:   cfg.Logging.FilePath,
			AlsoStderr: true,
		})
	}

	agent := &acpserver.Agent{
		AgentInfo: acp.Implementation{Name: "godex", Version: version.Current().Version},
		Handler:   acpserver.BackendPromptHandlerWithOptions(r.Backend, acpserver.BackendPromptOptions{AgentProfile: profile}),
		Features:  acpserver.BackendFeatures{Backend: r.Backend},
	}
	if err := acpserver.Serve(ctx, acpserver.ServeConfig{
		Agent: agent,
		In:    r.Stdin,
		Out:   r.Stdout,
	}); err != nil {
		return fmt.Errorf("acp server: %w", err)
	}
	return nil
}

func (r *Runner) runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)

	addr := "127.0.0.1:8080"
	fs.StringVar(&addr, "addr", addr, "HTTP listen address")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected serve arguments: %s", strings.Join(fs.Args(), " "))
	}
	if r.Serve == nil {
		return fmt.Errorf("serve mode unavailable")
	}

	fmt.Fprintf(r.Stdout, "Serving GoDex API on http://%s\n", addr)
	return r.Serve(ctx, addr)
}

func (r *Runner) runTUI(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)

	var sessionSpec string
	var profile string
	fs.StringVar(&sessionSpec, "session", "", "session key or channel:key")
	fs.StringVar(&profile, "profile", "", "agent profile for this TUI session: general or coding")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected tui arguments: %s", strings.Join(fs.Args(), " "))
	}
	if r.RunTUI == nil {
		return fmt.Errorf("tui mode unavailable")
	}

	locator := parseSessionSpecifier(sessionSpec, "web", "default")
	if strings.TrimSpace(profile) == "" && r.Cfg != nil {
		profile = r.Cfg.DefaultAgentProfileForChannel("tui")
	}
	applyLocatorAgentProfile(&locator, profile)
	return r.RunTUI(ctx, locator)
}

func (r *Runner) runDoctor(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "storage") {
		return r.runDoctorStorage()
	}
	if len(args) > 0 && args[0] == "sessions" {
		return r.runDoctorSessions(ctx, args[1:])
	}
	if len(args) > 0 && isHelpArg(args[0]) {
		fmt.Fprintln(r.Stdout, doctorHelpText())
		return nil
	}
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected doctor arguments: %s", strings.Join(fs.Args(), " "))
	}
	if r.Doctor == nil {
		return fmt.Errorf("doctor mode unavailable")
	}
	output, err := r.Doctor(ctx)
	if output != "" {
		fmt.Fprintln(r.Stdout, output)
	}
	return err
}

func (r *Runner) runDoctorSessions(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor sessions", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var sessionID string
	fs.StringVar(&sessionID, "session", "", "specific session id to diagnose")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected doctor sessions arguments: %s", strings.Join(fs.Args(), " "))
	}
	report, err := r.diagnoseSessions(ctx, sessionID)
	if err != nil {
		return err
	}
	r.printSessionRepairReport("Session diagnosis", report)
	return nil
}

func (r *Runner) runRepair(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(r.Stdout, repairHelpText())
		return nil
	}
	switch args[0] {
	case "sessions":
		return r.runRepairSessions(ctx, args[1:])
	default:
		return fmt.Errorf("unknown repair subcommand %q\n\n%s", args[0], repairHelpText())
	}
}

func (r *Runner) runRepairSessions(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("repair sessions", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var dryRun bool
	var sessionID string
	fs.BoolVar(&dryRun, "dry-run", false, "show planned repairs without changing files")
	fs.StringVar(&sessionID, "session", "", "specific session id to repair")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected repair sessions arguments: %s", strings.Join(fs.Args(), " "))
	}
	report, err := r.repairSessions(ctx, sessionID, dryRun)
	if err != nil {
		return err
	}
	r.printSessionRepairReport(actionLabel(dryRun), report)
	return nil
}

type sessionRepairBackend interface {
	DiagnoseSessions(context.Context, sessionrepair.Request) (sessionrepair.Report, error)
	RepairSessions(context.Context, sessionrepair.Request) (sessionrepair.Report, error)
}

func (r *Runner) diagnoseSessions(ctx context.Context, sessionID string) (sessionrepair.Report, error) {
	if backend, ok := r.Backend.(sessionRepairBackend); ok {
		return backend.DiagnoseSessions(ctx, sessionrepair.Request{SessionID: sessionID})
	}
	return sessionrepair.Diagnose(sessionrepair.Request{SessionsDir: r.currentConfig().SessionsDir, SessionID: sessionID, Now: r.Now()})
}

func (r *Runner) repairSessions(ctx context.Context, sessionID string, dryRun bool) (sessionrepair.Report, error) {
	req := sessionrepair.Request{SessionsDir: r.currentConfig().SessionsDir, SessionID: sessionID, DryRun: dryRun, Now: r.Now()}
	if dryRun {
		return sessionrepair.Diagnose(req)
	}
	if backend, ok := r.Backend.(sessionRepairBackend); ok {
		return backend.RepairSessions(ctx, req)
	}
	return sessionrepair.Repair(req)
}

func (r *Runner) printSessionRepairReport(prefix string, report sessionrepair.Report) {
	fmt.Fprintf(r.Stdout, "%s: %d session(s), %d finding(s), %d action(s).\n", prefix, len(report.Sessions), len(report.Findings), len(report.Actions))
	for _, session := range report.Sessions {
		status := "ok"
		if session.Error != "" {
			status = "error: " + session.Error
		} else if session.Changed {
			status = "changed"
		} else if len(session.Actions) > 0 {
			status = "planned"
		}
		fmt.Fprintf(r.Stdout, "- %s: %s", session.SessionID, status)
		if session.BackupDir != "" {
			fmt.Fprintf(r.Stdout, " backup=%s", session.BackupDir)
		}
		fmt.Fprintln(r.Stdout)
		for _, finding := range session.Findings {
			fmt.Fprintf(r.Stdout, "  finding %s %s %s\n", finding.Severity, finding.Code, finding.Message)
		}
		for _, action := range session.Actions {
			fmt.Fprintf(r.Stdout, "  action %s %s %s\n", action.Status, action.Code, action.Message)
		}
	}
}

func (r *Runner) runGC(ctx context.Context, args []string) error {
	_ = ctx
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(r.Stdout, gcHelpText())
		return nil
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return r.runGCAll(args)
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprintln(r.Stdout, gcHelpText())
		return nil
	case "browser-cache":
		return r.runGCBrowserCache(args[1:])
	case "sessions":
		return r.runGCSessions(args[1:])
	case "artifacts":
		return r.runGCArtifacts(args[1:])
	case "subagents":
		return r.runGCSubagents(args[1:])
	default:
		return fmt.Errorf("unknown gc subcommand %q\n\n%s", args[0], gcHelpText())
	}
}

func (r *Runner) runDoctorStorage() error {
	result := storagegc.Scan(r.storageGCOptions(false, 0))
	r.printStorageGCResult("Storage", result)
	diag := sessionStoreDiagnostics(r.currentConfig())
	status := "unhealthy"
	if diag.Healthy {
		status = "healthy"
	}
	path := diag.Path
	if path == "" {
		path = diag.SQLitePath
	}
	fmt.Fprintf(r.Stdout, "Session store: backend=%s status=%s path=%s schema=%d\n", diag.Backend, status, path, diag.SchemaVersion)
	if diag.Error != "" {
		fmt.Fprintf(r.Stdout, "Session store error: %s\n", diag.Error)
	}
	return nil
}

func sessionStoreDiagnostics(cfg *config.Config) sessionstore.Diagnostics {
	if cfg == nil {
		return sessionstore.Diagnostics{Healthy: false, Error: "missing config"}
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Storage.SessionBackend), "sqlite") {
		path := strings.TrimSpace(cfg.Storage.SQLitePath)
		if path == "" {
			path = filepath.Join(cfg.StateDir, "session-store.sqlite")
		}
		store, err := sessionstore.NewSQLiteStore(path)
		if err != nil {
			return sessionstore.Diagnostics{Backend: string(sessionstore.BackendSQLite), SQLitePath: path, Error: err.Error()}
		}
		defer store.Close()
		return store.Diagnostics(context.Background())
	}
	return sessionstore.NewJSONStore(cfg.SessionsDir).Diagnostics(context.Background())
}

func (r *Runner) runGCAll(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var dryRun bool
	fs.BoolVar(&dryRun, "dry-run", false, "show cleanable storage without deleting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected gc arguments: %s", strings.Join(fs.Args(), " "))
	}
	opts := r.storageGCOptions(dryRun, 0)
	var combined storagegc.Result
	for _, clean := range []func(storagegc.Options) (storagegc.Result, error){
		storagegc.CleanBrowserCache,
		storagegc.CleanSessionCheckpoints,
		storagegc.CleanArtifacts,
	} {
		result, err := clean(opts)
		if err != nil {
			return err
		}
		combined.Items = append(combined.Items, result.Items...)
		combined.Candidates += result.Candidates
		combined.Bytes += result.Bytes
	}
	r.printStorageGCResult(actionLabel(dryRun), combined)
	return nil
}

func (r *Runner) runGCBrowserCache(args []string) error {
	dryRun, err := parseGCDryRunOnly("gc browser-cache", args, r.Stderr)
	if err != nil {
		return err
	}
	result, err := storagegc.CleanBrowserCache(r.storageGCOptions(dryRun, 0))
	if err != nil {
		return err
	}
	r.printStorageGCResult(actionLabel(dryRun), result)
	return nil
}

func (r *Runner) runGCSessions(args []string) error {
	dryRun, olderThan, err := parseGCDryRunOlderThan("gc sessions", args, r.Stderr)
	if err != nil {
		return err
	}
	result, err := storagegc.CleanSessionCheckpoints(r.storageGCOptions(dryRun, olderThan))
	if err != nil {
		return err
	}
	r.printStorageGCResult(actionLabel(dryRun), result)
	return nil
}

func (r *Runner) runGCArtifacts(args []string) error {
	dryRun, olderThan, err := parseGCDryRunOlderThan("gc artifacts", args, r.Stderr)
	if err != nil {
		return err
	}
	result, err := storagegc.CleanArtifacts(r.storageGCOptions(dryRun, olderThan))
	if err != nil {
		return err
	}
	r.printStorageGCResult(actionLabel(dryRun), result)
	return nil
}

func (r *Runner) runGCSubagents(args []string) error {
	fs := flag.NewFlagSet("gc subagents", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var dryRun bool
	var mergedOnly bool
	var olderThan string
	fs.BoolVar(&dryRun, "dry-run", false, "show cleanable subagent workspaces without deleting")
	fs.BoolVar(&mergedOnly, "merged", false, "only clean merged or no-change subagent workspaces")
	fs.StringVar(&olderThan, "older-than", "", "also clean terminal subagent workspaces older than a duration such as 24h")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected gc subagents arguments: %s", strings.Join(fs.Args(), " "))
	}
	var duration time.Duration
	if strings.TrimSpace(olderThan) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(olderThan))
		if err != nil {
			return fmt.Errorf("parse --older-than: %w", err)
		}
		duration = parsed
	}
	result, err := agent.CleanupSubagentWorkspaces(r.currentConfig(), agent.SubagentWorkspaceGCOptions{
		DryRun:     dryRun,
		MergedOnly: mergedOnly,
		OlderThan:  duration,
		Now:        r.Now(),
	})
	if err != nil {
		return err
	}
	action := "Would clean"
	if !dryRun {
		action = "Cleaned"
	}
	fmt.Fprintf(r.Stdout, "%s %d subagent workspace(s), %.1f MB candidate data.\n", action, result.Candidates, float64(result.Bytes)/(1024*1024))
	for _, item := range result.Items {
		status := "candidate"
		if item.Cleaned {
			status = "cleaned"
		}
		if item.Reason != "" {
			status = item.Reason
		}
		fmt.Fprintf(r.Stdout, "- %s %s %.1f MB %s/%s\n", item.JobID, status, float64(item.Bytes)/(1024*1024), item.Isolation, item.MergeStatus)
	}
	return nil
}

func (r *Runner) storageGCOptions(dryRun bool, olderThan time.Duration) storagegc.Options {
	cfg := r.currentConfig()
	artifactTTL := time.Duration(cfg.Storage.ArtifactTTLHours) * time.Hour
	checkpointTTL := time.Duration(cfg.Storage.SessionCheckpointTTLHours) * time.Hour
	if olderThan > 0 {
		artifactTTL = olderThan
		checkpointTTL = olderThan
	}
	return storagegc.Options{
		StateDir:                    cfg.StateDir,
		TempDir:                     cfg.TempDir,
		SessionsDir:                 cfg.SessionsDir,
		DryRun:                      dryRun,
		Now:                         r.Now(),
		ArtifactTTL:                 artifactTTL,
		SessionCheckpointTTL:        checkpointTTL,
		SessionCheckpointKeepLatest: cfg.Storage.SessionCheckpointKeepLatest,
	}
}

func (r *Runner) printStorageGCResult(prefix string, result storagegc.Result) {
	fmt.Fprintf(r.Stdout, "%s %d storage item(s), %.1f MB candidate data.\n", prefix, result.Candidates, float64(result.Bytes)/(1024*1024))
	for _, item := range result.Items {
		fmt.Fprintf(r.Stdout, "- %s\t%.1f MB\t%s\t%s\t%s\n", item.Category, float64(item.Bytes)/(1024*1024), item.Risk, item.Action, item.Path)
	}
}

func parseGCDryRunOnly(name string, args []string, stderr io.Writer) (bool, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var dryRun bool
	fs.BoolVar(&dryRun, "dry-run", false, "show cleanable storage without deleting")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	if len(fs.Args()) > 0 {
		return false, fmt.Errorf("unexpected %s arguments: %s", name, strings.Join(fs.Args(), " "))
	}
	return dryRun, nil
}

func parseGCDryRunOlderThan(name string, args []string, stderr io.Writer) (bool, time.Duration, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var dryRun bool
	var olderThan string
	fs.BoolVar(&dryRun, "dry-run", false, "show cleanable storage without deleting")
	fs.StringVar(&olderThan, "older-than", "", "clean items older than a duration such as 168h")
	if err := fs.Parse(args); err != nil {
		return false, 0, err
	}
	if len(fs.Args()) > 0 {
		return false, 0, fmt.Errorf("unexpected %s arguments: %s", name, strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(olderThan) == "" {
		return dryRun, 0, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(olderThan))
	if err != nil {
		return false, 0, fmt.Errorf("parse --older-than: %w", err)
	}
	return dryRun, parsed, nil
}

func actionLabel(dryRun bool) string {
	if dryRun {
		return "Would clean"
	}
	return "Cleaned"
}

func gcHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex gc [--dry-run]",
		"  godex gc browser-cache [--dry-run]",
		"  godex gc sessions [--dry-run] [--older-than 168h]",
		"  godex gc artifacts [--dry-run] [--older-than 168h]",
		"  godex gc subagents [--dry-run] [--merged] [--older-than 24h]",
		"",
		"Inspect or clean local GoDex runtime storage.",
		"",
		"Examples:",
		"  godex gc --dry-run",
		"  godex gc browser-cache",
		"  godex gc artifacts --dry-run --older-than 168h",
		"  godex gc subagents --dry-run",
		"  godex gc subagents --merged --older-than 24h",
	}, "\n")
}

func (r *Runner) runProviders(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing providers subcommand\n\n%s", providersHelpText())
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprintln(r.Stdout, providersHelpText())
		return nil
	case "list":
		if len(args) > 1 {
			return fmt.Errorf("unexpected providers list arguments: %s", strings.Join(args[1:], " "))
		}
		for _, status := range providers.List(r.currentConfig()).Providers {
			fmt.Fprintf(r.Stdout, "%s\t%s\t%s\tcredential=%s\tpresent=%v\n", status.ID, status.Type, status.APIKeyEnv, status.CredentialKind, status.HasCredential)
		}
		return nil
	case "test":
		if len(args) != 2 {
			return fmt.Errorf("usage: godex providers test <id>")
		}
		result := providers.Test(ctx, r.currentConfig(), args[1])
		if result.OK {
			fmt.Fprintf(r.Stdout, "%s OK\n", result.Status.ID)
			return nil
		}
		return fmt.Errorf("%s: %s", result.Status.ID, result.Error)
	default:
		return fmt.Errorf("unknown providers subcommand %q\n\n%s", args[0], providersHelpText())
	}
}

func providersHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex providers list",
		"  godex providers test <id>",
		"",
		"Manage configured LLM providers.",
		"",
		"Examples:",
		"  godex providers list",
		"  godex providers test codex",
	}, "\n")
}

func (r *Runner) runLogin(ctx context.Context, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		fmt.Fprintln(r.Stdout, loginHelpText())
		return nil
	}
	target := strings.ToLower(strings.TrimSpace(args[0]))
	fs := flag.NewFlagSet("login "+target, flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	mode := "auto"
	fs.StringVar(&mode, "mode", mode, "platform-api-key, codex-oauth, or auto")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected login arguments: %s", strings.Join(fs.Args(), " "))
	}
	if r.ConfigManager == nil {
		return fmt.Errorf("config manager unavailable")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch target {
	case "openai":
		return r.loginOpenAI(ctx, mode)
	case "codex":
		return r.loginCodex(ctx, mode)
	default:
		return fmt.Errorf("unsupported login provider %q", target)
	}
}

func (r *Runner) runLogout(ctx context.Context, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		fmt.Fprintln(r.Stdout, logoutHelpText())
		return nil
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: godex logout openai|codex")
	}
	if r.ConfigManager == nil {
		return fmt.Errorf("config manager unavailable")
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "openai":
		if err := r.ConfigManager.RemoveHomeEnvVar("OPENAI_API_KEY"); err != nil {
			return err
		}
		if _, err := r.ConfigManager.Update(ctx, config.UpdateRequest{}); err != nil {
			return err
		}
		fmt.Fprintln(r.Stdout, "Logged out openai; removed OPENAI_API_KEY from home .env.")
	case "codex":
		if err := r.ConfigManager.RemoveHomeEnvVar("GODEX_OPENAI_CODEX_OAUTH_TOKEN"); err != nil {
			return err
		}
		if _, err := r.ConfigManager.Update(ctx, config.UpdateRequest{}); err != nil {
			return err
		}
		fmt.Fprintln(r.Stdout, "Logged out codex; removed GODEX_OPENAI_CODEX_OAUTH_TOKEN from home .env.")
	default:
		return fmt.Errorf("unsupported logout provider %q", args[0])
	}
	return nil
}

func (r *Runner) runMigrate(ctx context.Context, args []string) error {
	_ = ctx
	if len(args) == 0 || isHelpArg(args[0]) {
		fmt.Fprintln(r.Stdout, migrateHelpText())
		return nil
	}
	if strings.TrimSpace(args[0]) != "home" {
		return fmt.Errorf("usage: godex migrate home [--dry-run]")
	}
	fs := flag.NewFlagSet("migrate home", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	dryRun := false
	fs.BoolVar(&dryRun, "dry-run", false, "show what would be copied")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if r.ConfigManager == nil {
		return fmt.Errorf("config manager unavailable")
	}
	meta := r.ConfigManager.Meta()
	if dryRun {
		fmt.Fprintf(r.Stdout, "Would migrate project config %s -> %s\n", meta.ProjectConfigFile, meta.HomeConfigFile)
		fmt.Fprintf(r.Stdout, "Would migrate project env %s -> %s\n", meta.ProjectEnvFile, meta.HomeEnvFile)
		return nil
	}
	if err := copyIfExistsAndMissing(meta.ProjectConfigFile, meta.HomeConfigFile, 0600); err != nil {
		return err
	}
	if err := copyIfExistsAndMissing(meta.ProjectEnvFile, meta.HomeEnvFile, 0600); err != nil {
		return err
	}
	fmt.Fprintf(r.Stdout, "Migration checked. Home config: %s\n", meta.HomeConfigFile)
	return nil
}

func (r *Runner) loginOpenAI(ctx context.Context, mode string) error {
	if mode != "auto" && mode != "platform-api-key" {
		return fmt.Errorf("openai login supports --mode platform-api-key or auto")
	}
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key == "" {
		var err error
		key, err = r.readCredential("OpenAI API key")
		if err != nil {
			return err
		}
	}
	if auth.IdentifyKey(key) == auth.KeyKindCodexOAuth && mode == "auto" {
		return r.configureCodex(ctx, key, "auto")
	}
	return r.configureOpenAI(ctx, key)
}

func (r *Runner) loginCodex(ctx context.Context, mode string) error {
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case "platform-api-key":
		return r.loginOpenAI(ctx, mode)
	case "auto":
		if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" && auth.IdentifyKey(key) == auth.KeyKindAPIKey {
			return r.configureOpenAI(ctx, key)
		}
		fallthrough
	case "codex-oauth":
		preflight := auth.RunTLSPreflight(5 * time.Second)
		if preflight != nil && !preflight.OK {
			return fmt.Errorf("%s", auth.FormatTLSPreflightFix(preflight))
		}
		openBrowser := r.OpenBrowser
		if openBrowser == nil {
			openBrowser = openURL
		}
		flowCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		result, err := auth.PKCEFlow(flowCtx, auth.OpenAICodexProvider(), openBrowser)
		if err != nil {
			return err
		}
		if result == nil {
			return fmt.Errorf("codex OAuth did not return a result")
		}
		if result.Err != nil {
			return result.Err
		}
		return r.configureCodex(ctx, result.APIKey, "codex-oauth")
	default:
		return fmt.Errorf("codex login supports --mode codex-oauth, platform-api-key, or auto")
	}
}

func (r *Runner) configureOpenAI(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("missing OpenAI API key")
	}
	if err := r.ConfigManager.WriteHomeEnvVar("OPENAI_API_KEY", key); err != nil {
		return err
	}
	providersMap := r.providerMap()
	providersMap["openai"] = llm.ProviderConfig{
		Name:           "OpenAI",
		Type:           config.ProviderOpenAICompatible,
		BaseURL:        "https://api.openai.com/v1",
		APIKeyEnv:      "OPENAI_API_KEY",
		CredentialKind: "api-key",
		TimeoutSeconds: 600,
		Models: map[string]llm.ModelConfig{
			"gpt": {Name: "GPT", Model: "gpt-5.4-mini", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
		},
	}
	if _, err := r.ConfigManager.Update(ctx, config.UpdateRequest{Values: providerSaveValues("openai.gpt", providersMap)}); err != nil {
		return err
	}
	fmt.Fprintln(r.Stdout, "OpenAI provider configured. Secret stored in home .env as OPENAI_API_KEY.")
	return nil
}

func (r *Runner) configureCodex(ctx context.Context, token, mode string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("missing Codex OAuth token")
	}
	if err := r.ConfigManager.WriteHomeEnvVar("GODEX_OPENAI_CODEX_OAUTH_TOKEN", token); err != nil {
		return err
	}
	providersMap := r.providerMap()
	providersMap["codex"] = llm.ProviderConfig{
		Name:           "OpenAI Codex",
		Type:           config.ProviderOpenAICodex,
		BaseURL:        "https://chatgpt.com/backend-api/codex",
		APIKeyEnv:      "GODEX_OPENAI_CODEX_OAUTH_TOKEN",
		CredentialKind: "codex-oauth",
		OAuth:          llm.OAuthConfig{Provider: "openai", Mode: mode},
		TimeoutSeconds: 600,
		Models: map[string]llm.ModelConfig{
			"gpt-5.5": {Name: "GPT-5.5", Model: "gpt-5.5", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
			"gpt-5.4": {Name: "GPT-5.4", Model: "gpt-5.4", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
		},
	}
	if _, err := r.ConfigManager.Update(ctx, config.UpdateRequest{Values: providerSaveValues("codex.gpt-5.5", providersMap)}); err != nil {
		return err
	}
	fmt.Fprintln(r.Stdout, "Codex provider configured. Secret stored in home .env as GODEX_OPENAI_CODEX_OAUTH_TOKEN.")
	return nil
}

func (r *Runner) currentConfig() *config.Config {
	if r.ConfigManager != nil {
		return r.ConfigManager.Current()
	}
	return r.Cfg
}

func (r *Runner) providerMap() map[string]llm.ProviderConfig {
	current := r.currentConfig()
	out := map[string]llm.ProviderConfig{}
	if current == nil {
		return out
	}
	for id, provider := range current.LLMProviders {
		provider.APIKey = ""
		out[id] = provider
	}
	return out
}

func providerSaveValues(defaultModel string, providerMap map[string]llm.ProviderConfig) map[string]any {
	return map[string]any{
		"api.default_model": defaultModel,
		"api.providers":     providerMap,
		"api.model_strategy": llm.StrategyConfig{
			Type:       llm.StrategyFallback,
			Candidates: []llm.ModelRef{mustModelRef(defaultModel)},
		},
	}
}

func mustModelRef(profileID string) llm.ModelRef {
	ref, ok := llm.ParseProfileID(profileID)
	if !ok {
		return llm.ModelRef{}
	}
	return ref
}

func (r *Runner) readCredential(label string) (string, error) {
	if r.Stdin == nil {
		return "", fmt.Errorf("missing %s", strings.ToLower(label))
	}
	if r.Stderr != nil {
		fmt.Fprintf(r.Stderr, "%s: ", label)
	}
	line, err := bufio.NewReader(r.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return "", fmt.Errorf("missing %s", strings.ToLower(label))
	}
	return value, nil
}

func (r *Runner) runWeixin(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing weixin subcommand\n\n%s", weixinHelpText())
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprintln(r.Stdout, weixinHelpText())
		return nil
	case "setup":
		if len(args) > 1 {
			return fmt.Errorf("unexpected weixin setup arguments: %s", strings.Join(args[1:], " "))
		}
		if r.WeixinSetup == nil {
			return fmt.Errorf("weixin setup unavailable")
		}
		return r.WeixinSetup(ctx)
	case "logout":
		if len(args) > 1 {
			return fmt.Errorf("unexpected weixin logout arguments: %s", strings.Join(args[1:], " "))
		}
		if r.WeixinLogout == nil {
			return fmt.Errorf("weixin logout unavailable")
		}
		return r.WeixinLogout(ctx)
	default:
		return fmt.Errorf("unknown weixin subcommand %q\n\n%s", args[0], weixinHelpText())
	}
}

func weixinHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex weixin setup",
		"  godex weixin logout",
		"",
		"Manage Weixin/iLink channel authentication for the current workspace.",
		"",
		"Examples:",
		"  godex weixin setup",
		"  godex weixin logout",
	}, "\n")
}

func (r *Runner) printRootHelp() {
	fmt.Fprintln(r.Stdout, rootHelpText())
}

func rootHelpText() string {
	return strings.Join([]string{
		"GoDex - local-first AI agent workspace",
		"",
		"Usage:",
		"  godex [global flags] <command> [flags]",
		"  godex                         Start the terminal UI",
		"",
		"Quick start:",
		"  godex init                     Initialize a workspace",
		"  godex serve --addr 127.0.0.1:8088",
		"                                 Start Web UI and HTTP API",
		"  godex ask [--profile coding] \"...\"",
		"                                 Run a one-shot prompt",
		"",
		"Commands:",
		"  Chat",
		"    ask        Run a one-shot prompt",
		"    command    Run a slash command",
		"    repl       Open the legacy readline session",
		"    longtask   Create, run, and inspect durable story-loop tasks",
		"",
		"  Web & service",
		"    serve      Start Web UI and HTTP API",
		"    service    Install, start, stop, restart, inspect, or uninstall the service",
		"    acp-server Run GoDex as an ACP stdio agent",
		"    version    Print GoDex version information",
		"",
		"  Config",
		"    init       Initialize a workspace",
		"    setup      Alias for init",
		"    doctor     Diagnose config and runtime problems",
		"    login      Configure OpenAI or Codex credentials",
		"    logout     Remove stored credentials",
		"    config     Interactive provider configuration wizard",
		"    providers  List and test provider configuration",
		"    migrate    Migrate project config into ~/.godex",
		"    repair     Diagnose or repair persisted session state",
		"    gc         Inspect or clean local runtime storage",
		"    import     Import external agent ecosystem resources",
		"",
		"  Automation & channels",
		"    weixin     Setup or logout Weixin/iLink channel auth",
		"    eval       Run, list, or show evaluation suites",
		"",
		"Global flags:",
		"  --config path  Use an explicit godex.yaml for this process",
		"",
		"Profiles:",
		"  ACP/CLI/TUI default to the lean coding profile; Web and IM channels default to general.",
		"  Use --profile general|coding on ask or acp-server to override that entrypoint.",
		"",
		"Examples:",
		"  godex init --dir .",
		"  godex serve --addr 127.0.0.1:8088",
		"  godex service install --scope user --addr 127.0.0.1:8088",
		"  godex providers list",
		"  godex import claude --source .claude --dry-run",
		"  godex longtask list --session local:default",
		"  godex repair sessions --dry-run",
		"  godex version",
		"  godex doctor",
		"",
		"More help:",
		"  godex service --help",
		"  godex providers --help",
		"  godex import --help",
		"  godex repair --help",
		"  godex eval --help",
		"  godex weixin --help",
	}, "\n")
}

func repairHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex repair sessions --dry-run [--session <id>]",
		"  godex repair sessions [--session <id>]",
		"",
		"Diagnose or repair low-risk persisted session JSON/checkpoint inconsistencies.",
		"",
		"Examples:",
		"  godex doctor sessions",
		"  godex repair sessions --dry-run",
		"  godex repair sessions --session web-123",
	}, "\n")
}

func parseSessionSpecifier(spec, defaultChannel, defaultKey string) backend.SessionLocator {
	channel := defaultChannel
	key := defaultKey
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return backend.SessionLocator{Channel: channel, Key: key}
	}
	if parts := strings.SplitN(spec, ":", 2); len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
		channel = strings.TrimSpace(parts[0])
		key = strings.TrimSpace(parts[1])
	} else {
		key = spec
	}
	return backend.SessionLocator{Channel: channel, Key: key}
}

func applyLocatorAgentProfile(locator *backend.SessionLocator, profile string) {
	if strings.TrimSpace(profile) == "" {
		return
	}
	profile = config.NormalizeAgentProfile(profile)
	if locator.Metadata == nil {
		locator.Metadata = map[string]string{}
	}
	locator.Metadata["agent_profile"] = profile
}

func applyEnvelopeAgentProfile(envelope *message.Envelope, profile string) {
	if strings.TrimSpace(profile) == "" {
		return
	}
	profile = config.NormalizeAgentProfile(profile)
	if envelope.Metadata == nil {
		envelope.Metadata = map[string]string{}
	}
	envelope.Metadata["agent_profile"] = profile
}

func oneShotKey(now time.Time) string {
	return fmt.Sprintf("oneshot-%d", now.UnixNano())
}

func copyIfExistsAndMissing(src, dst string, perm os.FileMode) error {
	if strings.TrimSpace(src) == "" || strings.TrimSpace(dst) == "" {
		return nil
	}
	data, err := os.ReadFile(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	return os.WriteFile(dst, data, perm)
}

func openURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("missing URL")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}

type consolePrinter struct {
	stdout        io.Writer
	stderr        io.Writer
	printErrors   bool
	assistantOpen bool
}

func newConsolePrinter(stdout, stderr io.Writer, printErrors bool) *consolePrinter {
	return &consolePrinter{
		stdout:      stdout,
		stderr:      stderr,
		printErrors: printErrors,
	}
}

// HandleEvent renders one runtime event to the console.
func (p *consolePrinter) HandleEvent(event events.Event) {
	switch payload := event.Payload.(type) {
	case events.TextPayload:
		switch event.Type {
		case events.EventAssistantTextDelta:
			p.assistantOpen = true
			fmt.Fprint(p.stdout, payload.Text)
		case events.EventAssistantMessageComplete:
			if p.assistantOpen {
				fmt.Fprintln(p.stdout)
				p.assistantOpen = false
			}
		}
	case events.ToolCallPayload:
		if event.Type == events.EventToolCallFinished {
			if payload.Name == "todo_write" && payload.Error == "" {
				return
			}
			output := payload.Output
			if len(output) > 200 {
				output = output[:200]
			}
			fmt.Fprintf(p.stdout, "> %s:\n%s\n", payload.Name, output)
		}
	case events.TodoListPayload:
		if event.Type == events.EventTodoListUpdated {
			if p.assistantOpen {
				fmt.Fprintln(p.stdout)
				p.assistantOpen = false
			}
			fmt.Fprintln(p.stdout, payload.RenderPlain())
		}
	case events.NoticePayload:
		switch event.Type {
		case events.EventWarningRaised:
			fmt.Fprintf(p.stderr, "Warning: %s\n", payload.Message)
		case events.EventErrorRaised:
			if p.printErrors {
				fmt.Fprintf(p.stderr, "Error: %s\n", payload.Message)
			}
		}
	}
}

// Finish closes any still-open assistant line.
func (p *consolePrinter) Finish() {
	if p.assistantOpen {
		fmt.Fprintln(p.stdout)
		p.assistantOpen = false
	}
}

// isHelpArg returns true if the argument is a help flag.
func isHelpArg(arg string) bool {
	switch strings.TrimSpace(strings.ToLower(arg)) {
	case "help", "-h", "--help":
		return true
	default:
		return false
	}
}

// containsHelpArg returns true if any of the arguments is a help flag.
func containsHelpArg(args []string) bool {
	for _, a := range args {
		if isHelpArg(a) {
			return true
		}
	}
	return false
}

func replHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex repl [--session key] [--profile general|coding]",
		"",
		"Open an interactive readline session for chatting with the agent.",
		"",
		"Flags:",
		"  --session string   session key or channel:key",
		"  --profile string   agent profile: general or coding",
	}, "\n")
}

func longtaskHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex longtask list [--session key]",
		"  godex longtask create --file spec.json [--session key]",
		"  godex longtask run <id> [--session key] [--auto-repair] [--max-repair-attempts N] [--max-iterations N] [--wait-timeout-ms N] [--async] [--no-stop-on-failure] [--resume-run-id <id>]",
		"  godex longtask status <id> [--session key]",
		"  godex longtask cancel <id> (--node <node_id> | --all) [--session key]",
		"  godex longtask finalize <id> --node <node_id> [--session key]",
		"",
		"Create, run, and inspect durable story-loop tasks.",
		"Default run behavior: stop on the first blocked story. Pass",
		"--no-stop-on-failure to keep running past blocked stories.",
		"Pass --resume-run-id to continue a run that was interrupted",
		"(e.g. by Ctrl+C or HTTP client disconnect).",
	}, "\n")
}

func doctorHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex doctor              Diagnose config and runtime problems",
		"  godex doctor sessions     Diagnose persisted session state",
		"  godex doctor storage      Diagnose storage directories",
		"",
		"Run health checks on configuration, channels, sessions, and storage.",
	}, "\n")
}

func loginHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex login openai [--mode platform-api-key|auto]",
		"  godex login codex [--mode codex-oauth|platform-api-key|auto]",
		"",
		"Configure OpenAI or Codex credentials for the current workspace.",
	}, "\n")
}

func logoutHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex logout openai",
		"  godex logout codex",
		"",
		"Remove stored credentials for the given provider.",
	}, "\n")
}

func migrateHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex migrate home [--dry-run]",
		"",
		"Migrate project-level config files into ~/.godex home directory.",
		"",
		"Flags:",
		"  --dry-run   show what would be copied without changing files",
	}, "\n")
}
