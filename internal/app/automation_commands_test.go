package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/services/commands"
)

type fakeCronRuntime struct {
	jobs []automation.CronJob
	runs []automation.CronRunLog
}

func (f fakeCronRuntime) ListJobs() ([]automation.CronJob, error) { return f.jobs, nil }
func (f fakeCronRuntime) GetJob(jobID string) (automation.CronJob, error) {
	for _, job := range f.jobs {
		if job.ID == jobID {
			return job, nil
		}
	}
	return automation.CronJob{}, nil
}
func (f fakeCronRuntime) ToggleJob(jobID string, enabled bool) (automation.CronJob, error) {
	job, _ := f.GetJob(jobID)
	job.Enabled = enabled
	return job, nil
}
func (f fakeCronRuntime) DeleteJob(jobID string) error { return nil }
func (f fakeCronRuntime) RunNow(ctx context.Context, jobID string) (automation.CronRunLog, error) {
	if len(f.runs) > 0 {
		return f.runs[0], nil
	}
	return automation.CronRunLog{JobID: jobID, Status: "completed", StartedAt: time.Now(), FinishedAt: time.Now()}, nil
}
func (f fakeCronRuntime) ListRunLogs(jobID string, limit int) ([]automation.CronRunLog, error) {
	return f.runs, nil
}

type fakeHeartbeatRuntime struct {
	rule automation.HeartbeatRule
	runs []automation.HeartbeatRunLog
}

func (f fakeHeartbeatRuntime) GetRule() (automation.HeartbeatRule, error) { return f.rule, nil }
func (f fakeHeartbeatRuntime) Toggle(enabled bool) (automation.HeartbeatRule, error) {
	f.rule.Enabled = enabled
	return f.rule, nil
}
func (f fakeHeartbeatRuntime) TestNow(ctx context.Context) (automation.HeartbeatRunLog, error) {
	if len(f.runs) > 0 {
		return f.runs[0], nil
	}
	return automation.HeartbeatRunLog{RuleID: "default", Status: "completed", StartedAt: time.Now(), FinishedAt: time.Now()}, nil
}
func (f fakeHeartbeatRuntime) ListRunLogs(limit int) ([]automation.HeartbeatRunLog, error) {
	return f.runs, nil
}

func TestCronCommandHandlerListRendersJobs(t *testing.T) {
	handler := NewCronCommandHandler(fakeCronRuntime{
		jobs: []automation.CronJob{
			{
				ID:         "job-1",
				Name:       "daily inbox",
				Enabled:    true,
				Timezone:   "Asia/Shanghai",
				Schedule:   automation.CronSchedule{Type: "every", EverySeconds: 3600},
				NextRunAt:  time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
				LastStatus: "completed",
			},
		},
	})
	result, err := handler(context.Background(), commands.Command{Name: "cron", Args: []string{"list"}})
	if err != nil {
		t.Fatalf("cron list: %v", err)
	}
	if !strings.Contains(result.Output, "job-1") || !strings.Contains(result.Output, "daily inbox") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestCronCommandHandlerRequiresJobIDForEnable(t *testing.T) {
	handler := NewCronCommandHandler(fakeCronRuntime{})
	_, err := handler(context.Background(), commands.Command{Name: "cron", Args: []string{"enable"}})
	if err == nil || !strings.Contains(err.Error(), "usage: /cron enable <job-id>") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestHeartbeatCommandHandlerTestRendersRun(t *testing.T) {
	handler := NewHeartbeatCommandHandler(fakeHeartbeatRuntime{
		runs: []automation.HeartbeatRunLog{
			{
				RuleID:     "default",
				Status:     "suppressed",
				Suppressed: true,
				StartedAt:  time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
				FinishedAt: time.Date(2026, 4, 21, 9, 0, 1, 0, time.UTC),
			},
		},
	})
	result, err := handler(context.Background(), commands.Command{Name: "heartbeat", Args: []string{"test"}})
	if err != nil {
		t.Fatalf("heartbeat test: %v", err)
	}
	if !strings.Contains(result.Output, "suppressed") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}
