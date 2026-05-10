package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/services/commands"
)

type cronCommandRuntime interface {
	ListJobs() ([]automation.CronJob, error)
	GetJob(jobID string) (automation.CronJob, error)
	ToggleJob(jobID string, enabled bool) (automation.CronJob, error)
	DeleteJob(jobID string) error
	RunNow(context.Context, string) (automation.CronRunLog, error)
	ListRunLogs(jobID string, limit int) ([]automation.CronRunLog, error)
}

type heartbeatCommandRuntime interface {
	GetRule() (automation.HeartbeatRule, error)
	Toggle(enabled bool) (automation.HeartbeatRule, error)
	TestNow(context.Context) (automation.HeartbeatRunLog, error)
	ListRunLogs(limit int) ([]automation.HeartbeatRunLog, error)
}

// NewCronCommandHandler adapts the cron runtime into the slash-command surface.
func NewCronCommandHandler(runtime cronCommandRuntime) func(context.Context, commands.Command) (commands.Result, error) {
	return func(ctx context.Context, cmd commands.Command) (commands.Result, error) {
		if runtime == nil {
			return commands.Result{Name: "cron", Output: "Cron runtime is unavailable in this process."}, nil
		}
		if len(cmd.Args) == 0 {
			return commands.Result{}, fmt.Errorf("usage: /cron <list|get|run|logs|enable|disable|delete> [job-id]")
		}

		action := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
		args := cmd.Args[1:]
		switch action {
		case "list":
			if len(args) != 0 {
				return commands.Result{}, fmt.Errorf("usage: /cron list")
			}
			jobs, err := runtime.ListJobs()
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "cron", Output: renderCronJobList(jobs)}, nil
		case "get":
			if len(args) != 1 {
				return commands.Result{}, fmt.Errorf("usage: /cron get <job-id>")
			}
			job, err := runtime.GetJob(args[0])
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "cron", Output: renderCronJobDetail(job)}, nil
		case "run":
			if len(args) != 1 {
				return commands.Result{}, fmt.Errorf("usage: /cron run <job-id>")
			}
			run, err := runtime.RunNow(ctx, args[0])
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "cron", Output: renderCronRunResult(run, "Ran cron job")}, nil
		case "logs":
			if len(args) != 1 {
				return commands.Result{}, fmt.Errorf("usage: /cron logs <job-id>")
			}
			runs, err := runtime.ListRunLogs(args[0], 10)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "cron", Output: renderCronRunLogs(args[0], runs)}, nil
		case "enable":
			if len(args) != 1 {
				return commands.Result{}, fmt.Errorf("usage: /cron enable <job-id>")
			}
			job, err := runtime.ToggleJob(args[0], true)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "cron", Output: renderCronToggle(job, true)}, nil
		case "disable":
			if len(args) != 1 {
				return commands.Result{}, fmt.Errorf("usage: /cron disable <job-id>")
			}
			job, err := runtime.ToggleJob(args[0], false)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "cron", Output: renderCronToggle(job, false)}, nil
		case "delete":
			if len(args) != 1 {
				return commands.Result{}, fmt.Errorf("usage: /cron delete <job-id>")
			}
			if err := runtime.DeleteJob(args[0]); err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "cron", Output: fmt.Sprintf("Deleted cron job %s.", args[0])}, nil
		default:
			return commands.Result{}, fmt.Errorf("unknown /cron action %q", action)
		}
	}
}

// NewHeartbeatCommandHandler adapts the heartbeat runtime into the slash-command surface.
func NewHeartbeatCommandHandler(runtime heartbeatCommandRuntime) func(context.Context, commands.Command) (commands.Result, error) {
	return func(ctx context.Context, cmd commands.Command) (commands.Result, error) {
		if runtime == nil {
			return commands.Result{Name: "heartbeat", Output: "Heartbeat runtime is unavailable in this process."}, nil
		}
		if len(cmd.Args) == 0 {
			return commands.Result{}, fmt.Errorf("usage: /heartbeat <get|test|logs|enable|disable>")
		}

		action := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
		args := cmd.Args[1:]
		switch action {
		case "get":
			if len(args) != 0 {
				return commands.Result{}, fmt.Errorf("usage: /heartbeat get")
			}
			rule, err := runtime.GetRule()
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "heartbeat", Output: renderHeartbeatRule(rule)}, nil
		case "test":
			if len(args) != 0 {
				return commands.Result{}, fmt.Errorf("usage: /heartbeat test")
			}
			run, err := runtime.TestNow(ctx)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "heartbeat", Output: renderHeartbeatRunResult(run)}, nil
		case "logs":
			if len(args) != 0 {
				return commands.Result{}, fmt.Errorf("usage: /heartbeat logs")
			}
			runs, err := runtime.ListRunLogs(10)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "heartbeat", Output: renderHeartbeatRunLogs(runs)}, nil
		case "enable":
			if len(args) != 0 {
				return commands.Result{}, fmt.Errorf("usage: /heartbeat enable")
			}
			rule, err := runtime.Toggle(true)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "heartbeat", Output: renderHeartbeatToggle(rule, true)}, nil
		case "disable":
			if len(args) != 0 {
				return commands.Result{}, fmt.Errorf("usage: /heartbeat disable")
			}
			rule, err := runtime.Toggle(false)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "heartbeat", Output: renderHeartbeatToggle(rule, false)}, nil
		default:
			return commands.Result{}, fmt.Errorf("unknown /heartbeat action %q", action)
		}
	}
}

func renderCronJobList(jobs []automation.CronJob) string {
	if len(jobs) == 0 {
		return "No cron jobs configured."
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
	lines := []string{"Cron jobs:"}
	for _, job := range jobs {
		name := strings.TrimSpace(job.Name)
		if name == "" {
			name = "(unnamed)"
		}
		lines = append(lines, fmt.Sprintf(
			"- %s  %s  next=%s  tz=%s  status=%s  name=%s",
			job.ID,
			boolLabel(job.Enabled, "enabled", "disabled"),
			formatTime(job.NextRunAt),
			fallback(job.Timezone, "Local"),
			fallback(job.LastStatus, "pending"),
			name,
		))
	}
	return strings.Join(lines, "\n")
}

func renderCronJobDetail(job automation.CronJob) string {
	lines := []string{
		fmt.Sprintf("Cron job %s", job.ID),
		fmt.Sprintf("Name: %s", fallback(job.Name, "(unnamed)")),
		fmt.Sprintf("Enabled: %t", job.Enabled),
		fmt.Sprintf("Schedule: %s", renderCronSchedule(job.Schedule)),
		fmt.Sprintf("Timezone: %s", fallback(job.Timezone, "Local")),
		fmt.Sprintf("Session mode: %s", fallback(job.SessionMode, "shared")),
		fmt.Sprintf("Delivery target: %s", renderDeliveryTarget(job.DeliveryTarget)),
		fmt.Sprintf("Created by: %s", fallback(job.CreatedBy, "unknown")),
		fmt.Sprintf("Created from session: %s", fallback(job.CreatedFromSession, "-")),
		fmt.Sprintf("Last status: %s", fallback(job.LastStatus, "pending")),
		fmt.Sprintf("Last error: %s", fallback(job.LastError, "-")),
		fmt.Sprintf("Last run: %s", formatTime(job.LastRunAt)),
		fmt.Sprintf("Next run: %s", formatTime(job.NextRunAt)),
		"Message:",
		job.Message,
	}
	return strings.Join(lines, "\n")
}

func renderCronRunResult(run automation.CronRunLog, prefix string) string {
	lines := []string{
		fmt.Sprintf("%s %s.", prefix, run.JobID),
		fmt.Sprintf("Status: %s", run.Status),
		fmt.Sprintf("Started: %s", formatTime(run.StartedAt)),
		fmt.Sprintf("Finished: %s", formatTime(run.FinishedAt)),
		fmt.Sprintf("Session: %s", fallback(run.SessionID, "-")),
		fmt.Sprintf("Turn: %s", fallback(run.TurnID, "-")),
		fmt.Sprintf("Delivery: %s", renderDeliveryTarget(run.DeliveryTarget)),
	}
	if strings.TrimSpace(run.Error) != "" {
		lines = append(lines, fmt.Sprintf("Error: %s", run.Error))
	}
	return strings.Join(lines, "\n")
}

func renderCronRunLogs(jobID string, runs []automation.CronRunLog) string {
	if len(runs) == 0 {
		return fmt.Sprintf("No run logs for cron job %s.", jobID)
	}
	lines := []string{fmt.Sprintf("Cron run logs for %s:", jobID)}
	for _, run := range runs {
		line := fmt.Sprintf("- %s  status=%s  started=%s  finished=%s", run.ID, run.Status, formatTime(run.StartedAt), formatTime(run.FinishedAt))
		if strings.TrimSpace(run.Error) != "" {
			line += "  error=" + run.Error
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderCronToggle(job automation.CronJob, enabled bool) string {
	return fmt.Sprintf(
		"%s cron job %s. Next run: %s. Timezone: %s.",
		boolLabel(enabled, "Enabled", "Disabled"),
		job.ID,
		formatTime(job.NextRunAt),
		fallback(job.Timezone, "Local"),
	)
}

func renderHeartbeatRule(rule automation.HeartbeatRule) string {
	lines := []string{
		fmt.Sprintf("Heartbeat rule %s", rule.ID),
		fmt.Sprintf("Enabled: %t", rule.Enabled),
		fmt.Sprintf("Interval: %ds", rule.IntervalSeconds),
		fmt.Sprintf("Timezone: %s", fallback(rule.Timezone, "Local")),
		fmt.Sprintf("Active hours: %s", renderActiveHours(rule.ActiveHoursStart, rule.ActiveHoursEnd)),
		fmt.Sprintf("Session mode: %s", fallback(rule.SessionMode, "shared")),
		fmt.Sprintf("Delivery target: %s", renderDeliveryTarget(rule.DeliveryTarget)),
		fmt.Sprintf("Prompt override: %s", fallback(rule.PromptOverride, "-")),
		fmt.Sprintf("Created by: %s", fallback(rule.CreatedBy, "unknown")),
		fmt.Sprintf("Created from session: %s", fallback(rule.CreatedFromSession, "-")),
		fmt.Sprintf("Last status: %s", fallback(rule.LastStatus, "pending")),
		fmt.Sprintf("Last error: %s", fallback(rule.LastError, "-")),
		fmt.Sprintf("Last run: %s", formatTime(rule.LastRunAt)),
		fmt.Sprintf("Next run: %s", formatTime(rule.NextRunAt)),
	}
	return strings.Join(lines, "\n")
}

func renderHeartbeatRunResult(run automation.HeartbeatRunLog) string {
	lines := []string{
		fmt.Sprintf("Heartbeat test finished with status %s.", run.Status),
		fmt.Sprintf("Started: %s", formatTime(run.StartedAt)),
		fmt.Sprintf("Finished: %s", formatTime(run.FinishedAt)),
		fmt.Sprintf("Suppressed: %t", run.Suppressed),
		fmt.Sprintf("Session: %s", fallback(run.SessionID, "-")),
		fmt.Sprintf("Turn: %s", fallback(run.TurnID, "-")),
		fmt.Sprintf("Delivery: %s", renderDeliveryTarget(run.DeliveryTarget)),
	}
	if strings.TrimSpace(run.Error) != "" {
		lines = append(lines, fmt.Sprintf("Error: %s", run.Error))
	}
	return strings.Join(lines, "\n")
}

func renderHeartbeatRunLogs(runs []automation.HeartbeatRunLog) string {
	if len(runs) == 0 {
		return "No heartbeat run logs."
	}
	lines := []string{"Heartbeat run logs:"}
	for _, run := range runs {
		line := fmt.Sprintf("- %s  status=%s  started=%s  finished=%s  suppressed=%t", run.ID, run.Status, formatTime(run.StartedAt), formatTime(run.FinishedAt), run.Suppressed)
		if strings.TrimSpace(run.Error) != "" {
			line += "  error=" + run.Error
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderHeartbeatToggle(rule automation.HeartbeatRule, enabled bool) string {
	return fmt.Sprintf(
		"%s heartbeat. Next run: %s. Interval: %ds.",
		boolLabel(enabled, "Enabled", "Disabled"),
		formatTime(rule.NextRunAt),
		rule.IntervalSeconds,
	)
}

func renderCronSchedule(schedule automation.CronSchedule) string {
	switch schedule.Type {
	case "at":
		return "at " + formatTime(schedule.At)
	case "every":
		return fmt.Sprintf("every %ds", schedule.EverySeconds)
	case "cron":
		return "cron " + fallback(schedule.CronExpr, "-")
	default:
		return fallback(schedule.Type, "-")
	}
}

func renderActiveHours(start, end string) string {
	if strings.TrimSpace(start) == "" && strings.TrimSpace(end) == "" {
		return "always"
	}
	return fmt.Sprintf("%s-%s", fallback(start, "?"), fallback(end, "?"))
}

func renderDeliveryTarget(target automation.DeliveryTarget) string {
	if target.IsZero() {
		return "none"
	}
	switch target.Kind {
	case automation.DeliveryKindSession:
		return "session:" + fallback(target.SessionID, target.SessionKey)
	case automation.DeliveryKindChannel:
		parts := []string{fallback(target.Channel, "channel")}
		if strings.TrimSpace(target.SessionKey) != "" {
			parts = append(parts, "session="+target.SessionKey)
		}
		if strings.TrimSpace(target.Recipient) != "" {
			parts = append(parts, "recipient="+target.Recipient)
		}
		return strings.Join(parts, " ")
	default:
		return fallback(target.Kind, "custom")
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format(time.RFC3339)
}

func fallback(value, fallbackValue string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallbackValue
	}
	return value
}

func boolLabel(value bool, yes, no string) string {
	if value {
		return yes
	}
	return no
}
