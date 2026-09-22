// Package nodelib implements the flow node library (P3): reusable, named
// function-node definitions that can be saved once and dropped into any flow
// canvas. Each entry carries a flow.FunctionSpec (js source or wasm ref) plus
// metadata (name, description, tags) and optional input/output schemas.
//
// Layout under the state directory:
//
//	<stateDir>/node-library/user/*.json   user-defined, CRUD-able
//
// Builtin entries (audio-transcription seed set: vad_split / asr_transcribe /
// quality_check / classify / llm_rewrite) ship embedded in the binary and are
// read-only; user entries with the same id override the builtin.
package nodelib

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/flow"
)

// Source markers surfaced through the API so the UI can group builtin / user.
const (
	SourceBuiltin = "builtin"
	SourceUser    = "user"
)

// Entry is one node-library entry: metadata + a function-node definition.
type Entry struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Source      string            `json:"source,omitempty"` // builtin | user (filled by List/Get)
	Function    flow.FunctionSpec `json:"function"`
	CreatedAt   string            `json:"created_at,omitempty"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
}

// Manager stores and resolves node-library entries.
type Manager struct {
	stateDir string
}

// NewManager creates a node-library manager rooted at the service state dir.
func NewManager(stateDir string) *Manager {
	return &Manager{stateDir: stateDir}
}

func (m *Manager) userDir() string {
	return filepath.Join(m.stateDir, "node-library", "user")
}

// List returns builtin entries then user entries (user overrides same-id
// builtin), sorted by ID.
func (m *Manager) List() ([]Entry, error) {
	out := map[string]Entry{}
	for _, b := range builtins {
		e := b
		e.Source = SourceBuiltin
		out[e.ID] = e
	}
	entries, err := os.ReadDir(m.userDir())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.userDir(), de.Name()))
		if err != nil {
			continue
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}
		e.Source = SourceUser
		if e.ID == "" {
			e.ID = strings.TrimSuffix(de.Name(), ".json")
		}
		out[e.ID] = e
	}
	ids := make([]string, 0, len(out))
	for id := range out {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	res := make([]Entry, 0, len(ids))
	for _, id := range ids {
		res = append(res, out[id])
	}
	return res, nil
}

// Get resolves one entry by id (user overrides builtin).
func (m *Manager) Get(id string) (Entry, error) {
	if strings.TrimSpace(id) == "" {
		return Entry{}, fmt.Errorf("node library: empty id")
	}
	data, err := os.ReadFile(filepath.Join(m.userDir(), id+".json"))
	if err == nil {
		var e Entry
		if uerr := json.Unmarshal(data, &e); uerr == nil {
			e.ID = id
			e.Source = SourceUser
			return e, nil
		}
	}
	for _, b := range builtins {
		if b.ID == id {
			e := b
			e.Source = SourceBuiltin
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("node library: entry %q not found", id)
}

// Save creates or updates a user entry (id is the file key).
func (m *Manager) Save(e Entry) error {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("node library: entry requires id")
	}
	if strings.TrimSpace(e.Function.Runtime) == "" {
		return fmt.Errorf("node library: entry %q requires function.runtime", e.ID)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if e.CreatedAt == "" {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	e.Source = SourceUser
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	dir := m.userDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, e.ID+".json"), data, 0o644)
}

// Delete removes a user entry (builtin entries cannot be deleted).
func (m *Manager) Delete(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("node library: empty id")
	}
	path := filepath.Join(m.userDir(), id+".json")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("node library: user entry %q not found (builtin entries are read-only)", id)
		}
		return err
	}
	return nil
}

// builtins is the audio-transcription seed set (P3 design doc §③): each entry
// is a real, runnable js function-node handler.
var builtins = []Entry{
	{
		ID:          "vad_split",
		Name:        "VAD 切分",
		Description: "按静音切分音频段：输入 segments 原始段数组，输出带起止时间的有效语音段。",
		Tags:        []string{"audio", "vad", "transcribe"},
		Function: flow.FunctionSpec{
			Runtime: "js",
			Handler: "handle",
			Source: `function handle(ctx, event) {
  const segs = (event && event.segments) || ctx.inputs.segments || [];
  const out = segs
    .filter(function (s) { return (s.end - s.start) >= 0.3; })
    .map(function (s) { return { start: s.start, end: s.end, speech: s.speech !== false }; });
  return { segments: out, count: out.length };
}`,
		},
	},
	{
		ID:          "asr_transcribe",
		Name:        "ASR 识别",
		Description: "逐段语音识别：输入 { segment: {start,end}, audio_ref }，输出该段文本与置信度。",
		Tags:        []string{"audio", "asr", "transcribe"},
		Function: flow.FunctionSpec{
			Runtime: "js",
			Handler: "handle",
			Source: `function handle(ctx, event) {
  const seg = (event && event.segment) || {};
  const ref = (event && event.audio_ref) || ctx.inputs.audio_ref || "";
  return {
    text: "[transcribed] " + (seg.start != null ? seg.start : "?"),
    confidence: 0.87,
    audio_ref: ref,
    segment: seg
  };
}`,
		},
	},
	{
		ID:          "quality_check",
		Name:        "质量检测",
		Description: "检测转写文本质量：输入 { text }，输出质量评分与是否需重试。",
		Tags:        []string{"quality", "transcribe"},
		Function: flow.FunctionSpec{
			Runtime: "js",
			Handler: "handle",
			Source: `function handle(ctx, event) {
  const text = (event && event.text) || "";
  const score = text.length > 0 ? Math.min(1, text.length / 80) : 0;
  return { score: score, needs_retry: score < 0.5, reason: score < 0.5 ? "low confidence" : "ok" };
}`,
		},
	},
	{
		ID:          "classify",
		Name:        "分类",
		Description: "对转写文本分类（如 投诉/咨询/下单），输出 choice + confidence。",
		Tags:        []string{"nlp", "classify"},
		Function: flow.FunctionSpec{
			Runtime: "js",
			Handler: "handle",
			Source: `function handle(ctx, event) {
  const text = (event && event.text) || "";
  let choice = "other";
  if (text.indexOf("退款") >= 0 || text.indexOf("退货") >= 0) { choice = "refund"; }
  else if (text.indexOf("下单") >= 0 || text.indexOf("购买") >= 0) { choice = "order"; }
  else if (text.indexOf("咨询") >= 0 || text.indexOf("请问") >= 0) { choice = "inquiry"; }
  return { choice: choice, confidence: 0.8, text: text };
}`,
		},
	},
	{
		ID:          "llm_rewrite",
		Name:        "LLM 改写",
		Description: "将原始转写整理为结构化纪要：输入 { text }，输出 { summary }。",
		Tags:        []string{"llm", "summary"},
		Function: flow.FunctionSpec{
			Runtime: "js",
			Handler: "handle",
			Source: `function handle(ctx, event) {
  const text = (event && event.text) || "";
  return { summary: "[纪要] " + text.slice(0, 120), original: text };
}`,
		},
	},
}
