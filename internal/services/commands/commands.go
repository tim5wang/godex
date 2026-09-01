package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/services/localbash"
	"github.com/tim5wang/godex/internal/tools"
)

// ErrUnknownCommand indicates the input does not match a registered slash command.
var ErrUnknownCommand = errors.New("unknown command")

// Command is one normalized slash command invocation.
type Command struct {
	Name     string            `json:"name"`
	Args     []string          `json:"args,omitempty"`
	Raw      string            `json:"raw,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Result is the structured output of a command handler.
type Result struct {
	Name             string                  `json:"name"`
	Output           string                  `json:"output,omitempty"`
	ArtifactPath     string                  `json:"artifact_path,omitempty"`
	RefreshSnapshot  bool                    `json:"refresh_snapshot,omitempty"`
	Dispatch         *PackageCommandDispatch `json:"dispatch,omitempty"`
	DispatchedTurnID string                  `json:"dispatched_turn_id,omitempty"`
	DispatchedJobID  string                  `json:"dispatched_job_id,omitempty"`
	DispatchStatus   string                  `json:"dispatch_status,omitempty"`
	DispatchError    string                  `json:"dispatch_error,omitempty"`
	Diagnostics      []string                `json:"diagnostics,omitempty"`
}

// CommandMetadata is the shared discoverable slash-command description used by
// CLI/TUI/Web help text and ACP command discovery.
type CommandMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputHint   string `json:"input_hint,omitempty"`
}

// AvailableMetadata returns the stable list of built-in slash commands.
func AvailableMetadata() []CommandMetadata {
	items := []CommandMetadata{
		{Name: "bash", Description: "run a shell command in the workspace without an LLM turn", InputHint: "<shell command>"},
		{Name: "sh", Description: "alias for /bash", InputHint: "<shell command>"},
		{Name: "compact", Description: "compact the current session conversation"},
		{Name: "clear", Description: "clear current prompt state and reset transient tools"},
		{Name: "tasks", Description: "show task board items"},
		{Name: "team", Description: "show teammates and their current status"},
		{Name: "inbox", Description: "read the lead inbox"},
		{Name: "todos", Description: "show or clear the todo list", InputHint: "list|clear"},
		{Name: "insights", Description: "generate a workspace insights report"},
		{Name: "doctor", Description: "diagnose the active Godex configuration"},
		{Name: "channels", Description: "show runtime channel status"},
		{Name: "skills", Description: "inspect, load, expand, or unload skills for this session", InputHint: "list|active|get|load|expand|unload ..."},
		{Name: "packages", Description: "inspect installed packages, package commands, roles, and prompts", InputHint: "list|commands|roles|prompts ..."},
		{Name: "memory", Description: "browse durable memory and review memory candidates", InputHint: "list|search|candidates|accept|dismiss ..."},
		{Name: "note", Description: "create, list, search, append, or update markdown notes", InputHint: "create <title> [--tags a,b] -- <markdown>"},
		{Name: "memory-digest", Description: "analyze session signals and add durable-memory candidates"},
		{Name: "memory-log", Description: "show durable memory audit history", InputHint: "[limit]"},
		{Name: "memory-restore", Description: "restore a memory audit snapshot", InputHint: "<audit-id> [before|after]"},
		{Name: "ledger", Description: "show the current long-task project ledger"},
		{Name: "model", Description: "list models, switch this session, or update the default model", InputHint: "list|use <profile-id>|default <profile-or-model>|get"},
		{Name: "approve", Description: "approve the current pending permission request, or inspect approval blockers", InputHint: "[status|list|request-id] [once|task|session|pattern|count:N|timebox:10m]"},
		{Name: "deny", Description: "deny a pending permission request for this session", InputHint: "[request-id] [reason...]"},
		{Name: "session", Description: "inspect or create sessions", InputHint: "current|list [channel]|new [key|channel:key]|context|tokens|auth ..."},
		{Name: "history", Description: "inspect or search session history", InputHint: "show|tail [count]|search <query> [scope=...] [limit=N] [role=...]"},
		{Name: "cron", Description: "inspect, run, or toggle cron jobs"},
		{Name: "heartbeat", Description: "inspect, test, or toggle heartbeat"},
		{Name: "new", Description: "create a new empty session for the current workspace"},
		{Name: "resume", Description: "list and resume a previous session from this workspace", InputHint: "[session-id|session-name]"},
		{Name: "help", Description: "show this help message"},
	}
	return append([]CommandMetadata(nil), items...)
}

// PackageCommandDispatch is a safe execution request produced by a package
// command declaration. Backend owns the actual execution so it can reuse session
// queues, subagent event sinks, and permission/runtime state.
type PackageCommandDispatch struct {
	Mode               string   `json:"mode"`
	Prompt             string   `json:"prompt"`
	PackageName        string   `json:"package_name"`
	Namespace          string   `json:"namespace,omitempty"`
	CommandName        string   `json:"command_name"`
	Invocation         string   `json:"invocation,omitempty"`
	Args               []string `json:"args,omitempty"`
	AgentType          string   `json:"agent_type,omitempty"`
	WriteScope         []string `json:"write_scope,omitempty"`
	Roles              []string `json:"roles,omitempty"`
	Permissions        []string `json:"permissions,omitempty"`
	Capabilities       []string `json:"capabilities,omitempty"`
	ToolPolicy         []string `json:"tool_policy,omitempty"`
	RecommendedBundles []string `json:"recommended_bundles,omitempty"`
}

// SessionContext carries the current runtime session identity for commands that
// need to inspect or create sessions.
type SessionContext struct {
	SessionID string            `json:"session_id,omitempty"`
	Channel   string            `json:"channel,omitempty"`
	Key       string            `json:"key,omitempty"`
	UserID    string            `json:"user_id,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type sessionContextKey struct{}

// WithSessionContext attaches the current runtime session identity to ctx.
func WithSessionContext(ctx context.Context, session SessionContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	cloned := session
	if len(session.Metadata) > 0 {
		cloned.Metadata = make(map[string]string, len(session.Metadata))
		for key, value := range session.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return context.WithValue(ctx, sessionContextKey{}, cloned)
}

// CurrentSessionContext returns the command runtime session context when one is available.
func CurrentSessionContext(ctx context.Context) (SessionContext, bool) {
	if ctx == nil {
		return SessionContext{}, false
	}
	value, ok := ctx.Value(sessionContextKey{}).(SessionContext)
	if !ok {
		return SessionContext{}, false
	}
	return value, true
}

// Service executes slash commands against one session-scoped agent.
type Service struct {
	mu            sync.RWMutex
	cfg           *config.Config
	analyze       func(insights.Input) (*insights.Report, error)
	doctor        func() config.DoctorReport
	channels      func() string
	cron          func(context.Context, Command) (Result, error)
	heartbeat     func(context.Context, Command) (Result, error)
	model         func(context.Context, Command) (Result, error)
	session       func(context.Context, *agent.Agent, Command) (Result, error)
	newSession    func(context.Context, *agent.Agent, Command) (Result, error)
	resumeSession func(context.Context, *agent.Agent, Command) (Result, error)
	clear         func(context.Context, *agent.Agent, Command) (Result, error)
	approve       func(context.Context, *agent.Agent, Command) (Result, error)
	deny          func(context.Context, *agent.Agent, Command) (Result, error)
}

type insightsSnapshot struct {
	Messages     []protocol.Message
	ActiveSkills []string
	ToolCatalog  tools.ToolCatalog
	Todos        []todo.Item
	Tasks        []*task.FileTask
}

// NewService creates the shared slash-command service.
func NewService(cfg *config.Config) *Service {
	service := &Service{cfg: cfg}
	service.analyze = func(input insights.Input) (*insights.Report, error) {
		return insights.NewAnalyzer(cfg.TranscriptsDir, cfg.TempDir, cfg.MemoryDir).Analyze(input)
	}
	return service
}

// SetConfig updates the config snapshot used by command handlers.
func (s *Service) SetConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.analyze = func(input insights.Input) (*insights.Report, error) {
		return insights.NewAnalyzer(cfg.TranscriptsDir, cfg.TempDir, cfg.MemoryDir).Analyze(input)
	}
}

// SetAnalyzer overrides the insights analyzer implementation, mainly for tests.
func (s *Service) SetAnalyzer(analyze func(insights.Input) (*insights.Report, error)) {
	if analyze == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.analyze = analyze
}

// SetDoctor overrides config doctor generation for runtime use.
func (s *Service) SetDoctor(doctor func() config.DoctorReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doctor = doctor
}

// SetChannels installs a runtime channel status renderer for diagnostics.
func (s *Service) SetChannels(channels func() string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels = channels
}

// SetCron installs a runtime cron command handler.
func (s *Service) SetCron(handler func(context.Context, Command) (Result, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cron = handler
}

// SetHeartbeat installs a runtime heartbeat command handler.
func (s *Service) SetHeartbeat(handler func(context.Context, Command) (Result, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeat = handler
}

// SetModel installs a runtime model command handler.
func (s *Service) SetModel(handler func(context.Context, Command) (Result, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = handler
}

// SetSession installs a runtime session command handler.
func (s *Service) SetSession(handler func(context.Context, *agent.Agent, Command) (Result, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = handler
}

// SetNewSession installs a runtime /new command handler.
func (s *Service) SetNewSession(handler func(context.Context, *agent.Agent, Command) (Result, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.newSession = handler
}

// SetResumeSession installs a runtime /resume command handler.
func (s *Service) SetResumeSession(handler func(context.Context, *agent.Agent, Command) (Result, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumeSession = handler
}

// SetClear installs a runtime clear command handler.
func (s *Service) SetClear(handler func(context.Context, *agent.Agent, Command) (Result, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clear = handler
}

// SetApprove installs a runtime approve command handler.
func (s *Service) SetApprove(handler func(context.Context, *agent.Agent, Command) (Result, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approve = handler
}

// SetDeny installs a runtime deny command handler.
func (s *Service) SetDeny(handler func(context.Context, *agent.Agent, Command) (Result, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deny = handler
}

// Parse recognizes a slash command line.
func Parse(input string) (Command, bool) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return Command{}, false
	}
	fields := strings.Fields(strings.TrimPrefix(input, "/"))
	if len(fields) == 0 {
		return Command{}, false
	}
	return Command{
		Name: strings.ToLower(fields[0]),
		Args: append([]string{}, fields[1:]...),
		Raw:  input,
	}, true
}

// HelpText returns the discoverable command list.
func (s *Service) HelpText() string {
	lines := []string{"Available commands:"}
	for _, item := range AvailableMetadata() {
		usage := "/" + item.Name
		if hint := strings.TrimSpace(item.InputHint); hint != "" {
			usage += " " + hint
		}
		lines = append(lines, usage+" - "+item.Description)
	}
	return strings.Join(lines, "\n")
}

// Execute runs one normalized command.
func (s *Service) executeLocalBash(ctx context.Context, cmd Command) (Result, error) {
	shellCommand, ok := localbash.ParseCommand(cmd.Raw)
	if !ok {
		return Result{}, fmt.Errorf("usage: /%s <shell command>", cmd.Name)
	}
	workspaceDir := s.cfg.WorkspaceDir
	if sessionCtx, ok := CurrentSessionContext(ctx); ok {
		if dir := strings.TrimSpace(sessionCtx.Metadata["project_dir"]); dir != "" {
			workspaceDir = dir
		}
	}
	// Build execution config from the global tool execution settings so
	// /sh and /bash respect mode=ssh / mode=docker just like the agent.
	execution := tooling.ExecutionConfig{
		Mode:               s.cfg.Tools.Execution.Mode,
		DockerImage:        s.cfg.Tools.Execution.DockerImage,
		DockerNetwork:      s.cfg.Tools.Execution.DockerNetwork,
		SSHTarget:          s.cfg.Tools.Execution.SSHTarget,
		SSHWorkspace:       s.cfg.Tools.Execution.SSHWorkspace,
		SSHOptions:         append([]string{}, s.cfg.Tools.Execution.SSHOptions...),
		ShellAllowPatterns: append([]string{}, s.cfg.Tools.Execution.ShellAllowPatterns...),
		ShellDenyPatterns:  append([]string{}, s.cfg.Tools.Execution.ShellDenyPatterns...),
	}
	executor := tooling.NewWorkspaceExecutorWithTempDirAndExecution(workspaceDir, "", execution)
	result := localbash.CollectWithExecutor(ctx, executor, shellCommand)
	// Always return output (incl. stderr). Non-zero exit codes are
	// not fatal — the caller sees the same messages a terminal user would.
	if result.Err != nil && result.Output == "" {
		return Result{}, result.Err
	}
	return Result{
		Name:   cmd.Name,
		Output: result.Output,
	}, nil
}

func (s *Service) executeChannels() (Result, error) {
	s.mu.RLock()
	channels := s.channels
	s.mu.RUnlock()
	if channels == nil {
		return Result{Name: "channels", Output: "Channel runtime is unavailable in this process."}, nil
	}
	return Result{Name: "channels", Output: channels()}, nil
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func formatMemoryTime(ts time.Time) string {
	if ts.IsZero() {
		return "(unknown)"
	}
	return ts.UTC().Format("2006-01-02 15:04Z")
}

func collectInsightsSnapshot(a *agent.Agent) insightsSnapshot {
	return insightsSnapshot{
		Messages:     a.GetMessages(),
		ActiveSkills: a.ActiveSkillNames(),
		ToolCatalog:  a.ToolCatalog(),
		Todos:        a.TodoMgr().List(),
		Tasks:        a.TaskMgr().List(),
	}
}

func buildInsightsInput(snapshot insightsSnapshot) insights.Input {
	input := insights.Input{
		CurrentMessages: make([]insights.Message, 0, len(snapshot.Messages)),
		ActiveSkills:    append([]string{}, snapshot.ActiveSkills...),
		ToolCatalog: insights.ToolCatalog{
			ActiveBundles: append([]string{}, snapshot.ToolCatalog.ActiveBundles...),
		},
		Todos: make([]insights.WorkItem, 0, len(snapshot.Todos)),
		Tasks: make([]insights.WorkItem, 0, len(snapshot.Tasks)),
	}

	for _, msg := range snapshot.Messages {
		textParts := make([]string, 0, len(msg.Content))
		toolNames := make([]string, 0, len(msg.Content))
		for _, block := range msg.Content {
			switch string(block.Type) {
			case "text":
				textParts = append(textParts, block.Text)
			case "tool_use":
				if block.Name != "" {
					toolNames = append(toolNames, block.Name)
				}
			}
		}
		input.CurrentMessages = append(input.CurrentMessages, insights.Message{
			Text:      strings.Join(textParts, ""),
			ToolNames: toolNames,
		})
	}

	for _, item := range snapshot.Todos {
		input.Todos = append(input.Todos, insights.WorkItem{Status: string(item.Status)})
	}
	for _, item := range snapshot.Tasks {
		input.Tasks = append(input.Tasks, insights.WorkItem{Status: string(item.Status)})
	}
	return input
}
