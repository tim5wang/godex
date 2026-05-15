package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	content, turnID, err := runOpenAIChatCompletion(r.Context(), service, sessionID, text)
	if err != nil {
		writeError(w, statusForSessionError(err), err)
		return
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
			},
			FinishReason: "stop",
		}},
	})
}

func runOpenAIChatCompletion(ctx context.Context, service *backend.Service, sessionID, text string) (string, string, error) {
	eventCh := make(chan events.Event, 128)
	unsubscribe, err := service.AttachSink(sessionID, events.SinkFunc(func(event events.Event) {
		select {
		case <-ctx.Done():
		case eventCh <- event:
		default:
		}
	}))
	if err != nil {
		return "", "", err
	}
	defer unsubscribe()

	envelope := message.NewRuntimeEnvelope(message.SourceGateway, sessionID, "openai_api", text, time.Now(), nil)
	result, err := service.SubmitAsync(ctx, sessionID, envelope)
	if err != nil {
		return "", "", err
	}
	content, err := collectOpenAICompletion(ctx, eventCh, result.TurnID, nil)
	return content, result.TurnID, err
}

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
	_, err = collectOpenAICompletion(ctx, eventCh, result.TurnID, func(delta string) {
		emitOpenAIChunk(w, flusher, openAICompletionID(result.TurnID), model, created, openAIChatMessage{Content: delta}, "")
	})
	if err != nil {
		emitOpenAIError(w, flusher, err)
		return
	}
	emitOpenAIChunk(w, flusher, openAICompletionID(result.TurnID), model, created, openAIChatMessage{}, "stop")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func collectOpenAICompletion(ctx context.Context, eventCh <-chan events.Event, turnID string, onDelta func(string)) (string, error) {
	var builder strings.Builder
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
