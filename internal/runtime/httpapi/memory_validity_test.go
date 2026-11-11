package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

func TestMemoryValidityEndpoints(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &dummyCaller{}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()
	post := func(route string, payload map[string]any) memory.Entry {
		t.Helper()
		resp := doJSONWithToken(t, http.MethodPost, server.URL+route, payload, cfg.WebToken)
		defer resp.Body.Close()
		var entry memory.Entry
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d", route, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
			t.Fatal(err)
		}
		return entry
	}
	old := post("/memory/remember", map[string]any{"title": "Old OCR", "summary": "OCR old rule", "content": "old OCR command", "memory_type": "workflow"})
	current := post("/memory/remember", map[string]any{"title": "Current OCR", "summary": "OCR new rule", "content": "new OCR command", "memory_type": "workflow", "recall_policy": "manual"})
	if current.RecallPolicy != "manual" {
		t.Fatalf("recall policy lost: %+v", current)
	}
	updated := post("/memory/update", map[string]any{"match_file": old.File, "title": old.Title, "summary": old.Summary, "content": "old OCR command", "memory_type": "workflow", "superseded_by": current.ID})
	if updated.SupersededBy != current.ID {
		t.Fatalf("replacement relation lost: %+v", updated)
	}
	post("/memory/update", map[string]any{"match_file": current.File, "title": current.Title, "summary": current.Summary, "content": "new OCR command", "memory_type": "workflow", "expires_at": time.Now().Add(-time.Hour)})
	rows, err := service.ListMemory(context.Background(), memory.SearchOptions{Query: "OCR"})
	if err != nil || len(rows) != 0 {
		t.Fatalf("invalid memories recalled: %+v %v", rows, err)
	}
	rows, err = service.ListMemory(context.Background(), memory.SearchOptions{Status: memory.StatusAll})
	if err != nil || len(rows) != 2 {
		t.Fatalf("management history lost: %+v %v", rows, err)
	}
}
