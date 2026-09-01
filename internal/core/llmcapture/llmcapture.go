package llmcapture

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/platform/idgen"
	"github.com/tim5wang/godex/internal/platform/logger"
)

// Record is one captured LLM API exchange (request + response).
// It is serialized as one JSON line into the jsonl dump file and kept in an
// in-memory ring buffer so the web UI can browse recent records without
// re-reading the file on every request.
type Record struct {
	ID        string             `json:"id"`
	Timestamp time.Time          `json:"timestamp"`
	SessionID string             `json:"session_id,omitempty"`
	TurnID    string             `json:"turn_id,omitempty"`
	JobID     string             `json:"job_id,omitempty"`
	Channel   string             `json:"channel,omitempty"`
	Model     string             `json:"model,omitempty"`
	Stream    bool               `json:"stream"`
	LatencyMS int64              `json:"latency_ms"`
	Error     string             `json:"error,omitempty"`
	Request   protocol.Request   `json:"request"`
	Response  *protocol.Response `json:"response,omitempty"`
}

// Options controls where the capture dump lives and how many records are kept
// in memory for the web UI.
type Options struct {
	// DumpDir is the directory that receives llm_capture.jsonl.
	DumpDir string
	// MaxMemRecords bounds the in-memory ring buffer. Older records stay on
	// disk but are no longer listed by the UI.
	MaxMemRecords int
}

// Capture owns the enabled flag, the jsonl sink and the in-memory record ring.
// It subscribes to the conversation package usage hooks once at construction.
type Capture struct {
	enabled    atomic.Bool
	dumpPath   string
	writeMu    sync.Mutex
	file       *os.File
	mu         sync.RWMutex
	records    []*Record
	maxMem     int
	unsubscribe func()
}

// New creates a Capture and subscribes it to LLM usage events. Subscribing
// is cheap and always on; the capture filter is the enabled flag, so toggling
// never touches the hook registration.
func New(opts Options) *Capture {
	if opts.MaxMemRecords <= 0 {
		opts.MaxMemRecords = 500
	}
	c := &Capture{
		dumpPath: filepath.Join(opts.DumpDir, "llm_capture.jsonl"),
		maxMem:   opts.MaxMemRecords,
	}
	c.unsubscribe = conversation.AddUsageHook(c.onUsage)
	return c
}

// Close unsubscribes the hook and closes the jsonl file.
func (c *Capture) Close() {
	if c.unsubscribe != nil {
		c.unsubscribe()
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.file != nil {
		_ = c.file.Close()
		c.file = nil
	}
}

// SetEnabled toggles capture. When enabling, the dump file is (re)opened in
// append mode so records persist across toggles.
func (c *Capture) SetEnabled(enabled bool) error {
	if enabled == c.enabled.Load() {
		return nil
	}
	c.enabled.Store(enabled)
	if !enabled {
		return nil
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.file != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.dumpPath), 0o755); err != nil {
		c.enabled.Store(false)
		return fmt.Errorf("llm capture: create dump dir: %w", err)
	}
	f, err := os.OpenFile(c.dumpPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		c.enabled.Store(false)
		return fmt.Errorf("llm capture: open dump file: %w", err)
	}
	c.file = f
	return nil
}

// Enabled reports the current capture state.
func (c *Capture) Enabled() bool { return c.enabled.Load() }

// DumpPath returns the jsonl file path (empty until first enable).
func (c *Capture) DumpPath() string { return c.dumpPath }

// onUsage is invoked by the conversation package after every LLM call that
// carries a usage context. It only captures when the flag is set.
func (c *Capture) onUsage(_ context.Context, ev conversation.UsageEvent) {
	if !c.enabled.Load() {
		return
	}
	rec := &Record{
		ID:        idgen.New("llmcap-", 4),
		Timestamp: time.Now(),
		SessionID: ev.Context.SessionID,
		TurnID:    ev.Context.TurnID,
		JobID:     ev.Context.JobID,
		Channel:   ev.Context.SourceChannel,
		Model:     ev.Request.Model,
		Stream:    ev.Stream,
		LatencyMS: ev.Latency.Milliseconds(),
		Request:   ev.Request,
		Response:  ev.Response,
	}
	if ev.Error != nil {
		rec.Error = ev.Error.Error()
	}
	if rec.Model == "" {
		rec.Model = ev.Context.TargetModel
	}

	c.write(rec)

	c.mu.Lock()
	c.records = append(c.records, rec)
	if len(c.records) > c.maxMem {
		// Drop the oldest records but keep the most recent maxMem.
		overflow := len(c.records) - c.maxMem
		c.records = append([]*Record(nil), c.records[overflow:]...)
	}
	c.mu.Unlock()
}

func (c *Capture) write(rec *Record) {
	line, err := json.Marshal(rec)
	if err != nil {
		logger.Warnf("llm capture: marshal record: %v", err)
		return
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.file == nil {
		return
	}
	if _, err := c.file.Write(append(line, '\n')); err != nil {
		logger.Warnf("llm capture: append record: %v", err)
		return
	}
	_ = c.file.Sync()
}

// List returns recent records, newest first, as lightweight summaries. The
// full request/response bodies are stripped so the UI list stays cheap; the
// detail endpoint serves the complete record.
func (c *Capture) List(limit int) []Summary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if limit <= 0 || limit > len(c.records) {
		limit = len(c.records)
	}
	out := make([]Summary, 0, limit)
	for i := len(c.records) - 1; i >= 0 && len(out) < limit; i-- {
		rec := c.records[i]
		out = append(out, Summary{
			ID:        rec.ID,
			Timestamp: rec.Timestamp,
			SessionID: rec.SessionID,
			TurnID:    rec.TurnID,
			Model:     rec.Model,
			Stream:    rec.Stream,
			LatencyMS: rec.LatencyMS,
			Error:     rec.Error,
			HasResponse: rec.Response != nil,
			InputTokens:   tokenCount(rec.Response),
		})
	}
	return out
}

// Get returns one full record by id, or nil when not in the ring buffer.
func (c *Capture) Get(id string) *Record {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, rec := range c.records {
		if rec.ID == id {
			return rec
		}
	}
	return nil
}

// Clear wipes the in-memory ring. On-disk history is kept (the user can delete
// the file manually).
func (c *Capture) Clear() {
	c.mu.Lock()
	c.records = nil
	c.mu.Unlock()
}

func tokenCount(resp *protocol.Response) int {
	if resp == nil || resp.Usage == nil {
		return 0
	}
	return resp.Usage.InputTokens + resp.Usage.OutputTokens
}

// Summary is the lightweight list entry served to the web UI.
type Summary struct {
	ID          string    `json:"id"`
	Timestamp   time.Time `json:"timestamp"`
	SessionID   string    `json:"session_id,omitempty"`
	TurnID      string    `json:"turn_id,omitempty"`
	Model       string    `json:"model,omitempty"`
	Stream      bool      `json:"stream"`
	LatencyMS   int64     `json:"latency_ms"`
	Error       string    `json:"error,omitempty"`
	HasResponse bool      `json:"has_response"`
	InputTokens int       `json:"input_tokens"`
}

// ReadAllFromFile loads every record currently persisted in the jsonl dump
// (for tools/CLI or future pagination). Records are returned oldest first.
func ReadAllFromFile(path string) ([]*Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		out = append(out, &rec)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Timestamp.Before(out[j].Timestamp)
	})
	return out, nil
}
