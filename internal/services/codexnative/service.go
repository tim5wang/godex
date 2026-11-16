package codexnative

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/platform/logger"
)

var (
	ErrUnavailable  = errors.New("Codex shared app-server unavailable; start the shared runtime in the native CLI first")
	ErrForbidden    = errors.New("Codex thread is outside the allowed workspace roots")
	ErrConflict     = errors.New("Codex session state changed; refresh before retrying")
	ErrActiveWriter = errors.New("Codex thread is owned by another runtime; connect clients to the same app-server")
	ErrInvalid      = errors.New("invalid Codex request")
)

// Options are node-owner configuration, never remote request parameters.
type Options struct {
	Command        string
	Socket         string
	WorkspaceRoots []string
	Dial           func(context.Context) (io.ReadWriteCloser, error)
}

type Service struct {
	opts      Options
	mu        sync.Mutex
	client    *client
	actions   map[string]*operation
	workers   map[string]*queueWorker
	done      chan struct{}
	closeOnce sync.Once
}

type operation struct {
	fingerprint [32]byte
	done        chan struct{}
	result      json.RawMessage
	err         error
	created     time.Time
}

type queueWorker struct{ revision uint64 }

type Thread struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name,omitempty"`
	Preview              string          `json:"preview"`
	Cwd                  string          `json:"cwd"`
	Model                string          `json:"model,omitempty"`
	ReasoningEffort      string          `json:"reasoningEffort,omitempty"`
	ModelProvider        string          `json:"modelProvider"`
	CreatedAt            int64           `json:"createdAt"`
	UpdatedAt            int64           `json:"updatedAt"`
	Status               json.RawMessage `json:"status"`
	CanAcceptDirectInput *bool           `json:"canAcceptDirectInput,omitempty"`
}

type Page struct {
	Data       []json.RawMessage `json:"data"`
	NextCursor *string           `json:"nextCursor"`
}

type ThreadPage struct {
	Data       []Thread `json:"data"`
	NextCursor *string  `json:"nextCursor"`
}

type Access struct {
	Thread          Thread `json:"thread"`
	LoadedInRuntime bool   `json:"loadedInRuntime"`
}

type loadedPage struct {
	Data       []string `json:"data"`
	NextCursor *string  `json:"nextCursor"`
}

type ReasoningEffortOption struct {
	ReasoningEffort string `json:"reasoningEffort"`
	Description     string `json:"description"`
}

type Model struct {
	ID                        string                  `json:"id"`
	Model                     string                  `json:"model"`
	DisplayName               string                  `json:"displayName"`
	Hidden                    bool                    `json:"hidden"`
	DefaultReasoningEffort    string                  `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []ReasoningEffortOption `json:"supportedReasoningEfforts"`
}

// Only these two thread-scoped settings may be changed from Godex. Permission,
// instruction and provider overrides remain owned by the native client.
type ModelSettings struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

type Snapshot struct {
	Thread    Thread     `json:"thread"`
	Turns     Page       `json:"turns"`
	Approvals []approval `json:"approvals"`
	Sequence  uint64     `json:"sequence"`
}

type Status struct {
	Ready          bool     `json:"ready"`
	Reason         string   `json:"reason,omitempty"`
	Executable     string   `json:"executable,omitempty"`
	Socket         string   `json:"socket"`
	WorkspaceRoots []string `json:"workspaceRoots"`
}

func New(opts Options) *Service {
	if opts.Command == "" {
		opts.Command = os.Getenv("GODEX_CODEX_BIN")
	}
	if opts.Socket == "" {
		opts.Socket = os.Getenv("GODEX_CODEX_SOCKET")
	}
	if opts.Socket == "" {
		home := os.Getenv("CODEX_HOME")
		if home == "" {
			userHome, _ := os.UserHomeDir()
			home = filepath.Join(userHome, ".codex")
		}
		opts.Socket = filepath.Join(home, "app-server-control", "app-server-control.sock")
	}
	if roots := os.Getenv("GODEX_CODEX_WORKSPACE_ROOTS"); roots != "" {
		opts.WorkspaceRoots = filepath.SplitList(roots)
	}
	return &Service{opts: opts, actions: make(map[string]*operation), workers: make(map[string]*queueWorker), done: make(chan struct{})}
}

func (s *Service) dial(ctx context.Context) (rpcTransport, error) {
	if s.opts.Dial != nil {
		stream, err := s.opts.Dial(ctx)
		if err != nil {
			return nil, err
		}
		return newLineTransport(stream), nil
	}
	info, err := os.Stat(s.opts.Socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		if _, cliErr := s.executable(); cliErr != nil {
			return nil, cliErr
		}
		return nil, fmt.Errorf("%w (%s)", ErrUnavailable, s.opts.Socket)
	}
	return dialSocket(ctx, s.opts.Socket)
}

func (s *Service) connect(ctx context.Context) (*client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return nil, ErrUnavailable
	default:
	}
	if s.client != nil {
		select {
		case <-s.client.done:
			s.client = nil
		default:
			return s.client, nil
		}
	}
	transport, err := s.dial(ctx)
	if err != nil {
		return nil, err
	}
	c := newClient(transport)
	_, err = c.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "godex_native_connector", "title": "Godex", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true, "requiredHumanApproval": true}}, nil)
	if err != nil {
		c.close(err)
		return nil, fmt.Errorf("Codex initialize: %w", err)
	}
	err = c.write(ctx, map[string]any{"method": "initialized"})
	if err != nil {
		c.close(err)
		return nil, fmt.Errorf("Codex initialized notification: %w", err)
	}
	s.client = c
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-ticker.C:
				c.mu.Lock()
				watchers := len(c.subs)
				pending := len(c.pending)
				approvals := len(c.approvals)
				c.mu.Unlock()
				if watchers == 0 && pending == 0 && approvals == 0 && time.Since(time.Unix(0, c.lastUsed.Load())) > time.Minute {
					c.close(io.EOF)
					return
				}
			}
		}
	}()
	return c, nil
}

func (s *Service) Close() {
	s.closeOnce.Do(func() { close(s.done) })
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		s.client.close(io.EOF)
		s.client = nil
	}
}

func (s *Service) Status(ctx context.Context) Status {
	status := Status{Socket: s.opts.Socket, WorkspaceRoots: s.opts.WorkspaceRoots}
	status.Executable, _ = s.executable()
	c, err := s.connect(ctx)
	if err == nil {
		_, err = c.call(ctx, "thread/loaded/list", map[string]any{}, nil)
		if err != nil {
			if ctx.Err() != nil {
				c.close(err)
			}
			err = fmt.Errorf("Codex thread/loaded/list readiness probe: %w", err)
		}
	}
	status.Ready = err == nil
	if err != nil {
		status.Reason = err.Error()
	}
	return status
}

func (s *Service) allowed(cwd string) bool {
	if !filepath.IsAbs(cwd) {
		return false
	}
	// Empty scope fails closed. Owner-configured roots apply to reads AND writes.
	canonical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return false
	}
	for _, root := range s.opts.WorkspaceRoots {
		base, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(base, canonical)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (s *Service) read(ctx context.Context, c *client, id string) (Thread, error) {
	var response struct {
		Thread Thread `json:"thread"`
	}
	_, err := c.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &response)
	if err != nil {
		return Thread{}, err
	}
	if !s.allowed(response.Thread.Cwd) {
		return Thread{}, ErrForbidden
	}
	return response.Thread, nil
}

func (s *Service) List(ctx context.Context, cursor, search string) (ThreadPage, error) {
	c, err := s.connect(ctx)
	if err != nil {
		return ThreadPage{}, err
	}
	params := map[string]any{"limit": 50, "sortKey": "updated_at", "sortDirection": "desc", "useStateDbOnly": true, "modelProviders": []string{}}
	if cursor != "" {
		params["cursor"] = cursor
	}
	if search != "" {
		params["searchTerm"] = search
	}
	var page ThreadPage
	if _, err := c.call(ctx, "thread/list", params, &page); err != nil {
		return ThreadPage{}, err
	}
	visible := make([]Thread, 0, len(page.Data))
	for _, thread := range page.Data {
		if s.allowed(thread.Cwd) {
			visible = append(visible, thread)
		}
	}
	page.Data = visible
	return page, nil
}

// ListLoaded lists this runtime's threads, not a client-side filtered page of
// disk history. Native pagination still advances across out-of-scope threads.
func (s *Service) ListLoaded(ctx context.Context, cursor, search string) (ThreadPage, error) {
	c, err := s.connect(ctx)
	if err != nil {
		return ThreadPage{}, err
	}
	var loaded loadedPage
	params := map[string]any{"limit": 50}
	if cursor != "" {
		params["cursor"] = cursor
	}
	if _, err = c.call(ctx, "thread/loaded/list", params, &loaded); err != nil {
		return ThreadPage{}, err
	}
	page := ThreadPage{Data: make([]Thread, 0, len(loaded.Data)), NextCursor: loaded.NextCursor}
	for _, id := range loaded.Data {
		thread, err := s.read(ctx, c, id)
		if errors.Is(err, ErrForbidden) {
			continue
		}
		if err != nil {
			return ThreadPage{}, err
		}
		var status struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(thread.Status, &status)
		// A thread can unload between enumeration and its metadata read.
		if status.Type == "notLoaded" {
			continue
		}
		if search == "" || strings.Contains(strings.ToLower(thread.Name+"\n"+thread.Preview), strings.ToLower(search)) {
			page.Data = append(page.Data, thread)
		}
	}
	return page, nil
}

// Access is a read-only snapshot, not a writer lease. In particular, absence
// from this runtime cannot distinguish an idle saved thread from a live thread
// owned by another process. Only an explicitly requested resume decides that.
func (s *Service) Access(ctx context.Context, id string) (Access, error) {
	c, err := s.connect(ctx)
	if err != nil {
		return Access{}, err
	}
	thread, err := s.read(ctx, c, id)
	if err != nil {
		return Access{}, err
	}
	result := Access{Thread: thread}
	params := map[string]any{"limit": 200}
	seen := make(map[string]bool)
	for {
		var page loadedPage
		if _, err := c.call(ctx, "thread/loaded/list", params, &page); err != nil {
			return Access{}, err
		}
		for _, loaded := range page.Data {
			if loaded == id {
				result.LoadedInRuntime = true
				return result, nil
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return result, nil
		}
		if seen[*page.NextCursor] {
			return Access{}, fmt.Errorf("invalid Codex loaded-thread pagination cursor")
		}
		seen[*page.NextCursor] = true
		params["cursor"] = *page.NextCursor
	}
}

func (s *Service) History(ctx context.Context, id, cursor string) (Snapshot, error) {
	c, err := s.connect(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	thread, err := s.read(ctx, c, id)
	if err != nil {
		return Snapshot{}, err
	}
	// A full turn may contain hundreds of tools. Keep the first paint small;
	// older history remains available through the same native pagination cursor.
	limit := 5
	if cursor != "" {
		limit = 20
	}
	params := map[string]any{"threadId": id, "limit": limit, "sortDirection": "desc", "itemsView": "full"}
	if cursor != "" {
		params["cursor"] = cursor
	}
	result := Snapshot{Thread: thread, Approvals: c.pendingApprovals(id)}
	result.Sequence, err = c.call(ctx, "thread/turns/list", params, &result.Turns)
	return result, err
}

// Watch subscribes before resume, and returns the response sequence as a
// hydration barrier. No model, cwd, instructions or permission overrides exist.
func (s *Service) Watch(ctx context.Context, id string) (Snapshot, <-chan Event, func(), error) {
	return s.watch(ctx, id, 5)
}

func (s *Service) watch(ctx context.Context, id string, limit int) (Snapshot, <-chan Event, func(), error) {
	started := time.Now()
	stage, ready, reused := "connect", false, false
	var lockWait, hydrateTime time.Duration
	defer func() {
		logger.Infof("codex native watch: thread=%q stage=%s reused=%t lock_wait=%s hydrate=%s total=%s accepted=%t", id, stage, reused, lockWait.Round(time.Millisecond), hydrateTime.Round(time.Millisecond), time.Since(started).Round(time.Millisecond), ready)
	}()
	c, err := s.connect(ctx)
	if err != nil {
		return Snapshot{}, nil, nil, err
	}
	watch, release := c.retainWatch(id)
	stage = "wait"
	lockStarted := time.Now()
	select {
	case watch.gate <- struct{}{}:
	case <-ctx.Done():
		lockWait = time.Since(lockStarted)
		release()
		return Snapshot{}, nil, nil, ctx.Err()
	}
	lockWait = time.Since(lockStarted)
	// Authorize after waiting, not with metadata captured before a slow resume.
	// A second watcher must not get the pre-resume notLoaded state.
	stage = "metadata"
	thread, err := s.read(ctx, c, id)
	if err != nil {
		<-watch.gate
		release()
		return Snapshot{}, nil, nil, err
	}
	// Subscribe only after the cancellable, per-thread join lock. Other threads
	// never wait for this thread's native resume or unsubscribe RPC.
	sub, unsubscribe := c.subscribe(id)
	reused = watch.refs > 0
	var response struct {
		Thread           Thread `json:"thread"`
		InitialTurnsPage *Page  `json:"initialTurnsPage"`
	}
	pageParams := map[string]any{"limit": limit, "itemsView": "full", "sortDirection": "desc"}
	var sequence uint64
	hydrateStarted := time.Now()
	if reused {
		// One native subscription per Godex connection, not per browser or queue
		// worker. A fresh page is still required as each viewer's sequence barrier.
		stage = "turns"
		response.Thread = thread
		response.InitialTurnsPage = &Page{}
		pageParams["threadId"] = id
		sequence, err = c.call(ctx, "thread/turns/list", pageParams, response.InitialTurnsPage)
	} else {
		stage = "resume"
		sequence, err = c.call(ctx, "thread/resume", map[string]any{"threadId": id, "excludeTurns": true, "initialTurnsPage": pageParams}, &response)
	}
	hydrateTime = time.Since(hydrateStarted)
	if err == nil && (response.Thread.ID != id || !s.allowed(response.Thread.Cwd)) {
		err = ErrForbidden
	}
	if err == nil && response.InitialTurnsPage == nil {
		err = fmt.Errorf("this Codex app-server does not support paginated live resume; update the native CLI")
	}
	if err != nil {
		unsubscribe()
		// Resume can attach successfully even if its response is unsupported or
		// arrives after cancellation. Do not leave an orphan native subscription,
		// or detach one still used by another viewer.
		if watch.refs == 0 && !errors.Is(err, ErrActiveWriter) {
			leaveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, _ = c.call(leaveCtx, "thread/unsubscribe", map[string]string{"threadId": id}, nil)
			cancel()
		}
		<-watch.gate
		release()
		return Snapshot{}, nil, nil, err
	}
	watch.refs++
	<-watch.gate
	var once sync.Once
	leave := func() {
		once.Do(func() {
			unsubscribe()
			watch.gate <- struct{}{}
			defer func() { <-watch.gate; release() }()
			watch.refs--
			if watch.refs == 0 {
				leaveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_, _ = c.call(leaveCtx, "thread/unsubscribe", map[string]string{"threadId": id}, nil)
			}
		})
	}
	ready = true
	return Snapshot{Thread: response.Thread, Turns: *response.InitialTurnsPage, Sequence: sequence, Approvals: c.pendingApprovals(id)}, sub.events, leave, nil
}

type Action struct {
	RequestID string `json:"requestId"`
	Mode      string `json:"mode"`
	Text      string `json:"text"`
	TurnID    string `json:"turnId,omitempty"`
}

// SubmissionError means a command may have reached the native runtime. Clients
// must reconcile using its clientUserMessageId instead of blindly retrying.
type SubmissionError struct{ Cause error }

func (e *SubmissionError) Error() string {
	return "submission outcome unknown; do not resend automatically: " + e.Cause.Error()
}
func (e *SubmissionError) Unwrap() error { return e.Cause }

func (s *Service) Submit(ctx context.Context, id string, action Action) (json.RawMessage, error) {
	if strings.TrimSpace(action.Text) == "" || len(action.Text) > 256*1024 || len(action.RequestID) < 8 || len(action.RequestID) > 128 || strings.IndexFunc(action.RequestID, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) >= 0 {
		return nil, fmt.Errorf("%w: text and a stable requestId (8–128 ASCII letters, digits, hyphens or underscores) are required", ErrInvalid)
	}
	if action.Mode != "queue" && action.Mode != "steer" {
		return nil, fmt.Errorf("%w: mode must be queue or steer", ErrInvalid)
	}
	if action.Mode == "steer" && action.TurnID == "" {
		return nil, ErrConflict
	}
	c, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	thread, err := s.read(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if thread.CanAcceptDirectInput != nil && !*thread.CanAcceptDirectInput {
		return nil, fmt.Errorf("this native thread does not accept direct input")
	}
	encoded, _ := json.Marshal(action)
	fingerprint := sha256.Sum256(encoded)
	key := id + "\x00" + action.RequestID
	s.mu.Lock()
	if previous := s.actions[key]; previous != nil {
		s.mu.Unlock()
		if previous.fingerprint != fingerprint {
			return nil, ErrConflict
		}
		select {
		case <-previous.done:
			return previous.result, previous.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// Completed receipts are bounded; an uncertain receipt is NOT retried within
	// this process. Native history/queue remains the source of truth on restart.
	for key, previous := range s.actions {
		if time.Since(previous.created) > 24*time.Hour {
			select {
			case <-previous.done:
				var uncertain *SubmissionError
				if !errors.As(previous.err, &uncertain) {
					delete(s.actions, key)
				}
			default:
			}
		}
	}
	if len(s.actions) >= 4096 {
		s.mu.Unlock()
		return nil, fmt.Errorf("submission receipt capacity reached; reconcile outstanding requests before retrying")
	}
	op := &operation{fingerprint: fingerprint, done: make(chan struct{}), created: time.Now()}
	s.actions[key] = op
	s.mu.Unlock()
	params := map[string]any{"threadId": id, "clientUserMessageId": action.RequestID, "input": []any{map[string]any{"type": "text", "text": action.Text}}}
	method := "thread/queue/add"
	if action.Mode == "steer" {
		method = "turn/steer"
		params["expectedTurnId"] = action.TurnID
	}
	_, err = c.call(ctx, method, params, &op.result)
	if err != nil {
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) {
			err = &SubmissionError{Cause: err}
		}
	}
	op.err = err
	close(op.done)
	if err == nil && action.Mode == "queue" {
		s.runQueue(id)
	}
	return op.result, op.err
}

// Only explicit Godex submissions start this small native-queue dispatcher.
// Codex atomically consumes and executes its own persisted queue; we never
// replay a prompt or run a second agent. It outlives browser disconnects.
func (s *Service) runQueue(id string) {
	s.mu.Lock()
	if worker := s.workers[id]; worker != nil {
		worker.revision++
		s.mu.Unlock()
		return
	}
	worker := &queueWorker{}
	s.workers[id] = worker
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			if s.workers[id] == worker {
				delete(s.workers, id)
			}
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-s.done:
				cancel()
			case <-ctx.Done():
			}
		}()
		for ctx.Err() == nil {
			s.mu.Lock()
			revision := worker.revision
			s.mu.Unlock()
			joinCtx, stopJoin := context.WithTimeout(ctx, 15*time.Second)
			// The dispatcher only needs live events, not twenty turns of history.
			_, events, leave, err := s.watch(joinCtx, id, 1)
			stopJoin()
			// A different runtime owns this thread. Keep the durable queue intact
			// for explicit recovery, without repeatedly trying to acquire its writer.
			if errors.Is(err, ErrActiveWriter) {
				return
			}
			if err == nil {
				finished := s.drainQueue(ctx, id, events)
				leave()
				if finished {
					s.mu.Lock()
					if worker.revision == revision {
						delete(s.workers, id)
						s.mu.Unlock()
						return
					}
					s.mu.Unlock()
					continue
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
}

func (s *Service) drainQueue(ctx context.Context, id string, events <-chan Event) bool {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		c, err := s.connect(probeCtx)
		var thread Thread
		var queue struct {
			Data []json.RawMessage `json:"data"`
		}
		if err == nil {
			thread, err = s.read(probeCtx, c, id)
		}
		if err == nil {
			_, err = c.call(probeCtx, "thread/queue/list", map[string]any{"threadId": id, "limit": 1}, &queue)
		}
		if err == nil && len(queue.Data) == 0 {
			cancel()
			return true
		}
		if err == nil {
			var status struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(thread.Status, &status)
			if status.Type == "idle" {
				_, err = c.call(probeCtx, "thread/queue/start", map[string]string{"threadId": id}, nil)
			}
		}
		cancel()
		if errors.Is(err, ErrForbidden) {
			return true
		}
		var rpc *RPCError
		if errors.As(err, &rpc) && rpc.Code == -32601 {
			return true
		}
		if err != nil {
			return false
		}
		// Drain noisy text/tool deltas without making per-token RPC requests.
		for {
			select {
			case <-ctx.Done():
				return true
			case <-ticker.C:
				goto refresh
			case event, ok := <-events:
				if !ok {
					return false
				}
				if event.Method == "turn/completed" || event.Method == "thread/queue/changed" || event.Method == "thread/status/changed" {
					goto refresh
				}
			}
		}
	refresh:
	}
	return true
}

// StartQueue asks the native server to atomically start its next queued input.
// It never converts a racing follow-up into same-turn steering (turn/start can).
func (s *Service) StartQueue(ctx context.Context, id string) (json.RawMessage, error) {
	c, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = s.read(ctx, c, id); err != nil {
		return nil, err
	}
	var response json.RawMessage
	_, err = c.call(ctx, "thread/queue/start", map[string]string{"threadId": id}, &response)
	return response, err
}

func (s *Service) Queue(ctx context.Context, id string) (json.RawMessage, error) {
	c, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = s.read(ctx, c, id); err != nil {
		return nil, err
	}
	var response json.RawMessage
	_, err = c.call(ctx, "thread/queue/list", map[string]any{"threadId": id, "limit": 100}, &response)
	return response, err
}

func (s *Service) Interrupt(ctx context.Context, id, turn string) error {
	if turn == "" {
		return ErrConflict
	}
	c, err := s.connect(ctx)
	if err != nil {
		return err
	}
	if _, err = s.read(ctx, c, id); err != nil {
		return err
	}
	_, err = c.call(ctx, "turn/interrupt", map[string]string{"threadId": id, "turnId": turn}, nil)
	return err
}

func (s *Service) Approve(ctx context.Context, id, key, decision string) error {
	if decision != "accept" && decision != "decline" {
		return fmt.Errorf("%w: only one-time accept/decline decisions are supported", ErrInvalid)
	}
	c, err := s.connect(ctx)
	if err != nil {
		return err
	}
	if _, err = s.read(ctx, c, id); err != nil {
		return err
	}
	c.mu.Lock()
	request, ok := c.approvals[key]
	if !ok || eventThread(request.Params) != id {
		c.mu.Unlock()
		return ErrConflict
	}
	var limits struct {
		AvailableDecisions []json.RawMessage `json:"availableDecisions"`
	}
	_ = json.Unmarshal(request.Params, &limits)
	if len(limits.AvailableDecisions) > 0 {
		allowed := false
		for _, raw := range limits.AvailableDecisions {
			var value string
			_ = json.Unmarshal(raw, &value)
			if value == decision {
				allowed = true
			}
		}
		if !allowed {
			c.mu.Unlock()
			return fmt.Errorf("this native approval does not allow %s; handle it in a compatible native client", decision)
		}
	}
	delete(c.approvals, key)
	c.mu.Unlock()
	if err := c.write(ctx, map[string]any{"id": request.rawID, "result": map[string]string{"decision": decision}}); err != nil {
		return err
	}
	params, _ := json.Marshal(map[string]any{"requestId": request.rawID, "threadId": id})
	c.publish(Event{Method: "serverRequest/resolved", Params: params}, id)
	return nil
}
