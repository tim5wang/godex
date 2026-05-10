package storagegc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanClassifiesStorageCategories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".tmp", "browser", "user-data", "Default", "Cache", "Cache_Data", "entry"), "cache")
	writeFile(t, filepath.Join(root, ".tmp", "web_fetch", "fetch-old.md"), "fetch")
	writeFile(t, filepath.Join(root, ".tool-results", "web-1", "tool.json"), "tool")
	writeFile(t, filepath.Join(root, ".sessions", "web-1", "checkpoints", "20260101T000000.000000000Z-a", "state.json"), "state")
	writeFile(t, filepath.Join(root, "memory", "memory.db"), "memory")

	report := Scan(Options{
		StateDir:    root,
		TempDir:     filepath.Join(root, ".tmp"),
		SessionsDir: filepath.Join(root, ".sessions"),
		Now:         time.Now(),
	})

	for _, category := range []string{CategoryBrowserCache, CategoryWebFetchSpill, CategoryToolResult, CategorySessionCheckpoint} {
		if !hasCategory(report.Items, category) {
			t.Fatalf("expected category %s in %+v", category, report.Items)
		}
	}
	if hasCategory(report.Items, "memory") {
		t.Fatalf("did not expect memory classified as cleanable: %+v", report.Items)
	}
}

func TestCleanBrowserCacheSkipsArtifacts(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(root, ".tmp", "browser", "user-data", "Default", "Cache", "Cache_Data", "entry")
	screenshotPath := filepath.Join(root, ".tmp", "browser", "web-session", "page-1.png")
	writeFile(t, cachePath, "cache")
	writeFile(t, screenshotPath, "png")

	result, err := CleanBrowserCache(Options{
		TempDir: filepath.Join(root, ".tmp"),
		Now:     time.Now(),
		DryRun:  false,
	})
	if err != nil {
		t.Fatalf("clean browser cache: %v", err)
	}
	if result.Candidates == 0 {
		t.Fatalf("expected browser cache candidate, got %+v", result)
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("expected cache removed, got %v", err)
	}
	if _, err := os.Stat(screenshotPath); err != nil {
		t.Fatalf("expected screenshot artifact preserved: %v", err)
	}
}

func TestPruneSessionCheckpointsKeepsPointerAndLatest(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, ".sessions", "web-1")
	for i := 0; i < 5; i++ {
		id := time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC).Format("20060102T150405.000000000Z") + "-a"
		writeFile(t, filepath.Join(sessionDir, "checkpoints", id, "state.json"), "state")
	}
	pointer := `{"current":"20260101T000100.000000000Z-a","created_at":"2026-01-01T00:01:00Z"}`
	writeFile(t, filepath.Join(sessionDir, "checkpoint.json"), pointer)

	result, err := CleanSessionCheckpoints(Options{
		SessionsDir:                 filepath.Join(root, ".sessions"),
		SessionCheckpointKeepLatest: 2,
		SessionCheckpointTTL:        time.Hour,
		Now:                         time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("clean checkpoints: %v", err)
	}
	if result.Candidates != 2 {
		t.Fatalf("expected 2 pruned checkpoints, got %+v", result)
	}
	for _, keep := range []string{
		"20260101T000100.000000000Z-a",
		"20260101T000300.000000000Z-a",
		"20260101T000400.000000000Z-a",
	} {
		if _, err := os.Stat(filepath.Join(sessionDir, "checkpoints", keep)); err != nil {
			t.Fatalf("expected checkpoint %s kept: %v", keep, err)
		}
	}
}

func TestCleanArtifactsPreservesActiveSessionReferences(t *testing.T) {
	root := t.TempDir()
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	activeArtifact := filepath.Join(root, ".tool-results", "web-active", "tool.json")
	staleArtifact := filepath.Join(root, ".tmp", "web_fetch", "fetch-old.md")
	writeFile(t, activeArtifact, "active")
	writeFile(t, staleArtifact, "stale")
	setModTime(t, activeArtifact, old)
	setModTime(t, staleArtifact, old)

	result, err := CleanArtifacts(Options{
		StateDir:         root,
		TempDir:          filepath.Join(root, ".tmp"),
		SessionsDir:      filepath.Join(root, ".sessions"),
		ArtifactTTL:      time.Hour,
		ActiveSessionIDs: []string{"web-active"},
		Now:              old.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("clean artifacts: %v", err)
	}
	if result.Candidates != 1 {
		t.Fatalf("expected one stale artifact candidate, got %+v", result)
	}
	if _, err := os.Stat(activeArtifact); err != nil {
		t.Fatalf("expected active artifact kept: %v", err)
	}
	if _, err := os.Stat(staleArtifact); !os.IsNotExist(err) {
		t.Fatalf("expected stale artifact removed, got %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func setModTime(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func hasCategory(items []Item, category string) bool {
	for _, item := range items {
		if item.Category == category {
			return true
		}
	}
	return false
}
