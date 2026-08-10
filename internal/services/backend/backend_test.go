package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/notes"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/sessiongraph"
	"github.com/tim5wang/godex/internal/sessionstore"
	"github.com/tim5wang/godex/internal/tools"
)

type stubCaller struct {
	mu        sync.Mutex
	responses []protocol.Response
	requests  []protocol.Request
	calls     int
	started   chan struct{}
	block     chan struct{}
}

func (c *stubCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	if c.started != nil {
		select {
		case c.started <- struct{}{}:
		default:
		}
	}
	if c.block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.block:
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
	resp := c.responses[c.calls]
	c.calls++
	return &resp, nil
}

func TestOpenSessionReturnsStableIDForSameLocator(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	first, err := service.OpenSession(context.Background(), SessionLocator{Channel: "local", Key: "default"})
	if err != nil {
		t.Fatalf("open first session: %v", err)
	}
	second, err := service.OpenSession(context.Background(), SessionLocator{Channel: "local", Key: "default"})
	if err != nil {
		t.Fatalf("open second session: %v", err)
	}
	if first.SessionID != second.SessionID {
		t.Fatalf("expected stable session id, got %q and %q", first.SessionID, second.SessionID)
	}
}

func TestStableSessionIDIncludesProjectDirMetadata(t *testing.T) {
	a := stableSessionID(SessionLocator{
		Channel: "web",
		Key:     "default",
		Metadata: map[string]string{
			sessionProjectDirMetadataKey: "/tmp/project-a",
		},
	})
	b := stableSessionID(SessionLocator{
		Channel: "web",
		Key:     "default",
		Metadata: map[string]string{
			sessionProjectDirMetadataKey: "/tmp/project-b",
		},
	})
	if a == b {
		t.Fatalf("expected project-scoped session IDs to differ, both got %q", a)
	}
}

// TestStableSessionIDNormalisesProjectDir asserts that the
// session id is stable across surface-level variations of the
// same physical project directory.  Without normalisation,
// "/a/b" vs "/a/b/" vs "/a/./b" would each hash to a
// different id, so a user running the same session from
// different shells, IDEs, or CI scripts would get a brand-new
// session every time and their old history would seem to
// disappear.
func TestStableSessionIDNormalisesProjectDir(t *testing.T) {
	base := stableSessionID(SessionLocator{
		Channel: "local",
		Key:     "default",
		Metadata: map[string]string{
			sessionProjectDirMetadataKey: "/Users/foo/proj",
		},
	})
	variants := []struct {
		name, projectDir string
	}{
		{"trailing slash", "/Users/foo/proj/"},
		{"double slash", "/Users//foo/proj"},
		{"dot segment", "/Users/foo/./proj"},
		{"parent traversal that lands on base", "/Users/foo/sub/../proj"},
		{"trailing whitespace", "  /Users/foo/proj  "},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			got := stableSessionID(SessionLocator{
				Channel: "local",
				Key:     "default",
				Metadata: map[string]string{
					sessionProjectDirMetadataKey: v.projectDir,
				},
			})
			if got != base {
				t.Fatalf("variant %q (raw %q) produced id %q, want %q", v.name, v.projectDir, got, base)
			}
		})
	}
}

// TestStableSessionIDEmptyProjectDirStable asserts that an
// empty project dir (e.g. a CLI invocation with no
// workspace and no GODEX_PROJECT_DIR) still hashes
// deterministically — a missing directory must not produce
// a different id on each call.
func TestStableSessionIDEmptyProjectDirStable(t *testing.T) {
	a := stableSessionID(SessionLocator{Channel: "local", Key: "default"})
	b := stableSessionID(SessionLocator{Channel: "local", Key: "default"})
	if a != b {
		t.Fatalf("empty project dir must produce stable id, got %q then %q", a, b)
	}
}

func TestOpenSessionDoesNotListOrPersistEmptySession(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "empty"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionsDir, opened.SessionID)); !os.IsNotExist(err) {
		t.Fatalf("expected unopened empty session not to be persisted, got %v", err)
	}
	listed, err := service.ListSessions(context.Background(), SessionListFilter{Channel: "web"})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("expected empty opened session to stay out of list, got %#v", listed)
	}
	if err := service.DeleteSession(context.Background(), opened.SessionID); err != nil {
		t.Fatalf("delete in-memory empty session: %v", err)
	}
	reopened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "empty"})
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	if reopened.SessionID != opened.SessionID {
		t.Fatalf("expected same stable session id, got %q and %q", opened.SessionID, reopened.SessionID)
	}
	listed, err = service.ListSessions(context.Background(), SessionListFilter{Channel: "web"})
	if err != nil {
		t.Fatalf("list sessions after reopen: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("expected reopened empty session to stay out of list, got %#v", listed)
	}
}

func TestOpenSessionIgnoresLocatorMetadataForIdentity(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	first, err := service.OpenSession(context.Background(), SessionLocator{
		Channel:  "feishu",
		Key:      "oc_chat_1",
		UserID:   "ou_user_1",
		Metadata: map[string]string{"message_id": "om_1"},
	})
	if err != nil {
		t.Fatalf("open first session: %v", err)
	}
	second, err := service.OpenSession(context.Background(), SessionLocator{
		Channel:  "feishu",
		Key:      "oc_chat_1",
		UserID:   "ou_user_1",
		Metadata: map[string]string{"message_id": "om_2"},
	})
	if err != nil {
		t.Fatalf("open second session: %v", err)
	}
	if first.SessionID != second.SessionID {
		t.Fatalf("expected stable session id across varying metadata, got %q and %q", first.SessionID, second.SessionID)
	}
}

func TestRuntimeContextResolvesAgentProfileByEntrypoint(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.AgentProfile = config.AgentProfileGeneral
	cfg.AgentDefaultProfiles = config.AgentDefaultProfilesConfig{
		ACP:    config.AgentProfileCoding,
		CLI:    config.AgentProfileCoding,
		TUI:    config.AgentProfileCoding,
		Web:    config.AgentProfileGeneral,
		Weixin: config.AgentProfileGeneral,
		Feishu: config.AgentProfileGeneral,
	}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	acpCtx := service.buildRuntimeContext("session", SessionLocator{Channel: "acp", Key: "ide"}, message.NewTextEnvelope(message.SourceACP, "session", "user", "hi", time.Now()))
	if acpCtx.AgentProfile != config.AgentProfileCoding {
		t.Fatalf("expected acp profile %q, got %q", config.AgentProfileCoding, acpCtx.AgentProfile)
	}
	webCtx := service.buildRuntimeContext("session", SessionLocator{Channel: "web", Key: "chat"}, message.NewTextEnvelope(message.SourceWeb, "session", "user", "hi", time.Now()))
	if webCtx.AgentProfile != config.AgentProfileGeneral {
		t.Fatalf("expected web profile %q, got %q", config.AgentProfileGeneral, webCtx.AgentProfile)
	}
	envelope := message.NewTextEnvelope(message.SourceWeb, "session", "user", "hi", time.Now())
	envelope.Metadata = map[string]string{"agent_profile": "coding"}
	overrideCtx := service.buildRuntimeContext("session", SessionLocator{Channel: "web", Key: "chat"}, envelope)
	if overrideCtx.AgentProfile != config.AgentProfileCoding {
		t.Fatalf("expected envelope profile override %q, got %q", config.AgentProfileCoding, overrideCtx.AgentProfile)
	}
}

func TestSubmitPersistsSessionStateAndRestoresIt(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "local", Key: "default"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewCLIEnvelope(opened.SessionID, cfg.LeadName, "hello", time.Now())); err != nil {
		t.Fatalf("submit: %v", err)
	}

	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Messages) != 2 {
		t.Fatalf("expected user and assistant messages, got %d", len(snapshot.Messages))
	}

	restored := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})
	reopened, err := restored.OpenSession(context.Background(), SessionLocator{Channel: "local", Key: "default"})
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	restoredSnapshot, err := restored.Snapshot(context.Background(), reopened.SessionID)
	if err != nil {
		t.Fatalf("restored snapshot: %v", err)
	}
	if len(restoredSnapshot.Messages) != 2 {
		t.Fatalf("expected restored messages, got %d", len(restoredSnapshot.Messages))
	}
	if got := protocol.MessageText(restoredSnapshot.Messages[0]); got != "hello" {
		t.Fatalf("expected restored user message, got %q", got)
	}
}

func TestSubmitInjectsNoteContextForModelAndKeepsDisplayText(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := newTestService(cfg, caller)
	note, err := service.SaveNote(context.Background(), notes.SaveInput{
		Title:   "Spaced repetition plan",
		Summary: "Review schedule",
		Tags:    []string{"learning"},
		Content: "Use 1d, 3d, 7d review checkpoints.",
	})
	if err != nil {
		t.Fatalf("save note: %v", err)
	}

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "notes"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	envelope := message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "帮我整理复习计划", time.Now())
	envelope.Metadata = map[string]string{"note_id": note.ID}
	if _, err := service.Submit(context.Background(), opened.SessionID, envelope); err != nil {
		t.Fatalf("submit: %v", err)
	}

	caller.mu.Lock()
	if len(caller.requests) != 1 {
		t.Fatalf("expected one model request, got %d", len(caller.requests))
	}
	modelText := apiMessagesText(caller.requests[0].Messages)
	caller.mu.Unlock()
	if !strings.Contains(modelText, "Current note context:") ||
		!strings.Contains(modelText, "Spaced repetition plan") ||
		!strings.Contains(modelText, "Use 1d, 3d, 7d review checkpoints.") ||
		!strings.Contains(modelText, "User request:\n帮我整理复习计划") {
		t.Fatalf("expected model request to include note context and user request, got %q", modelText)
	}

	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Messages) == 0 || snapshot.Messages[0].Metadata == nil {
		t.Fatalf("expected user metadata in snapshot, got %+v", snapshot.Messages)
	}
	if got := snapshot.Messages[0].Metadata.Text; got != "帮我整理复习计划" {
		t.Fatalf("expected display text to stay user-authored, got %q", got)
	}
	if snapshot.Messages[0].Metadata.AppObjectType != "note" || snapshot.Messages[0].Metadata.AppObjectID != note.ID {
		t.Fatalf("expected note app object metadata, got %+v", snapshot.Messages[0].Metadata)
	}
}

func TestProjectLedgerUpdatesAfterCompletedTurn(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("implemented first slice and next run tests")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "ledger"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "build a small app", time.Now())); err != nil {
		t.Fatalf("submit: %v", err)
	}
	ledger, err := service.ProjectLedger(opened.SessionID)
	if err != nil {
		t.Fatalf("project ledger: %v", err)
	}
	if ledger.Goal != "build a small app" {
		t.Fatalf("expected user goal in ledger, got %+v", ledger)
	}
	if !strings.Contains(ledger.Compact, "implemented first slice") {
		t.Fatalf("expected assistant handoff in compact ledger, got %q", ledger.Compact)
	}
	result, err := service.ExecuteCommand(context.Background(), opened.SessionID, commands.Command{Name: "ledger"})
	if err != nil {
		t.Fatalf("ledger command: %v", err)
	}
	if !strings.Contains(result.Output, "build a small app") {
		t.Fatalf("expected ledger command output, got %q", result.Output)
	}
}

func TestSubmitBridgesInsightsIntoMemoryCandidates(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := newTestService(cfg, caller)
	service.analyze = func(input insights.Input) (*insights.Report, error) {
		return &insights.Report{
			Frictions: []string{
				"Model/API timeouts are recurring and should be treated as a first-class runtime friction.",
			},
		}, nil
	}

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "memory-bridge"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "hello", time.Now())); err != nil {
		t.Fatalf("submit: %v", err)
	}

	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	candidates, err := session.agent.MemoryMgr().ListCandidates()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatalf("expected insights bridge to add memory candidate, got %+v", candidates)
	}
	found := false
	for _, candidate := range candidates {
		if candidate.Type == memory.TypeWarning && candidate.Source == "insights-bridge" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected warning candidate from insights bridge, got %+v", candidates)
	}
}

func TestContextInspectorAggregatesMemoryPreviewAndHistoryRecall(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "context-inspector"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	if _, err := service.memoryManager().Remember(memory.SaveInput{
		Title:   "Project Identity",
		Summary: "Shared backend for web, TUI, and IM.",
		Content: "This workspace centers on a shared Godex backend that serves Web, TUI, and IM channels.",
		Type:    memory.TypeIdentity,
		Source:  "manual-web",
	}); err != nil {
		t.Fatalf("remember identity: %v", err)
	}
	if _, err := service.memoryManager().Remember(memory.SaveInput{
		Title:   "Weixin attachments",
		Summary: "Weixin attachment delivery needs careful protocol handling.",
		Content: "When debugging weixin attachment delivery, prioritize artifact persistence and channel payload branches.",
		Type:    memory.TypeProject,
		Source:  "timeline-bridge",
		Tags:    []string{"weixin"},
	}); err != nil {
		t.Fatalf("remember project memory: %v", err)
	}

	session.agent.RestoreStateForSession(opened.SessionID, agent.SessionState{
		Messages: []protocol.Message{
			protocol.NewSummaryMessage("Conversation compacted.", "transcript_context_inspector.json"),
			protocol.NewTextMessage(protocol.RoleUser, "please debug the weixin attachment delivery flow"),
		},
		TranscriptRefs: []string{"transcript_context_inspector.json"},
	})
	session.timeline.Seed([]events.Event{{
		SessionID: opened.SessionID,
		Type:      events.EventHistoryRecallDecision,
		Timestamp: time.Now(),
		Payload: events.HistoryRecallPayload{
			AllowTool:        true,
			Automatic:        true,
			ExplicitRequest:  false,
			RecommendedScope: tools.HistorySearchScopeSessionArchive,
			Score:            4,
			Reasons:          []string{"implicit history cue", "session has transcript archives"},
		},
	}})

	inspector, err := service.ContextInspector(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("context inspector: %v", err)
	}
	if inspector.Context.SessionID != opened.SessionID {
		t.Fatalf("expected context inspector session %q, got %#v", opened.SessionID, inspector.Context)
	}
	if inspector.Context.TokenEstimate != inspector.Context.TokenBreakdown.Total || inspector.Context.TotalTokenEstimate != inspector.Context.TokenBreakdown.Total {
		t.Fatalf("expected context inspector token totals to match breakdown, got %+v", inspector.Context)
	}
	if inspector.Context.TokenBreakdown.System == 0 || inspector.Context.TokenBreakdown.History == 0 {
		t.Fatalf("expected context inspector breakdown, got %+v", inspector.Context.TokenBreakdown)
	}
	if inspector.TranscriptRefCount != 1 || len(inspector.TranscriptRefs) != 1 {
		t.Fatalf("expected transcript refs, got %+v", inspector)
	}
	if !strings.Contains(inspector.RecallQuery, "weixin attachment delivery flow") {
		t.Fatalf("expected recall query summary, got %q", inspector.RecallQuery)
	}
	if len(inspector.MemoryPreview.Identity) == 0 {
		t.Fatalf("expected identity memory preview, got %+v", inspector.MemoryPreview)
	}
	if len(inspector.MemoryPreview.Core) == 0 && len(inspector.MemoryPreview.Relevant) == 0 {
		t.Fatalf("expected scoped memory preview, got %+v", inspector.MemoryPreview)
	}
	if inspector.HistoryRecall == nil {
		t.Fatalf("expected latest history recall decision, got nil")
	}
	if !inspector.HistoryRecall.AllowTool || inspector.HistoryRecall.RecommendedScope != tools.HistorySearchScopeSessionArchive {
		t.Fatalf("unexpected history recall summary: %+v", inspector.HistoryRecall)
	}
}

func TestContextInspectorDegradesWithoutUserQueryOrHistoryDecision(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "context-inspector-empty"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	session.agent.RestoreStateForSession(opened.SessionID, agent.SessionState{
		Messages: []protocol.Message{
			protocol.NewTextMessage(protocol.RoleAssistant, "ready"),
		},
	})
	session.timeline.Seed(nil)

	inspector, err := service.ContextInspector(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("context inspector: %v", err)
	}
	if inspector.RecallQuery != "" {
		t.Fatalf("expected empty recall query, got %q", inspector.RecallQuery)
	}
	if inspector.HistoryRecall != nil {
		t.Fatalf("expected no history recall decision, got %+v", inspector.HistoryRecall)
	}
	if len(inspector.MemoryPreview.Relevant) != 0 {
		t.Fatalf("expected empty relevant preview, got %+v", inspector.MemoryPreview.Relevant)
	}
}

func TestStoreAttachmentPersistsMessageMetadataAndRestoresIt(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "attachments"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	attachment, err := service.StoreAttachment(context.Background(), opened.SessionID, AttachmentUpload{
		Name:     "notes.txt",
		MIMEType: "text/plain",
		Reader:   bytes.NewBufferString("hello"),
	})
	if err != nil {
		t.Fatalf("store attachment: %v", err)
	}

	envelope := message.Envelope{
		Source:      message.SourceWeb,
		SessionID:   opened.SessionID,
		Sender:      cfg.LeadName,
		Text:        "please inspect attachment",
		Content:     "please inspect attachment",
		Attachments: []message.AttachmentRef{attachment},
		Timestamp:   time.Now(),
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, envelope); err != nil {
		t.Fatalf("submit with attachment: %v", err)
	}

	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Messages) == 0 || snapshot.Messages[0].Metadata == nil || len(snapshot.Messages[0].Metadata.Attachments) != 1 {
		t.Fatalf("expected attachment metadata in snapshot, got %+v", snapshot.Messages)
	}

	restored := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})
	reopened, err := restored.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "attachments"})
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	restoredSnapshot, err := restored.Snapshot(context.Background(), reopened.SessionID)
	if err != nil {
		t.Fatalf("restored snapshot: %v", err)
	}
	if got := restoredSnapshot.Messages[0].Metadata.Attachments[0].Name; got != "notes.txt" {
		t.Fatalf("expected restored attachment metadata, got %q", got)
	}
	if _, absolutePath, err := restored.ResolveAttachment(reopened.SessionID, attachment.ID); err != nil {
		t.Fatalf("resolve attachment after restore: %v", err)
	} else if _, statErr := os.Stat(absolutePath); statErr != nil {
		t.Fatalf("stat stored attachment: %v", statErr)
	}
}

func TestStoreAttachmentRejectsOversizedUpload(t *testing.T) {
	previousLimit := maxAttachmentBytes
	maxAttachmentBytes = 5
	t.Cleanup(func() {
		maxAttachmentBytes = previousLimit
	})

	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "oversized-attachment"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	_, err = service.StoreAttachment(context.Background(), opened.SessionID, AttachmentUpload{
		Name:     "large.txt",
		MIMEType: "text/plain",
		Reader:   strings.NewReader("123456"),
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds max size") {
		t.Fatalf("expected oversized attachment error, got %v", err)
	}
	entries, err := os.ReadDir(service.sessionAttachmentsDir(opened.SessionID))
	if err != nil {
		t.Fatalf("read attachments dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected oversized attachment to be removed, got %d files", len(entries))
	}
}

func TestPostRuntimeReplyWithArtifactPathsPersistsAttachments(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "runtime-artifacts"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	artifactPath := filepath.Join(t.TempDir(), "capture.png")
	if err := os.WriteFile(artifactPath, []byte("png"), 0644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := service.PostRuntimeReplyWithArtifactPaths(context.Background(), opened.SessionID, "generated screenshot", []string{artifactPath}); err != nil {
		t.Fatalf("post runtime reply with artifacts: %v", err)
	}

	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	found := false
	for _, msg := range snapshot.Messages {
		if msg.Role != protocol.RoleAssistant || msg.Metadata == nil || len(msg.Metadata.Attachments) == 0 {
			continue
		}
		found = true
		if msg.Metadata.Kind != protocol.KindBackground {
			t.Fatalf("expected background kind, got %#v", msg.Metadata)
		}
		if got := msg.Metadata.Attachments[0].Name; got != "capture.png" {
			t.Fatalf("expected capture.png attachment, got %q", got)
		}
	}
	if !found {
		t.Fatalf("expected attachment-bearing runtime reply in snapshot: %#v", snapshot.Messages)
	}
}

func TestPostRuntimeReplyWithArtifactPathsPersistsFileAttachments(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "runtime-file-artifacts"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	artifactPath := filepath.Join(t.TempDir(), "manual.pdf")
	if err := os.WriteFile(artifactPath, []byte("%PDF-1.7 fake bytes"), 0644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := service.PostRuntimeReplyWithArtifactPaths(context.Background(), opened.SessionID, "generated file", []string{artifactPath}); err != nil {
		t.Fatalf("post runtime reply with artifacts: %v", err)
	}

	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	found := false
	for _, msg := range snapshot.Messages {
		if msg.Role != protocol.RoleAssistant || msg.Metadata == nil || len(msg.Metadata.Attachments) == 0 {
			continue
		}
		found = true
		if got := msg.Metadata.Attachments[0].Name; got != "manual.pdf" {
			t.Fatalf("expected manual.pdf attachment, got %q", got)
		}
		if _, absolutePath, err := service.ResolveAttachment(opened.SessionID, msg.Metadata.Attachments[0].ID); err != nil {
			t.Fatalf("resolve persisted file attachment: %v", err)
		} else if _, statErr := os.Stat(absolutePath); statErr != nil {
			t.Fatalf("stat persisted file attachment: %v", statErr)
		}
	}
	if !found {
		t.Fatalf("expected file attachment-bearing runtime reply in snapshot: %#v", snapshot.Messages)
	}
}

func TestSnapshotIncludesPendingPermissionsAndApprovalClearsThem(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "command -v sh"})}},
		{Content: []protocol.Block{protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "command -v sh"})}},
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "permissions"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	result, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "run command -v sh", time.Now()))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.PendingPermissions) != 1 {
		t.Fatalf("expected one pending permission, got %+v", snapshot.PendingPermissions)
	}
	if snapshot.PendingPermissions[0].Request.ToolName != "bash" {
		t.Fatalf("unexpected pending permission: %+v", snapshot.PendingPermissions[0])
	}
	if snapshot.PendingPermissions[0].Request.TurnID != result.TurnID {
		t.Fatalf("expected pending permission to carry turn id %q, got %+v", result.TurnID, snapshot.PendingPermissions[0].Request)
	}
	if snapshot.ActivePermissionBlocker == nil || snapshot.ActivePermissionBlocker.RequestID != snapshot.PendingPermissions[0].ID {
		t.Fatalf("expected active permission blocker for pending request, got %+v", snapshot.ActivePermissionBlocker)
	}
	if snapshot.ActivePermissionBlocker.Status != tools.PermissionStatusPending {
		t.Fatalf("expected pending blocker status, got %+v", snapshot.ActivePermissionBlocker)
	}
	if len(snapshot.Turns) == 0 || snapshot.Turns[len(snapshot.Turns)-1].BlockedByPermissionID != snapshot.PendingPermissions[0].ID || snapshot.Turns[len(snapshot.Turns)-1].PermissionStatus != tools.PermissionStatusPending {
		t.Fatalf("expected pending turn permission status, got %+v", snapshot.Turns)
	}

	resolution, err := service.ApprovePermission(context.Background(), opened.SessionID, snapshot.PendingPermissions[0].ID, tools.PermissionGrantSession)
	if err != nil {
		t.Fatalf("approve permission: %v", err)
	}
	if !resolution.Resumed || resolution.ResumeStatus != "completed" || resolution.ResumeOutput != "done" {
		t.Fatalf("expected resumed approval result, got %+v", resolution)
	}
	if strings.TrimSpace(resolution.ResumeTurnID) == "" {
		t.Fatalf("expected resumed approval to include resume turn id, got %+v", resolution)
	}
	snapshot, err = service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot after approval: %v", err)
	}
	if len(snapshot.PendingPermissions) != 0 {
		t.Fatalf("expected approvals to clear pending queue, got %+v", snapshot.PendingPermissions)
	}
	if snapshot.ActivePermissionBlocker != nil {
		t.Fatalf("expected approvals to clear active blocker, got %+v", snapshot.ActivePermissionBlocker)
	}
	if len(snapshot.Turns) == 0 || snapshot.Turns[0].PermissionStatus != tools.PermissionStatusResumed {
		t.Fatalf("expected resumed turn permission status, got %+v", snapshot.Turns)
	}
	audit, err := service.SecurityAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("security audit: %v", err)
	}
	foundApprovalAudit := false
	for _, event := range audit {
		if event.Action == "approve_permission" && event.Metadata["request_id"] == resolution.RequestID && event.Metadata["scope"] == string(tools.PermissionGrantSession) {
			foundApprovalAudit = true
			break
		}
	}
	if !foundApprovalAudit {
		t.Fatalf("expected approval audit event, got %+v", audit)
	}
	if got := protocol.MessageText(snapshot.Messages[len(snapshot.Messages)-1]); got != "done" {
		t.Fatalf("expected resumed assistant reply, got %q", got)
	}

	restored := newTestService(cfg, &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "command -v sh"})}},
	}})
	reopened, err := restored.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "permissions"})
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	session, err := restored.requireSession(reopened.SessionID)
	if err != nil {
		t.Fatalf("require reopened session: %v", err)
	}
	exported := session.agent.ExportStateForSession(reopened.SessionID)
	if len(exported.PermissionState.Overrides) != 1 {
		t.Fatalf("expected restored approval override, got %+v", exported.PermissionState)
	}
}

func TestDenyPermissionClearsPendingResumeAndAddsRecoveryFeedback(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "command -v sh"})}},
		{Content: []protocol.Block{protocol.TextBlock("continued safely")}},
	}}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "permissions-deny"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	result, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "run command -v sh", time.Now()))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result == nil || !result.PendingApproval || result.PendingRequestID == "" {
		t.Fatalf("expected pending approval result, got %+v", result)
	}

	if _, err := service.DenyPermission(context.Background(), opened.SessionID, result.PendingRequestID, "not needed"); err != nil {
		t.Fatalf("deny permission: %v", err)
	}
	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot after deny: %v", err)
	}
	if len(snapshot.PendingPermissions) != 0 {
		t.Fatalf("expected denied permission to clear pending queue, got %+v", snapshot.PendingPermissions)
	}
	if snapshot.ActivePermissionBlocker != nil {
		t.Fatalf("expected denied permission to clear active blocker, got %+v", snapshot.ActivePermissionBlocker)
	}
	if len(snapshot.Turns) == 0 || snapshot.Turns[len(snapshot.Turns)-1].PermissionStatus != tools.PermissionStatusDenied {
		t.Fatalf("expected denied turn permission status, got %+v", snapshot.Turns)
	}
	foundFeedback := false
	for _, msg := range snapshot.Messages {
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindBackground && strings.Contains(protocol.MessageText(msg), "previously blocked tool permission was denied") {
			foundFeedback = true
			break
		}
	}
	if !foundFeedback {
		t.Fatalf("expected denial recovery feedback in model messages, got %+v", snapshot.Messages)
	}

	continued, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "继续", time.Now()))
	if err != nil {
		t.Fatalf("continue after deny: %v", err)
	}
	if continued == nil || continued.Status != "completed" {
		t.Fatalf("expected continue after deny to complete, got %+v", continued)
	}
}

func TestExpiredPendingPermissionClearsResumeBeforeContinue(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Tools.Permissions.InteractiveApprovalEnabled = true
	cfg.Tools.Permissions.PendingTTLSeconds = 1
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "command -v sh"})}},
		{Content: []protocol.Block{protocol.TextBlock("continued after expiry")}},
	}}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "permissions-expire"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	result, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "run command -v sh", time.Now()))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result == nil || !result.PendingApproval || result.PendingRequestID == "" {
		t.Fatalf("expected pending approval result, got %+v", result)
	}

	time.Sleep(1100 * time.Millisecond)

	continued, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "继续", time.Now()))
	if err != nil {
		t.Fatalf("continue after expiry: %v", err)
	}
	if continued == nil || continued.Status != "completed" {
		t.Fatalf("expected continue after expired approval to complete, got %+v", continued)
	}
	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot after expiry continue: %v", err)
	}
	if len(snapshot.PendingPermissions) != 0 || snapshot.ActivePermissionBlocker != nil {
		t.Fatalf("expected expired permission to clear blockers, pending=%+v active=%+v", snapshot.PendingPermissions, snapshot.ActivePermissionBlocker)
	}
	if len(snapshot.Turns) < 2 || snapshot.Turns[0].PermissionStatus != tools.PermissionStatusExpired {
		t.Fatalf("expected original turn marked expired, got %+v", snapshot.Turns)
	}
	if !containsBackgroundText(snapshot.Messages, "previously blocked tool permission expired") {
		t.Fatalf("expected expiry recovery feedback in messages, got %+v", snapshot.Messages)
	}
}

func TestSnapshotDisplayMessagesExpandCompactedTranscript(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("summary")}}}})
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "compact-display"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	session.agent.AddEnvelope(message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "first line\n- keep bullet", time.Unix(10, 0)))
	session.agent.AppendAssistantText("assistant reply", "")
	if _, err := session.agent.CompactConversation(); err != nil {
		t.Fatalf("compact conversation: %v", err)
	}

	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Messages) == 0 || snapshot.Messages[0].Metadata == nil || snapshot.Messages[0].Metadata.Kind != protocol.KindSummary {
		t.Fatalf("expected model-visible messages to stay compacted, got %+v", snapshot.Messages)
	}
	// Snapshot display messages mirror the (reasoning-trimmed) raw conversation.
	// Transcripts are no longer expanded inline: a compacted session's archive
	// can hold thousands of messages (10+ MB of JSON), which made every snapshot
	// — and every refresh during a running turn — slow on remote connections.
	if len(snapshot.DisplayMessages) != len(snapshot.Messages) {
		t.Fatalf("expected display messages to mirror the raw conversation, got %d vs %d", len(snapshot.DisplayMessages), len(snapshot.Messages))
	}
	if snapshot.DisplayMessages[0].Metadata == nil || snapshot.DisplayMessages[0].Metadata.Kind != protocol.KindSummary {
		t.Fatalf("expected display messages to stay compacted, got %+v", snapshot.DisplayMessages[0])
	}
}

func TestSnapshotTrimsReasoningContent(t *testing.T) {
	withReasoning := protocol.NewTextMessage(protocol.RoleAssistant, "reply")
	withReasoning.Metadata = &protocol.Metadata{ReasoningContent: "secret reasoning transcript"}
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "hello"),
		withReasoning,
	}
	out := snapshotDisplayMessages(messages)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	if out[1].Metadata == nil || out[1].Metadata.ReasoningContent != "" {
		t.Fatalf("expected reasoning_content trimmed from snapshot messages, got %+v", out[1].Metadata)
	}
	// The original agent-owned messages must be left untouched.
	if messages[1].Metadata == nil || messages[1].Metadata.ReasoningContent != "secret reasoning transcript" {
		t.Fatalf("snapshot trimming must not mutate the source messages")
	}
}

func TestPendingPermissionsPersistAcrossServiceRestart(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "command -v sh"})}},
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "permissions-restart"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewRuntimeEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "show command -v sh", time.Now(), nil)); err != nil {
		t.Fatalf("submit: %v", err)
	}

	restoredCaller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "command -v sh"})}},
		{Content: []protocol.Block{protocol.TextBlock("done after restart")}},
	}}
	restored := newTestService(cfg, restoredCaller)
	reopened, err := restored.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "permissions-restart"})
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	if reopened.SessionID != opened.SessionID {
		t.Fatalf("expected stable session id, got %q want %q", reopened.SessionID, opened.SessionID)
	}

	snapshot, err := restored.Snapshot(context.Background(), reopened.SessionID)
	if err != nil {
		t.Fatalf("snapshot after restore: %v", err)
	}
	if len(snapshot.PendingPermissions) != 1 || snapshot.PendingPermissions[0].Request.ToolName != "bash" {
		t.Fatalf("expected restored pending permission, got %+v", snapshot.PendingPermissions)
	}
	state, err := restored.readSessionState(reopened.SessionID)
	if err != nil {
		t.Fatalf("read session state: %v", err)
	}
	if state.PendingResume == nil || state.PendingResume.RequestID == "" {
		t.Fatalf("expected persisted pending resume state, got %+v", state.PendingResume)
	}

	resolution, err := restored.ApprovePermission(context.Background(), reopened.SessionID, snapshot.PendingPermissions[0].ID, tools.PermissionGrantSession)
	if err != nil {
		t.Fatalf("approve restored pending permission: %v", err)
	}
	if !resolution.Resumed || resolution.ResumeStatus != "completed" || resolution.ResumeOutput != "done after restart" {
		t.Fatalf("expected restored approval to resume blocked turn, got %+v", resolution)
	}
	after, err := restored.Snapshot(context.Background(), reopened.SessionID)
	if err != nil {
		t.Fatalf("snapshot after restored approval: %v", err)
	}
	if len(after.PendingPermissions) != 0 {
		t.Fatalf("expected restored pending approval to clear, got %+v", after.PendingPermissions)
	}
	if got := protocol.MessageText(after.Messages[len(after.Messages)-1]); got != "done after restart" {
		t.Fatalf("expected resumed assistant output after restart, got %q", got)
	}
	if restoredCaller.calls != 2 {
		t.Fatalf("expected restored caller to replay tool turn and final assistant turn, got %d calls", restoredCaller.calls)
	}
}

func TestApprovePermissionIncludesResumedOutputWhenAssistantPlaceholderIsUpdated(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "command -v sh"})}},
		{Content: []protocol.Block{protocol.TextBlock("command output")}},
	}}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "weixin", Key: "permissions-resume-output", UserID: "wx-user-1"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeixin, opened.SessionID, "wx-user-1", "run command -v sh", time.Now())); err != nil {
		t.Fatalf("submit: %v", err)
	}

	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.PendingPermissions) != 1 {
		t.Fatalf("expected one pending permission, got %+v", snapshot.PendingPermissions)
	}

	resolution, err := service.ApprovePermission(context.Background(), opened.SessionID, snapshot.PendingPermissions[0].ID, tools.PermissionGrantSession)
	if err != nil {
		t.Fatalf("approve permission: %v", err)
	}
	if !resolution.Resumed || resolution.ResumeStatus != "completed" || resolution.ResumeOutput != "command output" {
		t.Fatalf("expected resumed approval output, got %+v", resolution)
	}
}

func TestSubmitReturnsPendingApprovalStatusWithoutHTTPFailure(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.TextBlock("I need approval to continue."),
			protocol.ToolUseBlock("tool-1", "write_file", map[string]interface{}{"path": "notes/todo.txt", "content": "hello"}),
		}},
	}}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "pending-status"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	result, err := service.Submit(context.Background(), opened.SessionID, message.NewRuntimeEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "write it", time.Now(), nil))
	if err != nil {
		t.Fatalf("submit should not fail on pending approval: %v", err)
	}
	if result == nil || !result.PendingApproval || result.Status != "pending_approval" || result.PendingRequestID == "" || result.Completed {
		t.Fatalf("unexpected submit result: %+v", result)
	}

	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.PendingPermissions) != 1 || snapshot.PendingPermissions[0].ID != result.PendingRequestID {
		t.Fatalf("expected pending approval in snapshot, got %+v", snapshot.PendingPermissions)
	}
}

func TestSessionGateCancelsWaitingRequests(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		responses: []protocol.Response{
			{Content: []protocol.Block{protocol.TextBlock("done")}},
			{Content: []protocol.Block{protocol.TextBlock("second done")}},
		},
		started: make(chan struct{}, 1),
		block:   make(chan struct{}),
	}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "local", Key: "default"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := service.Submit(context.Background(), opened.SessionID, message.NewCLIEnvelope(opened.SessionID, cfg.LeadName, "hello", time.Now()))
		firstDone <- err
	}()

	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first submit to start")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = service.ExecuteCommand(waitCtx, opened.SessionID, commands.Command{Name: "help"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected waiting command to honor context cancellation, got %v", err)
	}

	close(caller.block)
	if err := <-firstDone; err != nil {
		t.Fatalf("expected first submit to finish successfully, got %v", err)
	}
}

func TestSubmitAsyncReturnsAcceptedTurnAndContinuesAfterRequestContextCanceled(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		responses: []protocol.Response{
			{Content: []protocol.Block{protocol.TextBlock("done")}},
			{Content: []protocol.Block{protocol.TextBlock("second done")}},
		},
		started: make(chan struct{}, 1),
		block:   make(chan struct{}),
	}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "async"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	requestCtx, cancel := context.WithCancel(context.Background())
	result, err := service.SubmitAsync(requestCtx, opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "hello", time.Now()))
	if err != nil {
		t.Fatalf("submit async: %v", err)
	}
	if result.Completed || result.Status != "running" || result.TurnID == "" {
		t.Fatalf("expected accepted running turn, got %+v", result)
	}
	cancel()

	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}

	runningSnapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("running snapshot: %v", err)
	}
	if !runningSnapshot.Running {
		t.Fatalf("expected session to be running, got %+v", runningSnapshot)
	}
	if got := turnRecordStatus(runningSnapshot.Turns, result.TurnID); got != "running" {
		t.Fatalf("expected running turn record, got %q in %+v", got, runningSnapshot.Turns)
	}

	close(caller.block)
	finished := waitForBackendSnapshot(t, service, opened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running && len(snapshot.Messages) >= 2
	})
	if got := protocol.MessageText(finished.Messages[len(finished.Messages)-1]); got != "done" {
		t.Fatalf("expected async turn to finish after request cancel, got %q", got)
	}
	if got := turnRecordStatus(finished.Turns, result.TurnID); got != "completed" {
		t.Fatalf("expected completed turn record, got %q in %+v", got, finished.Turns)
	}
}

func TestSubmitAsyncInjectsBusySessionFollowUp(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		responses: []protocol.Response{
			{Content: []protocol.Block{protocol.TextBlock("done")}},
			{Content: []protocol.Block{protocol.TextBlock("second done")}},
		},
		started: make(chan struct{}, 1),
		block:   make(chan struct{}),
	}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "async-busy"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.SubmitAsync(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "first", time.Now())); err != nil {
		t.Fatalf("submit first async turn: %v", err)
	}
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first async turn to start")
	}

	injected, err := service.SubmitAsync(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "second", time.Now()))
	if err != nil {
		t.Fatalf("inject second async turn: %v", err)
	}
	if injected.Status != "injected" || injected.TurnID == "" {
		t.Fatalf("expected injected result, got %+v", injected)
	}

	close(caller.block)
	_ = waitForBackendSnapshot(t, service, opened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})
}

func TestSubmitAsyncInjectsFollowUpForRunningSession(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		responses: []protocol.Response{
			{Content: []protocol.Block{protocol.TextBlock("first done")}},
			{Content: []protocol.Block{protocol.TextBlock("second done")}},
		},
		started: make(chan struct{}, 2),
		block:   make(chan struct{}),
	}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "async-queue"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.SubmitAsync(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "first", time.Now())); err != nil {
		t.Fatalf("submit first async turn: %v", err)
	}
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first async turn to start")
	}

	injected, err := service.SubmitAsync(
		context.Background(),
		opened.SessionID,
		message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "second", time.Now()),
		SubmitOptions{QueueMode: QueueModeFollowUp},
	)
	if err != nil {
		t.Fatalf("inject second async turn: %v", err)
	}
	if injected.Status != "injected" {
		t.Fatalf("expected injected status, got %+v", injected)
	}
	injectedSnapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot injected: %v", err)
	}
	if len(injectedSnapshot.QueuedTurns) != 0 || len(injectedSnapshot.Turns) == 0 || injectedSnapshot.Turns[len(injectedSnapshot.Turns)-1].InjectionCount != 1 {
		t.Fatalf("expected injected turn metadata, got queued=%+v turns=%+v", injectedSnapshot.QueuedTurns, injectedSnapshot.Turns)
	}

	close(caller.block)
	finished := waitForBackendSnapshot(t, service, opened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running && len(snapshot.Messages) >= 4 && len(snapshot.QueuedTurns) == 0
	})
	if got := protocol.MessageText(finished.Messages[len(finished.Messages)-1]); got != "second done" {
		t.Fatalf("expected injected follow-up to continue current turn, got %q", got)
	}
}

func TestClearCommandResetsPromptStateAndQueuedTurns(t *testing.T) {
	cfg := newTestConfig(t)
	commandService := commands.NewService(cfg)
	commandService.SetClear(func(ctx context.Context, a *agent.Agent, cmd commands.Command) (commands.Result, error) {
		_ = ctx
		_ = cmd
		a.ClearMessages()
		return commands.Result{Name: "clear", Output: "cleared", RefreshSnapshot: true}, nil
	})
	service := NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}},
	}), commandService)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "weixin", Key: "default:user-1", UserID: "user-1"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	session.agent.RestoreStateForSession(opened.SessionID, agent.SessionState{
		Messages:       []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "old question")},
		TranscriptRefs: []string{"transcripts/weixin-session.jsonl"},
		ActiveBundles:  []string{"background", "web"},
	})
	now := time.Now()
	session.seedQueue([]QueuedTurn{{
		ID:        "queued-1",
		Mode:      QueueModeFollowUp,
		Status:    "queued",
		Source:    string(message.SourceWeixin),
		Sender:    "user-1",
		Summary:   "old queued question",
		CreatedAt: now,
		UpdatedAt: now,
		Envelope:  message.NewTextEnvelope(message.SourceWeixin, opened.SessionID, "user-1", "old queued question", now),
	}})

	if _, err := service.ExecuteCommand(context.Background(), opened.SessionID, commands.Command{Name: "clear"}); err != nil {
		t.Fatalf("execute clear: %v", err)
	}

	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Messages) != 0 {
		t.Fatalf("expected messages to be cleared, got %d", len(snapshot.Messages))
	}
	if len(snapshot.QueuedTurns) != 0 {
		t.Fatalf("expected queued turns to be cleared, got %+v", snapshot.QueuedTurns)
	}
	for _, name := range snapshot.ToolCatalog.ActiveBundles {
		if name == "background" || name == "web" {
			t.Fatalf("expected transient active bundles to reset, got %v", snapshot.ToolCatalog.ActiveBundles)
		}
	}
}

func TestForkSessionCopiesTranscriptAndBranchMetadata(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "fork-source"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "hello", time.Now())); err != nil {
		t.Fatalf("submit: %v", err)
	}

	forked, err := service.ForkSession(context.Background(), opened.SessionID, ForkRequest{Title: "experiment"})
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}
	if forked.SessionID == opened.SessionID {
		t.Fatalf("expected distinct fork session id")
	}
	if forked.ParentSessionID != opened.SessionID || forked.BranchTitle != "experiment" {
		t.Fatalf("expected branch metadata, got %+v", forked)
	}
	snapshot, err := service.Snapshot(context.Background(), forked.SessionID)
	if err != nil {
		t.Fatalf("fork snapshot: %v", err)
	}
	if len(snapshot.Messages) != 2 {
		t.Fatalf("expected forked transcript messages, got %+v", snapshot.Messages)
	}
	if got := protocol.MessageText(snapshot.Messages[0]); got != "hello" {
		t.Fatalf("expected forked user message, got %q", got)
	}
	sourceGraph := readTestSessionGraph(t, service, opened.SessionID)
	if _, ok := sourceGraph.Head(sessiongraph.BranchID("branch:" + forked.SessionID)); !ok {
		t.Fatalf("expected source graph to include fork branch for %s, got %+v", forked.SessionID, sourceGraph.Branches)
	}
	forkGraph := readTestSessionGraph(t, service, forked.SessionID)
	if head, ok := forkGraph.Head(sessiongraph.MainBranchID); !ok || head.Head == "" {
		t.Fatalf("expected fork graph main branch head, got %+v ok=%v", head, ok)
	}
}

func TestOpenLegacySessionInitializesSessionGraph(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	locator := SessionLocator{Channel: "web", Key: "legacy-graph"}
	sessionID := stableSessionID(normalizeLocator(service.withDefaultLocatorMetadata(locator)))
	dir := filepath.Join(cfg.SessionsDir, sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	stateData := mustJSON(t, agent.SessionState{Messages: []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "legacy")}})
	manifest := SessionManifest{
		SessionID:      sessionID,
		Locator:        normalizeLocator(service.withDefaultLocatorMetadata(locator)),
		StateDigest:    stateDigest(stateData),
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), mustJSON(t, manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFileName), stateData, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	opened, err := service.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("open legacy session: %v", err)
	}
	graph := readTestSessionGraph(t, service, opened.SessionID)
	if head, ok := graph.Head(sessiongraph.MainBranchID); !ok || head.BranchID != sessiongraph.MainBranchID {
		t.Fatalf("expected initialized main branch, got %+v ok=%v", head, ok)
	}
}

func TestOpenSessionIgnoresMalformedGraphMetadata(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	locator := SessionLocator{Channel: "web", Key: "bad-graph"}
	normalized := normalizeLocator(service.withDefaultLocatorMetadata(locator))
	sessionID := stableSessionID(normalized)
	dir := filepath.Join(cfg.SessionsDir, sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	stateData := mustJSON(t, agent.SessionState{Messages: []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "legacy")}})
	manifest := SessionManifest{
		SessionID:      sessionID,
		Locator:        normalized,
		StateDigest:    stateDigest(stateData),
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), mustJSON(t, manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFileName), stateData, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionGraphFileName), []byte("{bad json"), 0644); err != nil {
		t.Fatalf("write bad graph: %v", err)
	}
	if _, err := service.OpenSession(context.Background(), locator); err != nil {
		t.Fatalf("open session with malformed graph metadata: %v", err)
	}
}

func TestPersistSessionAdvancesSessionGraphCheckpoint(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "graph-checkpoint"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	session.agent.AddEnvelope(message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "hello graph", time.Now()))
	if err := service.persistSession(session, time.Now()); err != nil {
		t.Fatalf("persist session: %v", err)
	}
	graph := readTestSessionGraph(t, service, opened.SessionID)
	head, ok := graph.Head(sessiongraph.MainBranchID)
	if !ok || head.Head == "" {
		t.Fatalf("expected main branch head after persist, got %+v ok=%v", head, ok)
	}
	node := graph.NodeSet[head.Head]
	if node.Checkpoint == nil || strings.TrimSpace(node.Checkpoint.CheckpointID) == "" {
		t.Fatalf("expected checkpoint node metadata, got %+v", node)
	}
}

func TestSQLiteSessionBackendRestoresSessionAfterRestart(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Storage.SessionBackend = "sqlite"
	cfg.Storage.SQLitePath = filepath.Join(cfg.StateDir, "session-store.sqlite")
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "sqlite-restore"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	session.agent.AddEnvelope(message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "persist through sqlite", time.Now()))
	if err := service.persistSession(session, time.Now()); err != nil {
		t.Fatalf("persist session: %v", err)
	}
	session.events.Emit(events.Event{
		SessionID: opened.SessionID,
		Type:      events.EventSnapshotReady,
		Timestamp: time.Now(),
		Payload:   events.SnapshotPayload{Running: false},
	})
	if err := service.writeSessionTimeline(session); err != nil {
		t.Fatalf("write timeline: %v", err)
	}
	now := time.Now()
	session.recordTurnStarted("turn-after-sqlite-row", message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "turn after sqlite row", now), len(session.agent.GetMessages()), now)
	if err := service.writeSessionTurns(session); err != nil {
		t.Fatalf("write turns: %v", err)
	}
	if diag := service.SessionStoreDiagnostics(context.Background()); !diag.Healthy || diag.Backend != "sqlite" || diag.SQLitePath == "" {
		t.Fatalf("expected healthy sqlite diagnostics, got %+v", diag)
	}

	restored := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("restored")}}}})
	data, ok, err := restored.store.Load(context.Background(), opened.SessionID)
	if err != nil || !ok {
		t.Fatalf("load sqlite session store ok=%v err=%v", ok, err)
	}
	if !strings.Contains(string(data.Turns), "turn-after-sqlite-row") {
		t.Fatalf("expected sqlite store to include incremental turn update, got %s", data.Turns)
	}
	if len(data.Timeline) == 0 || len(data.Graph) == 0 {
		t.Fatalf("expected sqlite store to include timeline and graph, timeline=%q graph=%q", data.Timeline, data.Graph)
	}
	if err := os.RemoveAll(filepath.Join(cfg.SessionsDir, opened.SessionID)); err != nil {
		t.Fatalf("remove json session dir: %v", err)
	}
	reopened, err := restored.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "sqlite-restore"})
	if err != nil {
		t.Fatalf("reopen sqlite session: %v", err)
	}
	snapshot, err := restored.Snapshot(context.Background(), reopened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Messages) != 1 || protocol.MessageText(snapshot.Messages[0]) != "persist through sqlite" {
		t.Fatalf("expected sqlite-restored message, got %+v", snapshot.Messages)
	}
	if timeline := restored.readSessionTimeline(reopened.SessionID); len(timeline) == 0 {
		t.Fatalf("expected sqlite-restored timeline")
	}
	graph := readTestSessionGraph(t, restored, reopened.SessionID)
	if head, ok := graph.Head(sessiongraph.MainBranchID); !ok || head.Head == "" {
		t.Fatalf("expected sqlite-restored graph head, got %+v ok=%v", head, ok)
	}
}

func TestSQLiteSessionBackendInitFailureDoesNotReportJSON(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Storage.SessionBackend = "sqlite"
	blocker := filepath.Join(t.TempDir(), "not-dir")
	if err := os.WriteFile(blocker, []byte("file"), 0644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	cfg.Storage.SQLitePath = filepath.Join(blocker, "session-store.sqlite")
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	diag := service.SessionStoreDiagnostics(context.Background())
	if diag.Healthy || diag.Backend != "sqlite" || !strings.Contains(diag.Error, "not a directory") {
		t.Fatalf("expected sqlite init failure diagnostics, got %+v", diag)
	}
}

func TestSQLiteImportedSessionListsAndDeletes(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Storage.SessionBackend = "sqlite"
	cfg.Storage.SQLitePath = filepath.Join(cfg.StateDir, "session-store.sqlite")
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	source := sessionstore.NewJSONStore(t.TempDir())
	sessionID := "imported-sqlite"
	stateData := mustJSON(t, agent.SessionState{Messages: []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "imported")}})
	manifestData := mustJSON(t, SessionManifest{
		SessionID:      sessionID,
		Locator:        SessionLocator{Channel: "web", Key: "imported"},
		StateDigest:    stateDigest(stateData),
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	})
	if err := source.Save(context.Background(), sessionstore.SessionData{
		SessionID: sessionID,
		Manifest:  manifestData,
		State:     stateData,
	}); err != nil {
		t.Fatalf("save source session: %v", err)
	}
	if err := service.ImportSessionFromStore(context.Background(), sessionID, source); err != nil {
		t.Fatalf("import session: %v", err)
	}
	listed, err := service.ListSessions(context.Background(), SessionListFilter{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	found := false
	for _, item := range listed {
		if item.SessionID == sessionID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected imported sqlite session in list, got %+v", listed)
	}
	if err := service.DeleteSession(context.Background(), sessionID); err != nil {
		t.Fatalf("delete imported session: %v", err)
	}
	if _, ok, err := service.store.Load(context.Background(), sessionID); err != nil || ok {
		t.Fatalf("expected imported sqlite row deleted, ok=%v err=%v", ok, err)
	}
}

func TestSetSessionModelProfilePersistsOverride(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.DefaultProfileID = "default"
	cfg.ModelProfiles = map[string]config.ModelProfileConfig{
		"default": {
			ID:                "default",
			Name:              "Default",
			Provider:          config.ProviderAnthropicCompatible,
			Model:             "claude-default",
			BaseURL:           "http://127.0.0.1",
			MaxTokens:         1024,
			TimeoutSeconds:    60,
			SupportsStreaming: true,
		},
		"openai": {
			ID:                "openai",
			Name:              "OpenAI Compatible",
			Provider:          config.ProviderOpenAICompatible,
			Model:             "gpt-test",
			BaseURL:           "http://127.0.0.1",
			MaxTokens:         2048,
			TimeoutSeconds:    30,
			SupportsStreaming: true,
		},
	}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "model-profile"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	view, err := service.SetSessionModelProfile(context.Background(), opened.SessionID, "openai")
	if err != nil {
		t.Fatalf("set model profile: %v", err)
	}
	if view.SessionProfileID != "openai" {
		t.Fatalf("expected openai session profile, got %+v", view)
	}
	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.ModelProfileID != "openai" {
		t.Fatalf("expected snapshot model profile, got %+v", snapshot)
	}

	restored := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})
	reopened, err := restored.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "model-profile"})
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	if reopened.ModelProfileID != "openai" {
		t.Fatalf("expected persisted model profile, got %+v", reopened)
	}
}

func TestSetSessionModelProfilePersistsReasoningEffortOverride(t *testing.T) {
	var requestBody []byte
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected LLM path: %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read LLM request body: %v", err)
		}
		requestBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer llmServer.Close()

	cfg := newTestConfig(t)
	cfg.DefaultProfileID = "default"
	cfg.ModelProfiles = map[string]config.ModelProfileConfig{
		"default": {
			ID:                "default",
			Name:              "Default",
			Provider:          config.ProviderOpenAICompatible,
			Model:             "gpt-default",
			BaseURL:           llmServer.URL,
			MaxTokens:         1024,
			TimeoutSeconds:    60,
			SupportsStreaming: true,
		},
		"openai": {
			ID:                "openai",
			Name:              "OpenAI Compatible",
			Provider:          config.ProviderOpenAICompatible,
			Model:             "gpt-test",
			BaseURL:           llmServer.URL,
			MaxTokens:         2048,
			TimeoutSeconds:    30,
			SupportsStreaming: true,
			ReasoningEffort:   "low",
		},
	}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "reasoning-profile"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	view, err := service.SetSessionModelProfileWithReasoning(context.Background(), opened.SessionID, "openai", "high")
	if err != nil {
		t.Fatalf("set model profile with reasoning: %v", err)
	}
	if view.SessionProfileID != "openai" || view.ReasoningEffort != "high" {
		t.Fatalf("expected openai/high session model view, got %+v", view)
	}
	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.ModelProfileID != "openai" || snapshot.ReasoningEffort != "high" {
		t.Fatalf("expected snapshot model profile and reasoning, got %+v", snapshot)
	}

	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	restored := newTestService(cfg, caller)
	reopened, err := restored.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "reasoning-profile"})
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	if reopened.ModelProfileID != "openai" || reopened.ReasoningEffort != "high" {
		t.Fatalf("expected persisted model profile and reasoning, got %+v", reopened)
	}
	if _, err := restored.Submit(context.Background(), reopened.SessionID, message.NewTextEnvelope(message.SourceWeb, reopened.SessionID, cfg.LeadName, "hello", time.Now())); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !strings.Contains(string(requestBody), `"model":"gpt-test"`) {
		t.Fatalf("expected reopened session to use selected model, got %s", string(requestBody))
	}
}

func TestCancelTurnStopsActiveAsyncTurn(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}},
		started:   make(chan struct{}, 1),
		block:     make(chan struct{}),
	}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "cancel"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	result, err := service.SubmitAsync(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "stop me", time.Now()))
	if err != nil {
		t.Fatalf("submit async: %v", err)
	}
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}

	cancelResult, err := service.CancelTurn(context.Background(), opened.SessionID, result.TurnID)
	if err != nil {
		t.Fatalf("cancel turn: %v", err)
	}
	if cancelResult.TurnID != result.TurnID || cancelResult.Status != "canceling" {
		t.Fatalf("unexpected cancel result: %+v", cancelResult)
	}
	if got := turnRecordStatus(readPersistedTurns(t, service, opened.SessionID), result.TurnID); got != "canceling" && got != "canceled" {
		t.Fatalf("expected persisted canceling or canceled status, got %q", got)
	}

	snapshot := waitForBackendSnapshot(t, service, opened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("expected active turn to be cleared, got %q", snapshot.ActiveTurnID)
	}
	if len(snapshot.Messages) != 1 || protocol.MessageText(snapshot.Messages[0]) != "stop me" {
		t.Fatalf("expected only accepted user message after cancellation, got %+v", snapshot.Messages)
	}
	if got := turnRecordStatus(snapshot.Turns, result.TurnID); got != "canceled" {
		t.Fatalf("expected canceled turn record, got %q in %+v", got, snapshot.Turns)
	}
	foundCanceled := false
	for _, item := range snapshot.Timeline {
		if item.Type != events.EventTurnCompleted {
			continue
		}
		payload, ok := item.Payload.(events.TurnPayload)
		if ok && payload.Status == "canceled" {
			foundCanceled = true
		}
	}
	if !foundCanceled {
		t.Fatalf("expected canceled turn in timeline, got %+v", snapshot.Timeline)
	}
}

func TestRetryTurnAsyncReplaysLatestCanceledTurn(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("retried")}}},
		started:   make(chan struct{}, 2),
		block:     make(chan struct{}),
	}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "retry"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	result, err := service.SubmitAsync(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "try again", time.Now()))
	if err != nil {
		t.Fatalf("submit async: %v", err)
	}
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}
	if _, err := service.CancelTurn(context.Background(), opened.SessionID, result.TurnID); err != nil {
		t.Fatalf("cancel turn: %v", err)
	}
	_ = waitForBackendSnapshot(t, service, opened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running && turnRecordStatus(snapshot.Turns, result.TurnID) == "canceled"
	})

	retryCaller := &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("retried")}}},
	}
	restored := newTestService(cfg, retryCaller)
	reopened, err := restored.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "retry"})
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	retry, err := restored.RetryTurnAsync(context.Background(), reopened.SessionID, result.TurnID)
	if err != nil {
		t.Fatalf("retry turn: %v", err)
	}
	if retry.Status != "running" || retry.TurnID == "" || retry.TurnID == result.TurnID || retry.RetryOf != result.TurnID {
		t.Fatalf("unexpected retry result: %+v", retry)
	}

	finished := waitForBackendSnapshot(t, restored, reopened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running && turnRecordStatus(snapshot.Turns, retry.TurnID) == "completed"
	})
	if len(finished.Messages) != 2 {
		t.Fatalf("expected retried transcript to replace canceled attempt, got %+v", finished.Messages)
	}
	if got := protocol.MessageText(finished.Messages[0]); got != "try again" {
		t.Fatalf("expected replayed user message, got %q", got)
	}
	if got := protocol.MessageText(finished.Messages[1]); got != "retried" {
		t.Fatalf("expected retried assistant message, got %q", got)
	}
	if got := turnRecordStatus(finished.Turns, result.TurnID); got != "canceled" {
		t.Fatalf("expected original turn to remain canceled, got %q in %+v", got, finished.Turns)
	}
	if retryRecord := turnRecordByID(finished.Turns, retry.TurnID); retryRecord.RetryOf != result.TurnID {
		t.Fatalf("expected retry_of to point at original turn, got %+v", retryRecord)
	}
	if turnRecordByID(finished.Turns, retry.TurnID).Envelope != nil {
		t.Fatalf("snapshot should not expose replay envelope: %+v", finished.Turns)
	}
}

func TestSessionSubagentViewsReviewAndMerge(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.ToolUseBlock("tool-write", "write_file", map[string]interface{}{
				"path":    "notes/result.txt",
				"content": "from service subagent\n",
			}),
		}},
		{Content: []protocol.Block{protocol.TextBlock("subagent handoff")}},
	}}
	service := newTestService(cfg, caller)
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "subagent-views"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	ctx := agent.WithSubagentEvents(context.Background(), opened.SessionID, "turn-parent", session.events)
	job, err := session.agent.StartDurableSubagentWithContext(ctx, "write a note", "general-purpose", []string{"notes"})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	waitForBackendSubagentStatus(t, service, opened.SessionID, job.IDString(), "completed")

	items, err := service.ListSubagents(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("list subagents: %v", err)
	}
	if len(items) != 1 || items[0].JobID != job.IDString() || items[0].ParentTurnID != "turn-parent" {
		t.Fatalf("unexpected subagent list: %+v", items)
	}
	if items[0].LastToolName != "write_file" || len(items[0].Progress) == 0 {
		t.Fatalf("expected tool/progress metadata, got %+v", items[0])
	}

	got, err := service.GetSubagent(context.Background(), opened.SessionID, job.IDString())
	if err != nil {
		t.Fatalf("get subagent: %v", err)
	}
	if got.Status != "completed" || got.WorktreeDir == "" {
		t.Fatalf("unexpected subagent view: %+v", got)
	}

	review, err := service.ReviewSubagent(context.Background(), opened.SessionID, job.IDString())
	if err != nil {
		t.Fatalf("review subagent: %v", err)
	}
	if len(review.Changes) != 1 || review.Changes[0].Path != "notes/result.txt" || !strings.Contains(review.Diff, "from service subagent") {
		t.Fatalf("unexpected review: %+v", review)
	}

	merged, err := service.MergeSubagent(context.Background(), opened.SessionID, job.IDString())
	if err != nil {
		t.Fatalf("merge subagent: %v", err)
	}
	if merged.Status != "merged" || len(merged.Applied) != 1 {
		t.Fatalf("unexpected merge: %+v", merged)
	}
	if data, err := os.ReadFile(filepath.Join(cfg.WorkspaceDir, "notes", "result.txt")); err != nil || string(data) != "from service subagent\n" {
		t.Fatalf("expected merged file, data=%q err=%v", string(data), err)
	}
}

func TestApprovePermissionResumesDurableSubagent(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.ToolUseBlock("tool-shell", "bash", map[string]interface{}{"command": "command -v sh"}),
		}},
		{Content: []protocol.Block{protocol.TextBlock("subagent resumed")}},
	}}
	service := newTestService(cfg, caller)
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "subagent-approval"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	ctx := agent.WithSubagentEvents(context.Background(), opened.SessionID, "turn-parent", session.events)
	ctx = tools.WithSessionContext(ctx, automation.SessionContext{
		SessionID: opened.SessionID,
		Source:    string(message.SourceWeb),
		Sender:    "user",
	})
	job, err := session.agent.StartDurableSubagentWithContext(ctx, "run a protected shell command", "general-purpose", []string{"notes"})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	pending := waitForBackendSubagentStatus(t, service, opened.SessionID, job.IDString(), "pending_approval")
	if !strings.Contains(pending.Error, "requires approval") {
		t.Fatalf("expected pending approval subagent, got %+v", pending)
	}
	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.PendingPermissions) != 1 || snapshot.PendingPermissions[0].Request.Sender != "subagent:"+job.IDString() {
		t.Fatalf("expected subagent pending permission, got %+v", snapshot.PendingPermissions)
	}
	resolution, err := service.ApprovePermission(context.Background(), opened.SessionID, snapshot.PendingPermissions[0].ID, tools.PermissionGrantOnce)
	if err != nil {
		t.Fatalf("approve subagent permission: %v", err)
	}
	if !resolution.Resumed || !strings.HasPrefix(resolution.ResumeStatus, "subagent_") {
		t.Fatalf("expected subagent resume resolution, got %+v", resolution)
	}
	completed := waitForBackendSubagentStatus(t, service, opened.SessionID, job.IDString(), "completed")
	if completed.Result != "subagent resumed" {
		t.Fatalf("expected resumed subagent result, got %+v", completed)
	}
}

func TestPackageCommandCompletedEventIncludesDispatchSummary(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("review done")}}}})
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "commands"), 0755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	manifest := `name: agent-kit
version: 0.1.0
resources:
  commands:
    - commands/review.yaml
`
	if err := os.WriteFile(filepath.Join(source, pkgregistry.ManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "commands", "review.yaml"), []byte("name: review\nmode: agent_turn\nprompt: Review {{args}}\n"), 0644); err != nil {
		t.Fatalf("write command: %v", err)
	}
	if _, err := service.InstallPackage(context.Background(), source); err != nil {
		t.Fatalf("install package: %v", err)
	}
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "command-dispatch"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	result, err := service.ExecuteCommand(context.Background(), opened.SessionID, commands.Command{Name: "agent-kit", Args: []string{"review", "src"}})
	if err != nil {
		t.Fatalf("execute package command: %v", err)
	}
	if result.DispatchedTurnID == "" {
		t.Fatalf("expected dispatched turn id, got %+v", result)
	}
	_ = waitForBackendSnapshot(t, service, opened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running && turnRecordStatus(snapshot.Turns, result.DispatchedTurnID) == "completed"
	})
	timeline, err := service.Timeline(context.Background(), opened.SessionID, 20)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	var payload events.CommandPayload
	for _, event := range timeline {
		if event.Type != events.EventCommandCompleted {
			continue
		}
		if typed, ok := event.Payload.(events.CommandPayload); ok {
			payload = typed
			break
		}
		data, _ := json.Marshal(event.Payload)
		_ = json.Unmarshal(data, &payload)
		if payload.Name != "" {
			break
		}
	}
	if payload.DispatchMode != "agent_turn" || payload.DispatchedTurnID != result.DispatchedTurnID || payload.DispatchInvocation != "/agent-kit review" {
		t.Fatalf("expected dispatch summary in command event, got %+v result=%+v", payload, result)
	}
}

func TestAsyncTurnPersistsTimelineWhileRunning(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}},
		started:   make(chan struct{}, 1),
		block:     make(chan struct{}),
	}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "timeline-running"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	result, err := service.SubmitAsync(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "persist me", time.Now()))
	if err != nil {
		t.Fatalf("submit async: %v", err)
	}
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}

	timeline := readPersistedTimeline(t, service, opened.SessionID)
	found := false
	for _, item := range timeline {
		if item.TurnID == result.TurnID && item.Type == events.EventUserMessageAccepted {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected persisted user_message_accepted for running turn, got %+v", timeline)
	}
	journal := readPersistedEventJournal(t, service, opened.SessionID)
	found = false
	for _, item := range journal {
		if item.TurnID == result.TurnID && item.Type == events.EventUserMessageAccepted {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected journaled user_message_accepted for running turn, got %+v", journal)
	}

	close(caller.block)
	_ = waitForBackendSnapshot(t, service, opened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})
}

func TestOpenSessionReplaysTimelineFromEventJournal(t *testing.T) {
	cfg := newTestConfig(t)
	locator := SessionLocator{Channel: "web", Key: "journal-replay"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})

	opened, err := service.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	turnID := "turn-journal-replay"
	now := time.Now().Add(-time.Minute)
	envelope := message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "replay journal after restart", now)
	session.agent.AddEnvelope(envelope)
	if err := service.persistSession(session, now); err != nil {
		t.Fatalf("persist fixture: %v", err)
	}
	session.events.Emit(events.Event{
		SessionID: opened.SessionID,
		TurnID:    turnID,
		Type:      events.EventUserMessageAccepted,
		Timestamp: now,
		Payload: events.MessagePayload{
			Source: string(message.SourceWeb),
			Sender: cfg.LeadName,
			Text:   envelope.BodyText(),
		},
	})
	if err := os.Remove(filepath.Join(service.sessionDir(opened.SessionID), timelineFileName)); err != nil {
		t.Fatalf("remove timeline cache: %v", err)
	}

	restored := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})
	reopened, err := restored.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	snapshot, err := restored.Snapshot(context.Background(), reopened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	foundAccepted := false
	foundInterrupted := false
	for _, item := range snapshot.Timeline {
		if item.TurnID != turnID {
			continue
		}
		if item.Type == events.EventUserMessageAccepted {
			foundAccepted = true
		}
		if item.Type == events.EventTurnCompleted && turnStatus(item.Payload) == "interrupted" {
			foundInterrupted = true
		}
	}
	if !foundAccepted || !foundInterrupted {
		t.Fatalf("expected journal replay and recovery marker, got %+v", snapshot.Timeline)
	}
}

func TestTimelinePageReadsFullJournalAndFilters(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "timeline-page"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 220; i++ {
		eventType := events.EventRunnerPhaseChanged
		if i%10 == 0 {
			eventType = events.EventSubagentJobUpdated
		}
		session.events.Emit(events.Event{
			SessionID: opened.SessionID,
			TurnID:    fmt.Sprintf("turn-%03d", i%7),
			Type:      eventType,
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Payload: map[string]any{
				"message": fmt.Sprintf("timeline page event %03d", i),
				"job_id":  fmt.Sprintf("job-%02d", i%5),
				"role_id": "worker",
			},
		})
	}

	recent, err := service.Timeline(context.Background(), opened.SessionID, 500)
	if err != nil {
		t.Fatalf("recent timeline: %v", err)
	}
	if len(recent) >= 220 {
		t.Fatalf("expected legacy timeline to stay recorder-capped, got %d", len(recent))
	}

	page, err := service.TimelinePage(context.Background(), opened.SessionID, TimelinePageRequest{Limit: 5})
	if err != nil {
		t.Fatalf("timeline page: %v", err)
	}
	if page.Total != 220 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("expected full paged journal total and cursor, got %+v", page)
	}
	if len(page.Items) != 5 {
		t.Fatalf("expected five page items, got %d", len(page.Items))
	}
	if got := page.Items[0].Payload.(map[string]any)["message"]; got != "timeline page event 219" {
		t.Fatalf("expected newest event first, got %v", got)
	}

	next, err := service.TimelinePage(context.Background(), opened.SessionID, TimelinePageRequest{Limit: 5, Cursor: page.NextCursor})
	if err != nil {
		t.Fatalf("next timeline page: %v", err)
	}
	if got := next.Items[0].Payload.(map[string]any)["message"]; got != "timeline page event 214" {
		t.Fatalf("expected cursor to continue newest-first page, got %v", got)
	}

	filtered, err := service.TimelinePage(context.Background(), opened.SessionID, TimelinePageRequest{
		Limit:  20,
		Types:  []string{string(events.EventSubagentJobUpdated)},
		Query:  "event 210",
		JobID:  "job-00",
		TurnID: "turn-000",
	})
	if err != nil {
		t.Fatalf("filtered timeline page: %v", err)
	}
	if filtered.Total != 1 || len(filtered.Items) != 1 {
		t.Fatalf("expected one filtered item, got %+v", filtered)
	}
	if got := filtered.Items[0].Payload.(map[string]any)["message"]; got != "timeline page event 210" {
		t.Fatalf("expected filtered event 210, got %v", got)
	}
}

func TestSubscribeReplayReplaysActiveTurnEvents(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}},
		started:   make(chan struct{}, 1),
		block:     make(chan struct{}),
	}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "replay-active"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	result, err := service.SubmitAsync(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "replay me", time.Now()))
	if err != nil {
		t.Fatalf("submit async: %v", err)
	}
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan events.Event, 4)
	done := make(chan error, 1)
	go func() {
		done <- service.SubscribeReplay(ctx, opened.SessionID, events.SinkFunc(func(event events.Event) {
			if event.TurnID == result.TurnID && event.Type == events.EventUserMessageAccepted {
				got <- event
				cancel()
			}
		}), EventReplayOptions{ActiveOnly: true})
	}()

	select {
	case event := <-got:
		if event.Type != events.EventUserMessageAccepted {
			t.Fatalf("expected replayed user_message_accepted, got %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replayed active turn event")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled subscription, got %v", err)
	}

	close(caller.block)
	_ = waitForBackendSnapshot(t, service, opened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})
}

func TestAttachSinkFansOutLiveEventsToIndependentConsumers(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("fanout ok")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "fanout-live"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	var mu sync.Mutex
	counts := map[string]int{"sse": 0, "channel": 0}
	attach := func(name string) {
		unsubscribe, err := service.AttachSink(opened.SessionID, events.SinkFunc(func(event events.Event) {
			if event.Type != events.EventAssistantTextDelta {
				return
			}
			payload, ok := event.Payload.(events.TextPayload)
			if !ok || payload.Text != "fanout ok" {
				return
			}
			mu.Lock()
			counts[name]++
			mu.Unlock()
		}))
		if err != nil {
			t.Fatalf("attach %s sink: %v", name, err)
		}
		t.Cleanup(unsubscribe)
	}
	attach("sse")
	attach("channel")

	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "fan out this turn", time.Now())); err != nil {
		t.Fatalf("submit: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if counts["sse"] != 1 || counts["channel"] != 1 {
		t.Fatalf("expected both consumers to receive assistant delta, got %+v", counts)
	}
}

func TestSubscribeReplayAndLiveSinkIndependentlyObserveActiveTurn(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("replay fanout")}}},
		started:   make(chan struct{}, 1),
		block:     make(chan struct{}),
	}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "fanout-replay"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	liveAccepted := make(chan struct{}, 1)
	liveDelta := make(chan struct{}, 1)
	unsubscribe, err := service.AttachSink(opened.SessionID, events.SinkFunc(func(event events.Event) {
		switch event.Type {
		case events.EventUserMessageAccepted:
			liveAccepted <- struct{}{}
		case events.EventAssistantTextDelta:
			if payload, ok := event.Payload.(events.TextPayload); ok && payload.Text == "replay fanout" {
				liveDelta <- struct{}{}
			}
		}
	}))
	if err != nil {
		t.Fatalf("attach live sink: %v", err)
	}
	defer unsubscribe()

	result, err := service.SubmitAsync(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "start active turn", time.Now()))
	if err != nil {
		t.Fatalf("submit async: %v", err)
	}
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}
	select {
	case <-liveAccepted:
	case <-time.After(time.Second):
		t.Fatal("live sink did not receive accepted event")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replayAccepted := make(chan struct{}, 1)
	replayDelta := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- service.SubscribeReplay(ctx, opened.SessionID, events.SinkFunc(func(event events.Event) {
			if event.TurnID != result.TurnID {
				return
			}
			switch event.Type {
			case events.EventUserMessageAccepted:
				replayAccepted <- struct{}{}
			case events.EventAssistantTextDelta:
				if payload, ok := event.Payload.(events.TextPayload); ok && payload.Text == "replay fanout" {
					replayDelta <- struct{}{}
				}
			}
		}), EventReplayOptions{ActiveOnly: true})
	}()

	select {
	case <-replayAccepted:
	case <-time.After(time.Second):
		t.Fatal("subscribe replay did not replay accepted event")
	}
	close(caller.block)
	select {
	case <-replayDelta:
	case <-time.After(time.Second):
		t.Fatal("subscribe replay did not receive live assistant delta")
	}
	select {
	case <-liveDelta:
	case <-time.After(time.Second):
		t.Fatal("live sink did not receive assistant delta")
	}
	_ = waitForBackendSnapshot(t, service, opened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled subscription, got %v", err)
	}
}

func TestOpenSessionMarksInterruptedTurnAfterRestart(t *testing.T) {
	cfg := newTestConfig(t)
	locator := SessionLocator{Channel: "web", Key: "interrupted-restart"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})

	opened, err := service.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}

	turnID := "turn-interrupted"
	now := time.Now().Add(-time.Minute)
	envelope := message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "continue this after restart", now)
	session.agent.AddEnvelope(envelope)
	session.setTitleIfEmpty(sessionTitleFromEnvelope(envelope))
	if err := service.persistSession(session, now); err != nil {
		t.Fatalf("persist interrupted fixture: %v", err)
	}
	session.events.Emit(events.Event{
		SessionID: opened.SessionID,
		TurnID:    turnID,
		Type:      events.EventUserMessageAccepted,
		Timestamp: now,
		Payload: events.MessagePayload{
			Source: string(message.SourceWeb),
			Sender: cfg.LeadName,
			Text:   envelope.BodyText(),
		},
	})
	session.events.Emit(events.Event{
		SessionID: opened.SessionID,
		TurnID:    turnID,
		Type:      events.EventSnapshotReady,
		Timestamp: now,
		Payload:   events.SnapshotPayload{UpdatedAt: now, Running: true},
	})

	restored := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})
	reopened, err := restored.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	if reopened.SessionID != opened.SessionID {
		t.Fatalf("expected stable session id, got %s want %s", reopened.SessionID, opened.SessionID)
	}
	snapshot, err := restored.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Running || snapshot.ActiveTurnID != "" {
		t.Fatalf("expected interrupted session to reopen idle, got running=%v active=%q", snapshot.Running, snapshot.ActiveTurnID)
	}
	if got := turnRecordStatus(snapshot.Turns, turnID); got != "interrupted" {
		t.Fatalf("expected interrupted turn record, got %q in %+v", got, snapshot.Turns)
	}

	foundWarning := false
	foundInterrupted := false
	for _, item := range snapshot.Timeline {
		if item.TurnID != turnID {
			continue
		}
		if item.Type == events.EventWarningRaised {
			foundWarning = true
		}
		if item.Type == events.EventTurnCompleted && turnStatus(item.Payload) == "interrupted" {
			foundInterrupted = true
		}
	}
	if !foundWarning || !foundInterrupted {
		t.Fatalf("expected interrupted warning and turn completion, got %+v", snapshot.Timeline)
	}

	restoredAgain := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})
	if _, err := restoredAgain.OpenSession(context.Background(), locator); err != nil {
		t.Fatalf("reopen recovered session: %v", err)
	}
	recoveredSnapshot, err := restoredAgain.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("recovered snapshot: %v", err)
	}
	interruptedCount := 0
	for _, item := range recoveredSnapshot.Timeline {
		if item.TurnID == turnID && item.Type == events.EventTurnCompleted && turnStatus(item.Payload) == "interrupted" {
			interruptedCount++
		}
	}
	if interruptedCount != 1 {
		t.Fatalf("expected interrupted marker to be written once, got %d events: %+v", interruptedCount, recoveredSnapshot.Timeline)
	}
}

func TestResumeTurnAsyncContinuesInterruptedCheckpoint(t *testing.T) {
	cfg := newTestConfig(t)
	locator := SessionLocator{Channel: "web", Key: "resume-interrupted"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})

	opened, err := service.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}

	turnID := "turn-resume"
	now := time.Now().Add(-time.Minute)
	envelope := message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "continue from checkpoint", now)
	session.agent.AddEnvelope(envelope)
	session.agent.AppendAssistantText("partial checkpoint", "")
	session.recordTurnStarted(turnID, envelope, 0, now)
	session.updateTurnStatus(turnID, "interrupted", "", "Previous process stopped before this turn completed.", now.Add(time.Second))
	if err := service.persistSession(session, now.Add(time.Second)); err != nil {
		t.Fatalf("persist interrupted checkpoint: %v", err)
	}

	restored := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("continued")}}}})
	reopened, err := restored.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	before, err := restored.Snapshot(context.Background(), reopened.SessionID)
	if err != nil {
		t.Fatalf("snapshot before resume: %v", err)
	}
	resumeRecord := turnRecordByID(before.Turns, turnID)
	if !resumeRecord.CanResume || !resumeRecord.CanRetry {
		t.Fatalf("expected interrupted turn to expose resume and retry affordances, got %+v", resumeRecord)
	}

	resumed, err := restored.ResumeTurnAsync(context.Background(), reopened.SessionID, turnID)
	if err != nil {
		t.Fatalf("resume turn: %v", err)
	}
	if resumed.TurnID != turnID || resumed.Status != "running" || resumed.Completed {
		t.Fatalf("unexpected resume result: %+v", resumed)
	}

	finished := waitForBackendSnapshot(t, restored, reopened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running && turnRecordStatus(snapshot.Turns, turnID) == "completed"
	})
	if len(finished.Messages) != 3 {
		t.Fatalf("expected checkpointed transcript plus resumed answer, got %+v", finished.Messages)
	}
	if got := protocol.MessageText(finished.Messages[0]); got != "continue from checkpoint" {
		t.Fatalf("expected original user message to remain first, got %q", got)
	}
	if got := protocol.MessageText(finished.Messages[1]); got != "partial checkpoint" {
		t.Fatalf("expected checkpointed assistant message to remain second, got %q", got)
	}
	if got := protocol.MessageText(finished.Messages[2]); got != "continued" {
		t.Fatalf("expected resumed assistant answer, got %q", got)
	}
}

func TestListSessionsFiltersByChannelAndSortsByUpdatedAt(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	writeManifest := func(sessionID, channel, key, title string, updated time.Time) {
		t.Helper()
		dir := filepath.Join(cfg.SessionsDir, sessionID)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir session dir: %v", err)
		}
		data, err := json.Marshal(SessionManifest{
			SessionID:      sessionID,
			Locator:        SessionLocator{Channel: channel, Key: key},
			Title:          title,
			CreatedAt:      updated.Add(-time.Hour),
			UpdatedAt:      updated,
			LastActivityAt: updated,
		})
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, manifestFileName), data, 0644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}

	now := time.Now()
	writeManifest("older-web", "web", "b", "Older chat", now.Add(-time.Minute))
	writeManifest("newer-web", "web", "a", "Newest chat", now)
	writeManifest("local-default", "local", "default", "Local session", now.Add(-2*time.Minute))

	sessions, err := service.ListSessions(context.Background(), SessionListFilter{Channel: "web"})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 web sessions, got %d", len(sessions))
	}
	if sessions[0].SessionID != "newer-web" || sessions[1].SessionID != "older-web" {
		t.Fatalf("unexpected session order: %#v", sessions)
	}
	if sessions[0].Title != "Newest chat" || sessions[1].Title != "Older chat" {
		t.Fatalf("expected titles from manifests, got %#v", sessions)
	}
}

func TestSubmitPersistsDerivedSessionTitle(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "title-test"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewCLIEnvelope(opened.SessionID, cfg.LeadName, "今天深圳天气怎么样，需要穿什么", time.Now())); err != nil {
		t.Fatalf("submit message: %v", err)
	}

	sessions, err := service.ListSessions(context.Background(), SessionListFilter{Channel: "web"})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected one session, got %d", len(sessions))
	}
	if sessions[0].Title != "今天深圳天气怎么样，需要穿什么" {
		t.Fatalf("expected derived title, got %#v", sessions[0].Title)
	}
}

func TestListSessionsBackfillsMissingManifestTitle(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	sessionID := "web-backfill"
	dir := filepath.Join(cfg.SessionsDir, sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	manifestData, err := json.Marshal(SessionManifest{
		SessionID:      sessionID,
		Locator:        SessionLocator{Channel: "web", Key: "backfill"},
		CreatedAt:      time.Now().Add(-time.Hour),
		UpdatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), manifestData, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	stateData, err := json.Marshal(agent.SessionState{
		Messages: []protocol.Message{
			message.NewCLIEnvelope(sessionID, cfg.LeadName, "这是一个很长的旧会话标题示例", time.Now()).ToProtocolMessage(protocol.RoleUser, "", false),
		},
	})
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFileName), stateData, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	sessions, err := service.ListSessions(context.Background(), SessionListFilter{Channel: "web"})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected one session, got %d", len(sessions))
	}
	if sessions[0].Title != "这是一个很长的旧会话标题示例" {
		t.Fatalf("expected backfilled title, got %#v", sessions[0].Title)
	}

	data, err := os.ReadFile(filepath.Join(dir, manifestFileName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var persisted SessionManifest
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if persisted.Title != "这是一个很长的旧会话标题示例" {
		t.Fatalf("expected persisted backfill title, got %#v", persisted.Title)
	}
	if persisted.StateDigest == "" {
		t.Fatalf("expected persisted state digest after backfill, got %#v", persisted)
	}
}

func TestDeleteSessionRemovesSessionDirAndUniqueTranscripts(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	writeSession := func(sessionID, key string, refs []string) {
		t.Helper()
		dir := filepath.Join(cfg.SessionsDir, sessionID)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir session dir: %v", err)
		}
		state := agent.SessionState{TranscriptRefs: refs}
		stateData, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal state: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, stateFileName), stateData, 0644); err != nil {
			t.Fatalf("write state: %v", err)
		}
		manifestData, err := json.Marshal(SessionManifest{
			SessionID:      sessionID,
			Locator:        SessionLocator{Channel: "web", Key: key},
			Title:          key,
			StateDigest:    stateDigest(stateData),
			CreatedAt:      time.Now().Add(-time.Hour),
			UpdatedAt:      time.Now(),
			LastActivityAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, manifestFileName), manifestData, 0644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}

	writeSession("target-web", "target", []string{"transcript_unique.json", "transcript_shared.json"})
	writeSession("other-web", "other", []string{"transcript_shared.json"})
	if err := os.WriteFile(filepath.Join(cfg.TranscriptsDir, "transcript_unique.json"), []byte("unique"), 0644); err != nil {
		t.Fatalf("write unique transcript: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.TranscriptsDir, "transcript_shared.json"), []byte("shared"), 0644); err != nil {
		t.Fatalf("write shared transcript: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.SessionsDir, "target-web", attachmentsDir), 0755); err != nil {
		t.Fatalf("mkdir attachments dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.SessionsDir, "target-web", attachmentsDir, "note.txt"), []byte("attachment"), 0644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	targetToolResult := filepath.Join(cfg.StateDir, ".tool-results", "target-web", "tool.json")
	sharedToolResult := filepath.Join(cfg.StateDir, ".tool-results", "other-web", "tool.json")
	if err := os.MkdirAll(filepath.Dir(targetToolResult), 0755); err != nil {
		t.Fatalf("mkdir target tool result: %v", err)
	}
	if err := os.WriteFile(targetToolResult, []byte("target artifact"), 0644); err != nil {
		t.Fatalf("write target tool result: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sharedToolResult), 0755); err != nil {
		t.Fatalf("mkdir other tool result: %v", err)
	}
	if err := os.WriteFile(sharedToolResult, []byte("other artifact"), 0644); err != nil {
		t.Fatalf("write other tool result: %v", err)
	}

	if err := service.DeleteSession(context.Background(), "target-web"); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionsDir, "target-web")); !os.IsNotExist(err) {
		t.Fatalf("expected target session dir removed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.TranscriptsDir, "transcript_unique.json")); !os.IsNotExist(err) {
		t.Fatalf("expected unique transcript removed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.TranscriptsDir, "transcript_shared.json")); err != nil {
		t.Fatalf("expected shared transcript to remain, got %v", err)
	}
	if _, err := os.Stat(targetToolResult); !os.IsNotExist(err) {
		t.Fatalf("expected target tool-result artifacts removed, got %v", err)
	}
	if _, err := os.Stat(sharedToolResult); err != nil {
		t.Fatalf("expected other session tool-result artifact to remain, got %v", err)
	}
}

func TestDeleteSessionRejectsRunningSession(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}, started: make(chan struct{}, 1), block: make(chan struct{})}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "busy"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := service.Submit(context.Background(), opened.SessionID, message.NewCLIEnvelope(opened.SessionID, cfg.LeadName, "hello", time.Now()))
		done <- runErr
	}()
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for running session")
	}
	err = service.DeleteSession(context.Background(), opened.SessionID)
	if !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("expected session busy error, got %v", err)
	}
	close(caller.block)
	if err := <-done; err != nil {
		t.Fatalf("submit should finish successfully, got %v", err)
	}
}

func TestOpenSessionRejectsPartialSessionFiles(t *testing.T) {
	tests := []struct {
		name  string
		files map[string][]byte
	}{
		{
			name: "manifest only",
			files: map[string][]byte{
				manifestFileName: mustJSON(t, SessionManifest{
					SessionID:      stableSessionID(SessionLocator{Channel: "web", Key: "partial"}),
					Locator:        SessionLocator{Channel: "web", Key: "partial"},
					StateDigest:    "missing",
					CreatedAt:      time.Now(),
					UpdatedAt:      time.Now(),
					LastActivityAt: time.Now(),
				}),
			},
		},
		{
			name: "state only",
			files: map[string][]byte{
				stateFileName: mustJSON(t, agent.SessionState{}),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newTestConfig(t)
			service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
			locator := SessionLocator{Channel: "web", Key: "partial"}
			sessionID := stableSessionID(locator)
			dir := filepath.Join(cfg.SessionsDir, sessionID)
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatalf("mkdir session dir: %v", err)
			}
			for name, data := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}

			_, err := service.OpenSession(context.Background(), locator)
			if !errors.Is(err, ErrSessionCorrupt) {
				t.Fatalf("expected corrupt session error, got %v", err)
			}
		})
	}
}

func TestOpenSessionRejectsCorruptSessionFiles(t *testing.T) {
	tests := []struct {
		name         string
		manifestData []byte
		stateData    []byte
	}{
		{
			name:         "invalid manifest json",
			manifestData: []byte("{"),
			stateData:    mustJSON(t, agent.SessionState{}),
		},
		{
			name: "invalid state json",
			manifestData: mustJSON(t, SessionManifest{
				SessionID:      stableSessionID(SessionLocator{Channel: "web", Key: "broken"}),
				Locator:        SessionLocator{Channel: "web", Key: "broken"},
				StateDigest:    "placeholder",
				CreatedAt:      time.Now(),
				UpdatedAt:      time.Now(),
				LastActivityAt: time.Now(),
			}),
			stateData: []byte("{"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newTestConfig(t)
			service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
			locator := SessionLocator{Channel: "web", Key: "broken"}
			sessionID := stableSessionID(locator)
			dir := filepath.Join(cfg.SessionsDir, sessionID)
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatalf("mkdir session dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, manifestFileName), tc.manifestData, 0644); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, stateFileName), tc.stateData, 0644); err != nil {
				t.Fatalf("write state: %v", err)
			}

			_, err := service.OpenSession(context.Background(), locator)
			if !errors.Is(err, ErrSessionCorrupt) {
				t.Fatalf("expected corrupt session error, got %v", err)
			}
		})
	}
}

func TestOpenSessionUsesCheckpointWhenRootDigestMismatch(t *testing.T) {
	cfg := newTestConfig(t)
	locator := SessionLocator{Channel: "web", Key: "digest-mismatch"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	opened, err := service.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewCLIEnvelope(opened.SessionID, cfg.LeadName, "persist me", time.Now())); err != nil {
		t.Fatalf("submit session: %v", err)
	}

	if err := os.WriteFile(filepath.Join(cfg.SessionsDir, opened.SessionID, stateFileName), []byte(`{"messages":[]}`), 0644); err != nil {
		t.Fatalf("overwrite state: %v", err)
	}

	restored := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	reopened, err := restored.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("expected checkpoint restore despite root digest mismatch, got %v", err)
	}
	snapshot, err := restored.Snapshot(context.Background(), reopened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Messages) != 2 || protocol.MessageText(snapshot.Messages[0]) != "persist me" {
		t.Fatalf("expected checkpointed transcript to survive, got %+v", snapshot.Messages)
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionsDir, opened.SessionID, checkpointPointerName)); err != nil {
		t.Fatalf("expected checkpoint pointer: %v", err)
	}
}

func TestWriteSessionCheckpointPrunesAndOmitsTimeline(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Storage = config.StorageConfig{
		SessionCheckpointKeepLatest: 2,
		SessionCheckpointTTLHours:   1,
		SessionCheckpointAutoPrune:  true,
	}
	service := newTestService(cfg, &stubCaller{})
	sessionID := "web-checkpoint-prune"
	state := agent.SessionState{Messages: []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "hello")}}
	stateData, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	manifest := SessionManifest{SessionID: sessionID, StateDigest: stateDigest(stateData)}
	for i := 0; i < 4; i++ {
		at := time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC)
		if _, err := service.writeSessionCheckpoint(sessionID, manifest, stateData, []events.Event{{SessionID: sessionID, Type: events.EventUserMessageAccepted}}, nil, nil, at); err != nil {
			t.Fatalf("write checkpoint %d: %v", i, err)
		}
	}
	checkpointsDir := filepath.Join(cfg.SessionsDir, sessionID, checkpointsDirName)
	entries, err := os.ReadDir(checkpointsDir)
	if err != nil {
		t.Fatalf("read checkpoints: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 checkpoints after prune, got %d", len(entries))
	}
	for _, entry := range entries {
		if _, err := os.Stat(filepath.Join(checkpointsDir, entry.Name(), timelineFileName)); !os.IsNotExist(err) {
			t.Fatalf("expected checkpoint timeline omitted for %s, got %v", entry.Name(), err)
		}
	}
	if _, ok, err := service.readSessionCheckpoint(sessionID); !ok || err != nil {
		t.Fatalf("expected checkpoint readable without timeline, ok=%v err=%v", ok, err)
	}
}

func TestOpenSessionRejectsLegacyStateDigestMismatch(t *testing.T) {
	cfg := newTestConfig(t)
	locator := SessionLocator{Channel: "web", Key: "legacy-digest-mismatch"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	sessionID := stableSessionID(locator)
	dir := filepath.Join(cfg.SessionsDir, sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	stateData := mustJSON(t, agent.SessionState{Messages: []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "persist me")}})
	manifest := SessionManifest{
		SessionID:      sessionID,
		Locator:        locator,
		StateDigest:    "wrong",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), mustJSON(t, manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFileName), stateData, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	_, err := service.OpenSession(context.Background(), locator)
	if !errors.Is(err, ErrSessionCorrupt) {
		t.Fatalf("expected legacy digest mismatch to surface as corrupt session, got %v", err)
	}
}

// TestOpenSessionReusesExistingLocalDefaultDirectoryOnDisk reproduces the
// regression where a TUI/REPL user opens a `local` session and persists it
// on disk as a directory named after a `local-<hash>` session id, but a
// later web request for `Channel=local Key=default` computes a different
// hash and ends up creating an empty parallel session. The contract is:
// when the computed id is missing on disk but another `local-*` directory
// already exists with a manifest that declares the same Channel+Key, the
// service must reuse that directory's id and surface its existing state.
func TestOpenSessionReusesExistingLocalDefaultDirectoryOnDisk(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	// Pre-seed disk with the "TUI/REPL" local default session: a directory
	// whose name is some other local-<hash> (not the one the service will
	// recompute from the web request) and whose manifest declares the same
	// (Channel, Key) pair the web URL uses.
	existingID := "local-2e89dede86fda9f7"
	dir := filepath.Join(cfg.SessionsDir, existingID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir existing session: %v", err)
	}
	stateData := mustJSON(t, agent.SessionState{
		Messages: []protocol.Message{
			protocol.NewTextMessage(protocol.RoleUser, "hello from REPL"),
		},
	})
	manifest := SessionManifest{
		SessionID:      existingID,
		Locator:        SessionLocator{Channel: "local", Key: "default"},
		Title:          "REPL hello",
		StateDigest:    stateDigest(stateData),
		CreatedAt:      time.Now().Add(-time.Hour),
		UpdatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), mustJSON(t, manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFileName), stateData, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	// Simulate the web request: a `local` channel with the canonical `default`
	// key. The computed stable id is unlikely to match the on-disk directory
	// because TUI/REPL inject project_dir metadata that web does not.
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "local", Key: "default"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if opened.SessionID != existingID {
		t.Fatalf("expected web OpenSession to reuse existing local directory %q, got %q", existingID, opened.SessionID)
	}

	// And the snapshot pulled from the reused id must reflect the on-disk
	// state (i.e. the REPL message) rather than an empty fresh session.
	snap, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap.Messages) != 1 || protocol.MessageText(snap.Messages[0]) != "hello from REPL" {
		t.Fatalf("expected reused session to surface the REPL state, got %+v", snap.Messages)
	}
}

// TestListSessionsIncludesLocalDefaultReusedByWeb guarantees the web UI's
// session list can see the `local` session that was originally created by
// TUI/REPL and then reused via OpenSession. Today the web list is filtered
// by channel and the local directory is invisible to it.
func TestListSessionsIncludesLocalDefaultReusedByWeb(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	existingID := "local-2e89dede86fda9f7"
	dir := filepath.Join(cfg.SessionsDir, existingID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stateData := mustJSON(t, agent.SessionState{
		Messages: []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "hi")},
	})
	manifest := SessionManifest{
		SessionID:      existingID,
		Locator:        SessionLocator{Channel: "local", Key: "default"},
		Title:          "REPL local",
		StateDigest:    stateDigest(stateData),
		CreatedAt:      time.Now().Add(-time.Hour),
		UpdatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), mustJSON(t, manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFileName), stateData, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	listed, err := service.ListSessions(context.Background(), SessionListFilter{Channel: "local"})
	if err != nil {
		t.Fatalf("list local sessions: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected the REPL local session to be visible in the local list, got %d entries", len(listed))
	}
	if listed[0].SessionID != existingID {
		t.Fatalf("expected listed session id %q, got %q", existingID, listed[0].SessionID)
	}
	if listed[0].Locator.Key != "default" {
		t.Fatalf("expected listed locator key=default, got %q", listed[0].Locator.Key)
	}
}

// TestHTTPOpenSessionReusesLocalDefaultDirectory exercises the /sessions
// HTTP endpoint the web UI calls, asserting that a POST {channel:local,
// key:default} body reuses the existing on-disk local directory. We use the
// public OpenSession service method here; the httpapi layer is a thin
// decoder on top of it and is covered by its own tests.
func TestHTTPOpenSessionReusesLocalDefaultDirectory(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	existingID := "local-2e89dede86fda9f7"
	dir := filepath.Join(cfg.SessionsDir, existingID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stateData := mustJSON(t, agent.SessionState{
		Messages: []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "hello")},
	})
	manifest := SessionManifest{
		SessionID:      existingID,
		Locator:        SessionLocator{Channel: "local", Key: "default"},
		Title:          "REPL local",
		StateDigest:    stateDigest(stateData),
		CreatedAt:      time.Now().Add(-time.Hour),
		UpdatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), mustJSON(t, manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFileName), stateData, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	// Simulate the HTTP request body the web client sends and decode the
	// payload using the same JSON shape the httpapi handler decodes.
	type openRequest struct {
		Locator SessionLocator `json:"locator"`
	}
	var decoded openRequest
	if err := json.Unmarshal([]byte(`{"locator":{"channel":"local","key":"default"}}`), &decoded); err != nil {
		t.Fatalf("decode simulated request body: %v", err)
	}
	opened, err := service.OpenSession(context.Background(), decoded.Locator)
	if err != nil {
		t.Fatalf("open via simulated http body: %v", err)
	}
	if opened.SessionID != existingID {
		t.Fatalf("expected HTTP OpenSession to reuse %q, got %q", existingID, opened.SessionID)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return data
}

func readTestSessionGraph(t *testing.T, service *Service, sessionID string) *sessiongraph.SessionGraph {
	t.Helper()
	graph, err := sessiongraph.NewStore(service.sessionGraphPath(sessionID)).Load()
	if err != nil {
		t.Fatalf("read session graph: %v", err)
	}
	return graph
}

func apiMessagesText(messages []protocol.APIMessage) string {
	var builder strings.Builder
	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.Text != "" {
				if builder.Len() > 0 {
					builder.WriteString("\n")
				}
				builder.WriteString(block.Text)
			}
		}
	}
	return builder.String()
}

func newTestService(cfg *config.Config, caller *stubCaller) *Service {
	shared := agent.NewSharedDependenciesWithCaller(cfg, caller)
	return NewService(cfg, shared, commands.NewService(cfg))
}

func containsBackgroundText(messages []protocol.Message, needle string) bool {
	for _, msg := range messages {
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindBackground && strings.Contains(protocol.MessageText(msg), needle) {
			return true
		}
	}
	return false
}

func waitForBackendSnapshot(t *testing.T, service *Service, sessionID string, ready func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := service.Snapshot(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if ready(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for snapshot condition, last snapshot: %+v", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForBackendSubagentStatus(t *testing.T, service *Service, sessionID, jobID, status string) agent.DurableSubagentJobView {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last agent.DurableSubagentJobView
	for {
		got, err := service.GetSubagent(context.Background(), sessionID, jobID)
		if err != nil {
			t.Fatalf("get subagent: %v", err)
		}
		last = got
		if got.Status == status {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for subagent %s status %s, got %+v", jobID, status, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readPersistedTimeline(t *testing.T, service *Service, sessionID string) []events.Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(service.sessionDir(sessionID), timelineFileName))
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	var decoded []events.Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	return decoded
}

func TestPackageQualityIncludesPackageAndToolHealth(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "skills", "qa"), 0755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	manifest := `name: qa-kit
version: 0.1.0
resources:
  skills:
    - skills/qa/SKILL.md
permissions:
  - shell
recommended_bundles:
  - core_code
`
	if err := os.WriteFile(filepath.Join(source, pkgregistry.ManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "skills", "qa", "SKILL.md"), []byte("# QA\n"), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if _, err := service.InstallPackage(context.Background(), source); err != nil {
		t.Fatalf("install package: %v", err)
	}
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "quality"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	if err := service.persistSession(session, time.Now()); err != nil {
		t.Fatalf("persist session: %v", err)
	}
	session.timeline.Emit(events.Event{
		SessionID: opened.SessionID,
		Type:      events.EventToolCallFinished,
		Timestamp: time.Now(),
		Payload:   events.ToolCallPayload{Name: "bash", Error: "command not allowed"},
	})
	if err := service.writeSessionTimeline(session); err != nil {
		t.Fatalf("write timeline: %v", err)
	}
	report, err := service.PackageQuality(context.Background())
	if err != nil {
		t.Fatalf("package quality: %v", err)
	}
	if report.PackageCount != 1 || report.ToolHealth.TotalRuns != 1 || report.ToolHealth.FailureRuns != 1 {
		t.Fatalf("unexpected quality report: %+v", report)
	}
	if len(report.Packages) != 1 || report.Packages[0].RiskLevel != "high" {
		t.Fatalf("expected high risk package due shell permission, got %+v", report.Packages)
	}
}

func TestRunPackageSmokeRecordsResultAndAudit(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	source := t.TempDir()
	manifest := `name: smoke-kit
version: 0.1.0
smoke_tests:
  - name: quick
    command: printf ok
    timeout_seconds: 5
`
	if err := os.WriteFile(filepath.Join(source, pkgregistry.ManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if _, err := service.InstallPackage(context.Background(), source); err != nil {
		t.Fatalf("install package: %v", err)
	}
	run, err := service.RunPackageSmoke(context.Background(), "smoke-kit", "quick", "")
	if err != nil {
		t.Fatalf("run smoke: %v", err)
	}
	if run.Status != "passed" || run.SessionID == "" || !strings.Contains(run.Output, "ok") {
		t.Fatalf("unexpected smoke run: %+v", run)
	}
	report, err := service.PackageQuality(context.Background())
	if err != nil {
		t.Fatalf("package quality: %v", err)
	}
	if len(report.Packages) != 1 || len(report.Packages[0].SmokeChecks) != 1 || report.Packages[0].SmokeChecks[0].Status != "passed" {
		t.Fatalf("expected passed smoke check in quality report, got %+v", report.Packages)
	}
	audit, err := service.SecurityAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("security audit: %v", err)
	}
	found := false
	for _, event := range audit {
		if event.Action == "run_package_smoke" && event.Metadata["package"] == "smoke-kit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected smoke audit event, got %+v", audit)
	}
}

func readPersistedEventJournal(t *testing.T, service *Service, sessionID string) []events.Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(service.sessionDir(sessionID), eventJournalFileName))
	if err != nil {
		t.Fatalf("read event journal: %v", err)
	}
	var decoded []events.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event events.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event journal line: %v", err)
		}
		decoded = append(decoded, event)
	}
	return decoded
}

func readPersistedTurns(t *testing.T, service *Service, sessionID string) []TurnRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(service.sessionDir(sessionID), turnsFileName))
	if err != nil {
		t.Fatalf("read turns: %v", err)
	}
	var decoded []TurnRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode turns: %v", err)
	}
	return decoded
}

func turnRecordStatus(records []TurnRecord, turnID string) string {
	for _, record := range records {
		if record.ID == turnID {
			return record.Status
		}
	}
	return ""
}

func turnRecordByID(records []TurnRecord, turnID string) TurnRecord {
	for _, record := range records {
		if record.ID == turnID {
			return record
		}
	}
	return TurnRecord{}
}

func turnStatus(payload any) string {
	switch value := payload.(type) {
	case events.TurnPayload:
		return value.Status
	case map[string]any:
		status, _ := value["status"].(string)
		return status
	default:
		return ""
	}
}

func newTestConfig(t *testing.T) *config.Config {
	t.Helper()
	workspace := t.TempDir()
	cfg := &config.Config{
		Model:             "test-model",
		BaseURL:           "http://127.0.0.1",
		MaxTokens:         1024,
		WorkspaceDir:      workspace,
		StateDir:          filepath.Join(workspace, ".godex"),
		TeamDir:           filepath.Join(workspace, ".godex", ".team"),
		TasksDir:          filepath.Join(workspace, ".godex", ".tasks"),
		TodosDir:          filepath.Join(workspace, ".godex", ".todos"),
		MemoryDir:         filepath.Join(workspace, ".godex", "memory"),
		RulesDir:          filepath.Join(workspace, ".godex", "rules"),
		SkillsDir:         filepath.Join(workspace, ".godex", "skills"),
		MCPConfigPath:     filepath.Join(workspace, ".godex", "mcp.json"),
		TempDir:           filepath.Join(workspace, ".godex", ".tmp"),
		TranscriptsDir:    filepath.Join(workspace, ".godex", ".transcripts"),
		SessionsDir:       filepath.Join(workspace, ".godex", ".sessions"),
		CompressThreshold: 100000,
		LeadName:          "lead",
		TeamName:          "default",
	}
	for _, dir := range []string{
		filepath.Join(cfg.TeamDir, "inbox"),
		cfg.TasksDir,
		cfg.TodosDir,
		cfg.MemoryDir,
		cfg.RulesDir,
		cfg.SkillsDir,
		cfg.TempDir,
		cfg.TranscriptsDir,
		cfg.SessionsDir,
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return cfg
}

// TestOpenSessionScopesTodosPerSession asserts that the
// per-session Agent created by loadSession has its own
// todo manager rooted under
// <sessionsDir>/<sessionID>/todos.json, so a web session
// opened in the same workspace as a local session never
// sees the local session's todos.
//
// Regression guard for the cross-session pollution bug
// where the legacy code created one process-wide todo
// manager at <TodosDir>/todos.json.
func TestOpenSessionScopesTodosPerSession(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	local, err := service.OpenSession(context.Background(), SessionLocator{Channel: "local", Key: "alpha"})
	if err != nil {
		t.Fatalf("open local session: %v", err)
	}
	web, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "alpha"})
	if err != nil {
		t.Fatalf("open web session: %v", err)
	}
	if local.SessionID == web.SessionID {
		t.Fatalf("local and web locators must hash to different session ids, got %q", local.SessionID)
	}

	// Drop a synthetic todo file into the local session's
	// per-session todo directory.  Without our fix the
	// global todo manager would have read it for the web
	// session too.
	localTodoPath := filepath.Join(cfg.SessionsDir, local.SessionID, "todos.json")
	if err := os.MkdirAll(filepath.Dir(localTodoPath), 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(localTodoPath, []byte(`[{"id":1,"content":"from local","status":"pending","active_form":""}]`), 0o644); err != nil {
		t.Fatalf("write local todos.json: %v", err)
	}

	// Drop a synthetic todo into the web session's dir too
	// so we can verify each session reads its OWN file.
	webTodoPath := filepath.Join(cfg.SessionsDir, web.SessionID, "todos.json")
	if err := os.MkdirAll(filepath.Dir(webTodoPath), 0o755); err != nil {
		t.Fatalf("mkdir web session dir: %v", err)
	}
	if err := os.WriteFile(webTodoPath, []byte(`[{"id":1,"content":"from web","status":"pending","active_form":""}]`), 0o644); err != nil {
		t.Fatalf("write web todos.json: %v", err)
	}

	// Evict both sessions from the in-memory cache so the
	// next Snapshot reloads their agents and TodoMgrs from
	// disk.  Without this the freshly opened sessions would
	// still report empty todo lists (the file was written
	// AFTER OpenSession ran).
	reloadSessionForTest(service, local.SessionID)
	reloadSessionForTest(service, web.SessionID)
	if _, err := service.OpenSession(context.Background(), SessionLocator{Channel: "local", Key: "alpha"}); err != nil {
		t.Fatalf("reopen local: %v", err)
	}
	if _, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "alpha"}); err != nil {
		t.Fatalf("reopen web: %v", err)
	}

	if err := assertSessionTodoCount(t, service, local.SessionID, 1, "local after seed"); err != nil {
		t.Fatal(err)
	}
	if err := assertSessionTodoCount(t, service, web.SessionID, 1, "web after seed"); err != nil {
		t.Fatal(err)
	}

	// /todos clear on the local session must leave ONLY the
	// local session empty; the web session must still see
	// its own todo (no cross-session file deletion).
	if _, err := service.ExecuteCommand(context.Background(), local.SessionID, commands.Command{Name: "todos", Args: []string{"clear"}}); err != nil {
		t.Fatalf("/todos clear: %v", err)
	}
	if err := assertSessionTodoCount(t, service, local.SessionID, 0, "local after /todos clear"); err != nil {
		t.Fatal(err)
	}
	if err := assertSessionTodoCount(t, service, web.SessionID, 1, "web after local /todos clear (must remain untouched)"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(localTodoPath)
	if err != nil {
		t.Fatalf("read todos.json after clear: %v", err)
	}
	if strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("expected /todos clear to write an empty array, got %q", string(data))
	}

	// Opening yet another channel after local has been
	// cleared must start with zero todos — this is the
	// regression scenario from the bug report.
	weixin, err := service.OpenSession(context.Background(), SessionLocator{Channel: "weixin", Key: "alpha"})
	if err != nil {
		t.Fatalf("open weixin session: %v", err)
	}
	if err := assertSessionTodoCount(t, service, weixin.SessionID, 0, "fresh weixin must not inherit any session's todos"); err != nil {
		t.Fatal(err)
	}
}

// assertSessionTodoCount checks the public Snapshot's Todos
// field.  We use the public API rather than poking at the
// private session map so the test stays robust to internal
// refactors.
func assertSessionTodoCount(t *testing.T, service *Service, sessionID string, want int, label string) error {
	t.Helper()
	snap, err := service.Snapshot(context.Background(), sessionID)
	if err != nil {
		return fmt.Errorf("%s: snapshot: %w", label, err)
	}
	if len(snap.Todos) != want {
		return fmt.Errorf("%s: want %d todos, got %d (items=%+v)", label, want, len(snap.Todos), snap.Todos)
	}
	return nil
}

// reloadSessionForTest evicts a session from the service's
// in-memory cache so the next OpenSession call reloads its
// todo state from disk.  Used by the cross-session isolation
// test to prove that what is persisted in one session's
// todos.json does not leak into a sibling session.
func reloadSessionForTest(service *Service, sessionID string) error {
	service.mu.Lock()
	delete(service.sessions, sessionID)
	service.mu.Unlock()
	return nil
}

// setTitleIfEmpty must allow a real title to replace the "New chat" placeholder
// that deriveSessionTitle persists before the first user message generates one.
// Otherwise sessions whose first turn needs a permission (or whose async LLM
// title fails) stay stuck on the placeholder forever.
func TestSetTitleIfEmptyReplacesNewChatPlaceholder(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "placeholder-title"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	service.mu.Lock()
	session := service.sessions[opened.SessionID]
	service.mu.Unlock()
	if session == nil {
		t.Fatalf("session not loaded")
	}

	// Simulate the placeholder persisted before the first message's title.
	session.setTitleIfEmpty("New chat")
	// Now the real title from the first user message must replace it.
	session.setTitleIfEmpty("如图，图1是godex的 web ui")
	if got := session.title; got != "如图，图1是godex的 web ui" {
		t.Fatalf("expected placeholder replaced by real title, got %#v", got)
	}

	// A subsequent placeholder attempt must not clobber a real title.
	session.setTitleIfEmpty("New chat")
	if got := session.title; got != "如图，图1是godex的 web ui" {
		t.Fatalf("expected real title preserved over later placeholder, got %#v", got)
	}
}

// TestValidateSessionProjectDir covers the API-boundary validation for
// caller-supplied per-session working directories: empty means "no
// override", valid directories resolve to absolute cleaned paths, and
// missing / non-directory paths are rejected with ErrInvalidWorkspaceDir.
func TestValidateSessionProjectDir(t *testing.T) {
	dir := t.TempDir()

	if got, err := validateSessionProjectDir(""); err != nil || got != "" {
		t.Fatalf("empty input: got %q, %v", got, err)
	}
	if got, err := validateSessionProjectDir(dir + string(filepath.Separator) + "." + string(filepath.Separator)); err != nil || got != filepath.Clean(dir) {
		t.Fatalf("dot variant: got %q, %v; want %q", got, err, filepath.Clean(dir))
	}
	if _, err := validateSessionProjectDir(filepath.Join(dir, "missing")); !errors.Is(err, ErrInvalidWorkspaceDir) {
		t.Fatalf("missing dir should fail with ErrInvalidWorkspaceDir, got %v", err)
	}
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := validateSessionProjectDir(file); !errors.Is(err, ErrInvalidWorkspaceDir) {
		t.Fatalf("regular file should fail with ErrInvalidWorkspaceDir, got %v", err)
	}
}

// TestOpenSessionWithWorkspaceDirPinsAgentWorkspace asserts that a session
// opened with an explicit project_dir gets an agent whose sandbox is
// rooted at that directory, while a default session keeps the service
// workspace.  It also guards the identity rule: same key + different
// directory = different session.
func TestOpenSessionWithWorkspaceDirPinsAgentWorkspace(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	otherDir := t.TempDir()

	opened, err := service.OpenSession(context.Background(), SessionLocator{
		Channel: "web",
		Key:     "scoped",
		Metadata: map[string]string{
			sessionProjectDirMetadataKey: otherDir,
		},
	})
	if err != nil {
		t.Fatalf("open scoped session: %v", err)
	}
	service.mu.Lock()
	session := service.sessions[opened.SessionID]
	service.mu.Unlock()
	if session == nil {
		t.Fatal("scoped session not loaded")
	}
	if got := session.agent.SandboxBinding().WorkspaceDir; got != filepath.Clean(otherDir) {
		t.Fatalf("scoped session workspace = %q, want %q", got, otherDir)
	}

	// Same key, different directory → different session id.
	def, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "scoped"})
	if err != nil {
		t.Fatalf("open default session: %v", err)
	}
	if def.SessionID == opened.SessionID {
		t.Fatal("expected distinct session ids for different workspaces")
	}
	service.mu.Lock()
	defSession := service.sessions[def.SessionID]
	service.mu.Unlock()
	if got := defSession.agent.SandboxBinding().WorkspaceDir; got != filepath.Clean(cfg.WorkspaceDir) {
		t.Fatalf("default session workspace = %q, want %q", got, cfg.WorkspaceDir)
	}

	// Reopening with the same directory resumes the same session.
	again, err := service.OpenSession(context.Background(), SessionLocator{
		Channel: "web",
		Key:     "scoped",
		Metadata: map[string]string{
			sessionProjectDirMetadataKey: otherDir + string(filepath.Separator),
		},
	})
	if err != nil {
		t.Fatalf("reopen scoped session: %v", err)
	}
	if again.SessionID != opened.SessionID {
		t.Fatalf("reopen with same dir hashed to %q, want %q", again.SessionID, opened.SessionID)
	}
}

// TestOpenSessionRejectsInvalidWorkspaceDir ensures a bad project_dir is
// rejected at the boundary instead of silently falling back to the
// service workspace.
func TestOpenSessionRejectsInvalidWorkspaceDir(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	_, err := service.OpenSession(context.Background(), SessionLocator{
		Channel: "web",
		Key:     "bad-dir",
		Metadata: map[string]string{
			sessionProjectDirMetadataKey: filepath.Join(cfg.WorkspaceDir, "does-not-exist"),
		},
	})
	if !errors.Is(err, ErrInvalidWorkspaceDir) {
		t.Fatalf("expected ErrInvalidWorkspaceDir, got %v", err)
	}
}

// TestApplyConfigKeepsSessionWorkspaceOverride asserts that a global
// config reload does not move a workspace-scoped session's tools back
// to the service directory.
func TestApplyConfigKeepsSessionWorkspaceOverride(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	otherDir := t.TempDir()

	opened, err := service.OpenSession(context.Background(), SessionLocator{
		Channel: "web",
		Key:     "scoped-reload",
		Metadata: map[string]string{
			sessionProjectDirMetadataKey: otherDir,
		},
	})
	if err != nil {
		t.Fatalf("open scoped session: %v", err)
	}

	if err := service.ApplyConfig(cfg); err != nil {
		t.Fatalf("apply config: %v", err)
	}
	service.mu.Lock()
	session := service.sessions[opened.SessionID]
	service.mu.Unlock()
	if got := session.agent.SandboxBinding().WorkspaceDir; got != filepath.Clean(otherDir) {
		t.Fatalf("workspace after ApplyConfig = %q, want %q", got, otherDir)
	}
}

func TestSteeringMessageInjectedIntoRunningTurn(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		responses: []protocol.Response{
			{Content: []protocol.Block{protocol.TextBlock("first response")}},
			{Content: []protocol.Block{protocol.TextBlock("steered response")}},
		},
		started: make(chan struct{}, 2),
		block:   make(chan struct{}),
	}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "steer"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	sessionID := opened.SessionID
	if _, err := service.SubmitAsync(context.Background(), sessionID, message.NewTextEnvelope(message.SourceWeb, sessionID, cfg.LeadName, "do task A", time.Now())); err != nil {
		t.Fatalf("submit async: %v", err)
	}
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}

	// While the first model call is in-flight, submit a steering message.
	steer, err := service.SubmitAsync(context.Background(), sessionID,
		message.NewTextEnvelope(message.SourceWeb, sessionID, cfg.LeadName, "now switch to task B", time.Now()),
		SubmitOptions{QueueMode: QueueModeSteering})
	if err != nil {
		t.Fatalf("submit steering: %v", err)
	}
	if steer.Status != "injected" {
		t.Fatalf("expected steering message to be injected into running turn, got status %q", steer.Status)
	}

	// Let the first response through; the runner should drain the injection
	// and issue a second model request that includes the steering message.
	close(caller.block)
	select {
	case <-caller.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for second model call after injection")
	}

	var secondReq protocol.Request
	func() {
		caller.mu.Lock()
		defer caller.mu.Unlock()
		if len(caller.requests) < 2 {
			return
		}
		secondReq = caller.requests[1]
	}()
	if len(secondReq.Messages) == 0 {
		t.Fatalf("expected second model request, got %d requests", len(caller.requests))
	}
	found := false
	for _, m := range secondReq.Messages {
		if m.Role != protocol.RoleUser {
			continue
		}
		var text string
		for _, b := range m.Content {
			if b.Type == protocol.BlockText {
				text += b.Text + " "
			}
		}
		if strings.Contains(text, "task B") {
			found = true
		}
	}
	if !found {
		t.Fatalf("steering message missing from second model request: %+v", secondReq.Messages)
	}
	// The steering message must be framed as an interruption directive so the
	// model pauses the running task instead of deferring it like a follow-up.
	framed := false
	for _, m := range secondReq.Messages {
		if m.Role != protocol.RoleUser {
			continue
		}
		var text string
		for _, b := range m.Content {
			if b.Type == protocol.BlockText {
				text += b.Text + " "
			}
		}
		if strings.Contains(text, "Steer") && strings.Contains(text, "task B") {
			framed = true
		}
	}
	if !framed {
		var lines []string
		for _, m := range secondReq.Messages {
			var text string
			for _, b := range m.Content {
				if b.Type == protocol.BlockText {
					text += b.Text + " | "
				}
			}
			lines = append(lines, m.Role+": "+text)
		}
		caller.mu.Lock()
		reqCount := len(caller.requests)
		caller.mu.Unlock()
		t.Fatalf("steering message not framed as interruption in model request (calls=%d):\n%s", reqCount, strings.Join(lines, "\n"))
	}

	snapshot := waitForBackendSnapshot(t, service, sessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})
	seenSteer := false
	for _, msg := range snapshot.Messages {
		if strings.Contains(protocol.MessageText(msg), "task B") {
			seenSteer = true
		}
	}
	if !seenSteer {
		t.Fatalf("steering message missing from session messages: %+v", snapshot.Messages)
	}
	// Injecting must emit user_message_accepted so the web UI shows the
	// message immediately instead of leaving a "sending..." placeholder.
	sawAccepted := false
	for _, item := range snapshot.Timeline {
		if item.Type != events.EventUserMessageAccepted {
			continue
		}
		if payload, ok := item.Payload.(events.MessagePayload); ok && strings.Contains(payload.Text, "task B") {
			sawAccepted = true
		}
	}
	if !sawAccepted {
		t.Fatalf("expected user_message_accepted for injected steering message, timeline: %+v", snapshot.Timeline)
	}
}

func TestGetTurnReturnsPersistedTurnStatus(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("get turn ok")}}},
		started:   make(chan struct{}, 1),
		block:     make(chan struct{}),
	}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "get-turn"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	result, err := service.SubmitAsync(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "query turn", time.Now()))
	if err != nil {
		t.Fatalf("submit async: %v", err)
	}
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}

	record, err := service.GetTurn(context.Background(), opened.SessionID, result.TurnID)
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if record == nil || record.ID != result.TurnID {
		t.Fatalf("expected turn record %q, got %+v", result.TurnID, record)
	}
	if record.Status != "running" {
		t.Fatalf("expected running turn status, got %q", record.Status)
	}

	// Unknown turn -> ErrTurnNotFound.
	if _, err := service.GetTurn(context.Background(), opened.SessionID, "turn-does-not-exist"); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("expected ErrTurnNotFound, got %v", err)
	}

	close(caller.block)
	waitForBackendSnapshot(t, service, opened.SessionID, func(snapshot Snapshot) bool { return !snapshot.Running })
}

func TestReplayEventsTurnIDFilter(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "replay-turn-filter"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	// Seed the session timeline directly with events belonging to two turns and
	// one turn-less event.
	now := time.Now()
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	session.timeline.Seed([]events.Event{
		{SessionID: opened.SessionID, TurnID: "turn-a", Type: events.EventUserMessageAccepted, Timestamp: now},
		{SessionID: opened.SessionID, TurnID: "turn-a", Type: events.EventTurnCompleted, Timestamp: now},
		{SessionID: opened.SessionID, TurnID: "turn-b", Type: events.EventUserMessageAccepted, Timestamp: now},
		{SessionID: opened.SessionID, TurnID: "", Type: events.EventSnapshotReady, Timestamp: now},
	})

	onlyA := session.replayEvents(EventReplayOptions{TurnID: "turn-a"})
	if len(onlyA) != 2 {
		t.Fatalf("expected 2 events for turn-a, got %d: %+v", len(onlyA), onlyA)
	}
	for _, event := range onlyA {
		if event.TurnID != "turn-a" {
			t.Fatalf("expected only turn-a events, got %+v", event)
		}
	}

	onlyB := session.replayEvents(EventReplayOptions{TurnID: "turn-b"})
	if len(onlyB) != 1 || onlyB[0].TurnID != "turn-b" {
		t.Fatalf("expected single turn-b event, got %+v", onlyB)
	}

	// TurnID takes precedence over ActiveOnly.
	active := session.replayEvents(EventReplayOptions{TurnID: "turn-a", ActiveOnly: true})
	if len(active) != 2 {
		t.Fatalf("expected TurnID to take precedence over ActiveOnly, got %d", len(active))
	}
}

func TestTurnCompletionRotatesEventJournal(t *testing.T) {
	cfg := newTestConfig(t)
	caller := &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}},
		started:   make(chan struct{}, 1),
		block:     make(chan struct{}),
	}
	service := newTestService(cfg, caller)

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "journal-rotate"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	result, err := service.SubmitAsync(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "rotate me", time.Now()))
	if err != nil {
		t.Fatalf("submit async: %v", err)
	}
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}

	// While the turn is running, the journal must keep the accepted event so a
	// crash mid-turn can reconstruct the timeline.
	journal := readPersistedEventJournal(t, service, opened.SessionID)
	foundRunning := false
	for _, item := range journal {
		if item.TurnID == result.TurnID && item.Type == events.EventUserMessageAccepted {
			foundRunning = true
			break
		}
	}
	if !foundRunning {
		t.Fatalf("expected journal to carry the running turn's events, got %+v", journal)
	}

	// Finish the turn; the terminal persist snapshots the timeline into a
	// checkpoint, so the journal should be rotated (truncated) afterwards.
	close(caller.block)
	waitForBackendSnapshot(t, service, opened.SessionID, func(snapshot Snapshot) bool { return !snapshot.Running })

	after := readPersistedEventJournal(t, service, opened.SessionID)
	if len(after) != 0 {
		t.Fatalf("expected journal rotated after terminal turn, still has %d events: %+v", len(after), after)
	}
}

func TestRotateSessionEventJournalIsBestEffort(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "journal-rotate-sqlite"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	// A journal file that does not exist must not error the rotation.
	if err := service.rotateSessionEventJournal(session); err != nil {
		t.Fatalf("expected best-effort rotate on missing journal, got %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(service.sessionDir(opened.SessionID), eventJournalFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected no journal file after rotate of missing journal, err=%v", err)
	}
}
