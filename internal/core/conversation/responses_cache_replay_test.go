package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/responses"
	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func writeResponsesEvent(w http.ResponseWriter, event any) {
	data, _ := json.Marshal(event)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}

func TestResponsesReplayKeepsGrowingWirePrefixAndCacheBoundaries(t *testing.T) {
	var requests []map[string]any
	// This fake cache only recognizes prefixes actually written at explicit
	// boundaries. It does not simply return a fixed, optimistic cached count.
	cache := make(map[string]bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests = append(requests, body)
		input := body["input"].([]any)
		cached := 0
		for i, item := range input {
			data, _ := json.Marshal(item)
			if !strings.Contains(string(data), "prompt_cache_breakpoint") {
				continue
			}
			prefix, _ := json.Marshal(input[:i+1])
			if cache[string(prefix)] {
				cached = len(prefix)
			}
			cache[string(prefix)] = true
		}
		n := len(requests)
		output := []any{
			map[string]any{"type": "reasoning", "id": fmt.Sprintf("rs_%d", n), "encrypted_content": fmt.Sprintf("opaque_%d", n), "summary": []any{}},
			map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", n), "call_id": fmt.Sprintf("call_%d", n), "name": "bash", "arguments": `{ "command": "pwd" }`, "status": "completed"},
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// added may contain incomplete reasoning; only done/final is reusable.
		writeResponsesEvent(w, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "reasoning", "id": "partial", "encrypted_content": "not-final"}})
		for i, item := range output {
			writeResponsesEvent(w, map[string]any{"type": "response.output_item.done", "output_index": i, "item": item})
		}
		writeResponsesEvent(w, map[string]any{"type": "response.completed", "response": map[string]any{
			"status": "completed", "output": output,
			"usage": map[string]any{"input_tokens": lenMustJSON(input), "output_tokens": 20,
				"input_tokens_details": map[string]any{"cached_tokens": cached}, "output_tokens_details": map[string]any{"reasoning_tokens": 15}},
		}})
	}))
	defer server.Close()
	client := NewOpenAIResponsesClient(server.URL, "private-key", time.Second)
	history := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "inspect workspace")}
	previousCached := 0
	for round, tail := range []string{"Monday", "Tuesday", "Tuesday", "Wednesday"} {
		apiMessages, err := BuildAPIMessages(context.Background(), history, BuildInputOptions{})
		if err != nil {
			t.Fatal(err)
		}
		req := NewRequestFromAPIMessages("gpt-6.1-sol", 1000, "medium", "stable system", SanitizeMessagesForProvider(apiMessages), nil)
		req.RuntimeTail, req.PromptCacheRetention = tail, protocol.CacheRetentionLong
		resp, err := client.Call(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if round > 0 && resp.Usage.CacheReadTokens == 0 {
			t.Fatal("append-only prefix failed mock cache lookup")
		}
		if round > 1 && resp.Usage.CacheReadTokens <= previousCached {
			t.Fatal("mock cache never advanced beyond the fixed initial prefix")
		}
		previousCached = resp.Usage.CacheReadTokens
		if resp.Usage.ReasoningTokens != 15 || resp.Usage.OutputTokens != 20 {
			t.Fatalf("usage: %+v", resp.Usage)
		}
		if resp.ResponsesReplay == nil || len(resp.ResponsesReplay.Output) != 2 {
			t.Fatalf("native replay lost: %+v", resp)
		}
		if strings.Contains(string(resp.ResponsesReplay.Output[0]), "not-final") {
			t.Fatal("replayed unfinished reasoning")
		}
		history = append(history, protocol.MessageFromResponse(*resp), protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock(fmt.Sprintf("call_%d", round+1), "workspace")))
		// Exercise persisted/reloaded checkpoints and clone semantics, not just
		// in-memory response pointers.
		stored, _ := json.Marshal(history)
		if err := json.Unmarshal(stored, &history); err != nil {
			t.Fatal(err)
		}
		history = protocol.CloneMessages(history)
		body := requests[round]
		options := body["prompt_cache_options"].(map[string]any)
		if options["mode"] != "explicit" || options["ttl"] != "30m" {
			t.Fatalf("options: %+v", options)
		}
		if _, ok := body["prompt_cache_retention"]; ok {
			t.Fatal("modern GPT received legacy retention")
		}
		if round > 0 {
			previous := requests[round-1]["input"].([]any)
			current := body["input"].([]any)
			if !reflect.DeepEqual(previous, current[:len(previous)]) {
				t.Fatalf("wire prefix mutated on round %d\nold=%+v\nnew=%+v", round, previous, current)
			}
		}
	}
	// Identical date-only tails are not appended on every tool round.
	last, _ := json.Marshal(requests[3]["input"])
	if strings.Count(string(last), responsesRuntimeHeader[:20]) != 3 {
		t.Fatalf("runtime dedup failed: %s", last)
	}
}

func lenMustJSON(value any) int { data, _ := json.Marshal(value); return len(data) }

func TestResponsesReplayScopesAndHistoryEdits(t *testing.T) {
	blocks := []protocol.Block{protocol.TextBlock("answer")}
	replay := &protocol.ResponsesReplay{Origin: "endpoint:model", ContentHash: responsesContentHash(blocks), RuntimeTail: "old date",
		Output: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"secret"}`), json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}],"phase":"final_answer"}`)}}
	for _, tc := range []struct {
		name, origin, text string
		wantReplay         bool
	}{
		{"same", "endpoint:model", "answer", true},
		{"provider switch", "other:model", "answer", false},
		{"model switch", "endpoint:other", "answer", false},
		{"edited history", "endpoint:model", "edited", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, _, count := responsesInputWithReplay(protocol.Request{Messages: []protocol.APIMessage{{Role: protocol.RoleAssistant, Content: []protocol.Block{protocol.TextBlock(tc.text)}, ResponsesReplay: replay}}}, tc.origin)
			data, _ := json.Marshal(items)
			if (count > 0) != tc.wantReplay || strings.Contains(string(data), "secret") != tc.wantReplay {
				t.Fatalf("wrong replay scope: %s", data)
			}
			if tc.wantReplay && !strings.Contains(string(data), `"phase":"final_answer"`) {
				t.Fatal("assistant phase lost")
			}
		})
	}
}

func TestResponsesExplicitCacheCompatibilityFallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		message   string
		wantCalls int
	}{
		{"unsupported options", 400, "Unknown parameter: prompt_cache_options", 3},
		{"unsupported breakpoint", 400, "prompt_cache_breakpoint is not supported", 3},
		{"invalid request", 400, "Invalid tool arguments", 2},
		{"auth failure", 401, "prompt_cache_options unsupported", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, starts := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if _, ok := body["prompt_cache_options"]; ok {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": tc.message, "type": "invalid_request_error"}})
					return
				}
				data, _ := json.Marshal(body)
				if strings.Contains(string(data), "prompt_cache_breakpoint") {
					t.Error("fallback kept breakpoints")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				writeResponsesEvent(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})
			}))
			defer server.Close()
			client := NewOpenAIResponsesClient(server.URL, "key", time.Second)
			for i := 0; i < 2; i++ {
				_, err := client.Stream(context.Background(), protocol.Request{Model: "gpt-6.1-sol", Messages: []protocol.APIMessage{{Role: "user", Content: []protocol.Block{protocol.TextBlock("hi")}}}}, StreamHandler{OnStreamStarted: func() { starts++ }})
				if (err == nil) != (tc.wantCalls == 3) {
					t.Fatalf("unexpected result: %v", err)
				}
			}
			if calls != tc.wantCalls {
				t.Fatalf("calls=%d want=%d", calls, tc.wantCalls)
			}
			if tc.wantCalls == 3 && starts != 2 {
				t.Fatalf("duplicate stream starts: %d", starts)
			}
		})
	}
}

func TestResponsesNeverRetriesStartedStream(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		writeResponsesEvent(w, map[string]any{"type": "response.output_text.delta", "delta": "partial"})
		writeResponsesEvent(w, map[string]any{"type": "error", "code": "unsupported", "message": "prompt_cache_options unsupported"})
	}))
	defer server.Close()
	_, err := NewOpenAIResponsesClient(server.URL, "key", time.Second).Call(context.Background(), protocol.Request{Model: "gpt-6.1-sol", Messages: []protocol.APIMessage{{Role: "user", Content: []protocol.Block{protocol.TextBlock("hi")}}}})
	if err == nil || calls != 1 {
		t.Fatalf("started request retried: calls=%d err=%v", calls, err)
	}
}

func TestResponsesCacheCapabilitiesAndUsageDetails(t *testing.T) {
	for model, want := range map[string]bool{"gpt-6.1-sol": true, "llm-gateway--gpt-6.1-sol": true, "gpt-5.6": true, "gpt-5.5": false, "deepseek-v4.1-flash-1017": false, "custom-gpt-6-proxy": false} {
		if supportsResponsesExplicitCache(model) != want {
			t.Errorf("model capability: %s", model)
		}
	}
	var raw responses.ResponseUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":600,"cache_write_tokens":300},"output_tokens_details":{"reasoning_tokens":150}}`), &raw); err != nil {
		t.Fatal(err)
	}
	usage := codexUsageToProtocol(raw)
	if usage.InputTokens != 100 || usage.CacheReadTokens != 600 || usage.CacheWriteTokens != 300 || usage.OutputTokens != 200 || usage.ReasoningTokens != 150 {
		t.Fatalf("usage details: %+v", usage)
	}
}

func TestResponsesDiagnosticsAreFinalWireFingerprints(t *testing.T) {
	input := openAIResponsesInputFromProtocol(protocol.Request{Messages: []protocol.APIMessage{{Role: "user", Content: []protocol.Block{protocol.TextBlock("private prompt")}}}, RuntimeTail: "secret date"})
	params := responses.ResponseNewParams{Model: "gpt-6.1-sol", Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input}}
	d := responsesRequestDiagnostics(params, "private endpoint", 11, 2, false, true)
	data, _ := json.Marshal(d)
	for _, secret := range []string{"private prompt", "secret date", "private endpoint", "Authorization"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("diagnostics leaked %q", secret)
		}
	}
	if !d.CacheFallback || len(d.InputHashes) != 2 || d.RequestHash == "" || d.ReplayItems != 2 {
		t.Fatalf("missing diagnostics: %+v", d)
	}
}

func TestResponsesDiagnosticsObserveFallbackWire(t *testing.T) {
	var finalWire map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["prompt_cache_options"]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unknown parameter: prompt_cache_options"}}`))
			return
		}
		finalWire = body
		w.Header().Set("Content-Type", "text/event-stream")
		writeResponsesEvent(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})
	}))
	defer server.Close()
	var diagnostics *RequestDiagnostics
	unsub := AddUsageHook(func(_ context.Context, ev UsageEvent) {
		if ev.Context.SessionID == "wire-check" && ev.RequestDiagnostics != nil {
			diagnostics = ev.RequestDiagnostics()
		}
	})
	defer unsub()
	ctx := WithUsageContext(context.Background(), UsageContext{SessionID: "wire-check"})
	_, err := NewOpenAIResponsesClient(server.URL, "secret-key", time.Second).Call(ctx, protocol.Request{Model: "gpt-6.1-sol", Messages: []protocol.APIMessage{{Role: "user", Content: []protocol.Block{protocol.TextBlock("private prompt")}}}})
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(finalWire)
	if diagnostics == nil || !diagnostics.CacheFallback || diagnostics.CacheMode != "server_default" || diagnostics.RequestHash != responsesHash(wire) {
		t.Fatalf("diagnostics did not capture actual fallback request: %+v", diagnostics)
	}
}

func TestResponsesNativeOutputSourcesAndIncompleteFallback(t *testing.T) {
	for _, mode := range []string{"done", "completed-only", "missing-message"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				output := []any{
					map[string]any{"type": "reasoning", "id": "rs", "encrypted_content": "native", "summary": []any{}},
					map[string]any{"type": "message", "id": "msg", "role": "assistant", "phase": "commentary", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "hello"}}},
				}
				if mode != "completed-only" {
					writeResponsesEvent(w, map[string]any{"type": "response.output_text.delta", "delta": "hello"})
					for i, item := range output {
						if mode == "missing-message" && i == 1 {
							continue
						}
						writeResponsesEvent(w, map[string]any{"type": "response.output_item.done", "output_index": i, "item": item})
					}
				}
				final := map[string]any{"status": "completed"}
				if mode == "completed-only" {
					final["output"] = output
				}
				writeResponsesEvent(w, map[string]any{"type": "response.completed", "response": final})
			}))
			defer server.Close()
			client := NewOpenAIResponsesClient(server.URL, "key", time.Second)
			resp, err := client.Call(context.Background(), protocol.Request{Model: "gpt-5.4", RuntimeTail: "date"})
			if err != nil {
				t.Fatal(err)
			}
			if protocol.BlocksText(resp.Content) != "hello" || len(protocol.ToolUses(resp.Content)) != 0 {
				t.Fatalf("native output corrupted visible reply: %+v", resp)
			}
			want := 2
			if mode == "missing-message" {
				want = 0
			}
			if resp.ResponsesReplay == nil || len(resp.ResponsesReplay.Output) != want || resp.ResponsesReplay.RuntimeTail != "date" {
				t.Fatalf("partial native output must fall back without losing runtime: %+v", resp.ResponsesReplay)
			}
		})
	}
}

func TestResponsesNonOpenAIKeepsLegacyHistoryShape(t *testing.T) {
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		data, _ := json.Marshal(body)
		bodies = append(bodies, data)
		if _, ok := body["prompt_cache_options"]; ok {
			t.Error("DeepSeek received OpenAI cache options")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeResponsesEvent(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "hello"}}},
		}}})
	}))
	defer server.Close()
	history := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "question")}
	client := NewOpenAIResponsesClient(server.URL, "key", time.Second)
	for i := 0; i < 2; i++ {
		resp, err := client.Call(context.Background(), protocol.Request{Model: "deepseek-v4.1-flash-1017", Messages: protocol.ToAPIMessages(history), RuntimeTail: "date"})
		if err != nil {
			t.Fatal(err)
		}
		if resp.ResponsesReplay != nil {
			t.Fatal("DeepSeek acquired OpenAI-only replay metadata")
		}
		history = append(history, protocol.MessageFromResponse(*resp))
	}
	if strings.Count(string(bodies[1]), responsesRuntimeHeader[:20]) != 1 {
		t.Fatal("DeepSeek history gained persisted runtime input")
	}
}
