package agent

import (
	"context"
	"sync"
)

type historyRecallTurnState struct {
	mu               sync.Mutex
	automaticCount   int
	automaticExposed bool
}

func (s *historyRecallTurnState) automaticUses() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.automaticCount
}

func (s *historyRecallTurnState) setAutomaticExposure(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.automaticExposed = enabled
}

func (s *historyRecallTurnState) consumeAutomaticExposure() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.automaticExposed {
		s.automaticCount++
	}
	s.automaticExposed = false
}

type historyRecallTurnStateKey struct{}

func withHistoryRecallTurnState(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(historyRecallTurnStateKey{}).(*historyRecallTurnState); ok {
		return ctx
	}
	return context.WithValue(ctx, historyRecallTurnStateKey{}, &historyRecallTurnState{})
}

func historyRecallTurnStateFromContext(ctx context.Context) *historyRecallTurnState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(historyRecallTurnStateKey{}).(*historyRecallTurnState)
	return state
}
