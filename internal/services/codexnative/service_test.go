package codexnative

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeCodex struct {
	mu            sync.Mutex
	writeMu       sync.Mutex
	root          string
	outside       string
	connections   []net.Conn
	calls         []frame
	queue         []map[string]any
	active        bool
	started       int
	dropSubmit    bool
	resumeOutside bool
	missingPage   bool
	resumeError   *RPCError
	turnsError    *RPCError
	beforeReply   func(frame)
	loadedPages   map[string]loadedPage
	loadedError   *RPCError
	noDirectInput bool
	notLoaded     bool
	model         string
	effort        string
	modelPages    map[string]modelPage
	modelError    *RPCError
	settingsError *RPCError
}

func newFakeCodex(t *testing.T) (*fakeCodex, *Service) {
	t.Helper()
	f := &fakeCodex{root: t.TempDir(), outside: t.TempDir(), model: "model-a", effort: "high"}
	s := New(Options{WorkspaceRoots: []string{f.root}, Dial: func(context.Context) (io.ReadWriteCloser, error) {
		local, remote := net.Pipe()
		f.mu.Lock()
		f.connections = append(f.connections, remote)
		f.mu.Unlock()
		go f.serve(remote)
		return local, nil
	}})
	t.Cleanup(func() {
		s.Close()
		f.mu.Lock()
		for _, conn := range f.connections {
			_ = conn.Close()
		}
		f.mu.Unlock()
	})
	return f, s
}

func (f *fakeCodex) thread(id string) map[string]any {
	root := f.root
	if id == "outside" {
		root = f.outside
	}
	status := "idle"
	if f.active {
		status = "active"
	}
	if f.notLoaded {
		status = "notLoaded"
	}
	return map[string]any{"id": id, "cwd": root, "name": "Native conversation", "preview": "hello", "status": map[string]any{"type": status}, "turns": []any{}, "canAcceptDirectInput": !f.noDirectInput, "model": f.model, "reasoningEffort": f.effort, "modelProvider": "native-provider"}
}

func (f *fakeCodex) send(conn net.Conn, value any) {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	_ = json.NewEncoder(conn).Encode(value)
}
func (f *fakeCodex) event(method string, params any) {
	f.mu.Lock()
	connections := append([]net.Conn(nil), f.connections...)
	f.mu.Unlock()
	for _, conn := range connections {
		f.send(conn, map[string]any{"method": method, "params": params})
	}
}

func (f *fakeCodex) serve(conn net.Conn) {
	defer conn.Close()
	decoder := json.NewDecoder(conn)
	for {
		var request frame
		if decoder.Decode(&request) != nil {
			return
		}
		var p map[string]any
		_ = json.Unmarshal(request.Params, &p)
		f.mu.Lock()
		f.calls = append(f.calls, request)
		var result any = map[string]any{}
		var rpcErr *RPCError
		drop := false
		switch request.Method {
		case "initialize":
			result = map[string]string{"userAgent": "codex-test/0.169"}
		case "thread/loaded/list":
			rpcErr = f.loadedError
			if f.loadedPages != nil {
				cursor, _ := p["cursor"].(string)
				result = f.loadedPages[cursor]
			} else {
				result = map[string]any{"data": []string{"thread"}}
			}
		case "thread/list":
			result = map[string]any{"data": []any{f.thread("thread"), f.thread("outside")}, "nextCursor": "next-page"}
		case "thread/read":
			result = map[string]any{"thread": f.thread(p["threadId"].(string))}
		case "model/list":
			rpcErr = f.modelError
			result = modelPage{Data: testModelCatalog()}
			if f.modelPages != nil {
				cursor, _ := p["cursor"].(string)
				result = f.modelPages[cursor]
			}
		case "thread/settings/update":
			rpcErr = f.settingsError
			if rpcErr == nil {
				f.model = p["model"].(string)
				if effort, ok := p["effort"].(string); ok {
					f.effort = effort
				}
			}
		case "thread/resume":
			rpcErr = f.resumeError
			result = map[string]any{"thread": f.thread(p["threadId"].(string)), "initialTurnsPage": map[string]any{"data": []any{}, "nextCursor": "older"}}
			if f.resumeOutside {
				result.(map[string]any)["thread"].(map[string]any)["cwd"] = f.outside
			}
			if f.missingPage {
				delete(result.(map[string]any), "initialTurnsPage")
			}
		case "thread/turns/list":
			rpcErr = f.turnsError
			result = map[string]any{"data": []any{map[string]any{"id": "old", "status": "completed", "items": []any{map[string]any{"type": "userMessage", "id": "u", "content": []any{map[string]string{"type": "text", "text": "original"}}}}}}, "nextCursor": nil}
		case "thread/queue/add":
			queued := map[string]any{"id": p["clientUserMessageId"], "clientUserMessageId": p["clientUserMessageId"], "input": p["input"]}
			f.queue = append(f.queue, queued)
			result = map[string]any{"queuedSubmission": queued}
			drop = f.dropSubmit
		case "thread/queue/list":
			result = map[string]any{"data": append([]map[string]any{}, f.queue...), "nextCursor": nil}
		case "thread/queue/start":
			if f.active || len(f.queue) == 0 {
				rpcErr = &RPCError{Code: -32000, Message: "no idle queued input"}
			} else {
				f.queue = f.queue[1:]
				f.active = true
				f.started++
				result = map[string]any{"turn": map[string]any{"id": "turn", "status": "inProgress", "items": []any{}}}
			}
		case "turn/steer":
			if p["expectedTurnId"] != "turn" {
				rpcErr = &RPCError{Code: -32000, Message: "active turn changed"}
			}
		case "turn/interrupt":
			if p["turnId"] != "turn" {
				rpcErr = &RPCError{Code: -32000, Message: "turn changed"}
			} else {
				f.active = false
			}
		}
		beforeReply := f.beforeReply
		f.mu.Unlock()
		if drop {
			return
		}
		if request.Method != "" && len(request.ID) > 0 {
			response := frame{ID: request.ID, Error: rpcErr}
			if rpcErr == nil {
				response.Result, _ = json.Marshal(result)
			}
			if beforeReply != nil {
				// A slow RPC must not block independent requests on the transport.
				go func(request frame, response frame) { beforeReply(request); f.send(conn, response) }(request, response)
			} else {
				f.send(conn, response)
			}
		}
	}
}

func (f *fakeCodex) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call.Method == method {
			n++
		}
	}
	return n
}
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestReadinessUsesProtocolAndNeverStartsThread(t *testing.T) {
	f, s := newFakeCodex(t)
	if status := s.Status(testContext(t)); !status.Ready {
		t.Fatal(status.Reason)
	}
	if f.count("initialize") != 1 || f.count("thread/resume") != 0 || f.count("turn/start") != 0 {
		t.Fatal("readiness changed a thread")
	}
	if status := s.Status(testContext(t)); !status.Ready || f.count("initialize") != 1 {
		t.Fatal("connection was not reused")
	}
}

func TestMissingSocketDoesNotLaunchDaemon(t *testing.T) {
	s := New(Options{Socket: filepath.Join(t.TempDir(), "missing.sock"), Command: "must-not-be-launched", WorkspaceRoots: []string{t.TempDir()}})
	defer s.Close()
	if status := s.Status(testContext(t)); status.Ready || status.Reason == "" {
		t.Fatal(status)
	}
}

func TestFailedResumeDetachesOnlyUnusedSubscription(t *testing.T) {
	for _, outside := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported-page", true: "scope-changed"}[outside], func(t *testing.T) {
			f, s := newFakeCodex(t)
			ctx := testContext(t)
			f.mu.Lock()
			f.resumeOutside = outside
			f.missingPage = !outside
			f.mu.Unlock()
			if _, _, _, err := s.Watch(ctx, "thread"); err == nil {
				t.Fatal("invalid resume accepted")
			}
			if f.count("thread/unsubscribe") != 1 {
				t.Fatal("failed resume left native subscription attached")
			}
			f.mu.Lock()
			f.resumeOutside, f.missingPage = false, false
			f.mu.Unlock()
			_, _, leave, err := s.Watch(ctx, "thread")
			if err != nil {
				t.Fatal(err)
			}
			defer leave()
			f.mu.Lock()
			f.turnsError = &RPCError{Code: -32000, Message: "snapshot unavailable"}
			f.mu.Unlock()
			if _, _, _, err = s.Watch(ctx, "thread"); err == nil {
				t.Fatal("failed shared snapshot accepted")
			}
			if f.count("thread/unsubscribe") != 1 {
				t.Fatal("failed join detached another viewer")
			}
		})
	}
}

func TestRequestIDsRejectControlCharacters(t *testing.T) {
	f, s := newFakeCodex(t)
	for _, id := range []string{"request\nforged", "request\rforged", "request:unknown", "request-中文"} {
		if _, err := s.Submit(testContext(t), "thread", Action{RequestID: id, Mode: "queue", Text: "inspect"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid id accepted: %q: %v", id, err)
		}
	}
	if f.count("initialize") != 0 {
		t.Fatal("invalid input reached native runtime")
	}
}

func TestUncertainReceiptsNeverExpireWithinProcess(t *testing.T) {
	_, s := newFakeCodex(t)
	action := Action{RequestID: "old-uncertain", Mode: "queue", Text: "inspect"}
	encoded, _ := json.Marshal(action)
	previous := &operation{fingerprint: sha256.Sum256(encoded), done: make(chan struct{}), err: &SubmissionError{Cause: io.EOF}, created: time.Now().Add(-48 * time.Hour)}
	close(previous.done)
	s.actions["thread\x00"+action.RequestID] = previous
	if _, err := s.Submit(testContext(t), "thread", Action{RequestID: "fresh-request", Mode: "steer", TurnID: "turn", Text: "inspect"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(testContext(t), "thread", action); !errors.As(err, new(*SubmissionError)) {
		t.Fatalf("uncertain receipt expired: %v", err)
	}
}

func TestHistoryIsReadOnlyAndScopeAppliesToEveryOperation(t *testing.T) {
	f, s := newFakeCodex(t)
	ctx := testContext(t)
	page, err := s.List(ctx, "", "")
	if err != nil || len(page.Data) != 1 || page.NextCursor == nil {
		t.Fatalf("%+v %v", page, err)
	}
	if _, err = s.History(ctx, "thread", ""); err != nil {
		t.Fatal(err)
	}
	if f.count("thread/resume") != 0 {
		t.Fatal("history viewing resumed a thread")
	}
	for _, check := range []func() error{
		func() error { _, err := s.History(ctx, "outside", ""); return err },
		func() error {
			_, err := s.Submit(ctx, "outside", Action{RequestID: "request-1", Text: "do not send", Mode: "queue"})
			return err
		},
		func() error { return s.Interrupt(ctx, "outside", "turn") },
		func() error { _, _, _, err := s.Watch(ctx, "outside"); return err },
	} {
		if err := check(); !errors.Is(err, ErrForbidden) {
			t.Fatalf("scope bypass: %v", err)
		}
	}
	if f.count("thread/queue/add") != 0 || f.count("turn/interrupt") != 0 {
		t.Fatal("out-of-scope mutation reached Codex")
	}
	link := filepath.Join(f.root, "escape")
	if err := os.Symlink(f.outside, link); err != nil {
		t.Fatal(err)
	}
	if s.allowed(link) {
		t.Fatal("symlink escaped workspace scope")
	}
}

func TestWatchReferenceCountsAndDoesNotOverrideConfiguration(t *testing.T) {
	f, s := newFakeCodex(t)
	ctx := testContext(t)
	_, first, leaveFirst, err := s.Watch(ctx, "thread")
	if err != nil {
		t.Fatal(err)
	}
	_, second, leaveSecond, err := s.Watch(ctx, "thread")
	if err != nil {
		t.Fatal(err)
	}
	if f.count("thread/resume") != 1 || f.count("thread/turns/list") != 1 {
		t.Fatal("an additional viewer must reuse the native subscription")
	}
	f.event("item/agentMessage/delta", map[string]string{"threadId": "thread", "turnId": "turn", "itemId": "item", "delta": "hello"})
	for _, events := range []<-chan Event{first, second} {
		select {
		case event := <-events:
			if event.Method != "item/agentMessage/delta" {
				t.Fatal(event)
			}
		case <-ctx.Done():
			t.Fatal("missing live event")
		}
	}
	leaveFirst()
	if f.count("thread/unsubscribe") != 0 {
		t.Fatal("one viewer unsubscribed every viewer")
	}
	leaveSecond()
	if f.count("thread/unsubscribe") != 1 {
		t.Fatal("last viewer did not detach")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Method == "thread/resume" {
			var params map[string]any
			_ = json.Unmarshal(call.Params, &params)
			if len(params) != 3 || params["threadId"] != "thread" {
				t.Fatalf("unexpected resume overrides: %s", call.Params)
			}
			if params["initialTurnsPage"].(map[string]any)["limit"] != float64(5) {
				t.Fatalf("join hydrated too much history: %s", call.Params)
			}
		}
	}
}

func TestWatchSlowThreadDoesNotBlockAnotherThreadOrCanceledWaiter(t *testing.T) {
	f, s := newFakeCodex(t)
	gate := make(chan struct{})
	var once sync.Once
	openGate := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(openGate)
	f.beforeReply = func(request frame) {
		var p struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(request.Params, &p)
		if request.Method == "thread/resume" && p.ThreadID == "slow" {
			<-gate
		}
	}
	type result struct {
		leave func()
		err   error
	}
	first := make(chan result, 1)
	firstCtx := testContext(t)
	go func() { _, _, leave, err := s.Watch(firstCtx, "slow"); first <- result{leave, err} }()
	eventually(t, func() bool { return f.count("thread/resume") == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, _, err := s.Watch(ctx, "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter ignored its deadline: %v", err)
	}
	if f.count("thread/resume") != 1 {
		t.Fatal("canceled waiter performed a second resume")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	_, _, leaveOther, err := s.Watch(ctx2, "other")
	if err != nil {
		t.Fatalf("unrelated thread was blocked by slow resume: %v", err)
	}
	leaveOther()
	openGate()
	r := <-first
	if r.err != nil {
		t.Fatal(r.err)
	}
	r.leave()
	r.leave() // releasing a viewer is idempotent
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if len(s.client.watches) != 0 || len(s.client.subs) != 0 {
		t.Fatal("join/wait/leave leaked keyed locks or local subscriptions")
	}
}

func TestWatchReusedSubscriptionKeepsSequenceBarrier(t *testing.T) {
	f, s := newFakeCodex(t)
	ctx := testContext(t)
	_, _, leaveFirst, err := s.Watch(ctx, "thread")
	if err != nil {
		t.Fatal(err)
	}
	defer leaveFirst()
	f.mu.Lock()
	f.beforeReply = func(request frame) {
		if request.Method == "thread/turns/list" {
			f.event("item/agentMessage/delta", map[string]string{"threadId": "thread", "turnId": "turn", "itemId": "item", "delta": "before"})
		}
	}
	f.mu.Unlock()
	snapshot, events, leave, err := s.Watch(ctx, "thread")
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	select {
	case event := <-events:
		if event.Sequence > snapshot.Sequence {
			t.Fatal("pre-snapshot event escaped hydration barrier")
		}
	case <-ctx.Done():
		t.Fatal("missing pre-snapshot event")
	}
	f.event("item/agentMessage/delta", map[string]string{"threadId": "thread", "turnId": "turn", "itemId": "item", "delta": "after"})
	select {
	case event := <-events:
		if event.Sequence <= snapshot.Sequence {
			t.Fatal("post-snapshot event would be dropped")
		}
	case <-ctx.Done():
		t.Fatal("missing post-snapshot event")
	}
	if f.count("thread/resume") != 1 {
		t.Fatal("reused watch resumed thread")
	}
}

func TestHistoryStartsSmallAndBackscrollKeepsNativeCursor(t *testing.T) {
	f, s := newFakeCodex(t)
	for _, cursor := range []string{"", "older"} {
		if _, err := s.History(testContext(t), "thread", cursor); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Method != "thread/turns/list" {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal(call.Params, &p)
		want := float64(5)
		if p["cursor"] == "older" {
			want = 20
		}
		if p["limit"] != want || p["itemsView"] != "full" {
			t.Fatalf("unexpected page: %s", call.Params)
		}
	}
}

func TestWatchActiveWriterKeepsHistoryWithoutTakingOver(t *testing.T) {
	f, s := newFakeCodex(t)
	f.resumeError = &RPCError{Code: -32600, Message: "thread thread already has an active writer"}
	ctx := testContext(t)
	_, events, leave, err := s.Watch(ctx, "thread")
	if !errors.Is(err, ErrActiveWriter) || events != nil || leave != nil {
		t.Fatalf("expected an explicit writer conflict, got %v", err)
	}
	if _, err := s.History(ctx, "thread", ""); err != nil {
		t.Fatalf("read-only history should remain available: %v", err)
	}
	s.client.mu.Lock()
	subs := len(s.client.subs)
	s.client.mu.Unlock()
	if subs != 0 || f.count("thread/resume") != 1 || f.count("thread/unsubscribe") != 0 {
		t.Fatal("writer conflict leaked a subscription, retried, or detached a native thread")
	}
	for _, method := range []string{"thread/fork", "turn/start", "turn/interrupt", "thread/queue/add"} {
		if f.count(method) != 0 {
			t.Fatalf("writer conflict triggered unsafe fallback: %s", method)
		}
	}
	// Once the original runtime has released ownership, an explicit retry can
	// attach normally. A conflict must not poison the shared connection.
	f.mu.Lock()
	f.resumeError = nil
	f.mu.Unlock()
	_, _, leave, err = s.Watch(ctx, "thread")
	if err != nil {
		t.Fatal(err)
	}
	leave()
}

func TestAccessIsReadOnlyAndChecksEveryLoadedPage(t *testing.T) {
	f, s := newFakeCodex(t)
	next := "next-page"
	f.loadedPages = map[string]loadedPage{"": {Data: []string{"unrelated"}, NextCursor: &next}, next: {Data: []string{"thread"}}}
	ctx := testContext(t)
	access, err := s.Access(ctx, "thread")
	if err != nil || !access.LoadedInRuntime || access.Thread.ID != "thread" || f.count("thread/loaded/list") != 2 {
		t.Fatalf("%+v %v", access, err)
	}
	access, err = s.Access(ctx, "saved-idle-thread")
	if err != nil || access.LoadedInRuntime {
		t.Fatalf("an unloaded thread is not evidence of another writer: %+v %v", access, err)
	}
	before := f.count("thread/loaded/list")
	if _, err := s.Access(ctx, "outside"); !errors.Is(err, ErrForbidden) || f.count("thread/loaded/list") != before {
		t.Fatalf("out-of-scope access reached runtime enumeration: %v", err)
	}
	for _, method := range []string{"thread/resume", "thread/turns/list", "thread/fork", "turn/start", "turn/interrupt", "thread/queue/add"} {
		if f.count(method) != 0 {
			t.Fatalf("read-only preflight invoked %s", method)
		}
	}
}

func TestAccessDoesNotTurnProbeFailureIntoUnloaded(t *testing.T) {
	f, s := newFakeCodex(t)
	f.loadedError = &RPCError{Code: -32601, Message: "unsupported"}
	if _, err := s.Access(testContext(t), "thread"); err == nil {
		t.Fatal("failed probe was reported as a known membership state")
	}
}

func TestAccessRejectsRepeatingNativeCursor(t *testing.T) {
	f, s := newFakeCodex(t)
	next := "loop"
	f.loadedPages = map[string]loadedPage{"": {NextCursor: &next}, next: {NextCursor: &next}}
	if _, err := s.Access(testContext(t), "thread"); err == nil || f.count("thread/loaded/list") != 2 {
		t.Fatalf("pagination did not stop: %v", err)
	}
}

func TestListLoadedUsesNativePaginationAndWorkspaceScope(t *testing.T) {
	f, s := newFakeCodex(t)
	next := "next-page"
	f.loadedPages = map[string]loadedPage{"": {Data: []string{"outside"}, NextCursor: &next}, next: {Data: []string{"thread"}}}
	ctx := testContext(t)
	page, err := s.ListLoaded(ctx, "", "")
	if err != nil || len(page.Data) != 0 || page.NextCursor == nil || *page.NextCursor != next {
		t.Fatalf("%+v %v", page, err)
	}
	page, err = s.ListLoaded(ctx, next, "NATIVE")
	if err != nil || len(page.Data) != 1 || page.Data[0].ID != "thread" {
		t.Fatalf("%+v %v", page, err)
	}
	page, err = s.ListLoaded(ctx, next, "not-matching")
	if err != nil || len(page.Data) != 0 {
		t.Fatalf("%+v %v", page, err)
	}
	for _, method := range []string{"thread/list", "thread/resume", "thread/turns/list", "turn/start"} {
		if f.count(method) != 0 {
			t.Fatalf("loaded view scanned history or changed a session: %s", method)
		}
	}
}

func TestQueueWorkerStopsOnActiveWriterConflict(t *testing.T) {
	f, s := newFakeCodex(t)
	f.resumeError = &RPCError{Code: -32600, Message: "thread thread already has an active writer"}
	s.runQueue("thread")
	eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.workers) == 0
	})
	if f.count("thread/resume") != 1 || f.count("thread/queue/start") != 0 {
		t.Fatal("queue worker retried or started work in a different runtime")
	}
}

func TestActiveWriterClassificationDoesNotMatchUnrelatedErrors(t *testing.T) {
	for _, message := range []string{"active turn changed", "thread thread not found", "already has an active writer", "thread thread already has an active writer: invalid params"} {
		if errors.Is(&RPCError{Code: -32600, Message: message}, ErrActiveWriter) {
			t.Fatalf("misclassified error: %s", message)
		}
	}
}

func TestSubmissionDedupAndNativeQueueDispatch(t *testing.T) {
	f, s := newFakeCodex(t)
	ctx := testContext(t)
	action := Action{RequestID: "stable-message-1", Text: "inspect files", Mode: "queue"}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Submit(ctx, "thread", action); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	eventually(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.started == 1 })
	if f.count("thread/queue/add") != 1 || f.count("turn/start") != 0 {
		t.Fatal("duplicate or unsafe turn/start")
	}
	action.Text = "different"
	if _, err := s.Submit(ctx, "thread", action); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestQueueContinuesAfterBrowserDisconnect(t *testing.T) {
	f, s := newFakeCodex(t)
	ctx := testContext(t)
	f.mu.Lock()
	f.active = true
	f.mu.Unlock()
	_, _, leave, err := s.Watch(ctx, "thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, "thread", Action{RequestID: "next-request", Text: "next", Mode: "queue"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.count("thread/queue/list") > 0 })
	leave()
	f.mu.Lock()
	f.active = false
	f.mu.Unlock()
	f.event("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "completed"}})
	eventually(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.started == 1 })
}

func TestUnknownSubmissionIsNeverReplayed(t *testing.T) {
	f, s := newFakeCodex(t)
	ctx := testContext(t)
	f.mu.Lock()
	f.dropSubmit = true
	f.mu.Unlock()
	action := Action{RequestID: "unknown-request", Text: "one execution", Mode: "queue"}
	_, err := s.Submit(ctx, "thread", action)
	var uncertain *SubmissionError
	if !errors.As(err, &uncertain) {
		t.Fatalf("expected uncertain outcome, got %v", err)
	}
	f.mu.Lock()
	f.dropSubmit = false
	f.mu.Unlock()
	_, err = s.Submit(ctx, "thread", action)
	if !errors.As(err, &uncertain) {
		t.Fatal(err)
	}
	if f.count("thread/queue/add") != 1 {
		t.Fatal("uncertain message was replayed")
	}
}

func TestSteerAndInterruptCarryNativeTurnFence(t *testing.T) {
	f, s := newFakeCodex(t)
	ctx := testContext(t)
	if _, err := s.Submit(ctx, "thread", Action{RequestID: "steer-request", Text: "focus", Mode: "steer", TurnID: "old"}); err == nil {
		t.Fatal("stale steering accepted")
	}
	if err := s.Interrupt(ctx, "thread", "turn"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		var params map[string]any
		_ = json.Unmarshal(call.Params, &params)
		if call.Method == "turn/steer" && params["expectedTurnId"] != "old" {
			t.Fatal(params)
		}
		if call.Method == "turn/interrupt" && params["turnId"] != "turn" {
			t.Fatal(params)
		}
	}
}

func TestApprovalsAreThreadScopedOneTimeAndResolvedAcrossViewers(t *testing.T) {
	f, s := newFakeCodex(t)
	ctx := testContext(t)
	_, events, leave, err := s.Watch(ctx, "thread")
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	f.mu.Lock()
	conn := f.connections[0]
	f.mu.Unlock()
	f.send(conn, map[string]any{"id": 7, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread", "turnId": "turn", "command": "go test", "availableDecisions": []string{"accept", "decline"}}})
	var request approval
	select {
	case event := <-events:
		_ = json.Unmarshal(event.Params, &request)
	case <-ctx.Done():
		t.Fatal("missing approval")
	}
	if err := s.Approve(ctx, "thread", request.ID, "acceptForSession"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, "thread", request.ID, "accept"); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, "thread", request.ID, "accept"); !errors.Is(err, ErrConflict) {
		t.Fatal("approval was reusable", err)
	}
	select {
	case event := <-events:
		if event.Method != "serverRequest/resolved" {
			t.Fatal(event)
		}
	case <-ctx.Done():
		t.Fatal("resolution not broadcast")
	}
}

func TestSlowSubscriberCannotBlockNativeReader(t *testing.T) {
	local, remote := net.Pipe()
	c := newClient(newLineTransport(local))
	defer c.close(io.EOF)
	defer remote.Close()
	sub, _ := c.subscribe("thread")
	for range 513 {
		c.publish(Event{Method: "delta"}, "thread")
	}
	count := 0
	for range sub.events {
		count++
	}
	if count != 512 {
		t.Fatal(count)
	}
	if c.err != nil {
		t.Fatal("slow subscriber closed native connection")
	}
}
