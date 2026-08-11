package memory

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/compress"
	_ "modernc.org/sqlite"
)

func TestForgetRemovesMemoryByTitle(t *testing.T) {
	manager := NewManager(t.TempDir())

	if _, err := manager.Remember(SaveInput{
		Title:   "Repo Convention",
		Summary: "Keep tests green before commit.",
		Content: "Run go test ./... before every commit.",
		Type:    TypeProject,
	}); err != nil {
		t.Fatalf("remember memory: %v", err)
	}

	entry, err := manager.Forget(ForgetInput{Title: "Repo Convention"})
	if err != nil {
		t.Fatalf("forget memory: %v", err)
	}
	if entry.Title != "Repo Convention" {
		t.Fatalf("expected forgotten entry title, got %+v", entry)
	}

	entries, err := manager.List()
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no remaining memories, got %+v", entries)
	}

	if _, err := os.Stat(filepath.Join(manager.Dir(), "repo_convention.md")); !os.IsNotExist(err) {
		t.Fatalf("expected memory file removal, got %v", err)
	}
}

func TestMemoryAuditLogAndRestore(t *testing.T) {
	dir := t.TempDir()
	manager := NewManager(dir)

	entry, err := manager.Remember(SaveInput{
		Title:   "Delivery Rule",
		Summary: "Prefer explicit delivery confirmations.",
		Content: "Initial durable memory body.",
		Type:    TypeProject,
		Source:  "test",
	})
	if err != nil {
		t.Fatalf("remember memory: %v", err)
	}
	if _, err := manager.Update(UpdateInput{
		Match:   ForgetInput{Title: "Delivery Rule"},
		Title:   "Delivery Rule",
		Summary: "Prefer explicit delivery confirmations.",
		Content: "Updated durable memory body.",
		Type:    TypeProject,
		Source:  "test",
	}); err != nil {
		t.Fatalf("update memory: %v", err)
	}

	log, err := manager.ListAudit(10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(log) < 2 {
		t.Fatalf("expected remember and update audit entries, got %+v", log)
	}
	var update AuditLogEntry
	for _, item := range log {
		if item.Action == AuditUpdate && item.MemoryID == entry.ID {
			update = item
			break
		}
	}
	if update.ID == "" || update.Before == nil || update.After == nil {
		t.Fatalf("expected update audit snapshots, got %+v", update)
	}
	if update.Before.Content != "Initial durable memory body." || update.After.Content != "Updated durable memory body." {
		t.Fatalf("unexpected audit before/after content: %+v", update)
	}

	if _, err := manager.RestoreAudit(update.ID, "before"); err != nil {
		t.Fatalf("restore audit: %v", err)
	}
	restored, err := manager.Get(entry.ID)
	if err != nil {
		t.Fatalf("get restored memory: %v", err)
	}
	if restored.Content != "Initial durable memory body." {
		t.Fatalf("expected restored content, got %q", restored.Content)
	}

	log, err = manager.ListAudit(1)
	if err != nil {
		t.Fatalf("list audit after restore: %v", err)
	}
	if len(log) != 1 || log[0].Action != AuditRestore {
		t.Fatalf("expected restore audit entry, got %+v", log)
	}
}

func TestBuildPromptSectionMentionsForgetMemory(t *testing.T) {
	manager := NewManager(t.TempDir())

	section, err := manager.BuildPromptSection()
	if err != nil {
		t.Fatalf("build prompt section: %v", err)
	}
	if !strings.Contains(section, "forget") {
		t.Fatalf("expected prompt section to mention forget, got %q", section)
	}
}

func TestListMigratesLegacyIndexToMachineIndex(t *testing.T) {
	dir := t.TempDir()
	legacyFile := filepath.Join(dir, "repo_convention.md")
	if err := os.WriteFile(filepath.Join(dir, EntrypointName), []byte("- [Repo Convention](repo_convention.md) - project - Keep tests green before commit.\n"), 0644); err != nil {
		t.Fatalf("write legacy index: %v", err)
	}
	legacyContent := strings.TrimSpace(strings.Join([]string{
		"# Repo Convention",
		"",
		"Type: project",
		"Summary: Keep tests green before commit.",
		"Updated: 2026-04-20T10:00:00Z",
		"",
		"Run go test ./... before every commit.",
	}, "\n")) + "\n"
	if err := os.WriteFile(legacyFile, []byte(legacyContent), 0644); err != nil {
		t.Fatalf("write legacy memory file: %v", err)
	}

	manager := NewManager(dir)
	entries, err := manager.List()
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one migrated entry, got %+v", entries)
	}
	if entries[0].ID == "" || entries[0].Fingerprint == "" {
		t.Fatalf("expected migrated entry to contain id and fingerprint, got %+v", entries[0])
	}
	if got := entries[0].UpdatedAt.Format(time.RFC3339); got != "2026-04-20T10:00:00Z" {
		t.Fatalf("expected migrated updated_at to round-trip, got %q", got)
	}

	indexData, err := os.ReadFile(filepath.Join(dir, IndexFileName))
	if err != nil {
		t.Fatalf("read machine index: %v", err)
	}
	var envelope indexEnvelope
	if err := json.Unmarshal(indexData, &envelope); err != nil {
		t.Fatalf("decode machine index: %v", err)
	}
	if len(envelope.Entries) != 1 {
		t.Fatalf("expected one machine-index entry, got %+v", envelope)
	}

	record, err := manager.Get("Repo Convention")
	if err != nil {
		t.Fatalf("get migrated memory: %v", err)
	}
	if !strings.Contains(record.Content, "Run go test ./... before every commit.") {
		t.Fatalf("expected migrated memory content, got %+v", record)
	}
}

func TestFindRelevantCachesMemoryFileReads(t *testing.T) {
	manager := NewManager(t.TempDir())
	entry, err := manager.Remember(SaveInput{
		Title:   "Go Workflow",
		Summary: "Run tests after changing Go code.",
		Content: "Run go test ./... before every commit.",
		Type:    TypeWorkflow,
	})
	if err != nil {
		t.Fatalf("remember memory: %v", err)
	}

	var reads atomic.Int32
	originalReadFile := manager.readFile
	manager.readFile = func(path string) ([]byte, error) {
		if filepath.Base(path) == entry.File {
			reads.Add(1)
		}
		return originalReadFile(path)
	}

	for i := 0; i < 2; i++ {
		relevant, err := manager.FindRelevant("go test", 5)
		if err != nil {
			t.Fatalf("find relevant memories: %v", err)
		}
		if len(relevant) != 1 {
			t.Fatalf("expected one relevant memory, got %+v", relevant)
		}
	}

	if reads.Load() != 0 {
		t.Fatalf("expected sidecar-backed search not to reread cached memory file, got %d reads", reads.Load())
	}
}

func TestFindRelevantInvalidatesCacheWhenMemoryFileChanges(t *testing.T) {
	manager := NewManager(t.TempDir())
	entry, err := manager.Remember(SaveInput{
		Title:   "Language Preference",
		Summary: "Prefer concise Chinese responses.",
		Content: "以后请用中文回复。",
		Type:    TypeUser,
	})
	if err != nil {
		t.Fatalf("remember memory: %v", err)
	}

	var reads atomic.Int32
	originalReadFile := manager.readFile
	manager.readFile = func(path string) ([]byte, error) {
		if filepath.Base(path) == entry.File {
			reads.Add(1)
		}
		return originalReadFile(path)
	}

	relevant, err := manager.FindRelevant("中文", 5)
	if err != nil {
		t.Fatalf("find relevant memories: %v", err)
	}
	if len(relevant) != 1 || !strings.Contains(relevant[0].Content, "中文") {
		t.Fatalf("expected original content to be returned, got %+v", relevant)
	}

	path := filepath.Join(manager.Dir(), entry.File)
	entry.UpdatedAt = time.Now().UTC()
	updatedContent := renderMemoryFile(*entry, "请始终使用中文并保持简洁。")
	if err := os.WriteFile(path, []byte(updatedContent), 0644); err != nil {
		t.Fatalf("rewrite memory file: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("update memory file timestamp: %v", err)
	}

	relevant, err = manager.FindRelevant("简洁", 5)
	if err != nil {
		t.Fatalf("find relevant memories after update: %v", err)
	}
	if len(relevant) != 1 || !strings.Contains(relevant[0].Content, "保持简洁") {
		t.Fatalf("expected updated content to be returned, got %+v", relevant)
	}
	if reads.Load() != 1 {
		t.Fatalf("expected file change to trigger one sidecar refresh read, got %d reads", reads.Load())
	}
}

func TestBuildContextLayersIncludesStableCoreAndRelevantRecall(t *testing.T) {
	manager := NewManager(t.TempDir())
	identityEntry, err := manager.Remember(SaveInput{
		Title:   "Project Identity",
		Summary: "GoDex is a shared backend workspace for Web, TUI, and IM channels.",
		Content: "Treat this project as a shared backend workspace that coordinates Web, TUI, and IM channels.",
		Type:    TypeIdentity,
	})
	if err != nil {
		t.Fatalf("remember identity memory: %v", err)
	}
	userEntry, err := manager.Remember(SaveInput{
		Title:   "Chinese Preference",
		Summary: "Reply in concise Chinese.",
		Content: "以后请用中文回复，并保持简洁。",
		Type:    TypeUser,
	})
	if err != nil {
		t.Fatalf("remember user memory: %v", err)
	}
	projectEntry, err := manager.Remember(SaveInput{
		Title:   "Runtime Project Focus",
		Summary: "This repo focuses on GoDex runtime and channels.",
		Content: "当前项目重点是 GoDex 的 runtime、channel 和 agent 稳定性。",
		Type:    TypeProject,
	})
	if err != nil {
		t.Fatalf("remember project memory: %v", err)
	}
	workflowEntry, err := manager.Remember(SaveInput{
		Title:   "Testing Workflow",
		Summary: "Run go test ./... after runtime changes.",
		Content: "修改 runtime 后先运行 go test ./...，再检查 channel 回归。",
		Type:    TypeWorkflow,
	})
	if err != nil {
		t.Fatalf("remember workflow memory: %v", err)
	}

	layers, err := manager.BuildContextLayers("Please update the runtime and run tests.")
	if err != nil {
		t.Fatalf("build context layers: %v", err)
	}
	if len(layers.Identity) != 1 || layers.Identity[0].ID != identityEntry.ID {
		t.Fatalf("expected identity memory in identity layer, got %+v", layers.Identity)
	}
	if len(layers.Core) != 2 {
		t.Fatalf("expected two core memories, got %+v", layers.Core)
	}
	if layers.Core[0].ID != userEntry.ID || layers.Core[1].ID != projectEntry.ID {
		t.Fatalf("expected user/project memories in core layer, got %+v", layers.Core)
	}
	if len(layers.Relevant) != 1 || layers.Relevant[0].ID != workflowEntry.ID {
		t.Fatalf("expected workflow memory in relevant layer, got %+v", layers.Relevant)
	}
}

func TestBuildContextLayersKeepsIdentitySeparateFromCoreAndRelevant(t *testing.T) {
	manager := NewManager(t.TempDir())
	identityEntry, err := manager.Remember(SaveInput{
		Title:   "Project Identity",
		Summary: "This repo is a shared backend workspace.",
		Content: "This repo is a shared backend workspace for Web, TUI, and IM channels.",
		Type:    TypeIdentity,
		Tags:    []string{"core"},
	})
	if err != nil {
		t.Fatalf("remember identity memory: %v", err)
	}
	if _, err := manager.Remember(SaveInput{
		Title:   "Workspace Reminder",
		Summary: "Shared backend workspace.",
		Content: "Shared backend workspace.",
		Type:    TypeProject,
	}); err != nil {
		t.Fatalf("remember project memory: %v", err)
	}

	layers, err := manager.BuildContextLayers("shared backend workspace")
	if err != nil {
		t.Fatalf("build context layers: %v", err)
	}
	if len(layers.Identity) != 1 || layers.Identity[0].ID != identityEntry.ID {
		t.Fatalf("expected identity layer to contain only identity memory, got %+v", layers.Identity)
	}
	for _, mem := range layers.Core {
		if mem.ID == identityEntry.ID {
			t.Fatalf("did not expect identity memory in core layer, got %+v", layers.Core)
		}
	}
	for _, mem := range layers.Relevant {
		if mem.ID == identityEntry.ID {
			t.Fatalf("did not expect identity memory in relevant layer, got %+v", layers.Relevant)
		}
	}
}

func TestUpdatePreservesIdentityAndFile(t *testing.T) {
	manager := NewManager(t.TempDir())
	entry, err := manager.Remember(SaveInput{
		Title:   "Repo Preference",
		Summary: "Prefer concise prose.",
		Content: "Keep responses concise and high signal.",
		Type:    TypeUser,
		Source:  "manual",
		Tags:    []string{"style"},
	})
	if err != nil {
		t.Fatalf("remember memory: %v", err)
	}

	updated, err := manager.Update(UpdateInput{
		Match:   ForgetInput{File: entry.File},
		Title:   "Repo Communication Preference",
		Summary: "Prefer concise Chinese prose.",
		Content: "以后请用中文回复，并保持高信息密度。",
		Type:    TypeUser,
		Source:  "manual-web",
		Tags:    []string{"style", "language"},
	})
	if err != nil {
		t.Fatalf("update memory: %v", err)
	}
	if updated.ID != entry.ID {
		t.Fatalf("expected update to preserve id, got %q vs %q", updated.ID, entry.ID)
	}
	if updated.File != entry.File {
		t.Fatalf("expected update to preserve file, got %q vs %q", updated.File, entry.File)
	}
	if updated.Title != "Repo Communication Preference" || updated.Source != "manual-web" {
		t.Fatalf("unexpected updated entry: %+v", updated)
	}

	record, err := manager.Get(updated.ID)
	if err != nil {
		t.Fatalf("get updated memory: %v", err)
	}
	if !strings.Contains(record.Content, "中文回复") {
		t.Fatalf("expected updated content, got %+v", record)
	}
	if len(record.Tags) != 2 {
		t.Fatalf("expected updated tags, got %+v", record.Tags)
	}
}

func TestBuildContextLayersDeduplicatesRelevantAgainstCore(t *testing.T) {
	manager := NewManager(t.TempDir())
	entry, err := manager.Remember(SaveInput{
		Title:   "Chinese Preference",
		Summary: "Reply in concise Chinese.",
		Content: "以后请用中文回复，并保持简洁。",
		Type:    TypeUser,
	})
	if err != nil {
		t.Fatalf("remember user memory: %v", err)
	}

	layers, err := manager.BuildContextLayers("中文回复")
	if err != nil {
		t.Fatalf("build context layers: %v", err)
	}
	if len(layers.Core) != 1 || layers.Core[0].ID != entry.ID {
		t.Fatalf("expected user memory in core layer, got %+v", layers.Core)
	}
	if len(layers.Relevant) != 0 {
		t.Fatalf("expected relevant layer to skip duplicated core memory, got %+v", layers.Relevant)
	}
}

func TestBuildContextLayersIncludesCoreTaggedMemory(t *testing.T) {
	manager := NewManager(t.TempDir())
	tagged, err := manager.Remember(SaveInput{
		Title:   "CLI Runbook",
		Summary: "Keep the release runbook always visible.",
		Content: "发布前先跑 go test ./...，再执行 release_check.sh。",
		Type:    TypeWorkflow,
		Tags:    []string{"core", "release"},
	})
	if err != nil {
		t.Fatalf("remember tagged memory: %v", err)
	}
	userEntry, err := manager.Remember(SaveInput{
		Title:   "Chinese Preference",
		Summary: "Reply in Chinese.",
		Content: "以后请用中文回复。",
		Type:    TypeUser,
	})
	if err != nil {
		t.Fatalf("remember user memory: %v", err)
	}

	layers, err := manager.BuildContextLayers("请继续完善 release 流程")
	if err != nil {
		t.Fatalf("build context layers: %v", err)
	}
	if len(layers.Core) != 2 {
		t.Fatalf("expected tagged workflow + user memory in core, got %+v", layers.Core)
	}
	if layers.Core[0].ID != tagged.ID {
		t.Fatalf("expected core-tagged memory to be prioritized first, got %+v", layers.Core)
	}
	if layers.Core[1].ID != userEntry.ID {
		t.Fatalf("expected user memory to remain in core, got %+v", layers.Core)
	}
}

func TestBuildContextLayersPrioritizesScopeMatchedRelevantRecall(t *testing.T) {
	manager := NewManager(t.TempDir())
	weixinEntry, err := manager.Remember(SaveInput{
		Title:   "Weixin Media Flow",
		Summary: "Remember the Weixin CDN media flow for image delivery.",
		Content: "When working on the Weixin channel, keep the CDN encrypted params and media upload behavior in mind.",
		Type:    TypeWorkflow,
		Tags:    []string{"weixin", "attachments"},
	})
	if err != nil {
		t.Fatalf("remember weixin memory: %v", err)
	}
	genericEntry, err := manager.Remember(SaveInput{
		Title:   "Image Delivery Debugging",
		Summary: "Investigate image sending issues in runtime channels.",
		Content: "Check generic channel delivery paths when image sending fails.",
		Type:    TypeWorkflow,
		Tags:    []string{"runtime"},
	})
	if err != nil {
		t.Fatalf("remember generic memory: %v", err)
	}

	layers, err := manager.BuildContextLayers("please fix the weixin image sending issue")
	if err != nil {
		t.Fatalf("build context layers: %v", err)
	}
	if len(layers.Relevant) < 2 {
		t.Fatalf("expected scoped and fallback relevant memories, got %+v", layers.Relevant)
	}
	if layers.Relevant[0].ID != weixinEntry.ID {
		t.Fatalf("expected weixin-scoped memory to be prioritized first, got %+v", layers.Relevant)
	}
	if layers.Relevant[1].ID != genericEntry.ID {
		t.Fatalf("expected generic memory to remain as fallback recall, got %+v", layers.Relevant)
	}
}

func TestBuildContextLayersMatchesScopeFromSourceMetadata(t *testing.T) {
	manager := NewManager(t.TempDir())
	feishuEntry, err := manager.Remember(SaveInput{
		Title:   "Attachment Upload Flow",
		Summary: "Cross-channel attachment troubleshooting notes.",
		Content: "The attachment upload flow differs between channels.",
		Type:    TypeWorkflow,
		Source:  "manual-feishu",
	})
	if err != nil {
		t.Fatalf("remember feishu memory: %v", err)
	}
	genericEntry, err := manager.Remember(SaveInput{
		Title:   "Attachment Debugging Checklist",
		Summary: "General attachment troubleshooting steps.",
		Content: "Start from transport logs and confirm upload credentials.",
		Type:    TypeWorkflow,
	})
	if err != nil {
		t.Fatalf("remember generic memory: %v", err)
	}

	layers, err := manager.BuildContextLayers("please debug the feishu attachment flow")
	if err != nil {
		t.Fatalf("build context layers: %v", err)
	}
	if len(layers.Relevant) < 2 {
		t.Fatalf("expected scoped and fallback relevant memories, got %+v", layers.Relevant)
	}
	if layers.Relevant[0].ID != feishuEntry.ID {
		t.Fatalf("expected source-scoped feishu memory to be prioritized first, got %+v", layers.Relevant)
	}
	if layers.Relevant[1].ID != genericEntry.ID {
		t.Fatalf("expected generic memory to remain as fallback recall, got %+v", layers.Relevant)
	}
}

func TestBuildContextLayersAppliesTokenBudgets(t *testing.T) {
	manager := NewManager(t.TempDir())
	longChinese := strings.Repeat("这是一个很长的记忆片段，用来测试上下文预算控制。", 40)

	if _, err := manager.Remember(SaveInput{
		Title:   "Project Identity",
		Summary: longChinese,
		Content: longChinese,
		Type:    TypeIdentity,
	}); err != nil {
		t.Fatalf("remember identity memory: %v", err)
	}
	if _, err := manager.Remember(SaveInput{
		Title:   "Chinese Preference",
		Summary: longChinese,
		Content: longChinese,
		Type:    TypeUser,
	}); err != nil {
		t.Fatalf("remember user memory: %v", err)
	}
	if _, err := manager.Remember(SaveInput{
		Title:   "Project Focus",
		Summary: longChinese,
		Content: longChinese,
		Type:    TypeProject,
	}); err != nil {
		t.Fatalf("remember project memory: %v", err)
	}
	if _, err := manager.Remember(SaveInput{
		Title:   "Testing Workflow",
		Summary: longChinese,
		Content: longChinese,
		Type:    TypeWorkflow,
	}); err != nil {
		t.Fatalf("remember workflow memory: %v", err)
	}
	if _, err := manager.Remember(SaveInput{
		Title:   "Delivery Warning",
		Summary: longChinese,
		Content: longChinese,
		Type:    TypeWarning,
	}); err != nil {
		t.Fatalf("remember warning memory: %v", err)
	}

	layers, err := manager.BuildContextLayers("请分析 runtime 和 delivery warning")
	if err != nil {
		t.Fatalf("build context layers: %v", err)
	}
	if len(layers.Core) == 0 || len(layers.Relevant) == 0 {
		t.Fatalf("expected both core and relevant layers, got %+v", layers)
	}
	if len(layers.Identity) == 0 {
		t.Fatalf("expected identity layer, got %+v", layers)
	}

	identityTokens := 0
	for _, mem := range layers.Identity {
		identityTokens += estimateRelevantMemoryTokens(mem)
		if compress.CountTokens(mem.Content) > maxIdentityBodyTokens {
			t.Fatalf("expected identity memory content to be truncated to token budget, got %d tokens", compress.CountTokens(mem.Content))
		}
	}
	if identityTokens > maxIdentityContextTokens {
		t.Fatalf("expected identity layer token usage <= %d, got %d", maxIdentityContextTokens, identityTokens)
	}

	coreTokens := 0
	for _, mem := range layers.Core {
		coreTokens += estimateRelevantMemoryTokens(mem)
		if compress.CountTokens(mem.Content) > maxCoreRetrievedBodyTokens {
			t.Fatalf("expected core memory content to be truncated to token budget, got %d tokens", compress.CountTokens(mem.Content))
		}
	}
	if coreTokens > maxCoreContextTokens {
		t.Fatalf("expected core layer token usage <= %d, got %d", maxCoreContextTokens, coreTokens)
	}

	relevantTokens := 0
	for _, mem := range layers.Relevant {
		relevantTokens += estimateRelevantMemoryTokens(mem)
		if compress.CountTokens(mem.Content) > maxRelevantRetrievedTokens {
			t.Fatalf("expected relevant memory content to be truncated to token budget, got %d tokens", compress.CountTokens(mem.Content))
		}
	}
	if relevantTokens > maxRelevantContextTokens {
		t.Fatalf("expected relevant layer token usage <= %d, got %d", maxRelevantContextTokens, relevantTokens)
	}
}

func TestGetReturnsMemoryByIDOrTitle(t *testing.T) {
	manager := NewManager(t.TempDir())
	entry, err := manager.Remember(SaveInput{
		Title:   "Repo Preference",
		Summary: "Prefer compact prose.",
		Content: "Keep responses concise and high signal.",
		Type:    TypeUser,
		Source:  "manual",
		Tags:    []string{"style", "writing"},
	})
	if err != nil {
		t.Fatalf("remember memory: %v", err)
	}

	byID, err := manager.Get(entry.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if byID.Title != "Repo Preference" || !strings.Contains(byID.Content, "concise") {
		t.Fatalf("unexpected get by id result: %+v", byID)
	}
	if len(byID.Tags) != 2 || byID.Source != "manual" {
		t.Fatalf("expected tags/source metadata to round-trip, got %+v", byID.Entry)
	}

	byTitle, err := manager.Get("repo preference")
	if err != nil {
		t.Fatalf("get by title: %v", err)
	}
	if byTitle.ID != entry.ID {
		t.Fatalf("expected title lookup to return same entry, got %+v vs %+v", byTitle, entry)
	}
}

func TestSearchFiltersAndOrdering(t *testing.T) {
	manager := NewManager(t.TempDir())
	first, err := manager.Remember(SaveInput{
		Title:   "Go Validation Workflow",
		Summary: "Run go test after code changes.",
		Content: "Run go test ./... after touching runtime code.",
		Type:    TypeWorkflow,
		Source:  "turn-end-extractor",
		Tags:    []string{"go", "validation"},
	})
	if err != nil {
		t.Fatalf("remember first memory: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	second, err := manager.Remember(SaveInput{
		Title:   "Channel Delivery Warning",
		Summary: "Weixin delivery may block without context tokens.",
		Content: "If context_token is missing, delivery should be marked blocked.",
		Type:    TypeWarning,
		Source:  "system",
		Tags:    []string{"weixin", "delivery"},
	})
	if err != nil {
		t.Fatalf("remember second memory: %v", err)
	}

	results, err := manager.Search(SearchOptions{Tag: "delivery"})
	if err != nil {
		t.Fatalf("search by tag: %v", err)
	}
	if len(results) != 1 || results[0].ID != second.ID {
		t.Fatalf("expected tag-filtered result for second memory, got %+v", results)
	}

	results, err = manager.Search(SearchOptions{Source: "turn-end-extractor"})
	if err != nil {
		t.Fatalf("search by source: %v", err)
	}
	if len(results) != 1 || results[0].ID != first.ID {
		t.Fatalf("expected source-filtered result for first memory, got %+v", results)
	}

	results, err = manager.Search(SearchOptions{Type: TypeWorkflow})
	if err != nil {
		t.Fatalf("search by type: %v", err)
	}
	if len(results) != 1 || results[0].ID != first.ID {
		t.Fatalf("expected type-filtered result for first memory, got %+v", results)
	}

	results, err = manager.Search(SearchOptions{Query: "delivery"})
	if err != nil {
		t.Fatalf("search by query: %v", err)
	}
	if len(results) != 1 || results[0].ID != second.ID {
		t.Fatalf("expected query-filtered result for second memory, got %+v", results)
	}

	all, err := manager.List()
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	if len(all) != 2 || all[0].ID != second.ID || all[1].ID != first.ID {
		t.Fatalf("expected list to be ordered by updated_at desc, got %+v", all)
	}
}

func TestListByTypeTagAndSource(t *testing.T) {
	manager := NewManager(t.TempDir())
	if _, err := manager.Remember(SaveInput{
		Title:   "Chinese Preference",
		Summary: "Reply in Chinese.",
		Content: "以后请用中文回复。",
		Type:    TypeUser,
		Source:  "manual",
		Tags:    []string{"language", "chinese"},
	}); err != nil {
		t.Fatalf("remember user memory: %v", err)
	}
	if _, err := manager.Remember(SaveInput{
		Title:   "Runtime Warning",
		Summary: "Run race tests for channel changes.",
		Content: "Use go test -race ./... for runtime/channel changes.",
		Type:    TypeWarning,
		Source:  "turn-end-extractor",
		Tags:    []string{"go", "runtime"},
	}); err != nil {
		t.Fatalf("remember warning memory: %v", err)
	}

	byType, err := manager.ListByType(TypeUser)
	if err != nil {
		t.Fatalf("list by type: %v", err)
	}
	if len(byType) != 1 || byType[0].Title != "Chinese Preference" {
		t.Fatalf("unexpected list by type result: %+v", byType)
	}

	byTag, err := manager.ListByTag("runtime")
	if err != nil {
		t.Fatalf("list by tag: %v", err)
	}
	if len(byTag) != 1 || byTag[0].Title != "Runtime Warning" {
		t.Fatalf("unexpected list by tag result: %+v", byTag)
	}

	bySource, err := manager.ListBySource("manual")
	if err != nil {
		t.Fatalf("list by source: %v", err)
	}
	if len(bySource) != 1 || bySource[0].Title != "Chinese Preference" {
		t.Fatalf("unexpected list by source result: %+v", bySource)
	}
}

func TestSearchCreatesSQLiteSidecar(t *testing.T) {
	manager := NewManager(t.TempDir())
	if _, err := manager.Remember(SaveInput{
		Title:   "Go Validation Workflow",
		Summary: "Run go test after code changes.",
		Content: "Run go test ./... after touching runtime code.",
		Type:    TypeWorkflow,
	}); err != nil {
		t.Fatalf("remember memory: %v", err)
	}

	results, err := manager.Search(SearchOptions{Query: "runtime"})
	if err != nil {
		t.Fatalf("search memories: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one search result, got %+v", results)
	}
	if _, err := os.Stat(filepath.Join(manager.Dir(), SidecarDBFileName)); err != nil {
		t.Fatalf("expected sqlite sidecar to exist: %v", err)
	}
}

func TestRememberFoldsDuplicateFactsOnSameTitleUpdate(t *testing.T) {
	manager := NewManager(t.TempDir())

	first, err := manager.Remember(SaveInput{
		Title:   "Go Validation Workflow",
		Summary: "Run go test after code changes.",
		Content: "Run go test ./... after touching runtime code.",
		Type:    TypeWorkflow,
	})
	if err != nil {
		t.Fatalf("remember first memory: %v", err)
	}

	// Same title, overlapping fact: the duplicated line must be folded away
	// and only the new line appended (foldCapture dedup).
	second, err := manager.Remember(SaveInput{
		Title:   "Go Validation Workflow",
		Summary: "Run go test after code changes.",
		Content: "Run go test ./... after touching runtime code.\nUse go test -race for channel changes.",
		Type:    TypeWorkflow,
	})
	if err != nil {
		t.Fatalf("remember same-title update: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected same-title update to preserve id, got %q vs %q", second.ID, first.ID)
	}

	record, err := manager.Get(second.ID)
	if err != nil {
		t.Fatalf("get updated memory: %v", err)
	}
	if strings.Count(record.Content, "Run go test ./... after touching runtime code") != 1 {
		t.Errorf("expected duplicate fact folded to one occurrence, content=%q", record.Content)
	}
	if !strings.Contains(record.Content, "go test -race for channel changes") {
		t.Errorf("expected new fact appended, content=%q", record.Content)
	}

	// Fully duplicated update adds nothing.
	third, err := manager.Remember(SaveInput{
		Title:   "Go Validation Workflow",
		Summary: "Run go test after code changes.",
		Content: "Run go test ./... after touching runtime code.\nUse go test -race for channel changes.",
		Type:    TypeWorkflow,
	})
	if err != nil {
		t.Fatalf("remember fully-duplicated update: %v", err)
	}
	recordAfter, err := manager.Get(third.ID)
	if err != nil {
		t.Fatalf("get memory after duplicate update: %v", err)
	}
	if recordAfter.Content != record.Content {
		t.Errorf("expected fully-duplicated update to keep content unchanged, got %q vs %q", recordAfter.Content, record.Content)
	}
}

func TestRememberIncrementallyUpdatesSQLiteSidecar(t *testing.T) {
	manager := NewManager(t.TempDir())

	first, err := manager.Remember(SaveInput{
		Title:   "Go Validation Workflow",
		Summary: "Run go test after code changes.",
		Content: "Run go test ./... after touching runtime code.",
		Type:    TypeWorkflow,
	})
	if err != nil {
		t.Fatalf("remember first memory: %v", err)
	}
	if got := countSidecarRows(t, filepath.Join(manager.Dir(), SidecarDBFileName)); got != 1 {
		t.Fatalf("expected 1 sidecar row after first remember, got %d", got)
	}

	second, err := manager.Remember(SaveInput{
		Title:   "Runtime Warning",
		Summary: "Run race tests for channel changes.",
		Content: "Use go test -race ./... for runtime/channel changes.",
		Type:    TypeWarning,
	})
	if err != nil {
		t.Fatalf("remember second memory: %v", err)
	}
	if got := countSidecarRows(t, filepath.Join(manager.Dir(), SidecarDBFileName)); got != 2 {
		t.Fatalf("expected 2 sidecar rows after second remember, got %d", got)
	}

	results, err := manager.Search(SearchOptions{Query: "runtime"})
	if err != nil {
		t.Fatalf("search memories: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected two search results after incremental sidecar update, got %+v", results)
	}
	seen := map[string]struct{}{}
	for _, result := range results {
		seen[result.ID] = struct{}{}
	}
	if _, ok := seen[first.ID]; !ok {
		t.Fatalf("expected first remembered entry to be searchable, got %+v", results)
	}
	if _, ok := seen[second.ID]; !ok {
		t.Fatalf("expected remembered entries to be searchable, got %+v", results)
	}
}

func TestForgetIncrementallyRemovesSQLiteSidecarEntry(t *testing.T) {
	manager := NewManager(t.TempDir())
	entry, err := manager.Remember(SaveInput{
		Title:   "Repo Convention",
		Summary: "Keep tests green before commit.",
		Content: "Run go test ./... before every commit.",
		Type:    TypeProject,
	})
	if err != nil {
		t.Fatalf("remember memory: %v", err)
	}
	if got := countSidecarRows(t, filepath.Join(manager.Dir(), SidecarDBFileName)); got != 1 {
		t.Fatalf("expected 1 sidecar row after remember, got %d", got)
	}

	if _, err := manager.Forget(ForgetInput{Title: entry.Title}); err != nil {
		t.Fatalf("forget memory: %v", err)
	}
	if got := countSidecarRows(t, filepath.Join(manager.Dir(), SidecarDBFileName)); got != 0 {
		t.Fatalf("expected sidecar row to be removed after forget, got %d", got)
	}
}

func countSidecarRows(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sidecar db: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&count); err != nil {
		t.Fatalf("count sidecar rows: %v", err)
	}
	return count
}
