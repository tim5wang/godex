package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestRecallChineseTaskRanksDocumentOverGenericWarnings(t *testing.T) {
	m := NewManager(t.TempDir())
	doc, err := m.Remember(SaveInput{Title: "GoDex 文档索引", Summary: "文档索引及各文档的用途", Content: "Source document: docs/docs.md\nGoDex 文档索引，工程文档入口", Type: TypeProject, Source: "docs/docs.md"})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []SaveInput{
		{Title: "GoDex provider migration", Summary: "Agent provider 切换流程", Content: "Agent 使用 provider 路由；迁移模型配置", Type: TypeProject},
		{Title: "GoDex 服务重启警示", Summary: "Agent 不自动重启服务", Content: "维护 GoDex Agent 服务时遵守启动规则", Type: TypeWarning},
	} {
		if _, err = m.Remember(input); err != nil {
			t.Fatal(err)
		}
	}
	query := "不要调用任何工具，仅根据本次注入的上下文，用中文输出四行：Agent 应使用的临时草稿目录、当前本地时刻（保留已有精度）、时区、GoDex 文档索引的文件位置。缺少的信息写“未提供”，不要猜测。"
	rows, err := m.FindRelevant(query, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != doc.ID {
		t.Fatalf("noisy/misranked recall: %+v", rows)
	}
	generic, err := m.FindRelevant("GoDex Agent 用中文回复", 5)
	if err != nil || len(generic) != 0 {
		t.Fatalf("generic instructions recalled work facts: %+v %v", generic, err)
	}
}

func TestConsolidatorUnchangedWatermarkSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if err := m.writeCandidates([]Candidate{newCandidate("one", "summary", "content", TypeProject, "test")}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	oneShot := func(context.Context, string, string) (string, error) { calls++; return "NONE", nil }
	for i := 0; i < 3; i++ {
		c := NewConsolidator(ConsolidatorOptions{Manager: NewManager(dir), AfterN: 1, OneShot: oneShot})
		if err := c.MaybeMaintain(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("unchanged inbox invoked model %d times", calls)
	}
}

func TestConsolidatorPreservesCandidateArrivingDuringCall(t *testing.T) {
	m := NewManager(t.TempDir())
	original := newCandidate("one", "first", "first", TypeProject, "test")
	if err := m.writeCandidates([]Candidate{original}); err != nil {
		t.Fatal(err)
	}
	c := NewConsolidator(ConsolidatorOptions{Manager: m, AfterN: 1, OneShot: func(context.Context, string, string) (string, error) {
		_, err := NewExtractor(NewManager(m.dir), "").ProposeCandidate(Candidate{Title: "concurrent", Summary: "concurrent", Content: "a new observation", Type: TypeProject, Source: "user"})
		return "DELETE 1", err
	}})
	if err := c.MaybeMaintain(context.Background()); err == nil {
		t.Fatal("concurrent snapshot should conflict")
	}
	rows, err := m.ListCandidates()
	if err != nil || len(rows) != 2 {
		t.Fatalf("candidate lost: %+v %v", rows, err)
	}
}

func TestOriginRoundTripAndNewEvidenceVersionCanBePromoted(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	origin := &Origin{ContextID: "desktop-A", FactID: "fact-1", FactVersion: 2, Applicability: `{"os":"darwin"}`, SourceRevision: "sha256:abc", ObservedAt: "2026-11-12T01:00:00Z", VerifiedAt: "2026-11-12T02:00:00Z", ValidUntil: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	extractor := NewExtractor(m, "")
	proposal := Candidate{Title: "Vision OCR", Summary: "Vision OCR 方法", Content: "在该 mac 节点使用 Vision", Type: TypeWorkMethod, Source: "workcontext://desktop-A/fact-1", Origin: origin}
	added, err := extractor.ProposeCandidate(proposal)
	if err != nil || len(added) != 1 {
		t.Fatalf("propose: %+v %v", added, err)
	}
	entry, err := m.AcceptCandidate(added[0].Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := NewManager(dir).Get(entry.ID)
	if err != nil || !reflect.DeepEqual(stored.Origin, origin) {
		t.Fatalf("markdown provenance lost: %+v %v", stored, err)
	}
	rows, err := NewManager(dir).Search(SearchOptions{Query: "Vision"})
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0].Origin, origin) {
		t.Fatalf("sidecar provenance lost: %+v %v", rows, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, IndexFileName))
	if err != nil || len(raw) == 0 {
		t.Fatal("index missing", err)
	}
	revised := *origin
	revised.FactVersion++
	revised.SourceRevision = "sha256:new"
	proposal.Origin = &revised
	proposal.Content = "修正后的 Vision 调用方法"
	added, err = extractor.ProposeCandidate(proposal)
	if err != nil || len(added) != 1 {
		t.Fatalf("same title blocked source correction: %+v %v", added, err)
	}
	updated, err := m.AcceptCandidate(added[0].Fingerprint)
	if err != nil || updated.ID != entry.ID || updated.Origin.FactVersion != 3 {
		t.Fatalf("lineage split: %+v %v", updated, err)
	}
	history, _ := m.Search(SearchOptions{Status: StatusAll})
	if len(history) != 1 {
		t.Fatalf("lineage duplicate: %+v", history)
	}
}

func TestConcurrentMemoryWritesAndReferenceReceiptsPreserveAllEntries(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	seed, err := m.Remember(SaveInput{Title: "seed", Content: "persistent evidence", Summary: "seed evidence", Type: TypeWorkflow})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			manager := NewManager(dir)
			manager.markReferenced(map[string]struct{}{seed.ID: {}})
			_, err := manager.Remember(SaveInput{Title: fmt.Sprintf("entry-%d", i), Content: fmt.Sprintf("evidence %d", i), Summary: fmt.Sprintf("summary %d", i), Type: TypeWorkFact})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := m.List()
	if err != nil || len(rows) != 17 {
		t.Fatalf("lost concurrent write: %d %v", len(rows), err)
	}
}

func TestLegacyImportDoesNotExposeWorkOrOtherOwner(t *testing.T) {
	base := t.TempDir()
	legacy := NewManager(base)
	preference, _ := legacy.Remember(SaveInput{Title: "Reply in Chinese", Content: "使用中文", Summary: "中文回复", Type: TypeUser})
	work, _ := legacy.Remember(SaveInput{Title: "old project", Content: "legacy work evidence", Summary: "legacy work", Type: TypeWorkFact})
	alice := Owner{UserID: "local-operator"}
	target := PersonalManager(base, alice)
	if err := ImportLegacyPreferences(base, alice, target); err != nil {
		t.Fatal(err)
	}
	rows, err := target.List()
	if err != nil || len(rows) != 1 || rows[0].ID != preference.ID {
		t.Fatalf("legacy scope: %+v %v", rows, err)
	}
	bob := Owner{UserID: "bob"}
	other := PersonalManager(base, bob)
	if err := ImportLegacyPreferences(base, bob, other); err != nil {
		t.Fatal(err)
	}
	rows, _ = other.List()
	if len(rows) != 0 {
		t.Fatal("other owner inherited legacy records")
	}
	if _, err := legacy.Get(work.ID); err != nil {
		t.Fatal("legacy work was deleted", err)
	}
}

func TestPromotionKeepsDistinctLineageWhenSameTitleIsRevised(t *testing.T) {
	m := NewManager(t.TempDir())
	extractor := NewExtractor(m, "")
	var ids []string
	for _, v := range []struct {
		desktop string
		version int
	}{{"A", 1}, {"B", 1}, {"B", 2}} {
		c := Candidate{Title: "OCR method", Summary: "scoped method", Content: fmt.Sprintf("evidence from %s v%d", v.desktop, v.version), Type: TypeWorkMethod, Source: "workcontext://" + v.desktop, Origin: &Origin{ContextID: v.desktop, FactID: "fact-" + v.desktop, FactVersion: v.version, Applicability: `{"os":"darwin"}`}}
		added, err := extractor.ProposeCandidate(c)
		if err != nil || len(added) != 1 {
			t.Fatalf("proposal: %+v %v", added, err)
		}
		entry, err := m.AcceptCandidate(added[0].Fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, entry.ID)
	}
	if ids[0] == ids[1] || ids[1] != ids[2] {
		t.Fatalf("evidence lineage merged or split: %+v", ids)
	}
	rows, _ := m.List()
	if len(rows) != 2 {
		t.Fatal("same-title origins were not preserved")
	}
}

func TestRecallDoesNotMatchArithmeticAgainstOpaqueSourceIDs(t *testing.T) {
	m := NewManager(t.TempDir())
	for i, source := range []string{"workcontext://aaa17bbb19ccccdd/facts/ddd19fff17bbbbee?version=17", "session://legacy-project-17", "report://12345678-abcd-4abc-9def-123456789019"} {
		_, err := m.Remember(SaveInput{Title: fmt.Sprintf("独立文档 %d", i), Summary: "文档目录入口", Content: "工程文档索引", Type: TypeWorkFact, Source: source})
		if err != nil {
			t.Fatal(err)
		}
	}
	query := "不要调用工具，计算 17*19，只输出结果。"
	for _, search := range []func(SearchOptions) ([]StoredMemory, error){m.searchWithSidecar, m.searchLinear} {
		rows, err := search(SearchOptions{Query: query, Automatic: true})
		if err != nil || len(rows) != 0 {
			t.Fatalf("arithmetic recalled source identifiers: %+v %v", rows, err)
		}
	}
}
