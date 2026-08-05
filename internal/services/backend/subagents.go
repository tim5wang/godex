package backend

import (
	"context"
	"github.com/tim5wang/godex/internal/agent"
)

func (s *Service) ListSubagents(ctx context.Context, sessionID string) ([]agent.DurableSubagentJobView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.ListDurableSubagents(sessionID), nil
}

// GetSubagent returns one durable subagent job scoped to a session.
func (s *Service) GetSubagent(ctx context.Context, sessionID, jobID string) (agent.DurableSubagentJobView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.DurableSubagentJobView{}, err
	}
	return session.agent.GetDurableSubagent(sessionID, jobID)
}

// ReviewSubagent returns the merge review for one durable subagent job.
func (s *Service) ReviewSubagent(ctx context.Context, sessionID, jobID string) (agent.DurableSubagentReviewView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.DurableSubagentReviewView{}, err
	}
	return session.agent.ReviewDurableSubagentView(sessionID, jobID)
}

// CancelSubagent requests cancellation of one durable subagent job.
func (s *Service) CancelSubagent(ctx context.Context, sessionID, jobID string) (agent.DurableSubagentJobView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.DurableSubagentJobView{}, err
	}
	result, err := session.agent.CancelDurableSubagentWithContext(agent.WithSubagentEvents(ctx, session.id, "", session.events), sessionID, jobID)
	if err != nil {
		return agent.DurableSubagentJobView{}, err
	}
	return result, nil
}

// ResumeSubagent resumes one interrupted, canceled, or errored durable subagent.
func (s *Service) ResumeSubagent(ctx context.Context, sessionID, jobID string) (agent.DurableSubagentJobView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.DurableSubagentJobView{}, err
	}
	result, err := session.agent.ResumeDurableSubagentViewWithContext(agent.WithSubagentEvents(ctx, session.id, "", session.events), sessionID, jobID)
	if err != nil {
		return agent.DurableSubagentJobView{}, err
	}
	return result, nil
}

// MergeSubagent applies reviewed subagent changes into the main workspace.
func (s *Service) MergeSubagent(ctx context.Context, sessionID, jobID string) (agent.DurableSubagentMergeView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.DurableSubagentMergeView{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.DurableSubagentMergeView{}, err
	}
	defer release()
	result, err := session.agent.MergeDurableSubagentViewWithContext(agent.WithSubagentEvents(ctx, session.id, "", session.events), sessionID, jobID)
	if err != nil {
		return agent.DurableSubagentMergeView{}, err
	}
	job, jobErr := session.agent.GetDurableSubagent(sessionID, jobID)
	if jobErr == nil {
		_ = s.appendSessionGraphMerge(session, job, "merged durable subagent "+jobID)
	}
	updatedAt := s.now()
	if err := s.persistSession(session, updatedAt); err != nil {
		return agent.DurableSubagentMergeView{}, err
	}
	return result, nil
}

// ListLongTasks returns durable LongTasks scoped to a session.
