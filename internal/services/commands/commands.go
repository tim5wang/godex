package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/notes"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/core/teammate"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/platform/stringutil"
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
	mu        sync.RWMutex
	cfg       *config.Config
	analyze   func(insights.Input) (*insights.Report, error)
	doctor    func() config.DoctorReport
	channels  func() string
	cron      func(context.Context, Command) (Result, error)
	heartbeat func(context.Context, Command) (Result, error)
	model     func(context.Context, Command) (Result, error)
	session   func(context.Context, *agent.Agent, Command) (Result, error)
	newSession    func(context.Context, *agent.Agent, Command) (Result, error)
	resumeSession func(context.Context, *agent.Agent, Command) (Result, error)
	clear     func(context.Context, *agent.Agent, Command) (Result, error)
	approve   func(context.Context, *agent.Agent, Command) (Result, error)
	deny      func(context.Context, *agent.Agent, Command) (Result, error)
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
func (s *Service) Execute(ctx context.Context, a *agent.Agent, cmd Command) (Result, error) {
	_ = ctx
	if a == nil {
		return Result{}, fmt.Errorf("missing agent")
	}
	if cmd.Name == "" {
		return Result{}, fmt.Errorf("%w: empty command", ErrUnknownCommand)
	}
	switch cmd.Name {
	case "bash", "sh":
		return s.executeLocalBash(ctx, cmd)
	case "compact":
		mode := a.DefaultCompactionMode()
		for _, arg := range cmd.Args {
			switch strings.TrimSpace(arg) {
			case "--model", "--deep":
				mode = "model"
			case "--hybrid":
				mode = "hybrid"
			default:
				return Result{}, fmt.Errorf("usage: /compact [--model|--deep|--hybrid]")
			}
		}
		output, err := a.CompactConversationWithMode(mode)
		return Result{Name: cmd.Name, Output: output, RefreshSnapshot: true}, err
	case "tasks":
		if len(cmd.Args) > 0 {
			return Result{}, fmt.Errorf("command /%s does not accept arguments", cmd.Name)
		}
		return Result{Name: cmd.Name, Output: fmt.Sprint(a.TaskMgr().List())}, nil
	case "team":
		if len(cmd.Args) > 0 {
			return Result{}, fmt.Errorf("command /%s does not accept arguments", cmd.Name)
		}
		return Result{Name: cmd.Name, Output: renderTeam(a.TeamMgr().List())}, nil
	case "inbox":
		if len(cmd.Args) > 0 {
			return Result{}, fmt.Errorf("command /%s does not accept arguments", cmd.Name)
		}
		return Result{Name: cmd.Name, Output: fmt.Sprint(a.MsgBus().ReadInbox(s.cfg.LeadName))}, nil
	case "todos":
		return s.executeTodos(a, cmd)
	case "insights":
		if len(cmd.Args) > 0 {
			return Result{}, fmt.Errorf("command /%s does not accept arguments", cmd.Name)
		}
		return s.executeInsights(a)
	case "doctor":
		if len(cmd.Args) > 0 {
			return Result{}, fmt.Errorf("command /%s does not accept arguments", cmd.Name)
		}
		return s.executeDoctor()
	case "channels":
		if len(cmd.Args) > 0 {
			return Result{}, fmt.Errorf("command /%s does not accept arguments", cmd.Name)
		}
		return s.executeChannels()
	case "skills":
		return s.executeSkills(a, cmd)
	case "packages":
		return s.executePackages(cmd)
	case "memory":
		return s.executeMemory(a, cmd)
	case "note":
		return s.executeNote(ctx, cmd)
	case "memory-digest":
		if len(cmd.Args) > 0 {
			return Result{}, fmt.Errorf("command /%s does not accept arguments", cmd.Name)
		}
		return s.executeMemoryDigest(a)
	case "memory-log":
		return s.executeMemoryLog(a, cmd)
	case "memory-restore":
		return s.executeMemoryRestore(a, cmd)
	case "model":
		return s.executeModel(a, ctx, cmd)
	case "clear":
		return s.executeClear(a, ctx, cmd)
	case "approve":
		return s.executeApprove(a, ctx, cmd)
	case "deny":
		return s.executeDeny(a, ctx, cmd)
	case "session":
		return s.executeSession(a, ctx, cmd)
	case "new":
		return s.executeNewSession(a, ctx, cmd)
	case "resume":
		return s.executeResumeSession(a, ctx, cmd)
	case "history":
		return s.executeHistory(a, ctx, cmd)
	case "cron":
		return s.executeCron(ctx, cmd)
	case "heartbeat":
		return s.executeHeartbeat(ctx, cmd)
	case "help":
		if len(cmd.Args) > 0 {
			return Result{}, fmt.Errorf("command /%s does not accept arguments", cmd.Name)
		}
		return Result{Name: cmd.Name, Output: s.HelpText()}, nil
	default:
		if result, ok, err := s.executePackageCommand(cmd); ok || err != nil {
			return result, err
		}
		return Result{}, fmt.Errorf("%w: /%s", ErrUnknownCommand, cmd.Name)
	}
}

// executeLocalBash executes /bash and /sh commands via the configured
// WorkspaceExecutor (local, SSH, or Docker).
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

func (s *Service) executeSkills(a *agent.Agent, cmd Command) (Result, error) {
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"list"}
	}
	switch strings.ToLower(strings.TrimSpace(cmd.Args[0])) {
	case "list":
		items, err := a.ListSkills()
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "skills", Output: renderSkillCatalog(items)}, nil
	case "sources":
		query := ""
		if len(cmd.Args) > 1 {
			query = strings.Join(cmd.Args[1:], " ")
		}
		var (
			items []tools.SkillSourceEntry
			err   error
		)
		if strings.TrimSpace(query) != "" {
			items, err = a.SearchSkillSources(query)
		} else {
			items, err = a.ListSkillSources()
		}
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "skills", Output: renderSkillSources(items)}, nil
	case "active":
		items, err := a.ActiveSkills()
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "skills", Output: renderActiveSkills(items)}, nil
	case "get":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /skills get <name>")
		}
		entry, err := a.GetSkill(cmd.Args[1])
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "skills", Output: renderSkillEntry(entry)}, nil
	case "install":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /skills install <source> [name]")
		}
		source := strings.TrimSpace(cmd.Args[1])
		name := ""
		if len(cmd.Args) > 2 {
			name = strings.TrimSpace(cmd.Args[2])
		}
		result, err := a.InstallSkill(source, name)
		return Result{Name: "skills", Output: renderSkillInstall(result)}, err
	case "load":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /skills load <name>")
		}
		result, err := a.ActivateSkill(cmd.Args[1])
		return Result{Name: "skills", Output: renderSkillActivation(result), RefreshSnapshot: err == nil}, err
	case "expand":
		if len(cmd.Args) < 3 {
			return Result{}, fmt.Errorf("usage: /skills expand <name> <section...>")
		}
		sections := make([]string, 0, len(cmd.Args)-2)
		for _, arg := range cmd.Args[2:] {
			for _, part := range strings.Split(arg, ",") {
				part = strings.TrimSpace(part)
				if part != "" {
					sections = append(sections, part)
				}
			}
		}
		if len(sections) == 0 {
			return Result{}, fmt.Errorf("usage: /skills expand <name> <section...>")
		}
		result, err := a.ExpandSkill(cmd.Args[1], sections)
		return Result{Name: "skills", Output: renderSkillExpansion(result), RefreshSnapshot: err == nil}, err
	case "unload":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /skills unload <name>")
		}
		result, err := a.UnloadSkill(cmd.Args[1])
		return Result{Name: "skills", Output: renderSkillActivation(result), RefreshSnapshot: err == nil}, err
	default:
		return Result{}, fmt.Errorf("unknown /skills subcommand %q", cmd.Args[0])
	}
}

func (s *Service) executePackages(cmd Command) (Result, error) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	if cfg == nil {
		return Result{Name: "packages", Output: "Package runtime is unavailable in this process."}, nil
	}
	manager := pkgregistry.NewManager(cfg.StateDir, cfg.SkillsDir)
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"list"}
	}
	switch strings.ToLower(strings.TrimSpace(cmd.Args[0])) {
	case "list":
		items, err := manager.List()
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "packages", Output: renderPackageList(items)}, nil
	case "commands":
		items, err := manager.ListCommands(false)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "packages", Output: renderPackageCommands(items)}, nil
	case "roles":
		items, err := manager.ListRoles(false)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "packages", Output: renderPackageRoles(items)}, nil
	case "prompts":
		items, err := manager.ListPrompts(false)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "packages", Output: renderPackagePrompts(items)}, nil
	default:
		return Result{}, fmt.Errorf("unknown /packages subcommand %q", cmd.Args[0])
	}
}

func (s *Service) executePackageCommand(cmd Command) (Result, bool, error) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	if cfg == nil {
		return Result{}, false, nil
	}
	manager := pkgregistry.NewManager(cfg.StateDir, cfg.SkillsDir)
	items, err := manager.ListCommands(true)
	if err != nil {
		return Result{}, false, err
	}
	match, args, ok, err := matchPackageCommand(cmd, items)
	if err != nil {
		return Result{}, true, err
	}
	if !ok {
		return Result{}, false, nil
	}
	prompt := renderPackageCommandPrompt(match, args)
	invocation := packageCommandInvocation(match)
	mode := strings.TrimSpace(match.Mode)
	if mode == "" {
		mode = "prompt_only"
	}
	result := Result{
		Name:           "package_command",
		Output:         fmt.Sprintf("Package command %s resolved in %s mode.", invocation, mode),
		DispatchStatus: "resolved",
	}
	if mode == "prompt_only" {
		result.Output = prompt
		return result, true, nil
	}
	if mode != "agent_turn" && mode != "subagent_job" {
		err := fmt.Errorf("package command %s uses unsupported dispatch mode %q", invocation, mode)
		result.DispatchStatus = "failed"
		result.DispatchError = err.Error()
		result.Diagnostics = append(result.Diagnostics, err.Error())
		return result, true, err
	}
	roleDiagnostics := packageCommandRoleDiagnostics(manager, match)
	if len(roleDiagnostics) > 0 {
		err := errors.New(strings.Join(roleDiagnostics, "; "))
		result.DispatchStatus = "failed"
		result.DispatchError = err.Error()
		result.Diagnostics = append(result.Diagnostics, roleDiagnostics...)
		return result, true, err
	}
	agentType := "Explore"
	if len(match.Roles) > 0 {
		agentType = match.Roles[0]
	}
	result.Dispatch = &PackageCommandDispatch{
		Mode:               mode,
		Prompt:             prompt,
		PackageName:        match.PackageName,
		Namespace:          match.Namespace,
		CommandName:        match.Name,
		Invocation:         invocation,
		Args:               append([]string{}, args...),
		AgentType:          agentType,
		WriteScope:         append([]string{}, match.WriteScope...),
		Roles:              append([]string{}, match.Roles...),
		Permissions:        append([]string{}, match.Permissions...),
		Capabilities:       append([]string{}, match.Capabilities...),
		ToolPolicy:         append([]string{}, match.ToolPolicy...),
		RecommendedBundles: append([]string{}, match.RecommendedBundles...),
	}
	result.DispatchStatus = "pending_dispatch"
	return result, true, nil
}

func packageCommandRoleDiagnostics(manager *pkgregistry.Manager, command pkgregistry.Command) []string {
	if len(command.Roles) == 0 || manager == nil {
		return nil
	}
	roles, err := manager.ListRoles(false)
	if err != nil {
		return []string{"list package roles: " + err.Error()}
	}
	known := map[string]struct{}{}
	for _, role := range roles {
		known[role.ID] = struct{}{}
		if role.Name != "" {
			known[role.Name] = struct{}{}
		}
	}
	var diagnostics []string
	for _, role := range command.Roles {
		if _, ok := known[role]; !ok {
			diagnostics = append(diagnostics, "package command role not found: "+role)
		}
	}
	return diagnostics
}

func matchPackageCommand(cmd Command, items []pkgregistry.Command) (pkgregistry.Command, []string, bool, error) {
	name := strings.ToLower(strings.TrimSpace(cmd.Name))
	if name == "" {
		return pkgregistry.Command{}, nil, false, nil
	}
	type candidate struct {
		item pkgregistry.Command
		args []string
	}
	var matches []candidate
	for _, item := range items {
		namespace := strings.ToLower(strings.TrimSpace(item.Namespace))
		commandName := strings.ToLower(strings.TrimSpace(item.Name))
		if namespace != "" && name == namespace && len(cmd.Args) > 0 && strings.EqualFold(cmd.Args[0], commandName) {
			matches = append(matches, candidate{item: item, args: append([]string{}, cmd.Args[1:]...)})
			continue
		}
		if containsFold(item.Aliases, cmd.Name) {
			matches = append(matches, candidate{item: item, args: append([]string{}, cmd.Args...)})
		}
	}
	if len(matches) == 0 {
		return pkgregistry.Command{}, nil, false, nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, packageCommandInvocation(match.item))
		}
		return pkgregistry.Command{}, nil, true, fmt.Errorf("ambiguous package command /%s: %s", cmd.Name, strings.Join(names, ", "))
	}
	return matches[0].item, matches[0].args, true, nil
}

func containsFold(values []string, want string) bool {
	want = strings.TrimPrefix(strings.TrimSpace(want), "/")
	for _, value := range values {
		value = strings.TrimPrefix(strings.TrimSpace(value), "/")
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

func renderPackageCommandPrompt(item pkgregistry.Command, args []string) string {
	prompt := strings.TrimSpace(item.Prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(item.Description)
	}
	if prompt == "" {
		prompt = "Run package command " + packageCommandInvocation(item) + "."
	}
	rawArgs := strings.Join(args, " ")
	replacements := map[string]string{
		"{{args}}":                rawArgs,
		"{{raw_args}}":            rawArgs,
		"{{command}}":             item.Name,
		"{{namespace}}":           item.Namespace,
		"{{package}}":             item.PackageName,
		"{{roles}}":               strings.Join(item.Roles, ", "),
		"{{recommended_bundles}}": strings.Join(item.RecommendedBundles, ", "),
	}
	rendered := prompt
	for old, replacement := range replacements {
		rendered = strings.ReplaceAll(rendered, old, replacement)
	}
	if rawArgs != "" && !strings.Contains(prompt, "{{args}}") && !strings.Contains(prompt, "{{raw_args}}") {
		rendered += "\n\nUser arguments:\n" + rawArgs
	}
	header := []string{
		"Package command: " + packageCommandInvocation(item),
	}
	if item.Description != "" {
		header = append(header, "Description: "+item.Description)
	}
	if len(item.Roles) > 0 {
		header = append(header, "Roles: "+strings.Join(item.Roles, ", "))
	}
	return strings.Join(header, "\n") + "\n\n" + strings.TrimSpace(rendered)
}

func packageCommandInvocation(item pkgregistry.Command) string {
	namespace := strings.TrimSpace(item.Namespace)
	if namespace == "" {
		namespace = strings.TrimSpace(item.PackageName)
	}
	if namespace == "" {
		return "/" + strings.TrimSpace(item.Name)
	}
	return "/" + namespace + " " + strings.TrimSpace(item.Name)
}

func (s *Service) executeMemory(a *agent.Agent, cmd Command) (Result, error) {
	mgr := a.MemoryMgr()
	if mgr == nil {
		return Result{Name: "memory", Output: "Memory runtime is unavailable in this process."}, nil
	}
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"list"}
	}
	switch strings.ToLower(strings.TrimSpace(cmd.Args[0])) {
	case "list":
		items, err := mgr.List()
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "memory", Output: renderMemoryList(items)}, nil
	case "get":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /memory get <id-or-title>")
		}
		record, err := mgr.Get(strings.Join(cmd.Args[1:], " "))
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "memory", Output: renderStoredMemory(*record)}, nil
	case "search":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /memory search <query>")
		}
		items, err := mgr.Search(memory.SearchOptions{Query: strings.Join(cmd.Args[1:], " ")})
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "memory", Output: renderMemorySearch(items)}, nil
	case "candidates":
		items, err := mgr.ListCandidates()
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "memory", Output: renderMemoryCandidates(items)}, nil
	case "accept":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /memory accept <fingerprint>")
		}
		entry, err := mgr.AcceptCandidate(strings.TrimSpace(cmd.Args[1]))
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "memory", Output: renderMemoryAccept(entry)}, nil
	case "dismiss":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /memory dismiss <fingerprint>")
		}
		candidate, err := mgr.DismissCandidate(strings.TrimSpace(cmd.Args[1]))
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "memory", Output: renderMemoryDismiss(candidate)}, nil
	case "digest":
		if len(cmd.Args) > 1 {
			return Result{}, fmt.Errorf("command /memory digest does not accept arguments")
		}
		return s.executeMemoryDigest(a)
	case "log":
		return s.executeMemoryLog(a, Command{Name: "memory-log", Args: cmd.Args[1:]})
	case "restore":
		return s.executeMemoryRestore(a, Command{Name: "memory-restore", Args: cmd.Args[1:]})
	default:
		return Result{}, fmt.Errorf("unknown /memory subcommand %q", cmd.Args[0])
	}
}

func (s *Service) executeNote(ctx context.Context, cmd Command) (Result, error) {
	manager := notes.NewManager(s.notesDir())
	if len(cmd.Args) == 0 {
		return Result{Name: "note", Output: "Usage: /note list|search [query] [--tag tag], /note create <title> [--tags a,b] -- <markdown>, /note append [id] -- <markdown>, or /note update [id] -- <markdown>"}, nil
	}
	switch strings.ToLower(strings.TrimSpace(cmd.Args[0])) {
	case "list", "search":
		opts := parseNoteSearchArgs(cmd.Args[1:])
		items, err := manager.List(opts)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "note", Output: renderNotesList(items)}, nil
	case "get":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /note get <id>")
		}
		item, err := manager.Get(cmd.Args[1])
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "note", Output: renderNote(item)}, nil
	case "create", "new":
		input := parseNoteCreateArgs(cmd.Args[1:])
		item, err := manager.Save(input)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "note", Output: fmt.Sprintf("Created note %s at %s.", item.ID, item.Path)}, nil
	case "append":
		noteID, content := parseNoteMutationArgs(cmd.Args[1:], currentNoteID(ctx, cmd))
		if noteID == "" {
			return Result{}, fmt.Errorf("usage: /note append <id> -- <markdown>")
		}
		if strings.TrimSpace(content) == "" {
			return Result{}, fmt.Errorf("note append content is required")
		}
		item, err := manager.Get(noteID)
		if err != nil {
			return Result{}, err
		}
		nextContent := strings.TrimSpace(item.Content)
		if nextContent != "" {
			nextContent += "\n\n"
		}
		nextContent += strings.TrimSpace(content)
		updated, err := manager.Save(notes.SaveInput{ID: item.ID, Title: item.Title, Summary: item.Summary, Tags: item.Tags, Content: nextContent})
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "note", Output: fmt.Sprintf("Appended note %s at %s.", updated.ID, updated.Path), RefreshSnapshot: true}, nil
	case "update", "edit":
		noteID, content := parseNoteMutationArgs(cmd.Args[1:], currentNoteID(ctx, cmd))
		if noteID == "" {
			return Result{}, fmt.Errorf("usage: /note update <id> -- <markdown>")
		}
		if strings.TrimSpace(content) == "" {
			return Result{}, fmt.Errorf("note update content is required")
		}
		item, err := manager.Get(noteID)
		if err != nil {
			return Result{}, err
		}
		updated, err := manager.Save(notes.SaveInput{ID: item.ID, Title: item.Title, Summary: item.Summary, Tags: item.Tags, Content: strings.TrimSpace(content)})
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "note", Output: fmt.Sprintf("Updated note %s at %s.", updated.ID, updated.Path), RefreshSnapshot: true}, nil
	default:
		input := parseNoteCreateArgs(cmd.Args)
		item, err := manager.Save(input)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "note", Output: fmt.Sprintf("Created note %s at %s.", item.ID, item.Path)}, nil
	}
}

func (s *Service) notesDir() string {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	if cfg == nil {
		return filepath.Join(".godex", "notes")
	}
	if strings.TrimSpace(cfg.HomeDir) != "" {
		return filepath.Join(cfg.HomeDir, "notes")
	}
	return filepath.Join(cfg.StateDir, "notes")
}

func (s *Service) executeMemoryDigest(a *agent.Agent) (Result, error) {
	mgr := a.MemoryMgr()
	if mgr == nil {
		return Result{Name: "memory-digest", Output: "Memory runtime is unavailable in this process."}, nil
	}

	s.mu.RLock()
	analyze := s.analyze
	cfg := s.cfg
	s.mu.RUnlock()

	report, err := analyze(buildInsightsInput(collectInsightsSnapshot(a)))
	if err != nil {
		return Result{}, err
	}
	extractor := memory.NewExtractor(mgr, cfg.TempDir)
	added, err := extractor.CaptureInsightsReport(report)
	if err != nil {
		return Result{}, err
	}

	markdown := report.Markdown()
	reportPath := filepath.Join(cfg.TempDir, "memory-digest-latest.md")
	if writeErr := os.WriteFile(reportPath, []byte(markdown), 0644); writeErr != nil {
		output := renderMemoryDigest(markdown, added, "")
		return Result{Name: "memory-digest", Output: output, RefreshSnapshot: len(added) > 0}, fmt.Errorf("write memory digest report: %w", writeErr)
	}
	return Result{
		Name:            "memory-digest",
		Output:          renderMemoryDigest(markdown, added, reportPath),
		ArtifactPath:    reportPath,
		RefreshSnapshot: len(added) > 0,
	}, nil
}

func (s *Service) executeMemoryLog(a *agent.Agent, cmd Command) (Result, error) {
	mgr := a.MemoryMgr()
	if mgr == nil {
		return Result{Name: "memory-log", Output: "Memory runtime is unavailable in this process."}, nil
	}
	limit := 20
	if len(cmd.Args) > 0 {
		parsed, err := strconv.Atoi(strings.TrimSpace(cmd.Args[0]))
		if err != nil || parsed <= 0 {
			return Result{}, fmt.Errorf("usage: /memory-log [limit]")
		}
		limit = parsed
	}
	items, err := mgr.ListAudit(limit)
	if err != nil {
		return Result{}, err
	}
	return Result{Name: "memory-log", Output: renderMemoryAuditLog(items)}, nil
}

func (s *Service) executeMemoryRestore(a *agent.Agent, cmd Command) (Result, error) {
	mgr := a.MemoryMgr()
	if mgr == nil {
		return Result{Name: "memory-restore", Output: "Memory runtime is unavailable in this process."}, nil
	}
	if len(cmd.Args) < 1 {
		return Result{}, fmt.Errorf("usage: /memory-restore <audit-id> [before|after]")
	}
	target := "before"
	if len(cmd.Args) > 1 {
		target = strings.TrimSpace(cmd.Args[1])
	}
	entry, err := mgr.RestoreAudit(strings.TrimSpace(cmd.Args[0]), target)
	if err != nil {
		return Result{}, err
	}
	return Result{Name: "memory-restore", Output: renderMemoryRestore(entry, target), RefreshSnapshot: true}, nil
}

func (s *Service) executeModel(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"list"}
	}
	action := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
	switch action {
	case "help", "-h", "--help":
		if len(cmd.Args) != 1 {
			return Result{}, fmt.Errorf("usage: /model help")
		}
		return Result{Name: "model", Output: modelHelpText()}, nil
	case "get":
		if len(cmd.Args) != 1 {
			return Result{}, fmt.Errorf("usage: /model get")
		}
		return Result{Name: "model", Output: renderModelState(a.CurrentModel())}, nil
	case "list", "use", "session", "set", "default":
		if action == "list" && len(cmd.Args) != 1 {
			return Result{}, fmt.Errorf("usage: /model list")
		}
		if (action == "use" || action == "session") && len(cmd.Args) != 2 {
			return Result{}, fmt.Errorf("usage: /model use <profile-id>")
		}
		if (action == "set" || action == "default") && len(cmd.Args) != 2 {
			return Result{}, fmt.Errorf("usage: /model default <profile-or-model>")
		}
		s.mu.RLock()
		handler := s.model
		s.mu.RUnlock()
		if handler == nil {
			return Result{Name: "model", Output: "Model runtime is unavailable in this process."}, nil
		}
		return handler(ctx, cmd)
	default:
		if len(cmd.Args) == 1 {
			s.mu.RLock()
			handler := s.model
			s.mu.RUnlock()
			if handler == nil {
				return Result{Name: "model", Output: "Model runtime is unavailable in this process."}, nil
			}
			return handler(ctx, Command{Name: cmd.Name, Args: []string{"use", cmd.Args[0]}, Raw: cmd.Raw})
		}
		return Result{}, fmt.Errorf("unknown /model action %q", cmd.Args[0])
	}
}

func modelHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  /model list",
		"  /model use <profile-id>",
		"  /model <profile-id>",
		"  /model default <profile-or-model>",
		"  /model get",
		"",
		"Use `/model use` to switch only the current conversation.",
		"Use `/model default` to update the workspace default for new sessions.",
	}, "\n")
}

func (s *Service) executeClear(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.clear
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "clear", Output: "Clear runtime is unavailable in this process."}, nil
	}
	return handler(ctx, a, cmd)
}

func (s *Service) executeApprove(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.approve
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "approve", Output: "Permission approval runtime is unavailable in this process."}, nil
	}
	return handler(ctx, a, cmd)
}

func (s *Service) executeDeny(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.deny
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "deny", Output: "Permission approval runtime is unavailable in this process."}, nil
	}
	return handler(ctx, a, cmd)
}

func (s *Service) executeSession(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.session
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "session", Output: "Session runtime is unavailable in this process."}, nil
	}
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"current"}
	}
	return handler(ctx, a, cmd)
}

func (s *Service) executeNewSession(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.newSession
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "new", Output: "New session runtime is unavailable in this process."}, nil
	}
	if len(cmd.Args) > 0 {
		return Result{}, fmt.Errorf("command /%s does not accept arguments", cmd.Name)
	}
	return handler(ctx, a, cmd)
}

func (s *Service) executeResumeSession(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.resumeSession
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "resume", Output: "Resume session runtime is unavailable in this process."}, nil
	}
	return handler(ctx, a, cmd)
}

func (s *Service) executeHistory(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"show"}
	}
	action := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
	switch action {
	case "show":
		if len(cmd.Args) != 1 {
			return Result{}, fmt.Errorf("usage: /history show")
		}
		return Result{Name: "history", Output: renderHistory(a.GetMessages(), 0)}, nil
	case "tail":
		limit := 10
		if len(cmd.Args) > 2 {
			return Result{}, fmt.Errorf("usage: /history tail [count]")
		}
		if len(cmd.Args) == 2 {
			parsed, err := strconv.Atoi(strings.TrimSpace(cmd.Args[1]))
			if err != nil || parsed <= 0 {
				return Result{}, fmt.Errorf("usage: /history tail [count]")
			}
			limit = parsed
		}
		return Result{Name: "history", Output: renderHistory(a.GetMessages(), limit)}, nil
	case "search":
		req, err := parseHistorySearchArgs(cmd.Args[1:])
		if err != nil {
			return Result{}, err
		}
		runtime := a.HistorySearchRuntime()
		if runtime == nil {
			return Result{Name: "history", Output: "History search is unavailable in this process."}, nil
		}
		sessionID, runtimeCtx := historySearchSessionContext(ctx)
		result, err := runtime.SearchHistory(ctx, sessionID, runtimeCtx, req)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "history", Output: renderHistorySearch(result)}, nil
	default:
		return Result{}, fmt.Errorf("unknown /history action %q", cmd.Args[0])
	}
}

func (s *Service) executeCron(ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.cron
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "cron", Output: "Cron runtime is unavailable in this process."}, nil
	}
	return handler(ctx, cmd)
}

func (s *Service) executeHeartbeat(ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.heartbeat
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "heartbeat", Output: "Heartbeat runtime is unavailable in this process."}, nil
	}
	return handler(ctx, cmd)
}

func (s *Service) executeTodos(a *agent.Agent, cmd Command) (Result, error) {
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"list"}
	}
	switch strings.ToLower(strings.TrimSpace(cmd.Args[0])) {
	case "list":
		return Result{Name: cmd.Name, Output: a.TodoMgr().Render()}, nil
	case "clear":
		// Reset persists the empty list to disk and clears
		// the in-memory state.  If persistence fails we
		// surface the error instead of reporting success —
		// otherwise the next session's todos would silently
		// reappear from the stale on-disk file, which is
		// exactly the cross-session pollution bug we are
		// guarding against.
		if err := a.TodoMgr().Reset(); err != nil {
			return Result{Name: cmd.Name, Output: "Failed to clear todos: " + err.Error()}, err
		}
		return Result{Name: cmd.Name, Output: "Cleared todo list.", RefreshSnapshot: true}, nil
	default:
		return Result{}, fmt.Errorf("unknown /todos subcommand %q (usage: /todos list|clear)", cmd.Args[0])
	}
}

func (s *Service) executeInsights(a *agent.Agent) (Result, error) {
	s.mu.RLock()
	analyze := s.analyze
	cfg := s.cfg
	s.mu.RUnlock()

	report, err := analyze(buildInsightsInput(collectInsightsSnapshot(a)))
	if err != nil {
		return Result{}, err
	}

	markdown := report.Markdown()
	reportPath := filepath.Join(cfg.TempDir, "insights-latest.md")
	if err := os.WriteFile(reportPath, []byte(markdown), 0644); err != nil {
		return Result{
			Name:   "insights",
			Output: markdown,
		}, fmt.Errorf("write insights report: %w", err)
	}

	output := markdown + "\nSaved insights report to " + reportPath
	return Result{
		Name:         "insights",
		Output:       output,
		ArtifactPath: reportPath,
	}, nil
}

func (s *Service) executeDoctor() (Result, error) {
	s.mu.RLock()
	doctor := s.doctor
	s.mu.RUnlock()
	if doctor == nil {
		return Result{Name: "doctor", Output: "Configuration doctor is unavailable in this runtime."}, nil
	}
	report := doctor()
	return Result{Name: "doctor", Output: report.Text()}, nil
}

func renderTeam(list []*teammate.Teammate) string {
	if len(list) == 0 {
		return "No teammates."
	}
	lines := make([]string, 0, len(list))
	for _, tm := range list {
		lines = append(lines, fmt.Sprintf("%s (%s): %s", tm.Name, tm.Role, tm.Status))
	}
	return strings.Join(lines, "\n")
}

func renderSkillCatalog(items []skill.CatalogEntry) string {
	if len(items) == 0 {
		return "No skills discovered in the current skills directory."
	}
	lines := []string{"Discoverable skills:"}
	for _, item := range items {
		line := "- " + skillLabel(item.ID, item.Name)
		if item.Description != "" {
			line += " — " + item.Description
		}
		if item.Compatibility.Status != "" {
			line += " [" + string(item.Compatibility.Status) + "]"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderSkillSources(items []tools.SkillSourceEntry) string {
	if len(items) == 0 {
		return "No curated skill sources are configured."
	}
	lines := []string{"Skill sources:"}
	for _, item := range items {
		line := "- " + item.Name
		if item.Summary != "" {
			line += " — " + item.Summary
		}
		if strings.TrimSpace(item.Version) != "" {
			line += " @" + item.Version
		}
		if item.Installed {
			line += " [installed]"
		}
		if !item.InstallSupported {
			line += " [install-unavailable]"
		}
		if strings.TrimSpace(item.Origin) != "" {
			line += " {" + item.Origin + "}"
		}
		if strings.TrimSpace(item.Trust) != "" {
			line += " [trust=" + item.Trust + "]"
		}
		if len(item.Categories) > 0 {
			line += " categories=" + strings.Join(item.Categories, ",")
		}
		if item.Source != "" {
			line += " <" + item.Source + ">"
		}
		lines = append(lines, line)
	}
	sourceWarnings := make([]string, 0, len(items))
	for _, item := range items {
		sourceWarnings = append(sourceWarnings, item.Warnings...)
	}
	for _, warning := range stringutil.Unique(sourceWarnings) {
		lines = append(lines, "warning: "+warning)
	}
	return strings.Join(lines, "\n")
}

func renderPackageList(items []pkgregistry.Entry) string {
	if len(items) == 0 {
		return "No packages installed."
	}
	lines := []string{"Installed packages:"}
	for _, item := range items {
		line := "- " + item.Name
		if item.Version != "" {
			line += " @" + item.Version
		}
		if item.Description != "" {
			line += " — " + item.Description
		}
		line += " [" + item.Trust + "]"
		resources := packageResourceSummary(item.Resources)
		if resources != "" {
			line += " resources=" + resources
		}
		if len(item.Requires) > 0 {
			line += " requires=" + strings.Join(item.Requires, ",")
		}
		if len(item.Provides) > 0 {
			line += " provides=" + strings.Join(item.Provides, ",")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderPackageCommands(items []pkgregistry.Command) string {
	if len(items) == 0 {
		return "No package commands installed."
	}
	lines := []string{"Package commands:"}
	for _, item := range items {
		namespace := item.Namespace
		if namespace == "" {
			namespace = item.PackageName
		}
		line := fmt.Sprintf("- /%s %s", namespace, item.Name)
		if item.Description != "" {
			line += " — " + item.Description
		}
		if item.Mode != "" {
			line += " [" + item.Mode + "]"
		}
		if len(item.Roles) > 0 {
			line += " roles=" + strings.Join(item.Roles, ",")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderPackageRoles(items []pkgregistry.Role) string {
	if len(items) == 0 {
		return "No package roles installed."
	}
	lines := []string{"Package roles:"}
	for _, item := range items {
		line := "- " + item.ID
		if item.Name != "" && item.Name != item.ID {
			line += " (" + item.Name + ")"
		}
		if item.Description != "" {
			line += " — " + item.Description
		}
		if item.WriteEnabled {
			line += " [write]"
		} else {
			line += " [read-only]"
		}
		if len(item.Tools) > 0 {
			line += " tools=" + strings.Join(item.Tools, ",")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderPackagePrompts(items []pkgregistry.Prompt) string {
	if len(items) == 0 {
		return "No package prompts installed."
	}
	lines := []string{"Package prompts:"}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- %s:%s <%s>", item.PackageName, item.Name, item.Path))
	}
	return strings.Join(lines, "\n")
}

func packageResourceSummary(resources pkgregistry.Resources) string {
	parts := []string{}
	if len(resources.Skills) > 0 {
		parts = append(parts, fmt.Sprintf("skills:%d", len(resources.Skills)))
	}
	if len(resources.Prompts) > 0 {
		parts = append(parts, fmt.Sprintf("prompts:%d", len(resources.Prompts)))
	}
	if len(resources.Commands) > 0 {
		parts = append(parts, fmt.Sprintf("commands:%d", len(resources.Commands)))
	}
	if len(resources.Roles) > 0 {
		parts = append(parts, fmt.Sprintf("roles:%d", len(resources.Roles)))
	}
	if len(resources.Docs) > 0 {
		parts = append(parts, fmt.Sprintf("docs:%d", len(resources.Docs)))
	}
	if len(resources.Assets) > 0 {
		parts = append(parts, fmt.Sprintf("assets:%d", len(resources.Assets)))
	}
	return strings.Join(parts, ",")
}

func renderSkillEntry(item skill.CatalogEntry) string {
	lines := []string{skillLabel(item.ID, item.Name)}
	if item.Description != "" {
		lines = append(lines, item.Description)
	}
	if item.Version != "" {
		lines = append(lines, "version: "+item.Version)
	}
	if len(item.Categories) > 0 {
		lines = append(lines, "categories: "+strings.Join(item.Categories, ", "))
	}
	if item.Compatibility.Status != "" {
		lines = append(lines, "compatibility: "+string(item.Compatibility.Status))
	}
	if item.InstallMemory != nil {
		parts := []string{}
		if item.InstallMemory.SourceOrigin != "" {
			parts = append(parts, item.InstallMemory.SourceOrigin)
		}
		if item.InstallMemory.Trust != "" {
			parts = append(parts, item.InstallMemory.Trust)
		}
		if item.InstallMemory.Version != "" {
			parts = append(parts, item.InstallMemory.Version)
		}
		line := "installed from: " + item.InstallMemory.Source
		if len(parts) > 0 {
			line += " (" + strings.Join(parts, ", ") + ")"
		}
		lines = append(lines, line)
	}
	if len(item.Sections) > 0 {
		lines = append(lines, "sections: "+strings.Join(item.Sections, ", "))
	}
	if len(item.RecommendedBundles) > 0 {
		lines = append(lines, "recommended bundles: "+strings.Join(item.RecommendedBundles, ", "))
	}
	if len(item.Compatibility.MissingDependencies) > 0 {
		lines = append(lines, "missing dependencies: "+strings.Join(item.Compatibility.MissingDependencies, ", "))
	}
	if len(item.Warnings) > 0 {
		lines = append(lines, "warnings: "+strings.Join(item.Warnings, " | "))
	}
	return strings.Join(lines, "\n")
}

func renderActiveSkills(items []tools.SkillActivation) string {
	if len(items) == 0 {
		return "No active skills in this session."
	}
	lines := []string{"Active skills:"}
	for _, item := range items {
		line := "- " + skillLabel(item.ID, item.Name)
		if len(item.LoadedSections) > 0 {
			line += " [" + strings.Join(item.LoadedSections, ", ") + "]"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderSkillActivation(item tools.SkillActivation) string {
	lines := []string{fmt.Sprintf("%s: %s", skillLabel(item.ID, item.Name), item.Status)}
	if item.Description != "" {
		lines = append(lines, item.Description)
	}
	if len(item.LoadedSections) > 0 {
		lines = append(lines, "loaded sections: "+strings.Join(item.LoadedSections, ", "))
	}
	if len(item.AvailableSections) > 0 {
		lines = append(lines, "available sections: "+strings.Join(item.AvailableSections, ", "))
	}
	if len(item.RecommendedBundles) > 0 {
		lines = append(lines, "recommended bundles: "+strings.Join(item.RecommendedBundles, ", "))
	}
	if item.Compatibility.Status != "" {
		lines = append(lines, "compatibility: "+string(item.Compatibility.Status))
	}
	return strings.Join(lines, "\n")
}

func renderSkillInstall(item tools.SkillInstallResult) string {
	lines := []string{fmt.Sprintf("%s: %s", skillLabel(item.ID, item.Name), item.Status)}
	if item.Description != "" {
		lines = append(lines, item.Description)
	}
	if item.Source != "" {
		lines = append(lines, "source: "+item.Source)
	}
	if item.SourceOrigin != "" {
		lines = append(lines, "source origin: "+item.SourceOrigin)
	}
	if item.Trust != "" {
		lines = append(lines, "trust: "+item.Trust)
	}
	if item.Version != "" {
		lines = append(lines, "version: "+item.Version)
	}
	if len(item.Categories) > 0 {
		lines = append(lines, "categories: "+strings.Join(item.Categories, ", "))
	}
	if item.InstalledPath != "" {
		lines = append(lines, "installed path: "+item.InstalledPath)
	}
	if len(item.Sections) > 0 {
		lines = append(lines, "sections: "+strings.Join(item.Sections, ", "))
	}
	if len(item.RecommendedBundles) > 0 {
		lines = append(lines, "recommended bundles: "+strings.Join(item.RecommendedBundles, ", "))
	}
	if item.Compatibility.Status != "" {
		lines = append(lines, "compatibility: "+string(item.Compatibility.Status))
	}
	if len(item.Compatibility.MissingDependencies) > 0 {
		lines = append(lines, "missing dependencies: "+strings.Join(item.Compatibility.MissingDependencies, ", "))
	}
	if len(item.Warnings) > 0 {
		lines = append(lines, "warnings: "+strings.Join(item.Warnings, " | "))
	}
	return strings.Join(lines, "\n")
}

func skillLabel(id, name string) string {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" {
		return name
	}
	if name == "" || strings.EqualFold(id, name) {
		return id
	}
	return fmt.Sprintf("%s [%s]", name, id)
}

func renderSkillExpansion(item tools.SkillExpansion) string {
	lines := []string{fmt.Sprintf("%s: %s", skillLabel(item.ID, item.Name), item.Status)}
	if len(item.ExpandedSections) > 0 {
		lines = append(lines, "expanded sections: "+strings.Join(item.ExpandedSections, ", "))
	}
	if len(item.LoadedSections) > 0 {
		lines = append(lines, "loaded sections: "+strings.Join(item.LoadedSections, ", "))
	}
	if len(item.AvailableSections) > 0 {
		lines = append(lines, "available sections: "+strings.Join(item.AvailableSections, ", "))
	}
	if item.Compatibility.Status != "" {
		lines = append(lines, "compatibility: "+string(item.Compatibility.Status))
	}
	return strings.Join(lines, "\n")
}

func renderMemoryList(items []memory.Entry) string {
	if len(items) == 0 {
		return "No durable memories yet."
	}
	lines := []string{"Durable memories:"}
	for _, item := range items {
		line := fmt.Sprintf("- %s [%s] updated %s", item.Title, item.Type, formatMemoryTime(item.UpdatedAt))
		if len(item.Tags) > 0 {
			line += " tags=" + strings.Join(item.Tags, ",")
		}
		if item.Source != "" {
			line += " source=" + item.Source
		}
		line += " id=" + item.ID
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderNotesList(items []notes.Note) string {
	if len(items) == 0 {
		return "No notes yet."
	}
	lines := []string{"Notes:"}
	for _, item := range items {
		line := fmt.Sprintf("- %s id=%s updated=%s", item.Title, item.ID, formatMemoryTime(item.UpdatedAt))
		if len(item.Tags) > 0 {
			line += " tags=" + strings.Join(item.Tags, ",")
		}
		if item.Summary != "" {
			line += " — " + item.Summary
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderNote(item notes.Note) string {
	lines := []string{
		item.Title,
		"id: " + item.ID,
		"path: " + item.Path,
		"updated: " + formatMemoryTime(item.UpdatedAt),
	}
	if len(item.Tags) > 0 {
		lines = append(lines, "tags: "+strings.Join(item.Tags, ", "))
	}
	if item.Summary != "" {
		lines = append(lines, "summary: "+item.Summary)
	}
	if strings.TrimSpace(item.Content) != "" {
		lines = append(lines, "", strings.TrimSpace(item.Content))
	}
	return strings.Join(lines, "\n")
}

func parseNoteSearchArgs(args []string) notes.SearchOptions {
	filtered := make([]string, 0, len(args))
	var tag string
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "--tag" && i+1 < len(args):
			tag = args[i+1]
			i++
		case strings.HasPrefix(arg, "--tag="):
			tag = strings.TrimPrefix(arg, "--tag=")
		default:
			filtered = append(filtered, args[i])
		}
	}
	return notes.SearchOptions{
		Query: strings.TrimSpace(strings.Join(filtered, " ")),
		Tag:   strings.TrimSpace(tag),
	}
}

func parseNoteCreateArgs(args []string) notes.SaveInput {
	filtered := make([]string, 0, len(args))
	var tags []string
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "--tags" && i+1 < len(args):
			tags = append(tags, splitCommandTags(args[i+1])...)
			i++
		case strings.HasPrefix(arg, "--tags="):
			tags = append(tags, splitCommandTags(strings.TrimPrefix(arg, "--tags="))...)
		default:
			filtered = append(filtered, args[i])
		}
	}
	raw := strings.TrimSpace(strings.Join(filtered, " "))
	if raw == "" {
		return notes.SaveInput{Tags: tags}
	}
	if before, after, ok := strings.Cut(raw, " -- "); ok {
		title := strings.TrimSpace(before)
		content := strings.TrimSpace(after)
		if content != "" && !strings.HasPrefix(content, "#") {
			content = "# " + title + "\n\n" + content
		}
		return notes.SaveInput{Title: title, Content: content, Tags: tags}
	}
	return notes.SaveInput{Title: raw, Content: "# " + raw, Tags: tags}
}

func parseNoteMutationArgs(args []string, fallbackID string) (string, string) {
	raw := strings.TrimSpace(strings.Join(args, " "))
	if raw == "" {
		return strings.TrimSpace(fallbackID), ""
	}
	if strings.HasPrefix(raw, "-- ") {
		return strings.TrimSpace(fallbackID), strings.TrimSpace(strings.TrimPrefix(raw, "-- "))
	}
	before, after, ok := strings.Cut(raw, " -- ")
	if !ok {
		return strings.TrimSpace(fallbackID), raw
	}
	before = strings.TrimSpace(before)
	if before == "" {
		before = fallbackID
	}
	return strings.TrimSpace(before), strings.TrimSpace(after)
}

func currentNoteID(ctx context.Context, cmd Command) string {
	if value := strings.TrimSpace(cmd.Metadata["note_id"]); value != "" {
		return value
	}
	if strings.EqualFold(cmd.Metadata["app_object_type"], "note") {
		if value := strings.TrimSpace(cmd.Metadata["app_object_id"]); value != "" {
			return value
		}
	}
	current, ok := CurrentSessionContext(ctx)
	if !ok {
		return ""
	}
	if value := strings.TrimSpace(current.Metadata["note_id"]); value != "" {
		return value
	}
	if strings.EqualFold(current.Metadata["app_object_type"], "note") {
		return strings.TrimSpace(current.Metadata["app_object_id"])
	}
	return ""
}

func splitCommandTags(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';'
	})
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		if tag := strings.TrimSpace(part); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func renderStoredMemory(item memory.StoredMemory) string {
	lines := []string{
		item.Title,
		fmt.Sprintf("id: %s", item.ID),
		fmt.Sprintf("type: %s", item.Type),
		fmt.Sprintf("updated: %s", formatMemoryTime(item.UpdatedAt)),
	}
	if !item.CreatedAt.IsZero() {
		lines = append(lines, fmt.Sprintf("created: %s", formatMemoryTime(item.CreatedAt)))
	}
	if item.File != "" {
		lines = append(lines, "file: "+item.File)
	}
	if item.Source != "" {
		lines = append(lines, "source: "+item.Source)
	}
	if len(item.Tags) > 0 {
		lines = append(lines, "tags: "+strings.Join(item.Tags, ", "))
	}
	if item.Summary != "" {
		lines = append(lines, "summary: "+item.Summary)
	}
	if strings.TrimSpace(item.Content) != "" {
		lines = append(lines, "", strings.TrimSpace(item.Content))
	}
	return strings.Join(lines, "\n")
}

func renderMemorySearch(items []memory.StoredMemory) string {
	if len(items) == 0 {
		return "No durable memories matched that query."
	}
	lines := []string{"Memory search results:"}
	for _, item := range items {
		line := fmt.Sprintf("- %s [%s] — %s", item.Title, item.Type, item.Summary)
		if len(item.Tags) > 0 {
			line += " tags=" + strings.Join(item.Tags, ",")
		}
		if item.Source != "" {
			line += " source=" + item.Source
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderMemoryCandidates(items []memory.Candidate) string {
	if len(items) == 0 {
		return "No pending memory candidates."
	}
	lines := []string{"Pending memory candidates:"}
	for _, item := range items {
		line := fmt.Sprintf("- %s [%s] — %s", item.Title, item.Type, item.Summary)
		if item.Source != "" {
			line += " source=" + item.Source
		}
		if !item.CreatedAt.IsZero() {
			line += " created=" + formatMemoryTime(item.CreatedAt)
		}
		line += " fingerprint=" + item.Fingerprint
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderMemoryAccept(item *memory.Entry) string {
	if item == nil {
		return "Accepted memory candidate."
	}
	return fmt.Sprintf("Accepted memory candidate: %s [%s] id=%s", item.Title, item.Type, item.ID)
}

func renderMemoryDismiss(item *memory.Candidate) string {
	if item == nil {
		return "Dismissed memory candidate."
	}
	return fmt.Sprintf("Dismissed memory candidate: %s [%s] fingerprint=%s", item.Title, item.Type, item.Fingerprint)
}

func renderMemoryAuditLog(items []memory.AuditLogEntry) string {
	if len(items) == 0 {
		return "No durable memory audit entries yet."
	}
	lines := []string{"Durable memory audit log:"}
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "(candidate only)"
		}
		line := fmt.Sprintf("- %s %s [%s] %s", item.ID, item.Action, item.Type, title)
		if item.MemoryID != "" {
			line += " memory_id=" + item.MemoryID
		}
		if item.CandidateFingerprint != "" {
			line += " candidate=" + item.CandidateFingerprint
		}
		if !item.CreatedAt.IsZero() {
			line += " at=" + formatMemoryTime(item.CreatedAt)
		}
		if item.Message != "" {
			line += " — " + item.Message
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderMemoryDigest(markdown string, added []memory.Candidate, reportPath string) string {
	lines := []string{"Memory digest completed."}
	if len(added) == 0 {
		lines = append(lines, "No new durable-memory candidates were added.")
	} else {
		lines = append(lines, fmt.Sprintf("Added %d durable-memory candidate(s):", len(added)))
		for _, item := range added {
			line := fmt.Sprintf("- %s [%s] — %s fingerprint=%s", item.Title, item.Type, item.Summary, item.Fingerprint)
			if item.Source != "" {
				line += " source=" + item.Source
			}
			lines = append(lines, line)
		}
	}
	if strings.TrimSpace(reportPath) != "" {
		lines = append(lines, "Saved digest report to "+reportPath)
	}
	if strings.TrimSpace(markdown) != "" {
		lines = append(lines, "", strings.TrimSpace(markdown))
	}
	return strings.Join(lines, "\n")
}

func renderMemoryRestore(item *memory.AuditLogEntry, target string) string {
	if item == nil {
		return "Restored memory audit snapshot."
	}
	target = strings.TrimSpace(target)
	if target == "" {
		target = "before"
	}
	title := item.Title
	if title == "" {
		title = item.MemoryID
	}
	return fmt.Sprintf("Restored %s snapshot from %s: %s [%s]", target, item.ID, title, item.Type)
}

func renderModelState(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "(unset)"
	}
	return "Current model: " + model
}

func renderHistory(messages []protocol.Message, tail int) string {
	if len(messages) == 0 {
		return "No conversation history yet."
	}
	start := 0
	if tail > 0 && len(messages) > tail {
		start = len(messages) - tail
	}
	lines := []string{"Conversation history:"}
	for idx := start; idx < len(messages); idx++ {
		msg := messages[idx]
		label := fmt.Sprintf("%d. %s", idx+1, msg.Role)
		if msg.Metadata != nil && msg.Metadata.Kind != "" {
			label += " [" + string(msg.Metadata.Kind) + "]"
		}
		lines = append(lines, label)
		text := strings.TrimSpace(protocol.MessageText(msg))
		if text != "" {
			lines = append(lines, "   "+text)
		}
		if msg.Metadata != nil && len(msg.Metadata.Attachments) > 0 {
			names := make([]string, 0, len(msg.Metadata.Attachments))
			for _, attachment := range msg.Metadata.Attachments {
				name := strings.TrimSpace(attachment.Name)
				if name == "" {
					name = attachment.ID
				}
				names = append(names, name)
			}
			lines = append(lines, "   attachments: "+strings.Join(names, ", "))
		}
		if text == "" && (msg.Metadata == nil || len(msg.Metadata.Attachments) == 0) {
			lines = append(lines, "   (no text)")
		}
	}
	return strings.Join(lines, "\n")
}

func parseHistorySearchArgs(args []string) (tools.HistorySearchRequest, error) {
	usageErr := fmt.Errorf("usage: /history search <query> [scope=current_session|session_archive|all_archives] [limit=N] [role=user|assistant|any]")
	if len(args) == 0 {
		return tools.HistorySearchRequest{}, usageErr
	}

	req := tools.HistorySearchRequest{}
	queryParts := make([]string, 0, len(args))
	for _, raw := range args {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			queryParts = append(queryParts, raw)
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if value == "" {
			return tools.HistorySearchRequest{}, usageErr
		}
		switch key {
		case "scope":
			switch value {
			case tools.HistorySearchScopeCurrentSession, tools.HistorySearchScopeSessionArchive, tools.HistorySearchScopeAllArchives:
				req.Scope = value
			default:
				return tools.HistorySearchRequest{}, usageErr
			}
		case "limit":
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return tools.HistorySearchRequest{}, usageErr
			}
			req.Limit = parsed
		case "role":
			switch value {
			case "user", "assistant", "any":
				req.Role = value
			default:
				return tools.HistorySearchRequest{}, usageErr
			}
		default:
			return tools.HistorySearchRequest{}, usageErr
		}
	}

	req.Query = strings.TrimSpace(strings.Join(queryParts, " "))
	if req.Query == "" {
		return tools.HistorySearchRequest{}, usageErr
	}
	return req, nil
}

func historySearchSessionContext(ctx context.Context) (string, automation.SessionContext) {
	current, ok := CurrentSessionContext(ctx)
	if !ok {
		return "", automation.SessionContext{}
	}
	return strings.TrimSpace(current.SessionID), automation.SessionContext{
		SessionID:      current.SessionID,
		LocatorChannel: current.Channel,
		LocatorKey:     current.Key,
		LocatorUserID:  current.UserID,
		Metadata:       cloneStringMap(current.Metadata),
	}
}

func renderHistorySearch(result tools.HistorySearchResult) string {
	lines := []string{
		"History search:",
		"Scope: " + strings.TrimSpace(result.Scope),
		fmt.Sprintf("Matches: %d", result.MatchCount),
	}
	if len(result.Snippets) == 0 {
		lines = append(lines, "No matching history snippets found.")
		return strings.Join(lines, "\n")
	}
	for idx, item := range result.Snippets {
		header := fmt.Sprintf("%d. %s", idx+1, item.Role)
		if !item.Timestamp.IsZero() {
			header += " @ " + item.Timestamp.UTC().Format("2006-01-02 15:04Z")
		}
		lines = append(lines, header)
		meta := make([]string, 0, 3)
		if item.SourceKind != "" {
			meta = append(meta, "source="+item.SourceKind)
		}
		if item.SessionID != "" {
			meta = append(meta, "session="+item.SessionID)
		}
		if item.SessionTitle != "" {
			meta = append(meta, "title="+item.SessionTitle)
		}
		if len(meta) > 0 {
			lines = append(lines, "   "+strings.Join(meta, " | "))
		}
		lines = append(lines, "   "+item.TextExcerpt)
	}
	return strings.Join(lines, "\n")
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
