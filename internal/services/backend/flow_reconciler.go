package backend

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/platform/processlock"
)

const flowRunReconcileInterval = time.Second

const flowSessionStateLockFilename = ".flow-session-runtime.lock"

// Start acquires exclusive ownership of the local state directory before
// starting the FlowRun reconciler and FlowSession scheduler.
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
	if s.flowSessionStopping {
		return fmt.Errorf("FlowSession runtime is still stopping")
	}
	if s.cfg == nil || strings.TrimSpace(s.cfg.StateDir) == "" {
		return fmt.Errorf("missing state directory for FlowSession runtime")
	}
	stateLock, err := processlock.Acquire(filepath.Join(s.cfg.StateDir, flowSessionStateLockFilename))
	if err != nil {
		if errors.Is(err, processlock.ErrLocked) {
			return fmt.Errorf("state directory %q is already owned by another GoDex runtime; only one backend runtime per local state directory is supported: %w", s.cfg.StateDir, err)
		}
		return fmt.Errorf("acquire FlowSession runtime lock for state directory %q: %w", s.cfg.StateDir, err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	sessionScheduler := newFlowSessionScheduler(s, runCtx)
	s.flowSessionStateLock = stateLock
	s.flowReconcilerCancel = cancel
	s.flowReconcilerDone = done
	s.flowSessionScheduler = sessionScheduler
	go s.runFlowRunReconciler(runCtx, done)
	go sessionScheduler.run()
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
	sessionScheduler := s.flowSessionScheduler
	if cancel == nil && sessionScheduler == nil {
		s.flowReconcilerMu.Unlock()
		return nil
	}
	s.flowReconcilerCancel = nil
	s.flowReconcilerDone = nil
	s.flowSessionScheduler = nil
	s.flowSessionStopping = true
	stateLock := s.flowSessionStateLock
	s.flowReconcilerMu.Unlock()
	if cancel != nil {
		cancel()
	}
	waits := []<-chan struct{}{done, flowSessionSchedulerDone(sessionScheduler)}
	for _, wait := range waits {
		if wait == nil {
			continue
		}
		if ctx == nil {
			<-wait
			continue
		}
		select {
		case <-wait:
		case <-ctx.Done():
			go func() {
				for _, pending := range waits {
					if pending != nil {
						<-pending
					}
				}
				if err := s.finishFlowSessionStop(stateLock); err != nil {
					logger.Warnf("release FlowSession runtime lock: %v", err)
				}
			}()
			return ctx.Err()
		}
	}
	return s.finishFlowSessionStop(stateLock)
}

func (s *Service) finishFlowSessionStop(stateLock *processlock.Lock) error {
	s.flowReconcilerMu.Lock()
	defer s.flowReconcilerMu.Unlock()
	if stateLock != nil && s.flowSessionStateLock == stateLock {
		err := stateLock.Close()
		s.flowSessionStateLock = nil
		s.flowSessionStopping = false
		return err
	}
	if s.flowSessionStateLock == nil {
		s.flowSessionStopping = false
	}
	return nil
}

func flowSessionSchedulerDone(scheduler *flowSessionScheduler) <-chan struct{} {
	if scheduler == nil {
		return nil
	}
	return scheduler.done
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
