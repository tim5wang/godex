package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/message"
)

type fakeCronManager struct {
	listJobsResult []automation.CronJob
	getJobResult   automation.CronJob
	runNowResult   automation.CronRunLog
	logsResult     []automation.CronRunLog

	createInput automation.CronCreateInput
	updateInput automation.CronUpdateInput
	toggleID    string
	toggleState bool
	deleteID    string
	runID       string
	logsJobID   string
	logsLimit   int
}

func (m *fakeCronManager) ListJobs() ([]automation.CronJob, error) {
	return append([]automation.CronJob{}, m.listJobsResult...), nil
}

func (m *fakeCronManager) GetJob(jobID string) (automation.CronJob, error) {
	job := m.getJobResult
	if job.ID == "" {
		job.ID = jobID
	}
	return job, nil
}

func (m *fakeCronManager) CreateJob(input automation.CronCreateInput) (automation.CronJob, error) {
	m.createInput = input
	return automation.CronJob{
		ID:                 "job-1",
		Name:               input.Name,
		Message:            input.Message,
		Timezone:           input.Timezone,
		Schedule:           input.Schedule,
		SessionMode:        input.SessionMode,
		DeliveryTarget:     input.DeliveryTarget.Clone(),
		Enabled:            input.Enabled,
		CreatedBy:          input.CreatedBy,
		CreatedFromSession: input.CreatedFromSession,
	}, nil
}

func (m *fakeCronManager) UpdateJob(input automation.CronUpdateInput) (automation.CronJob, error) {
	m.updateInput = input
	return automation.CronJob{ID: input.ID}, nil
}

func (m *fakeCronManager) ToggleJob(jobID string, enabled bool) (automation.CronJob, error) {
	m.toggleID = jobID
	m.toggleState = enabled
	return automation.CronJob{ID: jobID, Enabled: enabled}, nil
}

func (m *fakeCronManager) DeleteJob(jobID string) error {
	m.deleteID = jobID
	return nil
}

func (m *fakeCronManager) RunNow(ctx context.Context, jobID string) (automation.CronRunLog, error) {
	_ = ctx
	m.runID = jobID
	run := m.runNowResult
	if run.JobID == "" {
		run.JobID = jobID
	}
	return run, nil
}

func (m *fakeCronManager) ListRunLogs(jobID string, limit int) ([]automation.CronRunLog, error) {
	m.logsJobID = jobID
	m.logsLimit = limit
	return append([]automation.CronRunLog{}, m.logsResult...), nil
}

func TestCronToolCreateInheritsRuntimeDefaults(t *testing.T) {
	manager := &fakeCronManager{}
	tool := NewCronTool(manager)
	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "web-session",
		Source:    "web",
		Sender:    "taiwu",
		DefaultDelivery: automation.DeliveryTarget{
			Kind:      automation.DeliveryKindSession,
			SessionID: "web-session",
		},
	})

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":        "create",
		"name":          "daily inbox",
		"message":       "check inbox and summarize",
		"schedule_type": "every",
		"every_seconds": float64(3600),
	})
	if err != nil {
		t.Fatalf("execute create: %v", err)
	}
	if manager.createInput.CreatedFromSession != "web-session" {
		t.Fatalf("expected created_from_session to inherit runtime session, got %q", manager.createInput.CreatedFromSession)
	}
	if manager.createInput.CreatedBy != "taiwu" {
		t.Fatalf("expected created_by to inherit sender, got %q", manager.createInput.CreatedBy)
	}
	if manager.createInput.DeliveryTarget.SessionID != "web-session" {
		t.Fatalf("expected default delivery to inherit session target, got %+v", manager.createInput.DeliveryTarget)
	}
	if manager.createInput.SessionMode != "" {
		t.Fatalf("expected empty session mode so service default applies, got %q", manager.createInput.SessionMode)
	}
	if manager.createInput.Schedule.Type != "every" || manager.createInput.Schedule.EverySeconds != 3600 {
		t.Fatalf("unexpected create schedule: %+v", manager.createInput.Schedule)
	}

	var parsed struct {
		Action string             `json:"action"`
		Job    automation.CronJob `json:"job"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if parsed.Action != "create" || parsed.Job.ID == "" {
		t.Fatalf("unexpected create result: %+v", parsed)
	}
}

func TestCronToolUpdateParsesExplicitFields(t *testing.T) {
	manager := &fakeCronManager{}
	tool := NewCronTool(manager)
	target := map[string]interface{}{
		"kind":      automation.DeliveryKindChannel,
		"channel":   "feishu",
		"recipient": "chat-1",
		"metadata": map[string]interface{}{
			"chat_id": "chat-1",
		},
	}

	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":          "update",
		"job_id":          "job-7",
		"name":            "new name",
		"schedule_type":   "cron",
		"cron_expr":       "0 9 * * 1",
		"timezone":        "Asia/Shanghai",
		"session_mode":    "isolated",
		"enabled":         true,
		"delivery_target": target,
	}); err != nil {
		t.Fatalf("execute update: %v", err)
	}

	if manager.updateInput.ID != "job-7" {
		t.Fatalf("unexpected update id: %+v", manager.updateInput)
	}
	if manager.updateInput.Name == nil || *manager.updateInput.Name != "new name" {
		t.Fatalf("expected name update, got %+v", manager.updateInput)
	}
	if manager.updateInput.Schedule == nil || manager.updateInput.Schedule.CronExpr != "0 9 * * 1" {
		t.Fatalf("expected cron schedule update, got %+v", manager.updateInput.Schedule)
	}
	if manager.updateInput.DeliveryTarget == nil || manager.updateInput.DeliveryTarget.Channel != "feishu" {
		t.Fatalf("expected delivery target update, got %+v", manager.updateInput.DeliveryTarget)
	}
	if manager.updateInput.SessionMode == nil || *manager.updateInput.SessionMode != "isolated" {
		t.Fatalf("expected session mode update, got %+v", manager.updateInput.SessionMode)
	}
}

func TestCronToolRunAndLogs(t *testing.T) {
	manager := &fakeCronManager{
		getJobResult: automation.CronJob{ID: "job-5"},
		runNowResult: automation.CronRunLog{ID: "run-1", JobID: "job-5", Status: "completed"},
		logsResult:   []automation.CronRunLog{{ID: "run-2", JobID: "job-5", Status: "completed", StartedAt: time.Now()}},
	}
	tool := NewCronTool(manager)

	runResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "run",
		"job_id": "job-5",
	})
	if err != nil {
		t.Fatalf("execute run: %v", err)
	}
	if manager.runID != "job-5" {
		t.Fatalf("expected run id to be recorded, got %q", manager.runID)
	}
	var runPayload map[string]interface{}
	if err := json.Unmarshal([]byte(runResult), &runPayload); err != nil {
		t.Fatalf("parse run result: %v", err)
	}
	if runPayload["action"] != "run" {
		t.Fatalf("unexpected run payload: %+v", runPayload)
	}

	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "logs",
		"job_id": "job-5",
		"limit":  float64(5),
	}); err != nil {
		t.Fatalf("execute logs: %v", err)
	}
	if manager.logsJobID != "job-5" || manager.logsLimit != 5 {
		t.Fatalf("unexpected logs request: id=%q limit=%d", manager.logsJobID, manager.logsLimit)
	}
}

func TestCronToolCreateRejectedDuringAutomationRun(t *testing.T) {
	manager := &fakeCronManager{}
	handler := NewToolHandler()
	handler.AddBeforeInterceptors(NewPermissionInterceptor(NewPermissionManager(NewAutomationMutationRule())))
	handler.Register(NewCronTool(manager))
	ctx := WithSessionContext(context.Background(), automation.SessionContext{
		SessionID: "cron-session",
		Source:    string(message.SourceCron),
		Sender:    "cron",
	})

	_, err := handler.Handle(ctx, "cron", map[string]interface{}{
		"action":        "create",
		"name":          "follow-up",
		"message":       "remind me tomorrow",
		"schedule_type": "every",
		"every_seconds": float64(3600),
	})
	if err == nil || !strings.Contains(err.Error(), "disabled during active automation runs") {
		t.Fatalf("expected automation mutation error, got %v", err)
	}
	if manager.createInput.Message != "" {
		t.Fatalf("expected manager not to receive create input, got %+v", manager.createInput)
	}
}
