package cron

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/tim5wang/godex/internal/domain/automation"
)

type ScheduleType string

const (
	ScheduleAt    ScheduleType = "at"
	ScheduleEvery ScheduleType = "every"
	ScheduleCron  ScheduleType = "cron"
)

type SessionMode string

const (
	SessionModeShared   SessionMode = "shared"
	SessionModeIsolated SessionMode = "isolated"
)

type JobStatus string

const (
	JobStatusPending         JobStatus = "pending"
	JobStatusRunning         JobStatus = "running"
	JobStatusCompleted       JobStatus = "completed"
	JobStatusError           JobStatus = "error"
	JobStatusDeliveryBlocked JobStatus = "delivery_blocked"
	JobStatusSuppressed      JobStatus = "suppressed"
)

type Schedule struct {
	Type         ScheduleType `json:"type"`
	At           time.Time    `json:"at,omitempty"`
	EverySeconds int          `json:"every_seconds,omitempty"`
	CronExpr     string       `json:"cron_expr,omitempty"`
}

type Job struct {
	ID                 string                    `json:"id"`
	Name               string                    `json:"name,omitempty"`
	Message            string                    `json:"message"`
	Timezone           string                    `json:"timezone,omitempty"`
	Schedule           Schedule                  `json:"schedule"`
	SessionMode        SessionMode               `json:"session_mode,omitempty"`
	// ModelProfileID optionally pins the job to a configured model profile.
	// Empty means "use the current default profile" — the configured
	// strategy / fallback chain still applies to that run.
	ModelProfileID     string                    `json:"model_profile_id,omitempty"`
	// WatchdogScript is an optional shell script run before each job fires.
	// Exit 0 runs the message (agent); non-zero skips this tick (zero tokens);
	// missing script or timeout records an error.
	WatchdogScript     string                    `json:"watchdog_script,omitempty"`
	DeliveryTarget     automation.DeliveryTarget `json:"delivery_target,omitempty"`
	Enabled            bool                      `json:"enabled"`
	CreatedBy          string                    `json:"created_by,omitempty"`
	CreatedFromSession string                    `json:"created_from_session,omitempty"`
	CreatedAt          time.Time                 `json:"created_at"`
	UpdatedAt          time.Time                 `json:"updated_at"`
	LastRunAt          time.Time                 `json:"last_run_at,omitempty"`
	NextRunAt          time.Time                 `json:"next_run_at,omitempty"`
	LastStatus         JobStatus                 `json:"last_status,omitempty"`
	LastError          string                    `json:"last_error,omitempty"`
}

type RunLog struct {
	ID             string                    `json:"id"`
	JobID          string                    `json:"job_id"`
	SessionID      string                    `json:"session_id,omitempty"`
	TurnID         string                    `json:"turn_id,omitempty"`
	Status         JobStatus                 `json:"status"`
	Error          string                    `json:"error,omitempty"`
	Suppressed     bool                      `json:"suppressed,omitempty"`
	WatchdogOutput string                    `json:"watchdog_output,omitempty"`
	DeliveryTarget automation.DeliveryTarget `json:"delivery_target,omitempty"`
	StartedAt      time.Time                 `json:"started_at"`
	FinishedAt     time.Time                 `json:"finished_at,omitempty"`
}

type CreateJobInput struct {
	Name               string
	Message            string
	Timezone           string
	Schedule           Schedule
	SessionMode        SessionMode
	// ModelProfileID optionally pins the job to a configured model profile.
	// Empty means "use the current default profile".
	ModelProfileID     string
	WatchdogScript     string
	DeliveryTarget     automation.DeliveryTarget
	Enabled            bool
	CreatedBy          string
	CreatedFromSession string
}

type UpdateJobInput struct {
	ID             string
	Name           *string
	Message        *string
	Timezone       *string
	Schedule       *Schedule
	SessionMode    *SessionMode
	// ModelProfileID is a tri-state: nil means "leave unchanged", empty
	// string means "clear and use the default profile", non-empty means
	// "pin to this profile".
	ModelProfileID *string
	WatchdogScript *string
	DeliveryTarget *automation.DeliveryTarget
	Enabled        *bool
}

type Config struct {
	Enabled           bool
	TickSeconds       int
	DefaultTimezone   string
	MaxConcurrentRuns int
	// WorkspaceDir is the base directory used to resolve a relative
	// watchdog_script path. Passed through to runWatchdog.
	WorkspaceDir string
	// DefaultWatchdogScript is used when a job has no explicit watchdog script.
	DefaultWatchdogScript string
}

type dueJob struct {
	Job Job
	Now time.Time
}

func (j Job) normalize(cfg Config) Job {
	j.Name = strings.TrimSpace(j.Name)
	j.Message = strings.TrimSpace(j.Message)
	j.Timezone = normalizeTimezone(j.Timezone, cfg.DefaultTimezone)
	if j.SessionMode == "" {
		j.SessionMode = SessionModeShared
	}
	j.ModelProfileID = strings.TrimSpace(j.ModelProfileID)
	j.WatchdogScript = strings.TrimSpace(j.WatchdogScript)
	if j.WatchdogScript == "" {
		j.WatchdogScript = strings.TrimSpace(cfg.DefaultWatchdogScript)
	}
	j.DeliveryTarget = j.DeliveryTarget.Clone()
	return j
}

func normalizeTimezone(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return "Local"
	}
	return fallback
}

func validateSchedule(schedule Schedule) error {
	switch schedule.Type {
	case ScheduleAt:
		if schedule.At.IsZero() {
			return fmt.Errorf("at schedule requires a timestamp")
		}
	case ScheduleEvery:
		if schedule.EverySeconds <= 0 {
			return fmt.Errorf("every schedule requires every_seconds > 0")
		}
	case ScheduleCron:
		if strings.TrimSpace(schedule.CronExpr) == "" {
			return fmt.Errorf("cron schedule requires cron_expr")
		}
		if _, err := cron.ParseStandard(strings.TrimSpace(schedule.CronExpr)); err != nil {
			return fmt.Errorf("invalid cron expression: %w", err)
		}
	default:
		return fmt.Errorf("unsupported schedule type %q", schedule.Type)
	}
	return nil
}
