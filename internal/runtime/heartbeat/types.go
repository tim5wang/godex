package heartbeat

import (
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
)

type SessionMode string

const (
	SessionModeShared   SessionMode = "shared"
	SessionModeIsolated SessionMode = "isolated"
)

type RuleStatus string

const (
	RuleStatusPending         RuleStatus = "pending"
	RuleStatusRunning         RuleStatus = "running"
	RuleStatusCompleted       RuleStatus = "completed"
	RuleStatusSuppressed      RuleStatus = "suppressed"
	RuleStatusDeliveryBlocked RuleStatus = "delivery_blocked"
	RuleStatusError           RuleStatus = "error"
)

const defaultRuleID = "default"

type Rule struct {
	ID                 string                    `json:"id"`
	Enabled            bool                      `json:"enabled"`
	IntervalSeconds    int                       `json:"interval_seconds"`
	Timezone           string                    `json:"timezone,omitempty"`
	ActiveHoursStart   string                    `json:"active_hours_start,omitempty"`
	ActiveHoursEnd     string                    `json:"active_hours_end,omitempty"`
	SessionMode        SessionMode               `json:"session_mode,omitempty"`
	DeliveryTarget     automation.DeliveryTarget `json:"delivery_target,omitempty"`
	PromptOverride     string                    `json:"prompt_override,omitempty"`
	CreatedBy          string                    `json:"created_by,omitempty"`
	CreatedFromSession string                    `json:"created_from_session,omitempty"`
	CreatedAt          time.Time                 `json:"created_at"`
	UpdatedAt          time.Time                 `json:"updated_at"`
	LastRunAt          time.Time                 `json:"last_run_at,omitempty"`
	NextRunAt          time.Time                 `json:"next_run_at,omitempty"`
	LastStatus         RuleStatus                `json:"last_status,omitempty"`
	LastError          string                    `json:"last_error,omitempty"`
}

type RunLog struct {
	ID             string                    `json:"id"`
	RuleID         string                    `json:"rule_id"`
	SessionID      string                    `json:"session_id,omitempty"`
	TurnID         string                    `json:"turn_id,omitempty"`
	Status         RuleStatus                `json:"status"`
	Error          string                    `json:"error,omitempty"`
	Suppressed     bool                      `json:"suppressed,omitempty"`
	DeliveryTarget automation.DeliveryTarget `json:"delivery_target,omitempty"`
	StartedAt      time.Time                 `json:"started_at"`
	FinishedAt     time.Time                 `json:"finished_at,omitempty"`
}

type SetRuleInput struct {
	Enabled            *bool
	IntervalSeconds    *int
	Timezone           *string
	ActiveHoursStart   *string
	ActiveHoursEnd     *string
	SessionMode        *SessionMode
	DeliveryTarget     *automation.DeliveryTarget
	PromptOverride     *string
	CreatedBy          string
	CreatedFromSession string
}

type Config struct {
	Enabled                bool
	TickSeconds            int
	ChecklistPath          string
	WorkspaceDir           string
	StateDir               string
	OKToken                string
	DefaultIntervalSeconds int
	DefaultTimezone        string
}

func normalizeConfig(cfg Config) Config {
	if cfg.TickSeconds <= 0 {
		cfg.TickSeconds = 30
	}
	if strings.TrimSpace(cfg.OKToken) == "" {
		cfg.OKToken = "HEARTBEAT_OK"
	}
	if cfg.DefaultIntervalSeconds <= 0 {
		cfg.DefaultIntervalSeconds = 1800
	}
	if strings.TrimSpace(cfg.DefaultTimezone) == "" {
		cfg.DefaultTimezone = "Local"
	}
	return cfg
}

func (r Rule) normalize(cfg Config) Rule {
	r.ID = defaultRuleID
	if r.IntervalSeconds <= 0 {
		r.IntervalSeconds = cfg.DefaultIntervalSeconds
	}
	if strings.TrimSpace(r.Timezone) == "" {
		r.Timezone = cfg.DefaultTimezone
	}
	if r.SessionMode == "" {
		r.SessionMode = SessionModeShared
	}
	r.DeliveryTarget = r.DeliveryTarget.Clone()
	return r
}

func validateRule(rule Rule) error {
	if rule.IntervalSeconds <= 0 {
		return fmt.Errorf("heartbeat interval_seconds must be > 0")
	}
	switch rule.SessionMode {
	case SessionModeShared, SessionModeIsolated:
	default:
		return fmt.Errorf("unsupported heartbeat session mode %q", rule.SessionMode)
	}
	if _, err := time.LoadLocation(rule.Timezone); err != nil {
		return fmt.Errorf("invalid heartbeat timezone %q: %w", rule.Timezone, err)
	}
	if (rule.ActiveHoursStart == "") != (rule.ActiveHoursEnd == "") {
		return fmt.Errorf("heartbeat active hours require both start and end")
	}
	if rule.ActiveHoursStart != "" {
		if _, err := parseClock(rule.ActiveHoursStart); err != nil {
			return fmt.Errorf("invalid active_hours_start: %w", err)
		}
		if _, err := parseClock(rule.ActiveHoursEnd); err != nil {
			return fmt.Errorf("invalid active_hours_end: %w", err)
		}
	}
	return nil
}

func parseClock(value string) (int, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}
