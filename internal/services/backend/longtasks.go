package backend

import (
	"context"
	"github.com/tim5wang/godex/internal/agent"
	"time"
)

func (s *Service) ListLongTasks(ctx context.Context, sessionID string) ([]agent.LongTaskView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.ListLongTasks(sessionID)
}

// GetLongTask returns one durable LongTask.
func (s *Service) GetLongTask(ctx context.Context, sessionID, workflowID string) (agent.LongTaskView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	return session.agent.GetLongTask(workflowID)
}

// MintuiListLongTasks returns durable LongTasks in the mintui
// projection (LongTaskRow) used by the Ctrl+B background-task
// popup.  The mintui package deliberately does not import
// internal/agent, so we translate agent.LongTaskView → LongTaskRow
// at this boundary.
func (s *Service) MintuiListLongTasks(ctx context.Context, sessionID string) ([]LongTaskRow, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	views, err := session.agent.ListLongTasks(sessionID)
	if err != nil {
		return nil, err
	}
	rows := make([]LongTaskRow, 0, len(views))
	for _, v := range views {
		rows = append(rows, projectLongTaskView(v))
	}
	return rows, nil
}

// MintuiGetLongTask returns the detailed snapshot for one
// durable LongTask in the mintui projection.
func (s *Service) MintuiGetLongTask(ctx context.Context, sessionID, workflowID string) (LongTaskDetail, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return LongTaskDetail{}, err
	}
	view, err := session.agent.GetLongTask(workflowID)
	if err != nil {
		return LongTaskDetail{}, err
	}
	return projectLongTaskDetail(view), nil
}

// MintuiCancelLongTask cancels a running LongTask.  The agent
// CancelLongTask signature requires a non-empty nodeID; the
// mintui popup only knows about workflow-level cancellation
// today, so we delegate to CancelLongTaskAll which cancels
// every in-flight node under the workflow.
func (s *Service) MintuiCancelLongTask(ctx context.Context, sessionID, workflowID string) error {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	_, err = session.agent.CancelLongTaskAll(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID)
	return err
}

// MintuiListSubagents returns durable subagents for one session
// in the mintui projection (SubagentRow).
func (s *Service) MintuiListSubagents(ctx context.Context, sessionID string) ([]SubagentRow, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	views := session.agent.ListDurableSubagents(sessionID)
	rows := make([]SubagentRow, 0, len(views))
	for _, v := range views {
		rows = append(rows, projectSubagentRow(v))
	}
	return rows, nil
}

// MintuiLookupLongTask looks up commits or stories in a longtask
// and returns the results in the mintui projection.
func (s *Service) MintuiLookupLongTask(ctx context.Context, sessionID, commit, longtaskID string) (LongTaskLookupResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return LongTaskLookupResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return LongTaskLookupResult{}, err
	}
	defer release()
	entries, err := session.agent.LongTaskLookupByCommit(commit, longtaskID)
	if err != nil {
		return LongTaskLookupResult{Error: err.Error()}, nil
	}
	result := LongTaskLookupResult{}
	for _, e := range entries {
		result.Entries = append(result.Entries, LongTaskLookupEntry{
			LongTaskID: e.LongTaskID,
			StoryID:    e.StoryID,
			Status:     e.CommitHash,
			Title:      e.NodeID,
		})
	}
	return result, nil
}

// MintuiRollbackLongTaskStory rolls back a longtask story and
// returns the result in the mintui projection.
func (s *Service) MintuiRollbackLongTaskStory(ctx context.Context, sessionID, workflowID, nodeID, reason string) (LongTaskRollbackResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return LongTaskRollbackResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return LongTaskRollbackResult{}, err
	}
	defer release()
	result, err := session.agent.RollbackLongTaskStory(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID, nodeID, reason)
	if err != nil {
		return LongTaskRollbackResult{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return LongTaskRollbackResult{}, err
	}
	return projectRollbackResult(result), nil
}

// MintuiGCLongTaskArtifacts sweeps old longtask artifacts and
// returns the result in the mintui projection.
func (s *Service) MintuiGCLongTaskArtifacts(ctx context.Context, sessionID, workflowID string, olderThanSeconds int, apply bool) (LongTaskGCSweepResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return LongTaskGCSweepResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return LongTaskGCSweepResult{}, err
	}
	defer release()
	olderThan := time.Time{}
	if olderThanSeconds > 0 {
		olderThan = s.now().Add(-time.Duration(olderThanSeconds) * time.Second)
	}
	result, err := session.agent.SweepLongTaskArtifacts(workflowID, olderThan, apply)
	if err != nil {
		return LongTaskGCSweepResult{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return LongTaskGCSweepResult{}, err
	}
	return projectGCSweepResult(result), nil
}

// CreateLongTask creates a durable LongTask and backing workflow.
func (s *Service) CreateLongTask(ctx context.Context, sessionID string, args agent.LongTaskArgs) (agent.LongTaskView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	defer release()
	result, err := session.agent.CreateLongTask(sessionID, args)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskView{}, err
	}
	return result, nil
}

// RunLongTask drives a durable LongTask.
func (s *Service) RunLongTask(ctx context.Context, sessionID, workflowID string, args agent.LongTaskArgs) (agent.LongTaskView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	defer release()
	result, err := session.agent.RunLongTask(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID, args)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskView{}, err
	}
	return result, nil
}

// CancelLongTask cancels one LongTask workflow node.
func (s *Service) CancelLongTask(ctx context.Context, sessionID, workflowID, nodeID string) (agent.LongTaskView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	defer release()
	result, err := session.agent.CancelLongTask(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID, nodeID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskView{}, err
	}
	return result, nil
}

// CancelLongTaskAll cascades a cancel across every story in a longtask.
// Used by `godex longtask cancel --all` and the matching HTTP body
// `{"cancel_all": true}`.
func (s *Service) CancelLongTaskAll(ctx context.Context, sessionID, workflowID string) (agent.LongTaskView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	defer release()
	result, err := session.agent.CancelLongTaskAll(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskView{}, err
	}
	return result, nil
}

// FinalizeLongTaskStory validates, merges, and commits one completed LongTask story node.
func (s *Service) FinalizeLongTaskStory(ctx context.Context, sessionID, workflowID, nodeID string) (agent.LongTaskView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	defer release()
	result, err := session.agent.FinalizeLongTaskStory(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID, nodeID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskView{}, err
	}
	return result, nil
}

// LookupLongTask is the commit-hash reverse-lookup entry point.
// The result is a small wrapper that holds the matches and the
// queried commit so the CLI / TUI / Web can render a single
// "this commit came from longtask X, story Y" line.
func (s *Service) LookupLongTask(ctx context.Context, sessionID, commit, longtaskID string) (interface{}, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	entries, err := session.agent.LongTaskLookupByCommit(commit, longtaskID)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"commit":   commit,
		"longtask": longtaskID,
		"matches":  entries,
	}, nil
}

// RollbackLongTaskStory is the agent-level entry point for
// `godex longtask rollback`. The reason byte cap is enforced at
// the CLI / HTTP boundary AND inside the agent (defense in depth)
// so a misbehaving client cannot bypass the cap by talking
// directly to the HTTP API.
func (s *Service) RollbackLongTaskStory(ctx context.Context, sessionID, workflowID, nodeID, reason string) (agent.LongTaskRollbackResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskRollbackResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskRollbackResult{}, err
	}
	defer release()
	result, err := session.agent.RollbackLongTaskStory(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID, nodeID, reason)
	if err != nil {
		return agent.LongTaskRollbackResult{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskRollbackResult{}, err
	}
	return result, nil
}

// GCLongTaskArtifacts drives the explicit lazy GC for longtask
// run records. olderThanSeconds == 0 means permanent retention
// (T12 default); only an explicit --older-than triggers deletes,
// and --apply is the only path that mutates disk.
func (s *Service) GCLongTaskArtifacts(ctx context.Context, sessionID, workflowID string, olderThanSeconds int, apply bool) (agent.LongTaskGCSweepResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskGCSweepResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskGCSweepResult{}, err
	}
	defer release()
	olderThan := time.Time{}
	if olderThanSeconds > 0 {
		olderThan = s.now().Add(-time.Duration(olderThanSeconds) * time.Second)
	}
	result, err := session.agent.SweepLongTaskArtifacts(workflowID, olderThan, apply)
	if err != nil {
		return agent.LongTaskGCSweepResult{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskGCSweepResult{}, err
	}
	return result, nil
}

// PendingPermissions returns pending approval requests for one session.
