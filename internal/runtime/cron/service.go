package cron

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/tim5wang/godex/internal/core/config"
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
	// SetSessionModelProfile pins a freshly opened session to the given model
	// profile. The real backend implements this via SetSessionModelProfile so
	// that subsequent turns — including those run by cron — use the selected
	// profile (and the configured strategy / fallback chain for that profile)
	// rather than the global default.
	//
	// The call is best-effort: a nil error means the profile is now in effect
	// for sessionID. Implementations should treat a "profile not found" error
	// as terminal — the cron run should be marked failed and surfaced in logs.
	SetSessionModelProfile(ctx context.Context, sessionID, profileID string) error
}

type Deliverer interface {
	Deliver(context.Context, automation.DeliveryTarget, channels.ReplyPlan) error
}

type Service struct {
	cfg     Config
	store   Store
	backend Backend
	deliver Deliverer
	now     func() time.Time

	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	inFlight  map[string]struct{}
	semaphore chan struct{}
	wg        sync.WaitGroup
}

func NewService(cfg Config, store Store, backend Backend, deliver Deliverer) *Service {
	if store == nil {
		panic("cron store is required")
	}
	maxConcurrent := cfg.MaxConcurrentRuns
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	return &Service{
		cfg:       normalizeConfig(cfg),
		store:     store,
		backend:   backend,
		deliver:   deliver,
		now:       time.Now,
		inFlight:  make(map[string]struct{}),
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

func normalizeConfig(cfg Config) Config {
	if cfg.TickSeconds <= 0 {
		cfg.TickSeconds = 1
	}
	if strings.TrimSpace(cfg.DefaultTimezone) == "" {
		cfg.DefaultTimezone = "Local"
	}
	if cfg.MaxConcurrentRuns <= 0 {
		cfg.MaxConcurrentRuns = 2
	}
	return cfg
}

func (s *Service) ApplyConfig(cfg Config) {
	cfg = normalizeConfig(cfg)
	s.mu.Lock()
	s.cfg = cfg
	if cap(s.semaphore) != cfg.MaxConcurrentRuns {
		s.semaphore = make(chan struct{}, cfg.MaxConcurrentRuns)
	}
	s.mu.Unlock()
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
			Code:       "cron_invalid_timezone",
			Path:       "cron.default_timezone",
			Message:    fmt.Sprintf("Cron default timezone %q is invalid.", s.cfg.DefaultTimezone),
			Suggestion: "Set cron.default_timezone to a valid IANA timezone such as Asia/Shanghai.",
		})
		report.Warnings++
	}
	if fsStore, ok := s.store.(*FileStore); ok {
		if err := os.MkdirAll(filepath.Dir(fsStore.JobsDir()), 0755); err == nil {
			if testFile, createErr := os.CreateTemp(fsStore.JobsDir(), ".writable-*"); createErr == nil {
				_ = testFile.Close()
				_ = os.Remove(testFile.Name())
			} else {
				report.Checks = append(report.Checks, config.DoctorCheck{
					Severity:   "warning",
					Code:       "cron_store_unwritable",
					Path:       "cron",
					Message:    fmt.Sprintf("Cron store is not writable: %v", createErr),
					Suggestion: "Ensure .godex/cron is writable by the current user.",
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
	waitDone := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (s *Service) ListJobs() ([]Job, error) {
	return s.store.ListJobs()
}

func (s *Service) GetJob(jobID string) (Job, error) {
	return s.store.GetJob(jobID)
}

func (s *Service) CreateJob(input CreateJobInput) (Job, error) {
	now := s.now()
	job := Job{
		ID:                 newID("cronjob"),
		Name:               strings.TrimSpace(input.Name),
		Message:            strings.TrimSpace(input.Message),
		Timezone:           input.Timezone,
		Schedule:           input.Schedule,
		SessionMode:        input.SessionMode,
		ModelProfileID:     strings.TrimSpace(input.ModelProfileID),
		DeliveryTarget:     input.DeliveryTarget.Clone(),
		Enabled:            input.Enabled,
		CreatedBy:          strings.TrimSpace(input.CreatedBy),
		CreatedFromSession: strings.TrimSpace(input.CreatedFromSession),
		CreatedAt:          now,
		UpdatedAt:          now,
		LastStatus:         JobStatusPending,
	}
	if !input.Enabled {
		job.Enabled = false
	}
	job = job.normalize(s.cfg.DefaultTimezone)
	if err := s.validateJob(job); err != nil {
		return Job{}, err
	}
	job.NextRunAt = s.computeNextRun(job, now)
	if err := s.store.SaveJob(job); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) UpdateJob(input UpdateJobInput) (Job, error) {
	job, err := s.store.GetJob(input.ID)
	if err != nil {
		return Job{}, err
	}
	recomputeNext := false
	if input.Name != nil {
		job.Name = strings.TrimSpace(*input.Name)
	}
	if input.Message != nil {
		job.Message = strings.TrimSpace(*input.Message)
	}
	if input.Timezone != nil {
		job.Timezone = strings.TrimSpace(*input.Timezone)
		recomputeNext = true
	}
	if input.Schedule != nil {
		job.Schedule = *input.Schedule
		recomputeNext = true
	}
	if input.SessionMode != nil {
		job.SessionMode = *input.SessionMode
	}
	if input.ModelProfileID != nil {
		// tri-state: nil keeps the current value, "" clears it (use default),
		// non-empty pins to the new profile.
		job.ModelProfileID = strings.TrimSpace(*input.ModelProfileID)
	}
	if input.DeliveryTarget != nil {
		job.DeliveryTarget = input.DeliveryTarget.Clone()
	}
	if input.Enabled != nil {
		job.Enabled = *input.Enabled
		recomputeNext = true
	}
	now := s.now()
	job.UpdatedAt = now
	job = job.normalize(s.cfg.DefaultTimezone)
	if err := s.validateJob(job); err != nil {
		return Job{}, err
	}
	if recomputeNext {
		job.NextRunAt = s.computeNextRun(job, now)
	}
	if err := s.store.SaveJob(job); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) ToggleJob(jobID string, enabled bool) (Job, error) {
	return s.UpdateJob(UpdateJobInput{ID: jobID, Enabled: &enabled})
}

func (s *Service) DeleteJob(jobID string) error {
	return s.store.DeleteJob(jobID)
}

func (s *Service) ListRunLogs(jobID string, limit int) ([]RunLog, error) {
	return s.store.ListRunLogs(jobID, limit)
}

func (s *Service) RunNow(ctx context.Context, jobID string) (RunLog, error) {
	job, err := s.store.GetJob(jobID)
	if err != nil {
		return RunLog{}, err
	}
	return s.runJob(ctx, job, s.now())
}

func (s *Service) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Duration(s.cfg.TickSeconds) * time.Second)
	defer ticker.Stop()
	for {
		if err := s.dispatchDue(ctx); err != nil && ctx.Err() == nil {
			// keep looping; errors are reflected in job logs
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) dispatchDue(ctx context.Context) error {
	jobs, err := s.store.ListJobs()
	if err != nil {
		return err
	}
	now := s.now()
	for _, job := range jobs {
		if !job.Enabled || job.NextRunAt.IsZero() || job.NextRunAt.After(now) {
			continue
		}
		if !s.beginRun(job.ID) {
			continue
		}
		select {
		case s.semaphore <- struct{}{}:
		case <-ctx.Done():
			s.finishRun(job.ID)
			return ctx.Err()
		}
		s.wg.Add(1)
		go func(job Job, startedAt time.Time) {
			defer s.wg.Done()
			defer func() {
				<-s.semaphore
				s.finishRun(job.ID)
			}()
			_, _ = s.runJob(ctx, job, startedAt)
		}(job, now)
	}
	return nil
}

func (s *Service) beginRun(jobID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.inFlight[jobID]; ok {
		return false
	}
	s.inFlight[jobID] = struct{}{}
	return true
}

func (s *Service) finishRun(jobID string) {
	s.mu.Lock()
	delete(s.inFlight, jobID)
	s.mu.Unlock()
}

func (s *Service) runJob(ctx context.Context, job Job, startedAt time.Time) (RunLog, error) {
	job = job.normalize(s.cfg.DefaultTimezone)
	run := RunLog{
		ID:             newID("cronrun"),
		JobID:          job.ID,
		Status:         JobStatusRunning,
		DeliveryTarget: job.DeliveryTarget.Clone(),
		StartedAt:      startedAt,
	}
	job.LastStatus = JobStatusRunning
	job.LastError = ""
	job.UpdatedAt = startedAt
	if job.Schedule.Type == ScheduleAt {
		job.Enabled = false
		job.NextRunAt = time.Time{}
	} else {
		job.NextRunAt = s.computeNextRunAfterRun(job, startedAt)
	}
	if err := s.store.SaveJob(job); err != nil {
		return run, err
	}

	opened, err := s.backend.OpenSession(ctx, s.jobLocator(job, run.ID))
	if err != nil {
		run.Status = JobStatusError
		run.Error = err.Error()
		run.FinishedAt = s.now()
		_ = s.store.AppendRunLog(run)
		job.LastStatus = JobStatusError
		job.LastError = err.Error()
		job.UpdatedAt = run.FinishedAt
		_ = s.store.SaveJob(job)
		return run, err
	}
	run.SessionID = opened.SessionID

	// Pin the session to the job's selected model profile (if any) so that
	// Submit / fallback behavior is anchored to that profile. We do this
	// before attaching the sink so that any model-related error short-circuits
	// the run and is recorded as a clean JobStatusError with no partial
	// assistant output collected.
	if profileID := strings.TrimSpace(job.ModelProfileID); profileID != "" {
		if modelErr := s.backend.SetSessionModelProfile(ctx, opened.SessionID, profileID); modelErr != nil {
			run.Status = JobStatusError
			run.Error = modelErr.Error()
			run.FinishedAt = s.now()
			_ = s.store.AppendRunLog(run)
			job.LastStatus = JobStatusError
			job.LastError = modelErr.Error()
			job.UpdatedAt = run.FinishedAt
			_ = s.store.SaveJob(job)
			return run, modelErr
		}
	}

	collector := &replyPlanCollector{}
	unsubscribe, err := s.backend.AttachSink(opened.SessionID, collector)
	if err != nil {
		run.Status = JobStatusError
		run.Error = err.Error()
		run.FinishedAt = s.now()
		_ = s.store.AppendRunLog(run)
		job.LastStatus = JobStatusError
		job.LastError = err.Error()
		job.UpdatedAt = run.FinishedAt
		_ = s.store.SaveJob(job)
		return run, err
	}
	defer unsubscribe()

	result, err := s.backend.Submit(ctx, opened.SessionID, message.NewRuntimeEnvelope(message.SourceCron, opened.SessionID, "cron", buildCronPrompt(job), startedAt, map[string]string{
		"job_id": job.ID,
		"run_id": run.ID,
	}))
	if result != nil {
		run.TurnID = result.TurnID
	}
	plan := collector.Plan()
	if strings.TrimSpace(plan.Text) == "" {
		plan.Text = "Completed."
	}

	run.FinishedAt = s.now()
	if err != nil {
		run.Status = JobStatusError
		run.Error = err.Error()
		job.LastStatus = JobStatusError
		job.LastError = err.Error()
	} else {
		run.Status = JobStatusCompleted
		job.LastStatus = JobStatusCompleted
		job.LastError = ""
	}
	job.LastRunAt = run.FinishedAt
	job.UpdatedAt = run.FinishedAt

	if run.Status == JobStatusCompleted && !job.DeliveryTarget.IsZero() && s.deliver != nil {
		if deliverErr := s.deliver.Deliver(ctx, job.DeliveryTarget, plan); deliverErr != nil {
			if automation.IsBlockedError(deliverErr) {
				run.Status = JobStatusDeliveryBlocked
				run.Error = deliverErr.Error()
				job.LastStatus = JobStatusDeliveryBlocked
				job.LastError = deliverErr.Error()
			} else {
				run.Status = JobStatusError
				run.Error = deliverErr.Error()
				job.LastStatus = JobStatusError
				job.LastError = deliverErr.Error()
			}
			if err == nil {
				err = deliverErr
			}
		}
	}

	if appendErr := s.store.AppendRunLog(run); appendErr != nil && err == nil {
		err = appendErr
	}
	if saveErr := s.store.SaveJob(job); saveErr != nil && err == nil {
		err = saveErr
	}
	return run, err
}

func (s *Service) validateJob(job Job) error {
	if strings.TrimSpace(job.Message) == "" {
		return fmt.Errorf("cron message is required")
	}
	if err := validateSchedule(job.Schedule); err != nil {
		return err
	}
	switch job.SessionMode {
	case SessionModeShared, SessionModeIsolated:
	default:
		return fmt.Errorf("unsupported session mode %q", job.SessionMode)
	}
	if _, err := time.LoadLocation(job.Timezone); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", job.Timezone, err)
	}
	return nil
}

func (s *Service) computeNextRun(job Job, reference time.Time) time.Time {
	if !job.Enabled {
		return time.Time{}
	}
	loc, err := time.LoadLocation(job.Timezone)
	if err != nil {
		return time.Time{}
	}
	ref := reference.In(loc)
	switch job.Schedule.Type {
	case ScheduleAt:
		at := job.Schedule.At.In(loc)
		if at.Before(ref) || at.Equal(ref) {
			return at
		}
		return at
	case ScheduleEvery:
		return ref.Add(time.Duration(job.Schedule.EverySeconds) * time.Second).UTC()
	case ScheduleCron:
		parser, _ := cron.ParseStandard(strings.TrimSpace(job.Schedule.CronExpr))
		return parser.Next(ref).UTC()
	default:
		return time.Time{}
	}
}

func (s *Service) computeNextRunAfterRun(job Job, reference time.Time) time.Time {
	if !job.Enabled {
		return time.Time{}
	}
	loc, err := time.LoadLocation(job.Timezone)
	if err != nil {
		return time.Time{}
	}
	ref := reference.In(loc)
	switch job.Schedule.Type {
	case ScheduleAt:
		job.Enabled = false
		return time.Time{}
	case ScheduleEvery:
		return ref.Add(time.Duration(job.Schedule.EverySeconds) * time.Second).UTC()
	case ScheduleCron:
		parser, _ := cron.ParseStandard(strings.TrimSpace(job.Schedule.CronExpr))
		return parser.Next(ref).UTC()
	default:
		return time.Time{}
	}
}

func (s *Service) jobLocator(job Job, runID string) rtbackend.SessionLocator {
	key := job.ID
	if job.SessionMode == SessionModeIsolated {
		key = job.ID + ":" + runID
	}
	locator := rtbackend.SessionLocator{
		Channel: "cron",
		Key:     key,
		Metadata: map[string]string{
			"job_id": job.ID,
			"run_id": runID,
		},
	}
	if profileID := strings.TrimSpace(job.ModelProfileID); profileID != "" {
		locator.Metadata["model_profile_id"] = profileID
	}
	return locator
}

func buildCronPrompt(job Job) string {
	lines := []string{
		"This is a scheduled cron execution that has already been triggered.",
		"Carry out the scheduled work now and produce the reminder, update, or result that should be delivered to the configured target.",
		"Do not create, update, toggle, delete, run, or test cron or heartbeat tasks just because the message sounds like a reminder request.",
		"Only manage cron or heartbeat schedules if the scheduled payload explicitly asks you to modify automation settings.",
		"",
		"Scheduled payload:",
		strings.TrimSpace(job.Message),
	}
	return strings.Join(lines, "\n")
}

func newID(prefix string) string {
	var raw [8]byte
	if _, err := crand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
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
				Status: ternary(strings.TrimSpace(payload.Error) == "", "completed", "failed"),
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
	case events.EventCommandCompleted:
		if payload, ok := event.Payload.(events.CommandPayload); ok && strings.TrimSpace(payload.ArtifactPath) != "" {
			c.tools = append(c.tools, channels.ReplyTool{
				Name:   payload.Name,
				Status: ternary(strings.TrimSpace(payload.Error) == "", "completed", "failed"),
				Output: payload.Output,
				Error:  payload.Error,
			})
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

func ternary[T any](condition bool, whenTrue, whenFalse T) T {
	if condition {
		return whenTrue
	}
	return whenFalse
}
