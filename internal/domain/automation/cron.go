package automation

import "time"

type CronSchedule struct {
	Type         string    `json:"type"`
	At           time.Time `json:"at,omitempty"`
	EverySeconds int       `json:"every_seconds,omitempty"`
	CronExpr     string    `json:"cron_expr,omitempty"`
}

type CronJob struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name,omitempty"`
	Message            string         `json:"message"`
	Timezone           string         `json:"timezone,omitempty"`
	Schedule           CronSchedule   `json:"schedule"`
	SessionMode        string         `json:"session_mode,omitempty"`
	// ModelProfileID optionally pins the job to a configured model profile.
	// Empty means "use the current default profile" (and the configured
	// strategy / fallback chain still applies).
	ModelProfileID     string         `json:"model_profile_id,omitempty"`
	// WatchdogScript is an optional shell script run before each job fires.
	// Exit 0 runs the message (agent); non-zero skips this tick (zero tokens);
	// missing script or timeout records an error.
	WatchdogScript     string         `json:"watchdog_script,omitempty"`
	DeliveryTarget     DeliveryTarget `json:"delivery_target,omitempty"`
	Enabled            bool           `json:"enabled"`
	CreatedBy          string         `json:"created_by,omitempty"`
	CreatedFromSession string         `json:"created_from_session,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	LastRunAt          time.Time      `json:"last_run_at,omitempty"`
	NextRunAt          time.Time      `json:"next_run_at,omitempty"`
	LastStatus         string         `json:"last_status,omitempty"`
	LastError          string         `json:"last_error,omitempty"`
}

type CronRunLog struct {
	ID             string         `json:"id"`
	JobID          string         `json:"job_id"`
	SessionID      string         `json:"session_id,omitempty"`
	TurnID         string         `json:"turn_id,omitempty"`
	Status         string         `json:"status"`
	Error          string         `json:"error,omitempty"`
	Suppressed     bool           `json:"suppressed,omitempty"`
	WatchdogOutput string         `json:"watchdog_output,omitempty"`
	DeliveryTarget DeliveryTarget `json:"delivery_target,omitempty"`
	StartedAt      time.Time      `json:"started_at"`
	FinishedAt     time.Time      `json:"finished_at,omitempty"`
}

type CronCreateInput struct {
	Name               string         `json:"name,omitempty"`
	Message            string         `json:"message"`
	Timezone           string         `json:"timezone,omitempty"`
	Schedule           CronSchedule   `json:"schedule"`
	SessionMode        string         `json:"session_mode,omitempty"`
	// ModelProfileID optionally pins the job to a configured model profile.
	// Empty means "use the current default profile".
	ModelProfileID     string         `json:"model_profile_id,omitempty"`
	WatchdogScript     string         `json:"watchdog_script,omitempty"`
	DeliveryTarget     DeliveryTarget `json:"delivery_target,omitempty"`
	Enabled            bool           `json:"enabled"`
	CreatedBy          string         `json:"created_by,omitempty"`
	CreatedFromSession string         `json:"created_from_session,omitempty"`
}

type CronUpdateInput struct {
	ID             string          `json:"id"`
	Name           *string         `json:"name,omitempty"`
	Message        *string         `json:"message,omitempty"`
	Timezone       *string         `json:"timezone,omitempty"`
	Schedule       *CronSchedule   `json:"schedule,omitempty"`
	SessionMode    *string         `json:"session_mode,omitempty"`
	// ModelProfileID is a tri-state: nil means "leave unchanged", empty
	// string means "clear and use the default profile", non-empty means
	// "pin to this profile".
	ModelProfileID *string         `json:"model_profile_id,omitempty"`
	WatchdogScript *string         `json:"watchdog_script,omitempty"`
	DeliveryTarget *DeliveryTarget `json:"delivery_target,omitempty"`
	Enabled        *bool           `json:"enabled,omitempty"`
}
