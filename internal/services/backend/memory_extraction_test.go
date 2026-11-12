package backend

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/domain/message"
)

func waitExtraction(t *testing.T, s *Service, id, status string) []map[string]any {
	t.Helper()
	store, _ := s.WorkContexts()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := store.ExtractionHistory(context.Background(), desktopTestScope(), id)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 && rows[0]["status"] == status {
			return rows
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("extraction did not reach " + status)
	return nil
}
func TestSinglePassMemoryIsBackgroundAndUnchangedInputSkipsModel(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Memory.ExtractionMode = "single-pass"
	reply := protocol.Response{Content: []protocol.Block{protocol.TextBlock("此节点可以使用 Vision，但尚未核实。")}}
	s := newTestService(cfg, &stubCaller{responses: []protocol.Response{reply, reply}})
	c := createDesktopTest(t, s, "capture")
	session := linkDesktopTest(t, s, c.ID, "capture")
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	s.memoryExtract = func(ctx context.Context, prompt, input string) (string, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
		}
		if !strings.Contains(prompt, "ADD-only") {
			t.Error("missing extraction contract")
		}
		return `{"observations":[{"title":"OCR 节点观察","content":"当前节点可能支持 Vision，尚未客观核实。","kind":"method","source_ids":["user"]}]}`, nil
	}
	result, err := s.Submit(desktopOwnerContext(), session.id, message.NewTextEnvelope(message.SourceWeb, session.id, "user", "记录当前节点可能支持 Vision 的工作观察", time.Now()))
	if err != nil || result.Status != "completed" {
		close(release)
		t.Fatal(result, err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("extraction was not scheduled")
	}
	// Submit already returned while the extraction model is still blocked.
	close(release)
	rows := waitExtraction(t, s, c.ID, "completed")
	raw, _ := json.Marshal(rows[0])
	if !strings.Contains(string(raw), `"added":1`) {
		t.Fatal(string(raw))
	}
	store, _ := s.WorkContexts()
	facts, _ := store.Facts(context.Background(), desktopTestScope(), c.ID)
	if len(facts) != 1 || facts[0].Status != "observed" || facts[0].VerifiedAt != "" {
		t.Fatal("background capture promoted a claim", facts)
	}
	s.captureSinglePassMemory(session, result.TurnID, "completed", 0)
	if calls.Load() != 1 {
		t.Fatal("unchanged delta caused another model request", calls.Load())
	}
}
func TestSinglePassMemoryRejectsMutationAndPersistsFailure(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Memory.ExtractionMode = "single-pass"
	s := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("received")}}}})
	c := createDesktopTest(t, s, "reject")
	session := linkDesktopTest(t, s, c.ID, "reject")
	var calls atomic.Int32
	s.memoryExtract = func(context.Context, string, string) (string, error) {
		calls.Add(1)
		return `{"observations":[{"title":"x","content":"y","kind":"fact","source_ids":["user"],"status":"confirmed"}]}`, nil
	}
	if _, err := s.Submit(desktopOwnerContext(), session.id, message.NewTextEnvelope(message.SourceWeb, session.id, "user", "新增一个尚未核实的工作观察", time.Now())); err != nil {
		t.Fatal(err)
	}
	rows := waitExtraction(t, s, c.ID, "failed")
	if !strings.Contains(rows[0]["error"].(string), "unknown field") {
		t.Fatal(rows)
	}
	store, _ := s.WorkContexts()
	facts, _ := store.Facts(context.Background(), desktopTestScope(), c.ID)
	if len(facts) != 0 || calls.Load() != 1 {
		t.Fatal("invalid result wrote facts or retried immediately", facts, calls.Load())
	}
	s.sweepMemoryExtractions(context.Background(), store)
	if calls.Load() != 1 {
		t.Fatal("backoff ignored")
	}
}
func TestSinglePassMemoryDefaultIsZeroModelAndCancellationDiscardsOutput(t *testing.T) {
	cfg := newTestConfig(t)
	s := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("received")}}}})
	c := createDesktopTest(t, s, "default")
	session := linkDesktopTest(t, s, c.ID, "default")
	var calls atomic.Int32
	s.memoryExtract = func(context.Context, string, string) (string, error) { calls.Add(1); return `{"observations":[]}`, nil }
	if _, err := s.Submit(desktopOwnerContext(), session.id, message.NewTextEnvelope(message.SourceWeb, session.id, "user", "普通工作进度查询", time.Now())); err != nil {
		t.Fatal(err)
	}
	store, _ := s.WorkContexts()
	rows, _ := store.ExtractionHistory(context.Background(), desktopTestScope(), c.ID)
	if len(rows) != 0 || calls.Load() != 0 {
		t.Fatal("default mode added an LLM call", rows, calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.extractMemoryOnce(ctx, workcontext.ExtractionJob{}); !errors.Is(err, context.Canceled) {
		t.Fatal("late extraction applied after cancellation", err)
	}
}
func TestSinglePassMemoryRecoversPersistedQueue(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Memory.ExtractionMode = "single-pass"
	s := newTestService(cfg, &stubCaller{})
	c := createDesktopTest(t, s, "restore")
	store, _ := s.WorkContexts()
	job := workcontext.ExtractionJob{ContextID: c.ID, SessionID: "session", TurnID: "turn", Sources: []workcontext.ExtractionSource{extractionSource("user", "user_statement", "scoped observation", "", false)}}
	if queued, err := store.EnqueueMemoryExtraction(context.Background(), desktopTestScope(), job); err != nil || !queued {
		t.Fatal(queued, err)
	}
	s.memoryExtract = func(context.Context, string, string) (string, error) { return `{"observations":[]}`, nil }
	// This is the same persistent queue scan used by the startup/maintenance loop.
	s.sweepMemoryExtractions(context.Background(), store)
	waitExtraction(t, s, c.ID, "completed")
}
