package automation

import "time"

type HeartbeatRule struct {
	ID                 string         `json:"id"`
	Enabled            bool           `json:"enabled"`
	IntervalSeconds    int            `json:"interval_seconds"`
	Timezone           string         `json:"timezone,omitempty"`
	ActiveHoursStart   string         `json:"active_hours_start,omitempty"`
	ActiveHoursEnd     string         `json:"active_hours_end,omitempty"`
	SessionMode        string         `json:"session_mode,omitempty"`
	DeliveryTarget     DeliveryTarget `json:"delivery_target,omitempty"`
	PromptOverride     string         `json:"prompt_override,omitempty"`
	WatchdogScript     string         `json:"watchdog_script,omitempty"`
	CreatedBy          string         `json:"created_by,omitempty"`
	CreatedFromSession string         `json:"created_from_session,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	LastRunAt          time.Time      `json:"last_run_at,omitempty"`
	NextRunAt          time.Time      `json:"next_run_at,omitempty"`
	LastStatus         string         `json:"last_status,omitempty"`
	LastError          string         `json:"last_error,omitempty"`
}

type HeartbeatRunLog struct {
	ID             string         `json:"id"`
	RuleID         string         `json:"rule_id"`
	SessionID      string         `json:"session_id,omitempty"`
	TurnID         string         `json:"turn_id,omitempty"`
	Status         string         `json:"status"`
	Error          string         `json:"error,omitempty"`
	Suppressed     bool           `json:"suppressed,omitempty"`
	DeliveryTarget DeliveryTarget `json:"delivery_target,omitempty"`
	StartedAt      time.Time      `json:"started_at"`
	FinishedAt     time.Time      `json:"finished_at,omitempty"`
}

type HeartbeatSetInput struct {
	Enabled            *bool           `json:"enabled,omitempty"`
	IntervalSeconds    *int            `json:"interval_seconds,omitempty"`
	Timezone           *string         `json:"timezone,omitempty"`
	ActiveHoursStart   *string         `json:"active_hours_start,omitempty"`
	ActiveHoursEnd     *string         `json:"active_hours_end,omitempty"`
	SessionMode        *string         `json:"session_mode,omitempty"`
	DeliveryTarget     *DeliveryTarget `json:"delivery_target,omitempty"`
	PromptOverride     *string         `json:"prompt_override,omitempty"`
	WatchdogScript     *string         `json:"watchdog_script,omitempty"`
	CreatedBy          string          `json:"created_by,omitempty"`
	CreatedFromSession string          `json:"created_from_session,omitempty"`
}
