package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
)

const defaultCronLogLimit = 20

// CronManager exposes runtime cron resources to the always-active cron tool.
type CronManager interface {
	ListJobs() ([]automation.CronJob, error)
	GetJob(jobID string) (automation.CronJob, error)
	CreateJob(input automation.CronCreateInput) (automation.CronJob, error)
	UpdateJob(input automation.CronUpdateInput) (automation.CronJob, error)
	ToggleJob(jobID string, enabled bool) (automation.CronJob, error)
	DeleteJob(jobID string) error
	RunNow(ctx context.Context, jobID string) (automation.CronRunLog, error)
	ListRunLogs(jobID string, limit int) ([]automation.CronRunLog, error)
}

type cronArgs struct {
	Action         string                     `json:"action"`
	JobID          string                     `json:"job_id,omitempty"`
	Name           *string                    `json:"name,omitempty"`
	Message        *string                    `json:"message,omitempty"`
	ScheduleType   string                     `json:"schedule_type,omitempty"`
	At             string                     `json:"at,omitempty"`
	EverySeconds   *int                       `json:"every_seconds,omitempty"`
	CronExpr       string                     `json:"cron_expr,omitempty"`
	Timezone       *string                    `json:"timezone,omitempty"`
	SessionMode    *string                    `json:"session_mode,omitempty"`
	DeliveryTarget *automation.DeliveryTarget `json:"delivery_target,omitempty"`
	Enabled        *bool                      `json:"enabled,omitempty"`
	Limit          int                        `json:"limit,omitempty"`
}

// NewCronTool creates a new cron tool.
func NewCronTool(manager CronManager) Tool {
	return NewTypedTool(NewToolSpec("cron", "Create, inspect, update, run, and delete persistent cron jobs for scheduled work.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Cron action to perform",
				"enum":        []string{"create", "update", "toggle", "delete", "get", "list", "run", "logs"},
			},
			"job_id": map[string]interface{}{
				"type":        "string",
				"description": "Existing cron job ID for non-create actions",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Optional human-readable job name",
			},
			"message": map[string]interface{}{
				"type":        "string",
				"description": "Prompt/message to execute when the cron job runs",
			},
			"schedule_type": map[string]interface{}{
				"type":        "string",
				"description": "Schedule kind for create/update",
				"enum":        []string{"at", "every", "cron"},
			},
			"at": map[string]interface{}{
				"type":        "string",
				"description": "RFC3339 timestamp for one-shot jobs",
			},
			"every_seconds": map[string]interface{}{
				"type":        "integer",
				"description": "Recurring interval in seconds for every schedules",
			},
			"cron_expr": map[string]interface{}{
				"type":        "string",
				"description": "Standard 5-field cron expression for cron schedules",
			},
			"timezone": map[string]interface{}{
				"type":        "string",
				"description": "IANA timezone such as Asia/Shanghai",
			},
			"session_mode": map[string]interface{}{
				"type":        "string",
				"description": "Whether runs reuse one cron session or get isolated sessions",
				"enum":        []string{"shared", "isolated"},
			},
			"delivery_target": deliveryTargetSchema(),
			"enabled": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether the job is enabled",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Optional log limit for logs action",
			},
		},
		"required": []string{"action"},
	}, nil), func(ctx context.Context, args cronArgs) (ToolResult, error) {
		if manager == nil {
			return ToolResult{}, fmt.Errorf("cron service is unavailable")
		}
		action := strings.ToLower(strings.TrimSpace(args.Action))
		runtimeCtx := SessionContextFromContext(ctx)

		switch action {
		case "list":
			jobs, err := manager.ListJobs()
			if err != nil {
				return ToolResult{}, err
			}
			sortCronJobs(jobs)
			return ToolResult{Structured: map[string]interface{}{"action": "list", "jobs": jobs}}, nil
		case "get":
			if strings.TrimSpace(args.JobID) == "" {
				return ToolResult{}, fmt.Errorf("missing job_id")
			}
			job, err := manager.GetJob(args.JobID)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"action": "get", "job": job}}, nil
		case "logs":
			if strings.TrimSpace(args.JobID) == "" {
				return ToolResult{}, fmt.Errorf("missing job_id")
			}
			limit := args.Limit
			if limit <= 0 {
				limit = defaultCronLogLimit
			}
			logs, err := manager.ListRunLogs(args.JobID, limit)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{
				"action": "logs",
				"job_id": args.JobID,
				"limit":  limit,
				"runs":   logs,
			}}, nil
		case "run":
			if strings.TrimSpace(args.JobID) == "" {
				return ToolResult{}, fmt.Errorf("missing job_id")
			}
			run, err := manager.RunNow(ctx, args.JobID)
			if err != nil {
				return ToolResult{}, err
			}
			job, err := manager.GetJob(args.JobID)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{
				"action": "run",
				"job":    job,
				"run":    run,
			}}, nil
		case "delete":
			if strings.TrimSpace(args.JobID) == "" {
				return ToolResult{}, fmt.Errorf("missing job_id")
			}
			job, err := manager.GetJob(args.JobID)
			if err != nil {
				return ToolResult{}, err
			}
			if err := manager.DeleteJob(args.JobID); err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{
				"action":  "delete",
				"job_id":  args.JobID,
				"deleted": true,
				"job":     job,
			}}, nil
		case "toggle":
			if strings.TrimSpace(args.JobID) == "" {
				return ToolResult{}, fmt.Errorf("missing job_id")
			}
			if args.Enabled == nil {
				return ToolResult{}, fmt.Errorf("toggle requires enabled")
			}
			job, err := manager.ToggleJob(args.JobID, *args.Enabled)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"action": "toggle", "job": job}}, nil
		case "create":
			if args.Message == nil || strings.TrimSpace(*args.Message) == "" {
				return ToolResult{}, fmt.Errorf("missing message")
			}
			schedule, _, err := parseCronScheduleArgs(args, false)
			if err != nil {
				return ToolResult{}, err
			}
			deliveryTarget := runtimeCtx.DefaultDelivery.Clone()
			if args.DeliveryTarget != nil {
				deliveryTarget = args.DeliveryTarget.Clone()
			}
			createdBy := strings.TrimSpace(runtimeCtx.Sender)
			if createdBy == "" {
				createdBy = strings.TrimSpace(runtimeCtx.Source)
			}
			if createdBy == "" {
				createdBy = "agent"
			}
			name := derefString(args.Name)
			timezone := derefString(args.Timezone)
			sessionMode := derefString(args.SessionMode)
			enabled := true
			if args.Enabled != nil {
				enabled = *args.Enabled
			}
			job, err := manager.CreateJob(automation.CronCreateInput{
				Name:               name,
				Message:            *args.Message,
				Timezone:           timezone,
				Schedule:           schedule,
				SessionMode:        sessionMode,
				DeliveryTarget:     deliveryTarget.Clone(),
				Enabled:            enabled,
				CreatedBy:          createdBy,
				CreatedFromSession: runtimeCtx.SessionID,
			})
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"action": "create", "job": job}}, nil
		case "update":
			if strings.TrimSpace(args.JobID) == "" {
				return ToolResult{}, fmt.Errorf("missing job_id")
			}
			update := automation.CronUpdateInput{ID: args.JobID}
			if args.Name != nil {
				value := *args.Name
				update.Name = &value
			}
			if args.Message != nil {
				value := *args.Message
				update.Message = &value
			}
			if args.Timezone != nil {
				value := *args.Timezone
				update.Timezone = &value
			}
			if args.SessionMode != nil {
				value := *args.SessionMode
				update.SessionMode = &value
			}
			if args.Enabled != nil {
				value := *args.Enabled
				update.Enabled = &value
			}
			if schedule, ok, err := parseCronScheduleArgs(args, true); err != nil {
				return ToolResult{}, err
			} else if ok {
				update.Schedule = &schedule
			}
			if args.DeliveryTarget != nil {
				target := args.DeliveryTarget.Clone()
				update.DeliveryTarget = &target
			}
			job, err := manager.UpdateJob(update)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"action": "update", "job": job}}, nil
		default:
			return ToolResult{}, fmt.Errorf("unsupported cron action %q", action)
		}
	})
}

func deliveryTargetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":        "object",
		"description": "Optional explicit delivery target. If omitted, the current session/channel is inherited.",
		"properties": map[string]interface{}{
			"kind":        map[string]interface{}{"type": "string", "enum": []string{automation.DeliveryKindSession, automation.DeliveryKindChannel}},
			"session_id":  map[string]interface{}{"type": "string"},
			"channel":     map[string]interface{}{"type": "string"},
			"session_key": map[string]interface{}{"type": "string"},
			"recipient":   map[string]interface{}{"type": "string"},
			"metadata": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": map[string]interface{}{"type": "string"},
			},
		},
	}
}

func parseCronScheduleArgs(args cronArgs, allowAbsent bool) (automation.CronSchedule, bool, error) {
	typeProvided := strings.TrimSpace(args.ScheduleType) != ""
	atProvided := strings.TrimSpace(args.At) != ""
	everyProvided := args.EverySeconds != nil
	cronProvided := strings.TrimSpace(args.CronExpr) != ""
	if !typeProvided && !atProvided && !everyProvided && !cronProvided {
		if allowAbsent {
			return automation.CronSchedule{}, false, nil
		}
		return automation.CronSchedule{}, false, fmt.Errorf("missing schedule_type")
	}

	scheduleType := strings.ToLower(strings.TrimSpace(args.ScheduleType))
	if scheduleType == "" {
		switch {
		case atProvided:
			scheduleType = "at"
		case everyProvided:
			scheduleType = "every"
		case cronProvided:
			scheduleType = "cron"
		default:
			return automation.CronSchedule{}, false, fmt.Errorf("missing schedule_type")
		}
	}

	schedule := automation.CronSchedule{Type: scheduleType}
	switch schedule.Type {
	case "at":
		if !atProvided {
			return automation.CronSchedule{}, false, fmt.Errorf("at schedule requires at")
		}
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(args.At))
		if err != nil {
			return automation.CronSchedule{}, false, fmt.Errorf("at must be RFC3339: %w", err)
		}
		schedule.At = parsed
	case "every":
		if !everyProvided || args.EverySeconds == nil || *args.EverySeconds <= 0 {
			return automation.CronSchedule{}, false, fmt.Errorf("every schedule requires every_seconds > 0")
		}
		schedule.EverySeconds = *args.EverySeconds
	case "cron":
		if !cronProvided {
			return automation.CronSchedule{}, false, fmt.Errorf("cron schedule requires cron_expr")
		}
		schedule.CronExpr = strings.TrimSpace(args.CronExpr)
	default:
		return automation.CronSchedule{}, false, fmt.Errorf("unsupported schedule_type %q", schedule.Type)
	}
	return schedule, true, nil
}

func sortCronJobs(jobs []automation.CronJob) {
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
