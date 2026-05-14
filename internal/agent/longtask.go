package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/tools"
)

const (
	longTaskSpecFile       = "longtask.json"
	longTaskValidationsDir = "validations"
	longTaskCommitsDir     = "commits"

	longTaskValidationPending = "pending"
	longTaskValidationPass    = "pass"
	longTaskValidationFail    = "fail"
	longTaskValidationSkipped = "skipped"

	longTaskMergePolicyAutoMerge  = "auto_merge"
	longTaskMergePolicyReviewOnly = "review_only"
	longTaskCommitPolicyAuto      = "auto_commit"
	longTaskCommitPolicyNone      = "none"

	longTaskMergeSkippedNoJob     = "skipped_no_job"
	longTaskMergeSkippedNoScope   = "skipped_no_write_scope"
	longTaskMergeSkippedReview    = "skipped_review_only"
	longTaskCommitSkippedDisabled = "skipped_disabled"
	longTaskCommitSkippedNoGit    = "skipped_non_git"
	longTaskCommitSkippedNoChange = "skipped_no_changes"
	longTaskCommitCommitted       = "committed"
	longTaskCommitFailed          = "failed"

	longTaskDefaultRunMaxIterations = 10
	longTaskDefaultWaitTimeoutMS    = 60000
)

type longTaskSpec struct {
	ID            string               `json:"id"`
	WorkflowID    string               `json:"workflow_id"`
	Project       string               `json:"project,omitempty"`
	BranchName    string               `json:"branch_name,omitempty"`
	Description   string               `json:"description,omitempty"`
	QualityChecks []string             `json:"quality_checks,omitempty"`
	MergePolicy   string               `json:"merge_policy,omitempty"`
	CommitPolicy  string               `json:"commit_policy,omitempty"`
	Stories       []longTaskStoryInput `json:"stories"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

type longTaskStoryInput struct {
	ID                 string   `json:"id,omitempty"`
	Title              string   `json:"title,omitempty"`
	Description        string   `json:"description,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	Priority           int      `json:"priority,omitempty"`
	AgentType          string   `json:"agent_type,omitempty"`
	WriteScope         []string `json:"write_scope,omitempty"`
}

type longTaskArgs struct {
	Action            string               `json:"action,omitempty"`
	LongTaskID        string               `json:"longtask_id,omitempty"`
	WorkflowID        string               `json:"workflow_id,omitempty"`
	Project           string               `json:"project,omitempty"`
	BranchName        string               `json:"branch_name,omitempty"`
	Description       string               `json:"description,omitempty"`
	QualityChecks     []string             `json:"quality_checks,omitempty"`
	MergePolicy       string               `json:"merge_policy,omitempty"`
	CommitPolicy      string               `json:"commit_policy,omitempty"`
	Stories           []longTaskStoryInput `json:"stories,omitempty"`
	NodeID            string               `json:"node_id,omitempty"`
	Result            string               `json:"result,omitempty"`
	Mode              string               `json:"mode,omitempty"`
	TimeoutMS         int                  `json:"timeout_ms,omitempty"`
	MaxIterations     int                  `json:"max_iterations,omitempty"`
	WaitTimeoutMS     int                  `json:"wait_timeout_ms,omitempty"`
	StopOnFailure     bool                 `json:"stop_on_failure,omitempty"`
	AutoRepair        bool                 `json:"auto_repair,omitempty"`
	MaxRepairAttempts int                  `json:"max_repair_attempts,omitempty"`
}

type longTaskValidationCheck struct {
	Command       string    `json:"command"`
	Status        string    `json:"status"`
	OutputPreview string    `json:"output_preview,omitempty"`
	Error         string    `json:"error,omitempty"`
	DurationMS    int64     `json:"duration_ms,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
}

type longTaskValidation struct {
	WorkflowID string                    `json:"workflow_id"`
	NodeID     string                    `json:"node_id"`
	Attempt    int                       `json:"attempt"`
	Status     string                    `json:"status"`
	Checks     []longTaskValidationCheck `json:"checks,omitempty"`
	CreatedAt  time.Time                 `json:"created_at"`
}

type longTaskCommitArtifact struct {
	WorkflowID    string               `json:"workflow_id"`
	NodeID        string               `json:"node_id"`
	Attempt       int                  `json:"attempt"`
	JobID         string               `json:"job_id,omitempty"`
	MergePolicy   string               `json:"merge_policy"`
	CommitPolicy  string               `json:"commit_policy"`
	MergeStatus   string               `json:"merge_status,omitempty"`
	CommitStatus  string               `json:"commit_status,omitempty"`
	CommitHash    string               `json:"commit_hash,omitempty"`
	CommitMessage string               `json:"commit_message,omitempty"`
	ChangedFiles  []subagentFileChange `json:"changed_files,omitempty"`
	Error         string               `json:"error,omitempty"`
	CreatedAt     time.Time            `json:"created_at"`
}

type longTaskStoryView struct {
	ID                 string    `json:"id"`
	NodeID             string    `json:"node_id,omitempty"`
	RepairAttempts     int       `json:"repair_attempts,omitempty"`
	Title              string    `json:"title,omitempty"`
	Description        string    `json:"description,omitempty"`
	AcceptanceCriteria []string  `json:"acceptance_criteria,omitempty"`
	Priority           int       `json:"priority,omitempty"`
	Status             string    `json:"status"`
	Passes             bool      `json:"passes"`
	Verdict            string    `json:"verdict,omitempty"`
	JobID              string    `json:"job_id,omitempty"`
	HandoffRef         string    `json:"handoff_ref,omitempty"`
	ResultPreview      string    `json:"result_preview,omitempty"`
	Error              string    `json:"error,omitempty"`
	ValidationStatus   string    `json:"validation_status,omitempty"`
	ValidationRef      string    `json:"validation_ref,omitempty"`
	MergeStatus        string    `json:"merge_status,omitempty"`
	CommitStatus       string    `json:"commit_status,omitempty"`
	CommitHash         string    `json:"commit_hash,omitempty"`
	CommitRef          string    `json:"commit_ref,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}

type longTaskRepairSummary struct {
	StoryID      string `json:"story_id"`
	FailedNodeID string `json:"failed_node_id"`
	RepairNodeID string `json:"repair_node_id"`
	Attempt      int    `json:"attempt"`
	Reason       string `json:"reason,omitempty"`
}

type longTaskRunSummary struct {
	Status        string                  `json:"status"`
	Iterations    int                     `json:"iterations"`
	MaxIterations int                     `json:"max_iterations,omitempty"`
	Started       []string                `json:"started,omitempty"`
	Finalized     []string                `json:"finalized,omitempty"`
	Repaired      []longTaskRepairSummary `json:"repaired,omitempty"`
	BlockedBy     string                  `json:"blocked_by,omitempty"`
	Message       string                  `json:"message,omitempty"`
}

type longTaskView struct {
	LongTaskID    string              `json:"longtask_id"`
	WorkflowID    string              `json:"workflow_id"`
	Project       string              `json:"project,omitempty"`
	BranchName    string              `json:"branch_name,omitempty"`
	Description   string              `json:"description,omitempty"`
	QualityChecks []string            `json:"quality_checks,omitempty"`
	Status        string              `json:"status"`
	Total         int                 `json:"total"`
	Pending       int                 `json:"pending"`
	Running       int                 `json:"running"`
	Completed     int                 `json:"completed"`
	Failed        int                 `json:"failed"`
	Stories       []longTaskStoryView `json:"stories"`
	Workflow      workflowView        `json:"workflow"`
	Started       []string            `json:"started,omitempty"`
	Wait          *subagentWaitView   `json:"wait,omitempty"`
	Run           *longTaskRunSummary `json:"run,omitempty"`
}

// LongTaskArgs is the public API/CLI input shape for durable LongTask actions.
type LongTaskArgs = longTaskArgs

// LongTaskView is the public API/CLI view for one durable LongTask.
type LongTaskView = longTaskView

func newLongTaskTool(agent *Agent) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("longtask", "Create and drive Ralph-style long tasks by compiling prioritized user stories into a durable workflow. Each story runs as a fresh durable subagent node with bounded handoffs.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{"create", "status", "start", "wait", "cancel", "complete_story", "finalize_story", "run"},
			},
			"longtask_id":         map[string]string{"type": "string"},
			"workflow_id":         map[string]string{"type": "string"},
			"project":             map[string]string{"type": "string"},
			"branch_name":         map[string]string{"type": "string"},
			"description":         map[string]string{"type": "string"},
			"quality_checks":      map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
			"merge_policy":        map[string]interface{}{"type": "string", "enum": []string{"auto_merge", "review_only"}},
			"commit_policy":       map[string]interface{}{"type": "string", "enum": []string{"auto_commit", "none"}},
			"node_id":             map[string]string{"type": "string"},
			"result":              map[string]string{"type": "string"},
			"mode":                map[string]interface{}{"type": "string", "enum": []string{"any", "all"}},
			"timeout_ms":          map[string]string{"type": "integer"},
			"max_iterations":      map[string]string{"type": "integer"},
			"wait_timeout_ms":     map[string]string{"type": "integer"},
			"stop_on_failure":     map[string]string{"type": "boolean"},
			"auto_repair":         map[string]string{"type": "boolean"},
			"max_repair_attempts": map[string]string{"type": "integer"},
			"stories": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":                  map[string]string{"type": "string"},
						"title":               map[string]string{"type": "string"},
						"description":         map[string]string{"type": "string"},
						"acceptance_criteria": map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
						"priority":            map[string]string{"type": "integer"},
						"agent_type":          map[string]string{"type": "string"},
						"write_scope":         map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
					},
				},
			},
		},
	}, nil), func(ctx context.Context, args longTaskArgs) (tools.ToolResult, error) {
		action := strings.ToLower(strings.TrimSpace(args.Action))
		if action == "" {
			action = "status"
		}
		switch action {
		case "create":
			view, err := agent.createLongTask(tools.SessionContextFromContext(ctx).SessionID, args)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "status":
			view, err := agent.longTaskStatus(args.longTaskWorkflowID())
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "start":
			view, err := agent.startLongTask(ctx, args.longTaskWorkflowID())
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "wait":
			view, err := agent.waitLongTask(ctx, args.longTaskWorkflowID(), args.Mode, args.TimeoutMS)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "cancel":
			state, err := agent.cancelWorkflowNode(ctx, args.longTaskWorkflowID(), args.NodeID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			view, err := agent.longTaskViewForState(state)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "complete_story":
			state, err := agent.completeWorkflowNode(args.longTaskWorkflowID(), args.NodeID, args.Result)
			if err != nil {
				return tools.ToolResult{}, err
			}
			view, err := agent.longTaskViewForState(state)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "finalize_story":
			view, err := agent.finalizeLongTaskStory(ctx, args.longTaskWorkflowID(), args.NodeID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "run":
			view, err := agent.runLongTask(ctx, args.longTaskWorkflowID(), args)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		default:
			return tools.ToolResult{}, fmt.Errorf("unsupported longtask action %q", action)
		}
	})
}

func (args longTaskArgs) longTaskWorkflowID() string {
	if id := strings.TrimSpace(args.WorkflowID); id != "" {
		return id
	}
	return strings.TrimSpace(args.LongTaskID)
}

// ListLongTasks returns LongTasks known to this agent workspace.
func (a *Agent) ListLongTasks(sessionIDs ...string) ([]LongTaskView, error) {
	if a == nil || a.workflows == nil || strings.TrimSpace(a.workflows.dir) == "" {
		return nil, fmt.Errorf("workflow store is unavailable")
	}
	sessionID := ""
	if len(sessionIDs) > 0 {
		sessionID = strings.TrimSpace(sessionIDs[0])
	}
	entries, err := os.ReadDir(a.workflows.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var items []LongTaskView
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workflowID := entry.Name()
		if _, err := os.Stat(filepath.Join(a.workflows.dir, workflowID, longTaskSpecFile)); err != nil {
			continue
		}
		if sessionID != "" {
			state, err := a.workflows.load(workflowID)
			if err != nil || strings.TrimSpace(state.Summary.SessionID) != sessionID {
				continue
			}
		}
		view, err := a.longTaskStatus(workflowID)
		if err != nil {
			continue
		}
		items = append(items, view)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].WorkflowID < items[j].WorkflowID
	})
	return items, nil
}

// GetLongTask returns one LongTask by workflow id.
func (a *Agent) GetLongTask(workflowID string) (LongTaskView, error) {
	return a.longTaskStatus(workflowID)
}

// CreateLongTask creates a LongTask spec and backing workflow.
func (a *Agent) CreateLongTask(sessionID string, args LongTaskArgs) (LongTaskView, error) {
	return a.createLongTask(sessionID, args)
}

// RunLongTask drives a LongTask until it completes, blocks, stalls, or reaches max iterations.
func (a *Agent) RunLongTask(ctx context.Context, workflowID string, args LongTaskArgs) (LongTaskView, error) {
	return a.runLongTask(ctx, workflowID, args)
}

// CancelLongTask cancels a LongTask workflow node.
func (a *Agent) CancelLongTask(ctx context.Context, workflowID, nodeID string) (LongTaskView, error) {
	state, err := a.cancelWorkflowNode(ctx, workflowID, nodeID)
	if err != nil {
		return LongTaskView{}, err
	}
	return a.longTaskViewForState(state)
}

// FinalizeLongTaskStory validates and finalizes one completed story node.
func (a *Agent) FinalizeLongTaskStory(ctx context.Context, workflowID, nodeID string) (LongTaskView, error) {
	return a.finalizeLongTaskStory(ctx, workflowID, nodeID)
}

func (a *Agent) createLongTask(sessionID string, args longTaskArgs) (longTaskView, error) {
	id := strings.TrimSpace(args.LongTaskID)
	if id == "" {
		id = strings.TrimSpace(args.WorkflowID)
	}
	if id == "" {
		id = "lt_" + fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	workflowID := strings.TrimSpace(args.WorkflowID)
	if workflowID == "" {
		workflowID = id
	}
	if len(args.Stories) == 0 {
		return longTaskView{}, fmt.Errorf("missing longtask stories")
	}
	now := time.Now().UTC()
	stories := normalizeLongTaskStories(args.Stories)
	nodes := make([]workflowNodeInput, 0, len(stories))
	for i, story := range stories {
		deps := []string{}
		if i > 0 {
			deps = []string{stories[i-1].ID}
		}
		nodes = append(nodes, workflowNodeInput{
			ID:              story.ID,
			Kind:            "story",
			Title:           story.Title,
			Prompt:          renderLongTaskStoryPrompt(args, story),
			DependsOn:       deps,
			HandoffPolicy:   workflowHandoffPolicySummary,
			HandoffMaxBytes: workflowDefaultHandoffMaxBytes,
			AgentType:       story.AgentType,
			WriteScope:      story.WriteScope,
		})
	}
	state, err := a.workflows.create(sessionID, workflowID, nodes, nil)
	if err != nil {
		return longTaskView{}, err
	}
	spec := longTaskSpec{
		ID:            id,
		WorkflowID:    workflowID,
		Project:       strings.TrimSpace(args.Project),
		BranchName:    strings.TrimSpace(args.BranchName),
		Description:   strings.TrimSpace(args.Description),
		QualityChecks: normalizeWorkflowStrings(args.QualityChecks),
		MergePolicy:   normalizeLongTaskMergePolicy(args.MergePolicy),
		CommitPolicy:  normalizeLongTaskCommitPolicy(args.CommitPolicy),
		Stories:       stories,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := a.workflows.writeLongTaskSpec(workflowID, spec); err != nil {
		return longTaskView{}, err
	}
	_ = a.workflows.appendEvent(workflowID, map[string]interface{}{"event": "longtask_created", "longtask_id": id, "stories": len(stories), "at": now})
	return a.longTaskViewForState(state)
}

func normalizeLongTaskStories(input []longTaskStoryInput) []longTaskStoryInput {
	stories := make([]longTaskStoryInput, 0, len(input))
	for i, story := range input {
		story.ID = strings.TrimSpace(story.ID)
		if story.ID == "" {
			story.ID = fmt.Sprintf("US-%03d", i+1)
		}
		story.Title = strings.TrimSpace(story.Title)
		story.Description = strings.TrimSpace(story.Description)
		story.AcceptanceCriteria = normalizeWorkflowStrings(story.AcceptanceCriteria)
		story.AgentType = strings.TrimSpace(story.AgentType)
		story.WriteScope = normalizeWorkflowStrings(story.WriteScope)
		if story.AgentType == "" && len(story.WriteScope) > 0 {
			story.AgentType = "general-purpose"
		}
		stories = append(stories, story)
	}
	sort.SliceStable(stories, func(i, j int) bool {
		pi, pj := stories[i].Priority, stories[j].Priority
		if pi <= 0 {
			pi = 1 << 30
		}
		if pj <= 0 {
			pj = 1 << 30
		}
		return pi < pj
	})
	return stories
}

func normalizeLongTaskMergePolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", longTaskMergePolicyAutoMerge:
		return longTaskMergePolicyAutoMerge
	case longTaskMergePolicyReviewOnly:
		return longTaskMergePolicyReviewOnly
	default:
		return longTaskMergePolicyAutoMerge
	}
}

func normalizeLongTaskCommitPolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", longTaskCommitPolicyAuto:
		return longTaskCommitPolicyAuto
	case longTaskCommitPolicyNone:
		return longTaskCommitPolicyNone
	default:
		return longTaskCommitPolicyAuto
	}
}

func renderLongTaskStoryPrompt(args longTaskArgs, story longTaskStoryInput) string {
	var builder strings.Builder
	builder.WriteString("You are executing one story in a Ralph-style GoDex long task. Work on this story only; do not start other stories.\n\n")
	if project := strings.TrimSpace(args.Project); project != "" {
		builder.WriteString("Project: ")
		builder.WriteString(project)
		builder.WriteString("\n")
	}
	if branch := strings.TrimSpace(args.BranchName); branch != "" {
		builder.WriteString("Target branch: ")
		builder.WriteString(branch)
		builder.WriteString("\n")
	}
	if desc := strings.TrimSpace(args.Description); desc != "" {
		builder.WriteString("Long task description: ")
		builder.WriteString(desc)
		builder.WriteString("\n")
	}
	builder.WriteString("\nStory ID: ")
	builder.WriteString(story.ID)
	if story.Title != "" {
		builder.WriteString("\nTitle: ")
		builder.WriteString(story.Title)
	}
	if story.Description != "" {
		builder.WriteString("\nDescription: ")
		builder.WriteString(story.Description)
	}
	if len(story.AcceptanceCriteria) > 0 {
		builder.WriteString("\nAcceptance criteria:")
		for _, item := range story.AcceptanceCriteria {
			builder.WriteString("\n- ")
			builder.WriteString(item)
		}
	}
	checks := normalizeWorkflowStrings(args.QualityChecks)
	if len(checks) > 0 {
		builder.WriteString("\n\nRequired quality checks before reporting pass:")
		for _, check := range checks {
			builder.WriteString("\n- ")
			builder.WriteString(check)
		}
	}
	builder.WriteString("\n\nCompletion contract:\n- Keep changes minimal and focused on this story.\n- Run the relevant checks above when possible.\n- Finish with an explicit line: Verdict: pass|fail|blocked|needs_fix.\n- Include a compact summary, changed files, validation run, and reusable learnings.\n")
	return builder.String()
}

func (a *Agent) longTaskStatus(workflowID string) (longTaskView, error) {
	state, err := a.workflowState(workflowID)
	if err != nil {
		return longTaskView{}, err
	}
	return a.longTaskViewForState(state)
}

func (a *Agent) startLongTask(ctx context.Context, workflowID string) (longTaskView, error) {
	workflow, err := a.startWorkflowReadyNodes(ctx, workflowID)
	if err != nil {
		return longTaskView{}, err
	}
	view, err := a.longTaskViewForWorkflow(workflow)
	if err != nil {
		return longTaskView{}, err
	}
	view.Started = append([]string{}, workflow.Started...)
	return view, nil
}

func (a *Agent) waitLongTask(ctx context.Context, workflowID, mode string, timeoutMS int) (longTaskView, error) {
	workflow, err := a.waitWorkflow(ctx, workflowID, mode, timeoutMS)
	if err != nil {
		return longTaskView{}, err
	}
	view, err := a.longTaskViewForWorkflow(workflow)
	if err != nil {
		return longTaskView{}, err
	}
	view.Wait = workflow.Wait
	return view, nil
}

func (a *Agent) runLongTask(ctx context.Context, workflowID string, args longTaskArgs) (longTaskView, error) {
	maxIterations := args.MaxIterations
	if maxIterations <= 0 {
		maxIterations = longTaskDefaultRunMaxIterations
	}
	waitTimeoutMS := args.WaitTimeoutMS
	if waitTimeoutMS <= 0 {
		waitTimeoutMS = args.TimeoutMS
	}
	if waitTimeoutMS <= 0 {
		waitTimeoutMS = longTaskDefaultWaitTimeoutMS
	}
	maxRepairAttempts := args.MaxRepairAttempts
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = 2
	}
	summary := &longTaskRunSummary{Status: workflowStatusRunning, MaxIterations: maxIterations}
	var view longTaskView
	for i := 0; i < maxIterations; i++ {
		summary.Iterations = i + 1
		current, err := a.longTaskStatus(workflowID)
		if err != nil {
			return longTaskView{}, err
		}
		if longTaskAllStoriesPass(current) {
			current.Run = summary
			current.Run.Status = workflowStatusCompleted
			current.Run.Message = "all stories passed"
			return current, nil
		}
		if blockedBy, message := longTaskBlockedStory(current); blockedBy != "" {
			if args.AutoRepair {
				repaired, repairView, err := a.appendLongTaskRepair(ctx, workflowID, current, blockedBy, message, maxRepairAttempts)
				if err != nil {
					return longTaskView{}, err
				}
				if repaired.RepairNodeID != "" {
					summary.Repaired = append(summary.Repaired, repaired)
					view = repairView
					continue
				}
			}
			current.Run = summary
			current.Run.Status = "blocked"
			current.Run.BlockedBy = blockedBy
			current.Run.Message = message
			return current, nil
		}

		progressed := false
		for _, story := range current.Stories {
			if story.Status == workflowStatusCompleted && normalizeWorkflowVerdict(story.Verdict) == workflowVerdictPass && story.ValidationStatus == longTaskValidationPending {
				finalized, err := a.finalizeLongTaskStory(ctx, workflowID, firstNonEmpty(story.NodeID, story.ID))
				if err != nil {
					return longTaskView{}, err
				}
				summary.Finalized = append(summary.Finalized, story.ID)
				view = finalized
				progressed = true
				if blockedBy, message := longTaskBlockedStory(finalized); blockedBy != "" {
					if args.AutoRepair {
						repaired, repairView, err := a.appendLongTaskRepair(ctx, workflowID, finalized, blockedBy, message, maxRepairAttempts)
						if err != nil {
							return longTaskView{}, err
						}
						if repaired.RepairNodeID != "" {
							summary.Repaired = append(summary.Repaired, repaired)
							view = repairView
							progressed = true
							continue
						}
					}
					finalized.Run = summary
					finalized.Run.Status = "blocked"
					finalized.Run.BlockedBy = blockedBy
					finalized.Run.Message = message
					return finalized, nil
				}
				if longTaskAllStoriesPass(finalized) {
					finalized.Run = summary
					finalized.Run.Status = workflowStatusCompleted
					finalized.Run.Message = "all stories passed"
					return finalized, nil
				}
			}
		}
		if progressed {
			continue
		}

		if current.Running > 0 {
			waited, err := a.waitLongTask(ctx, workflowID, "all", waitTimeoutMS)
			if err != nil {
				return longTaskView{}, err
			}
			view = waited
			progressed = true
			continue
		}

		started, err := a.startLongTask(ctx, workflowID)
		if err != nil {
			return longTaskView{}, err
		}
		if len(started.Started) > 0 {
			summary.Started = append(summary.Started, started.Started...)
			waited, err := a.waitLongTask(ctx, workflowID, "all", waitTimeoutMS)
			if err != nil {
				return longTaskView{}, err
			}
			view = waited
			progressed = true
			continue
		}
		view = started
		if !progressed {
			view.Run = summary
			view.Run.Status = "stalled"
			view.Run.Message = "no ready stories, running jobs, or pending validations"
			return view, nil
		}
	}
	if view.LongTaskID == "" {
		var err error
		view, err = a.longTaskStatus(workflowID)
		if err != nil {
			return longTaskView{}, err
		}
	}
	view.Run = summary
	view.Run.Status = "max_iterations"
	view.Run.Message = "reached max_iterations before completion"
	return view, nil
}

func (a *Agent) appendLongTaskRepair(ctx context.Context, workflowID string, view longTaskView, storyID, reason string, maxAttempts int) (longTaskRepairSummary, longTaskView, error) {
	_ = ctx
	story, ok := longTaskStoryByID(view, storyID)
	if !ok {
		return longTaskRepairSummary{}, view, nil
	}
	if story.RepairAttempts >= maxAttempts {
		return longTaskRepairSummary{}, view, nil
	}
	spec, err := a.workflows.loadLongTaskSpec(workflowID)
	if err != nil {
		return longTaskRepairSummary{}, longTaskView{}, err
	}
	attempt := story.RepairAttempts + 1
	repairID := longTaskRepairNodeID(storyID, attempt)
	failedNodeID := firstNonEmpty(story.NodeID, storyID)
	summary := longTaskRepairSummary{
		StoryID:      story.ID,
		FailedNodeID: failedNodeID,
		RepairNodeID: repairID,
		Attempt:      attempt,
		Reason:       strings.TrimSpace(reason),
	}
	appended, err := a.appendWorkflowNodes(workflowID, []workflowNodeInput{{
		ID:              repairID,
		Kind:            "repair",
		Title:           "Repair " + storyID,
		Prompt:          renderLongTaskRepairPrompt(spec, story, failedNodeID, reason),
		HandoffPolicy:   workflowHandoffPolicySummaryArtifacts,
		HandoffFrom:     []string{failedNodeID},
		HandoffMaxBytes: workflowDefaultHandoffMaxBytes,
		AgentType:       storyAgentType(spec, storyID),
		WriteScope:      storyWriteScope(spec, storyID),
	}}, nil, "longtask-repair-"+repairID, failedNodeID, reason)
	if err != nil {
		return longTaskRepairSummary{}, longTaskView{}, err
	}
	state, err := a.workflows.load(workflowID)
	if err != nil {
		return longTaskRepairSummary{}, longTaskView{}, err
	}
	rewired := false
	for i := range state.Nodes {
		if state.Nodes[i].ID == repairID || state.Nodes[i].Status != workflowStatusPending {
			continue
		}
		if replaceWorkflowDep(state.Nodes[i].DependsOn, failedNodeID, repairID) {
			rewired = true
		}
	}
	if rewired {
		state.Summary.UpdatedAt = time.Now().UTC()
		if err := a.workflows.save(state); err != nil {
			return longTaskRepairSummary{}, longTaskView{}, err
		}
		return summary, a.longTaskViewFromSpec(spec, workflowViewFromState(state)), nil
	}
	return summary, a.longTaskViewFromSpec(spec, appended), nil
}

func longTaskStoryByID(view longTaskView, storyID string) (longTaskStoryView, bool) {
	for _, story := range view.Stories {
		if story.ID == storyID {
			return story, true
		}
	}
	return longTaskStoryView{}, false
}

func replaceWorkflowDep(deps []string, oldID, newID string) bool {
	changed := false
	for i := range deps {
		if deps[i] == oldID {
			deps[i] = newID
			changed = true
		}
	}
	return changed
}

func longTaskRepairNodeID(storyID string, attempt int) string {
	return fmt.Sprintf("%s_repair_%d", storyID, attempt)
}

func renderLongTaskRepairPrompt(spec longTaskSpec, story longTaskStoryView, failedNodeID, reason string) string {
	var builder strings.Builder
	builder.WriteString("You are executing a fresh repair attempt for one Ralph-style GoDex long-task story. Fix this story only.\n\n")
	if spec.Project != "" {
		builder.WriteString("Project: ")
		builder.WriteString(spec.Project)
		builder.WriteString("\n")
	}
	if spec.Description != "" {
		builder.WriteString("Long task description: ")
		builder.WriteString(spec.Description)
		builder.WriteString("\n")
	}
	builder.WriteString("Story ID: ")
	builder.WriteString(story.ID)
	if story.Title != "" {
		builder.WriteString("\nTitle: ")
		builder.WriteString(story.Title)
	}
	if story.Description != "" {
		builder.WriteString("\nDescription: ")
		builder.WriteString(story.Description)
	}
	if len(story.AcceptanceCriteria) > 0 {
		builder.WriteString("\nAcceptance criteria:")
		for _, item := range story.AcceptanceCriteria {
			builder.WriteString("\n- ")
			builder.WriteString(item)
		}
	}
	builder.WriteString("\n\nFailed attempt node: ")
	builder.WriteString(failedNodeID)
	if reason != "" {
		builder.WriteString("\nFailure reason: ")
		builder.WriteString(reason)
	}
	if story.ValidationRef != "" {
		builder.WriteString("\nValidation artifact: ")
		builder.WriteString(story.ValidationRef)
	}
	if story.ResultPreview != "" {
		builder.WriteString("\nPrevious result preview:\n")
		builder.WriteString(story.ResultPreview)
	}
	if len(spec.QualityChecks) > 0 {
		builder.WriteString("\n\nRequired quality checks before reporting pass:")
		for _, check := range spec.QualityChecks {
			builder.WriteString("\n- ")
			builder.WriteString(check)
		}
	}
	builder.WriteString("\n\nCompletion contract:\n- Keep changes minimal and focused on this repair.\n- Finish with an explicit line: Verdict: pass|fail|blocked|needs_fix.\n- Include a compact summary, changed files, validation run, and reusable learnings.\n")
	return builder.String()
}

func storyAgentType(spec longTaskSpec, storyID string) string {
	for _, story := range spec.Stories {
		if story.ID == storyID {
			return story.AgentType
		}
	}
	return ""
}

func storyWriteScope(spec longTaskSpec, storyID string) []string {
	for _, story := range spec.Stories {
		if story.ID == storyID {
			return append([]string{}, story.WriteScope...)
		}
	}
	return nil
}

func longTaskAllStoriesPass(view longTaskView) bool {
	if len(view.Stories) == 0 {
		return false
	}
	for _, story := range view.Stories {
		if !story.Passes {
			return false
		}
	}
	return true
}

func longTaskBlockedStory(view longTaskView) (string, string) {
	for _, story := range view.Stories {
		if story.Status == workflowStatusError {
			return story.ID, firstNonEmpty(story.Error, "story entered error state")
		}
		if story.ValidationStatus == longTaskValidationFail {
			return story.ID, "validation failed"
		}
		if story.Status == workflowStatusCompleted {
			switch normalizeWorkflowVerdict(story.Verdict) {
			case workflowVerdictBlocked:
				return story.ID, "story reported blocked"
			case workflowVerdictNeedsFix:
				return story.ID, "story reported needs_fix"
			case workflowVerdictFail:
				return story.ID, "story reported fail"
			}
		}
	}
	return "", ""
}

func (a *Agent) finalizeLongTaskStory(ctx context.Context, workflowID, nodeID string) (longTaskView, error) {
	state, err := a.workflowState(workflowID)
	if err != nil {
		return longTaskView{}, err
	}
	spec, err := a.workflows.loadLongTaskSpec(state.Summary.ID)
	if err != nil {
		return longTaskView{}, err
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return longTaskView{}, fmt.Errorf("missing node_id")
	}
	nodeIndex := -1
	for i := range state.Nodes {
		if state.Nodes[i].ID == nodeID {
			nodeIndex = i
			break
		}
	}
	if nodeIndex < 0 {
		return longTaskView{}, fmt.Errorf("longtask story not found: %s", nodeID)
	}
	node := &state.Nodes[nodeIndex]
	if node.Status != workflowStatusCompleted {
		return longTaskView{}, fmt.Errorf("longtask story %s is not completed", nodeID)
	}
	if normalizeWorkflowVerdict(node.Verdict) != workflowVerdictPass {
		return a.longTaskViewForState(state)
	}
	validation, err := a.runLongTaskValidation(ctx, spec, *node)
	if err != nil {
		return longTaskView{}, err
	}
	if err := a.workflows.writeLongTaskValidation(state.Summary.ID, validation); err != nil {
		return longTaskView{}, err
	}
	if validation.Status == longTaskValidationFail {
		now := time.Now().UTC()
		node.Status = workflowStatusError
		node.Error = "longtask validation failed"
		node.UpdatedAt = now
		node.FinishedAt = now
		state.Summary.UpdatedAt = now
		a.refreshWorkflowStatus(&state)
		if err := a.workflows.save(state); err != nil {
			return longTaskView{}, err
		}
	}
	if validation.Status == longTaskValidationPass || validation.Status == longTaskValidationSkipped {
		commitArtifact, commitErr := a.finalizeLongTaskMergeCommit(ctx, spec, *node)
		if commitErr != nil {
			return longTaskView{}, commitErr
		}
		if err := a.workflows.writeLongTaskCommit(state.Summary.ID, commitArtifact); err != nil {
			return longTaskView{}, err
		}
		if longTaskCommitBlocksStory(commitArtifact) {
			now := time.Now().UTC()
			node.Status = workflowStatusError
			node.Error = firstNonEmpty(commitArtifact.Error, "longtask merge/commit failed")
			node.UpdatedAt = now
			node.FinishedAt = now
			state.Summary.UpdatedAt = now
			a.refreshWorkflowStatus(&state)
			if err := a.workflows.save(state); err != nil {
				return longTaskView{}, err
			}
		}
		_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{"event": "longtask_merge_commit", "node_id": nodeID, "merge_status": commitArtifact.MergeStatus, "commit_status": commitArtifact.CommitStatus, "commit_hash": commitArtifact.CommitHash, "commit_ref": longTaskCommitRef(nodeID, commitArtifact.Attempt), "at": commitArtifact.CreatedAt})
	}
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{"event": "longtask_validation", "node_id": nodeID, "status": validation.Status, "validation_ref": longTaskValidationRef(nodeID, validation.Attempt), "at": validation.CreatedAt})
	return a.longTaskViewForState(state)
}

func (a *Agent) finalizeLongTaskMergeCommit(ctx context.Context, spec longTaskSpec, node workflowNode) (longTaskCommitArtifact, error) {
	attempt := node.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	artifact := longTaskCommitArtifact{
		WorkflowID:   spec.WorkflowID,
		NodeID:       node.ID,
		Attempt:      attempt,
		JobID:        node.JobID,
		MergePolicy:  normalizeLongTaskMergePolicy(spec.MergePolicy),
		CommitPolicy: normalizeLongTaskCommitPolicy(spec.CommitPolicy),
		CreatedAt:    time.Now().UTC(),
	}
	if strings.TrimSpace(node.JobID) == "" {
		artifact.MergeStatus = longTaskMergeSkippedNoJob
		artifact.CommitStatus = longTaskCommitSkippedNoChange
		return artifact, nil
	}
	job, err := a.subagentJobs.Get(node.JobID)
	if err != nil {
		artifact.MergeStatus = "failed"
		artifact.CommitStatus = longTaskCommitFailed
		artifact.Error = err.Error()
		return artifact, nil
	}
	if len(job.WriteScope) == 0 {
		artifact.MergeStatus = longTaskMergeSkippedNoScope
		artifact.CommitStatus = longTaskCommitSkippedNoChange
		return artifact, nil
	}
	if artifact.MergePolicy == longTaskMergePolicyReviewOnly {
		review, reviewErr := a.ReviewDurableSubagent(node.JobID)
		if reviewErr != nil {
			artifact.MergeStatus = "failed"
			artifact.CommitStatus = longTaskCommitFailed
			artifact.Error = reviewErr.Error()
			return artifact, nil
		}
		artifact.MergeStatus = longTaskMergeSkippedReview
		artifact.ChangedFiles = append([]subagentFileChange{}, review.Changes...)
		artifact.CommitStatus = longTaskCommitSkippedDisabled
		artifact.Error = "merge_policy review_only requires manual review/merge"
		return artifact, nil
	}
	merge, err := a.MergeDurableSubagentWithContext(ctx, node.JobID)
	if err != nil {
		artifact.MergeStatus = "failed"
		artifact.CommitStatus = longTaskCommitFailed
		artifact.Error = err.Error()
		return artifact, nil
	}
	artifact.MergeStatus = merge.Status
	artifact.ChangedFiles = append([]subagentFileChange{}, merge.Applied...)
	if merge.Status == subagentMergeConflict {
		artifact.CommitStatus = longTaskCommitFailed
		artifact.Error = strings.Join(merge.Conflicts, "\n")
		return artifact, nil
	}
	if merge.Status != subagentMergeMerged && merge.Status != subagentMergeNoChanges {
		artifact.CommitStatus = longTaskCommitFailed
		artifact.Error = "subagent merge did not complete"
		return artifact, nil
	}
	if artifact.CommitPolicy == longTaskCommitPolicyNone {
		artifact.CommitStatus = longTaskCommitSkippedDisabled
		return artifact, nil
	}
	if len(artifact.ChangedFiles) == 0 || merge.Status == subagentMergeNoChanges {
		artifact.CommitStatus = longTaskCommitSkippedNoChange
		return artifact, nil
	}
	if !longTaskGitRepo(a.cfg.WorkspaceDir) {
		artifact.CommitStatus = longTaskCommitSkippedNoGit
		return artifact, nil
	}
	message := longTaskCommitMessage(spec, node)
	hash, err := longTaskGitCommit(ctx, a.cfg.WorkspaceDir, artifact.ChangedFiles, message)
	if err != nil {
		artifact.CommitStatus = longTaskCommitFailed
		artifact.CommitMessage = message
		artifact.Error = err.Error()
		return artifact, nil
	}
	artifact.CommitStatus = longTaskCommitCommitted
	artifact.CommitHash = hash
	artifact.CommitMessage = message
	return artifact, nil
}

func longTaskCommitBlocksStory(artifact longTaskCommitArtifact) bool {
	switch artifact.MergeStatus {
	case longTaskMergeSkippedNoJob, longTaskMergeSkippedNoScope, subagentMergeMerged, subagentMergeNoChanges:
	default:
		return true
	}
	return artifact.CommitStatus == longTaskCommitFailed
}

func longTaskCommitMessage(spec longTaskSpec, node workflowNode) string {
	storyID := node.ID
	if idx := strings.Index(storyID, "_repair_"); idx > 0 {
		storyID = storyID[:idx]
	}
	title := strings.TrimSpace(node.Title)
	if title == "" {
		title = storyID
	}
	return fmt.Sprintf("longtask(%s): complete %s %s", firstNonEmpty(spec.ID, spec.WorkflowID), storyID, title)
}

func longTaskGitRepo(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func longTaskGitCommit(ctx context.Context, dir string, changes []subagentFileChange, message string) (string, error) {
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		path := strings.TrimSpace(change.Path)
		if path != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return "", nil
	}
	addArgs := append([]string{"add", "--"}, paths...)
	if out, err := longTaskRunGit(ctx, dir, addArgs...); err != nil {
		return "", fmt.Errorf("git add: %w: %s", err, out)
	}
	if out, err := longTaskRunGit(ctx, dir, "commit", "-m", message); err != nil {
		return "", fmt.Errorf("git commit: %w: %s", err, out)
	}
	hash, err := longTaskRunGit(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w: %s", err, hash)
	}
	return strings.TrimSpace(hash), nil
}

func longTaskRunGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (a *Agent) runLongTaskValidation(ctx context.Context, spec longTaskSpec, node workflowNode) (longTaskValidation, error) {
	attempt := node.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	validation := longTaskValidation{
		WorkflowID: spec.WorkflowID,
		NodeID:     node.ID,
		Attempt:    attempt,
		Status:     longTaskValidationSkipped,
		CreatedAt:  time.Now().UTC(),
	}
	checks := normalizeWorkflowStrings(spec.QualityChecks)
	if len(checks) == 0 {
		return validation, nil
	}
	validation.Status = longTaskValidationPass
	workspaceDir := a.longTaskValidationWorkspace(node)
	executor := tooling.NewWorkspaceExecutorWithTempDirAndExecution(workspaceDir, a.cfg.TempDir, executionConfigFromRuntime(a.cfg.Tools.Execution))
	for _, command := range checks {
		started := time.Now().UTC()
		output, err := executor.RunShellBudgeted(ctx, command)
		finished := time.Now().UTC()
		check := longTaskValidationCheck{
			Command:       command,
			Status:        longTaskValidationPass,
			OutputPreview: strings.TrimSpace(output.ModelText()),
			DurationMS:    finished.Sub(started).Milliseconds(),
			StartedAt:     started,
			FinishedAt:    finished,
		}
		if err != nil {
			check.Status = longTaskValidationFail
			check.Error = err.Error()
			validation.Status = longTaskValidationFail
		}
		validation.Checks = append(validation.Checks, check)
		if err != nil {
			break
		}
	}
	return validation, nil
}

func (a *Agent) longTaskValidationWorkspace(node workflowNode) string {
	if strings.TrimSpace(node.JobID) != "" {
		if job, err := a.subagentJobs.Get(node.JobID); err == nil {
			if dir := strings.TrimSpace(job.WorktreeDir); dir != "" {
				if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
					return dir
				}
			}
		}
	}
	return a.cfg.WorkspaceDir
}

func (a *Agent) longTaskViewForState(state workflowState) (longTaskView, error) {
	return a.longTaskViewForWorkflow(workflowViewFromState(state))
}

func (a *Agent) longTaskViewForWorkflow(workflow workflowView) (longTaskView, error) {
	spec, err := a.workflows.loadLongTaskSpec(workflow.WorkflowID)
	if err != nil {
		return longTaskView{}, err
	}
	return a.longTaskViewFromSpec(spec, workflow), nil
}

func (a *Agent) longTaskViewFromSpec(spec longTaskSpec, workflow workflowView) longTaskView {
	view := longTaskView{
		LongTaskID:    spec.ID,
		WorkflowID:    spec.WorkflowID,
		Project:       spec.Project,
		BranchName:    spec.BranchName,
		Description:   spec.Description,
		QualityChecks: append([]string{}, spec.QualityChecks...),
		Status:        workflow.Status,
		Total:         workflow.Total,
		Pending:       workflow.Pending,
		Running:       workflow.Running,
		Completed:     workflow.Completed,
		Failed:        workflow.Failed,
		Workflow:      workflow,
	}
	for _, story := range spec.Stories {
		node, repairs := latestLongTaskStoryNode(workflow, story.ID)
		validationStatus, validationRef := a.longTaskStoryValidationView(spec, workflow.WorkflowID, node)
		mergeStatus, commitStatus, commitHash, commitRef := a.longTaskStoryCommitView(workflow.WorkflowID, node)
		passes := node.Status == workflowStatusCompleted &&
			normalizeWorkflowVerdict(node.Verdict) == workflowVerdictPass &&
			(validationStatus == longTaskValidationPass || validationStatus == longTaskValidationSkipped) &&
			longTaskMergeCommitAllowsPass(mergeStatus, commitStatus)
		view.Stories = append(view.Stories, longTaskStoryView{
			ID:                 story.ID,
			NodeID:             node.ID,
			RepairAttempts:     repairs,
			Title:              story.Title,
			Description:        story.Description,
			AcceptanceCriteria: append([]string{}, story.AcceptanceCriteria...),
			Priority:           story.Priority,
			Status:             node.Status,
			Passes:             passes,
			Verdict:            node.Verdict,
			JobID:              node.JobID,
			HandoffRef:         node.HandoffRef,
			ResultPreview:      node.ResultPreview,
			Error:              node.Error,
			ValidationStatus:   validationStatus,
			ValidationRef:      validationRef,
			MergeStatus:        mergeStatus,
			CommitStatus:       commitStatus,
			CommitHash:         commitHash,
			CommitRef:          commitRef,
			UpdatedAt:          node.UpdatedAt,
		})
	}
	return view
}

func latestLongTaskStoryNode(workflow workflowView, storyID string) (workflowNodeView, int) {
	var base workflowNodeView
	var latest workflowNodeView
	latestRepair := 0
	prefix := storyID + "_repair_"
	for _, node := range workflow.Nodes {
		if node.ID == storyID {
			base = node
			continue
		}
		if !strings.HasPrefix(node.ID, prefix) {
			continue
		}
		var attempt int
		if _, err := fmt.Sscanf(strings.TrimPrefix(node.ID, prefix), "%d", &attempt); err != nil || attempt <= 0 {
			continue
		}
		if attempt > latestRepair {
			latestRepair = attempt
			latest = node
		}
	}
	if latestRepair > 0 {
		return latest, latestRepair
	}
	return base, 0
}

func (a *Agent) longTaskStoryValidationView(spec longTaskSpec, workflowID string, node workflowNodeView) (string, string) {
	if node.ID == "" {
		return "", ""
	}
	attempt := node.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	ref := longTaskValidationRef(node.ID, attempt)
	if validation, err := a.workflows.loadLongTaskValidation(workflowID, node.ID, attempt); err == nil {
		return validation.Status, ref
	}
	if node.Status == workflowStatusCompleted && normalizeWorkflowVerdict(node.Verdict) == workflowVerdictPass {
		if len(spec.QualityChecks) == 0 {
			return longTaskValidationSkipped, ""
		}
		return longTaskValidationPending, ref
	}
	return "", ref
}

func (a *Agent) longTaskStoryCommitView(workflowID string, node workflowNodeView) (string, string, string, string) {
	if node.ID == "" {
		return "", "", "", ""
	}
	attempt := node.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	ref := longTaskCommitRef(node.ID, attempt)
	artifact, err := a.workflows.loadLongTaskCommit(workflowID, node.ID, attempt)
	if err != nil {
		if node.Status == workflowStatusCompleted && normalizeWorkflowVerdict(node.Verdict) == workflowVerdictPass {
			return "", "", "", ref
		}
		return "", "", "", ""
	}
	return artifact.MergeStatus, artifact.CommitStatus, artifact.CommitHash, ref
}

func longTaskMergeCommitAllowsPass(mergeStatus, commitStatus string) bool {
	switch mergeStatus {
	case "", longTaskMergeSkippedNoJob, longTaskMergeSkippedNoScope, subagentMergeMerged, subagentMergeNoChanges:
	default:
		return false
	}
	return commitStatus != longTaskCommitFailed
}

func (s *workflowStore) writeLongTaskSpec(workflowID string, spec longTaskSpec) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return err
	}
	path := filepath.Join(s.dir, workflowID, longTaskSpecFile)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(path, spec, 0644)
}

func (s *workflowStore) loadLongTaskSpec(workflowID string) (longTaskSpec, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return longTaskSpec{}, fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return longTaskSpec{}, err
	}
	var spec longTaskSpec
	if err := readJSONFile(filepath.Join(s.dir, workflowID, longTaskSpecFile), &spec); err != nil {
		return longTaskSpec{}, fmt.Errorf("read longtask spec: %w", err)
	}
	if spec.WorkflowID == "" {
		spec.WorkflowID = workflowID
	}
	if spec.ID == "" {
		spec.ID = workflowID
	}
	spec.MergePolicy = normalizeLongTaskMergePolicy(spec.MergePolicy)
	spec.CommitPolicy = normalizeLongTaskCommitPolicy(spec.CommitPolicy)
	return spec, nil
}

func (s *workflowStore) writeLongTaskValidation(workflowID string, validation longTaskValidation) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return err
	}
	path := filepath.Join(s.dir, workflowID, longTaskValidationRef(validation.NodeID, validation.Attempt))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(path, validation, 0644)
}

func (s *workflowStore) writeLongTaskCommit(workflowID string, artifact longTaskCommitArtifact) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return err
	}
	if artifact.Attempt <= 0 {
		artifact.Attempt = 1
	}
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = time.Now().UTC()
	}
	path := filepath.Join(s.dir, workflowID, longTaskCommitRef(artifact.NodeID, artifact.Attempt))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(path, artifact, 0644)
}

func (s *workflowStore) loadLongTaskValidation(workflowID, nodeID string, attempt int) (longTaskValidation, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return longTaskValidation{}, fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return longTaskValidation{}, err
	}
	var validation longTaskValidation
	if err := readJSONFile(filepath.Join(s.dir, workflowID, longTaskValidationRef(nodeID, attempt)), &validation); err != nil {
		return longTaskValidation{}, err
	}
	return validation, nil
}

func (s *workflowStore) loadLongTaskCommit(workflowID, nodeID string, attempt int) (longTaskCommitArtifact, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return longTaskCommitArtifact{}, fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return longTaskCommitArtifact{}, err
	}
	var artifact longTaskCommitArtifact
	if err := readJSONFile(filepath.Join(s.dir, workflowID, longTaskCommitRef(nodeID, attempt)), &artifact); err != nil {
		return longTaskCommitArtifact{}, err
	}
	return artifact, nil
}

func longTaskValidationRef(nodeID string, attempt int) string {
	if attempt <= 0 {
		attempt = 1
	}
	return filepath.ToSlash(filepath.Join(longTaskValidationsDir, strings.TrimSpace(nodeID), fmt.Sprintf("%d.json", attempt)))
}

func longTaskCommitRef(nodeID string, attempt int) string {
	if attempt <= 0 {
		attempt = 1
	}
	return filepath.ToSlash(filepath.Join(longTaskCommitsDir, strings.TrimSpace(nodeID), fmt.Sprintf("%d.json", attempt)))
}
