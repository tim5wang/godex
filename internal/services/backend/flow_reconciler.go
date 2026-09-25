package backend

import (
	"context"
	"fmt"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/platform/logger"
)

const flowRunReconcileInterval = time.Second

// Start owns the backend's durable FlowRun reconciler. The reconciler is
// lifecycle-bound to the serving process and resumes only runs explicitly
// started in auto-schedule mode.
func (s *Service) Start(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("backend service unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.flowReconcilerMu.Lock()
	defer s.flowReconcilerMu.Unlock()
	if s.flowReconcilerCancel != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.flowReconcilerCancel = cancel
	s.flowReconcilerDone = done
	go s.runFlowRunReconciler(runCtx, done)
	return nil
}

// Stop cancels the reconciler and waits for its final pass to exit.
func (s *Service) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.flowReconcilerMu.Lock()
	cancel := s.flowReconcilerCancel
	done := s.flowReconcilerDone
	s.flowReconcilerCancel = nil
	s.flowReconcilerDone = nil
	s.flowReconcilerMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	if done == nil {
		return nil
	}
	if ctx == nil {
		<-done
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) runFlowRunReconciler(ctx context.Context, done chan struct{}) {
	defer close(done)
	lastErrors := make(map[agent.FlowRunRef]string)
	needsStartupScan := true
	ticker := time.NewTicker(flowRunReconcileInterval)
	defer ticker.Stop()
	for {
		if needsStartupScan {
			if err := s.seedAutoScheduledFlowRuns(); err != nil {
				logger.Warnf("Flow run reconciler startup scan failed: %v", err)
			} else {
				needsStartupScan = false
			}
		}
		if err := s.reconcileTrackedFlowRuns(ctx, lastErrors); err != nil && ctx.Err() == nil {
			logger.Warnf("Flow run reconciler pass failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) seedAutoScheduledFlowRuns() error {
	a, err := s.flowAgent()
	if err != nil {
		return err
	}
	refs, err := a.AutoScheduledFlowRuns()
	if err != nil {
		return err
	}
	s.flowRunsMu.Lock()
	if s.flowRuns == nil {
		s.flowRuns = make(map[agent.FlowRunRef]struct{})
	}
	for _, ref := range refs {
		s.flowRuns[ref] = struct{}{}
	}
	s.flowRunsMu.Unlock()
	return nil
}

func (s *Service) reconcileTrackedFlowRuns(ctx context.Context, lastErrors map[agent.FlowRunRef]string) error {
	s.flowRunsMu.Lock()
	refs := make([]agent.FlowRunRef, 0, len(s.flowRuns))
	for ref := range s.flowRuns {
		refs = append(refs, ref)
	}
	s.flowRunsMu.Unlock()
	if len(refs) == 0 {
		return nil
	}
	a, err := s.flowAgent()
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		view, err := a.AdvanceFlowRun(ctx, ref.FlowID, ref.RunID)
		if err != nil {
			if lastErrors[ref] != err.Error() {
				logger.Warnf("Flow run reconciliation failed flow=%s run=%s: %v", ref.FlowID, ref.RunID, err)
				lastErrors[ref] = err.Error()
			}
			continue
		}
		delete(lastErrors, ref)
		if agentFlowRunTerminal(view.Status) {
			s.untrackFlowRun(ref)
		}
	}
	return nil
}

func (s *Service) trackFlowRun(ref agent.FlowRunRef, status string) {
	if s == nil || ref.FlowID == "" || ref.RunID == "" || agentFlowRunTerminal(status) {
		return
	}
	s.flowRunsMu.Lock()
	if s.flowRuns == nil {
		s.flowRuns = make(map[agent.FlowRunRef]struct{})
	}
	s.flowRuns[ref] = struct{}{}
	s.flowRunsMu.Unlock()
}

func (s *Service) untrackFlowRun(ref agent.FlowRunRef) {
	if s == nil {
		return
	}
	s.flowRunsMu.Lock()
	delete(s.flowRuns, ref)
	s.flowRunsMu.Unlock()
}

func agentFlowRunTerminal(status string) bool {
	switch status {
	case "completed", "error", "canceled":
		return true
	default:
		return false
	}
}
