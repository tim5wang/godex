package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/memory"
)

func TestMemoryToolReplacesFactAndMarksSupersededMemory(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	old, err := a.memoryMgr.Remember(memory.SaveInput{Title: "OCR procedure", Summary: "OCR with tesseract", Content: "Use tesseract", Type: memory.TypeWorkflow})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "update", "id_or_title": old.ID, "summary": "OCR with Vision", "content": "Use Vision"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := a.memoryMgr.Get(old.ID)
	if err != nil || record.Content != "Use Vision" || strings.Contains(record.Summary, "tesseract") {
		t.Fatalf("old fact survived update: %+v %v", record, err)
	}
	_, err = a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "remember", "title": "Current OCR rule", "summary": "Use Vision for OCR", "content": "Use Vision for OCR", "memory_type": "workflow"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := a.memoryMgr.Get("Current OCR rule")
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "update", "id_or_title": old.ID, "superseded_by": current.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "update", "id_or_title": current.ID, "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := a.memoryMgr.FindRelevant("OCR", 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("invalid memory recalled: %+v %v", rows, err)
	}
	_, err = a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "update", "id_or_title": current.ID, "expires_at": ""})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = a.memoryMgr.FindRelevant("OCR", 10)
	if err != nil || len(rows) != 1 || rows[0].ID != current.ID {
		t.Fatalf("expiry not cleared: %+v %v", rows, err)
	}
	_, err = a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "update", "id_or_title": current.ID, "recall_policy": "manual"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = a.memoryMgr.FindRelevant("OCR", 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("manual policy ignored: %+v %v", rows, err)
	}
	if _, err = a.memoryMgr.Archive(memory.ForgetInput{File: current.File}); err != nil {
		t.Fatal(err)
	}
	result, err := a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "search", "query": "OCR", "status": "all"})
	if err != nil || !strings.Contains(result, current.ID) {
		t.Fatalf("archived history inaccessible: %s %v", result, err)
	}
}
