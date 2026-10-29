package conversation

import (
	"context"
	"encoding/json"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpenAIResponsesShortCacheUsesServerDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if value, exists := body["prompt_cache_retention"]; exists {
			t.Errorf("short retention should use server default, got %v", value)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()
	client := NewOpenAIResponsesClient(server.URL, "test", time.Second*5)
	if _, err := client.Stream(context.Background(), protocol.Request{Model: "test", PromptCacheRetention: protocol.CacheRetentionShort}, StreamHandler{}); err != nil {
		t.Fatal(err)
	}
}
