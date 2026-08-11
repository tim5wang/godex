package backend

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/protocol"
)

func TestReadTranscriptServesSessionOwnedArchive(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{})
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "transcript-owner"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	session.agent.RestoreStateForSession(opened.SessionID, agent.SessionState{
		Messages: []protocol.Message{
			protocol.NewSummaryMessage("Conversation compacted.", "transcript_owned.json"),
		},
		TranscriptRefs: []string{"transcript_owned.json"},
	})

	// Seed the archive file the ref points at.
	archived := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "original question before compaction"),
		protocol.NewTextMessage(protocol.RoleAssistant, "original long answer before compaction"),
	}
	data, err := json.MarshalIndent(archived, "", "  ")
	if err != nil {
		t.Fatalf("marshal archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.TranscriptsDir, "transcript_owned.json"), data, 0644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	messages, err := service.ReadTranscript(opened.SessionID, "transcript_owned.json")
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 archived messages, got %d", len(messages))
	}
	if got := protocol.MessageText(messages[0]); got != "original question before compaction" {
		t.Fatalf("unexpected first archived message: %q", got)
	}
}

func TestReadTranscriptRejectsForeignOrTraversalRefs(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{})
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "transcript-owner"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	session.agent.RestoreStateForSession(opened.SessionID, agent.SessionState{
		Messages: []protocol.Message{
			protocol.NewSummaryMessage("Conversation compacted.", "transcript_owned.json"),
		},
		TranscriptRefs: []string{"transcript_owned.json"},
	})

	cases := []string{
		"../transcript_owned.json",      // path traversal
		"/etc/passwd",                   // absolute path
		"transcript_other_session.json", // not owned by this session
		"",                              // empty
		".",                             // dot
	}
	for _, ref := range cases {
		if _, err := service.ReadTranscript(opened.SessionID, ref); !errors.Is(err, ErrTranscriptNotFound) {
			t.Fatalf("ref %q: expected ErrTranscriptNotFound, got %v", ref, err)
		}
	}
}

func TestReadTranscriptMissingSessionOrFile(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{})

	if _, err := service.ReadTranscript("does-not-exist", "transcript_owned.json"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "transcript-owner"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	session.agent.RestoreStateForSession(opened.SessionID, agent.SessionState{
		TranscriptRefs: []string{"transcript_missing.json"},
	})
	if _, err := service.ReadTranscript(opened.SessionID, "transcript_missing.json"); !errors.Is(err, ErrTranscriptNotFound) {
		t.Fatalf("expected ErrTranscriptNotFound for missing file, got %v", err)
	}
}
