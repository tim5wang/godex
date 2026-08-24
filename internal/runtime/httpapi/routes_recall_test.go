package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/services/usage"
)

// TestRecallFromHTTPFetchesChunks verifies the external provider contract:
// POST {url}/retrieve → chunks rendered as a marked knowledge block.
func TestRecallFromHTTPFetchesChunks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/retrieve") {
			t.Fatalf("expected /retrieve path, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			t.Fatalf("expected auth header, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chunks":[{"id":"c1","title":"doc-a","content":"content of doc a","source":"kb"}]}`))
	}))
	defer srv.Close()

	ref := &usage.ProviderRef{Name: "sales", URL: srv.URL, TokenRef: "tok-123"}
	chunks, err := recallFromHTTP(context.Background(), ref, "orders")
	if err != nil {
		t.Fatalf("recallFromHTTP: %v", err)
	}
	if len(chunks) != 1 || chunks[0].ID != "c1" || chunks[0].Content != "content of doc a" {
		t.Fatalf("unexpected chunks: %+v", chunks)
	}
}

// TestRecallStepFormatsMarkedBlock verifies the rendered block carries the
// knowledge-reference markers and provider name.
func TestRecallStepFormatsMarkedBlock(t *testing.T) {
	got := formatRecallChunks("sales_crm", []recallChunk{
		{ID: "c1", Title: "doc-a", Content: "line1\nline2", Source: "kb"},
	})
	for _, want := range []string{"## sales_crm", "- doc-a", "line1", "line2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("block missing %q:\n%s", want, got)
		}
	}
}

// TestRecallStepSkipsUnboundProvider verifies a provider not bound to the key
// is skipped gracefully (step still proceeds).
func TestRecallStepSkipsUnboundProvider(t *testing.T) {
	key := &usage.BizAPIKey{
		Providers: []usage.ProviderRef{{Name: "sales", URL: "http://127.0.0.1:1"}},
	}
	got := recallStep(context.Background(), nil, key, []string{"unknown_provider"}, "q")
	if got != "" {
		t.Fatalf("expected empty block for unbound provider, got %q", got)
	}
}

// TestRecallStepRendersChunksFromHTTPProvider is an end-to-end check that
// recallStep renders external provider output into the prompt block.
func TestRecallStepRendersChunksFromHTTPProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chunks":[{"id":"c1","title":"kb-a","content":"kb content"}]}`))
	}))
	defer srv.Close()

	key := &usage.BizAPIKey{
		Providers: []usage.ProviderRef{{Name: "sales", URL: srv.URL}},
	}
	got := recallStep(context.Background(), nil, key, []string{"sales"}, "orders")
	if !strings.Contains(got, "知识库参考") || !strings.Contains(got, "kb content") {
		t.Fatalf("expected recall block with content, got %q", got)
	}
}