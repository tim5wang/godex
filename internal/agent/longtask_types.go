package agent

import "time"

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

	longTaskDefaultRunMaxIterations       = 10
	longTaskDefaultWaitTimeoutMS          = 60000
	longTaskDefaultValidationTimeoutMS    = 60000
)

type longTaskSpec struct {
	ID                   string               `json:"id"`
	WorkflowID           string               `json:"workflow_id"`
	Project              string               `json:"project,omitempty"`
	BranchName           string               `json:"branch_name,omitempty"`
	Description          string               `json:"description,omitempty"`
	QualityChecks        []string             `json:"quality_checks,omitempty"`
	ValidationTimeoutMS  int                  `json:"validation_timeout_ms,omitempty"`
	MaxValidationBudgetMS int                 `json:"max_validation_budget_ms,omitempty"`
	MergePolicy          string               `json:"merge_policy,omitempty"`
	CommitPolicy         string               `json:"commit_policy,omitempty"`
	Stories              []longTaskStoryInput `json:"stories"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
}

type longTaskStoryInput struct {
	ID                 string   `json:"id,omitempty"`
	Title              string   `json:"title,omitempty"`
	Description        string   `json:"description,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	Priority           int      `json:"priority,omitempty"`
	AgentType          string   `json:"agent_type,omitempty"`
	WriteScope         []string `json:"write_scope,omitempty"`
	HandoffPolicy      string   `json:"handoff_policy,omitempty"`
	HandoffMaxBytes    int      `json:"handoff_max_bytes,omitempty"`
}

type longTaskArgs struct {
	Action               string               `json:"action,omitempty"`
	LongTaskID           string               `json:"longtask_id,omitempty"`
	WorkflowID           string               `json:"workflow_id,omitempty"`
	Project              string               `json:"project,omitempty"`
	BranchName           string               `json:"branch_name,omitempty"`
	Description          string               `json:"description,omitempty"`
	QualityChecks        []string             `json:"quality_checks,omitempty"`
	ValidationTimeoutMS  int                  `json:"validation_timeout_ms,omitempty"`
	MergePolicy          string               `json:"merge_policy,omitempty"`
	CommitPolicy         string               `json:"commit_policy,omitempty"`
	Stories              []longTaskStoryInput `json:"stories,omitempty"`
	NodeID               string               `json:"node_id,omitempty"`
	Result               string               `json:"result,omitempty"`
	Mode                 string               `json:"mode,omitempty"`
	TimeoutMS            int                  `json:"timeout_ms,omitempty"`
	MaxIterations        int                  `json:"max_iterations,omitempty"`
	WaitTimeoutMS        int                  `json:"wait_timeout_ms,omitempty"`
	StopOnFailure        *bool                `json:"stop_on_failure,omitempty"`
	AutoRepair           bool                 `json:"auto_repair,omitempty"`
	MaxRepairAttempts    int                  `json:"max_repair_attempts,omitempty"`
	Async                bool                 `json:"async,omitempty"`
	ResumeRunID          string               `json:"resume_run_id,omitempty"`
	NoReflux             bool                 `json:"no_reflux,omitempty"`
	SessionID            string               `json:"session_id,omitempty"`
	CancelAll            bool                 `json:"cancel_all,omitempty"`
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
