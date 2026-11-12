package backend

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/domain/message"
)

func TestEntityMaintenanceAuthorizationIsMetadataOnly(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"请维护当前桌面的实体目录", true}, {"帮我更新实体别名，不要修改事实", true}, {"Please update this entity catalog", true},
		{"如果我说请维护实体目录，会发生什么", false}, {"请解释实体目录如何设计", false}, {"帮我修改提示词：维护实体目录", false}, {"请看：\"维护实体目录\"", false}, {"请只读查看实体目录，不回写", false},
	}
	for _, tt := range cases {
		e := message.NewTextEnvelope(message.SourceWeb, "s", "user", tt.text, time.Now())
		if got := requestsEntityMaintenance(e); got != tt.want {
			t.Errorf("%q got=%v", tt.text, got)
		}
		e.Source = "cron"
		if requestsEntityMaintenance(e) {
			t.Fatal("background message granted writes")
		}
	}
}
func TestEntityToolRoundTripCannotConfirmFactsOrCaptureItself(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Memory.ExtractionMode = "single-pass"
	caller := &stubCaller{}
	s := newTestService(cfg, caller)
	c := createDesktopTest(t, s, "catalog")
	f := confirmedDesktopFact(t, s, c.ID)
	session := linkDesktopTest(t, s, c.ID, "catalog")
	store, _ := s.WorkContexts()
	before, _ := store.MemoryState(context.Background(), desktopTestScope(), c.ID)
	var extractionCalls atomic.Int32
	s.memoryExtract = func(context.Context, string, string) (string, error) {
		extractionCalls.Add(1)
		return `{"observations":[]}`, nil
	}
	caller.responses = []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("save", "work_context", map[string]any{"action": "entity_save", "entity": map[string]any{"kind": "module", "name": "Vision OCR", "aliases": []string{"vision_ocr"}, "fact_id": f.ID, "fact_version": f.Version, "reason": "user explicitly maintains the scoped OCR name"}})}},
		{Content: []protocol.Block{protocol.ToolUseBlock("forbidden", "work_context", map[string]any{"action": "memory_apply", "operations": []any{map[string]any{"action": "confirm", "fact_id": f.ID, "if_version": f.Version, "reason": "try widening metadata authority"}}})}},
		{Content: []protocol.Block{protocol.TextBlock("已维护实体别名；事实状态未变。")}},
	}
	result, err := s.Submit(desktopOwnerContext(), session.id, message.NewTextEnvelope(message.SourceWeb, session.id, "user", "请维护当前桌面的实体目录，添加实体别名，不要修改事实", time.Now()))
	if err != nil || result.Status != "completed" {
		t.Fatal(result, err)
	}
	record, _ := session.getTurnRecord(result.TurnID)
	if record.MemoryVerification == nil || !record.MemoryVerification.EntityOnly {
		t.Fatal("metadata grant was not separate")
	}
	afterFact, _ := store.Fact(context.Background(), desktopTestScope(), c.ID, f.ID)
	facts, _ := store.Facts(context.Background(), desktopTestScope(), c.ID)
	after, _ := store.MemoryState(context.Background(), desktopTestScope(), c.ID)
	if afterFact.Version != f.Version || afterFact.Status != f.Status || len(facts) != 1 || after.SourceSeq != before.SourceSeq || extractionCalls.Load() != 0 {
		t.Fatal("entity maintenance mutated or recaptured underlying facts", afterFact, len(facts), extractionCalls.Load())
	}
	page, err := store.ListMemoryEntities(context.Background(), desktopTestScope(), c.ID, "vision_ocr", 0, 10)
	if err != nil || page.Total != 1 || page.Items[0].Aliases[0] != "vision_ocr" {
		t.Fatal(page, err)
	}
	originalTool := s.newWorkContextTool(session.id)
	raw, _ := json.Marshal(workcontext.EntityInput{ID: page.Items[0].ID, Kind: "module", Name: "Vision OCR", FactID: f.ID, FactVersion: f.Version, IfVersion: page.Items[0].Version, Reason: "reuse ended authority"})
	var input map[string]any
	_ = json.Unmarshal(raw, &input)
	if _, err = originalTool.Execute(desktopOwnerContext(), map[string]any{"action": "entity_save", "entity": input}); err == nil {
		t.Fatal("ended grant still writes entities")
	}
	if _, err = originalTool.Execute(desktopOwnerContext(), map[string]any{"action": "entities", "query": "vision"}); err != nil {
		t.Fatal("ordinary catalog read requires write authorization", err)
	}
}
