package backend

import "time"

// SubagentRow is the mintui projection of a durable subagent job
// shown in the workbench workers tab.  Keeping this type in the
// backend package avoids importing internal/agent from mintui.
type SubagentRow struct {
	JobID             string
	DisplayTitle      string
	Objective         string
	Status            string
	MergeStatus       string
	LastPhase         string
	LastMessage       string
	Result            string
	Error             string
	WorkerID          string
	SandboxID         string
	SourceBranchID    string
	WorkerBranchID    string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// LongTaskRollbackResult is the mintui projection of a rollback
// outcome.  agent.LongTaskRollbackResult stays in internal/agent.
type LongTaskRollbackResult struct {
	Success      bool
	StoryID      string
	CommitRevert string
	Error        string
}

// LongTaskLookupEntry is a single match from a commit/story lookup.
type LongTaskLookupEntry struct {
	LongTaskID string
	StoryID    string
	Status     string
	Title      string
}

// LongTaskLookupResult wraps lookup entries and an optional error.
type LongTaskLookupResult struct {
	Entries []LongTaskLookupEntry
	Error   string
}

// LongTaskGCSweepResult is the mintui projection of a GC sweep.
type LongTaskGCSweepResult struct {
	WorkflowID      string
	RemovedCount    int
	RemovedBytes    int64
	KeptCount       int
	DryRun          bool
	Error           string
}
