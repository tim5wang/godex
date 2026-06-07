package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/backend"
)

func handleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request, service *backend.Service) {
	var req openAIChatCompletionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	text := openAIUserMessageText(req.Messages)
	if strings.TrimSpace(text) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("at least one non-empty user message is required"))
		return
	}
	sessionID, err := openAIResolveSession(r.Context(), service, r, req)
	if err != nil {
		writeError(w, statusForSessionError(err), err)
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = "godex"
	}
	if req.Stream {
		streamOpenAIChatCompletion(w, r, service, sessionID, model, text)
		return
	}
	content, turnID, toolCalls, err := runOpenAIChatCompletion(r.Context(), service, sessionID, text)
	if err != nil {
		writeError(w, statusForSessionError(err), err)
		return
	}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		// The model wanted to invoke one or more tools. Surface the
		// OpenAI SDK's canonical finish reason so the client tool
		// loop runs the requested tools rather than treating the
		// turn as done.
		finishReason = "tool_calls"
	}
	writeJSON(w, http.StatusOK, openAIChatCompletionResponse{
		ID:      openAICompletionID(turnID),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openAIChatChoice{{
			Index: 0,
			Message: &openAIChatMessage{
				Role:    "assistant",
				Content: content,
				// Only include tool_calls when the runtime surfaced
				// at least one invocation. OpenAI SDKs require the
				// `tool_calls` array to be present (not null) for
				// the assistant message to be parsed as a tool
				// call, so we conditionally attach it.
				ToolCalls: toolCalls,
			},
			FinishReason: finishReason,
		}},
	})
}

func runOpenAIChatCompletion(ctx context.Context, service *backend.Service, sessionID, text string) (string, string, []openAIToolCallWire, error) {
	eventCh := make(chan events.Event, 128)
	unsubscribe, err := service.AttachSink(sessionID, events.SinkFunc(func(event events.Event) {
		select {
		case <-ctx.Done():
		case eventCh <- event:
		default:
		}
	}))
	if err != nil {
		return "", "", nil, err
	}
	defer unsubscribe()

	envelope := message.NewRuntimeEnvelope(message.SourceGateway, sessionID, "openai_api", text, time.Now(), nil)
	result, err := service.SubmitAsync(ctx, sessionID, envelope)
	if err != nil {
		return "", "", nil, err
	}
	// The non-streaming path surfaces every tool invocation the runtime
	// forwards so the OpenAI client can execute the requested tool
	// rather than treating the turn as done. The web-token path is the
	// only caller; the response shape is the standard OpenAI
	// chat.completion JSON (text content + optional tool_calls array).
	var toolCalls []openAIToolCallWire
	content, err := collectOpenAICompletion(ctx, eventCh, result.TurnID, nil, func(tc openAIStreamToolCall) {
		// Dedupe by id; the runtime may emit start + finish events
		// for the same tool call, and we only want the final
		// arguments in the non-streaming response.
		for _, existing := range toolCalls {
			if existing.ID == tc.id {
				return
			}
		}
		toolCalls = append(toolCalls, openAIToolCallWire{
			Index: tc.index,
			ID:    tc.id,
			Type:  "function",
			Function: openAIFunctionCall{
				Name:      tc.name,
				Arguments: tc.arguments,
			},
		})
	})
	return content, result.TurnID, toolCalls, err
}

// openAIStreamToolCall is one in-flight OpenAI chat.completion.chunk
// tool_calls delta we are forwarding. We dedupe by id and forward a
// per-chunk arguments fragment so the receiving OpenAI SDK can
// concatenate it into the final JSON input. The runtime resends the
// full input on every ToolCall event, so we track previousInput to
// compute a delta suffix that adds the new keys (or falls back to
// the full input when the change cannot be expressed as a clean
// suffix, e.g. value updates or key removals).
type openAIStreamToolCall struct {
	id        string
	name      string
	index     int
	// arguments is the per-chunk arguments fragment the gateway will
	// forward in this OpenAI chat.completion.chunk. OpenAI SDKs
	// concatenate arguments across chunks, so we must send only the
	// new bytes since the last emission, not the full input.
	arguments string
	// previousInput is a copy of the last full input we emitted a
	// delta for. We compare the new payload.Input against this to
	// decide what delta to forward.
	previousInput map[string]interface{}
	// previousRaw is the marshaled form of previousInput, kept for a
	// fast identity check (skip on identical resends).
	previousRaw string
}

// onOpenAIToolDeltaFn is the callback the web-token streaming handler
// supplies to collectOpenAICompletion so it can forward every tool
// invocation to the OpenAI SDK client. The id and name are always set;
// the arguments fragment may be empty for the start frame.
type onOpenAIToolDeltaFn func(delta openAIStreamToolCall)

func streamOpenAIChatCompletion(w http.ResponseWriter, r *http.Request, service *backend.Service, sessionID, model, text string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	ctx := r.Context()
	eventCh := make(chan events.Event, 128)
	unsubscribe, err := service.AttachSink(sessionID, events.SinkFunc(func(event events.Event) {
		select {
		case <-ctx.Done():
		case eventCh <- event:
		default:
		}
	}))
	if err != nil {
		writeError(w, statusForSessionError(err), err)
		return
	}
	defer unsubscribe()

	envelope := message.NewRuntimeEnvelope(message.SourceGateway, sessionID, "openai_api", text, time.Now(), nil)
	result, err := service.SubmitAsync(ctx, sessionID, envelope)
	if err != nil {
		writeError(w, statusForSessionError(err), err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	created := time.Now().Unix()
	emitOpenAIChunk(w, flusher, openAICompletionID(result.TurnID), model, created, openAIChatMessage{Role: "assistant"}, "")
	// toolCallsSeen flips when the runtime forwards a tool invocation so
	// the terminal chunk can declare finish_reason="tool_calls" (OpenAI's
	// canonical stop reason) instead of "stop". The OpenAI SDK uses this
	// signal to enter its tool loop rather than treating the turn as
	// done.
	toolCallsSeen := false
	_, err = collectOpenAICompletion(ctx, eventCh, result.TurnID, func(delta string) {
		emitOpenAIChunk(w, flusher, openAICompletionID(result.TurnID), model, created, openAIChatMessage{Content: delta}, "")
	}, func(tc openAIStreamToolCall) {
		toolCallsSeen = true
		calls := []openAIToolCallWire{{
			Index: tc.index,
			ID:    tc.id,
			Type:  "function",
			Function: openAIFunctionCall{
				Name:      tc.name,
				Arguments: tc.arguments,
			},
		}}
		emitOpenAIChunk(w, flusher, openAICompletionID(result.TurnID), model, created, openAIChatMessage{ToolCalls: calls}, "")
	})
	if err != nil {
		emitOpenAIError(w, flusher, err)
		return
	}
	finishReason := "stop"
	if toolCallsSeen {
		finishReason = "tool_calls"
	}
	emitOpenAIChunk(w, flusher, openAICompletionID(result.TurnID), model, created, openAIChatMessage{}, finishReason)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func collectOpenAICompletion(ctx context.Context, eventCh <-chan events.Event, turnID string, onDelta func(string), onToolCall onOpenAIToolDeltaFn) (string, error) {
	var builder strings.Builder
	// toolBuffers maps tool call id → in-flight openAIStreamToolCall so
	// we can surface the (possibly updated) input as the runtime emits
	// successive events. The runtime surfaces the full input on every
	// ToolCall event, so we always reassign the arguments rather than
	// concatenating fragments, which keeps each OpenAI tool_calls chunk
	// idempotent for SDK consumers that re-emit the latest arguments.
	toolBuffers := map[string]*openAIStreamToolCall{}
	for {
		select {
		case <-ctx.Done():
			return strings.TrimSpace(builder.String()), ctx.Err()
		case event := <-eventCh:
			if event.TurnID != turnID {
				continue
			}
			switch event.Type {
			case events.EventAssistantTextDelta:
				payload, ok := event.Payload.(events.TextPayload)
				if !ok || payload.Text == "" {
					continue
				}
				builder.WriteString(payload.Text)
				if onDelta != nil {
					onDelta(payload.Text)
				}
			case events.EventToolCallStarted, events.EventToolCallFinished:
				payload, ok := event.Payload.(events.ToolCallPayload)
				if !ok || strings.TrimSpace(payload.Name) == "" {
					continue
				}
				toolID := strings.TrimSpace(payload.ID)
				if toolID == "" {
					// Defensive: never emit an OpenAI tool_calls chunk
					// with an empty id because the SDK uses it to dedupe
					// across chunks. Use a stable placeholder so the
					// downstream tool loop can still execute.
					toolID = fmt.Sprintf("call_%d", time.Now().UnixNano())
				}
				buf, exists := toolBuffers[toolID]
				if !exists {
					buf = &openAIStreamToolCall{id: toolID, name: payload.Name}
					toolBuffers[toolID] = buf
				}
				// Compute the per-chunk arguments fragment. The runtime
				// resends the full input on every ToolCall event, but
				// OpenAI SDKs concatenate arguments across chunks, so we
				// must not resend the full string. We compute a delta
				// semantically:
				//   - First emission: forward the full input.
				//   - Identical resend: skip (no change).
				//   - New keys added: forward a JSON suffix that adds
				//     only the new keys (e.g. `, "content":"hello"}`
				//     appended to `{"path":"/tmp/x"}`).
				//   - Existing key updated, key removed, or shape
				//     changed: fall back to the full new input. The
				//     SDK's accumulated args may become invalid, but
				//     this is the best we can do without a patch-based
				//     protocol.
				// Reset the per-chunk arguments before computing the
				// new delta so the "no change" path doesn't surface
				// the previous chunk's fragment again.
				buf.arguments = ""
				if len(payload.Input) > 0 {
					if raw, err := json.Marshal(payload.Input); err == nil {
						newRaw := string(raw)
						var delta string
						switch {
						case buf.previousRaw == "":
							delta = newRaw
						case newRaw == buf.previousRaw:
							// No change since the last emission.
						default:
							delta = openAIInputDeltaSuffix(buf.previousInput, payload.Input, newRaw)
						}
						if delta != "" {
							buf.previousInput = cloneInputMap(payload.Input)
							buf.previousRaw = newRaw
							buf.arguments = delta
						}
					}
				}
				if onToolCall != nil && buf.arguments != "" {
					onToolCall(*buf)
				}
				// Drain the buffer once the tool has finished so the
				// next invocation starts fresh. The handler still
				// received the final tool_calls chunk above.
				if event.Type == events.EventToolCallFinished {
					delete(toolBuffers, toolID)
				}
			case events.EventErrorRaised:
				if payload, ok := event.Payload.(events.NoticePayload); ok && strings.TrimSpace(payload.Message) != "" {
					return strings.TrimSpace(builder.String()), errors.New(payload.Message)
				}
			case events.EventTurnCompleted:
				return strings.TrimSpace(builder.String()), nil
			}
		}
	}
}

func emitOpenAIChunk(w http.ResponseWriter, flusher http.Flusher, id, model string, created int64, delta openAIChatMessage, finishReason string) {
	chunk := openAIChatCompletionResponse{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []openAIChatChoice{{
			Index:        0,
			Delta:        &delta,
			FinishReason: finishReason,
		}},
	}
	data, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func emitOpenAIError(w http.ResponseWriter, flusher http.Flusher, err error) {
	data, marshalErr := json.Marshal(map[string]interface{}{
		"error": map[string]string{
			"message": err.Error(),
			"type":    "godex_error",
		},
	})
	if marshalErr != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func openAIResolveSession(ctx context.Context, service *backend.Service, r *http.Request, req openAIChatCompletionRequest) (string, error) {
	requested := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if requested == "" {
		requested = strings.TrimSpace(r.Header.Get("X-GoDex-Session-ID"))
	}
	if requested == "" && req.Metadata != nil {
		if value, ok := req.Metadata["session_id"]; ok {
			requested = strings.TrimSpace(fmt.Sprint(value))
		}
	}
	if requested != "" {
		if _, err := service.Snapshot(ctx, requested); err == nil {
			return requested, nil
		}
		opened, err := service.OpenSession(ctx, backend.SessionLocator{
			Channel: "openai_api",
			Key:     requested,
			UserID:  "openai_api",
			Metadata: map[string]string{
				"api_session": requested,
			},
		})
		if err != nil {
			return "", err
		}
		return opened.SessionID, nil
	}
	opened, err := service.OpenSession(ctx, backend.SessionLocator{
		Channel: "openai_api",
		Key:     fmt.Sprintf("chat-%d", time.Now().UnixNano()),
		UserID:  "openai_api",
	})
	if err != nil {
		return "", err
	}
	return opened.SessionID, nil
}

func openAIUserMessageText(messages []openAIChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if !strings.EqualFold(strings.TrimSpace(messages[i].Role), "user") {
			continue
		}
		return strings.TrimSpace(openAIContentText(messages[i].Content))
	}
	return ""
}

func openAIContentText(content interface{}) string {
	switch value := content.(type) {
	case string:
		return value
	case []interface{}:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			part, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			partType := strings.TrimSpace(fmt.Sprint(part["type"]))
			switch partType {
			case "text", "input_text":
				if text := strings.TrimSpace(fmt.Sprint(part["text"])); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func openAICompletionID(turnID string) string {
	if strings.TrimSpace(turnID) == "" {
		return "chatcmpl-godex"
	}
	return "chatcmpl-" + strings.TrimSpace(turnID)
}

// openAIInputDeltaSuffix returns a JSON fragment that, when
// concatenated to the previously-emitted input, yields the new
// input. The semantics are:
//
//   - prev is nil: returns newRaw (first emission).
//   - prev and next are deeply equal: returns "" (caller should skip).
//   - new keys added to next: returns a JSON suffix that adds only
//     the new keys, e.g. `, "content":"hello"}` appended to
//     `{"path":"/tmp/x"}`. The OpenAI SDK concatenates the two
//     strings to recover the final JSON.
//   - existing key updated, key removed, or shape changed: returns
//     newRaw (the full new input). The SDK's accumulated args may
//     become invalid, but this is the best we can do without a
//     patch-based protocol.
func openAIInputDeltaSuffix(prev map[string]interface{}, next map[string]interface{}, newRaw string) string {
	if prev == nil {
		return newRaw
	}
	newKeys := make(map[string]interface{}, len(next))
	for k, v := range next {
		if _, ok := prev[k]; !ok {
			newKeys[k] = v
		}
	}
	if len(newKeys) == 0 {
		if mapsEqual(prev, next) {
			return ""
		}
		return newRaw
	}
	raw, err := json.Marshal(newKeys)
	if err != nil {
		return newRaw
	}
	suffix := string(raw)
	if strings.HasPrefix(suffix, "{") {
		// `{"content":"hello"}` -> `, "content":"hello"}` so the
		// suffix is a valid extension of the previous JSON object.
		suffix = ", " + suffix[1:]
	}
	return suffix
}

func mapsEqual(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !valueEqual(av, bv) {
			return false
		}
	}
	return true
}

func valueEqual(a, b interface{}) bool {
	am, aok := a.(map[string]interface{})
	bm, bok := b.(map[string]interface{})
	if aok && bok {
		return mapsEqual(am, bm)
	}
	as, aok := a.([]interface{})
	bs, bok := b.([]interface{})
	if aok && bok {
		if len(as) != len(bs) {
			return false
		}
		for i := range as {
			if !valueEqual(as[i], bs[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

func cloneInputMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
