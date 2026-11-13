package backend

import (
	"context"
	"testing"

	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/workcontext"
)

func TestContextInspectorShowsScopedRecallEvidenceAndExclusions(t *testing.T) {
	s := newTestService(newTestConfig(t), &stubCaller{})
	c := createDesktopTest(t, s, "diagnostic")
	f := confirmedDesktopFact(t, s, c.ID)
	session := linkDesktopTest(t, s, c.ID, "diagnostic")
	session.agent.AddMessage("如何使用 Vision OCR")
	store, _ := s.WorkContexts()
	before, _ := store.MemoryState(context.Background(), desktopTestScope(), c.ID)
	preview, err := s.ContextInspector(desktopOwnerContext(), session.id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range preview.RecallDiagnostics {
		if d.Layer != "desktop" {
			continue
		}
		found = true
		if d.Intent != "method" || d.Returned != 1 || len(d.Decisions) == 0 || d.Decisions[0].ID != f.ID || d.Decisions[0].Source != f.Source || d.Decisions[0].Version != f.Version {
			t.Fatal(d)
		}
	}
	if !found || preview.RecallMode != "direct" {
		t.Fatal("desktop diagnostic missing", preview)
	}
	after, _ := store.MemoryState(context.Background(), desktopTestScope(), c.ID)
	if after.SourceSeq != before.SourceSeq {
		t.Fatal("preview dirtied Dream")
	}
	if _, err = store.ReviewFact(context.Background(), desktopTestScope(), c.ID, f.ID, workcontext.FactReview{Status: "stale", IfVersion: f.Version}); err != nil {
		t.Fatal(err)
	}
	preview, err = s.ContextInspector(desktopOwnerContext(), session.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range preview.RecallDiagnostics {
		if d.Layer == "desktop" && (d.Returned != 0 || d.Eligibility["stale"] != 1 || len(d.Decisions) != 0) {
			t.Fatal("inactive evidence was exposed as a candidate", d)
		}
	}
	other := memory.WithOwner(context.Background(), memory.Owner{TenantID: "tenant", UserID: "bob"})
	if _, err = s.ContextInspector(other, session.id); err == nil {
		t.Fatal("diagnostics crossed session owners")
	}
}
