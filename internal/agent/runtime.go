package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/mcp"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/pluginrt"
	"github.com/tim5wang/godex/internal/services/sessionadmin"
	"github.com/tim5wang/godex/internal/tools"
)

// SharedDependencies holds workspace-scoped services that can be reused across
// multiple session-scoped agents.
type SharedDependencies struct {
	mu   sync.RWMutex
	deps dependencies
	// browserCDPDialer is the relay-backed CDP dialer installed by the center
	// (distributed browser runtime). It is re-applied after ApplyConfig
	// rebuilds the dependency set.
	browserCDPDialer tools.CDPDialer
	// longTaskResumeOnce guards the one-time sweep of stale longtask run
	// records at process startup. After a crash the previous process may
	// leave runs marked "running"; this flips them to "interrupted" so they
	// can be resumed via --resume-run-id by a later turn.
	longTaskResumeOnce sync.Once
}

// NewSharedDependencies creates a reusable dependency set for one workspace.
func NewSharedDependencies(cfg *config.Config) *SharedDependencies {
	return &SharedDependencies{deps: buildDependencies(cfg)}
}

// NewSharedDependenciesWithCaller creates reusable dependencies with a caller override.
func NewSharedDependenciesWithCaller(cfg *config.Config, caller conversation.Caller) *SharedDependencies {
	deps := buildDependencies(cfg)
	if caller != nil {
		deps.client = caller
		deps.skillLoader = newSkillLoader(cfg, caller)
		deps.teamMgr = newTeamManager(cfg, deps.taskMgr, deps.msgBus, caller)
		if deps.subagentJobs == nil {
			deps.subagentJobs = newSubagentJobStore(subagentJobsDir(cfg))
		}
	}
	return &SharedDependencies{deps: deps}
}

func (s *SharedDependencies) snapshot() dependencies {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deps
}

// MCPManager returns the workspace-scoped MCP server registry manager used for
// MCP lifecycle management (list/upsert/delete/test), or nil if unavailable.
func (s *SharedDependencies) MCPManager() *mcp.Manager {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deps.mcpMgr
}

// PluginManager returns the shared plugin kernel that owns reversible
// plugin registrations (routes, tools, schedules), or nil if unavailable.
func (s *SharedDependencies) PluginManager() *pluginrt.Manager {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deps.pluginMgr
}

// ApplyConfig refreshes shared workspace-scoped services for subsequent turns.
func (s *SharedDependencies) ApplyConfig(cfg *config.Config) {
	if s == nil || cfg == nil {
		return
	}
	s.mu.RLock()
	cronService := s.deps.cron
	heartbeatService := s.deps.heartbeat
	permissionManager := s.deps.permissions
	sessionAdminService := s.deps.sessionAdmin
	subagentJobs := s.deps.subagentJobs
	cdpDialer := s.browserCDPDialer
	s.mu.RUnlock()
	deps := buildDependencies(cfg)
	deps.cron = cronService
	deps.heartbeat = heartbeatService
	deps.sessionAdmin = sessionAdminService
	if subagentJobs != nil {
		deps.subagentJobs = subagentJobs
	}
	if permissionManager != nil {
		permissionManager.ApplyPolicy(permissionPolicyFromConfig(cfg))
		deps.permissions = permissionManager
	}
	if cdpDialer != nil {
		deps.browser.SetCDPDialer(cdpDialer)
	}
	s.mu.Lock()
	s.deps = deps
	s.mu.Unlock()
}

// SetCronService installs a workspace-scoped cron manager for all future agents.
func (s *SharedDependencies) SetCronService(cron tools.CronManager) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.deps.cron = cron
	s.mu.Unlock()
}

// SetHeartbeatService installs a workspace-scoped heartbeat manager for all future agents.
func (s *SharedDependencies) SetHeartbeatService(heartbeat tools.HeartbeatManager) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.deps.heartbeat = heartbeat
	s.mu.Unlock()
}

// SetSessionAdminService installs a shared session-management runtime for future agents.
func (s *SharedDependencies) SetSessionAdminService(admin *sessionadmin.Service) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.deps.sessionAdmin = admin
	s.mu.Unlock()
}

// SetBrowserCDPDialer installs the relay-backed CDP dialer (distributed
// browser runtime). It is applied to the current browser service and
// re-applied after every ApplyConfig rebuild.
func (s *SharedDependencies) SetBrowserCDPDialer(dialer tools.CDPDialer) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.browserCDPDialer = dialer
	if s.deps.browser != nil {
		s.deps.browser.SetCDPDialer(dialer)
	}
	s.mu.Unlock()
}

// ResumeLongTasksAfterRestart runs the one-time startup sweep that marks
// stale longtask run records (left "running" by a process that crashed)
// as "interrupted" so they can be resumed via --resume-run-id. It is safe
// to call more than once; only the first call performs the sweep.
func (s *SharedDependencies) ResumeLongTasksAfterRestart() {
	if s == nil {
		return
	}
	s.longTaskResumeOnce.Do(func() {
		s.mu.RLock()
		store := s.deps.workflows
		s.mu.RUnlock()
		if store == nil {
			return
		}
		_, _ = store.sweepStaleLongTaskRuns()
	})
}

// NewWithSharedDependencies creates a session-scoped agent on top of shared workspace services.
//
// The sessionID argument scopes per-session state — most importantly the
// todo list — to the given session, so that todos from one session never
// leak into a freshly-opened web / weixin / local session on the same
// workspace.  An empty sessionID is treated as a legacy "global" scope
// and is kept around only for unit tests and tooling that have no notion
// of a session.
func NewWithSharedDependencies(cfg *config.Config, shared *SharedDependencies, sessionID string) *Agent {
	if shared == nil {
		shared = NewSharedDependencies(cfg)
	}
	deps := shared.snapshot()
	if strings.TrimSpace(sessionID) != "" {
		// Always rebuild the todo manager per session so
		// two sessions opened in the same workspace can
		// never read each other's todos.json.  Without
		// this guard the shared deps' global todo
		// manager would carry over across sessions.
		deps.todoMgr = todo.NewManagerForSession(cfg.SessionsDir, sessionID)
		// Roadmap 6.2: when memory.session_scope is enabled, each session
		// also gets its own memory manager rooted under cfg.MemoryDir so
		// durable memory cannot leak across sessions. The default (disabled)
		// keeps the shared org-level memory layer.
		if cfg != nil && cfg.Memory.SessionScope {
			scopedMgr := memory.NewScopedManager(cfg.MemoryDir, scope.Session(sessionID))
			deps.memoryMgr = scopedMgr
			deps.memoryExt, deps.memoryStrategy = buildMemoryStack(cfg, deps.client, scopedMgr)
		}
	}
	return newAgentWithDependencies(cfg, deps)
}

// NewForSession is the preferred entry point when a sessionID is
// known.  It wires a per-session todo manager rooted at
// <sessionsDir>/<sessionID>/todos.json so that todos cannot cross
// session boundaries.  All other dependencies are still sourced
// from the shared workspace services.
//
// When cfg carries a WorkspaceDir different from the one the shared
// dependencies were built with, the returned agent owns a per-session
// sandbox rooted at cfg.WorkspaceDir so tool execution lands in the
// session's working directory instead of the service directory.
func NewForSession(cfg *config.Config, shared *SharedDependencies, sessionID string) *Agent {
	agent := NewWithSharedDependencies(cfg, shared, sessionID)
	agent.sessionID = strings.TrimSpace(sessionID)
	agent.ensureWorkspaceSandbox(cfg, shared)
	return agent
}

// ensureWorkspaceSandbox detects a per-session workspace override and
// rebuilds the agent's sandbox against cfg.WorkspaceDir when the shared
// sandbox is still bound to a different directory.  The workspaceOverride
// field records the override so ApplyConfig can re-apply it after global
// config reloads.
func (a *Agent) ensureWorkspaceSandbox(cfg *config.Config, shared *SharedDependencies) {
	if a == nil || cfg == nil || shared == nil {
		return
	}
	dir := strings.TrimSpace(cfg.WorkspaceDir)
	if dir == "" {
		return
	}
	sharedDir := ""
	if sandbox := shared.snapshot().sandbox; sandbox != nil {
		sharedDir = strings.TrimSpace(sandbox.ToolBinding().WorkspaceDir)
	}
	if sharedDir == "" || sameWorkspaceDir(dir, sharedDir) {
		return
	}
	a.mu.Lock()
	a.workspaceOverride = dir
	a.sandbox = localSandboxFromConfig(cfg)
	a.mu.Unlock()
}

func sameWorkspaceDir(a, b string) bool {
	if a == b {
		return true
	}
	// Resolve symlinks (macOS /var → /private/var) so the default
	// session never allocates a redundant sandbox.
	resolve := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		return p
	}
	return resolve(a) == resolve(b)
}

// CloneConfigForWorkspace returns a copy of cfg whose WorkspaceDir is
// pinned to workspaceDir, with TempDir re-derived when it lived inside
// the original workspace.  The backend uses it when loading a session
// opened against an explicit working directory, and ApplyConfig uses
// it to re-pin refreshed global configs onto the same override.
func CloneConfigForWorkspace(cfg *config.Config, workspaceDir string) *config.Config {
	if cfg == nil {
		return nil
	}
	cloned := cfg.Clone()
	cloned.WorkspaceDir = workspaceDir
	cloned.TempDir = workspaceDerivedTempDir(workspaceDir, cfg.WorkspaceDir, cfg.TempDir)
	return cloned
}

// workspaceDerivedTempDir picks the temp dir for a session whose
// workspace was overridden: when the base temp dir lives inside the
// base workspace (the default `<workspace>/.godex/.tmp`), the derived
// temp dir mirrors that layout inside the overridden workspace so
// spilled tool results stay next to the files they describe.  A
// temp dir configured outside the workspace is user intent and is
// kept as-is.
func workspaceDerivedTempDir(workspaceDir, baseWorkspaceDir, baseTempDir string) string {
	baseTempDir = strings.TrimSpace(baseTempDir)
	if baseTempDir == "" {
		return ""
	}
	rel, err := filepath.Rel(baseWorkspaceDir, baseTempDir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return baseTempDir
	}
	return filepath.Join(workspaceDir, rel)
}

// ApplyConfig swaps the agent runtime dependencies to a fresh config snapshot.
//
// Sessions opened against an explicit per-session working directory
// (workspaceOverride set by NewForSession) keep their override: the
// refreshed global config is cloned and WorkspaceDir is pinned back to
// the session directory, and the sandbox is rebuilt against it, so a
// global config reload can never silently move tool execution back to
// the service directory.
func (a *Agent) ApplyConfig(cfg *config.Config, shared *SharedDependencies) {
	if cfg == nil {
		return
	}
	if shared == nil {
		shared = NewSharedDependencies(cfg)
	}
	deps := shared.snapshot()

	handler := a.toolHandler
	activeBundles := append([]string{}, handler.Catalog().ActiveBundles...)
	a.mu.Lock()
	if override := strings.TrimSpace(a.workspaceOverride); override != "" && !sameWorkspaceDir(override, strings.TrimSpace(cfg.WorkspaceDir)) {
		cfg = CloneConfigForWorkspace(cfg, override)
		a.sandbox = localSandboxFromConfig(cfg)
	} else {
		a.sandbox = deps.sandbox
	}
	a.cfg = cfg
	a.taskMgr = deps.taskMgr
	a.msgBus = deps.msgBus
	a.client = deps.client
	a.screener = buildScreener(cfg, deps.client)
	a.skillLoader = deps.skillLoader
	a.instrLoader = deps.instrLoader
	a.memoryMgr = deps.memoryMgr
	a.memoryExt = deps.memoryExt
	a.memoryStrategy = deps.memoryStrategy
	a.mcpMgr = deps.mcpMgr
	a.compressor = deps.compressor
	a.summarizer = deps.summarizer
	a.bgMgr = deps.bgMgr
	a.webSearch = deps.webSearch
	a.webFetch = deps.webFetch
	a.browser = deps.browser
	a.permissions = deps.permissions
	if deps.history != nil {
		a.historySearch = deps.history.Bind(a)
	} else {
		a.historySearch = nil
	}
	if deps.sessionAdmin != nil {
		a.sessionAdmin = deps.sessionAdmin.Bind(a)
	} else {
		a.sessionAdmin = nil
	}
	a.cron = deps.cron
	a.heartbeat = deps.heartbeat
	a.media = deps.media
	a.teamMgr = deps.teamMgr
	a.subagentJobs = deps.subagentJobs
	if a.subagentJobs == nil {
		a.subagentJobs = newSubagentJobStore(subagentJobsDir(cfg))
	}
	a.todoMgr = deps.todoMgr
	a.mu.Unlock()

	// Package-owned registrations live in the current ToolHandler. Tear them
	// down before replacing that registry, then recreate them on the stable
	// handler after the builtin registry swap.
	if err := a.DeactivateInstalledPackageRuntimes(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to deactivate package runtimes before config reload: %v\n", err)
	}
	nextHandler := tools.NewToolHandler()
	nextHandler.AddBeforeInterceptors(tools.NewPermissionInterceptorWithReview(a.permissions, a.reviewPermissionRequest))
	a.registerToolsWith(nextHandler)
	nextHandler.SetActiveBundles(activeBundles...)
	handler.ReplaceWith(nextHandler)
	// tool_exchange mutates the handler it is constructed with. Rebind it to
	// the stable session handler after registry replacement, not the temporary
	// rebuild handler.
	a.registerToolTo(handler, tools.NewToolExchangeTool(handler), tools.ToolMeta{AlwaysActive: true})
	if err := a.ActivateInstalledPackageRuntimes(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to activate package runtimes after config reload: %v\n", err)
	}
}

// ApplyModelProfile swaps only the model caller/config for this session.
func (a *Agent) ApplyModelProfile(profile config.ModelProfileConfig) {
	client := callerForConfigProfile(a.cfg, profile)
	a.mu.Lock()
	cfg := a.cfg.Clone()
	cfg.DefaultProfileID = profile.ID
	cfg.APIKey = profile.APIKey
	cfg.Model = profile.Model
	cfg.ReasoningEffort = profile.ReasoningEffort
	cfg.BaseURL = profile.BaseURL
	cfg.MaxTokens = profile.MaxTokens
	cfg.APITimeoutSeconds = profile.TimeoutSeconds
	if cfg.ModelProfiles == nil {
		cfg.ModelProfiles = map[string]config.ModelProfileConfig{}
	}
	cfg.ModelProfiles[profile.ID] = profile
	a.cfg = cfg
	a.client = client
	a.skillLoader = newSkillLoader(cfg, client)
	a.teamMgr = newTeamManager(cfg, a.taskMgr, a.msgBus, client)
	// Keep the compaction summarizer bound to the session's active model so
	// model/hybrid compaction uses the LLM the session is currently on instead
	// of the startup default (which is rule-based when cfg.APIKey was empty).
	ruleSummarizer := compress.NewRuleBasedSessionSummarizer(a.compressor)
	if client != nil && strings.TrimSpace(profile.Model) != "" {
		a.summarizer = compress.NewLLMSessionSummarizer(client, profile.Model, min(profile.MaxTokens, 2048), a.compressor, ruleSummarizer)
	} else {
		a.summarizer = ruleSummarizer
	}
	a.mu.Unlock()
}

// RunOptions configures one agent turn execution.
type RunOptions struct {
	SessionID          string
	TurnID             string
	ActorID            string
	ActorKind          string
	EmitRunnerPhases   bool
	Sink               events.Sink
	RuntimeContext     automation.SessionContext
	Checkpoint         func()
	DrainInjections    func(context.Context, int) (conversation.InjectionDrain, error)
	OnInjectionDrained func(conversation.InjectionDrain)
	// Harness selects a non-default engine for this turn (roadmap 6.4). When
	// empty or "godex", the default engine loop runs as before. A different
	// id routes the turn through the harness router, which resets engine
	// state when the session switches engines.
	Harness string
}

// SessionSkillState stores one activated skill's fully loaded prompt state.
type SessionSkillState struct {
	Name          string             `json:"name"`
	Catalog       skill.CatalogEntry `json:"catalog"`
	Core          string             `json:"core,omitempty"`
	Expanded      map[string]string  `json:"expanded,omitempty"`
	ExpandedOrder []string           `json:"expanded_order,omitempty"`
}

// PendingResumeState stores one blocked user turn that should be replayed after approval.
type PendingResumeState struct {
	RequestID         string                    `json:"request_id,omitempty"`
	PriorMessageCount int                       `json:"prior_message_count"`
	Envelope          message.Envelope          `json:"envelope"`
	Injections        []message.Envelope        `json:"injections,omitempty"`
	RuntimeContext    automation.SessionContext `json:"runtime_context,omitempty"`
}

// SessionUsageTotals persists the session-lifetime cumulative model-token
// totals (input + output) reported by the provider, so the status-bar
// cumulative counters survive service restarts and agent rebuilds.
type SessionUsageTotals struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// SessionState is the persisted session-scoped portion of an agent.
type SessionState struct {
	Messages             []protocol.Message           `json:"messages"`
	TranscriptRefs       []string                     `json:"transcript_refs,omitempty"`
	ActiveSkills         []SessionSkillState          `json:"active_skills,omitempty"`
	ActiveBundles        []string                     `json:"active_bundles,omitempty"`
	PermissionState      tools.PermissionSessionState `json:"permission_state,omitempty"`
	PendingResume        *PendingResumeState          `json:"pending_resume,omitempty"`
	HistoryVersion       int64                        `json:"history_version"`
	LastCompactedVersion int64                        `json:"last_compacted_version"`
	// CacheUsage persists the provider-reported prompt-cache aggregation so
	// the real cache hit rate survives restarts and agent rebuilds. Nil when
	// no provider usage has been observed yet.
	CacheUsage *tools.CacheUsageInspection `json:"cache_usage,omitempty"`
	// UsageTotals persists the session-lifetime cumulative token totals.
	UsageTotals *SessionUsageTotals `json:"usage_totals,omitempty"`
}

// ExportState snapshots the session-scoped agent state for persistence.
func (a *Agent) ExportState() SessionState {
	return a.ExportStateForSession("")
}

// ExportStateForSession snapshots session state plus session-scoped permission state.
func (a *Agent) ExportStateForSession(sessionID string) SessionState {
	a.mu.Lock()
	defer a.mu.Unlock()

	names := make([]string, 0, len(a.activeSkills))
	for name := range a.activeSkills {
		names = append(names, name)
	}
	sort.Strings(names)

	skills := make([]SessionSkillState, 0, len(names))
	for _, name := range names {
		state := a.activeSkills[name]
		if state == nil {
			continue
		}
		skills = append(skills, SessionSkillState{
			Name:          name,
			Catalog:       state.catalog,
			Core:          state.core,
			Expanded:      cloneExpandedSections(state.expanded),
			ExpandedOrder: append([]string{}, state.expandedOrder...),
		})
	}

	cacheUsage := a.cacheUsageSnapshot()
	cumInput, cumOutput := a.cumulativeTokenUsage()
	var cachePtr *tools.CacheUsageInspection
	if cacheUsage.Calls > 0 {
		c := cacheUsage
		cachePtr = &c
	}
	var usagePtr *SessionUsageTotals
	if cumInput > 0 || cumOutput > 0 {
		usagePtr = &SessionUsageTotals{InputTokens: cumInput, OutputTokens: cumOutput}
	}

	return SessionState{
		Messages:             protocol.CloneMessages(a.messages),
		TranscriptRefs:       append([]string{}, a.transcriptRefs...),
		ActiveSkills:         skills,
		ActiveBundles:        append([]string{}, a.toolHandler.Catalog().ActiveBundles...),
		PermissionState:      exportPermissionState(a.permissions, sessionID),
		PendingResume:        clonePendingResumeState(a.pendingResume),
		HistoryVersion:       a.historyVersion,
		LastCompactedVersion: a.lastCompactedVersion,
		CacheUsage:           cachePtr,
		UsageTotals:          usagePtr,
	}
}

// RestoreState restores a previously persisted session snapshot.
func (a *Agent) RestoreState(state SessionState) {
	a.RestoreStateForSession("", state)
}

// RestoreStateForSession restores persisted session state plus permission state.
func (a *Agent) RestoreStateForSession(sessionID string, state SessionState) {
	activeSkills := make(map[string]*activeSkillState, len(state.ActiveSkills))
	for _, item := range state.ActiveSkills {
		name := item.Name
		if name == "" {
			name = item.Catalog.ID
		}
		if name == "" {
			name = item.Catalog.Name
		}
		if name == "" {
			continue
		}
		activeSkills[name] = &activeSkillState{
			catalog:       item.Catalog,
			core:          item.Core,
			expanded:      cloneExpandedSections(item.Expanded),
			expandedOrder: append([]string{}, item.ExpandedOrder...),
		}
	}

	a.mu.Lock()
	a.messages = protocol.CloneMessages(state.Messages)
	a.transcriptRefs = mergeTranscriptRefs(state.TranscriptRefs, extractTranscriptRefs(state.Messages))
	a.activeSkills = activeSkills
	a.pendingResume = clonePendingResumeState(state.PendingResume)
	a.historyVersion = state.HistoryVersion
	a.lastCompactedVersion = state.LastCompactedVersion
	a.mu.Unlock()

	// Restore provider-reported cache/usage aggregates so the real cache hit
	// rate and cumulative token counters survive restarts. The agent is freshly
	// built at this point, so no usage hook is racing these writes.
	a.cacheStatsMu.Lock()
	if state.CacheUsage != nil && state.CacheUsage.Calls > 0 {
		a.cacheStats = sessionCacheStats{
			Calls:            state.CacheUsage.Calls,
			InputTokens:      state.CacheUsage.InputTokens,
			CacheReadTokens:  state.CacheUsage.CacheReadTokens,
			CacheWriteTokens: state.CacheUsage.CacheWriteTokens,
		}
	}
	a.cacheStatsMu.Unlock()
	a.usageMu.Lock()
	if state.UsageTotals != nil {
		a.usage = sessionUsage{InputTokens: state.UsageTotals.InputTokens, OutputTokens: state.UsageTotals.OutputTokens}
	}
	a.usageMu.Unlock()

	a.toolHandler.SetActiveBundles(state.ActiveBundles...)
	restorePermissionState(a.permissions, sessionID, state.PermissionState)
}

func exportPermissionState(manager *tools.PermissionManager, sessionID string) tools.PermissionSessionState {
	if manager == nil {
		return tools.PermissionSessionState{}
	}
	return manager.ExportSession(sessionID)
}

func restorePermissionState(manager *tools.PermissionManager, sessionID string, state tools.PermissionSessionState) {
	if manager == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	manager.RestoreSession(sessionID, state)
}

// RunWithOptions runs the agent loop without assuming any specific frontend.
func (a *Agent) RunWithOptions(ctx context.Context, opts RunOptions) error {
	// Roadmap 6.4: a per-turn engine request routes through the harness
	// router (which resets engine state when the session switches engines).
	// Empty or "godex" keeps the default loop below.
	if strings.TrimSpace(opts.Harness) != "" && opts.Harness != "godex" {
		// P2 #1: give the engine a stable access surface instead of the host's
		// internals. The godex engine is the only one that may reach into the
		// Agent; external engines build their turn from these inputs.
		result, err := a.harnessRouter().RunTurn(ctx, HarnessTurnInput{
			SessionID:          opts.SessionID,
			TurnID:             opts.TurnID,
			ActorID:            opts.ActorID,
			ActorKind:          opts.ActorKind,
			EmitRunnerPhases:   opts.EmitRunnerPhases,
			Sink:               opts.Sink,
			RuntimeContext:     opts.RuntimeContext,
			Checkpoint:         opts.Checkpoint,
			DrainInjections:    opts.DrainInjections,
			OnInjectionDrained: opts.OnInjectionDrained,
			Harness:            opts.Harness,
			Messages: func() []protocol.Message {
				return a.GetMessages()
			},
			WorkspaceDir: a.SandboxBinding().WorkspaceDir,
			Scope:        a.SandboxScope(),
			UsageContext: a.usageContext,
		})
		if err != nil {
			return err
		}
		// P2 #2: the host consumes the engine's reply — append it to the
		// transcript and checkpoint, exactly like the default loop does.
		if reply := strings.TrimSpace(result.Reply); reply != "" {
			a.AppendAssistantText(reply, "")
			if opts.Checkpoint != nil {
				opts.Checkpoint()
			}
			if sink := opts.Sink; sink != nil {
				sink.Emit(events.Event{
					SessionID: opts.SessionID,
					TurnID:    opts.TurnID,
					Type:      events.EventAssistantMessageComplete,
					Timestamp: a.now(),
					Payload:   events.TextPayload{Role: protocol.RoleAssistant, Text: reply},
				})
			}
		}
		return nil
	}
	ctx = tools.WithSessionID(ctx, opts.SessionID)
	ctx = tools.WithSessionContext(ctx, opts.RuntimeContext)
	ctx = conversation.WithUsageContext(ctx, a.usageContext(opts.RuntimeContext, opts.SessionID, opts.TurnID, ""))
	ctx = withHistoryRecallTurnState(ctx)
	a.registerUsageHook(opts.SessionID)
	a.resetIdle()
	var ackRuntime func()
	// thinkingBuf accumulates the reasoning deltas of the in-flight model call
	// so the completed assistant message (and its timeline detail) can carry
	// the full thinking text alongside the answer.
	var thinkingBuf strings.Builder
	sink := opts.Sink
	if sink == nil {
		sink = events.NopSink
	}
	a.emitSink = sink
	ctx = withSubagentEventTarget(ctx, subagentEventTarget{
		sessionID:  opts.SessionID,
		turnID:     opts.TurnID,
		sink:       sink,
		scopeLabel: string(a.SandboxScope()),
	})

	emit := func(eventType events.EventType, payload any) {
		sink.Emit(events.Event{
			SessionID: opts.SessionID,
			TurnID:    opts.TurnID,
			Type:      eventType,
			Timestamp: a.now(),
			Payload:   payload,
		})
	}
	checkpoint := func() {
		if opts.Checkpoint != nil {
			opts.Checkpoint()
		}
	}

	maxTurns := a.maxTurns()
	a.mu.Lock()
	caller := a.client
	loopGuard := a.cfg.Tools.LoopGuard
	toolTimeout := a.toolTimeout()
	a.mu.Unlock()
	result, err := conversation.Runner{
		Caller:                     caller,
		MaxRepeatedTools:           loopGuard.MaxRepeatedTools,
		MaxRepeatedPollingTools:    loopGuard.MaxRepeatedPollingTools,
		MaxStalledTaskPollingTools: loopGuard.MaxStalledTaskPollingTools,
		MaxLoopGuardRecoveries:     loopGuard.MaxRecoveries,
		LoopGuardMode:              conversation.LoopGuardMode(loopGuard.Mode),
		ToolTimeout:                toolTimeout,
		BuildRequest: func(ctx context.Context) (protocol.Request, error) {
			build, err := a.buildContext(ctx)
			if err != nil {
				return protocol.Request{}, err
			}
			if build.HistoryRecall != nil {
				emit(events.EventHistoryRecallDecision, events.HistoryRecallPayload{
					AllowTool:        build.HistoryRecall.AllowTool,
					Automatic:        build.HistoryRecall.Automatic,
					ExplicitRequest:  build.HistoryRecall.ExplicitRequest,
					RecommendedScope: build.HistoryRecall.RecommendedScope,
					Score:            build.HistoryRecall.Score,
					Reasons:          append([]string{}, build.HistoryRecall.Reasons...),
				})
			}
			if build.Compacted {
				emit(events.EventSnapshotReady, events.SnapshotPayload{
					UpdatedAt:           a.now(),
					Running:             true,
					Compacted:           true,
					CompressionReasons:  append([]string{}, build.CompressionReasons...),
					TokenEstimateBefore: build.CompactionBefore,
					TokenEstimateAfter:  build.CompactionAfter,
				})
				// Compaction rewrote the in-memory message history at this
				// request boundary. Persist the compacted context now so a
				// crash mid-turn does not lose the compression: the next
				// AppendAssistant/AppendToolResults checkpoint would otherwise
				// be the first durable snapshot past the compaction.
				checkpoint()
			}
			ackRuntime = build.AckRuntime
			apiMessages, err := conversation.BuildAPIMessages(ctx, build.Messages, conversation.BuildInputOptions{
				SessionID:     opts.SessionID,
				SupportsImage: true,
				Processor:     a.media,
			})
			if err != nil {
				return protocol.Request{}, err
			}
			req := conversation.NewRequestFromAPIMessages(a.cfg.Model, a.cfg.MaxTokens, a.cfg.ReasoningEffort, build.System, apiMessages, build.ToolSchemas)
			if strings.TrimSpace(opts.SessionID) != "" {
				req.PromptCacheKey = clampCacheKey(opts.SessionID)
				req.PromptCacheRetention = protocol.CacheRetentionLong
			}
			return req, nil
		},
		AppendAssistant: func(msg protocol.Message) {
			a.appendMessage(msg)
			checkpoint()
		},
		AppendToolResults: func(msg protocol.Message) {
			a.appendMessage(msg)
			checkpoint()
		},
		AppendRuntimeFeedback: func(msg protocol.Message) {
			a.appendMessage(msg)
			checkpoint()
		},
		ExecuteTool:      a.handleToolResult,
		ToolResultFilter: a.filterModelToolResult,
		OnAssistantTextDelta: func(text string) {
			if text == "" {
				return
			}
			payload := events.TextPayload{Role: protocol.RoleAssistant, Text: text}
			emit(events.EventAssistantTextDelta, payload)
		},
		OnAssistantThinkingDelta: func(text string) {
			if text == "" {
				return
			}
			thinkingBuf.WriteString(text)
			payload := events.TextPayload{Role: protocol.RoleAssistant, Text: text}
			emit(events.EventAssistantThinkingDelta, payload)
		},
		OnStreamStarted: func() {
			thinkingBuf.Reset()
			// The ChatGPT codex backend streams reasoning only as encrypted
			// content, so there are no plaintext thinking deltas to forward.
			// Emit a one-shot placeholder so the frontend shows "Thinking…"
			// instead of a blank wait while the model reasons.
			payload := events.TextPayload{Role: protocol.RoleAssistant, Text: "…"}
			emit(events.EventAssistantThinkingDelta, payload)
		},
		OnContextOverflow: func(ctx context.Context) bool {
			return a.compactForOverflow(ctx)
		},
		MaxContextOverflowRecoveries: 1,
		OnModelRequest: func(req protocol.Request, resp *protocol.Response, startedAt, firstTokenAt, completedAt time.Time) {
			payload := events.ModelRequestPayload{
				Model:        req.Model,
				StartedAt:    startedAt,
				FirstTokenAt: firstTokenAt,
				CompletedAt:  completedAt,
				DurationMS:   completedAt.Sub(startedAt).Milliseconds(),
			}
			if !firstTokenAt.IsZero() && firstTokenAt.After(startedAt) {
				payload.TTFTMS = firstTokenAt.Sub(startedAt).Milliseconds()
			}
			if resp != nil {
				payload.StopReason = resp.StopReason
				if resp.Usage != nil {
					payload.InputTokens = resp.Usage.InputTokens
					payload.OutputTokens = resp.Usage.OutputTokens
					payload.CacheReadTokens = resp.Usage.CacheReadTokens
					payload.CacheWriteTokens = resp.Usage.CacheWriteTokens
				}
			}
			emit(events.EventModelRequestCompleted, payload)
		},
		OnAssistantText: func(text string) {
			payload := events.TextPayload{Role: protocol.RoleAssistant, Text: text, Thinking: thinkingBuf.String()}
			thinkingBuf.Reset()
			emit(events.EventAssistantMessageComplete, payload)
		},
		OnToolStarted: func(block protocol.Block) {
			emit(events.EventToolCallStarted, events.ToolCallPayload{
				ID:    block.ID,
				Name:  block.Name,
				Input: protocol.ToolUseBlock(block.ID, block.Name, block.Input).Input,
			})
		},
		OnToolFinished: func(tool conversation.ExecutedTool) {
			emit(events.EventToolCallFinished, events.ToolCallPayload{
				ID:            tool.ID,
				Name:          tool.Name,
				Input:         protocol.ToolUseBlock(tool.ID, tool.Name, tool.Input).Input,
				Output:        tool.Output,
				Error:         tool.Error,
				ArtifactPaths: append([]string{}, tool.ArtifactPaths...),
				Code:          tool.Code,
				RecoveryHint:  tool.RecoveryHint,
				TimedOut:      tool.TimedOut,
				DurationMS:    tool.DurationMS,
			})
			if tool.Name == "todo_write" && strings.TrimSpace(tool.Error) == "" {
				emit(events.EventTodoListUpdated, todoListPayload(a.todoMgr.List(), tool.ID, tool.Name))
			}
		},
		OnToolStuck: func(tool conversation.ToolStuckEvent) {
			emit(events.EventWarningRaised, events.NoticePayload{
				Message:      fmt.Sprintf("tool %s has been running for %s; hard timeout is %s", tool.Name, tool.Elapsed, tool.Timeout),
				Code:         "tool_stuck",
				ActorKind:    "tool",
				ActorID:      tool.ID,
				RecoveryHint: fmt.Sprintf("If %s keeps running, the runner will return a model-visible timeout result and continue from available context.", tool.Name),
			})
		},
		OnPhase: func(phase conversation.PhaseEvent) {
			if !opts.EmitRunnerPhases {
				return
			}
			actorKind := strings.TrimSpace(opts.ActorKind)
			if actorKind == "" {
				actorKind = "main"
			}
			actorID := strings.TrimSpace(opts.ActorID)
			if actorID == "" {
				actorID = opts.TurnID
			}
			emit(events.EventRunnerPhaseChanged, events.RunnerPhasePayload{
				RunnerID:     opts.TurnID,
				ActorKind:    actorKind,
				ActorID:      actorID,
				Phase:        phase.Phase,
				Iteration:    phase.Iteration,
				MaxTurns:     maxTurns,
				Model:        phase.Model,
				Message:      phase.Message,
				ToolID:       phase.ToolID,
				ToolName:     phase.ToolName,
				RecoveryHint: phase.RecoveryHint,
				ScopeLabel:   string(a.SandboxScope()),
			})
		},
		DrainInjections: opts.DrainInjections,
		AppendInjectedMessages: func(messages []protocol.Message) {
			for _, msg := range messages {
				a.appendMessage(msg)
			}
			checkpoint()
		},
		AfterTurn: func() {
			if ackRuntime != nil {
				ackRuntime()
				ackRuntime = nil
			}
		},
		StopAfterTools: a.consumeIdleRequest,
		MaxTurns:       maxTurns,
	}.Run(ctx)
	if err == nil && result != nil && result.Completed {
		if captureErr := a.captureMemoryCandidates(); captureErr != nil {
			emit(events.EventWarningRaised, events.NoticePayload{
				Message: fmt.Sprintf("failed to capture memory candidates: %v", captureErr),
			})
		}
		a.maybeStartBackgroundCompaction(ctx)
	}
	if errors.Is(err, conversation.ErrMaxTurnsReached) {
		err = fmt.Errorf("%w after %d turns", conversation.ErrMaxTurnsReached, maxTurns)
	}
	var pending tools.ErrPermissionPending
	if err != nil && !errors.As(err, &pending) && !errors.Is(err, context.Canceled) {
		payload := events.NoticePayload{Message: err.Error()}
		if errors.Is(err, conversation.ErrMaxTurnsReached) {
			actorKind := strings.TrimSpace(opts.ActorKind)
			if actorKind == "" || actorKind == "main" {
				actorKind = "agent"
			}
			actorID := strings.TrimSpace(opts.ActorID)
			if actorID == "" {
				actorID = opts.TurnID
			}
			payload.Code = "max_turns_reached"
			payload.ActorKind = actorKind
			payload.ActorID = actorID
			payload.Iteration = maxTurns
			payload.MaxTurns = maxTurns
			if result != nil {
				payload.Iteration = result.Turns
				payload.RecoveryHint = result.RecoveryHint
			}
		}
		emit(events.EventErrorRaised, payload)
	}
	return err
}

func (a *Agent) usageContext(runtimeCtx automation.SessionContext, sessionID, turnID, jobID string) conversation.UsageContext {
	source := strings.ToLower(strings.TrimSpace(firstNonEmpty(runtimeCtx.Source, runtimeCtx.LocatorChannel)))
	if source == "" {
		source = "unknown"
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = runtimeCtx.SessionID
	}
	return conversation.UsageContext{
		APIKeyID:        "system:" + source,
		SourceChannel:   source,
		SessionID:       strings.TrimSpace(sessionID),
		TurnID:          strings.TrimSpace(turnID),
		JobID:           strings.TrimSpace(jobID),
		TargetProfileID: strings.TrimSpace(a.cfg.DefaultProfileID),
		TargetModel:     strings.TrimSpace(a.cfg.Model),
		CreditWeight:    1,
	}
}

func (a *Agent) maxTurns() int {
	if a != nil && a.cfg != nil && a.cfg.MaxTurns > 0 {
		return a.cfg.MaxTurns
	}
	return 1000
}

func (a *Agent) toolTimeout() time.Duration {
	if a != nil && a.cfg != nil && a.cfg.Tools.Execution.ToolTimeoutSeconds > 0 {
		return time.Duration(a.cfg.Tools.Execution.ToolTimeoutSeconds) * time.Second
	}
	return 30 * time.Minute
}

func clonePendingResumeState(input *PendingResumeState) *PendingResumeState {
	if input == nil {
		return nil
	}
	cloned := *input
	cloned.Envelope = input.Envelope.Normalized()
	if len(input.Injections) > 0 {
		cloned.Injections = make([]message.Envelope, 0, len(input.Injections))
		for _, item := range input.Injections {
			cloned.Injections = append(cloned.Injections, item.Normalized())
		}
	}
	cloned.RuntimeContext = input.RuntimeContext.Clone()
	return &cloned
}

func cloneExpandedSections(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

// DefaultSkillLoadResult summarizes default skill activation for a fresh
// session. Missing skills are configuration diagnostics, not runtime noise.
type DefaultSkillLoadResult struct {
	Loaded  []string
	Missing []string
	Failed  map[string]string
}

func loadDefaultSkills(agent *Agent) DefaultSkillLoadResult {
	if agent == nil || agent.cfg == nil {
		return DefaultSkillLoadResult{}
	}
	return loadSkillNames(agent, agent.cfg.DefaultSkills)
}

// loadSkillNames activates an explicit list of skill names into the agent,
// tolerating missing skills as diagnostics rather than hard failures.
func loadSkillNames(agent *Agent, names []string) DefaultSkillLoadResult {
	result := DefaultSkillLoadResult{}
	for _, skillName := range names {
		skillName = strings.TrimSpace(skillName)
		if skillName == "" {
			continue
		}
		if err := agent.LoadSkill(skillName); err != nil {
			if errors.Is(err, skill.ErrSkillNotFound) {
				result.Missing = append(result.Missing, skillName)
				continue
			}
			if result.Failed == nil {
				result.Failed = map[string]string{}
			}
			result.Failed[skillName] = err.Error()
			continue
		}
		result.Loaded = append(result.Loaded, skillName)
	}
	return result
}

// LoadNamedSkills activates an explicit list of skill names for a fresh
// session, e.g. skills requested at session creation. Missing skills are
// reported in the result rather than failing the session.
func (a *Agent) LoadNamedSkills(names []string) DefaultSkillLoadResult {
	return loadSkillNames(a, names)
}

func todoListPayload(items []todo.Item, sourceToolCallID, sourceToolName string) events.TodoListPayload {
	payload := events.TodoListPayload{
		Items:            make([]events.TodoItemPayload, 0, len(items)),
		Total:            len(items),
		SourceToolCallID: strings.TrimSpace(sourceToolCallID),
		SourceToolName:   strings.TrimSpace(sourceToolName),
	}
	for _, item := range items {
		switch item.Status {
		case todo.StatusCompleted:
			payload.Completed++
		case todo.StatusInProgress:
			payload.InProgress++
		case todo.StatusPending:
			payload.Pending++
		}
		payload.Items = append(payload.Items, events.TodoItemPayload{
			ID:         item.ID,
			Content:    item.Content,
			Status:     string(item.Status),
			ActiveForm: item.ActiveForm,
		})
	}
	return payload
}

// LoadDefaultSkills activates configured default skills for a fresh session.
func (a *Agent) LoadDefaultSkills() DefaultSkillLoadResult {
	return loadDefaultSkills(a)
}

// clampCacheKey truncates a session ID to the 64-char limit required by
// OpenAI prompt_cache_key. Characters beyond 64 are silently dropped.
func clampCacheKey(sessionID string) string {
	const maxCacheKeyLen = 64
	runes := []rune(sessionID)
	if len(runes) <= maxCacheKeyLen {
		return sessionID
	}
	return string(runes[:maxCacheKeyLen])
}
