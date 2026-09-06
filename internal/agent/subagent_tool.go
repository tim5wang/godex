package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/tools"
)

func (a *Agent) registerSubagentTool(handler *tools.ToolHandler) {
	a.registerToolTo(handler, newSubagentTool(a), tools.ToolMeta{
		Bundle:  bundleSubagent,
		Summary: "isolated delegated exploration or implementation work",
	})
}

type subagentArgs struct {
	Action          string              `json:"action,omitempty"`
	JobID           string              `json:"job_id,omitempty"`
	JobIDs          []string            `json:"job_ids,omitempty"`
	Prompt          string              `json:"prompt,omitempty"`
	AgentType       string              `json:"agent_type,omitempty"`
	Mode            string              `json:"mode,omitempty"`
	WriteScope      []string            `json:"write_scope,omitempty"`
	RequiredBundles []string            `json:"required_bundles,omitempty"`
	RequiredTools   []string            `json:"required_tools,omitempty"`
	BundleOverrides []string            `json:"bundle_overrides,omitempty"`
	DeactivateBundles []string          `json:"deactivate_bundles,omitempty"`
	Limit           int                 `json:"limit,omitempty"`
	TimeoutMS       int                 `json:"timeout_ms,omitempty"`
	JobTimeoutMS    int                 `json:"job_timeout_ms,omitempty"`
	MaxTurns        int                 `json:"max_turns,omitempty"`
	Wait            bool                `json:"wait,omitempty"`
	Input           string              `json:"input,omitempty"`
	Tasks           []subagentBatchItem `json:"tasks,omitempty"`
}

type subagentBatchItem struct {
	Prompt          string   `json:"prompt,omitempty"`
	AgentType       string   `json:"agent_type,omitempty"`
	WriteScope      []string `json:"write_scope,omitempty"`
	RequiredBundles []string `json:"required_bundles,omitempty"`
	RequiredTools   []string `json:"required_tools,omitempty"`
	BundleOverrides []string `json:"bundle_overrides,omitempty"`
	DeactivateBundles []string `json:"deactivate_bundles,omitempty"`
	JobTimeoutMS    int      `json:"job_timeout_ms,omitempty"`
	MaxTurns        int      `json:"max_turns,omitempty"`
}

type subagentLogsView struct {
	JobID    string                        `json:"job_id"`
	Status   string                        `json:"status,omitempty"`
	Count    int                           `json:"count"`
	Total    int                           `json:"total"`
	Progress []DurableSubagentProgressView `json:"progress"`
}

type subagentModelJobView struct {
	JobID          string    `json:"job_id"`
	SessionID      string    `json:"session_id,omitempty"`
	ParentTurnID   string    `json:"parent_turn_id,omitempty"`
	IdentityID     string    `json:"identity_id,omitempty"`
	WorkerID       string    `json:"worker_id,omitempty"`
	SourceBranchID string    `json:"source_branch_id,omitempty"`
	SourceNodeID   string    `json:"source_node_id,omitempty"`
	WorkerBranchID string    `json:"worker_branch_id,omitempty"`
	AgentType      string    `json:"agent_type,omitempty"`
	RoleID         string    `json:"role_id,omitempty"`
	RoleName       string    `json:"role_name,omitempty"`
	SandboxID      string    `json:"sandbox_id,omitempty"`
	Status         string    `json:"status,omitempty"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	MergeStatus    string    `json:"merge_status,omitempty"`
	LastPhase      string    `json:"last_phase,omitempty"`
	LastMessage    string    `json:"last_message,omitempty"`
	LastToolName   string    `json:"last_tool_name,omitempty"`
	ProgressCount  int       `json:"progress_count"`
	ResultPreview  string    `json:"result_preview,omitempty"`
	ResultBytes    int       `json:"result_bytes,omitempty"`
	ResultDigest   string    `json:"result_digest,omitempty"`
}

type subagentBatchView struct {
	Status  string                   `json:"status"`
	Total   int                      `json:"total"`
	Started int                      `json:"started"`
	Failed  int                      `json:"failed"`
	Jobs    []subagentModelJobView   `json:"jobs,omitempty"`
	Errors  []subagentBatchErrorView `json:"errors,omitempty"`
	Wait    *subagentWaitView        `json:"wait,omitempty"`
}

type subagentBatchErrorView struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

type subagentWaitView struct {
	Status    string                 `json:"status"`
	Mode      string                 `json:"mode"`
	TimeoutMS int                    `json:"timeout_ms"`
	Total     int                    `json:"total"`
	Completed int                    `json:"completed"`
	Running   int                    `json:"running"`
	Failed    int                    `json:"failed"`
	Jobs      []subagentModelJobView `json:"jobs"`
}

type subagentRunView struct {
	Status  string               `json:"status"`
	JobID   string               `json:"job_id"`
	Job     subagentModelJobView `json:"job"`
	Wait    subagentWaitView     `json:"wait"`
	Result  string               `json:"result,omitempty"`
	Timeout bool                 `json:"timeout,omitempty"`
}

const (
	defaultSubagentWaitTimeoutMS = 30000
	maxSubagentWaitTimeoutMS     = 120000
	subagentResultPreviewLimit   = 2000
)

func newSubagentTool(agent *Agent) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("subagent", "Run or manage durable subagents for isolated exploration or work. Use action='run' to create a visible durable job and wait for its result, 'start' for one durable background job, 'batch' for multiple durable jobs, and 'wait' to wait for any/all durable jobs. Use 'status' for compact state, 'logs' only for bounded progress diagnostics, and 'review'/'merge' for diffs. Prefer wait over repeated status polling.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"run", "start", "batch", "wait", "status", "logs", "list", "cancel", "resume", "review", "merge", "send_input", "followup_task", "iterate"},
				"description": "Subagent action to perform",
			},
			"job_id": map[string]string{
				"type":        "string",
				"description": "Durable subagent job id for status, logs, wait, cancel, resume, review, or merge",
			},
			"job_ids": map[string]interface{}{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Durable subagent job ids for action='wait'",
			},
			"prompt": map[string]string{
				"type":        "string",
				"description": "The task prompt for the subagent",
			},
			"agent_type": map[string]interface{}{
				"type":        "string",
				"description": "Type or named role of subagent to spawn (start), or new role for an iterate re-open (roadmap 4.5). Explore is read-only; general-purpose can write within write_scope; package role ids are preserved for visualization and prompt guidance.",
			},
			"mode": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"sync", "async", "any", "all"},
				"description": "Compatibility alias: async starts a durable job; sync starts a visible durable job and waits. For action='wait', choose any or all.",
			},
			"write_scope": map[string]interface{}{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Workspace-relative paths a write-capable durable subagent may edit (start), or updated scope for an iterate re-open (roadmap 4.5).",
			},
			"required_bundles": map[string]interface{}{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Tool bundles this subagent needs, such as web for web_search/web_fetch. The parent agent must have the bundle active before it can be inherited.",
			},
			"required_tools": map[string]interface{}{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Specific tools this subagent needs. Tools are inherited only when active in the parent agent.",
			},
			"bundle_overrides": map[string]interface{}{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Replace the inherited parent-agent bundles with this explicit bundle set (start) or for an iterate re-open (roadmap 4.5). Overrides bundle inheritance (roadmap 4.4).",
			},
			"deactivate_bundles": map[string]interface{}{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Remove these bundles from the subagent's inherited bundle set (start) or for an iterate re-open (roadmap 4.5).",
			},
			"input": map[string]string{
				"type":        "string",
				"description": "Message to send to a running subagent (send_input), follow-up task prompt (followup_task), or review feedback for a fix iteration (iterate).",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "For action='logs', number of recent progress events to return. Defaults to 20 and caps at 80.",
			},
			"timeout_ms": map[string]interface{}{
				"type":        "integer",
				"description": "For action='wait' or batch wait=true, wait timeout in milliseconds. Defaults to 30000 and caps at 120000.",
			},
			"job_timeout_ms": map[string]interface{}{
				"type":        "integer",
				"description": "For action='start', optional per-job runtime timeout in milliseconds. Defaults to 240000 for detected web-research tasks and disabled otherwise; caps at tools.subagent.max_job_timeout_ms.",
			},
			"wait": map[string]interface{}{
				"type":        "boolean",
				"description": "For action='batch', wait for started jobs after launching them.",
			},
			"tasks": map[string]interface{}{
				"type":        "array",
				"description": "For action='batch', durable subagent jobs to start, capped by tools.subagent.max_batch_size.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"prompt": map[string]string{
							"type":        "string",
							"description": "The task prompt for this subagent.",
						},
						"agent_type": map[string]string{
							"type":        "string",
							"description": "Type or named role of this subagent.",
						},
						"write_scope": map[string]interface{}{
							"type":        "array",
							"items":       map[string]string{"type": "string"},
							"description": "Workspace-relative paths this durable subagent may edit.",
						},
						"required_bundles": map[string]interface{}{
							"type":        "array",
							"items":       map[string]string{"type": "string"},
							"description": "Tool bundles this subagent needs, such as web.",
						},
						"required_tools": map[string]interface{}{
							"type":        "array",
							"items":       map[string]string{"type": "string"},
							"description": "Specific active parent tools this subagent needs.",
						},
						"job_timeout_ms": map[string]interface{}{
							"type":        "integer",
							"description": "Optional per-job runtime timeout in milliseconds. Defaults to 240000 for detected web-research tasks and disabled otherwise; caps at tools.subagent.max_job_timeout_ms.",
						},
					},
				},
			},
		},
	}, nil), func(ctx context.Context, args subagentArgs) (tools.ToolResult, error) {
		action := strings.ToLower(strings.TrimSpace(args.Action))
		if action == "" {
			if strings.EqualFold(strings.TrimSpace(args.Mode), "async") {
				action = "start"
			} else {
				action = "run"
			}
		}
		switch action {
		case "list":
			return tools.ToolResult{Structured: formatSubagentJobList(agent.subagentJobs.List())}, nil
		case "status":
			job, err := agent.subagentJobs.Get(args.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: formatSubagentModelJob(job)}, nil
		case "logs":
			job, err := agent.subagentJobs.Get(args.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: formatSubagentLogs(job, args.Limit)}, nil
		case "wait":
			result, err := waitSubagents(ctx, agent, subagentWaitRequest{
				JobID:     args.JobID,
				JobIDs:    args.JobIDs,
				Mode:      args.Mode,
				TimeoutMS: args.TimeoutMS,
			})
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: result}, nil
		case "cancel":
			view, err := agent.CancelDurableSubagentWithContext(ctx, "", args.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			job, err := agent.subagentJobs.Get(view.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: formatSubagentModelJob(job)}, nil
		case "resume":
			job, err := agent.ResumeDurableSubagentWithContext(ctx, args.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: formatSubagentModelJob(job)}, nil
		case "review":
			review, err := agent.ReviewDurableSubagent(args.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: review}, nil
		case "merge":
			result, err := agent.MergeDurableSubagentWithContext(ctx, args.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: result}, nil
		case "send_input", "followup_task":
			input := strings.TrimSpace(args.Input)
			if input == "" {
				return tools.ToolResult{}, fmt.Errorf("missing input argument")
			}
			job, err := agent.subagentJobs.AppendPendingInputs(args.JobID, []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, input)})
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: formatSubagentModelJob(job)}, nil
		case "iterate":
			feedback := strings.TrimSpace(args.Input)
			if feedback == "" {
				return tools.ToolResult{}, fmt.Errorf("missing input (review feedback) argument")
			}
			// roadmap 4.5: iterate 可携带可选配置更新（角色/写 scope/bundle），
			// 重开时自动重新解析并更新 job 的 ToolNames/DefaultBundles 等。
			job, err := agent.IterateDurableSubagentWithUpdate(ctx, args.JobID, feedback, subagentReopenUpdate{
				AgentType:         args.AgentType,
				WriteScope:        args.WriteScope,
				BundleOverrides:   args.BundleOverrides,
				DeactivateBundles: args.DeactivateBundles,
			})
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: formatSubagentModelJob(job)}, nil
		case "start":
			prompt := strings.TrimSpace(args.Prompt)
			if prompt == "" {
				return tools.ToolResult{}, fmt.Errorf("missing prompt argument")
			}
			job, err := agent.startDurableSubagentWithContext(ctx, durableSubagentStartRequest{
				Prompt:            prompt,
				AgentType:         args.AgentType,
				WriteScope:        args.WriteScope,
				RequiredBundles:   args.RequiredBundles,
				RequiredTools:     args.RequiredTools,
				BundleOverrides:   args.BundleOverrides,
				DeactivateBundles: args.DeactivateBundles,
				MaxTurns:          args.MaxTurns,
				JobTimeoutMS:      args.JobTimeoutMS,
			})
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: formatSubagentModelJob(job)}, nil
		case "batch":
			result := startSubagentBatch(ctx, agent, args.Tasks, args.Wait, args.TimeoutMS)
			return tools.ToolResult{Structured: result}, nil
		case "run":
		default:
			return tools.ToolResult{}, fmt.Errorf("unsupported subagent action %q", action)
		}
		prompt := strings.TrimSpace(args.Prompt)
		if prompt == "" {
			return tools.ToolResult{}, fmt.Errorf("missing prompt argument")
		}
		agentType := strings.TrimSpace(args.AgentType)
		if agentType == "" {
			agentType = "Explore"
		}
		result, err := agent.runDurableSubagentSync(ctx, durableSubagentStartRequest{
			Prompt:            prompt,
			AgentType:         agentType,
			WriteScope:        args.WriteScope,
			RequiredBundles:   args.RequiredBundles,
			RequiredTools:     args.RequiredTools,
			BundleOverrides:   args.BundleOverrides,
			DeactivateBundles: args.DeactivateBundles,
			MaxTurns:          args.MaxTurns,
			JobTimeoutMS:      args.JobTimeoutMS,
		}, args.TimeoutMS)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: result}, nil
	})
}

func formatSubagentJobList(jobs []*subagentJob) []subagentModelJobView {
	out := make([]subagentModelJobView, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, formatSubagentModelJob(job))
	}
	return out
}

func formatSubagentModelJob(job *subagentJob) subagentModelJobView {
	if job == nil {
		return subagentModelJobView{}
	}
	progress := durableSubagentProgressViews(job.Progress)
	view := subagentModelJobView{
		JobID:          job.ID,
		SessionID:      job.SessionID,
		ParentTurnID:   job.ParentTurnID,
		IdentityID:     job.Identity.ID,
		WorkerID:       firstNonEmpty(job.WorkerID, localGoDexWorkerID),
		SourceBranchID: job.SourceBranchID,
		SourceNodeID:   job.SourceNodeID,
		WorkerBranchID: job.WorkerBranchID,
		AgentType:      job.AgentType,
		RoleID:         job.RoleID,
		RoleName:       job.RoleName,
		SandboxID:      job.SandboxID,
		Status:         string(job.Status),
		Error:          job.Error,
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
		FinishedAt:     job.FinishedAt,
		MergeStatus:    job.MergeStatus,
		ProgressCount:  len(job.Progress),
	}
	if len(progress) > 0 {
		latest := progress[len(progress)-1]
		view.LastPhase = latest.Phase
		view.LastMessage = latest.Message
		view.LastToolName = latest.ToolName
	}
	for i := len(progress) - 1; i >= 0; i-- {
		if strings.TrimSpace(view.LastToolName) == "" && strings.TrimSpace(progress[i].ToolName) != "" {
			view.LastToolName = progress[i].ToolName
		}
		if strings.TrimSpace(view.LastMessage) == "" && strings.TrimSpace(progress[i].Message) != "" {
			view.LastMessage = progress[i].Message
		}
	}
	if subagentStatusTerminal(job.Status) && strings.TrimSpace(job.Result) != "" {
		view.ResultPreview = previewSubagentResultForModel(job.Result)
		view.ResultBytes = len([]byte(job.Result))
		view.ResultDigest = subagentResultDigest(job.Result)
	}
	return view
}

func formatSubagentLogs(job *subagentJob, limit int) subagentLogsView {
	if limit <= 0 {
		limit = 20
	}
	if limit > subagentProgressLimit {
		limit = subagentProgressLimit
	}
	total := len(job.Progress)
	progress := job.Progress
	if total > limit {
		progress = progress[total-limit:]
	}
	return subagentLogsView{
		JobID:    job.ID,
		Status:   string(job.Status),
		Count:    len(progress),
		Total:    total,
		Progress: durableSubagentProgressViews(progress),
	}
}

func startSubagentBatch(ctx context.Context, agent *Agent, tasks []subagentBatchItem, wait bool, timeoutMS int) subagentBatchView {
	total := len(tasks)
	view := subagentBatchView{
		Status: "started",
		Total:  total,
		Jobs:   make([]subagentModelJobView, 0, len(tasks)),
		Errors: make([]subagentBatchErrorView, 0),
	}
	if total == 0 {
		view.Status = "failed"
		view.Failed = 1
		view.Errors = append(view.Errors, subagentBatchErrorView{Index: 0, Error: "missing tasks argument"})
		return view
	}
	batchLimit := agent.subagentBatchLimit()
	if len(tasks) > batchLimit {
		for i := batchLimit; i < len(tasks); i++ {
			view.Errors = append(view.Errors, subagentBatchErrorView{Index: i, Error: fmt.Sprintf("batch limit is %d tasks", batchLimit)})
		}
		tasks = tasks[:batchLimit]
	}
	for i, item := range tasks {
		prompt := strings.TrimSpace(item.Prompt)
		if prompt == "" {
			view.Errors = append(view.Errors, subagentBatchErrorView{Index: i, Error: "missing prompt argument"})
			continue
		}
		job, err := agent.startDurableSubagentWithContext(ctx, durableSubagentStartRequest{
			Prompt:            prompt,
			AgentType:         item.AgentType,
			WriteScope:        item.WriteScope,
			RequiredBundles:   item.RequiredBundles,
			RequiredTools:     item.RequiredTools,
			BundleOverrides:   item.BundleOverrides,
			DeactivateBundles: item.DeactivateBundles,
			MaxTurns:          item.MaxTurns,
			JobTimeoutMS:      item.JobTimeoutMS,
		})
		if err != nil {
			view.Errors = append(view.Errors, subagentBatchErrorView{Index: i, Error: err.Error()})
			continue
		}
		view.Jobs = append(view.Jobs, formatSubagentModelJob(job))
	}
	view.Started = len(view.Jobs)
	view.Failed = len(view.Errors)
	if view.Failed > 0 && view.Started == 0 {
		view.Status = "failed"
	} else if view.Failed > 0 {
		view.Status = "partial"
	}
	if wait && view.Started > 0 {
		jobIDs := make([]string, 0, len(view.Jobs))
		for _, job := range view.Jobs {
			jobIDs = append(jobIDs, job.JobID)
		}
		waitView, err := waitSubagents(ctx, agent, subagentWaitRequest{
			JobIDs:    jobIDs,
			Mode:      "all",
			TimeoutMS: timeoutMS,
		})
		if err != nil {
			view.Errors = append(view.Errors, subagentBatchErrorView{Index: -1, Error: err.Error()})
			view.Failed = len(view.Errors)
			if view.Started == 0 {
				view.Status = "failed"
			} else {
				view.Status = "partial"
			}
		} else {
			view.Wait = &waitView
			view.Jobs = waitView.Jobs
			view.Status = waitView.Status
		}
	}
	return view
}

func (a *Agent) runDurableSubagentSync(ctx context.Context, req durableSubagentStartRequest, timeoutMS int) (subagentRunView, error) {
	job, err := a.startDurableSubagentWithContext(ctx, req)
	if err != nil {
		return subagentRunView{}, err
	}
	waitView, err := waitSubagents(ctx, a, subagentWaitRequest{
		JobID:      job.ID,
		Mode:       "all",
		TimeoutMS:  timeoutMS,
		Indefinite: timeoutMS <= 0,
	})
	if err != nil {
		return subagentRunView{}, err
	}
	view := subagentRunView{
		Status:  waitView.Status,
		JobID:   job.ID,
		Wait:    waitView,
		Timeout: waitView.Status == "timeout",
	}
	if len(waitView.Jobs) > 0 {
		view.Job = waitView.Jobs[0]
		view.Result = waitView.Jobs[0].ResultPreview
	}
	return view, nil
}

type subagentWaitRequest struct {
	JobID      string
	JobIDs     []string
	Mode       string
	TimeoutMS  int
	Indefinite bool
}

func waitSubagents(ctx context.Context, agent *Agent, req subagentWaitRequest) (subagentWaitView, error) {
	jobIDs := normalizeSubagentWaitJobIDs(req.JobID, req.JobIDs)
	if len(jobIDs) == 0 {
		return subagentWaitView{}, fmt.Errorf("missing job_ids argument")
	}
	mode := normalizeSubagentWaitMode(req.Mode)
	timeoutMS := 0
	if !req.Indefinite {
		timeoutMS = normalizeSubagentWaitTimeoutMS(req.TimeoutMS)
	}
	updates, unsubscribe := agent.subagentJobs.Watch()
	defer unsubscribe()
	deadline := time.Time{}
	if !req.Indefinite {
		deadline = time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
	}
	for {
		view, err := snapshotSubagentWait(agent, jobIDs, mode, timeoutMS)
		if err != nil {
			return subagentWaitView{}, err
		}
		if subagentWaitSatisfied(view) {
			view.Status = "completed"
			return view, nil
		}
		if req.Indefinite {
			select {
			case <-ctx.Done():
				view.Status = "interrupted"
				return view, nil
			case <-updates:
			}
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			view.Status = "timeout"
			return view, nil
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			view.Status = "timeout"
			return view, nil
		case <-updates:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func snapshotSubagentWait(agent *Agent, jobIDs []string, mode string, timeoutMS int) (subagentWaitView, error) {
	view := subagentWaitView{
		Status:    "running",
		Mode:      mode,
		TimeoutMS: timeoutMS,
		Total:     len(jobIDs),
		Jobs:      make([]subagentModelJobView, 0, len(jobIDs)),
	}
	for _, id := range jobIDs {
		job, err := agent.subagentJobs.Get(id)
		if err != nil {
			return subagentWaitView{}, err
		}
		item := formatSubagentModelJob(job)
		view.Jobs = append(view.Jobs, item)
		switch {
		case subagentStatusTerminal(job.Status):
			view.Completed++
			if job.Status == subagentStatusError || job.Status == subagentStatusTimeout {
				view.Failed++
			}
		default:
			view.Running++
		}
	}
	return view, nil
}

func subagentWaitSatisfied(view subagentWaitView) bool {
	if view.Total == 0 {
		return false
	}
	if view.Mode == "any" {
		return view.Completed > 0
	}
	return view.Completed == view.Total
}

func normalizeSubagentWaitJobIDs(jobID string, jobIDs []string) []string {
	out := make([]string, 0, len(jobIDs)+1)
	seen := make(map[string]struct{})
	appendID := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	appendID(jobID)
	for _, id := range jobIDs {
		appendID(id)
	}
	return out
}

func normalizeSubagentWaitMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "any" {
		return "any"
	}
	return "all"
}

func normalizeSubagentWaitTimeoutMS(timeoutMS int) int {
	if timeoutMS <= 0 {
		return defaultSubagentWaitTimeoutMS
	}
	if timeoutMS > maxSubagentWaitTimeoutMS {
		return maxSubagentWaitTimeoutMS
	}
	return timeoutMS
}

func subagentStatusTerminal(status subagentJobStatus) bool {
	switch status {
	case subagentStatusCompleted, subagentStatusCanceled, subagentStatusInterrupted, subagentStatusTimeout, subagentStatusError:
		return true
	default:
		return false
	}
}

func previewSubagentResultForModel(result string) string {
	result = strings.TrimSpace(result)
	if len([]rune(result)) <= subagentResultPreviewLimit {
		return result
	}
	runes := []rune(result)
	return string(runes[:subagentResultPreviewLimit]) + "..."
}

func subagentResultDigest(result string) string {
	sum := sha256.Sum256([]byte(result))
	return fmt.Sprintf("%x", sum[:])
}

func formatSubagentJob(job *subagentJob, includeMessages bool) map[string]interface{} {
	_ = includeMessages
	if job == nil {
		return map[string]interface{}{}
	}
	out := map[string]interface{}{
		"job_id":           job.ID,
		"session_id":       job.SessionID,
		"parent_turn_id":   job.ParentTurnID,
		"agent_type":       job.AgentType,
		"role_id":          job.RoleID,
		"role_name":        job.RoleName,
		"package_name":     job.PackageName,
		"status":           job.Status,
		"result":           job.Result,
		"error":            job.Error,
		"created_at":       job.CreatedAt,
		"updated_at":       job.UpdatedAt,
		"started_at":       job.StartedAt,
		"finished_at":      job.FinishedAt,
		"write_scope":      append([]string{}, job.WriteScope...),
		"default_bundles":  append([]string{}, job.DefaultBundles...),
		"bundle_overrides": append([]string{}, job.BundleOverrides...),
		"deactivate_bundles": append([]string{}, job.DeactivateBundles...),
		"tool_names":       append([]string{}, job.ToolNames...),
		"worker_id":        firstNonEmpty(job.WorkerID, localGoDexWorkerID),
		"sandbox_id":       job.SandboxID,
		"source_branch_id": job.SourceBranchID,
		"source_node_id":   job.SourceNodeID,
		"worker_branch_id": job.WorkerBranchID,
		"worktree_dir":     job.WorktreeDir,
		"isolation":        job.Isolation,
		"merge_status":     job.MergeStatus,
		"merged_at":        job.MergedAt,
	}
	out["progress"] = cloneSubagentProgress(job.Progress)
	return out
}

// RunSubagent runs a subagent with limited tools.
func (a *Agent) RunSubagent(ctx context.Context, prompt string, agentType string) (string, error) {
	toolNames := []string{"bash", "read_file"}
	if normalizeSubagentType(agentType) == "general-purpose" {
		toolNames = append(toolNames, "write_file", "edit_file")
	}

	result, err := a.runScopedSubagent(ctx, prompt, "You are a subagent. Be concise. Prefer workspace-relative file paths.", toolNames, 30)
	if err != nil && !errors.Is(err, conversation.ErrMaxTurnsReached) {
		return "", fmt.Errorf("subagent API error: %w", err)
	}

	if result == nil || result.LastAssistantText == "" {
		return "(subagent completed with no text output)", nil
	}
	return result.LastAssistantText, nil
}

func (a *Agent) runScopedSubagent(ctx context.Context, prompt, basePrompt string, toolNames []string, maxTurns int) (*conversation.Result, error) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, prompt)}
	prompts := conversation.PromptLayers{Base: strings.TrimSpace(basePrompt)}
	runtimeCtx := tools.SessionContextFromContext(ctx)
	ctx = conversation.WithUsageContext(ctx, a.usageContext(runtimeCtx, runtimeCtx.SessionID, "", "scoped"))
	return conversation.Runner{
		Caller: a.client,
		BuildRequest: func(ctx context.Context) (protocol.Request, error) {
			_ = ctx
			req := conversation.NewRequest(a.cfg.Model, a.cfg.MaxTokens, a.cfg.ReasoningEffort, prompts.Build(), messages, a.toolHandler.ActiveSchemas(toolNames...))
			if sid := strings.TrimSpace(runtimeCtx.SessionID); sid != "" {
				req.PromptCacheKey = clampCacheKey(sid)
				req.PromptCacheRetention = protocol.CacheRetentionShort
			}
			return req, nil
		},
		AppendAssistant: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		AppendToolResults: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		ExecuteTool:      a.executeSubagentTool,
		ToolResultFilter: a.filterModelToolResult,
		MaxTurns:         maxTurns,
	}.Run(ctx)
}

func (a *Agent) executeSubagentTool(ctx context.Context, name string, input map[string]interface{}) (conversation.ToolExecutionResult, error) {
	return executeSubagentToolWithHandlers(ctx, name, input, subagentToolHandlers{
		runBash: func(ctx context.Context, command string, allowUnlisted bool) (conversation.ToolExecutionResult, error) {
			input := map[string]interface{}{"command": command}
			if allowUnlisted {
				input["_allow_unlisted_commands"] = true
			}
			return a.handleToolResult(ctx, "bash", input)
		},
		readFile: func(ctx context.Context, path string, limit, offset, startLine, maxLines int) (conversation.ToolExecutionResult, error) {
			input := map[string]interface{}{"path": path}
			if limit > 0 {
				input["limit"] = limit
			}
			if offset > 0 {
				input["offset"] = offset
			}
			if startLine > 0 {
				input["start_line"] = startLine
			}
			if maxLines > 0 {
				input["max_lines"] = maxLines
			}
			return a.handleToolResult(ctx, "read_file", input)
		},
		writeFile: func(ctx context.Context, path, content string) (conversation.ToolExecutionResult, error) {
			return a.handleToolResult(ctx, "write_file", map[string]interface{}{"path": path, "content": content})
		},
		editFile: func(ctx context.Context, path, oldText, newText string) (conversation.ToolExecutionResult, error) {
			return a.handleToolResult(ctx, "edit_file", map[string]interface{}{"path": path, "old_text": oldText, "new_text": newText})
		},
	})
}
