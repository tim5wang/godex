package conversation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/responses"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/media"
)

func TestResponsesInputPreservesImageOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		blocks []protocol.Block
		want   []string
	}{
		{"mixed", []protocol.Block{protocol.TextBlock("before"), protocol.ImageBlock("image/png", "aGVsbG8="), protocol.TextBlock("between"), protocol.ImageBlock("image/jpeg", "d29ybGQ="), protocol.TextBlock("after")}, []string{"before", "data:image/png;base64,aGVsbG8=", "between", "data:image/jpeg;base64,d29ybGQ=", "after"}},
		{"image only", []protocol.Block{protocol.ImageBlock("image/png", "aGVsbG8=")}, []string{"data:image/png;base64,aGVsbG8="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := codexInputFromProtocol(protocol.Request{Messages: []protocol.APIMessage{{Role: protocol.RoleUser, Content: tc.blocks}}})
			body := decodeResponsesImageWire(t, items)
			if len(body) != 1 {
				t.Fatalf("expected one message, got %d", len(body))
			}
			parts, ok := body[0]["content"].([]any)
			if !ok || len(parts) != len(tc.want) {
				t.Fatalf("expected %d ordered content parts, got %+v", len(tc.want), body[0]["content"])
			}
			for i, value := range tc.want {
				part := parts[i].(map[string]any)
				if strings.HasPrefix(value, "data:") {
					if part["type"] != "input_image" || part["image_url"] != value || part["detail"] != "auto" {
						t.Fatalf("image %d corrupted: %+v", i, part)
					}
				} else if part["type"] != "input_text" || part["text"] != value {
					t.Fatalf("text %d reordered: %+v", i, part)
				}
			}
		})
	}
	// Keep the legacy string shape for text-only requests.
	items := codexInputFromProtocol(protocol.Request{Messages: []protocol.APIMessage{{Role: protocol.RoleUser, Content: []protocol.Block{protocol.TextBlock("hello"), protocol.TextBlock("world")}}}})
	if got := decodeResponsesImageWire(t, items)[0]["content"]; got != "hello\nworld" {
		t.Fatalf("text-only wire changed: %+v", got)
	}
}

func TestResponsesImageCacheIsImmutable(t *testing.T) {
	for _, trailingText := range []bool{false, true} {
		t.Run(fmt.Sprintf("trailing_text=%t", trailingText), func(t *testing.T) {
			blocks := []protocol.Block{protocol.TextBlock("look"), protocol.ImageBlock("image/png", "aGVsbG8=")}
			if trailingText {
				blocks = append(blocks, protocol.TextBlock("answer briefly"))
			}
			items := openAIResponsesInputFromProtocol(protocol.Request{Messages: []protocol.APIMessage{{Role: protocol.RoleUser, Content: blocks}}, RuntimeTail: "date"})
			before, _ := json.Marshal(items)
			marked, count := responsesCacheInput(items)
			if count != 1 {
				t.Fatalf("expected one message boundary, got %d", count)
			}
			wire := decodeResponsesImageWire(t, marked)
			parts := wire[0]["content"].([]any)
			if len(parts) != len(blocks) || parts[1].(map[string]any)["image_url"] != "data:image/png;base64,aGVsbG8=" {
				t.Fatalf("cache marking dropped image content: %+v", parts)
			}
			for i, part := range parts {
				_, hasMarker := part.(map[string]any)["prompt_cache_breakpoint"]
				if hasMarker != (i == len(parts)-1) {
					t.Fatalf("cache boundary not at end of complete message: %+v", parts)
				}
			}
			after, _ := json.Marshal(items)
			if string(before) != string(after) {
				t.Fatal("cache markers mutated the unmarked fallback input")
			}
			again, _ := responsesCacheInput(items)
			if !reflect.DeepEqual(decodeResponsesImageWire(t, marked), decodeResponsesImageWire(t, again)) {
				t.Fatal("multimodal cache marking is not stable")
			}
			if !reflect.DeepEqual(wire[1], decodeResponsesImageWire(t, items)[1]) {
				t.Fatal("runtime tail was changed or marked")
			}
		})
	}
}

func TestResponsesImageAttachmentsReachWireAcrossToolRounds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		model    string
		codex    bool
		official bool
		fallback bool
	}{
		{name: "responses", model: "gpt-6.1-sol"},
		{name: "responses cache fallback", model: "gpt-6.1-sol", fallback: true},
		{name: "responses legacy", model: "gpt-5.5"},
		{name: "codex official", model: "gpt-6.1-sol", codex: true, official: true},
		{name: "codex oauth", model: "gpt-6.1-sol", codex: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// Synthetic image bytes: never copy private session attachments into tests.
			attachments := make([]protocol.Attachment, 2)
			imageURLs := make([]string, len(attachments))
			parts := []protocol.ContentPart{{Type: "text", Text: "compare screenshots"}}
			for i := range attachments {
				picture := image.NewRGBA(image.Rect(0, 0, 1, 1))
				picture.Set(0, 0, color.RGBA{R: uint8(40 + i*100), G: 80, B: 120, A: 255})
				var encoded bytes.Buffer
				if err := png.Encode(&encoded, picture); err != nil {
					t.Fatal(err)
				}
				imageURLs[i] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
				name := fmt.Sprintf("screenshot-%d.png", i)
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
				attachments[i] = protocol.Attachment{ID: name, Name: name, MIMEType: "image/png", Path: path}
				parts = append(parts, protocol.ContentPart{Type: "attachment", Attachment: &attachments[i]})
			}
			history := []protocol.Message{{Role: protocol.RoleUser, Metadata: &protocol.Metadata{Text: "compare screenshots", Parts: parts, Attachments: attachments}}}
			processor := media.NewProcessor(config.MediaConfig{OCR: config.OCRMediaConfig{Mode: "disabled"}}, dir, dir, dir)
			var requests []map[string]any
			var accepted []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests = append(requests, body)
				if tc.fallback && body["prompt_cache_options"] != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"message":"Unknown parameter: prompt_cache_options"}}`))
					return
				}
				accepted = append(accepted, body)
				output := []any{map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", len(accepted)), "call_id": fmt.Sprintf("call_%d", len(accepted)), "name": "read_file", "arguments": `{"path":"README.md"}`, "status": "completed"}}
				w.Header().Set("Content-Type", "text/event-stream")
				writeResponsesEvent(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output}})
			}))
			defer server.Close()
			var client Caller = NewOpenAIResponsesClient(server.URL, "test-key", time.Second)
			if tc.codex {
				client = NewOpenAICodexClientForEndpoint(server.URL, "test-key", time.Second, tc.official)
			}
			for round := 0; round < 2; round++ {
				messages, err := BuildAPIMessages(context.Background(), history, BuildInputOptions{SessionID: "images-test", SupportsImage: true, Processor: processor})
				if err != nil {
					t.Fatal(err)
				}
				req := NewRequestFromAPIMessages(tc.model, 1000, "medium", "stable system", SanitizeMessagesForProvider(messages), nil)
				req.RuntimeTail = fmt.Sprintf("date-%d", round)
				resp, err := client.Call(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				history = append(history, protocol.MessageFromResponse(*resp), protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock(fmt.Sprintf("call_%d", round+1), "file contents")))
				stored, _ := json.Marshal(history)
				if err := json.Unmarshal(stored, &history); err != nil {
					t.Fatal(err)
				}
			}
			wantCalls := 2
			if tc.fallback {
				wantCalls++
			}
			if len(requests) != wantCalls || len(accepted) != 2 {
				t.Fatalf("unexpected requests=%d accepted=%d", len(requests), len(accepted))
			}
			for i, body := range requests {
				input := body["input"].([]any)
				first := input[0].(map[string]any)
				content, ok := first["content"].([]any)
				if !ok || len(content) != 4 {
					t.Fatalf("request %d lost attachment parts: %+v", i, first)
				}
				for imageIndex, index := range []int{1, 3} {
					part := content[index].(map[string]any)
					if part["type"] != "input_image" || part["image_url"] != imageURLs[imageIndex] {
						t.Fatalf("request %d image %d missing/corrupted", i, index)
					}
				}
				wire, _ := json.Marshal(body)
				if strings.Contains(string(wire), "[image attachment") {
					t.Fatal("image downgraded to placeholder")
				}
				_, marked := content[3].(map[string]any)["prompt_cache_breakpoint"]
				wantMarked := tc.model == "gpt-6.1-sol" && (!tc.codex || tc.official) && (!tc.fallback || i == 0)
				if marked != wantMarked {
					t.Fatalf("request %d cache boundary=%t want=%t", i, marked, wantMarked)
				}
				if tc.fallback && i > 0 && strings.Contains(string(wire), "prompt_cache_breakpoint") {
					t.Fatal("fallback retained cache markers")
				}
			}
			previous, current := accepted[0]["input"].([]any), accepted[1]["input"].([]any)
			if len(current) < len(previous) || !reflect.DeepEqual(previous, current[:len(previous)]) {
				t.Fatal("multimodal history prefix changed after persistence/tool round")
			}
		})
	}
}

func decodeResponsesImageWire(t *testing.T, items responses.ResponseInputParam) []map[string]any {
	t.Helper()
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var result []map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
