package teammate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/toolruntime"
)

// Status represents teammate status.
type Status string

const (
	StatusIdle         Status = "idle"
	StatusWorking      Status = "working"
	StatusShutdown     Status = "shutdown"
	StatusShuttingDown Status = "shutting_down"
	StatusShutdowning         = StatusShuttingDown
)

// RuntimeConfig controls teammate loop behavior and prompt defaults.
type RuntimeConfig struct {
	TeamName         string
	WorkLoopLimit    int
	IdlePollInterval time.Duration
	IdleTimeout      time.Duration
}

// Teammate represents an AI teammate.
type Teammate struct {
	Name       string    `json:"name"`
	Role       string    `json:"role"`
	Prompt     string    `json:"prompt"`
	Status     Status    `json:"status"`
	CreatedAt  time.Time `json:"createdAt"`
	LastActive time.Time `json:"lastActive"`
	ctx        context.Context
	cancel     context.CancelFunc
	generation int64
}

// Manager manages teammates.
type Manager struct {
	mu           sync.RWMutex
	teammates    map[string]*Teammate
	taskMgr      *task.Manager
	msgBus       *message.Bus
	teamDir      string
	workspaceDir string
	tooling      *tooling.WorkspaceExecutor
	runtime      RuntimeConfig
	model        string
	client       conversation.Caller
	wakeCh       map[string]chan struct{}
	loopTools    []LoopToolFactory
	defaultTools []LoopToolFactory
}

// IdleSignal lets the injected idle tool update teammate loop state.
type IdleSignal interface {
	SetIdle(bool)
}

// LoopToolContext is the narrow set of teammate state needed by concrete
// loop-tool adapters.
type LoopToolContext struct {
	WorkspaceDir string
	TaskManager  *task.Manager
	IdleSignal   IdleSignal
}

// LoopToolFactory creates one tool instance for a teammate loop.
type LoopToolFactory func(LoopToolContext) toolruntime.Tool

type loopState struct {
	name            string
	role            string
	generation      int64
	runtime         RuntimeConfig
	prompts         conversation.PromptLayers
	messages        []protocol.Message
	runtimeMessages []protocol.Message
	ackRuntime      func()
}

// NewManager creates a new teammate manager.
func NewManager(workspaceDir, teamDir string, taskMgr *task.Manager, msgBus *message.Bus, model string, client conversation.Caller, defaultTools []LoopToolFactory) *Manager {
	absWorkspaceDir, err := filepath.Abs(workspaceDir)
	if err == nil {
		workspaceDir = absWorkspaceDir
	}

	m := &Manager{
		teammates:    make(map[string]*Teammate),
		taskMgr:      taskMgr,
		msgBus:       msgBus,
		teamDir:      teamDir,
		workspaceDir: workspaceDir,
		tooling:      tooling.NewWorkspaceExecutor(workspaceDir),
		runtime:      DefaultRuntimeConfig(),
		model:        model,
		client:       client,
		wakeCh:       make(map[string]chan struct{}),
		loopTools:    cloneLoopToolFactories(defaultTools),
		defaultTools: cloneLoopToolFactories(defaultTools),
	}
	if msgBus != nil {
		msgBus.RegisterNotifier(m.handleMessageNotification)
	}
	m.loadAll()
	return m
}

func (m *Manager) loadAll() {
	configPath := filepath.Join(m.teamDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	var teammates map[string]*Teammate
	if err := json.Unmarshal(data, &teammates); err == nil {
		resume := make([]resumeTeammate, 0, len(teammates))
		for _, teammate := range teammates {
			normalizeLoadedStatus(teammate)
			m.ensureWakeChannelLocked(teammate.Name)
			if teammate.Status != StatusShutdown && m.canResumeLoadedTeammates() {
				ctx, cancel := context.WithCancel(context.Background())
				teammate.ctx = ctx
				teammate.cancel = cancel
				teammate.generation++
				resume = append(resume, resumeTeammate{
					name:       teammate.Name,
					role:       teammate.Role,
					prompt:     teammate.Prompt,
					ctx:        ctx,
					generation: teammate.generation,
				})
			}
		}
		m.teammates = teammates
		_ = m.saveLocked()
		for _, item := range resume {
			go m.teammateLoop(item.name, item.role, item.prompt, item.ctx, item.generation)
		}
	}
}

func (m *Manager) saveLocked() error {
	configPath := filepath.Join(m.teamDir, "config.json")
	data, err := json.MarshalIndent(m.teammates, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

// Spawn creates a new teammate and starts its loop.
func (m *Manager) Spawn(name, role, prompt string) (*Teammate, error) {
	m.mu.Lock()

	teammate, exists := m.teammates[name]
	if exists {
		if teammate.Status != StatusIdle && teammate.Status != StatusShutdown {
			m.mu.Unlock()
			return nil, fmt.Errorf("teammate '%s' is currently %s", name, teammate.Status)
		}
		if teammate.cancel != nil {
			teammate.cancel()
		}
		teammate.Role = role
		teammate.Prompt = prompt
		teammate.Status = StatusWorking
	} else {
		teammate = &Teammate{
			Name:       name,
			Role:       role,
			Prompt:     prompt,
			Status:     StatusWorking,
			CreatedAt:  time.Now(),
			LastActive: time.Now(),
		}
		m.teammates[name] = teammate
	}

	ctx, cancel := context.WithCancel(context.Background())
	teammate.ctx = ctx
	teammate.cancel = cancel
	teammate.LastActive = time.Now()
	teammate.generation++
	generation := teammate.generation
	m.ensureWakeChannelLocked(name)
	snapshot := cloneTeammate(teammate)

	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Unlock()

	go m.teammateLoop(snapshot.Name, snapshot.Role, snapshot.Prompt, ctx, generation)
	return snapshot, nil
}

// Get returns a teammate by name.
func (m *Manager) Get(name string) (*Teammate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if t, ok := m.teammates[name]; ok {
		return cloneTeammate(t), nil
	}
	return nil, fmt.Errorf("teammate not found: %s", name)
}

// List returns all teammates.
func (m *Manager) List() []*Teammate {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]*Teammate, 0, len(m.teammates))
	for _, t := range m.teammates {
		list = append(list, cloneTeammate(t))
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

// UpdateStatus updates teammate status.
func (m *Manager) UpdateStatus(name string, status Status) error {
	m.mu.Lock()
	if err := m.updateStatusLocked(name, status); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	if status == StatusShutdown || status == StatusShuttingDown {
		m.signalWake(name)
	}
	return nil
}

// ShutdownTeammate requests a teammate to shut down.
func (m *Manager) ShutdownTeammate(name string) error {
	return m.UpdateStatus(name, StatusShuttingDown)
}

// Configure updates teammate runtime behavior.
func (m *Manager) Configure(runtime RuntimeConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runtime = normalizeRuntimeConfig(runtime)
}

// SetLoopToolFactories replaces the factories used for future teammate turns.
// Passing an empty slice restores the default tool set.
func (m *Manager) SetLoopToolFactories(factories []LoopToolFactory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(factories) == 0 {
		m.loopTools = cloneLoopToolFactories(m.defaultTools)
		return
	}
	m.loopTools = cloneLoopToolFactories(factories)
}

// teammateLoop runs the teammate's work loop.
func (m *Manager) teammateLoop(name, role, prompt string, ctx context.Context, generation int64) {
	state := newLoopState(name, role, prompt, generation, m.runtimeConfig())

	for {
		if m.shouldStopLoop(ctx, state.name, state.generation) {
			return
		}
		if err := m.updateStatusForGeneration(state.name, state.generation, StatusWorking); err != nil {
			return
		}
		workDone, err := m.runLoopTurn(ctx, state)
		if err != nil {
			m.shutdownLoop(state.name, state.generation)
			return
		}

		if workDone {
			if err := m.updateStatusForGeneration(state.name, state.generation, StatusIdle); err != nil {
				return
			}
		}

		resume, err := m.waitForLoopResume(ctx, state)
		if err != nil {
			return
		}
		if !resume {
			return
		}
	}
}

func newLoopState(name, role, prompt string, generation int64, runtime RuntimeConfig) *loopState {
	sysPrompt := fmt.Sprintf("You are '%s', role: %s, team: %s. Use idle when done with current work. You may auto-claim tasks. Prefer workspace-relative paths for file tools.", name, role, runtime.TeamName)
	return &loopState{
		name:       name,
		role:       role,
		generation: generation,
		runtime:    runtime,
		prompts:    conversation.PromptLayers{Base: sysPrompt},
		messages:   []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, prompt)},
	}
}

func loopIdleChecks(runtime RuntimeConfig) int {
	idleChecks := int(runtime.IdleTimeout / runtime.IdlePollInterval)
	if idleChecks < 1 {
		return 1
	}
	return idleChecks
}

func (m *Manager) shouldStopLoop(ctx context.Context, name string, generation int64) bool {
	select {
	case <-ctx.Done():
		m.shutdownLoop(name, generation)
		return true
	default:
		return false
	}
}

func (m *Manager) shutdownLoop(name string, generation int64) {
	_ = m.updateStatusForGeneration(name, generation, StatusShutdown)
}

func (m *Manager) runLoopTurn(ctx context.Context, state *loopState) (bool, error) {
	loopTools := m.newLoopToolHandler(state.name, state.generation)
	result, err := conversation.Runner{
		Caller: m.client,
		BuildRequest: func(ctx context.Context) (protocol.Request, error) {
			_ = ctx
			return conversation.NewRequest(m.model, 8000, "", state.prompts.Build(), state.combinedMessages(), loopTools.Schemas()), nil
		},
		AppendAssistant: func(msg protocol.Message) {
			state.messages = append(state.messages, msg)
		},
		AppendToolResults: func(msg protocol.Message) {
			state.messages = append(state.messages, msg)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (conversation.ToolExecutionResult, error) {
			output, err := loopTools.Handle(ctx, name, input)
			if err != nil {
				return conversation.ToolExecutionResult{}, err
			}
			return conversation.ToolExecutionResult{Output: output}, nil
		},
		StopAfterTools: func() bool {
			status := m.statusForGeneration(state.name, state.generation)
			return status == StatusIdle || status == StatusShuttingDown
		},
		AfterTurn: state.afterTurn,
		MaxTurns:  state.runtime.WorkLoopLimit,
	}.Run(ctx)
	if err != nil && !errors.Is(err, conversation.ErrMaxTurnsReached) {
		return false, err
	}
	return result != nil && (result.Completed || m.statusForGeneration(state.name, state.generation) == StatusIdle), nil
}

func (m *Manager) waitForLoopResume(ctx context.Context, state *loopState) (bool, error) {
	checks := loopIdleChecks(state.runtime)
	for i := 0; i < checks; i++ {
		if m.statusForGeneration(state.name, state.generation) == StatusShuttingDown {
			m.shutdownLoop(state.name, state.generation)
			return false, nil
		}
		if m.resumeFromInbox(state) {
			return true, nil
		}
		resume, err := m.tryAutoClaimPendingTask(state)
		if err != nil {
			return false, err
		}
		if resume {
			return true, nil
		}
		if i == checks-1 {
			continue
		}
		if !m.waitForIdleCheck(ctx, state.name, state.runtime.IdlePollInterval) {
			m.shutdownLoop(state.name, state.generation)
			return false, nil
		}
	}
	m.shutdownLoop(state.name, state.generation)
	return false, nil
}

func (m *Manager) resumeFromInbox(state *loopState) bool {
	previewedRuntime, ackInbox, hasInbox := m.consumeInboxMessages(state.name, state.generation)
	if !hasInbox {
		return false
	}
	state.runtimeMessages = previewedRuntime
	state.ackRuntime = ackInbox
	return true
}

func (m *Manager) tryAutoClaimPendingTask(state *loopState) (bool, error) {
	if m.taskMgr == nil {
		return false, nil
	}
	tasks := m.taskMgr.List()
	for _, item := range tasks {
		if item.Status != task.StatusPending {
			continue
		}
		claimed, err := m.taskMgr.ClaimPending(item.ID)
		if err != nil {
			continue
		}
		if err := m.updateStatusForGeneration(state.name, state.generation, StatusWorking); err != nil {
			return false, err
		}
		state.addAutoClaimedTask(claimed)
		return true, nil
	}
	return false, nil
}

func (m *Manager) consumeInboxMessages(name string, generation int64) ([]protocol.Message, func(), bool) {
	if m.msgBus == nil {
		return nil, nil, false
	}

	inboxMsgs := m.msgBus.PeekInbox(name)
	if len(inboxMsgs) == 0 {
		return nil, nil, false
	}

	if err := m.updateStatusForGeneration(name, generation, StatusWorking); err != nil {
		return nil, nil, false
	}

	runtimeMessages := []protocol.Message{protocol.NewEphemeralTextMessage(protocol.KindInbox, formatInboxMessages(inboxMsgs))}
	ack := func() {
		m.msgBus.AckInbox(name, inboxMsgs)
	}
	return runtimeMessages, ack, true
}

func formatInboxMessages(messages []message.Message) string {
	return message.FormatInboxMessages(messages)
}

func formatAutoClaimedTask(t *task.FileTask) string {
	return fmt.Sprintf("Auto-claimed task #%d: %s\n%s", t.ID, t.Subject, t.Description)
}

// DefaultRuntimeConfig returns the default teammate runtime settings.
func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		TeamName:         "default",
		WorkLoopLimit:    50,
		IdlePollInterval: 5 * time.Second,
		IdleTimeout:      60 * time.Second,
	}
}

func normalizeRuntimeConfig(runtime RuntimeConfig) RuntimeConfig {
	defaults := DefaultRuntimeConfig()
	if runtime.TeamName == "" {
		runtime.TeamName = defaults.TeamName
	}
	if runtime.WorkLoopLimit <= 0 {
		runtime.WorkLoopLimit = defaults.WorkLoopLimit
	}
	if runtime.IdlePollInterval <= 0 {
		runtime.IdlePollInterval = defaults.IdlePollInterval
	}
	if runtime.IdleTimeout <= 0 {
		runtime.IdleTimeout = defaults.IdleTimeout
	}
	if runtime.IdleTimeout < runtime.IdlePollInterval {
		runtime.IdleTimeout = runtime.IdlePollInterval
	}
	return runtime
}

type teammateIdleSignal struct {
	manager    *Manager
	name       string
	generation int64
}

func (s teammateIdleSignal) SetIdle(idle bool) {
	status := StatusWorking
	if idle {
		status = StatusIdle
	}
	_ = s.manager.updateStatusForGeneration(s.name, s.generation, status)
}

func (m *Manager) newLoopToolHandler(name string, generation int64) *toolruntime.ToolHandler {
	handler := toolruntime.NewToolHandler()
	context := LoopToolContext{
		WorkspaceDir: m.workspaceDir,
		TaskManager:  m.taskMgr,
		IdleSignal:   teammateIdleSignal{manager: m, name: name, generation: generation},
	}
	for _, factory := range m.loopToolFactories() {
		handler.Register(factory(context))
	}
	return handler
}

func (m *Manager) loopToolFactories() []LoopToolFactory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneLoopToolFactories(m.loopTools)
}

func (m *Manager) runtimeConfig() RuntimeConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.runtime
}

func (m *Manager) updateStatusLocked(name string, status Status) error {
	t, ok := m.teammates[name]
	if !ok {
		return fmt.Errorf("teammate not found: %s", name)
	}
	t.Status = status
	t.LastActive = time.Now()
	if status == StatusShutdown && t.cancel != nil {
		t.cancel()
	}
	return m.saveLocked()
}

func (m *Manager) updateStatusForGeneration(name string, generation int64, status Status) error {
	m.mu.Lock()
	t, ok := m.teammates[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("teammate not found: %s", name)
	}
	if t.generation != generation {
		m.mu.Unlock()
		return nil
	}
	if err := m.updateStatusLocked(name, status); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	if status == StatusShutdown || status == StatusShuttingDown {
		m.signalWake(name)
	}
	return nil
}

func (m *Manager) statusForGeneration(name string, generation int64) Status {
	m.mu.RLock()
	defer m.mu.RUnlock()

	t, ok := m.teammates[name]
	if !ok || t.generation != generation {
		return StatusShutdown
	}
	return t.Status
}

func (m *Manager) ensureWakeChannelLocked(name string) chan struct{} {
	if ch, ok := m.wakeCh[name]; ok {
		return ch
	}
	ch := make(chan struct{}, 1)
	m.wakeCh[name] = ch
	return ch
}

func (m *Manager) signalWake(name string) {
	m.mu.Lock()
	ch := m.ensureWakeChannelLocked(name)
	m.mu.Unlock()

	select {
	case ch <- struct{}{}:
	default:
	}
}

func (m *Manager) waitForIdleCheck(ctx context.Context, name string, pollInterval time.Duration) bool {
	m.mu.Lock()
	ch := m.ensureWakeChannelLocked(name)
	m.mu.Unlock()

	timer := time.NewTimer(pollInterval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-ch:
		return true
	case <-timer.C:
		return true
	}
}

func (m *Manager) handleMessageNotification(msg message.Message) {
	if msg.To == "" {
		return
	}

	m.mu.RLock()
	_, ok := m.teammates[msg.To]
	m.mu.RUnlock()
	if ok {
		m.signalWake(msg.To)
	}
}

func cloneTeammate(t *Teammate) *Teammate {
	if t == nil {
		return nil
	}

	return &Teammate{
		Name:       t.Name,
		Role:       t.Role,
		Prompt:     t.Prompt,
		Status:     t.Status,
		CreatedAt:  t.CreatedAt,
		LastActive: t.LastActive,
	}
}

type resumeTeammate struct {
	name       string
	role       string
	prompt     string
	ctx        context.Context
	generation int64
}

func (s *loopState) combinedMessages() []protocol.Message {
	combined := append(protocol.CloneMessages(s.messages), protocol.CloneMessages(s.runtimeMessages)...)
	return combined
}

func (s *loopState) afterTurn() {
	if s.ackRuntime != nil {
		s.ackRuntime()
		s.ackRuntime = nil
	}
	s.runtimeMessages = nil
}

func (s *loopState) addAutoClaimedTask(claimed *task.FileTask) {
	if len(s.messages) <= 3 {
		s.messages = append(s.messages, protocol.NewTextMessage(protocol.RoleUser, fmt.Sprintf("Reminder: you are '%s', role: %s, team: %s.", s.name, s.role, s.runtime.TeamName)))
	}
	s.messages = append(s.messages, protocol.NewTextMessage(protocol.RoleUser, formatAutoClaimedTask(claimed)))
}

func normalizeLoadedStatus(teammate *Teammate) {
	if teammate == nil {
		return
	}
	if teammate.Status == Status("shutdowning") {
		teammate.Status = StatusShuttingDown
	}
	switch teammate.Status {
	case "":
		teammate.Status = StatusIdle
	case StatusWorking:
		teammate.Status = StatusIdle
	case StatusShuttingDown:
		teammate.Status = StatusShutdown
	}
}

func (m *Manager) canResumeLoadedTeammates() bool {
	return strings.TrimSpace(m.model) != "" && m.client != nil
}

func cloneLoopToolFactories(factories []LoopToolFactory) []LoopToolFactory {
	return append([]LoopToolFactory(nil), factories...)
}
