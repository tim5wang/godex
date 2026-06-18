package backend

import "time"

// LongTaskRow is the projection of a durable long-running task
// shown in the mintui background-task popup (Ctrl+B).  We keep
// this type in the backend package so the projection is defined
// next to the agent-to-Surface conversion; mintui imports it
// as rtbackend.LongTaskRow.
//
// The mintui package deliberately does not import internal/agent
// — keeping the projection here means the frontend never sees
// internal agent types and we can evolve them without churning
// the UI.
type LongTaskRow struct {
	WorkflowID     string
	Title          string
	Description    string
	Status         string
	Total          int
	Running        int
	Completed      int
	Failed         int
	UpdatedAt      time.Time
	Project        string
	BranchName     string
	LastStoryTitle string
}

// LongTaskStoryRow is a single story inside a LongTaskDetail.
type LongTaskStoryRow struct {
	ID         string
	Title      string
	Status     string
	Passes     bool
	CommitHash string
	Error      string
}

// LongTaskDetail is the full snapshot shown in the detail popup.
// Stories are kept in a separate slice to keep the row cheap to
// render in the list popup.  Wait/Run summaries are intentionally
// not exposed here — agent.subagentWaitView is unexported and we
// want to keep this projection free of internal/agent imports.
type LongTaskDetail struct {
	Row     LongTaskRow
	Stories []LongTaskStoryRow
}
