package heartbeat

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/idempotency"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/runtime/channels"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
)

type Backend interface {
	OpenSession(context.Context, rtbackend.SessionLocator) (*rtbackend.OpenedSession, error)
	Submit(context.Context, string, message.Envelope) (*rtbackend.SubmitResult, error)
	AttachSink(string, events.Sink) (func(), error)
}

type Deliverer interface {
	Deliver(context.Context, automation.DeliveryTarget, channels.ReplyPlan) error
}

type BusyChecker func() bool

// ServiceOption configures a Service after construction.
type ServiceOption func(*Service)

// WithIdempotencyStore wires an idempotency store into the Service so that
// duplicate dispatches of the same run are suppressed.
func WithIdempotencyStore(st idempotency.Store) ServiceOption {
	return func(s *Service) { s.idempotency = st }
}

type Service struct {
	cfg     Config
	store   Store
	backend Backend
	deliver Deliverer
	now     func() time.Time
	busy    BusyChecker

	idempotency idempotency.Store

	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	inFlight bool
}

func NewService(cfg Config, store Store, backend Backend, deliver Deliverer, opts ...ServiceOption) *Service {
	if store == nil {
		panic("heartbeat store is required")
	}
	s := &Service{
		cfg:     normalizeConfig(cfg),
		store:   store,
		backend: backend,
		deliver: deliver,
		now:     time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) SetBusyChecker(check BusyChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy = check
}

func (s *Service) ApplyConfig(cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = normalizeConfig(cfg)
}

func (s *Service) Reconcile(ctx context.Context, cfg Config) error {
	s.ApplyConfig(cfg)
	if cfg.Enabled {
		return s.Start(ctx)
	}
	return s.Stop(ctx)
}

func (s *Service) AugmentDoctor(report config.DoctorReport) config.DoctorReport {
	if !s.cfg.Enabled {
		return report
	}
	if _, err := time.LoadLocation(s.cfg.DefaultTimezone); err != nil {
		report.Checks = append(report.Checks, config.DoctorCheck{
			Severity:   "warning",
			Code:       "heartbeat_invalid_timezone",
			Path:       "heartbeat.default_timezone",
			Message:    fmt.Sprintf("Heartbeat default timezone %q is invalid.", s.cfg.DefaultTimezone),
			Suggestion: "Set heartbeat.default_timezone to a valid IANA timezone such as Asia/Shanghai.",
		})
		report.Warnings++
	}
	if _, err := loadChecklist(s.cfg); err != nil {
		report.Checks = append(report.Checks, config.DoctorCheck{
			Severity:   "warning",
			Code:       "heartbeat_checklist_missing",
			Path:       "heartbeat.checklist_path",
			Message:    err.Error(),
			Suggestion: "Create HEARTBEAT.md in the workspace root or in the configured GODEX_HOME state directory.",
		})
		report.Warnings++
	}
	if fsStore, ok := s.store.(*FileStore); ok {
		if err := os.MkdirAll(filepath.Dir(fsStore.RulePath()), 0755); err == nil {
			if testFile, createErr := os.CreateTemp(filepath.Dir(fsStore.RulePath()), ".writable-*"); createErr == nil {
				_ = testFile.Close()
				_ = os.Remove(testFile.Name())
			} else {
				report.Checks = append(report.Checks, config.DoctorCheck{
					Severity:   "warning",
					Code:       "heartbeat_store_unwritable",
					Path:       "heartbeat",
					Message:    fmt.Sprintf("Heartbeat store is not writable: %v", createErr),
					Suggestion: "Ensure .godex/heartbeat is writable by the current user.",
				})
				report.Warnings++
			}
		}
	}
	return report
}

func (s *Service) Start(ctx context.Context) error {
	if !s.cfg.Enabled {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.loop(runCtx, s.done)
	return nil
}

func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) GetRule() (Rule, error) {
	return s.ensureRule()
}

func (s *Service) SetRule(input SetRuleInput) (Rule, error) {
	rule, err := s.ensureRule()
	if err != nil {
		return Rule{}, err
	}
	recomputeNext := false
	if input.Enabled != nil {
		rule.Enabled = *input.Enabled
		recomputeNext = true
	}
	if input.IntervalSeconds != nil {
		rule.IntervalSeconds = *input.IntervalSeconds
		recomputeNext = true
	}
	if input.Timezone != nil {
		rule.Timezone = strings.TrimSpace(*input.Timezone)
		recomputeNext = true
	}
	if input.ActiveHoursStart != nil {
		rule.ActiveHoursStart = strings.TrimSpace(*input.ActiveHoursStart)
	}
	if input.ActiveHoursEnd != nil {
		rule.ActiveHoursEnd = strings.TrimSpace(*input.ActiveHoursEnd)
	}
	if input.SessionMode != nil {
		rule.SessionMode = *input.SessionMode
	}
	if input.DeliveryTarget != nil {
		rule.DeliveryTarget = input.DeliveryTarget.Clone()
	}
	if input.PromptOverride != nil {
		rule.PromptOverride = strings.TrimSpace(*input.PromptOverride)
	}
	if input.WatchdogScript != nil {
		rule.WatchdogScript = strings.TrimSpace(*input.WatchdogScript)
	}
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = s.now()
	}
	if input.CreatedBy != "" && rule.CreatedBy == "" {
		rule.CreatedBy = input.CreatedBy
	}
	if input.CreatedFromSession != "" && rule.CreatedFromSession == "" {
		rule.CreatedFromSession = input.CreatedFromSession
	}
	now := s.now()
	rule.UpdatedAt = now
	rule = rule.normalize(s.cfg)
	if err := validateRule(rule); err != nil {
		return Rule{}, err
	}
	if !rule.Enabled {
		rule.NextRunAt = time.Time{}
	} else if recomputeNext || rule.NextRunAt.IsZero() {
		rule.NextRunAt = s.computeNextRun(rule, now)
	}
	if err := s.store.SaveRule(rule); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

func (s *Service) Toggle(enabled bool) (Rule, error) {
	return s.SetRule(SetRuleInput{Enabled: &enabled})
}

func (s *Service) TestNow(ctx context.Context) (RunLog, error) {
	rule, err := s.ensureRule()
	if err != nil {
		return RunLog{}, err
	}
	return s.runRule(ctx, rule, s.now(), true)
}

func (s *Service) ListRunLogs(limit int) ([]RunLog, error) {
	return s.store.ListRunLogs(limit)
}

func (s *Service) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Duration(s.cfg.TickSeconds) * time.Second)
	defer ticker.Stop()
	for {
		if err := s.dispatchDue(ctx); err != nil && ctx.Err() == nil {
			// keep looping; errors surface in rule state and run logs
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) dispatchDue(ctx context.Context) error {
	rule, err := s.ensureRule()
	if err != nil {
		return err
	}
	now := s.now()
	if !rule.Enabled || rule.NextRunAt.IsZero() || rule.NextRunAt.After(now) {
		return nil
	}
	s.mu.Lock()
	if s.inFlight {
		s.mu.Unlock()
		return nil
	}
	busy := s.busy != nil && s.busy()
	if busy {
		s.mu.Unlock()
		return nil
	}
	s.inFlight = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight = false
		s.mu.Unlock()
	}()
	if !withinActiveHours(rule, now) {
		return nil
	}
	_, err = s.runRule(ctx, rule, now, false)
	return err
}

func (s *Service) runRule(ctx context.Context, rule Rule, startedAt time.Time, manual bool) (RunLog, error) {
	rule = rule.normalize(s.cfg)

	// Idempotency guard: skip if this run has already been committed.
	if s.idempotency != nil {
		idempKey := fmt.Sprintf("heartbeat:run:%s:%d", rule.ID, startedAt.Unix())
		committed, err := s.idempotency.Committed(idempKey)
		if err != nil {
			return RunLog{}, fmt.Errorf("idempotency check failed: %w", err)
		}
		if committed {
			return RunLog{}, nil
		}
	}

	run := RunLog{
		ID:             newID("hb-run"),
		RuleID:         rule.ID,
		Status:         RuleStatusRunning,
		DeliveryTarget: rule.DeliveryTarget.Clone(),
		StartedAt:      startedAt,
	}
	rule.LastStatus = RuleStatusRunning
	rule.LastError = ""
	rule.UpdatedAt = startedAt
	if !manual && rule.Enabled {
		rule.NextRunAt = s.computeNextRun(rule, startedAt)
	}
	if err := s.store.SaveRule(rule); err != nil {
		return run, err
	}

	checklist, err := loadChecklist(s.cfg)
	if err != nil {
		run.Status = RuleStatusError
		run.Error = err.Error()
		run.FinishedAt = s.now()
		rule.LastStatus = RuleStatusError
		rule.LastError = err.Error()
		rule.LastRunAt = run.FinishedAt
		rule.UpdatedAt = run.FinishedAt
		_ = s.store.AppendRunLog(run)
		_ = s.store.SaveRule(rule)
		return run, err
	}

	// Pre-run watchdog: a non-zero exit skips this tick entirely (no agent
	// execution, no delivery). Errors (missing script, timeout) are recorded
	// as failed runs so the misconfiguration is visible.
	if strings.TrimSpace(rule.WatchdogScript) != "" {
		watchdogOut, wdErr := runWatchdog(ctx, rule.WatchdogScript, s.cfg.WorkspaceDir, 0)
		if wdErr != nil {
			run.Status = RuleStatusError
			run.Error = wdErr.Error()
			run.FinishedAt = s.now()
			rule.LastStatus = RuleStatusError
			rule.LastError = wdErr.Error()
			rule.LastRunAt = run.FinishedAt
			rule.UpdatedAt = run.FinishedAt
			_ = s.store.AppendRunLog(run)
			_ = s.store.SaveRule(rule)
			return run, wdErr
		}
		if watchdogOut.Skipped {
			run.Status = RuleStatusSuppressed
			run.Suppressed = true
			run.Error = ""
			if strings.TrimSpace(watchdogOut.Output) != "" {
				run.Error = "watchdog skipped: " + watchdogOut.Output
			}
			run.FinishedAt = s.now()
			rule.LastStatus = RuleStatusSuppressed
			rule.LastError = run.Error
			rule.LastRunAt = run.FinishedAt
			rule.UpdatedAt = run.FinishedAt
			_ = s.store.AppendRunLog(run)
			_ = s.store.SaveRule(rule)
			return run, nil
		}
	}

	opened, err := s.backend.OpenSession(ctx, s.ruleLocator(rule, run.ID))
	if err != nil {
		run.Status = RuleStatusError
		run.Error = err.Error()
		run.FinishedAt = s.now()
		rule.LastStatus = RuleStatusError
		rule.LastError = err.Error()
		rule.LastRunAt = run.FinishedAt
		rule.UpdatedAt = run.FinishedAt
		_ = s.store.AppendRunLog(run)
		_ = s.store.SaveRule(rule)
		return run, err
	}
	run.SessionID = opened.SessionID

	collector := &replyPlanCollector{}
	unsubscribe, err := s.backend.AttachSink(opened.SessionID, collector)
	if err != nil {
		run.Status = RuleStatusError
		run.Error = err.Error()
		run.FinishedAt = s.now()
		rule.LastStatus = RuleStatusError
		rule.LastError = err.Error()
		rule.LastRunAt = run.FinishedAt
		rule.UpdatedAt = run.FinishedAt
		_ = s.store.AppendRunLog(run)
		_ = s.store.SaveRule(rule)
		return run, err
	}
	defer unsubscribe()

	result, err := s.backend.Submit(ctx, opened.SessionID, message.NewRuntimeEnvelope(message.SourceHeartbeat, opened.SessionID, "heartbeat", buildHeartbeatPrompt(rule, checklist, s.cfg.OKToken), startedAt, map[string]string{
		"rule_id":        rule.ID,
		"run_id":         run.ID,
		"checklist_path": checklist.Path,
	}))
	if result != nil {
		run.TurnID = result.TurnID
	}
	plan := collector.Plan()
	if strings.TrimSpace(plan.Text) == "" && len(plan.Notices) == 0 {
		plan.Text = "Heartbeat completed."
	}

	run.FinishedAt = s.now()
	if err != nil {
		run.Status = RuleStatusError
		run.Error = err.Error()
		rule.LastStatus = RuleStatusError
		rule.LastError = err.Error()
	} else {
		run.Status = RuleStatusCompleted
		rule.LastStatus = RuleStatusCompleted
		rule.LastError = ""
	}
	rule.LastRunAt = run.FinishedAt
	rule.UpdatedAt = run.FinishedAt

	if run.Status == RuleStatusCompleted && strings.Contains(plan.RenderText(), s.cfg.OKToken) {
		run.Status = RuleStatusSuppressed
		run.Suppressed = true
		rule.LastStatus = RuleStatusSuppressed
		rule.LastError = ""
	} else if run.Status == RuleStatusCompleted && !rule.DeliveryTarget.IsZero() && s.deliver != nil {
		if deliverErr := s.deliver.Deliver(ctx, rule.DeliveryTarget, plan); deliverErr != nil {
			if automation.IsBlockedError(deliverErr) {
				run.Status = RuleStatusDeliveryBlocked
				run.Error = deliverErr.Error()
				rule.LastStatus = RuleStatusDeliveryBlocked
				rule.LastError = deliverErr.Error()
			} else {
				run.Status = RuleStatusError
				run.Error = deliverErr.Error()
				rule.LastStatus = RuleStatusError
				rule.LastError = deliverErr.Error()
			}
			if err == nil {
				err = deliverErr
			}
		}
	}

	if appendErr := s.store.AppendRunLog(run); appendErr != nil && err == nil {
		err = appendErr
	}
	if saveErr := s.store.SaveRule(rule); saveErr != nil && err == nil {
		err = saveErr
	}

	// Commit the idempotency key after a clean successful run.
	if s.idempotency != nil && err == nil && run.Status == RuleStatusCompleted {
		idempKey := fmt.Sprintf("heartbeat:run:%s:%d", rule.ID, startedAt.Unix())
		if _, onceErr := s.idempotency.Once(idempKey, func() error { return nil }); onceErr != nil && err == nil {
			err = fmt.Errorf("idempotency commit failed: %w", onceErr)
		}
	}

	return run, err
}

func (s *Service) ensureRule() (Rule, error) {
	rule, err := s.store.GetRule()
	if err == nil {
		return rule.normalize(s.cfg), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Rule{}, err
	}
	now := s.now()
	rule = Rule{
		ID:              defaultRuleID,
		Enabled:         false,
		IntervalSeconds: s.cfg.DefaultIntervalSeconds,
		Timezone:        s.cfg.DefaultTimezone,
		SessionMode:     SessionModeShared,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastStatus:      RuleStatusPending,
	}
	rule = rule.normalize(s.cfg)
	if err := s.store.SaveRule(rule); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

func (s *Service) computeNextRun(rule Rule, reference time.Time) time.Time {
	if !rule.Enabled {
		return time.Time{}
	}
	loc, err := time.LoadLocation(rule.Timezone)
	if err != nil {
		return time.Time{}
	}
	return reference.In(loc).Add(time.Duration(rule.IntervalSeconds) * time.Second).UTC()
}

func withinActiveHours(rule Rule, now time.Time) bool {
	if strings.TrimSpace(rule.ActiveHoursStart) == "" || strings.TrimSpace(rule.ActiveHoursEnd) == "" {
		return true
	}
	loc, err := time.LoadLocation(rule.Timezone)
	if err != nil {
		return true
	}
	current := now.In(loc)
	minutes := current.Hour()*60 + current.Minute()
	start, err := parseClock(rule.ActiveHoursStart)
	if err != nil {
		return true
	}
	end, err := parseClock(rule.ActiveHoursEnd)
	if err != nil {
		return true
	}
	if start == end {
		return true
	}
	if start < end {
		return minutes >= start && minutes < end
	}
	return minutes >= start || minutes < end
}

func buildHeartbeatPrompt(rule Rule, checklist checklistSource, okToken string) string {
	if strings.TrimSpace(rule.PromptOverride) != "" {
		return strings.TrimSpace(rule.PromptOverride)
	}
	return strings.Join([]string{
		"Run the configured heartbeat checklist for this workspace.",
		"This is an active heartbeat execution, not a request to create or manage cron/heartbeat schedules.",
		"Do not create, update, toggle, delete, run, or test cron or heartbeat tasks unless the checklist explicitly asks you to change automation settings.",
		"",
		"Checklist path: " + checklist.Path,
		"",
		"Checklist:",
		checklist.Content,
		"",
		"Review current state, pending work, and anything that needs proactive follow-up.",
		"If everything is fine and no proactive update is needed, respond with exactly " + okToken + ".",
		"Otherwise provide a concise proactive update with only the important items.",
	}, "\n")
}

func (s *Service) ruleLocator(rule Rule, runID string) rtbackend.SessionLocator {
	key := rule.ID
	if rule.SessionMode == SessionModeIsolated {
		key = rule.ID + ":" + runID
	}
	return rtbackend.SessionLocator{
		Channel: "heartbeat",
		Key:     key,
		Metadata: map[string]string{
			"rule_id": rule.ID,
			"run_id":  runID,
		},
	}
}

type replyPlanCollector struct {
	builder   strings.Builder
	notices   []string
	tools     []channels.ReplyTool
	artifacts []channels.ReplyArtifact
}

func (c *replyPlanCollector) Emit(event events.Event) {
	switch event.Type {
	case events.EventAssistantTextDelta:
		if payload, ok := event.Payload.(events.TextPayload); ok {
			c.builder.WriteString(payload.Text)
		}
	case events.EventWarningRaised, events.EventErrorRaised:
		if payload, ok := event.Payload.(events.NoticePayload); ok && strings.TrimSpace(payload.Message) != "" {
			c.notices = append(c.notices, strings.TrimSpace(payload.Message))
		}
	case events.EventToolCallFinished:
		if payload, ok := event.Payload.(events.ToolCallPayload); ok {
			c.tools = append(c.tools, channels.ReplyTool{
				ID:     payload.ID,
				Name:   payload.Name,
				Status: heartbeatTernary(strings.TrimSpace(payload.Error) == "", "completed", "failed"),
				Output: payload.Output,
				Error:  payload.Error,
			})
			for _, path := range payload.ArtifactPaths {
				path = strings.TrimSpace(path)
				if path == "" || c.hasArtifact(path) {
					continue
				}
				c.artifacts = append(c.artifacts, channels.ReplyArtifact{
					Path: path,
					Name: filepath.Base(path),
				})
			}
		}
	}
}

func (c *replyPlanCollector) Plan() channels.ReplyPlan {
	return channels.ReplyPlan{
		Text:      strings.TrimSpace(c.builder.String()),
		Notices:   append([]string{}, c.notices...),
		Tools:     append([]channels.ReplyTool{}, c.tools...),
		Artifacts: append([]channels.ReplyArtifact{}, c.artifacts...),
		Status:    "completed",
	}
}

func (c *replyPlanCollector) hasArtifact(path string) bool {
	for _, artifact := range c.artifacts {
		if artifact.Path == path {
			return true
		}
	}
	return false
}

func heartbeatTernary[T any](condition bool, whenTrue, whenFalse T) T {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func newID(prefix string) string {
	var raw [8]byte
	if _, err := crand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
}
