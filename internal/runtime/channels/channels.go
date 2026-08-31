package channels

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/security"
	"github.com/tim5wang/godex/internal/platform/logger"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/tools"
)

const (
	defaultInboundDedupTTL = 15 * time.Minute
	defaultSlowAckDelay    = 3 * time.Second
	defaultDeliveryRetries = 3
	defaultDeliveryDelay   = 100 * time.Millisecond
	maxDeliveryRecords     = 20


	StateDisabled   = "disabled"
	StateStarting   = "starting"
	StateRunning    = "running"
	StateRestarting = "restarting"
	StateStopped    = "stopped"
	StateError      = "error"

	DeliveryStatusRetrying  = "retrying"
	DeliveryStatusDelivered = "delivered"
	DeliveryStatusFailed    = "failed"

	AccessActionAllow            = "allow"
	AccessActionDeny             = "deny"
	AccessActionApprovalRequired = "approval_required"
)

const (
	MetadataChannelID   = "channel_id"
	MetadataPlatformID  = "platform_id"
	MetadataThreadID    = "thread_id"
	MetadataSenderID    = "sender_id"
	MetadataSessionMode = "session_mode"

	SessionModeShared      = "shared"
	SessionModePerThread   = "per-thread"
	SessionModeAgentShared = "agent-shared"
)

// Backend is the shared runtime backend surface used by channels.
type Backend interface {
	OpenSession(context.Context, rtbackend.SessionLocator) (*rtbackend.OpenedSession, error)
	Snapshot(context.Context, string) (rtbackend.Snapshot, error)
	Submit(context.Context, string, message.Envelope) (*rtbackend.SubmitResult, error)
	PostRuntimeReply(context.Context, string, string) error
	PostRuntimeReplyWithArtifactPaths(context.Context, string, string, []string) error
	StoreAttachment(context.Context, string, rtbackend.AttachmentUpload) (message.AttachmentRef, error)
	ExecuteCommand(context.Context, string, commands.Command) (commands.Result, error)
	PendingPermissions(context.Context, string) ([]tools.PendingPermission, error)
	AttachSink(string, events.Sink) (func(), error)
}

type securityAuditor interface {
	AppendSecurityEvent(security.SecurityEvent)
}

// Channel is one external chat platform adapter.
type Channel interface {
	Name() string
	Start(context.Context) error
	Stop(context.Context) error
}

// CapabilityProvider describes optional adapter features without changing the
// core lifecycle contract.
type CapabilityProvider interface {
	Capabilities() ChannelCapabilities
}

// ChannelCapabilities is the platform-neutral v1 capability summary exposed by
// a channel adapter.
type ChannelCapabilities struct {
	Delivery     bool     `json:"delivery,omitempty"`
	AuthLogin    bool     `json:"auth_login,omitempty"`
	Media        bool     `json:"media,omitempty"`
	Streaming    bool     `json:"streaming,omitempty"`
	Typing       bool     `json:"typing,omitempty"`
	Status       bool     `json:"status,omitempty"`
	AllowFrom    bool     `json:"allow_from,omitempty"`
	SessionModes []string `json:"session_modes,omitempty"`
}

// Factory creates a concrete channel implementation from config.
type Factory interface {
	Name() string
	Enabled(*config.Config) bool
	Build(*config.Config, *Manager) (Channel, error)
}

// SchemaProvider optionally describes a channel config section for editor UIs.
type SchemaProvider interface {
	ConfigSchema() config.SectionSchema
}

// ReplySender delivers one final aggregated platform reply.
type ReplySender interface {
	SendText(context.Context, string) error
}

// AckSender optionally emits a lightweight "working on it" acknowledgement.
type AckSender interface {
	SendAck(context.Context) error
}

// Deliverer optionally supports proactive delivery outside an inbound turn.
type Deliverer interface {
	Deliver(context.Context, automation.DeliveryTarget, ReplyPlan) error
}

// RoutingIdentity captures the stable external source of one inbound message.
// SessionKey remains the compatibility key; routing fields are used for richer
// diagnostics and for deriving a key when an adapter does not provide one.
type RoutingIdentity struct {
	ChannelID   string `json:"channel_id,omitempty"`
	PlatformID  string `json:"platform_id,omitempty"`
	ThreadID    string `json:"thread_id,omitempty"`
	SenderID    string `json:"sender_id,omitempty"`
	SessionMode string `json:"session_mode,omitempty"`
}

// InboundMessage is the normalized platform-to-backend input shape.
type InboundMessage struct {
	Channel     string
	SessionKey  string
	Sender      string
	Text        string
	Attachments []message.AttachmentRef
	Metadata    map[string]string
	Source      message.EnvelopeSource
	Routing     RoutingIdentity
}

// ChannelStatus captures one runtime channel health snapshot.
type ChannelStatus struct {
	Name          string               `json:"name"`
	Enabled       bool                 `json:"enabled"`
	Running       bool                 `json:"running"`
	State         string               `json:"state,omitempty"`
	Detail        string               `json:"detail,omitempty"`
	UpdatedAt     time.Time            `json:"updated_at"`
	LastStartAt   time.Time            `json:"last_start_at,omitempty"`
	LastStopAt    time.Time            `json:"last_stop_at,omitempty"`
	LastPollAt    time.Time            `json:"last_poll_at,omitempty"`
	LastInboundAt time.Time            `json:"last_inbound_at,omitempty"`
	LastAckAt     time.Time            `json:"last_ack_at,omitempty"`
	LastReplyAt   time.Time            `json:"last_reply_at,omitempty"`
	LastDuplicate time.Time            `json:"last_duplicate_at,omitempty"`
	LastError     string               `json:"last_error,omitempty"`
	LastEvent     string               `json:"last_event,omitempty"`
	LastDelivery  *DeliveryRecord      `json:"last_delivery,omitempty"`
	LastAccess    *AccessDecision      `json:"last_access,omitempty"`
	Capabilities  *ChannelCapabilities `json:"capabilities,omitempty"`
}

// AccessDecision is the audited outcome of the channel sender gate.
type AccessDecision struct {
	Action     string    `json:"action"`
	Reason     string    `json:"reason,omitempty"`
	Channel    string    `json:"channel,omitempty"`
	SenderID   string    `json:"sender_id,omitempty"`
	PlatformID string    `json:"platform_id,omitempty"`
	ThreadID   string    `json:"thread_id,omitempty"`
	DecidedAt  time.Time `json:"decided_at"`
}

// DeliveryRecord captures the latest known status for one outbound attempt.
type DeliveryRecord struct {
	ID          string    `json:"id"`
	TargetKind  string    `json:"target_kind,omitempty"`
	Channel     string    `json:"channel,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	LastError   string    `json:"last_error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	DeliveredAt time.Time `json:"delivered_at,omitempty"`
	FailedAt    time.Time `json:"failed_at,omitempty"`
}

// ChannelStatusUpdate mutates one status record.
type ChannelStatusUpdate struct {
	Enabled     *bool
	Running     *bool
	State       string
	Detail      string
	LastError   string
	ClearError  bool
	LastEvent   string
	MarkStart   bool
	MarkStop    bool
	MarkPoll    bool
	MarkInbound bool
	MarkAck     bool
	MarkReply   bool
	MarkDup     bool
}

// StatusReport is the runtime-facing channel health view.
type StatusReport struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Channels    []ChannelStatus  `json:"channels"`
	Deliveries  []DeliveryRecord `json:"deliveries,omitempty"`
}

// Manager owns channel factories, lifecycle, and backend routing.
type Manager struct {
	cfg     *config.Config
	backend Backend

	mu        sync.Mutex
	factories []Factory
	channels  map[string]Channel
	statuses  map[string]ChannelStatus
	active    bool

	recentInbound   map[string]time.Time
	inboundDedupTTL time.Duration
	slowAckDelay    time.Duration
	deliverySeq     atomic.Uint64
	deliveryDelay   time.Duration
	deliveries      []DeliveryRecord
}

var channelsLog = logger.New("channels")

// NewManager creates a new channel manager on top of the shared backend.
func NewManager(cfg *config.Config, backend Backend) *Manager {
	return &Manager{
		cfg:             cfg,
		backend:         backend,
		channels:        make(map[string]Channel),
		statuses:        make(map[string]ChannelStatus),
		recentInbound:   make(map[string]time.Time),
		inboundDedupTTL: defaultInboundDedupTTL,
		slowAckDelay:    defaultSlowAckDelay,
		deliveryDelay:   defaultDeliveryDelay,
	}
}

// RegisterFactory adds a channel factory to the startup roster.
func (m *Manager) RegisterFactory(factory Factory) {
	if factory == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.factories = append(m.factories, factory)
	status := m.statuses[factory.Name()]
	status.Name = factory.Name()
	status.Enabled = factory.Enabled(m.cfg)
	if status.Enabled {
		status.State = StateStopped
	} else if status.State == "" {
		status.State = StateDisabled
	}
	status.UpdatedAt = time.Now()
	m.statuses[factory.Name()] = status
}

func (m *Manager) Start(ctx context.Context) error {
	return m.StartAll(ctx)
}

func (m *Manager) Stop(ctx context.Context) error {
	return m.StopAll(ctx)
}

// StartAll builds and starts all enabled channels.
func (m *Manager) StartAll(ctx context.Context) error {
	m.mu.Lock()
	factories := append([]Factory{}, m.factories...)
	m.active = true
	m.mu.Unlock()

	for _, factory := range factories {
		enabled := factory.Enabled(m.cfg)
		m.SetStatus(factory.Name(), ChannelStatusUpdate{
			Enabled: boolPtr(enabled),
			Running: boolPtr(false),
			State:   ternary(enabled, StateStarting, StateDisabled),
			Detail:  "",
		})
		if !enabled {
			channelsLog.Infof("channel %s is disabled in config; skipping start", factory.Name())
			continue
		}
		channelsLog.Infof("starting channel %s", factory.Name())
		channel, err := factory.Build(m.cfg, m)
		if err != nil {
			m.SetStatus(factory.Name(), ChannelStatusUpdate{
				Running:   boolPtr(false),
				State:     StateError,
				LastError: err.Error(),
				Detail:    "build failed",
			})
			channelsLog.Errorf("build channel %s: %v", factory.Name(), err)
			return fmt.Errorf("build channel %s: %w", factory.Name(), err)
		}
		if err := channel.Start(ctx); err != nil {
			m.SetStatus(factory.Name(), ChannelStatusUpdate{
				Running:   boolPtr(false),
				State:     StateError,
				LastError: err.Error(),
				Detail:    "start failed",
			})
			channelsLog.Errorf("start channel %s: %v", factory.Name(), err)
			return fmt.Errorf("start channel %s: %w", factory.Name(), err)
		}
		m.mu.Lock()
		m.channels[channel.Name()] = channel
		m.mu.Unlock()
		m.SetStatus(channel.Name(), ChannelStatusUpdate{
			Enabled:    boolPtr(true),
			Running:    boolPtr(true),
			State:      StateRunning,
			Detail:     "started",
			ClearError: true,
			MarkStart:  true,
		})
		channelsLog.Infof("channel %s started", channel.Name())
	}
	return nil
}

// StopAll stops all started channels.
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	started := make([]Channel, 0, len(m.channels))
	for _, channel := range m.channels {
		started = append(started, channel)
	}
	m.channels = make(map[string]Channel)
	m.mu.Unlock()

	for _, channel := range started {
		channelsLog.Infof("stopping channel %s", channel.Name())
		if err := channel.Stop(ctx); err != nil {
			m.SetStatus(channel.Name(), ChannelStatusUpdate{
				Running:   boolPtr(false),
				State:     StateError,
				LastError: err.Error(),
				Detail:    "stop failed",
				MarkStop:  true,
			})
			channelsLog.Errorf("stop channel %s: %v", channel.Name(), err)
			return err
		}
		m.SetStatus(channel.Name(), ChannelStatusUpdate{
			Running:    boolPtr(false),
			State:      StateStopped,
			Detail:     "stopped",
			ClearError: true,
			MarkStop:   true,
		})
	}
	return nil
}

// Reconcile incrementally swaps channel implementations and rolls back any
// failed restart to preserve the last healthy runtime.
func (m *Manager) Reconcile(ctx context.Context, cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("missing config")
	}
	channelsLog.Infof("reconciling channel runtime")

	m.mu.Lock()
	oldCfg := m.cfg
	factories := append([]Factory{}, m.factories...)
	active := m.active
	m.mu.Unlock()

	if !active {
		m.mu.Lock()
		m.cfg = cfg
		for _, factory := range factories {
			status := m.statuses[factory.Name()]
			status.Name = factory.Name()
			status.Enabled = factory.Enabled(cfg)
			status.UpdatedAt = time.Now()
			if status.Enabled {
				status.Running = false
				status.State = StateStopped
			} else {
				status.Running = false
				status.State = StateDisabled
				status.Detail = ""
			}
			m.statuses[factory.Name()] = status
		}
		m.mu.Unlock()
		return nil
	}

	for _, factory := range factories {
		name := factory.Name()
		enabled := factory.Enabled(cfg)
		current := m.currentChannel(name)

		if !enabled {
			if current != nil {
				channelsLog.Infof("stopping disabled channel %s during reconcile", name)
				if err := current.Stop(ctx); err != nil {
					m.SetStatus(name, ChannelStatusUpdate{
						Enabled:   boolPtr(true),
						Running:   boolPtr(false),
						State:     StateError,
						Detail:    "failed to stop disabled channel",
						LastError: err.Error(),
					})
					return fmt.Errorf("stop channel %s: %w", name, err)
				}
				m.deleteChannel(name)
			}
			m.SetStatus(name, ChannelStatusUpdate{
				Enabled:    boolPtr(false),
				Running:    boolPtr(false),
				State:      StateDisabled,
				Detail:     "",
				ClearError: true,
				MarkStop:   true,
			})
			continue
		}

		if current == nil {
			m.SetStatus(name, ChannelStatusUpdate{
				Enabled:    boolPtr(true),
				Running:    boolPtr(false),
				State:      StateStarting,
				Detail:     "starting channel with updated configuration",
				ClearError: true,
			})
			candidate, err := factory.Build(cfg, m)
			if err != nil {
				m.SetStatus(name, ChannelStatusUpdate{
					Enabled:   boolPtr(true),
					Running:   boolPtr(false),
					State:     StateError,
					Detail:    "build failed during reconcile",
					LastError: err.Error(),
				})
				return fmt.Errorf("build channel %s: %w", name, err)
			}
			if err := candidate.Start(ctx); err != nil {
				m.SetStatus(name, ChannelStatusUpdate{
					Enabled:   boolPtr(true),
					Running:   boolPtr(false),
					State:     StateError,
					Detail:    "start failed during reconcile",
					LastError: err.Error(),
				})
				return fmt.Errorf("start channel %s: %w", name, err)
			}
			m.setChannel(name, candidate)
			m.SetStatus(name, ChannelStatusUpdate{
				Enabled:    boolPtr(true),
				Running:    boolPtr(true),
				State:      StateRunning,
				Detail:     "started with updated configuration",
				ClearError: true,
				MarkStart:  true,
			})
			continue
		}

		m.SetStatus(name, ChannelStatusUpdate{
			Enabled:    boolPtr(true),
			Running:    boolPtr(true),
			State:      StateRestarting,
			Detail:     "restarting with updated configuration",
			ClearError: true,
		})
		candidate, err := factory.Build(cfg, m)
		if err != nil {
			m.SetStatus(name, ChannelStatusUpdate{
				Enabled:    boolPtr(true),
				Running:    boolPtr(true),
				State:      StateRunning,
				Detail:     "kept previous configuration after reconcile build failed",
				ClearError: true,
				LastEvent:  "reconcile_rollback",
			})
			return fmt.Errorf("build channel %s: %w", name, err)
		}
		if err := current.Stop(ctx); err != nil {
			m.SetStatus(name, ChannelStatusUpdate{
				Enabled:   boolPtr(true),
				Running:   boolPtr(false),
				State:     StateError,
				Detail:    "failed to stop channel during reconcile",
				LastError: err.Error(),
			})
			return fmt.Errorf("stop channel %s: %w", name, err)
		}
		if err := candidate.Start(ctx); err != nil {
			restored, restoreErr := factory.Build(oldCfg, m)
			if restoreErr == nil {
				restoreErr = restored.Start(ctx)
			}
			if restoreErr != nil {
				m.deleteChannel(name)
				m.SetStatus(name, ChannelStatusUpdate{
					Enabled:   boolPtr(true),
					Running:   boolPtr(false),
					State:     StateError,
					Detail:    "restart failed and rollback could not recover previous channel",
					LastError: fmt.Sprintf("restart error: %v; rollback error: %v", err, restoreErr),
				})
				return fmt.Errorf("start channel %s: %w (rollback failed: %v)", name, err, restoreErr)
			}
			m.setChannel(name, restored)
			m.SetStatus(name, ChannelStatusUpdate{
				Enabled:    boolPtr(true),
				Running:    boolPtr(true),
				State:      StateRunning,
				Detail:     "rolled back to previous configuration after restart failure",
				ClearError: true,
				LastEvent:  "reconcile_rollback",
			})
			return fmt.Errorf("start channel %s: %w", name, err)
		}
		m.setChannel(name, candidate)
		m.SetStatus(name, ChannelStatusUpdate{
			Enabled:    boolPtr(true),
			Running:    boolPtr(true),
			State:      StateRunning,
			Detail:     "running with updated configuration",
			ClearError: true,
			MarkStart:  true,
		})
	}

	m.mu.Lock()
	m.cfg = cfg
	for _, factory := range factories {
		status := m.statuses[factory.Name()]
		status.Name = factory.Name()
		status.Enabled = factory.Enabled(cfg)
		status.UpdatedAt = time.Now()
		if !status.Enabled {
			status.Running = false
			status.State = StateDisabled
			status.Detail = ""
		}
		m.statuses[factory.Name()] = status
	}
	m.mu.Unlock()
	return nil
}

// RunningNames returns the currently started channel names.
func (m *Manager) RunningNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.channels))
	for name := range m.channels {
		names = append(names, name)
	}
	return names
}

// SetStatus applies one runtime status update for a channel.
func (m *Manager) SetStatus(name string, update ChannelStatusUpdate) {
	if strings.TrimSpace(name) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	status := m.statuses[name]
	status.Name = name
	now := time.Now()
	if update.Enabled != nil {
		status.Enabled = *update.Enabled
	}
	if update.Running != nil {
		status.Running = *update.Running
	}
	if update.State != "" {
		status.State = update.State
	}
	if update.Detail != "" || update.State == StateDisabled {
		status.Detail = update.Detail
	}
	if update.ClearError {
		status.LastError = ""
	}
	if update.LastError != "" {
		status.LastError = update.LastError
	}
	if update.LastEvent != "" {
		status.LastEvent = update.LastEvent
	}
	if update.MarkStart {
		status.LastStartAt = now
	}
	if update.MarkStop {
		status.LastStopAt = now
	}
	if update.MarkPoll {
		status.LastPollAt = now
	}
	if update.MarkInbound {
		status.LastInboundAt = now
	}
	if update.MarkAck {
		status.LastAckAt = now
	}
	if update.MarkReply {
		status.LastReplyAt = now
	}
	if update.MarkDup {
		status.LastDuplicate = now
	}
	status.State = normalizeChannelState(status.State, status.Enabled, status.Running)
	status.UpdatedAt = now
	m.statuses[name] = status
}

// StatusReport returns a stable snapshot of all known channel statuses.
func (m *Manager) StatusReport() StatusReport {
	m.mu.Lock()
	defer m.mu.Unlock()

	report := StatusReport{
		GeneratedAt: time.Now(),
		Channels:    make([]ChannelStatus, 0, len(m.statuses)),
		Deliveries:  append([]DeliveryRecord{}, m.deliveries...),
	}
	for _, status := range m.statuses {
		if channel := m.channels[status.Name]; channel != nil {
			if provider, ok := channel.(CapabilityProvider); ok {
				capabilities := provider.Capabilities()
				status.Capabilities = &capabilities
			}
		}
		report.Channels = append(report.Channels, status)
	}
	sort.Slice(report.Channels, func(i, j int) bool {
		return report.Channels[i].Name < report.Channels[j].Name
	})
	return report
}

func (m *Manager) beginDelivery(target automation.DeliveryTarget) DeliveryRecord {
	now := time.Now()
	record := DeliveryRecord{
		ID:         fmt.Sprintf("delivery-%d", m.deliverySeq.Add(1)),
		TargetKind: strings.TrimSpace(target.Kind),
		Channel:    strings.TrimSpace(target.Channel),
		SessionID:  strings.TrimSpace(target.SessionID),
		Status:     DeliveryStatusRetrying,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if record.TargetKind == "" {
		record.TargetKind = automation.DeliveryKindSession
	}
	m.recordDelivery(record)
	return record
}

func (m *Manager) recordDelivery(record DeliveryRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now()
	}
	updated := false
	for i := range m.deliveries {
		if m.deliveries[i].ID == record.ID {
			m.deliveries[i] = record
			updated = true
			break
		}
	}
	if !updated {
		m.deliveries = append(m.deliveries, record)
		if len(m.deliveries) > maxDeliveryRecords {
			m.deliveries = append([]DeliveryRecord{}, m.deliveries[len(m.deliveries)-maxDeliveryRecords:]...)
		}
	}
	if record.Channel == "" {
		return
	}
	status := m.statuses[record.Channel]
	status.Name = record.Channel
	status.LastDelivery = &record
	status.UpdatedAt = record.UpdatedAt
	if record.Status == DeliveryStatusDelivered {
		status.LastReplyAt = record.DeliveredAt
		status.LastEvent = "delivery_delivered"
	} else if record.Status == DeliveryStatusFailed {
		status.LastError = record.LastError
		status.LastEvent = "delivery_failed"
	}
	m.statuses[record.Channel] = status
}

func (m *Manager) deliverWithTracking(ctx context.Context, target automation.DeliveryTarget, plan ReplyPlan, deliver func(context.Context, ReplyPlan) error) error {
	record := m.beginDelivery(target)
	var lastErr error
	for attempt := 1; attempt <= defaultDeliveryRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			lastErr = err
			break
		}
		lastErr = deliver(ctx, plan)
		record.Attempts = attempt
		record.UpdatedAt = time.Now()
		if lastErr == nil {
			record.Status = DeliveryStatusDelivered
			record.LastError = ""
			record.DeliveredAt = record.UpdatedAt
			m.recordDelivery(record)
			return nil
		}
		record.Status = DeliveryStatusRetrying
		record.LastError = lastErr.Error()
		m.recordDelivery(record)
		if attempt == defaultDeliveryRetries {
			break
		}
		if !sleepDeliveryRetry(ctx, m.deliveryDelay) {
			lastErr = ctx.Err()
			break
		}
	}
	record.Status = DeliveryStatusFailed
	record.UpdatedAt = time.Now()
	record.FailedAt = record.UpdatedAt
	if lastErr != nil {
		record.LastError = lastErr.Error()
	}
	m.recordDelivery(record)
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("delivery failed")
}

func sleepDeliveryRetry(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (m *Manager) checkInboundAccess(inbound InboundMessage) AccessDecision {
	now := time.Now()
	decision := AccessDecision{
		Action:     AccessActionAllow,
		Channel:    inbound.Channel,
		SenderID:   firstNonEmpty(inbound.Routing.SenderID, inbound.Sender),
		PlatformID: inbound.Routing.PlatformID,
		ThreadID:   inbound.Routing.ThreadID,
		DecidedAt:  now,
	}
	if m == nil || m.cfg == nil {
		decision.Reason = "no channel access policy configured"
		return decision
	}
	switch strings.ToLower(strings.TrimSpace(inbound.Channel)) {
	case "weixin":
		if !senderAllowed(m.cfg.Weixin.AllowFrom, decision.SenderID) {
			decision.Action = AccessActionDeny
			decision.Reason = "sender outside channels.weixin.allow_from"
		} else {
			decision.Reason = "sender allowed by channels.weixin.allow_from"
		}
	case "feishu":
		decision.Reason = "no channels.feishu.allow_from policy configured"
	default:
		decision.Reason = "no channel-specific access policy configured"
	}
	return decision
}

func (m *Manager) recordAccessDecision(decision AccessDecision) {
	if decision.DecidedAt.IsZero() {
		decision.DecidedAt = time.Now()
	}
	m.mu.Lock()
	if decision.Channel != "" {
		status := m.statuses[decision.Channel]
		status.Name = decision.Channel
		status.LastAccess = &decision
		status.UpdatedAt = decision.DecidedAt
		if decision.Action == AccessActionDeny {
			status.State = StateRunning
			status.Detail = "sender/channel access denied"
			status.LastEvent = "access_denied"
		}
		m.statuses[decision.Channel] = status
	}
	m.mu.Unlock()

	if decision.Action == AccessActionDeny {
		m.auditAccessDecision(decision)
	}
}

func (m *Manager) auditAccessDecision(decision AccessDecision) {
	auditor, ok := m.backend.(securityAuditor)
	if !ok || auditor == nil {
		return
	}
	auditor.AppendSecurityEvent(security.SecurityEvent{
		At:       decision.DecidedAt,
		Category: "channel_access",
		Action:   decision.Action,
		Severity: "warning",
		Source:   decision.Channel,
		Summary:  decision.Reason,
		Metadata: map[string]string{
			"channel":     decision.Channel,
			"sender_id":   decision.SenderID,
			"platform_id": decision.PlatformID,
			"thread_id":   decision.ThreadID,
		},
	})
}

func senderAllowed(allow []string, sender string) bool {
	sender = strings.TrimSpace(sender)
	if sender == "" {
		return false
	}
	if len(allow) == 0 {
		return true
	}
	for _, item := range allow {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if item == "*" || item == sender {
			return true
		}
	}
	return false
}

// StatusText renders a concise human-readable runtime channel summary.
func (m *Manager) StatusText() string {
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	if !active {
		return "Channel runtime is not active in this process."
	}
	report := m.StatusReport()
	if len(report.Channels) == 0 {
		return "No channel factories are registered."
	}
	lines := []string{"Channel runtime status:"}
	for _, status := range report.Channels {
		line := fmt.Sprintf("- %s: enabled=%t running=%t state=%s", status.Name, status.Enabled, status.Running, defaultString(status.State, "unknown"))
		if status.Detail != "" {
			line += " (" + status.Detail + ")"
		}
		lines = append(lines, line)
		if !status.LastInboundAt.IsZero() {
			lines = append(lines, "  last inbound: "+status.LastInboundAt.Format(time.RFC3339))
		}
		if !status.LastPollAt.IsZero() {
			lines = append(lines, "  last poll: "+status.LastPollAt.Format(time.RFC3339))
		}
		if !status.LastAckAt.IsZero() {
			lines = append(lines, "  last ack: "+status.LastAckAt.Format(time.RFC3339))
		}
		if !status.LastReplyAt.IsZero() {
			lines = append(lines, "  last reply: "+status.LastReplyAt.Format(time.RFC3339))
		}
		if !status.LastDuplicate.IsZero() {
			lines = append(lines, "  last duplicate: "+status.LastDuplicate.Format(time.RFC3339))
		}
		if status.LastEvent != "" {
			lines = append(lines, "  last event: "+status.LastEvent)
		}
		if !status.UpdatedAt.IsZero() {
			lines = append(lines, "  updated: "+status.UpdatedAt.Format(time.RFC3339))
		}
		if status.LastError != "" {
			lines = append(lines, "  last error: "+status.LastError)
		}
	}
	return strings.Join(lines, "\n")
}

// AugmentDoctor appends runtime channel diagnostics onto the base config doctor report.
func (m *Manager) AugmentDoctor(report config.DoctorReport) config.DoctorReport {
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	if !active {
		return report
	}
	for _, status := range m.StatusReport().Channels {
		if !status.Enabled {
			continue
		}
		path := "runtime.channels." + status.Name
		if !status.Running {
			check := config.DoctorCheck{
				Severity:   "warning",
				Code:       "channel_not_running_" + status.Name,
				Path:       path,
				Message:    fmt.Sprintf("Channel %s is enabled but not running.", status.Name),
				Suggestion: channelRecoverySuggestion(status.Name, status.State),
			}
			if status.LastError != "" {
				check.Message = fmt.Sprintf("Channel %s is enabled but not running. Last error: %s", status.Name, status.LastError)
			}
			report.Checks = append(report.Checks, check)
			report.Warnings++
			continue
		}
		if status.LastInboundAt.IsZero() {
			report.Checks = append(report.Checks, config.DoctorCheck{
				Severity:   "info",
				Code:       "channel_waiting_inbound_" + status.Name,
				Path:       path,
				Message:    fmt.Sprintf("Channel %s is running but has not seen any inbound messages yet.", status.Name),
				Suggestion: channelIdleSuggestion(status.Name),
			})
			report.Infos++
			continue
		}
		report.Checks = append(report.Checks, config.DoctorCheck{
			Severity: "info",
			Code:     "channel_running_" + status.Name,
			Path:     path,
			Message:  fmt.Sprintf("Channel %s is running. Last inbound: %s.", status.Name, status.LastInboundAt.Format(time.RFC3339)),
		})
		report.Infos++
	}
	sort.Slice(report.Checks, func(i, j int) bool {
		return report.Checks[i].Code < report.Checks[j].Code
	})
	return report
}

// RouteInbound sends a normalized platform message through the shared backend
// and aggregates the final assistant or command result into one platform reply.
func (m *Manager) RouteInbound(ctx context.Context, inbound InboundMessage, reply ReplySender) error {
	inbound = normalizeInbound(inbound)
	if reply == nil {
		return fmt.Errorf("missing reply sender")
	}
	if strings.TrimSpace(inbound.Channel) == "" {
		return fmt.Errorf("missing inbound channel")
	}
	if strings.TrimSpace(inbound.SessionKey) == "" {
		return fmt.Errorf("missing inbound session key")
	}
	m.SetStatus(inbound.Channel, ChannelStatusUpdate{
		LastEvent:   "inbound message",
		Detail:      "processing inbound message",
		State:       StateRunning,
		MarkInbound: true,
	})
	dedupKey, duplicate := m.beginInbound(inbound)
	if duplicate {
		m.SetStatus(inbound.Channel, ChannelStatusUpdate{
			State:     StateRunning,
			Detail:    "duplicate inbound ignored",
			LastEvent: "duplicate inbound",
			MarkDup:   true,
		})
		channelsLog.Infof("ignoring duplicate inbound message from channel=%s session=%s sender=%s", inbound.Channel, inbound.SessionKey, inbound.Sender)
		return nil
	}
	success := false
	defer func() {
		m.finishInbound(dedupKey, success)
	}()
	channelsLog.Infof("routing inbound message from channel=%s session=%s sender=%s text=%t attachments=%d", inbound.Channel, inbound.SessionKey, inbound.Sender, strings.TrimSpace(inbound.Text) != "", len(inbound.Attachments))

	decision := m.checkInboundAccess(inbound)
	m.recordAccessDecision(decision)
	if decision.Action == AccessActionDeny {
		err := automation.NewBlockedError("channel access denied: " + decision.Reason)
		m.SetStatus(inbound.Channel, ChannelStatusUpdate{
			State:       StateRunning,
			LastError:   err.Error(),
			Detail:      "sender/channel access denied",
			LastEvent:   "access_denied",
			MarkInbound: true,
		})
		channelsLog.Infof("denied inbound message from channel=%s sender=%s reason=%s", inbound.Channel, decision.SenderID, decision.Reason)
		return err
	}

	opened, err := m.OpenInboundSession(ctx, inbound)
	if err != nil {
		m.SetStatus(inbound.Channel, ChannelStatusUpdate{
			State:     StateError,
			LastError: err.Error(),
			Detail:    "open inbound session failed",
		})
		channelsLog.Errorf("open inbound session for channel %s: %v", inbound.Channel, err)
		return err
	}

	if cmd, ok := commands.Parse(strings.TrimSpace(inbound.Text)); ok {
		result, err := m.backend.ExecuteCommand(ctx, opened.SessionID, cmd)
		if err != nil {
			m.SetStatus(inbound.Channel, ChannelStatusUpdate{
				State:     StateError,
				LastError: err.Error(),
				Detail:    "command execution failed",
			})
			channelsLog.Errorf("execute inbound command /%s on channel %s: %v", cmd.Name, inbound.Channel, err)
			return reply.SendText(ctx, userFacingError(err))
		}
		output := strings.TrimSpace(result.Output)
		if output == "" {
			output = fmt.Sprintf("/%s completed.", result.Name)
		}
		plan := ReplyPlan{
			Text:    output,
			Command: result.Name,
			Status:  ternary(err == nil, "completed", "error"),
		}
		if strings.TrimSpace(result.ArtifactPath) != "" {
			plan.Artifacts = append(plan.Artifacts, ReplyArtifact{
				Path: result.ArtifactPath,
				Name: filepath.Base(result.ArtifactPath),
			})
		}
		if err := m.deliverWithTracking(ctx, automation.DeliveryTarget{
			Kind:      automation.DeliveryKindChannel,
			Channel:   inbound.Channel,
			SessionID: opened.SessionID,
		}, plan, func(ctx context.Context, plan ReplyPlan) error {
			return sendReply(ctx, reply, plan)
		}); err != nil {
			m.SetStatus(inbound.Channel, ChannelStatusUpdate{
				State:     StateError,
				LastError: err.Error(),
				Detail:    "send command reply failed",
			})
			channelsLog.Errorf("send command reply on channel %s: %v", inbound.Channel, err)
			return err
		}
		m.SetStatus(inbound.Channel, ChannelStatusUpdate{
			State:      StateRunning,
			Detail:     "command reply sent",
			ClearError: true,
			MarkReply:  true,
			LastEvent:  "command reply",
		})
		channelsLog.Infof("command reply sent on channel %s for session %s", inbound.Channel, inbound.SessionKey)
		success = true
		return nil
	}

	collector := &replyCollector{}
	unsubscribe, err := m.backend.AttachSink(opened.SessionID, collector)
	if err != nil {
		return err
	}
	defer unsubscribe()

	stopAck := m.startSlowAck(ctx, inbound.Channel, reply)
	defer stopAck()

	envelope := message.NewTextEnvelope(resolveSource(inbound.Source), opened.SessionID, inbound.Sender, inbound.Text, time.Now())
	envelope.Attachments = append([]message.AttachmentRef{}, inbound.Attachments...)
	envelope.Metadata = metadataWithRouting(inbound.Metadata, inbound.Routing)
	submitResult, err := m.backend.Submit(ctx, opened.SessionID, envelope)
	if err != nil {
		m.SetStatus(inbound.Channel, ChannelStatusUpdate{
			State:     StateError,
			LastError: err.Error(),
			Detail:    "submit failed",
		})
		channelsLog.Errorf("submit inbound message on channel %s: %v", inbound.Channel, err)
		return sendReply(ctx, reply, ReplyPlan{Text: userFacingError(err), Status: "error"})
	}

	plan := collector.Plan()
	snapshot, snapErr := m.backend.Snapshot(ctx, opened.SessionID)
	if snapErr == nil {
		applyCurrentTurnSnapshot(&plan, snapshot.Messages)
	}
	if submitResult != nil && submitResult.PendingApproval {
		plan.Status = "pending_approval"
		if approval, ok := m.pendingApprovalSummary(ctx, opened.SessionID, submitResult.PendingRequestID); ok {
			plan.Approvals = append(plan.Approvals, approval)
		}
		plan.Notices = append(plan.Notices, pendingApprovalNotice(submitResult.PendingRequestID))
		if strings.TrimSpace(plan.Text) == "" {
			plan.Text = "This action is waiting for approval before GoDex can continue."
		}
	}
	if strings.TrimSpace(plan.Text) == "" && len(plan.Notices) == 0 {
		plan.Text = "Completed."
	}
	if strings.TrimSpace(plan.Status) == "" {
		plan.Status = "completed"
	}
	if err := m.deliverWithTracking(ctx, automation.DeliveryTarget{
		Kind:      automation.DeliveryKindChannel,
		Channel:   inbound.Channel,
		SessionID: opened.SessionID,
	}, plan, func(ctx context.Context, plan ReplyPlan) error {
		return sendReply(ctx, reply, plan)
	}); err != nil {
		m.SetStatus(inbound.Channel, ChannelStatusUpdate{
			State:     StateError,
			LastError: err.Error(),
			Detail:    "send assistant reply failed",
		})
		channelsLog.Errorf("send assistant reply on channel %s: %v", inbound.Channel, err)
		return err
	}
	m.SetStatus(inbound.Channel, ChannelStatusUpdate{
		State:      StateRunning,
		Detail:     "assistant reply sent",
		ClearError: true,
		MarkReply:  true,
		LastEvent:  "assistant reply",
	})
	channelsLog.Infof("assistant reply sent on channel %s for session %s", inbound.Channel, inbound.SessionKey)
	success = true
	return nil
}

// OpenInboundSession resolves the stable backend session for one inbound platform message.
func (m *Manager) OpenInboundSession(ctx context.Context, inbound InboundMessage) (*rtbackend.OpenedSession, error) {
	inbound = normalizeInbound(inbound)
	return m.backend.OpenSession(ctx, rtbackend.SessionLocator{
		Channel:  inbound.Channel,
		Key:      inbound.SessionKey,
		UserID:   inbound.Sender,
		Metadata: metadataWithRouting(inbound.Metadata, inbound.Routing),
	})
}

// StoreAttachment persists one attachment into the backend session store.
func (m *Manager) StoreAttachment(ctx context.Context, sessionID string, upload rtbackend.AttachmentUpload) (message.AttachmentRef, error) {
	return m.backend.StoreAttachment(ctx, sessionID, upload)
}

// Deliver proactively sends an automation/background reply to a target session or channel.
func (m *Manager) Deliver(ctx context.Context, target automation.DeliveryTarget, plan ReplyPlan) error {
	target = target.Clone()
	switch strings.TrimSpace(target.Kind) {
	case "", automation.DeliveryKindSession:
		sessionID := strings.TrimSpace(target.SessionID)
		if sessionID == "" {
			return automation.NewBlockedError("session delivery target is missing session_id")
		}
		text := strings.TrimSpace(plan.RenderText())
		if text == "" {
			text = "Completed."
		}
		artifactPaths := make([]string, 0, len(plan.Artifacts))
		for _, artifact := range plan.Artifacts {
			if path := strings.TrimSpace(artifact.Path); path != "" {
				artifactPaths = append(artifactPaths, path)
			}
		}
		return m.deliverWithTracking(ctx, target, plan, func(ctx context.Context, plan ReplyPlan) error {
			_ = plan
			chunks := splitSessionReplyChunks(text, maxSessionReplyChunkRunes)
			for i, chunk := range chunks {
				chunkArtifacts := artifactPaths
				if i > 0 {
					chunkArtifacts = nil // only attach artifacts to the first chunk
				}
				if err := m.backend.PostRuntimeReplyWithArtifactPaths(ctx, sessionID, chunk, chunkArtifacts); err != nil {
					return err
				}
			}
			return nil
		})
	case automation.DeliveryKindChannel:
		channelName := strings.TrimSpace(target.Channel)
		if channelName == "" {
			return automation.NewBlockedError("channel delivery target is missing channel name")
		}
		m.mu.Lock()
		channel := m.channels[channelName]
		m.mu.Unlock()
		if channel == nil {
			return automation.NewBlockedError(fmt.Sprintf("channel %s is not running", channelName))
		}
		deliverer, ok := channel.(Deliverer)
		if !ok {
			return fmt.Errorf("channel %s does not support proactive delivery", channelName)
		}
		if err := m.deliverWithTracking(ctx, target, plan, func(ctx context.Context, plan ReplyPlan) error {
			return deliverer.Deliver(ctx, target, plan)
		}); err != nil {
			var deliveryErr *automation.DeliveryError
			if errors.As(err, &deliveryErr) {
				return err
			}
			return fmt.Errorf("deliver via channel %s: %w", channelName, err)
		}
		return nil
	default:
		return fmt.Errorf("unknown delivery target kind %q", target.Kind)
	}
}

func lastAssistantText(messages []protocol.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != protocol.RoleAssistant {
			continue
		}
		if text := strings.TrimSpace(protocol.MessageText(messages[i])); text != "" {
			return text
		}
	}
	return ""
}

// maxSessionReplyChunkRunes is the default maximum rune count for session
// delivery chunks.  The value matches the weixin channel limit so that
// replies posted through session delivery (e.g. cron tasks) can be forwarded
// to channels without exceeding per-message size limits.
//
// It is defined here (not as a const) so tests can override it.
var maxSessionReplyChunkRunes = 1200

// splitSessionReplyChunks splits text into chunks of at most maxRunes runes,
// breaking at natural sentence/punctuation boundaries when possible.
// It mirrors the weixin-channel splitReplyChunks logic but lives in the
// shared channels package so Manager.Deliver can use it for session delivery.
func splitSessionReplyChunks(text string, maxRunes int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxRunes <= 0 {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return []string{text}
	}
	chunks := make([]string, 0, len(runes)/maxRunes+1)
	start := 0
	for start < len(runes) {
		end := start + maxRunes
		if end >= len(runes) {
			chunks = append(chunks, strings.TrimSpace(string(runes[start:])))
			break
		}
		split := findSessionChunkBoundary(runes, start, end)
		chunks = append(chunks, strings.TrimSpace(string(runes[start:split])))
		start = split
	}
	out := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) != "" {
			out = append(out, chunk)
		}
	}
	return out
}

// findSessionChunkBoundary walks backwards from end looking for a natural
// break point (newline, CJK/ASCII punctuation, or space).
func findSessionChunkBoundary(runes []rune, start, end int) int {
	limit := end - start
	for i := end; i > start+limit/3; i-- {
		switch runes[i] {
		case '\n', '。', '！', '？', '.', '!', '?', '，', ',', ';', '；', ' ':
			return i + 1
		}
	}
	return end
}

func currentTurnAssistantText(messages []protocol.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == protocol.RoleUser {
			break
		}
		if msg.Role != protocol.RoleAssistant {
			continue
		}
		if text := strings.TrimSpace(protocol.MessageText(msg)); text != "" {
			return text
		}
	}
	return ""
}

func currentTurnAssistantArtifacts(messages []protocol.Message) []ReplyArtifact {
	artifacts := make([]ReplyArtifact, 0, 2)
	seen := make(map[string]struct{})
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == protocol.RoleUser {
			break
		}
		if msg.Role != protocol.RoleAssistant || msg.Metadata == nil || len(msg.Metadata.Attachments) == 0 {
			continue
		}
		for _, attachment := range msg.Metadata.Attachments {
			path := strings.TrimSpace(attachment.Path)
			if path == "" {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			artifacts = append(artifacts, ReplyArtifact{
				Path: path,
				Name: strings.TrimSpace(attachment.Name),
			})
		}
	}
	return artifacts
}

func applyCurrentTurnSnapshot(plan *ReplyPlan, messages []protocol.Message) {
	if plan == nil {
		return
	}
	if text := currentTurnAssistantText(messages); text != "" {
		plan.Text = text
	}
	if artifacts := currentTurnAssistantArtifacts(messages); len(artifacts) > 0 {
		plan.Artifacts = append([]ReplyArtifact{}, artifacts...)
	}
}

func mergeReplyArtifacts(plan *ReplyPlan, extra []ReplyArtifact) {
	if plan == nil || len(extra) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		if path := strings.TrimSpace(artifact.Path); path != "" {
			seen[path] = struct{}{}
		}
	}
	for _, artifact := range extra {
		path := strings.TrimSpace(artifact.Path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		plan.Artifacts = append(plan.Artifacts, ReplyArtifact{
			Path: path,
			Name: strings.TrimSpace(artifact.Name),
		})
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func ternary[T any](condition bool, whenTrue, whenFalse T) T {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func channelRecoverySuggestion(name, state string) string {
	return "Inspect log/log.txt and restart `godex serve` if needed."
}

func channelIdleSuggestion(name string) string {
	switch name {
	case "feishu":
		return "Verify the bot receives p2p messages and that event subscription includes im.message.receive_v1."
	case "weixin":
		return "Verify `godex weixin setup` completed successfully and send the bot a DM text message from an allowed iLink user."
	default:
		return "Send one inbound message through the channel and inspect log/log.txt if nothing arrives."
	}
}

func resolveSource(source message.EnvelopeSource) message.EnvelopeSource {
	if source == "" {
		return message.SourceGateway
	}
	return source
}

func cloneMetadata(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func normalizeInbound(inbound InboundMessage) InboundMessage {
	inbound.Channel = strings.TrimSpace(inbound.Channel)
	inbound.SessionKey = strings.TrimSpace(inbound.SessionKey)
	inbound.Sender = strings.TrimSpace(inbound.Sender)
	inbound.Routing = normalizeRouting(inbound.Routing, inbound.Channel, inbound.SessionKey, inbound.Sender)
	if inbound.SessionKey == "" {
		inbound.SessionKey = inbound.Routing.DerivedSessionKey()
	}
	if inbound.Channel == "" {
		inbound.Channel = inbound.Routing.ChannelID
	}
	if inbound.Sender == "" {
		inbound.Sender = inbound.Routing.SenderID
	}
	return inbound
}

func normalizeRouting(route RoutingIdentity, channel, sessionKey, sender string) RoutingIdentity {
	route.ChannelID = strings.TrimSpace(route.ChannelID)
	route.PlatformID = strings.TrimSpace(route.PlatformID)
	route.ThreadID = strings.TrimSpace(route.ThreadID)
	route.SenderID = strings.TrimSpace(route.SenderID)
	route.SessionMode = normalizeSessionMode(route.SessionMode)
	if route.ChannelID == "" {
		route.ChannelID = strings.TrimSpace(channel)
	}
	if route.PlatformID == "" {
		route.PlatformID = strings.TrimSpace(sessionKey)
	}
	if route.SenderID == "" {
		route.SenderID = strings.TrimSpace(sender)
	}
	return route
}

func normalizeSessionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case SessionModePerThread, "per_thread", "thread":
		return SessionModePerThread
	case SessionModeAgentShared, "agent_shared", "agent":
		return SessionModeAgentShared
	default:
		return SessionModeShared
	}
}

func (r RoutingIdentity) DerivedSessionKey() string {
	switch normalizeSessionMode(r.SessionMode) {
	case SessionModePerThread:
		if strings.TrimSpace(r.ThreadID) != "" {
			return strings.TrimSpace(r.PlatformID) + ":" + strings.TrimSpace(r.ThreadID)
		}
	case SessionModeAgentShared:
		if strings.TrimSpace(r.ChannelID) != "" {
			return strings.TrimSpace(r.ChannelID)
		}
	}
	if strings.TrimSpace(r.PlatformID) != "" {
		return strings.TrimSpace(r.PlatformID)
	}
	return strings.TrimSpace(r.ThreadID)
}

func metadataWithRouting(input map[string]string, route RoutingIdentity) map[string]string {
	route = normalizeRouting(route, "", "", "")
	out := cloneMetadata(input)
	put := func(key, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if out == nil {
			out = make(map[string]string)
		}
		if strings.TrimSpace(out[key]) == "" {
			out[key] = value
		}
	}
	put(MetadataChannelID, route.ChannelID)
	put(MetadataPlatformID, route.PlatformID)
	put(MetadataThreadID, route.ThreadID)
	put(MetadataSenderID, route.SenderID)
	put(MetadataSessionMode, route.SessionMode)
	return out
}

func userFacingError(err error) string {
	if err == nil {
		return ""
	}
	return "GoDex failed to process the message: " + err.Error()
}

func pendingApprovalNotice(requestID string) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return "Approval is required before GoDex can continue. Reply `/approve` to allow the only pending request, `/approve list` to inspect all requests, or `/deny <id>` to reject."
	}
	return fmt.Sprintf(
		"Approval is required before GoDex can continue. Reply `/approve` to allow once, `/approve session` to allow this session, or `/deny %s` to reject. You can still use `/approve %s` if there are multiple requests.",
		requestID,
		requestID,
	)
}

func (m *Manager) pendingApprovalSummary(ctx context.Context, sessionID, requestID string) (ReplyApproval, bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ReplyApproval{}, false
	}
	pending, err := m.backend.PendingPermissions(ctx, sessionID)
	if err != nil {
		channelsLog.Warnf("load pending permission detail for session %s: %v", sessionID, err)
		return ReplyApproval{}, false
	}
	for _, item := range pending {
		if item.ID == requestID {
			return ReplyApprovalFromPending(item), true
		}
	}
	return ReplyApproval{}, false
}

func (m *Manager) startSlowAck(ctx context.Context, channel string, reply ReplySender) func() {
	ackSender, ok := reply.(AckSender)
	if !ok || ackSender == nil || m.slowAckDelay <= 0 {
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(m.slowAckDelay)
		defer timer.Stop()
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		if err := ackSender.SendAck(ctx); err != nil {
			channelsLog.Warnf("send slow ack on channel %s: %v", channel, err)
			return
		}
		m.SetStatus(channel, ChannelStatusUpdate{
			State:      StateRunning,
			Detail:     "slow ack sent",
			ClearError: true,
			MarkAck:    true,
			LastEvent:  "slow ack",
		})
		channelsLog.Infof("slow ack sent on channel %s", channel)
	}()

	return func() {
		close(done)
	}
}

func normalizeChannelState(state string, enabled, running bool) string {
	switch state {
	case StateDisabled, StateStarting, StateRunning, StateRestarting, StateStopped, StateError:
		return state
	}
	if !enabled {
		return StateDisabled
	}
	if running {
		return StateRunning
	}
	return StateStopped
}

func (m *Manager) currentChannel(name string) Channel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.channels[name]
}

func (m *Manager) setChannel(name string, channel Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[name] = channel
}

func (m *Manager) deleteChannel(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.channels, name)
}

func (m *Manager) beginInbound(inbound InboundMessage) (string, bool) {
	key := inboundDedupKey(inbound)
	if key == "" {
		return "", false
	}

	now := time.Now()
	expireBefore := now.Add(-m.inboundDedupTTL)

	m.mu.Lock()
	defer m.mu.Unlock()

	for existingKey, seenAt := range m.recentInbound {
		if seenAt.Before(expireBefore) {
			delete(m.recentInbound, existingKey)
		}
	}
	if seenAt, ok := m.recentInbound[key]; ok && now.Sub(seenAt) < m.inboundDedupTTL {
		return key, true
	}
	m.recentInbound[key] = now
	return key, false
}

func (m *Manager) finishInbound(key string, success bool) {
	if key == "" || success {
		return
	}
	m.mu.Lock()
	delete(m.recentInbound, key)
	m.mu.Unlock()
}

func inboundDedupKey(inbound InboundMessage) string {
	if strings.TrimSpace(inbound.Channel) == "" || strings.TrimSpace(inbound.SessionKey) == "" {
		return ""
	}
	if len(inbound.Metadata) == 0 {
		return ""
	}
	for _, key := range []string{"message_id", "event_id", "delivery_id"} {
		value := strings.TrimSpace(inbound.Metadata[key])
		if value == "" {
			continue
		}
		return inbound.Channel + "|" + inbound.SessionKey + "|" + key + "|" + value
	}
	return ""
}
