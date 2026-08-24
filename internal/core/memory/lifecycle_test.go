package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveHidesFromRecallAndInjection(t *testing.T) {
	manager := NewManager(t.TempDir())

	if _, err := manager.Remember(SaveInput{
		Title:   "Active Convention",
		Summary: "Still relevant.",
		Content: "Keep tests green before commit.",
		Type:    TypeProject,
		Source:  "test",
	}); err != nil {
		t.Fatalf("remember active memory: %v", err)
	}
	if _, err := manager.Remember(SaveInput{
		Title:   "Archived Milestone",
		Summary: "Old milestone, no longer relevant.",
		Content: "Phase 5 all done.",
		Type:    TypeProject,
		Source:  "test",
	}); err != nil {
		t.Fatalf("remember archived memory: %v", err)
	}

	if _, err := manager.Archive(ForgetInput{Title: "Archived Milestone"}); err != nil {
		t.Fatalf("archive memory: %v", err)
	}

	// Default search excludes archived.
	all, err := manager.Search(SearchOptions{Query: "Milestone"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, mem := range all {
		if strings.EqualFold(mem.Title, "Archived Milestone") {
			t.Fatalf("archived memory leaked into default search: %+v", mem)
		}
	}

	// Status=archived returns it.
	archived, err := manager.Search(SearchOptions{Query: "Milestone", Status: StatusArchived})
	if err != nil {
		t.Fatalf("search archived: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("expected 1 archived memory, got %+v", archived)
	}

	// BuildContextLayers must not include archived.
	layers, err := manager.BuildContextLayers("Milestone")
	if err != nil {
		t.Fatalf("build context layers: %v", err)
	}
	for _, mem := range append(append([]RelevantMemory{}, layers.Identity...), layers.Core...) {
		if strings.EqualFold(mem.Title, "Archived Milestone") {
			t.Fatalf("archived memory reached context layers: %+v", mem)
		}
	}
}

func TestArchiveRestoreRoundTripPersists(t *testing.T) {
	dir := t.TempDir()
	manager := NewManager(dir)

	if _, err := manager.Remember(SaveInput{
		Title:   "Temp Knowledge",
		Summary: "Ephemeral but archivable.",
		Content: "Body.",
		Type:    TypeWorkflow,
		Source:  "test",
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}

	if _, err := manager.Archive(ForgetInput{Title: "Temp Knowledge"}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Re-open a fresh manager to prove status persisted on disk.
	fresh := NewManager(dir)
	archived, err := fresh.Search(SearchOptions{Status: StatusArchived})
	if err != nil {
		t.Fatalf("search archived: %v", err)
	}
	if len(archived) != 1 || !strings.EqualFold(archived[0].Title, "Temp Knowledge") {
		t.Fatalf("expected archived memory after reopen, got %+v", archived)
	}

	if _, err := fresh.Restore(ForgetInput{Title: "Temp Knowledge"}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	active, err := fresh.Search(SearchOptions{Query: "Temp"})
	if err != nil {
		t.Fatalf("search active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("expected restored memory in active search, got %+v", active)
	}
}

func TestListMilestonesAndArchive(t *testing.T) {
	manager := NewManager(t.TempDir())

	for _, input := range []SaveInput{
		{Title: "Phase 5 全部完成：Turn Error 分层", Summary: "s1", Content: "c1", Type: TypeProject, Source: "session"},
		{Title: "UI 优化 P0 完成", Summary: "s2", Content: "c2", Type: TypeProject, Source: "manual-web"},
		{Title: "Coding guidelines", Summary: "s3", Content: "c3", Type: TypeProject, Source: "manual-web"},
	} {
		if _, err := manager.Remember(input); err != nil {
			t.Fatalf("remember %q: %v", input.Title, err)
		}
	}

	matches, err := manager.ListMilestoneMemories()
	if err != nil {
		t.Fatalf("list milestones: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 milestone memories, got %+v", matches)
	}

	archived, err := manager.ArchiveMilestones()
	if err != nil {
		t.Fatalf("archive milestones: %v", err)
	}
	if len(archived) != 2 {
		t.Fatalf("expected 2 archived milestones, got %d", len(archived))
	}

	// The non-milestone memory stays active.
	active, err := manager.Search(SearchOptions{Query: "Coding"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("expected 1 active memory, got %+v", active)
	}
}

func TestRemoveSuppressionAllowsRecapture(t *testing.T) {
	dir := t.TempDir()
	manager := NewManager(dir)

	candidate := newCandidate("Workflow: Validate Go changes", "Run go test after changes.", "Run go test ./... after changes.", TypeWorkflow, "turn-end-extractor")
	if err := manager.writeCandidates([]Candidate{candidate}); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	if _, err := manager.DismissCandidate(candidate.Fingerprint); err != nil {
		t.Fatalf("dismiss candidate: %v", err)
	}

	suppressions, err := manager.ListSuppressions()
	if err != nil {
		t.Fatalf("list suppressions: %v", err)
	}
	if len(suppressions) != 1 {
		t.Fatalf("expected 1 suppression, got %+v", suppressions)
	}

	if err := manager.RemoveSuppression(suppressions[0].Key); err != nil {
		t.Fatalf("remove suppression: %v", err)
	}

	suppressions, err = manager.ListSuppressions()
	if err != nil {
		t.Fatalf("list suppressions after removal: %v", err)
	}
	if len(suppressions) != 0 {
		t.Fatalf("expected no suppressions after removal, got %+v", suppressions)
	}
}

func TestLastReferencedAtTracksInjection(t *testing.T) {
	manager := NewManager(t.TempDir())

	if _, err := manager.Remember(SaveInput{
		Title:   "Referenced Fact",
		Summary: "Will be injected.",
		Content: "Body that matches query.",
		Type:    TypeProject,
		Source:  "test",
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}

	if _, err := manager.BuildContextLayers("Referenced Fact"); err != nil {
		t.Fatalf("build context layers: %v", err)
	}

	entries, err := manager.readEntries()
	if err != nil {
		t.Fatalf("read entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %+v", entries)
	}
	if entries[0].LastReferencedAt.IsZero() {
		t.Fatalf("expected LastReferencedAt to be recorded after injection, got %+v", entries[0])
	}

	// Persisted to the memory file header.
	path := filepath.Join(manager.Dir(), entries[0].File)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read memory file: %v", err)
	}
	if !strings.Contains(string(data), "LastReferenced:") {
		t.Fatalf("expected LastReferenced header in memory file:\n%s", data)
	}
}

func TestArchivePersistsStatusHeader(t *testing.T) {
	dir := t.TempDir()
	manager := NewManager(dir)

	if _, err := manager.Remember(SaveInput{
		Title:   "Archivable",
		Summary: "s",
		Content: "c",
		Type:    TypeProject,
		Source:  "test",
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if _, err := manager.Archive(ForgetInput{Title: "Archivable"}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	entries, err := manager.readEntries()
	if err != nil {
		t.Fatalf("read entries: %v", err)
	}
	path := filepath.Join(manager.Dir(), entries[0].File)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(data), "Status: archived") {
		t.Fatalf("expected Status: archived in file:\n%s", data)
	}
}

func TestReferenceThrottleSkipsFrequentWrites(t *testing.T) {
	manager := NewManager(t.TempDir())
	if _, err := manager.Remember(SaveInput{
		Title:   "Throttled",
		Summary: "s",
		Content: "c",
		Type:    TypeProject,
		Source:  "test",
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}

	if _, err := manager.BuildContextLayers("Throttled"); err != nil {
		t.Fatalf("build context layers: %v", err)
	}
	entries, _ := manager.readEntries()
	first := entries[0].LastReferencedAt
	if first.IsZero() {
		t.Fatalf("expected first reference recorded")
	}

	// Second immediate recall should be throttled (no change).
	if _, err := manager.BuildContextLayers("Throttled"); err != nil {
		t.Fatalf("build context layers: %v", err)
	}
	entries, _ = manager.readEntries()
	if !entries[0].LastReferencedAt.Equal(first) {
		t.Fatalf("expected throttled reference to keep the first timestamp, got %v vs %v", entries[0].LastReferencedAt, first)
	}

	// Advancing time beyond the throttle records a new timestamp.
	manager.mu.Lock()
	entries[0].LastReferencedAt = time.Now().UTC().Add(-2 * referenceThrottle)
	_ = manager.writeEntries(entries)
	manager.mu.Unlock()

	if _, err := manager.BuildContextLayers("Throttled"); err != nil {
		t.Fatalf("build context layers: %v", err)
	}
	entries, _ = manager.readEntries()
	if entries[0].LastReferencedAt.Equal(first) {
		t.Fatalf("expected reference timestamp to refresh after throttle elapsed")
	}
}
