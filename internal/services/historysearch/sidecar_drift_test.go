package historysearch

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/history"
)

// assertFTSAligned proves the invariant the reconcile delete depends on:
// history_fts.rowid == history_entries.rowid for every entry, with no orphan FTS
// rows left behind. A violated invariant is invisible in MatchCount (orphan rows
// keep matching), so it has to be asserted directly.
func assertFTSAligned(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sidecar: %v", err)
	}
	defer db.Close()

	var entries, fts, orphans int
	if err := db.QueryRow(`SELECT COUNT(*) FROM history_entries`).Scan(&entries); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM history_fts`).Scan(&fts); err != nil {
		t.Fatalf("count fts: %v", err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM history_entries e
		LEFT JOIN history_fts f ON f.rowid = e.rowid
		WHERE f.rowid IS NULL OR f.id != e.id`).Scan(&orphans); err != nil {
		t.Fatalf("count orphans: %v", err)
	}
	if entries != fts || orphans != 0 {
		t.Fatalf("fts index misaligned: entries=%d fts=%d orphans=%d", entries, fts, orphans)
	}
}

func countFTSDrift(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sidecar: %v", err)
	}
	defer db.Close()

	var drift int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM history_entries e
		LEFT JOIN history_fts f ON f.rowid = e.rowid
		WHERE f.rowid IS NULL OR f.id != e.id`).Scan(&drift); err != nil {
		t.Fatalf("count drift: %v", err)
	}
	return drift
}

func writeAllArchiveSession(t *testing.T, cfg *config.Config, id, title, text string, day int) {
	t.Helper()
	writeSession(t, cfg.SessionsDir, id, title, time.Date(2026, 5, day, 10, 0, 0, 0, time.UTC), []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, text),
	}, nil)
}

// A read-only search must not leave orphan full-text rows. Deletes go through the
// FTS rowid (the FTS `id` column is UNINDEXED, which is what made the old per-row
// delete quadratic), so reconcile and delete have to stay row-aligned.
func TestHistorySearchDeleteKeepsFTSRowsAligned(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{SessionsDir: filepath.Join(root, ".sessions"), TranscriptsDir: filepath.Join(root, ".transcripts")}
	if err := os.MkdirAll(cfg.SessionsDir, 0755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writeAllArchiveSession(t, cfg, "keep", "Keep", "aurora kept", 1)
	writeAllArchiveSession(t, cfg, "drop", "Drop", "aurora dropped", 2)

	service := NewService(cfg)
	runtime := service.Bind(nil)
	if _, err := runtime.SearchHistory(context.Background(), "", automation.SessionContext{}, history.SearchRequest{
		Query: "aurora", Scope: history.HistorySearchScopeAllArchives,
	}); err != nil {
		t.Fatalf("initial search: %v", err)
	}
	dbPath := service.sidecarPath()
	if got := countHistorySidecarRows(t, dbPath, "history_entries"); got != 2 {
		t.Fatalf("expected 2 indexed entries, got %d", got)
	}

	// Drop one source, then re-search: reconcile removes its rows in the same
	// transaction. If the FTS delete missed them they would survive as orphans.
	if err := os.RemoveAll(filepath.Join(cfg.SessionsDir, "drop")); err != nil {
		t.Fatalf("remove session: %v", err)
	}
	result, err := runtime.SearchHistory(context.Background(), "", automation.SessionContext{}, history.SearchRequest{
		Query: "aurora", Scope: history.HistorySearchScopeAllArchives,
	})
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if result.MatchCount != 1 {
		t.Fatalf("expected only the surviving source to match, got %#v", result)
	}
	if got := countHistorySidecarRows(t, dbPath, "history_entries"); got != 1 {
		t.Fatalf("expected the dropped source to be removed, got %d entries", got)
	}
	assertFTSAligned(t, dbPath)
}

// If a previously-broken index is opened, the row alignment is repaired instead of
// silently serving orphan rows forever.
func TestHistorySearchHealsDriftedFTSIndex(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{SessionsDir: filepath.Join(root, ".sessions"), TranscriptsDir: filepath.Join(root, ".transcripts")}
	if err := os.MkdirAll(cfg.SessionsDir, 0755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writeAllArchiveSession(t, cfg, "s1", "S1", "nebula marker", 1)
	writeAllArchiveSession(t, cfg, "s2", "S2", "nebula secondary", 2)

	service := NewService(cfg)
	runtime := service.Bind(nil)
	if _, err := runtime.SearchHistory(context.Background(), "", automation.SessionContext{}, history.SearchRequest{
		Query: "nebula", Scope: history.HistorySearchScopeAllArchives,
	}); err != nil {
		t.Fatalf("initial search: %v", err)
	}
	dbPath := service.sidecarPath()

	// Simulate an index written by the old (misaligned) code path.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sidecar: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM history_fts WHERE rowid = (SELECT MIN(rowid) FROM history_fts)`); err != nil {
		t.Fatalf("corrupt fts index: %v", err)
	}
	db.Close()
	if drift := countFTSDrift(t, dbPath); drift == 0 {
		t.Fatalf("expected the corruption to create drift")
	}

	// Any subsequent search opens the index, so it must heal on the way in.
	result, err := runtime.SearchHistory(context.Background(), "", automation.SessionContext{}, history.SearchRequest{
		Query: "nebula", Scope: history.HistorySearchScopeAllArchives,
	})
	if err != nil {
		t.Fatalf("search after drift: %v", err)
	}
	if result.MatchCount != 2 {
		t.Fatalf("expected both sources to match after healing, got %#v", result)
	}
	assertFTSAligned(t, dbPath)
}

// A caller-sized limit must not turn one search into an unbounded response, while
// MatchCount stays truthful so a large corpus remains observable.
func TestHistorySearchClampsLimit(t *testing.T) {
	messages := make([]protocol.Message, 0, maxSearchLimit+50)
	for i := 0; i < maxSearchLimit+50; i++ {
		messages = append(messages, protocol.NewTextMessage(protocol.RoleUser, "aurora line "+strconv.Itoa(i)))
	}
	service := NewService(&config.Config{})
	runtime := service.Bind(fakeCurrent{messages: messages})

	result, err := runtime.SearchHistory(context.Background(), "session-1", automation.SessionContext{}, history.SearchRequest{
		Query: "aurora",
		Limit: 100000,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(result.Snippets) != maxSearchLimit {
		t.Fatalf("expected snippets to be clamped to %d, got %d", maxSearchLimit, len(result.Snippets))
	}
	if result.MatchCount != len(messages) {
		t.Fatalf("expected MatchCount to stay truthful, got %d want %d", result.MatchCount, len(messages))
	}
}

// A cancelled caller must abort the walk instead of holding the write transaction
// until the (much longer) tool timeout, which is how a hung search used to be
// unkillable.
func TestHistorySearchHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{SessionsDir: filepath.Join(root, ".sessions"), TranscriptsDir: filepath.Join(root, ".transcripts")}
	if err := os.MkdirAll(cfg.SessionsDir, 0755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writeAllArchiveSession(t, cfg, "s1", "S1", "aurora marker", 1)

	service := NewService(cfg)
	runtime := service.Bind(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runtime.SearchHistory(ctx, "", automation.SessionContext{}, history.SearchRequest{
		Query: "aurora", Scope: history.HistorySearchScopeAllArchives,
	})
	if err == nil {
		t.Fatalf("expected a cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
