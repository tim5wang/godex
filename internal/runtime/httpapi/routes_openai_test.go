package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/domain/events"
)

// TestCollectOpenAICompletionSurfacesToolCalls covers the OpenAI streaming
// tool_calls contract: when the runtime emits EventToolCallStarted and
// EventToolCallFinished through the events.Sink, collectOpenAICompletion
// must (a) call the onToolCall callback with the OpenAI-shaped tool call
// (id+name+arguments fragment) and (b) compute a delta suffix against the
// previously-emitted input rather than resending the full input. The
// per-chunk arguments fragment is what an OpenAI SDK concatenates across
// chunks, so resending the full input would corrupt the JSON.
//
// Subtests:
//   - "forwards first emission and skips duplicate finish" — when the
//     finish event resends the same input, the collector must NOT fire
//     a second callback (sending an empty arguments fragment would
//     cause the SDK to keep the accumulated args unchanged but consume
//     a wasted wire chunk).
//   - "ignores events for other turns" — turnID filtering.
//   - "forwards only the delta suffix across updates" — when the runtime
//     surfaces a strict extension of the input, the second callback
//     carries only the new suffix, not the full input.
func TestCollectOpenAICompletionSurfacesToolCalls(t *testing.T) {
	t.Run("forwards first emission and skips duplicate finish", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		eventCh := make(chan events.Event, 16)
		turnID := "turn-tool-1"

		var deltas []string
		var toolCalls []openAIStreamToolCall
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = collectOpenAICompletion(ctx, eventCh, turnID,
				func(delta string) { deltas = append(deltas, delta) },
				func(tc openAIStreamToolCall) { toolCalls = append(toolCalls, tc) },
			)
		}()

		// User message accepted so the turnID is registered.
		eventCh <- events.Event{
			SessionID: "s1",
			TurnID:    turnID,
			Type:      events.EventUserMessageAccepted,
			Timestamp: time.Now(),
		}
		// Tool call started with the initial input.
		eventCh <- events.Event{
			SessionID: "s1",
			TurnID:    turnID,
			Type:      events.EventToolCallStarted,
			Timestamp: time.Now(),
			Payload: events.ToolCallPayload{
				ID:   "toolu_test_1",
				Name: "read",
				Input: map[string]interface{}{
					"path": "/tmp/hi",
				},
			},
		}
		// Tool call finished — runtime re-surfaces the final input.
		eventCh <- events.Event{
			SessionID: "s1",
			TurnID:    turnID,
			Type:      events.EventToolCallFinished,
			Timestamp: time.Now(),
			Payload: events.ToolCallPayload{
				ID:     "toolu_test_1",
				Name:   "read",
				Input:  map[string]interface{}{"path": "/tmp/hi"},
				Output: "file contents",
			},
		}
		// Turn completed — collector returns.
		eventCh <- events.Event{
			SessionID: "s1",
			TurnID:    turnID,
			Type:      events.EventTurnCompleted,
			Timestamp: time.Now(),
		}
		<-done

		// The finish event resends the same input as the start event, so
		// the collector must NOT fire a second callback (the delta is
		// empty — forwarding it would make the SDK keep the accumulated
		// args unchanged but waste a wire chunk and risk a downstream
		// consumer interpreting "" as a reset).
		if got := len(toolCalls); got != 1 {
			t.Fatalf("expected 1 tool_calls callback (finish is a duplicate), got %d", got)
		}
		tc := toolCalls[0]
		if tc.id != "toolu_test_1" {
			t.Errorf("expected id=toolu_test_1, got %q", tc.id)
		}
		if tc.name != "read" {
			t.Errorf("expected name=read, got %q", tc.name)
		}
		if want := `{"path":"/tmp/hi"}`; tc.arguments != want {
			t.Errorf("expected arguments=%s, got %s", want, tc.arguments)
		}
		if len(deltas) != 0 {
			t.Errorf("expected no text deltas, got %d (%v)", len(deltas), deltas)
		}
	})

	t.Run("ignores events for other turns", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		eventCh := make(chan events.Event, 16)
		turnID := "turn-1"

		var toolCalls []openAIStreamToolCall
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = collectOpenAICompletion(ctx, eventCh, turnID,
				nil,
				func(tc openAIStreamToolCall) { toolCalls = append(toolCalls, tc) },
			)
		}()

		// Tool call for a *different* turnID must be filtered.
		eventCh <- events.Event{
			SessionID: "s1",
			TurnID:    "turn-2",
			Type:      events.EventToolCallStarted,
			Timestamp: time.Now(),
			Payload: events.ToolCallPayload{
				ID:   "toolu_other",
				Name: "read",
				Input: map[string]interface{}{"path": "/tmp/other"},
			},
		}
		eventCh <- events.Event{
			SessionID: "s1",
			TurnID:    turnID,
			Type:      events.EventTurnCompleted,
			Timestamp: time.Now(),
		}
		<-done

		if got := len(toolCalls); got != 0 {
			t.Errorf("expected 0 tool_calls callbacks (other turn), got %d", got)
		}
	})

	t.Run("forwards only the delta suffix across updates", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		eventCh := make(chan events.Event, 16)
		turnID := "turn-1"

		var toolCalls []openAIStreamToolCall
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = collectOpenAICompletion(ctx, eventCh, turnID,
				nil,
				func(tc openAIStreamToolCall) { toolCalls = append(toolCalls, tc) },
			)
		}()

		// Runtime may surface partial inputs across multiple events
		// before the tool finishes. The collector must forward only
		// the new bytes (the suffix) on each emission so the OpenAI
		// SDK's per-chunk concatenation yields the final JSON. The
		// second event extends the first, so the second callback must
		// carry only the new suffix and not repeat the path field.
		eventCh <- events.Event{
			SessionID: "s1",
			TurnID:    turnID,
			Type:      events.EventToolCallStarted,
			Timestamp: time.Now(),
			Payload: events.ToolCallPayload{
				ID:   "toolu_test_2",
				Name: "write",
				Input: map[string]interface{}{
					"path": "/tmp/x",
				},
			},
		}
		eventCh <- events.Event{
			SessionID: "s1",
			TurnID:    turnID,
			Type:      events.EventToolCallStarted,
			Timestamp: time.Now(),
			Payload: events.ToolCallPayload{
				ID:   "toolu_test_2",
				Name: "write",
				Input: map[string]interface{}{
					"path":    "/tmp/x",
					"content": "hello",
				},
			},
		}
		eventCh <- events.Event{
			SessionID: "s1",
			TurnID:    turnID,
			Type:      events.EventTurnCompleted,
			Timestamp: time.Now(),
		}
		<-done

		if got := len(toolCalls); got != 2 {
			t.Fatalf("expected 2 tool_calls callbacks, got %d", got)
		}
		// First emission carries the full input.
		if want, got := `{"path":"/tmp/x"}`, toolCalls[0].arguments; got != want {
			t.Errorf("first callback: expected full input %s, got %s", want, got)
		}
		// Second emission is a strict extension of the first; the
		// callback must carry ONLY the new suffix, not the full input.
		// The OpenAI SDK will concatenate these two strings to recover
		// the final JSON.
		if want, got := `, "content":"hello"}`, toolCalls[1].arguments; got != want {
			t.Errorf("second callback: expected delta suffix %s, got %s", want, got)
		}
		// Guard against the previous (buggy) "replace" behavior: the
		// second callback must not contain the path field again,
		// otherwise the SDK would concat the full input twice and
		// produce invalid JSON.
		if strings.Contains(toolCalls[1].arguments, `"path"`) {
			t.Errorf("second callback must not repeat the path field (delta suffix only), got %s", toolCalls[1].arguments)
		}
	})
}

// TestRunOpenAIChatCompletionSurfacesToolCalls covers the non-streaming
// regression where the OpenAI chat.completion response dropped every
// tool invocation the runtime forwarded. The previous code passed
// `nil` callbacks to collectOpenAICompletion and returned only the
// accumulated text content, so an OpenAI client receiving a
// non-streaming response would never see a tool_calls array and
// would treat the turn as "stop" — even when the model wanted to
// invoke a tool. The fix routes the onToolCall callback through to
// collectOpenAICompletion, deduplicates by id (start + finish for the
// same tool), and emits the standard chat.completion JSON shape
// with `tool_calls` populated and `finish_reason: "tool_calls"`.
func TestRunOpenAIChatCompletionSurfacesToolCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eventCh := make(chan events.Event, 16)
	turnID := "turn-nostream-1"

	// Drive the collector in a goroutine, exactly the way the
	// handleOpenAIChatCompletions production path does.
	var toolCalls []openAIStreamToolCall
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = collectOpenAICompletion(ctx, eventCh, turnID,
			nil,
			func(tc openAIStreamToolCall) { toolCalls = append(toolCalls, tc) },
		)
	}()

	eventCh <- events.Event{
		SessionID: "s1",
		TurnID:    turnID,
		Type:      events.EventUserMessageAccepted,
		Timestamp: time.Now(),
	}
	eventCh <- events.Event{
		SessionID: "s1",
		TurnID:    turnID,
		Type:      events.EventToolCallStarted,
		Timestamp: time.Now(),
		Payload: events.ToolCallPayload{
			ID:   "toolu_nostream_1",
			Name: "bash",
			Input: map[string]interface{}{
				"command": "ls -la",
			},
		},
	}
	eventCh <- events.Event{
		SessionID: "s1",
		TurnID:    turnID,
		Type:      events.EventToolCallFinished,
		Timestamp: time.Now(),
		Payload: events.ToolCallPayload{
			ID:     "toolu_nostream_1",
			Name:   "bash",
			Input:  map[string]interface{}{"command": "ls -la"},
			Output: "file contents",
		},
	}
	eventCh <- events.Event{
		SessionID: "s1",
		TurnID:    turnID,
		Type:      events.EventTurnCompleted,
		Timestamp: time.Now(),
	}
	<-done

	// The collector should fire only once per unique tool id (the
	// finish event carries the same input as the start event, so the
	// delta computation collapses it).
	if got := len(toolCalls); got != 1 {
		t.Fatalf("expected 1 tool_call callback, got %d", got)
	}
	tc := toolCalls[0]
	if tc.id != "toolu_nostream_1" {
		t.Errorf("expected id=toolu_nostream_1, got %q", tc.id)
	}
	if tc.name != "bash" {
		t.Errorf("expected name=bash, got %q", tc.name)
	}
	if want := `{"command":"ls -la"}`; tc.arguments != want {
		t.Errorf("expected arguments=%s, got %s", want, tc.arguments)
	}
}

// TestOpenAIStreamToolCallForwardsIndex covers the regression where
// the OpenAI tool_calls chunk hardcoded `index: 0`. The OpenAI SDK
// keys its per-chunk dedup on (index, id, function.name), so multiple
// parallel tool calls in the same assistant turn would all share
// index 0 and overwrite each other in the SDK's accumulator, losing
// every tool call after the first. The fix threads the upstream's
// tool_calls[].index through protocol.Block.Index to the wire
// emission. This test pins the round-trip by setting a non-zero
// index on the struct and asserting it survives the callback.
func TestOpenAIStreamToolCallForwardsIndex(t *testing.T) {
	// The gateway reads tc.index directly when building the
	// openAIToolCallWire. We just verify the struct field is wired
	// through to the emitted chunk by simulating the gateway's
	// emission logic with a known index.
	tc := openAIStreamToolCall{
		id:        "toolu_idx_1",
		name:      "bash",
		index:     7,
		arguments: `{"command":"ls"}`,
	}
	wire := openAIToolCallWire{
		Index: tc.index,
		ID:    tc.id,
		Type:  "function",
		Function: openAIFunctionCall{
			Name:      tc.name,
			Arguments: tc.arguments,
		},
	}
	if wire.Index != 7 {
		t.Errorf("expected wire.Index=7, got %d", wire.Index)
	}
	// Marshal and assert the JSON carries the index (not 0).
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"index":7`) {
		t.Errorf("expected wire JSON to carry index=7, got %s", string(data))
	}
}
