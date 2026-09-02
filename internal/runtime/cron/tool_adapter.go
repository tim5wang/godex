package cron

import (
	"context"

	"github.com/tim5wang/godex/internal/domain/automation"
)

// ToolAdapter exposes cron service operations through tool-friendly neutral models.
type ToolAdapter struct {
	service *Service
}

func NewToolAdapter(service *Service) *ToolAdapter {
	return &ToolAdapter{service: service}
}

func (a *ToolAdapter) ListJobs() ([]automation.CronJob, error) {
	jobs, err := a.service.ListJobs()
	if err != nil {
		return nil, err
	}
	out := make([]automation.CronJob, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, toAutomationJob(job))
	}
	return out, nil
}

func (a *ToolAdapter) GetJob(jobID string) (automation.CronJob, error) {
	job, err := a.service.GetJob(jobID)
	if err != nil {
		return automation.CronJob{}, err
	}
	return toAutomationJob(job), nil
}

func (a *ToolAdapter) CreateJob(input automation.CronCreateInput) (automation.CronJob, error) {
	job, err := a.service.CreateJob(CreateJobInput{
		Name:               input.Name,
		Message:            input.Message,
		Timezone:           input.Timezone,
		Schedule:           fromAutomationSchedule(input.Schedule),
		SessionMode:        SessionMode(input.SessionMode),
		ModelProfileID:     input.ModelProfileID,
		WatchdogScript:     input.WatchdogScript,
		WatchdogDirective:  input.WatchdogDirective,
		DeliveryTarget:     input.DeliveryTarget.Clone(),
		Enabled:            input.Enabled,
		CreatedBy:          input.CreatedBy,
		CreatedFromSession: input.CreatedFromSession,
	})
	if err != nil {
		return automation.CronJob{}, err
	}
	return toAutomationJob(job), nil
}

func (a *ToolAdapter) UpdateJob(input automation.CronUpdateInput) (automation.CronJob, error) {
	update := UpdateJobInput{ID: input.ID}
	if input.Name != nil {
		update.Name = input.Name
	}
	if input.Message != nil {
		update.Message = input.Message
	}
	if input.Timezone != nil {
		update.Timezone = input.Timezone
	}
	if input.Schedule != nil {
		schedule := fromAutomationSchedule(*input.Schedule)
		update.Schedule = &schedule
	}
	if input.SessionMode != nil {
		mode := SessionMode(*input.SessionMode)
		update.SessionMode = &mode
	}
	if input.ModelProfileID != nil {
		profile := *input.ModelProfileID
		update.ModelProfileID = &profile
	}
	if input.WatchdogScript != nil {
		script := *input.WatchdogScript
		update.WatchdogScript = &script
	}
	if input.DeliveryTarget != nil {
		target := input.DeliveryTarget.Clone()
		update.DeliveryTarget = &target
	}
	if input.Enabled != nil {
		update.Enabled = input.Enabled
	}
	job, err := a.service.UpdateJob(update)
	if err != nil {
		return automation.CronJob{}, err
	}
	return toAutomationJob(job), nil
}

func (a *ToolAdapter) ToggleJob(jobID string, enabled bool) (automation.CronJob, error) {
	job, err := a.service.ToggleJob(jobID, enabled)
	if err != nil {
		return automation.CronJob{}, err
	}
	return toAutomationJob(job), nil
}

func (a *ToolAdapter) DeleteJob(jobID string) error {
	return a.service.DeleteJob(jobID)
}

func (a *ToolAdapter) RunNow(ctx context.Context, jobID string) (automation.CronRunLog, error) {
	run, err := a.service.RunNow(ctx, jobID)
	if err != nil {
		return automation.CronRunLog{}, err
	}
	return toAutomationRunLog(run), nil
}

func (a *ToolAdapter) ListRunLogs(jobID string, limit int) ([]automation.CronRunLog, error) {
	runs, err := a.service.ListRunLogs(jobID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]automation.CronRunLog, 0, len(runs))
	for _, run := range runs {
		out = append(out, toAutomationRunLog(run))
	}
	return out, nil
}

func toAutomationJob(job Job) automation.CronJob {
	return automation.CronJob{
		ID:                 job.ID,
		Name:               job.Name,
		Message:            job.Message,
		Timezone:           job.Timezone,
		Schedule:           toAutomationSchedule(job.Schedule),
		SessionMode:        string(job.SessionMode),
		ModelProfileID:     job.ModelProfileID,
		WatchdogScript:     job.WatchdogScript,
		WatchdogDirective:  job.WatchdogDirective,
		DeliveryTarget:     job.DeliveryTarget.Clone(),
		Enabled:            job.Enabled,
		CreatedBy:          job.CreatedBy,
		CreatedFromSession: job.CreatedFromSession,
		CreatedAt:          job.CreatedAt,
		UpdatedAt:          job.UpdatedAt,
		LastRunAt:          job.LastRunAt,
		NextRunAt:          job.NextRunAt,
		LastStatus:         string(job.LastStatus),
		LastError:          job.LastError,
	}
}

func toAutomationSchedule(schedule Schedule) automation.CronSchedule {
	return automation.CronSchedule{
		Type:         string(schedule.Type),
		At:           schedule.At,
		EverySeconds: schedule.EverySeconds,
		CronExpr:     schedule.CronExpr,
	}
}

func fromAutomationSchedule(schedule automation.CronSchedule) Schedule {
	return Schedule{
		Type:         ScheduleType(schedule.Type),
		At:           schedule.At,
		EverySeconds: schedule.EverySeconds,
		CronExpr:     schedule.CronExpr,
	}
}

func toAutomationRunLog(run RunLog) automation.CronRunLog {
	return automation.CronRunLog{
		ID:             run.ID,
		JobID:          run.JobID,
		SessionID:      run.SessionID,
		TurnID:         run.TurnID,
		Status:         string(run.Status),
		Error:          run.Error,
		Suppressed:     run.Suppressed,
		WatchdogOutput: run.WatchdogOutput,
		DeliveryTarget: run.DeliveryTarget.Clone(),
		StartedAt:      run.StartedAt,
		FinishedAt:     run.FinishedAt,
	}
}
