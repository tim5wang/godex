package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/services/sessionadmin"
	"github.com/tim5wang/godex/internal/tools"
)

// SharedDependencies holds workspace-scoped services that can be reused across
// multiple session-scoped agents.
type SharedDependencies struct {
	mu   sync.RWMutex
	deps dependencies
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

// NewWithSharedDependencies creates a session-scoped agent on top of shared workspace services.
func NewWithSharedDependencies(cfg *config.Config, shared *SharedDependencies) *Agent {
	if shared == nil {
		shared = NewSharedDependencies(cfg)
	}
	return newAgentWithDependencies(cfg, shared.snapshot())
}

// ApplyConfig swaps the agent runtime dependencies to a fresh config snapshot.
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
	a.cfg = cfg
	a.taskMgr = deps.taskMgr
	a.msgBus = deps.msgBus
	a.client = deps.client
	a.skillLoader = deps.skillLoader
	a.instrLoader = deps.instrLoader
	a.memoryMgr = deps.memoryMgr
	a.memoryExt = deps.memoryExt
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
	a.sandbox = deps.sandbox
	if a.sandbox == nil {
		a.sandbox = localSandboxFromConfig(cfg)
	}
	a.mu.Unlock()

	nextHandler := tools.NewToolHandler()
	nextHandler.AddBeforeInterceptors(tools.NewPermissionInterceptorWithReview(a.permissions, a.reviewPermissionRequest))
	a.registerToolsWith(nextHandler)
	nextHandler.SetActiveBundles(activeBundles...)
	handler.ReplaceWith(nextHandler)
	// tool_exchange mutates the handler it is constructed with. Rebind it to
	// the stable session handler after registry replacement, not the temporary
	// rebuild handler.
	a.registerToolTo(handler, tools.NewToolExchangeTool(handler), tools.ToolMeta{AlwaysActive: true})
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

	return SessionState{
		Messages:             protocol.CloneMessages(a.messages),
		TranscriptRefs:       append([]string{}, a.transcriptRefs...),
		ActiveSkills:         skills,
		ActiveBundles:        append([]string{}, a.toolHandler.Catalog().ActiveBundles...),
		PermissionState:      exportPermissionState(a.permissions, sessionID),
		PendingResume:        clonePendingResumeState(a.pendingResume),
		HistoryVersion:       a.historyVersion,
		LastCompactedVersion: a.lastCompactedVersion,
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
	ctx = tools.WithSessionID(ctx, opts.SessionID)
	ctx = tools.WithSessionContext(ctx, opts.RuntimeContext)
	ctx = withHistoryRecallTurnState(ctx)
	a.resetIdle()
	var ackRuntime func()
	sink := opts.Sink
	if sink == nil {
		sink = events.NopSink
	}
	ctx = withSubagentEventTarget(ctx, subagentEventTarget{
		sessionID: opts.SessionID,
		turnID:    opts.TurnID,
		sink:      sink,
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
	result, err := conversation.Runner{
		Caller: a.client,
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
			return conversation.NewRequestFromAPIMessages(a.cfg.Model, a.cfg.MaxTokens, a.cfg.ReasoningEffort, build.System, apiMessages, build.ToolSchemas), nil
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
		OnAssistantText: func(text string) {
			payload := events.TextPayload{Role: protocol.RoleAssistant, Text: text}
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

func (a *Agent) maxTurns() int {
	if a != nil && a.cfg != nil && a.cfg.MaxTurns > 0 {
		return a.cfg.MaxTurns
	}
	return 1000
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
	result := DefaultSkillLoadResult{}
	if agent == nil || agent.cfg == nil {
		return result
	}
	for _, skillName := range agent.cfg.DefaultSkills {
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
