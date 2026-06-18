package backend

import (
	"strings"

	"github.com/tim5wang/godex/internal/agent"
)

// projectLongTaskView converts the agent-layer LongTaskView into
// the mintui-shaped LongTaskRow.  Keeping the conversion here
// (next to Service methods) means the mintui package never has
// to import internal/agent — which is the same isolation rule
// already used for Snapshot, ListedSession, etc.
func projectLongTaskView(v agent.LongTaskView) LongTaskRow {
	title := strings.TrimSpace(v.WorkflowID)
	if title == "" {
		title = v.LongTaskID
	}
	row := LongTaskRow{
		WorkflowID:  v.WorkflowID,
		Title:       title,
		Description: v.Description,
		Status:      v.Status,
		Total:       v.Total,
		Running:     v.Running,
		Completed:   v.Completed,
		Failed:      v.Failed,
		Project:     v.Project,
		BranchName:  v.BranchName,
	}
	if len(v.Stories) > 0 {
		// Story update times are the most recent per-row signal
		// for "what's still moving" — the workflowView type is
		// unexported so we cannot reach its UpdatedAt from here.
		row.UpdatedAt = v.Stories[len(v.Stories)-1].UpdatedAt
	}
	row.LastStoryTitle = pickLastStoryTitle(v)
	return row
}

// projectLongTaskDetail builds the detail-popup snapshot from a
// full LongTaskView: row + flattened story rows + short wait/run
// messages.
func projectLongTaskDetail(v agent.LongTaskView) LongTaskDetail {
	row := projectLongTaskView(v)
	stories := make([]LongTaskStoryRow, 0, len(v.Stories))
	for _, s := range v.Stories {
		stories = append(stories, LongTaskStoryRow{
			ID:         s.NodeID,
			Title:      strings.TrimSpace(s.Title),
			Status:     s.Status,
			Passes:     s.Passes,
			CommitHash: shortCommitHash(s.CommitHash),
			Error:      strings.TrimSpace(s.Error),
		})
	}
	return LongTaskDetail{
		Row:     row,
		Stories: stories,
	}
}

// pickLastStoryTitle returns the title of the most recent story
// that is still in flight, or the most recent one overall.  The
// list popup uses this as a short "what's this task about" hint
// when the workflow-level description is empty.
func pickLastStoryTitle(v agent.LongTaskView) string {
	if len(v.Stories) == 0 {
		return ""
	}
	for i := len(v.Stories) - 1; i >= 0; i-- {
		s := v.Stories[i]
		if !s.Passes && s.Status != "completed" {
			return strings.TrimSpace(s.Title)
		}
	}
	return strings.TrimSpace(v.Stories[len(v.Stories)-1].Title)
}

// shortCommitHash returns the first 7 chars of a commit hash
// (the conventional short form).  An empty hash yields "".
func shortCommitHash(h string) string {
	h = strings.TrimSpace(h)
	if len(h) <= 7 {
		return h
	}
	return h[:7]
}
