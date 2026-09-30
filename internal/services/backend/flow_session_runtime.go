package backend

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/flow"
	"github.com/tim5wang/godex/internal/platform/logger"
)

var (
	ErrFlowSessionRuntimeUnavailable = errors.New("flow session runtime is not started")
	ErrFlowSessionMailboxFull        = errors.New("flow session runtime mailbox is full")
)

const (
	flowSessionWorkerCount       = 8
	flowSessionFastWorkerCount   = 4
	flowSessionSlowWorkerCount   = 4
	flowSessionWorkQueueCapacity = 256
	flowSessionSignalKeysPerFlow = 64
	flowSessionRecoveryInterval  = 2 * time.Second
	flowSessionLaneTickInterval  = 10 * time.Millisecond
	flowSessionDurableLaneKey    = "\x00durable"
)

type flowSessionRef struct {
	flowID    string
	sessionID string
}

type flowSessionJobKey struct {
	flowSessionRef
	laneID string
}

type flowSessionScheduledWork struct {
	dirty             bool
	inFlight          bool
	cancel            context.CancelFunc
	signal            *agent.FlowSessionEventInput
	work              *agent.FlowSessionWork
	execution         *agent.FlowSessionInFlight
	nextSequence      uint64
	interruptSequence uint64
	interrupting      bool
	interruptDone     chan struct{}
	token             uint64
}

type flowSessionAsyncTask struct {
	key        flowSessionJobKey
	token      uint64
	agent      *agent.Agent
	ctx        context.Context
	cancel     context.CancelFunc
	work       *agent.FlowSessionWork
	signal     *agent.FlowSessionEventInput
	laneClass  string
	deadlineMS int
}

type flowSessionPeriodicLane struct {
	key       flowSessionJobKey
	eventType string
	lane      flow.SessionLane
	next      time.Time
	sequence  uint64
	enabled   bool
}

type flowSessionScheduler struct {
	service *Service
	ctx     context.Context
	work    chan flowSessionJobKey
	fast    chan flowSessionAsyncTask
	normal  chan flowSessionAsyncTask
	slow    chan flowSessionAsyncTask
	done    chan struct{}

	mu         sync.Mutex
	jobs       map[flowSessionJobKey]*flowSessionScheduledWork
	signals    map[flowSessionJobKey]map[string]agent.FlowSessionEventInput
	periodic   map[flowSessionJobKey]*flowSessionPeriodicLane
	nextToken  uint64
	workRunner func(context.Context, *agent.Agent, *agent.FlowSessionWork) (agent.FlowSessionWorkResult, error)
}

func newFlowSessionScheduler(service *Service, ctx context.Context) *flowSessionScheduler {
	scheduler := &flowSessionScheduler{
		service:  service,
		ctx:      ctx,
		work:     make(chan flowSessionJobKey, flowSessionWorkQueueCapacity),
		fast:     make(chan flowSessionAsyncTask, flowSessionWorkQueueCapacity),
		normal:   make(chan flowSessionAsyncTask, flowSessionWorkQueueCapacity),
		slow:     make(chan flowSessionAsyncTask, flowSessionWorkQueueCapacity),
		done:     make(chan struct{}),
		jobs:     make(map[flowSessionJobKey]*flowSessionScheduledWork),
		signals:  make(map[flowSessionJobKey]map[string]agent.FlowSessionEventInput),
		periodic: make(map[flowSessionJobKey]*flowSessionPeriodicLane),
	}
	scheduler.workRunner = func(ctx context.Context, a *agent.Agent, work *agent.FlowSessionWork) (agent.FlowSessionWorkResult, error) {
		return a.ExecuteFlowSessionWork(ctx, work)
	}
	return scheduler
}

func (s *flowSessionScheduler) run() {
	defer close(s.done)
	var workers sync.WaitGroup
	for i := 0; i < flowSessionWorkerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.worker()
		}()
	}
	for i := 0; i < flowSessionWorkerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.taskWorker(s.normal)
		}()
	}
	for i := 0; i < flowSessionFastWorkerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.taskWorker(s.fast)
		}()
	}
	for i := 0; i < flowSessionSlowWorkerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.taskWorker(s.slow)
		}()
	}
	s.recoverPendingSessions()
	ticker := time.NewTicker(flowSessionRecoveryInterval)
	defer ticker.Stop()
	laneTicker := time.NewTicker(flowSessionLaneTickInterval)
	defer laneTicker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			s.mu.Lock()
			for _, job := range s.jobs {
				if job.execution != nil {
					job.execution.Status = "canceling"
				}
				if job.cancel != nil {
					job.cancel()
				}
			}
			s.mu.Unlock()
			workers.Wait()
			s.mu.Lock()
			clear(s.jobs)
			clear(s.signals)
			clear(s.periodic)
			s.mu.Unlock()
			return
		case <-ticker.C:
			s.recoverPendingSessions()
		case <-laneTicker.C:
			s.dispatchDuePeriodicLanes()
		}
	}
}

func (s *flowSessionScheduler) wake(ref flowSessionRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduleLocked(flowSessionJobKey{flowSessionRef: ref, laneID: flowSessionDurableLaneKey})
}

func (s *flowSessionScheduler) publishSignal(ref flowSessionRef, input agent.FlowSessionEventInput) (bool, error) {
	key := flowSessionJobKey{flowSessionRef: ref, laneID: input.LaneID}
	signalKey := input.Source + "\x00" + input.Type
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return false, ErrFlowSessionRuntimeUnavailable
	}
	pending := s.signals[key]
	if pending == nil {
		pending = make(map[string]agent.FlowSessionEventInput)
	}
	_, coalesced := pending[signalKey]
	job := s.jobs[key]
	if input.OverloadPolicy == flow.SessionLaneOverloadDropNewest &&
		(coalesced || (job != nil && job.inFlight)) {
		return true, nil
	}
	if !coalesced && len(pending) >= flowSessionSignalKeysPerFlow {
		return false, agent.ErrFlowSessionPendingLimit
	}
	if job == nil && !s.scheduleLocked(key) {
		return false, ErrFlowSessionMailboxFull
	}
	if s.signals[key] == nil {
		s.signals[key] = pending
	}
	input.Payload = append(input.Payload[:0:0], input.Payload...)
	s.signals[key][signalKey] = input
	if job = s.jobs[key]; job != nil {
		job.dirty = true
		if job.inFlight && input.OverloadPolicy == flow.SessionLaneOverloadCoalesceLatest && job.cancel != nil {
			job.cancel()
		}
	}
	return coalesced, nil
}

// scheduleLocked adds one coalesced wakeup. Durable payloads remain in the
// journal, so a full volatile work queue never loses accepted events.
func (s *flowSessionScheduler) scheduleLocked(key flowSessionJobKey) bool {
	if s.ctx.Err() != nil {
		return false
	}
	if job := s.jobs[key]; job != nil {
		job.dirty = true
		return true
	}
	if len(s.jobs) >= flowSessionWorkQueueCapacity {
		return false
	}
	job := &flowSessionScheduledWork{dirty: true}
	s.jobs[key] = job
	select {
	case s.work <- key:
		return true
	default:
		delete(s.jobs, key)
		return false
	}
}

func (s *flowSessionScheduler) worker() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case key := <-s.work:
			s.process(key)
		}
	}
}

func (s *flowSessionScheduler) process(key flowSessionJobKey) {
	ref := key.flowSessionRef
	for {
		s.mu.Lock()
		job := s.jobs[key]
		if job == nil || job.inFlight || job.interrupting {
			s.mu.Unlock()
			return
		}
		expectedJob := job
		job.dirty = false
		job.nextSequence = 0
		s.mu.Unlock()

		a, err := s.service.flowAgent()
		var view agent.FlowSessionView
		if err == nil {
			view, err = a.GetFlowSession(ref.flowID, ref.sessionID)
			if err == nil && view.Status != "active" {
				s.mu.Lock()
				if s.jobs[key] == expectedJob {
					delete(s.jobs, key)
				}
				if view.Status == "completed" || view.Status == "canceled" {
					for signalKey := range s.signals {
						if signalKey.flowSessionRef == ref {
							delete(s.signals, signalKey)
						}
					}
				}
				s.mu.Unlock()
				return
			}
		}
		if err != nil && s.ctx.Err() == nil {
			logger.Warnf("Flow session reconciliation failed flow=%s session=%s: %v", ref.flowID, ref.sessionID, err)
			s.mu.Lock()
			if s.jobs[key] == expectedJob {
				delete(s.jobs, key)
			}
			s.mu.Unlock()
			return
		}
		if s.ctx.Err() != nil {
			return
		}

		if key.laneID == flowSessionDurableLaneKey {
			if view.LastSequence > view.ProcessedSequence {
				s.mu.Lock()
				if s.jobs[key] == expectedJob {
					expectedJob.nextSequence = view.ProcessedSequence + 1
				}
				s.mu.Unlock()
			}
			work, err := a.PrepareFlowSessionWork(ref.flowID, ref.sessionID)
			if err != nil {
				if s.ctx.Err() == nil {
					logger.Warnf("Flow session work preparation failed flow=%s session=%s: %v", ref.flowID, ref.sessionID, err)
				}
				s.mu.Lock()
				if s.jobs[key] == expectedJob {
					delete(s.jobs, key)
				}
				s.mu.Unlock()
				return
			}
			if work != nil {
				progress := work.Progress()
				s.mu.Lock()
				if s.jobs[key] != expectedJob {
					s.mu.Unlock()
					return
				}
				expectedJob.work = work
				expectedJob.nextSequence = progress.InputSequence
				s.mu.Unlock()
				s.dispatch(key, expectedJob, a, work, nil)
				return
			}
		} else if view.LastSequence > view.ProcessedSequence {
			// Durable journal order wins over volatile state updates. Keep the
			// signal queued; the durable lane schedules it again after checkpoint.
			s.mu.Lock()
			if s.jobs[key] == expectedJob {
				delete(s.jobs, key)
			}
			s.mu.Unlock()
			return
		}

		if key.laneID != flowSessionDurableLaneKey {
			if signal, ok := s.takeSignal(key); ok {
				s.dispatch(key, expectedJob, a, nil, &signal)
				return
			}
		} else {
			// A durable checkpoint may make previously blocked signals runnable.
			s.schedulePendingSignals(ref)
		}

		s.mu.Lock()
		job = s.jobs[key]
		if job == nil || job != expectedJob || job.interrupting {
			s.mu.Unlock()
			return
		}
		if job.dirty {
			s.mu.Unlock()
			continue
		}
		delete(s.jobs, key)
		s.mu.Unlock()
		return
	}
}

func (s *flowSessionScheduler) dispatch(
	key flowSessionJobKey,
	expectedJob *flowSessionScheduledWork,
	a *agent.Agent,
	work *agent.FlowSessionWork,
	signal *agent.FlowSessionEventInput,
) bool {
	laneClass := flow.SessionLaneClassStandard
	deadlineMS := 0
	if work != nil {
		_, capturedClass, capturedDeadline := work.LaneConfig()
		if capturedClass != "" {
			laneClass = capturedClass
		}
		deadlineMS = capturedDeadline
	} else if signal != nil {
		if signal.LaneClass != "" {
			laneClass = signal.LaneClass
		}
		deadlineMS = signal.DeadlineMS
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if deadlineMS > 0 {
		ctx, cancel = context.WithTimeout(s.ctx, time.Duration(deadlineMS)*time.Millisecond)
	} else {
		ctx, cancel = context.WithCancel(s.ctx)
	}
	s.mu.Lock()
	job := s.jobs[key]
	if job == nil || job != expectedJob || job.inFlight || job.interrupting || s.ctx.Err() != nil {
		s.mu.Unlock()
		cancel()
		return false
	}
	s.nextToken++
	job.inFlight = true
	job.cancel = cancel
	job.signal = signal
	job.work = work
	job.token = s.nextToken
	var execution *agent.FlowSessionInFlight
	switch {
	case work != nil:
		progress := work.Progress()
		if progress.EventType != agent.FlowSessionOutputEventType {
			execution = &progress
		}
	case signal != nil:
		execution = &agent.FlowSessionInFlight{
			Delivery:       flow.SessionDeliveryLatestWins,
			LaneID:         signal.LaneID,
			EventType:      signal.Type,
			Source:         signal.Source,
			SourceSequence: signal.SourceSequence,
			CorrelationID:  signal.CorrelationID,
		}
	}
	if execution != nil {
		execution.Status = "queued"
		execution.QueuedAt = time.Now()
	}
	job.execution = execution
	task := flowSessionAsyncTask{
		key: key, token: job.token, agent: a, ctx: ctx, cancel: cancel,
		work: work, signal: signal, laneClass: laneClass, deadlineMS: deadlineMS,
	}
	queue := s.normal
	switch laneClass {
	case flow.SessionLaneClassFast:
		queue = s.fast
	case flow.SessionLaneClassSlow:
		queue = s.slow
	}
	select {
	case queue <- task:
		s.mu.Unlock()
		notifyFlowSessionChanged(key.flowSessionRef)
		return true
	default:
		job.inFlight = false
		job.cancel = nil
		job.signal = nil
		job.execution = nil
		delete(s.jobs, key)
		s.mu.Unlock()
		cancel()
		if signal != nil {
			s.restoreSignals(key, []agent.FlowSessionEventInput{*signal})
		}
		return false
	}
}

func (s *flowSessionScheduler) taskWorker(queue <-chan flowSessionAsyncTask) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case task := <-queue:
			s.runAsyncTask(task)
		}
	}
}

func (s *flowSessionScheduler) runAsyncTask(task flowSessionAsyncTask) {
	startedAt := time.Now()
	started := false
	s.mu.Lock()
	if job := s.jobs[task.key]; job != nil && job.token == task.token &&
		job.execution != nil && task.ctx.Err() == nil {
		job.execution.Status = "running"
		job.execution.StartedAt = &startedAt
		started = true
	}
	s.mu.Unlock()
	if started {
		notifyFlowSessionChanged(task.key.flowSessionRef)
	}

	var err error
	switch {
	case task.work != nil:
		task.work.SetProgressHandler(func(progress agent.FlowSessionProgressUpdate) {
			s.reportWorkProgress(task.key, task.token, progress)
		})
		if err = task.ctx.Err(); err == nil {
			result, runErr := s.workRunner(task.ctx, task.agent, task.work)
			err = runErr
			if err == nil && task.ctx.Err() == nil {
				err = task.agent.CommitFlowSessionWork(task.work, result)
			} else if err == nil {
				err = task.ctx.Err()
			}
		}
	case task.signal != nil:
		err = task.agent.ProcessFlowSessionSignal(task.ctx, task.key.flowID, task.key.sessionID, *task.signal)
	}

	retry := err == nil
	switch {
	case errors.Is(err, agent.ErrFlowSessionPendingEvents):
		retry = false
		if task.signal != nil {
			s.restoreSignals(task.key, []agent.FlowSessionEventInput{*task.signal})
		}
	case errors.Is(err, agent.ErrFlowSessionStaleWork), errors.Is(err, agent.ErrFlowSessionStaleSignal):
		retry = false
	case err != nil && task.ctx.Err() == nil && s.ctx.Err() == nil:
		retry = false
		if task.signal != nil {
			s.restoreSignals(task.key, []agent.FlowSessionEventInput{*task.signal})
		}
		if task.work != nil {
			logger.Warnf("Flow session worker failed flow=%s session=%s: %v", task.key.flowID, task.key.sessionID, err)
		} else {
			logger.Warnf("Flow session signal failed flow=%s session=%s type=%s: %v", task.key.flowID, task.key.sessionID, task.signal.Type, err)
		}
	}

	task.cancel()
	for {
		s.mu.Lock()
		job := s.jobs[task.key]
		if job == nil || job.token != task.token {
			s.mu.Unlock()
			return
		}
		if job.interrupting {
			done := job.interruptDone
			s.mu.Unlock()
			if done != nil {
				<-done
				continue
			}
		}
		retry = retry || job.dirty
		delete(s.jobs, task.key)
		if retry && s.ctx.Err() == nil {
			s.scheduleLocked(task.key)
		}
		if task.key.laneID == flowSessionDurableLaneKey && s.ctx.Err() == nil {
			s.schedulePendingSignalsLocked(task.key.flowSessionRef)
		}
		s.mu.Unlock()
		break
	}
	if err == nil {
		notifyFlowSessionChanged(task.key.flowSessionRef)
	}
}

func (s *flowSessionScheduler) reportWorkProgress(
	key flowSessionJobKey,
	token uint64,
	progress agent.FlowSessionProgressUpdate,
) {
	s.mu.Lock()
	job := s.jobs[key]
	if job == nil || job.token != token || job.execution == nil {
		s.mu.Unlock()
		return
	}
	job.execution.NodeID = progress.NodeID
	job.execution.Phase = progress.Phase
	job.execution.ToolName = progress.ToolName
	s.mu.Unlock()
	notifyFlowSessionChanged(key.flowSessionRef)
}

func (s *flowSessionScheduler) interruptFlowSessionWork(
	ref flowSessionRef,
	a *agent.Agent,
	work *agent.FlowSessionWork,
) (bool, error) {
	if a == nil || work == nil {
		return false, fmt.Errorf("flow session work unavailable")
	}
	progress := work.Progress()
	key := flowSessionJobKey{
		flowSessionRef: ref,
		laneID:         flowSessionDurableLaneKey,
	}
	s.mu.Lock()
	job := s.jobs[key]
	if job != nil {
		if job.work != nil && job.work.Progress().InputSequence != progress.InputSequence {
			s.mu.Unlock()
			return false, nil
		}
		if job.work == nil && job.nextSequence != 0 && job.nextSequence != progress.InputSequence {
			s.mu.Unlock()
			return false, nil
		}
		if job.interrupting {
			s.mu.Unlock()
			return false, nil
		}
		job.interrupting = true
		job.interruptSequence = progress.InputSequence
		job.interruptDone = make(chan struct{})
		job.dirty = true
		if job.execution != nil {
			job.execution.Status = "canceling"
		}
		if job.cancel != nil {
			job.cancel()
		}
	}
	s.mu.Unlock()
	if job != nil {
		notifyFlowSessionChanged(ref)
	}

	canceled, err := a.CancelFlowSessionWork(work)

	if job != nil {
		s.mu.Lock()
		if job.interrupting && job.interruptSequence == progress.InputSequence {
			job.interrupting = false
			if job.interruptDone != nil {
				close(job.interruptDone)
			}
			job.interruptDone = nil
			if s.jobs[key] == job && !job.inFlight {
				delete(s.jobs, key)
				s.scheduleLocked(key)
			} else if s.jobs[key] == job {
				job.dirty = true
			}
		}
		s.mu.Unlock()
	}
	return canceled, err
}

func (s *flowSessionScheduler) inFlight(ref flowSessionRef) *agent.FlowSessionInFlight {
	inFlight := s.inFlightLanes(ref)
	if len(inFlight) == 0 {
		return nil
	}
	return &inFlight[0]
}

func (s *flowSessionScheduler) inFlightLanes(ref flowSessionRef) []agent.FlowSessionInFlight {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]flowSessionJobKey, 0)
	for key, job := range s.jobs {
		if key.flowSessionRef == ref && job.inFlight && job.execution != nil {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].laneID < keys[j].laneID })
	out := make([]agent.FlowSessionInFlight, 0, len(keys))
	for _, key := range keys {
		job := s.jobs[key]
		progress := *job.execution
		if job.execution.StartedAt != nil {
			startedAt := *job.execution.StartedAt
			progress.StartedAt = &startedAt
			if elapsed := time.Since(startedAt).Milliseconds(); elapsed > 0 {
				progress.DurationMS = elapsed
			}
		}
		out = append(out, progress)
	}
	return out
}

func (s *flowSessionScheduler) takeSignal(key flowSessionJobKey) (agent.FlowSessionEventInput, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.signals[key]
	if len(pending) == 0 {
		delete(s.signals, key)
		return agent.FlowSessionEventInput{}, false
	}
	keys := make([]string, 0, len(pending))
	for key := range pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	signalKey := keys[0]
	signal := pending[signalKey]
	delete(pending, signalKey)
	if len(pending) == 0 {
		delete(s.signals, key)
	}
	return signal, true
}

func (s *flowSessionScheduler) restoreSignals(key flowSessionJobKey, signals []agent.FlowSessionEventInput) {
	if len(signals) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	for _, signal := range signals {
		s.restoreSignalLocked(key, signal)
	}
	if job := s.jobs[key]; job != nil {
		job.dirty = true
	}
}

func (s *flowSessionScheduler) restoreSignalLocked(key flowSessionJobKey, signal agent.FlowSessionEventInput) {
	pending := s.signals[key]
	if pending == nil {
		pending = make(map[string]agent.FlowSessionEventInput)
		s.signals[key] = pending
	}
	signalKey := signal.Source + "\x00" + signal.Type
	if _, newerAlreadyQueued := pending[signalKey]; newerAlreadyQueued {
		return
	}
	signal.Payload = append(signal.Payload[:0:0], signal.Payload...)
	pending[signalKey] = signal
}

func (s *flowSessionScheduler) cancelSession(ref flowSessionRef, preserveSignals bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, job := range s.jobs {
		if key.flowSessionRef != ref {
			continue
		}
		if preserveSignals && job.signal != nil {
			s.restoreSignalLocked(key, *job.signal)
		}
		if job.execution != nil {
			job.execution.Status = "canceling"
		}
		if job.cancel != nil {
			job.cancel()
		}
		if job.inFlight {
			// Keep the per-session slot until the canceled worker returns. A
			// quick resume can mark it dirty, but cannot launch overlapping work.
			job.signal = nil
			job.dirty = true
		} else {
			delete(s.jobs, key)
		}
	}
	if !preserveSignals {
		for key := range s.signals {
			if key.flowSessionRef == ref {
				delete(s.signals, key)
			}
		}
		for key := range s.periodic {
			if key.flowSessionRef == ref {
				delete(s.periodic, key)
			}
		}
	}
}

func (s *flowSessionScheduler) recoverPendingSessions() {
	if s.ctx.Err() != nil {
		return
	}
	s.wakePendingSignals()
	flows, err := s.service.ListFlows()
	if err != nil {
		if s.ctx.Err() == nil {
			logger.Warnf("Flow session startup scan failed: %v", err)
		}
		return
	}
	activePeriodic := make(map[flowSessionJobKey]struct{})
	for _, item := range flows {
		if s.ctx.Err() != nil {
			return
		}
		sessions, err := s.service.ListFlowSessions(item.FlowID)
		if err != nil {
			logger.Warnf("Flow session scan failed flow=%s: %v", item.FlowID, err)
			continue
		}
		for _, session := range sessions {
			if session.Status != "active" {
				continue
			}
			ref := flowSessionRef{flowID: session.FlowID, sessionID: session.SessionID}
			if session.LastSequence > session.ProcessedSequence {
				s.wake(ref)
			}
			spec, err := s.service.FlowSessionWorkflow(ref.flowID, ref.sessionID)
			if err != nil {
				logger.Warnf("Flow session lane scan failed flow=%s session=%s: %v", ref.flowID, ref.sessionID, err)
				continue
			}
			for key := range s.periodicLaneKeys(ref, spec) {
				activePeriodic[key] = struct{}{}
			}
			s.syncPeriodicLanes(ref, spec)
		}
	}
	s.mu.Lock()
	for key := range s.periodic {
		if _, active := activePeriodic[key]; !active {
			delete(s.periodic, key)
		}
	}
	s.mu.Unlock()
}

func (s *flowSessionScheduler) syncPeriodicLanes(ref flowSessionRef, spec *flow.SessionWorkflowSpec) {
	desired := make(map[flowSessionJobKey]flowSessionPeriodicLane)
	if spec != nil {
		lanes := make(map[string]flow.SessionLane, len(spec.Lanes))
		for _, lane := range spec.Lanes {
			if lane.Cadence == flow.SessionLaneCadencePeriodic || lane.Cadence == flow.SessionLaneCadenceHybrid {
				lanes[lane.ID] = lane
			}
		}
		for _, trigger := range spec.Triggers {
			lane, exists := lanes[trigger.LaneID]
			if !exists {
				continue
			}
			key := flowSessionJobKey{flowSessionRef: ref, laneID: lane.ID}
			desired[key] = flowSessionPeriodicLane{
				key: key, eventType: trigger.EventType, lane: lane,
				next:    time.Now().Add(time.Duration(lane.IntervalMS) * time.Millisecond),
				enabled: true,
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.periodic {
		if key.flowSessionRef == ref {
			if _, keep := desired[key]; !keep {
				delete(s.periodic, key)
			}
		}
	}
	for key, initial := range desired {
		if s.periodic[key] == nil {
			s.periodic[key] = &initial
		} else {
			s.periodic[key].enabled = true
		}
	}
}

func (s *flowSessionScheduler) setPeriodicActive(ref flowSessionRef, active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, scheduled := range s.periodic {
		if key.flowSessionRef != ref {
			continue
		}
		scheduled.enabled = active
		if active {
			scheduled.next = time.Now().Add(time.Duration(scheduled.lane.IntervalMS) * time.Millisecond)
		}
	}
}

func (s *flowSessionScheduler) periodicLaneKeys(ref flowSessionRef, spec *flow.SessionWorkflowSpec) map[flowSessionJobKey]struct{} {
	keys := make(map[flowSessionJobKey]struct{})
	if spec == nil {
		return keys
	}
	lanes := make(map[string]struct{}, len(spec.Lanes))
	for _, lane := range spec.Lanes {
		if lane.Cadence == flow.SessionLaneCadencePeriodic || lane.Cadence == flow.SessionLaneCadenceHybrid {
			lanes[lane.ID] = struct{}{}
		}
	}
	for _, trigger := range spec.Triggers {
		if _, exists := lanes[trigger.LaneID]; exists {
			keys[flowSessionJobKey{flowSessionRef: ref, laneID: trigger.LaneID}] = struct{}{}
		}
	}
	return keys
}

func (s *flowSessionScheduler) wakePendingSignals() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, signals := range s.signals {
		if len(signals) > 0 {
			s.scheduleLocked(key)
		}
	}
}

func (s *flowSessionScheduler) schedulePendingSignals(ref flowSessionRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.schedulePendingSignalsLocked(ref)
}

func (s *flowSessionScheduler) schedulePendingSignalsLocked(ref flowSessionRef) {
	for key, signals := range s.signals {
		if key.flowSessionRef == ref && len(signals) > 0 {
			s.scheduleLocked(key)
		}
	}
}

func (s *flowSessionScheduler) dispatchDuePeriodicLanes() {
	type dueSignal struct {
		ref   flowSessionRef
		input agent.FlowSessionEventInput
	}
	now := time.Now()
	var due []dueSignal
	s.mu.Lock()
	for _, scheduled := range s.periodic {
		if !scheduled.enabled || now.Before(scheduled.next) {
			continue
		}
		interval := time.Duration(scheduled.lane.IntervalMS) * time.Millisecond
		missed := now.Sub(scheduled.next)/interval + 1
		scheduled.next = scheduled.next.Add(missed * interval)
		scheduled.sequence++
		lane := scheduled.lane
		due = append(due, dueSignal{
			ref: scheduled.key.flowSessionRef,
			input: agent.FlowSessionEventInput{
				Source:         "godex-lane:" + lane.ID,
				SourceSequence: scheduled.sequence,
				Type:           scheduled.eventType,
				Payload:        []byte(`{}`),
				LaneID:         lane.ID,
				LaneClass:      lane.Class,
				DeadlineMS:     lane.DeadlineMS,
				OverloadPolicy: lane.OverloadPolicy,
			},
		})
	}
	s.mu.Unlock()
	for _, item := range due {
		if _, err := s.publishSignal(item.ref, item.input); err != nil && s.ctx.Err() == nil {
			logger.Warnf("Flow session periodic lane enqueue failed flow=%s session=%s lane=%s: %v",
				item.ref.flowID, item.ref.sessionID, item.input.LaneID, err)
		}
	}
}
