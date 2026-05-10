package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/evalharness"
	"github.com/tim5wang/godex/internal/tools"
)

type fakeBackend struct {
	locators      []backend.SessionLocator
	submitted     []message.Envelope
	executed      []commands.Command
	sink          events.Sink
	commandResult commands.Result
	commandErr    error
	submitErr     error
}

type fakeEvalCommandBackend struct{}

func (f fakeEvalCommandBackend) OpenSession(ctx context.Context, locator backend.SessionLocator) (*backend.OpenedSession, error) {
	_ = ctx
	return &backend.OpenedSession{SessionID: "eval-session", Locator: locator}, nil
}
func (f fakeEvalCommandBackend) SetSessionModelProfile(ctx context.Context, sessionID, profileID string) (backend.ModelsView, error) {
	_ = ctx
	_ = sessionID
	return backend.ModelsView{SessionProfileID: profileID}, nil
}
func (f fakeEvalCommandBackend) Submit(ctx context.Context, sessionID string, envelope message.Envelope) (*backend.SubmitResult, error) {
	_ = ctx
	_ = envelope
	return &backend.SubmitResult{SessionID: sessionID, TurnID: "turn-1", Completed: true, Status: "completed"}, nil
}
func (f fakeEvalCommandBackend) SubmitAsync(ctx context.Context, sessionID string, envelope message.Envelope, _ ...backend.SubmitOptions) (*backend.SubmitResult, error) {
	return f.Submit(ctx, sessionID, envelope)
}
func (f fakeEvalCommandBackend) PendingPermissions(ctx context.Context, sessionID string) ([]tools.PendingPermission, error) {
	_ = ctx
	_ = sessionID
	return nil, nil
}
func (f fakeEvalCommandBackend) Snapshot(ctx context.Context, sessionID string) (backend.Snapshot, error) {
	_ = ctx
	return backend.Snapshot{SessionID: sessionID, Messages: []protocol.Message{protocol.NewTextMessage(protocol.RoleAssistant, "ready")}}, nil
}
func (f fakeEvalCommandBackend) Timeline(ctx context.Context, sessionID string, limit int) ([]events.Event, error) {
	_ = ctx
	_ = sessionID
	_ = limit
	return nil, nil
}

func (f *fakeBackend) OpenSession(ctx context.Context, locator backend.SessionLocator) (*backend.OpenedSession, error) {
	_ = ctx
	f.locators = append(f.locators, locator)
	return &backend.OpenedSession{SessionID: "session-1", Locator: locator}, nil
}

func (f *fakeBackend) Submit(ctx context.Context, sessionID string, envelope message.Envelope) (*backend.SubmitResult, error) {
	_ = ctx
	_ = sessionID
	f.submitted = append(f.submitted, envelope)
	if f.sink != nil {
		f.sink.Emit(events.Event{
			SessionID: sessionID,
			Type:      events.EventAssistantTextDelta,
			Payload:   events.TextPayload{Role: "assistant", Text: "hello back"},
		})
		f.sink.Emit(events.Event{
			SessionID: sessionID,
			Type:      events.EventAssistantMessageComplete,
			Payload:   events.TextPayload{Role: "assistant", Text: "hello back"},
		})
	}
	return &backend.SubmitResult{SessionID: sessionID, TurnID: "turn-1", Completed: f.submitErr == nil}, f.submitErr
}

func (f *fakeBackend) SubmitAsync(ctx context.Context, sessionID string, envelope message.Envelope, _ ...backend.SubmitOptions) (*backend.SubmitResult, error) {
	return f.Submit(ctx, sessionID, envelope)
}

func (f *fakeBackend) ExecuteCommand(ctx context.Context, sessionID string, cmd commands.Command) (commands.Result, error) {
	_ = ctx
	_ = sessionID
	f.executed = append(f.executed, cmd)
	return f.commandResult, f.commandErr
}

func (f *fakeBackend) AttachSink(sessionID string, sink events.Sink) (func(), error) {
	_ = sessionID
	f.sink = sink
	return func() {
		f.sink = nil
	}, nil
}

func (f *fakeBackend) PendingPermissions(ctx context.Context, sessionID string) ([]tools.PendingPermission, error) {
	_ = ctx
	_ = sessionID
	return nil, nil
}

func (f *fakeBackend) ApprovePermission(ctx context.Context, sessionID, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	_ = ctx
	_ = sessionID
	return tools.PermissionResolution{RequestID: requestID, Decision: tools.PermissionAllow, Scope: scope}, nil
}

func (f *fakeBackend) DenyPermission(ctx context.Context, sessionID, requestID, reason string) (tools.PermissionResolution, error) {
	_ = ctx
	_ = sessionID
	return tools.PermissionResolution{RequestID: requestID, Decision: tools.PermissionDeny, Reason: reason}, nil
}

func (f *fakeBackend) Models(ctx context.Context, sessionID string) (backend.ModelsView, error) {
	_ = ctx
	_ = sessionID
	return backend.ModelsView{}, nil
}

func (f *fakeBackend) SetSessionModelProfile(ctx context.Context, sessionID, profileID string) (backend.ModelsView, error) {
	_ = ctx
	_ = sessionID
	return backend.ModelsView{SessionProfileID: profileID}, nil
}

func (f *fakeBackend) ListLongTasks(ctx context.Context, sessionID string) ([]agent.LongTaskView, error) {
	_ = ctx
	_ = sessionID
	return nil, nil
}

func (f *fakeBackend) GetLongTask(ctx context.Context, sessionID, workflowID string) (agent.LongTaskView, error) {
	_ = ctx
	_ = sessionID
	return agent.LongTaskView{LongTaskID: workflowID, WorkflowID: workflowID}, nil
}

func (f *fakeBackend) CreateLongTask(ctx context.Context, sessionID string, args agent.LongTaskArgs) (agent.LongTaskView, error) {
	_ = ctx
	_ = sessionID
	id := args.LongTaskID
	if id == "" {
		id = args.WorkflowID
	}
	return agent.LongTaskView{LongTaskID: id, WorkflowID: id}, nil
}

func (f *fakeBackend) RunLongTask(ctx context.Context, sessionID, workflowID string, args agent.LongTaskArgs) (agent.LongTaskView, error) {
	_ = ctx
	_ = sessionID
	_ = args
	return agent.LongTaskView{LongTaskID: workflowID, WorkflowID: workflowID}, nil
}

func (f *fakeBackend) CancelLongTask(ctx context.Context, sessionID, workflowID, nodeID string) (agent.LongTaskView, error) {
	_ = ctx
	_ = sessionID
	_ = nodeID
	return agent.LongTaskView{LongTaskID: workflowID, WorkflowID: workflowID}, nil
}

func (f *fakeBackend) FinalizeLongTaskStory(ctx context.Context, sessionID, workflowID, nodeID string) (agent.LongTaskView, error) {
	_ = ctx
	_ = sessionID
	_ = nodeID
	return agent.LongTaskView{LongTaskID: workflowID, WorkflowID: workflowID}, nil
}

func TestRunnerAskUsesPromptArguments(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	backend := &fakeBackend{}
	runner := &Runner{
		Cfg:     &config.Config{LeadName: "lead"},
		Backend: backend,
		Stdout:  stdout,
		Stderr:  stderr,
		Stdin:   strings.NewReader(""),
		Now: func() time.Time {
			return time.Unix(123, 0)
		},
	}

	if err := runner.Run(context.Background(), []string{"ask", "hello", "world"}); err != nil {
		t.Fatalf("run ask: %v", err)
	}
	if len(backend.locators) != 1 {
		t.Fatalf("expected one session open, got %d", len(backend.locators))
	}
	if backend.locators[0].Channel != "cli" || backend.locators[0].Key != "oneshot-123000000000" {
		t.Fatalf("unexpected ask locator: %+v", backend.locators[0])
	}
	if len(backend.submitted) != 1 || backend.submitted[0].Text != "hello world" {
		t.Fatalf("unexpected submitted envelope: %+v", backend.submitted)
	}
	if got := stdout.String(); got != "hello back\n" {
		t.Fatalf("unexpected ask output %q", got)
	}
}

func TestConsolePrinterRendersTodoListUpdated(t *testing.T) {
	stdout := &bytes.Buffer{}
	printer := newConsolePrinter(stdout, &bytes.Buffer{}, false)

	printer.HandleEvent(events.Event{
		Type: events.EventToolCallFinished,
		Payload: events.ToolCallPayload{
			Name:   "todo_write",
			Output: "[x] Inspect changes\n[ ] Run tests",
		},
	})
	printer.HandleEvent(events.Event{
		Type: events.EventTodoListUpdated,
		Payload: events.TodoListPayload{
			Total:     2,
			Completed: 1,
			Pending:   1,
			Items: []events.TodoItemPayload{
				{Content: "Inspect changes", Status: "completed", ActiveForm: "Inspecting changes"},
				{Content: "Run tests", Status: "pending", ActiveForm: "Running tests"},
			},
		},
	})

	got := stdout.String()
	for _, want := range []string{"Todo list (1/2 completed)", "[x] Inspect changes", "[ ] Run tests"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected console todo output to contain %q, got %q", want, got)
		}
	}
	if strings.Contains(got, "> todo_write") {
		t.Fatalf("expected todo output not to use tool log prefix, got %q", got)
	}
}

func TestRunnerAskProfileFlagAnnotatesLocatorAndEnvelope(t *testing.T) {
	backend := &fakeBackend{}
	runner := &Runner{
		Cfg:     &config.Config{LeadName: "lead"},
		Backend: backend,
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
		Stdin:   strings.NewReader(""),
		Now:     func() time.Time { return time.Unix(123, 0) },
	}

	if err := runner.Run(context.Background(), []string{"ask", "--profile", "general", "hello"}); err != nil {
		t.Fatalf("run ask: %v", err)
	}
	if got := backend.locators[0].Metadata["agent_profile"]; got != config.AgentProfileGeneral {
		t.Fatalf("expected locator profile %q, got %q", config.AgentProfileGeneral, got)
	}
	if got := backend.submitted[0].Metadata["agent_profile"]; got != config.AgentProfileGeneral {
		t.Fatalf("expected envelope profile %q, got %q", config.AgentProfileGeneral, got)
	}
}

func TestRunnerTUIDefaultsToConfiguredTUIProfile(t *testing.T) {
	var got backend.SessionLocator
	runner := &Runner{
		Cfg: &config.Config{AgentDefaultProfiles: config.AgentDefaultProfilesConfig{TUI: config.AgentProfileCoding}},
		RunTUI: func(ctx context.Context, locator backend.SessionLocator) error {
			_ = ctx
			got = locator
			return nil
		},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}

	if err := runner.Run(context.Background(), nil); err != nil {
		t.Fatalf("run default tui: %v", err)
	}
	if got.Channel != "local" || got.Key != "default" {
		t.Fatalf("unexpected tui locator: %+v", got)
	}
	if profile := got.Metadata["agent_profile"]; profile != config.AgentProfileCoding {
		t.Fatalf("expected tui profile %q, got %q", config.AgentProfileCoding, profile)
	}
}

func TestRunnerRootHelpIsGroupedAndExampleDriven(t *testing.T) {
	stdout := &bytes.Buffer{}
	runner := &Runner{Stdout: stdout, Stderr: &bytes.Buffer{}}

	if err := runner.Run(context.Background(), []string{"--help"}); err != nil {
		t.Fatalf("run help: %v", err)
	}
	output := stdout.String()
	for _, want := range []string{
		"GoDex - local-first AI agent workspace",
		"Quick start:",
		"Commands:",
		"  Chat",
		"repl",
		"  Web & service",
		"acp-server",
		"  Config",
		"  Automation & channels",
		"Examples:",
		"godex service install --scope user --addr 127.0.0.1:8088",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected help to contain %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "godex [--config path] login openai|codex") {
		t.Fatalf("expected root help to avoid dense ungrouped command list:\n%s", output)
	}
	if strings.Contains(output, "    tui") || strings.Contains(output, "godex tui") {
		t.Fatalf("expected root help to omit removed tui subcommand:\n%s", output)
	}
}

func TestRunnerACPServerRejectsRemovedModelFlag(t *testing.T) {
	runner := &Runner{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	err := runner.Run(context.Background(), []string{"acp-server", "--model", "gpt-test"})
	if err == nil {
		t.Fatal("expected removed --model flag to fail")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("unexpected error %q", err.Error())
	}
}

func TestRunnerSubcommandHelp(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{args: []string{"providers", "--help"}, want: []string{"Usage:", "godex providers list", "godex providers test <id>"}},
		{args: []string{"weixin", "--help"}, want: []string{"Usage:", "godex weixin setup", "godex weixin logout"}},
		{args: []string{"eval", "--help"}, want: []string{"Usage:", "godex eval run", "godex eval list", "godex eval show"}},
		{args: []string{"service", "--help"}, want: []string{"Usage:", "godex service install", "Default scope is user"}},
		{args: []string{"gc", "--help"}, want: []string{"Usage:", "godex gc subagents", "--dry-run"}},
		{args: []string{"import", "--help"}, want: []string{"Usage:", "godex import claude", ".claude/agents/**/*.md"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			stdout := &bytes.Buffer{}
			runner := &Runner{Stdout: stdout, Stderr: &bytes.Buffer{}}
			if err := runner.Run(context.Background(), tt.args); err != nil {
				t.Fatalf("run %v: %v", tt.args, err)
			}
			output := stdout.String()
			for _, want := range tt.want {
				if !strings.Contains(output, want) {
					t.Fatalf("expected help to contain %q:\n%s", want, output)
				}
			}
		})
	}
}

func TestParseServiceInstallRuntimeOptions(t *testing.T) {
	root := t.TempDir()
	runner := &Runner{
		Cfg: &config.Config{
			HomeDir:      filepath.Join(root, "home"),
			ProjectDir:   filepath.Join(root, "workspace"),
			WorkspaceDir: filepath.Join(root, "workspace"),
		},
		Stderr: &bytes.Buffer{},
	}
	opts, err := runner.parseServiceOptions("install", []string{
		"--addr", "127.0.0.1:3800",
		"--gomemlimit", "180MiB",
		"--gogc", "40",
		"--gomaxprocs", "2",
		"--godebug", "madvdontneed=1,gctrace=1",
		"--watchdog-sec", "20",
		"--memory-high", "240M",
		"--memory-max", "300M",
	}, true)
	if err != nil {
		t.Fatalf("parse service options: %v", err)
	}
	if opts.GOMEMLIMIT != "180MiB" || opts.GOGC != "40" || opts.GOMAXPROCS != "2" || opts.GODEBUG != "madvdontneed=1,gctrace=1" {
		t.Fatalf("unexpected runtime options: %+v", opts)
	}
	if opts.WatchdogSec != 20 || opts.MemoryHigh != "240M" || opts.MemoryMax != "300M" {
		t.Fatalf("unexpected watchdog/memory options: %+v", opts)
	}
}

func TestRunnerImportClaudeDryRun(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "commands", "review.md"), `---
description: Review changes.
allowed-tools: Read
---
Review changes.
`)
	stdout := &bytes.Buffer{}
	runner := &Runner{
		Cfg:    &config.Config{StateDir: t.TempDir(), SkillsDir: filepath.Join(t.TempDir(), "skills")},
		Stdout: stdout,
		Stderr: &bytes.Buffer{},
	}
	if err := runner.Run(context.Background(), []string{"import", "claude", "--source", source, "--dry-run"}); err != nil {
		t.Fatalf("run import claude dry-run: %v", err)
	}
	output := stdout.String()
	for _, want := range []string{"Claude import dry run", "Commands: 1", "Run without --dry-run"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected dry-run output to contain %q:\n%s", want, output)
		}
	}
}

func TestRunnerGCDryRunReportsAllStorageCategories(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".tmp", "browser", "user-data", "Default", "Cache", "Cache_Data", "entry"), "cache")
	webFetchPath := filepath.Join(root, ".tmp", "web_fetch", "fetch-old.md")
	toolResultPath := filepath.Join(root, ".tool-results", "web-1", "tool.json")
	writeTestFile(t, webFetchPath, "fetch")
	writeTestFile(t, toolResultPath, "tool")
	writeTestFile(t, filepath.Join(root, ".sessions", "web-1", "checkpoints", "20260101T000000.000000000Z-a", "state.json"), "state")
	writeTestFile(t, filepath.Join(root, ".sessions", "web-1", "checkpoints", "20260101T000100.000000000Z-a", "state.json"), "state")
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(webFetchPath, old, old); err != nil {
		t.Fatalf("chtimes web fetch spill: %v", err)
	}
	if err := os.Chtimes(toolResultPath, old, old); err != nil {
		t.Fatalf("chtimes tool result: %v", err)
	}
	stdout := &bytes.Buffer{}
	runner := &Runner{
		Cfg: &config.Config{
			StateDir:    root,
			TempDir:     filepath.Join(root, ".tmp"),
			SessionsDir: filepath.Join(root, ".sessions"),
			Storage:     config.StorageConfig{ArtifactTTLHours: 1, SessionCheckpointTTLHours: 1, SessionCheckpointKeepLatest: 1},
		},
		Stdout: stdout,
		Stderr: &bytes.Buffer{},
		Now: func() time.Time {
			return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
		},
	}

	if err := runner.Run(context.Background(), []string{"gc", "--dry-run"}); err != nil {
		t.Fatalf("run gc dry-run: %v", err)
	}
	output := stdout.String()
	for _, want := range []string{"browser_cache", "web_fetch_spill", "tool_result", "session_checkpoint"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in gc output:\n%s", want, output)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".tmp", "browser", "user-data", "Default", "Cache", "Cache_Data", "entry")); err != nil {
		t.Fatalf("dry-run should not delete cache: %v", err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRunnerAskSupportsStdinAndExplicitSession(t *testing.T) {
	backend := &fakeBackend{}
	runner := &Runner{
		Cfg:     &config.Config{LeadName: "lead"},
		Backend: backend,
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
		Stdin:   strings.NewReader("from stdin\n"),
		Now:     time.Now,
	}

	if err := runner.Run(context.Background(), []string{"ask", "--stdin", "--session", "local:default"}); err != nil {
		t.Fatalf("run ask from stdin: %v", err)
	}
	if backend.locators[0].Channel != "local" || backend.locators[0].Key != "default" {
		t.Fatalf("unexpected explicit locator: %+v", backend.locators[0])
	}
	if backend.submitted[0].Text != "from stdin" {
		t.Fatalf("unexpected stdin prompt: %+v", backend.submitted[0])
	}
}

func TestRunnerCommandDefaultsToLocalDefaultSession(t *testing.T) {
	stdout := &bytes.Buffer{}
	backend := &fakeBackend{
		commandResult: commands.Result{Name: "tasks", Output: "task list"},
	}
	runner := &Runner{
		Cfg:     &config.Config{LeadName: "lead"},
		Backend: backend,
		Stdout:  stdout,
		Stderr:  &bytes.Buffer{},
		Stdin:   strings.NewReader(""),
		Now:     time.Now,
	}

	if err := runner.Run(context.Background(), []string{"command", "tasks"}); err != nil {
		t.Fatalf("run command: %v", err)
	}
	if backend.locators[0].Channel != "local" || backend.locators[0].Key != "default" {
		t.Fatalf("unexpected command locator: %+v", backend.locators[0])
	}
	if len(backend.executed) != 1 || backend.executed[0].Name != "tasks" {
		t.Fatalf("unexpected executed command: %+v", backend.executed)
	}
	if got := stdout.String(); got != "task list\n" {
		t.Fatalf("unexpected command output %q", got)
	}
}

func TestRunnerLoginOpenAIStoresSecretInHomeEnvAndProviderReference(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	manager, err := config.NewManager(config.Options{HomeDir: home, WorkspaceDir: workspace})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	stdout := &bytes.Buffer{}
	runner := &Runner{
		Cfg:           manager.Current(),
		ConfigManager: manager,
		Backend:       &fakeBackend{},
		Stdout:        stdout,
		Stderr:        &bytes.Buffer{},
		Stdin:         strings.NewReader("sk-test-login\n"),
		Now:           time.Now,
	}

	if err := runner.Run(context.Background(), []string{"login", "openai", "--mode", "platform-api-key"}); err != nil {
		t.Fatalf("login openai: %v", err)
	}
	envData, err := os.ReadFile(filepath.Join(home, ".env"))
	if err != nil {
		t.Fatalf("read home env: %v", err)
	}
	envInfo, err := os.Stat(filepath.Join(home, ".env"))
	if err != nil {
		t.Fatalf("stat home env: %v", err)
	}
	if envInfo.Mode().Perm() != 0600 {
		t.Fatalf("expected home env permissions 0600, got %#o", envInfo.Mode().Perm())
	}
	if !strings.Contains(string(envData), "OPENAI_API_KEY=sk-test-login") {
		t.Fatalf("expected key in home env, got %q", string(envData))
	}
	configData, err := os.ReadFile(filepath.Join(home, "godex.yaml"))
	if err != nil {
		t.Fatalf("read home config: %v", err)
	}
	if strings.Contains(string(configData), "sk-test-login") {
		t.Fatalf("secret leaked into yaml: %s", string(configData))
	}
	provider := manager.Current().LLMProviders["openai"]
	if provider.APIKeyEnv != "OPENAI_API_KEY" || provider.CredentialKind != "api-key" || provider.APIKey != "sk-test-login" {
		t.Fatalf("unexpected provider config: %#v", provider)
	}
	if !strings.Contains(stdout.String(), "OpenAI provider configured") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
}

func TestRunnerEvalRunListAndShow(t *testing.T) {
	dir := t.TempDir()
	suitePath := dir + "/godex.eval.yaml"
	if err := os.WriteFile(suitePath, []byte(`name: cli
cases:
  - id: one
    prompt: say ready
    expected:
      required_substrings: ["ready"]
`), 0644); err != nil {
		t.Fatalf("write suite: %v", err)
	}
	outDir := dir + "/runs"
	stdout := &bytes.Buffer{}
	runner := &Runner{
		Cfg:     &config.Config{LeadName: "lead"},
		Backend: &fakeBackend{},
		Stdout:  stdout,
		Stderr:  &bytes.Buffer{},
		Stdin:   strings.NewReader(""),
		Now:     time.Now,
		Eval: &evalharness.Service{
			Backend:  fakeEvalCommandBackend{},
			LeadName: "lead",
			Now: func() time.Time {
				return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
			},
		},
	}
	if err := runner.Run(context.Background(), []string{"eval", "run", "--suite", suitePath, "--out", outDir}); err != nil {
		t.Fatalf("eval run: %v", err)
	}
	if !strings.Contains(stdout.String(), "1/1 passed") {
		t.Fatalf("unexpected eval run output: %q", stdout.String())
	}
	stdout.Reset()
	if err := runner.Run(context.Background(), []string{"eval", "list", "--dir", outDir}); err != nil {
		t.Fatalf("eval list: %v", err)
	}
	fields := strings.Fields(stdout.String())
	if len(fields) == 0 {
		t.Fatalf("expected eval list output")
	}
	stdout.Reset()
	if err := runner.Run(context.Background(), []string{"eval", "show", "--run", outDir + "/" + fields[0]}); err != nil {
		t.Fatalf("eval show: %v", err)
	}
	if !strings.Contains(stdout.String(), `"passed": true`) {
		t.Fatalf("unexpected eval show output: %q", stdout.String())
	}
}

func TestRunnerServeUsesInjectedServeFunction(t *testing.T) {
	stdout := &bytes.Buffer{}
	var servedAddr string
	runner := &Runner{
		Cfg:     &config.Config{},
		Backend: &fakeBackend{},
		Stdout:  stdout,
		Stderr:  &bytes.Buffer{},
		Stdin:   strings.NewReader(""),
		Now:     time.Now,
		Serve: func(ctx context.Context, addr string) error {
			_ = ctx
			servedAddr = addr
			return nil
		},
	}

	if err := runner.Run(context.Background(), []string{"serve", "--addr", ":9090"}); err != nil {
		t.Fatalf("run serve: %v", err)
	}
	if servedAddr != ":9090" {
		t.Fatalf("unexpected served addr %q", servedAddr)
	}
	if !strings.Contains(stdout.String(), "http://:9090") {
		t.Fatalf("expected serve output to mention address, got %q", stdout.String())
	}
}

func TestRunnerDefaultsToTUI(t *testing.T) {
	var got backend.SessionLocator
	replCalled := false
	runner := &Runner{
		Cfg:     &config.Config{},
		Backend: &fakeBackend{},
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
		Stdin:   strings.NewReader(""),
		Now:     time.Now,
		RunREPL: func(ctx context.Context) error {
			_ = ctx
			replCalled = true
			return nil
		},
		RunTUI: func(ctx context.Context, locator backend.SessionLocator) error {
			_ = ctx
			got = locator
			return nil
		},
	}

	if err := runner.Run(context.Background(), nil); err != nil {
		t.Fatalf("run default tui: %v", err)
	}
	if replCalled {
		t.Fatal("expected default command not to call repl")
	}
	if got.Channel != "local" || got.Key != "default" {
		t.Fatalf("unexpected default tui locator: %+v", got)
	}
}

func TestRunnerExplicitREPLUsesReadline(t *testing.T) {
	called := false
	runner := &Runner{
		Cfg:     &config.Config{},
		Backend: &fakeBackend{},
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
		Stdin:   strings.NewReader(""),
		Now:     time.Now,
		RunREPL: func(ctx context.Context) error {
			_ = ctx
			called = true
			return nil
		},
	}

	if err := runner.Run(context.Background(), []string{"repl"}); err != nil {
		t.Fatalf("run repl: %v", err)
	}
	if !called {
		t.Fatal("expected repl runner to be called")
	}
}

func TestRunnerTUISubcommandRemoved(t *testing.T) {
	runner := &Runner{
		Cfg:    &config.Config{},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Now:    time.Now,
		RunTUI: func(ctx context.Context, locator backend.SessionLocator) error {
			t.Fatalf("tui subcommand should not call RunTUI")
			return nil
		},
	}
	err := runner.Run(context.Background(), []string{"tui"})
	if err == nil || !strings.Contains(err.Error(), `unknown subcommand "tui"`) {
		t.Fatalf("expected unknown tui subcommand, got %v", err)
	}
}

func TestRunnerWeixinSetupUsesInjectedCallback(t *testing.T) {
	called := false
	runner := &Runner{
		Cfg:         &config.Config{},
		Backend:     &fakeBackend{},
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
		Stdin:       strings.NewReader(""),
		Now:         time.Now,
		WeixinSetup: func(ctx context.Context) error { _ = ctx; called = true; return nil },
	}

	if err := runner.Run(context.Background(), []string{"weixin", "setup"}); err != nil {
		t.Fatalf("run weixin setup: %v", err)
	}
	if !called {
		t.Fatal("expected weixin setup callback to run")
	}
}

func TestRunnerWeixinLogoutUsesInjectedCallback(t *testing.T) {
	called := false
	runner := &Runner{
		Cfg:          &config.Config{},
		Backend:      &fakeBackend{},
		Stdout:       &bytes.Buffer{},
		Stderr:       &bytes.Buffer{},
		Stdin:        strings.NewReader(""),
		Now:          time.Now,
		WeixinLogout: func(ctx context.Context) error { _ = ctx; called = true; return nil },
	}

	if err := runner.Run(context.Background(), []string{"weixin", "logout"}); err != nil {
		t.Fatalf("run weixin logout: %v", err)
	}
	if !called {
		t.Fatal("expected weixin logout callback to run")
	}
}
