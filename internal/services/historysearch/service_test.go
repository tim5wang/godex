package historysearch

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/history"

	_ "modernc.org/sqlite"
)

type fakeCurrent struct {
	messages []protocol.Message
	refs     []string
}

func (f fakeCurrent) GetMessages() []protocol.Message {
	return protocol.CloneMessages(f.messages)
}

func (f fakeCurrent) TranscriptRefs() []string {
	return append([]string{}, f.refs...)
}

func TestServiceSearchHistoryCurrentSession(t *testing.T) {
	cfg := &config.Config{SessionsDir: filepath.Join(t.TempDir(), ".sessions"), TranscriptsDir: filepath.Join(t.TempDir(), ".transcripts")}
	service := NewService(cfg)
	runtime := service.Bind(fakeCurrent{
		messages: []protocol.Message{
			protocol.NewTextMessage(protocol.RoleUser, "We settled on the aurora API yesterday."),
			protocol.NewEphemeralTextMessage(protocol.KindMemory, "aurora should not be visible"),
			protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "pwd"})),
		},
	})

	result, err := runtime.SearchHistory(context.Background(), "session-1", automation.SessionContext{}, history.SearchRequest{
		Query: "aurora",
	})
	if err != nil {
		t.Fatalf("search history: %v", err)
	}
	if result.MatchCount != 1 || len(result.Snippets) != 1 {
		t.Fatalf("expected one current-session hit, got %#v", result)
	}
	if got := result.Snippets[0].SourceKind; got != "current_session" {
		t.Fatalf("expected current_session source kind, got %q", got)
	}
	if !strings.Contains(strings.ToLower(result.Snippets[0].TextExcerpt), "aurora") {
		t.Fatalf("expected excerpt to contain aurora, got %#v", result.Snippets[0])
	}
}

func TestServiceSearchHistorySessionArchiveFromTranscriptRefs(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{SessionsDir: filepath.Join(root, ".sessions"), TranscriptsDir: filepath.Join(root, ".transcripts")}
	if err := os.MkdirAll(cfg.TranscriptsDir, 0755); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}
	filename := "transcript_20260424_120000.json"
	writeTranscript(t, filepath.Join(cfg.TranscriptsDir, filename), []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "The PDF lives in ~/Documents/share/The.Go.Programming.Language.2015.11.pdf"),
	})

	service := NewService(cfg)
	runtime := service.Bind(fakeCurrent{refs: []string{filename}})
	result, err := runtime.SearchHistory(context.Background(), "session-1", automation.SessionContext{}, history.SearchRequest{
		Query: "Programming.Language",
		Scope: history.HistorySearchScopeSessionArchive,
	})
	if err != nil {
		t.Fatalf("search session archive: %v", err)
	}
	if result.MatchCount != 1 || len(result.Snippets) != 1 {
		t.Fatalf("expected one transcript hit, got %#v", result)
	}
	if got := result.Snippets[0].SourceKind; got != "transcript" {
		t.Fatalf("expected transcript source kind, got %q", got)
	}
}

func TestServiceSearchHistoryAllArchivesIncludesSessionsAndTranscripts(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{SessionsDir: filepath.Join(root, ".sessions"), TranscriptsDir: filepath.Join(root, ".transcripts")}
	if err := os.MkdirAll(cfg.SessionsDir, 0755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.MkdirAll(cfg.TranscriptsDir, 0755); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}

	writeSession(t, cfg.SessionsDir, "session-a", "Alpha session", time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC), []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Project aurora has a rollback checklist."),
	}, []string{"transcript_20260424_100000.json"})
	writeSession(t, cfg.SessionsDir, "session-b", "Beta session", time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC), []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Unrelated topic."),
	}, nil)
	writeTranscript(t, filepath.Join(cfg.TranscriptsDir, "transcript_20260424_100000.json"), []protocol.Message{
		protocol.NewTextMessage(protocol.RoleAssistant, "The aurora rollback checklist needs two approvers."),
	})

	service := NewService(cfg)
	runtime := service.Bind(nil)
	result, err := runtime.SearchHistory(context.Background(), "", automation.SessionContext{}, history.SearchRequest{
		Query: "rollback checklist",
		Scope: history.HistorySearchScopeAllArchives,
	})
	if err != nil {
		t.Fatalf("search all archives: %v", err)
	}
	if result.MatchCount == 0 || len(result.Snippets) == 0 {
		t.Fatalf("expected all_archives hits, got %#v", result)
	}
	first := result.Snippets[0]
	if first.SessionID != "session-a" || first.SessionTitle != "Alpha session" {
		t.Fatalf("expected transcript owner metadata, got %#v", first)
	}
}

func TestServiceSearchHistoryAllArchivesCreatesSQLiteSidecar(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{SessionsDir: filepath.Join(root, ".sessions"), TranscriptsDir: filepath.Join(root, ".transcripts")}
	if err := os.MkdirAll(cfg.SessionsDir, 0755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.MkdirAll(cfg.TranscriptsDir, 0755); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}
	writeSession(t, cfg.SessionsDir, "session-a", "Alpha session", time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC), []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Aurora deployment checklist."),
	}, []string{"transcript_20260424_100000.json"})
	writeTranscript(t, filepath.Join(cfg.TranscriptsDir, "transcript_20260424_100000.json"), []protocol.Message{
		protocol.NewTextMessage(protocol.RoleAssistant, "Aurora rollback requires two approvers."),
	})

	service := NewService(cfg)
	runtime := service.Bind(nil)
	result, err := runtime.SearchHistory(context.Background(), "", automation.SessionContext{}, history.SearchRequest{
		Query: "aurora",
		Scope: history.HistorySearchScopeAllArchives,
	})
	if err != nil {
		t.Fatalf("search all archives: %v", err)
	}
	if result.MatchCount == 0 {
		t.Fatalf("expected sidecar-backed hits, got %#v", result)
	}
	dbPath := filepath.Join(root, SidecarDBFileName)
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected history sqlite sidecar to exist: %v", err)
	}
	if got := countHistorySidecarRows(t, dbPath, "history_entries"); got < 2 {
		t.Fatalf("expected sidecar rows for session and transcript, got %d", got)
	}
}

func TestServiceSearchHistorySidecarRefreshesChangedTranscript(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{SessionsDir: filepath.Join(root, ".sessions"), TranscriptsDir: filepath.Join(root, ".transcripts")}
	if err := os.MkdirAll(cfg.TranscriptsDir, 0755); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}
	filename := "transcript_20260424_120000.json"
	path := filepath.Join(cfg.TranscriptsDir, filename)
	writeTranscript(t, path, []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Aurora release notes."),
	})

	service := NewService(cfg)
	runtime := service.Bind(fakeCurrent{refs: []string{filename}})
	first, err := runtime.SearchHistory(context.Background(), "session-1", automation.SessionContext{}, history.SearchRequest{
		Query: "aurora",
		Scope: history.HistorySearchScopeSessionArchive,
	})
	if err != nil {
		t.Fatalf("search initial transcript: %v", err)
	}
	if first.MatchCount != 1 {
		t.Fatalf("expected initial aurora hit, got %#v", first)
	}

	writeTranscript(t, path, []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Nebula release notes."),
	})
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("touch transcript: %v", err)
	}

	second, err := runtime.SearchHistory(context.Background(), "session-1", automation.SessionContext{}, history.SearchRequest{
		Query: "nebula",
		Scope: history.HistorySearchScopeSessionArchive,
	})
	if err != nil {
		t.Fatalf("search refreshed transcript: %v", err)
	}
	if second.MatchCount != 1 || !strings.Contains(strings.ToLower(second.Snippets[0].TextExcerpt), "nebula") {
		t.Fatalf("expected refreshed nebula hit, got %#v", second)
	}
	stale, err := runtime.SearchHistory(context.Background(), "session-1", automation.SessionContext{}, history.SearchRequest{
		Query: "aurora",
		Scope: history.HistorySearchScopeSessionArchive,
	})
	if err != nil {
		t.Fatalf("search stale transcript term: %v", err)
	}
	if stale.MatchCount != 0 {
		t.Fatalf("expected stale aurora hit to disappear after refresh, got %#v", stale)
	}
}

func TestServiceSearchHistoryFiltersInvisibleMessages(t *testing.T) {
	cfg := &config.Config{SessionsDir: filepath.Join(t.TempDir(), ".sessions"), TranscriptsDir: filepath.Join(t.TempDir(), ".transcripts")}
	service := NewService(cfg)
	runtime := service.Bind(fakeCurrent{
		messages: []protocol.Message{
			protocol.NewEphemeralTextMessage(protocol.KindMemory, "anchor should stay hidden"),
			{
				Role:     protocol.RoleUser,
				Content:  []protocol.Block{protocol.TextBlock("anchor should stay hidden too")},
				Metadata: &protocol.Metadata{Kind: protocol.KindBackground},
			},
			protocol.NewMessage(protocol.RoleAssistant, protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "echo anchor"})),
			protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-1", "anchor")),
			protocol.NewTextMessage(protocol.RoleAssistant, "public anchor remains visible"),
		},
	})

	result, err := runtime.SearchHistory(context.Background(), "session-1", automation.SessionContext{}, history.SearchRequest{
		Query: "anchor",
	})
	if err != nil {
		t.Fatalf("search history: %v", err)
	}
	if result.MatchCount != 1 || len(result.Snippets) != 1 {
		t.Fatalf("expected only one visible hit, got %#v", result)
	}
	if !strings.Contains(result.Snippets[0].TextExcerpt, "public anchor") {
		t.Fatalf("expected visible assistant text, got %#v", result.Snippets[0])
	}
}

func TestServiceSearchHistoryTruncatesAndSortsByRecency(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{SessionsDir: filepath.Join(root, ".sessions"), TranscriptsDir: filepath.Join(root, ".transcripts")}
	if err := os.MkdirAll(cfg.SessionsDir, 0755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	longText := strings.Repeat("prefix ", 40) + "aurora marker " + strings.Repeat("suffix ", 40)
	writeSession(t, cfg.SessionsDir, "newer", "Newer session", time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC), []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, longText),
	}, nil)
	writeSession(t, cfg.SessionsDir, "older", "Older session", time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC), []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, longText),
	}, nil)

	service := NewService(cfg)
	runtime := service.Bind(nil)
	result, err := runtime.SearchHistory(context.Background(), "", automation.SessionContext{}, history.SearchRequest{
		Query: "aurora",
		Scope: history.HistorySearchScopeAllArchives,
	})
	if err != nil {
		t.Fatalf("search all archives: %v", err)
	}
	if len(result.Snippets) < 2 {
		t.Fatalf("expected two hits, got %#v", result)
	}
	if result.Snippets[0].SessionID != "newer" {
		t.Fatalf("expected newer session first, got %#v", result.Snippets)
	}
	if got := utf8Len(result.Snippets[0].TextExcerpt); got > 241 {
		t.Fatalf("expected excerpt to be truncated, got len=%d excerpt=%q", got, result.Snippets[0].TextExcerpt)
	}
}

func writeSession(t *testing.T, sessionsDir, sessionID, title string, updatedAt time.Time, messages []protocol.Message, refs []string) {
	t.Helper()
	dir := filepath.Join(sessionsDir, sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	manifestData, err := json.MarshalIndent(storedManifest{
		SessionID: sessionID,
		Title:     title,
		UpdatedAt: updatedAt,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestData, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	stateData, err := json.MarshalIndent(storedSessionState{
		Messages:       messages,
		TranscriptRefs: refs,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), stateData, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func writeTranscript(t *testing.T, path string, messages []protocol.Message) {
	t.Helper()
	data, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		t.Fatalf("marshal transcript: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func utf8Len(value string) int {
	return len([]rune(value))
}

func countHistorySidecarRows(t *testing.T, dbPath, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open history sidecar db: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatalf("count sidecar rows: %v", err)
	}
	return count
}
