package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/notes"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/tools"
)

func TestBuildInsightsInputPreservesConversationShape(t *testing.T) {
	snapshot := insightsSnapshot{
		Messages: []protocol.Message{
			{
				Role: protocol.RoleUser,
				Content: []protocol.Block{
					{Type: protocol.BlockText, Text: "hello "},
					{Type: protocol.BlockToolUse, Name: "bash"},
					{Type: protocol.BlockText, Text: "world"},
				},
			},
			{
				Role: protocol.RoleAssistant,
				Content: []protocol.Block{
					{Type: protocol.BlockToolUse, Name: "read_file"},
				},
			},
		},
		ActiveSkills: []string{"reviewer"},
		ToolCatalog: tools.ToolCatalog{
			ActiveBundles: []string{"core-code"},
		},
		Todos: []todo.Item{
			{Status: todo.StatusInProgress},
			{Status: todo.StatusCompleted},
		},
		Tasks: []*task.FileTask{
			{Status: task.StatusPending},
			{Status: task.StatusCompleted},
		},
	}

	got := buildInsightsInput(snapshot)
	want := insights.Input{
		CurrentMessages: []insights.Message{
			{Text: "hello world", ToolNames: []string{"bash"}},
			{Text: "", ToolNames: []string{"read_file"}},
		},
		ActiveSkills: []string{"reviewer"},
		ToolCatalog: insights.ToolCatalog{
			ActiveBundles: []string{"core-code"},
		},
		Todos: []insights.WorkItem{
			{Status: "in_progress"},
			{Status: "completed"},
		},
		Tasks: []insights.WorkItem{
			{Status: "pending"},
			{Status: "completed"},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected insights input\nwant: %#v\ngot:  %#v", want, got)
	}
}

func TestAvailableMetadataDrivesHelpText(t *testing.T) {
	service := NewService(&config.Config{})
	metadata := AvailableMetadata()
	if len(metadata) == 0 {
		t.Fatal("expected command metadata")
	}
	seen := map[string]CommandMetadata{}
	for _, item := range metadata {
		seen[item.Name] = item
	}
	for _, name := range []string{"help", "model", "approve", "deny", "todos"} {
		item, ok := seen[name]
		if !ok {
			t.Fatalf("missing command metadata for %q", name)
		}
		if strings.TrimSpace(item.Description) == "" {
			t.Fatalf("metadata for %q has empty description", name)
		}
		if !strings.Contains(service.HelpText(), "/"+name) {
			t.Fatalf("HelpText() does not include /%s:\n%s", name, service.HelpText())
		}
	}
	if strings.Count(service.HelpText(), "/model") != 1 {
		t.Fatalf("expected HelpText to render one /model entry, got:\n%s", service.HelpText())
	}
}

func TestExecuteNoteCreatesAndListsMarkdownNotes(t *testing.T) {
	home := t.TempDir()
	service := NewService(&config.Config{HomeDir: home, StateDir: filepath.Join(home, ".state")})
	result, err := service.executeNote(context.Background(), Command{Name: "note", Args: []string{"create", "Architecture", "--", "Review notes"}})
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	if !strings.Contains(result.Output, "Created note architecture") {
		t.Fatalf("unexpected create output: %s", result.Output)
	}
	manager := notes.NewManager(filepath.Join(home, "notes"))
	items, err := manager.List(notes.SearchOptions{Query: "review"})
	if err != nil {
		t.Fatalf("list notes: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Architecture" {
		t.Fatalf("unexpected notes: %+v", items)
	}
	result, err = service.executeNote(context.Background(), Command{Name: "note", Args: []string{"list", "review"}})
	if err != nil {
		t.Fatalf("list notes command: %v", err)
	}
	if !strings.Contains(result.Output, "Architecture") {
		t.Fatalf("unexpected list output: %s", result.Output)
	}
}

func TestExecuteNoteFiltersByTag(t *testing.T) {
	home := t.TempDir()
	service := NewService(&config.Config{HomeDir: home, StateDir: filepath.Join(home, ".state")})
	if _, err := service.executeNote(context.Background(), Command{Name: "note", Args: []string{"create", "Learning", "--tags", "study,review", "--", "Review schedule"}}); err != nil {
		t.Fatalf("create tagged note: %v", err)
	}
	if _, err := service.executeNote(context.Background(), Command{Name: "note", Args: []string{"create", "Deploy", "--tags", "ops", "--", "Deployment notes"}}); err != nil {
		t.Fatalf("create ops note: %v", err)
	}

	result, err := service.executeNote(context.Background(), Command{Name: "note", Args: []string{"list", "--tag", "study"}})
	if err != nil {
		t.Fatalf("list tagged notes: %v", err)
	}
	if !strings.Contains(result.Output, "Learning") || strings.Contains(result.Output, "Deploy") {
		t.Fatalf("unexpected tagged list output: %s", result.Output)
	}

	result, err = service.executeNote(context.Background(), Command{Name: "note", Args: []string{"search", "review", "--tag", "study"}})
	if err != nil {
		t.Fatalf("search tagged notes: %v", err)
	}
	if !strings.Contains(result.Output, "Learning") || strings.Contains(result.Output, "Deploy") {
		t.Fatalf("unexpected tagged search output: %s", result.Output)
	}
}

func TestExecuteNoteAppendAndUpdateUseCurrentNoteContext(t *testing.T) {
	home := t.TempDir()
	service := NewService(&config.Config{HomeDir: home, StateDir: filepath.Join(home, ".state")})
	manager := notes.NewManager(filepath.Join(home, "notes"))
	note, err := manager.Save(notes.SaveInput{
		Title:   "Current note",
		Summary: "Keep me",
		Tags:    []string{"chat"},
		Content: "# Current note\n\nOriginal",
	})
	if err != nil {
		t.Fatalf("save note: %v", err)
	}
	ctx := commandsContextWithNote(note.ID)

	result, err := service.executeNote(ctx, Command{Name: "note", Args: []string{"append", "--", "Agent output"}})
	if err != nil {
		t.Fatalf("append note: %v", err)
	}
	if !strings.Contains(result.Output, "Appended note "+note.ID) || !result.RefreshSnapshot {
		t.Fatalf("unexpected append result: %+v", result)
	}
	updated, err := manager.Get(note.ID)
	if err != nil {
		t.Fatalf("get appended note: %v", err)
	}
	if !strings.Contains(updated.Content, "Original") || !strings.Contains(updated.Content, "Agent output") || updated.Summary != "Keep me" || len(updated.Tags) != 1 || updated.Tags[0] != "chat" {
		t.Fatalf("append did not preserve note metadata/content: %+v", updated)
	}

	result, err = service.executeNote(ctx, Command{Name: "note", Args: []string{"update", "--", "# Replacement"}})
	if err != nil {
		t.Fatalf("update note: %v", err)
	}
	if !strings.Contains(result.Output, "Updated note "+note.ID) || !result.RefreshSnapshot {
		t.Fatalf("unexpected update result: %+v", result)
	}
	updated, err = manager.Get(note.ID)
	if err != nil {
		t.Fatalf("get updated note: %v", err)
	}
	if strings.Contains(updated.Content, "Original") || updated.Content != "# Replacement" {
		t.Fatalf("unexpected updated content: %q", updated.Content)
	}
}

func commandsContextWithNote(noteID string) context.Context {
	return WithSessionContext(context.Background(), SessionContext{
		Metadata: map[string]string{
			"note_id":         noteID,
			"app_object_type": "note",
			"app_object_id":   noteID,
		},
	})
}

func TestParseRecognizesSlashCommands(t *testing.T) {
	cmd, ok := Parse("/insights")
	if !ok {
		t.Fatal("expected slash command to parse")
	}
	if cmd.Name != "insights" || cmd.Raw != "/insights" {
		t.Fatalf("unexpected parsed command: %+v", cmd)
	}
	if _, ok := Parse("hello"); ok {
		t.Fatal("did not expect plain text to parse as command")
	}
}

func TestExecuteInsightsWritesArtifact(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	service.SetAnalyzer(func(input insights.Input) (*insights.Report, error) {
		return &insights.Report{
			AgentMDAdditions: []string{"Persist this workflow."},
		}, nil
	})

	a := newTestAgent(t, cfg)
	result, err := service.Execute(context.Background(), a, Command{Name: "insights"})
	if err != nil {
		t.Fatalf("execute insights: %v", err)
	}
	if result.ArtifactPath == "" {
		t.Fatalf("expected artifact path, got %+v", result)
	}
	data, err := os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !strings.Contains(string(data), "# Insights") {
		t.Fatalf("expected markdown report, got %q", string(data))
	}
}

func TestExecuteChannelsUsesRuntimeProvider(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	service.SetChannels(func() string { return "Channel runtime status:\n- feishu: enabled=true running=true state=connected" })

	a := newTestAgent(t, cfg)
	result, err := service.Execute(context.Background(), a, Command{Name: "channels"})
	if err != nil {
		t.Fatalf("execute channels: %v", err)
	}
	if !strings.Contains(result.Output, "feishu") {
		t.Fatalf("expected channel status output, got %q", result.Output)
	}
}

func TestExecutePackagesListsCommandAndRoleDeclarations(t *testing.T) {
	cfg := newTestConfig(t)
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "commands"), 0755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(source, "roles"), 0755); err != nil {
		t.Fatalf("mkdir roles: %v", err)
	}
	manifest := `name: agent-kit
version: 0.1.0
resources:
  commands:
    - commands/review.yaml
  roles:
    - roles/reviewer.yaml
`
	if err := os.WriteFile(filepath.Join(source, "godex.package.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "commands", "review.yaml"), []byte("name: review\nmode: agent_turn\nprompt: Review {{args}}\nroles:\n  - agent-kit:reviewer\n"), 0644); err != nil {
		t.Fatalf("write command: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "roles", "reviewer.yaml"), []byte("id: agent-kit:reviewer\nname: Reviewer\n"), 0644); err != nil {
		t.Fatalf("write role: %v", err)
	}
	a := newTestAgent(t, cfg)
	if _, err := a.InstallPackage(source); err != nil {
		t.Fatalf("install package: %v", err)
	}

	service := NewService(cfg)
	commandsResult, err := service.Execute(context.Background(), a, Command{Name: "packages", Args: []string{"commands"}})
	if err != nil {
		t.Fatalf("execute /packages commands: %v", err)
	}
	if !strings.Contains(commandsResult.Output, "/agent-kit review") || !strings.Contains(commandsResult.Output, "agent_turn") {
		t.Fatalf("unexpected package commands output: %q", commandsResult.Output)
	}
	rolesResult, err := service.Execute(context.Background(), a, Command{Name: "packages", Args: []string{"roles"}})
	if err != nil {
		t.Fatalf("execute /packages roles: %v", err)
	}
	if !strings.Contains(rolesResult.Output, "agent-kit:reviewer") {
		t.Fatalf("unexpected package roles output: %q", rolesResult.Output)
	}
	dispatchResult, err := service.Execute(context.Background(), a, Command{Name: "agent-kit", Args: []string{"review", "src"}})
	if err != nil {
		t.Fatalf("execute package command dispatch: %v", err)
	}
	if dispatchResult.Dispatch == nil || dispatchResult.Dispatch.Mode != "agent_turn" || dispatchResult.Dispatch.AgentType != "agent-kit:reviewer" {
		t.Fatalf("unexpected package command dispatch: %+v", dispatchResult)
	}
	if !strings.Contains(dispatchResult.Dispatch.Prompt, "Review src") {
		t.Fatalf("expected rendered prompt to include args, got %q", dispatchResult.Dispatch.Prompt)
	}
}

func TestExecutePackageCommandReportsMissingRoleDiagnostic(t *testing.T) {
	cfg := newTestConfig(t)
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
	if err := os.WriteFile(filepath.Join(source, "godex.package.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	command := `name: review
mode: agent_turn
prompt: Review {{args}}
roles:
  - agent-kit:missing
`
	if err := os.WriteFile(filepath.Join(source, "commands", "review.yaml"), []byte(command), 0644); err != nil {
		t.Fatalf("write command: %v", err)
	}
	a := newTestAgent(t, cfg)
	if _, err := a.InstallPackage(source); err != nil {
		t.Fatalf("install package: %v", err)
	}
	service := NewService(cfg)
	result, err := service.Execute(context.Background(), a, Command{Name: "agent-kit", Args: []string{"review", "src"}})
	if err == nil {
		t.Fatal("expected missing role error")
	}
	if result.DispatchStatus != "failed" || result.DispatchError == "" || len(result.Diagnostics) == 0 {
		t.Fatalf("expected dispatch diagnostics, got result=%+v err=%v", result, err)
	}
}

func TestExecuteModelGetAndSet(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	service.SetModel(func(ctx context.Context, cmd Command) (Result, error) {
		if !reflect.DeepEqual(cmd.Args, []string{"default", "kimi-k2.5"}) {
			t.Fatalf("unexpected model args: %#v", cmd.Args)
		}
		return Result{Name: "model", Output: "Updated default model to kimi-k2.5."}, nil
	})

	a := newTestAgent(t, cfg)
	getResult, err := service.Execute(context.Background(), a, Command{Name: "model", Args: []string{"get"}})
	if err != nil {
		t.Fatalf("model get: %v", err)
	}
	if !strings.Contains(getResult.Output, "test-model") {
		t.Fatalf("unexpected model get output: %q", getResult.Output)
	}

	setResult, err := service.Execute(context.Background(), a, Command{Name: "model", Args: []string{"default", "kimi-k2.5"}})
	if err != nil {
		t.Fatalf("model default: %v", err)
	}
	if !strings.Contains(setResult.Output, "kimi-k2.5") {
		t.Fatalf("unexpected model default output: %q", setResult.Output)
	}
}

func TestExecuteModelListUseAndShorthandRouteToRuntimeHandler(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	var seen [][]string
	service.SetModel(func(ctx context.Context, cmd Command) (Result, error) {
		seen = append(seen, append([]string{}, cmd.Args...))
		if _, ok := CurrentSessionContext(ctx); !ok {
			t.Fatal("expected model command to receive current session context")
		}
		return Result{Name: "model", Output: strings.Join(cmd.Args, " ")}, nil
	})

	a := newTestAgent(t, cfg)
	ctx := WithSessionContext(context.Background(), SessionContext{SessionID: "session-1", Channel: "web", Key: "default"})
	if _, err := service.Execute(ctx, a, Command{Name: "model", Args: []string{"list"}}); err != nil {
		t.Fatalf("model list: %v", err)
	}
	if _, err := service.Execute(ctx, a, Command{Name: "model", Args: []string{"use", "mini"}}); err != nil {
		t.Fatalf("model use: %v", err)
	}
	if _, err := service.Execute(ctx, a, Command{Name: "model", Args: []string{"mini"}}); err != nil {
		t.Fatalf("model shorthand: %v", err)
	}
	want := [][]string{{"list"}, {"use", "mini"}, {"use", "mini"}}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("unexpected routed model args: got %#v want %#v", seen, want)
	}
}

func TestExecuteModelHelp(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	a := newTestAgent(t, cfg)

	result, err := service.Execute(context.Background(), a, Command{Name: "model", Args: []string{"help"}})
	if err != nil {
		t.Fatalf("model help: %v", err)
	}
	for _, want := range []string{"/model list", "/model use <profile-id>", "/model default <profile-or-model>"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("expected model help to contain %q, got %q", want, result.Output)
		}
	}
}

func TestExecuteSessionRoutesToRuntimeHandler(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	service.SetSession(func(ctx context.Context, _ *agent.Agent, cmd Command) (Result, error) {
		current, ok := CurrentSessionContext(ctx)
		if !ok {
			t.Fatal("expected current session context")
		}
		if current.SessionID != "session-1" || current.Channel != "web" || current.Key != "default" {
			t.Fatalf("unexpected session context: %+v", current)
		}
		if !reflect.DeepEqual(cmd.Args, []string{"current"}) {
			t.Fatalf("unexpected session args: %#v", cmd.Args)
		}
		return Result{Name: "session", Output: "Current session:\nsession-1"}, nil
	})

	a := newTestAgent(t, cfg)
	ctx := WithSessionContext(context.Background(), SessionContext{SessionID: "session-1", Channel: "web", Key: "default"})
	result, err := service.Execute(ctx, a, Command{Name: "session", Args: []string{"current"}})
	if err != nil {
		t.Fatalf("session current: %v", err)
	}
	if !strings.Contains(result.Output, "session-1") {
		t.Fatalf("unexpected session output: %q", result.Output)
	}
}

func TestExecuteHistoryShowAndTail(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	a := newTestAgent(t, cfg)
	a.AppendAssistantText("assistant ready", "")
	a.AddEnvelope(message.NewCLIEnvelope("session-1", cfg.LeadName, "hello world", time.Now()))
	a.AppendAssistantDelivery("", protocol.KindBackground, []message.AttachmentRef{{ID: "att-1", Name: "capture.png"}})

	showResult, err := service.Execute(context.Background(), a, Command{Name: "history", Args: []string{"show"}})
	if err != nil {
		t.Fatalf("history show: %v", err)
	}
	if !strings.Contains(showResult.Output, "Conversation history:") || !strings.Contains(showResult.Output, "attachments: capture.png") {
		t.Fatalf("unexpected history show output: %q", showResult.Output)
	}

	tailResult, err := service.Execute(context.Background(), a, Command{Name: "history", Args: []string{"tail", "1"}})
	if err != nil {
		t.Fatalf("history tail: %v", err)
	}
	if strings.Contains(tailResult.Output, "assistant ready") || !strings.Contains(tailResult.Output, "capture.png") {
		t.Fatalf("unexpected history tail output: %q", tailResult.Output)
	}
}

func TestExecuteHistorySearchCurrentSession(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	a := newTestAgent(t, cfg)
	a.AddEnvelope(message.NewCLIEnvelope("session-1", cfg.LeadName, "We settled on the aurora API yesterday.", time.Now()))

	ctx := WithSessionContext(context.Background(), SessionContext{SessionID: "session-1", Channel: "web", Key: "default"})
	result, err := service.Execute(ctx, a, Command{Name: "history", Args: []string{"search", "aurora", "role=user"}})
	if err != nil {
		t.Fatalf("history search current session: %v", err)
	}
	if !strings.Contains(result.Output, "History search:") || !strings.Contains(result.Output, "Scope: current_session") || !strings.Contains(strings.ToLower(result.Output), "aurora") {
		t.Fatalf("unexpected history search output: %q", result.Output)
	}
}

func TestExecuteHistorySearchSessionArchive(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	transcriptName := "transcript_20260424_120000.json"
	writeHistoryTranscript(t, filepath.Join(cfg.TranscriptsDir, transcriptName), []protocol.Message{
		protocol.NewTextMessage(protocol.RoleAssistant, "The PDF lives in ~/Documents/share/The.Go.Programming.Language.2015.11.pdf"),
	})

	a := newTestAgent(t, cfg)
	a.RestoreStateForSession("session-1", agent.SessionState{
		Messages:       []protocol.Message{protocol.NewSummaryMessage("Compacted history available.", transcriptName)},
		TranscriptRefs: []string{transcriptName},
	})

	ctx := WithSessionContext(context.Background(), SessionContext{SessionID: "session-1", Channel: "web", Key: "default"})
	result, err := service.Execute(ctx, a, Command{Name: "history", Args: []string{"search", "Programming.Language", "scope=session_archive"}})
	if err != nil {
		t.Fatalf("history search session archive: %v", err)
	}
	if !strings.Contains(result.Output, "Scope: session_archive") || !strings.Contains(result.Output, "Programming.Language") {
		t.Fatalf("unexpected archive search output: %q", result.Output)
	}
}

func TestExecuteHistorySearchAllArchives(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	writeHistorySessionArchive(t, cfg, "session-a", "Alpha session", time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC), []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "Project aurora has a rollback checklist."),
	}, nil)

	a := newTestAgent(t, cfg)
	result, err := service.Execute(context.Background(), a, Command{Name: "history", Args: []string{"search", "rollback", "checklist", "scope=all_archives"}})
	if err != nil {
		t.Fatalf("history search all archives: %v", err)
	}
	if !strings.Contains(result.Output, "Scope: all_archives") || !strings.Contains(result.Output, "Alpha session") {
		t.Fatalf("unexpected all archives output: %q", result.Output)
	}
}

func TestExecuteHistorySearchRejectsInvalidArgs(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	a := newTestAgent(t, cfg)

	_, err := service.Execute(context.Background(), a, Command{Name: "history", Args: []string{"search", "aurora", "scope=bad"}})
	if err == nil || !strings.Contains(err.Error(), "usage: /history search") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestExecuteCronRoutesToRuntimeHandler(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	service.SetCron(func(ctx context.Context, cmd Command) (Result, error) {
		if cmd.Name != "cron" {
			t.Fatalf("expected cron command, got %q", cmd.Name)
		}
		if !reflect.DeepEqual(cmd.Args, []string{"list"}) {
			t.Fatalf("unexpected args: %#v", cmd.Args)
		}
		return Result{Name: "cron", Output: "Cron jobs:\n- job-1"}, nil
	})

	a := newTestAgent(t, cfg)
	result, err := service.Execute(context.Background(), a, Command{Name: "cron", Args: []string{"list"}})
	if err != nil {
		t.Fatalf("execute cron: %v", err)
	}
	if !strings.Contains(result.Output, "job-1") {
		t.Fatalf("expected cron output, got %q", result.Output)
	}
}

func writeHistorySessionArchive(t *testing.T, cfg *config.Config, sessionID, title string, updatedAt time.Time, messages []protocol.Message, refs []string) {
	t.Helper()
	dir := filepath.Join(cfg.SessionsDir, sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir session archive dir: %v", err)
	}
	manifestData, err := json.MarshalIndent(map[string]interface{}{
		"session_id": sessionID,
		"title":      title,
		"updated_at": updatedAt,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestData, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	stateData, err := json.MarshalIndent(agent.SessionState{
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

func writeHistoryTranscript(t *testing.T, path string, messages []protocol.Message) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	data, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		t.Fatalf("marshal transcript: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func TestExecuteHeartbeatRoutesToRuntimeHandler(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	service.SetHeartbeat(func(ctx context.Context, cmd Command) (Result, error) {
		if cmd.Name != "heartbeat" {
			t.Fatalf("expected heartbeat command, got %q", cmd.Name)
		}
		if !reflect.DeepEqual(cmd.Args, []string{"test"}) {
			t.Fatalf("unexpected args: %#v", cmd.Args)
		}
		return Result{Name: "heartbeat", Output: "Heartbeat test finished"}, nil
	})

	a := newTestAgent(t, cfg)
	result, err := service.Execute(context.Background(), a, Command{Name: "heartbeat", Args: []string{"test"}})
	if err != nil {
		t.Fatalf("execute heartbeat: %v", err)
	}
	if !strings.Contains(result.Output, "Heartbeat test") {
		t.Fatalf("expected heartbeat output, got %q", result.Output)
	}
}

func TestExecuteCronWithoutRuntimeReturnsAvailabilityMessage(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)

	a := newTestAgent(t, cfg)
	result, err := service.Execute(context.Background(), a, Command{Name: "cron", Args: []string{"list"}})
	if err != nil {
		t.Fatalf("execute cron: %v", err)
	}
	if !strings.Contains(result.Output, "unavailable") {
		t.Fatalf("expected runtime unavailable message, got %q", result.Output)
	}
}

func TestExecuteModelSetWithoutRuntimeReturnsAvailabilityMessage(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	a := newTestAgent(t, cfg)

	result, err := service.Execute(context.Background(), a, Command{Name: "model", Args: []string{"set", "kimi-k2.5"}})
	if err != nil {
		t.Fatalf("execute model set: %v", err)
	}
	if !strings.Contains(result.Output, "unavailable") {
		t.Fatalf("expected runtime unavailable message, got %q", result.Output)
	}
}

func TestExecuteSkillsSupportsLoadExpandActiveAndUnload(t *testing.T) {
	cfg := newTestConfig(t)
	writeTestSkill(t, cfg.SkillsDir, "review-helper", `---
description: Review code changes with a checklist
recommended_bundles:
  - background
sections:
  - core
  - workflow
---
## Core
Look for bugs and regressions.

## Workflow
Read the diff first.`)

	service := NewService(cfg)
	a := newTestAgent(t, cfg)

	listResult, err := service.Execute(context.Background(), a, Command{Name: "skills", Args: []string{"list"}})
	if err != nil {
		t.Fatalf("skills list: %v", err)
	}
	if !strings.Contains(listResult.Output, "review-helper") {
		t.Fatalf("expected discovered skill in list output, got %q", listResult.Output)
	}

	getResult, err := service.Execute(context.Background(), a, Command{Name: "skills", Args: []string{"get", "review-helper"}})
	if err != nil {
		t.Fatalf("skills get: %v", err)
	}
	if !strings.Contains(getResult.Output, "sections: core, workflow") {
		t.Fatalf("expected detailed skill output, got %q", getResult.Output)
	}

	loadResult, err := service.Execute(context.Background(), a, Command{Name: "skills", Args: []string{"load", "review-helper"}})
	if err != nil {
		t.Fatalf("skills load: %v", err)
	}
	if !loadResult.RefreshSnapshot || !strings.Contains(loadResult.Output, "review-helper: activated") {
		t.Fatalf("unexpected load result: %+v", loadResult)
	}

	activeResult, err := service.Execute(context.Background(), a, Command{Name: "skills", Args: []string{"active"}})
	if err != nil {
		t.Fatalf("skills active: %v", err)
	}
	if !strings.Contains(activeResult.Output, "Active skills:") || !strings.Contains(activeResult.Output, "review-helper [core]") {
		t.Fatalf("unexpected active output: %q", activeResult.Output)
	}

	expandResult, err := service.Execute(context.Background(), a, Command{Name: "skills", Args: []string{"expand", "review-helper", "workflow"}})
	if err != nil {
		t.Fatalf("skills expand: %v", err)
	}
	if !expandResult.RefreshSnapshot || !strings.Contains(expandResult.Output, "expanded sections: workflow") {
		t.Fatalf("unexpected expand result: %+v", expandResult)
	}

	unloadResult, err := service.Execute(context.Background(), a, Command{Name: "skills", Args: []string{"unload", "review-helper"}})
	if err != nil {
		t.Fatalf("skills unload: %v", err)
	}
	if !unloadResult.RefreshSnapshot || !strings.Contains(unloadResult.Output, "review-helper: unloaded") {
		t.Fatalf("unexpected unload result: %+v", unloadResult)
	}
}

func TestExecuteSkillsInstall(t *testing.T) {
	cfg := newTestConfig(t)
	sourceDir := filepath.Join(t.TempDir(), "playwright-cli")
	writeTestSkill(t, sourceDir, "", `---
description: Browser automation helpers
recommended_bundles:
  - web
sections:
  - core
  - workflow
---
## Core
Capture screenshots.

## Workflow
Open then capture.`)

	service := NewService(cfg)
	a := newTestAgent(t, cfg)

	result, err := service.Execute(context.Background(), a, Command{Name: "skills", Args: []string{"install", sourceDir, "playwright-cli"}})
	if err != nil {
		t.Fatalf("skills install: %v", err)
	}
	if !strings.Contains(result.Output, "playwright-cli: installed") || !strings.Contains(result.Output, "source: "+sourceDir) {
		t.Fatalf("unexpected install output: %q", result.Output)
	}

	listResult, err := service.Execute(context.Background(), a, Command{Name: "skills", Args: []string{"list"}})
	if err != nil {
		t.Fatalf("skills list after install: %v", err)
	}
	if !strings.Contains(listResult.Output, "playwright-cli") {
		t.Fatalf("expected installed skill in list output, got %q", listResult.Output)
	}
}

func TestExecuteSkillsSources(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	a := newTestAgent(t, cfg)

	result, err := service.Execute(context.Background(), a, Command{Name: "skills", Args: []string{"sources"}})
	if err != nil {
		t.Fatalf("skills sources: %v", err)
	}
	if !strings.Contains(result.Output, "Skill sources:") || !strings.Contains(result.Output, "playwright-cli") {
		t.Fatalf("unexpected sources output: %q", result.Output)
	}
}

func TestExecuteMemoryCommands(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	a := newTestAgent(t, cfg)

	listResult, err := service.Execute(context.Background(), a, Command{Name: "memory", Args: []string{"list"}})
	if err != nil {
		t.Fatalf("memory list empty: %v", err)
	}
	if !strings.Contains(listResult.Output, "No durable memories yet.") {
		t.Fatalf("unexpected empty list output: %q", listResult.Output)
	}

	entry, err := a.MemoryMgr().Remember(memory.SaveInput{
		Title:   "Delivery Rule",
		Summary: "Prefer explicit delivery confirmations.",
		Content: "When automation delivers to a channel, make the result visible to the user.",
		Type:    memory.TypeProject,
		Source:  "manual",
		Tags:    []string{"automation", "delivery"},
	})
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	listResult, err = service.Execute(context.Background(), a, Command{Name: "memory", Args: []string{"list"}})
	if err != nil {
		t.Fatalf("memory list: %v", err)
	}
	for _, want := range []string{"Durable memories:", "Delivery Rule", "automation,delivery", "source=manual"} {
		if !strings.Contains(listResult.Output, want) {
			t.Fatalf("expected list output to contain %q, got %q", want, listResult.Output)
		}
	}

	getResult, err := service.Execute(context.Background(), a, Command{Name: "memory", Args: []string{"get", entry.ID}})
	if err != nil {
		t.Fatalf("memory get: %v", err)
	}
	if !strings.Contains(getResult.Output, "When automation delivers to a channel") {
		t.Fatalf("unexpected get output: %q", getResult.Output)
	}

	searchResult, err := service.Execute(context.Background(), a, Command{Name: "memory", Args: []string{"search", "delivery"}})
	if err != nil {
		t.Fatalf("memory search: %v", err)
	}
	if !strings.Contains(searchResult.Output, "Memory search results:") || !strings.Contains(searchResult.Output, "Delivery Rule") {
		t.Fatalf("unexpected search output: %q", searchResult.Output)
	}
}

func TestExecuteMemoryCandidateCommands(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	a := newTestAgent(t, cfg)

	if _, err := a.MemoryMgr().Remember(memory.SaveInput{
		Title:   "Existing Workflow",
		Summary: "Seed one memory.",
		Content: "Seed body.",
		Type:    memory.TypeWorkflow,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	if _, err := a.MemoryMgr().DismissCandidate("missing"); err == nil {
		t.Fatal("expected dismissing missing candidate to fail")
	}
	if _, err := a.MemoryMgr().DismissCandidate(""); err == nil {
		t.Fatal("expected empty dismiss fingerprint to fail")
	}
	if _, err := a.MemoryMgr().AcceptCandidate(""); err == nil {
		t.Fatal("expected empty accept fingerprint to fail")
	}

	// Seed the inbox directly through the extractor-compatible path.
	extractor := memory.NewExtractor(a.MemoryMgr(), cfg.TempDir)
	added, err := extractor.Capture([]protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "以后请用中文回复。"),
		protocol.NewTextMessage(protocol.RoleAssistant, "好的，我之后会使用中文回复。"),
	})
	if err != nil {
		t.Fatalf("capture candidate: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected one candidate, got %+v", added)
	}

	candidatesResult, err := service.Execute(context.Background(), a, Command{Name: "memory", Args: []string{"candidates"}})
	if err != nil {
		t.Fatalf("memory candidates: %v", err)
	}
	if !strings.Contains(candidatesResult.Output, "Pending memory candidates:") || !strings.Contains(candidatesResult.Output, "User Preference: Reply in Chinese") {
		t.Fatalf("unexpected candidates output: %q", candidatesResult.Output)
	}

	acceptResult, err := service.Execute(context.Background(), a, Command{Name: "memory", Args: []string{"accept", added[0].Fingerprint}})
	if err != nil {
		t.Fatalf("memory accept: %v", err)
	}
	if !strings.Contains(acceptResult.Output, "Accepted memory candidate") {
		t.Fatalf("unexpected accept output: %q", acceptResult.Output)
	}

	added, err = extractor.Capture([]protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "修一下 runtime。"),
		protocol.NewTextMessage(protocol.RoleAssistant, "Run go test ./... after Go changes."),
	})
	if err != nil {
		t.Fatalf("capture second candidate: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected one second candidate, got %+v", added)
	}

	dismissResult, err := service.Execute(context.Background(), a, Command{Name: "memory", Args: []string{"dismiss", added[0].Fingerprint}})
	if err != nil {
		t.Fatalf("memory dismiss: %v", err)
	}
	if !strings.Contains(dismissResult.Output, "Dismissed memory candidate") {
		t.Fatalf("unexpected dismiss output: %q", dismissResult.Output)
	}
}

func TestExecuteMemoryAuditCommands(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	a := newTestAgent(t, cfg)

	entry, err := a.MemoryMgr().Remember(memory.SaveInput{
		Title:   "Delivery Rule",
		Summary: "Prefer explicit delivery confirmations.",
		Content: "Initial body.",
		Type:    memory.TypeProject,
		Source:  "manual",
	})
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	if _, err := a.MemoryMgr().Update(memory.UpdateInput{
		Match:   memory.ForgetInput{Title: "Delivery Rule"},
		Title:   "Delivery Rule",
		Summary: "Prefer explicit delivery confirmations.",
		Content: "Updated body.",
		Type:    memory.TypeProject,
		Source:  "manual",
	}); err != nil {
		t.Fatalf("update memory: %v", err)
	}

	logResult, err := service.Execute(context.Background(), a, Command{Name: "memory-log", Args: []string{"10"}})
	if err != nil {
		t.Fatalf("memory log: %v", err)
	}
	if !strings.Contains(logResult.Output, "Durable memory audit log:") || !strings.Contains(logResult.Output, "update") {
		t.Fatalf("unexpected audit log output: %q", logResult.Output)
	}

	log, err := a.MemoryMgr().ListAudit(10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var updateID string
	for _, item := range log {
		if item.Action == memory.AuditUpdate && item.MemoryID == entry.ID {
			updateID = item.ID
			break
		}
	}
	if updateID == "" {
		t.Fatalf("expected update audit entry, got %+v", log)
	}

	restoreResult, err := service.Execute(context.Background(), a, Command{Name: "memory-restore", Args: []string{updateID}})
	if err != nil {
		t.Fatalf("memory restore: %v", err)
	}
	if !restoreResult.RefreshSnapshot || !strings.Contains(restoreResult.Output, "Restored before snapshot") {
		t.Fatalf("unexpected restore result: %+v", restoreResult)
	}
	restored, err := a.MemoryMgr().Get(entry.ID)
	if err != nil {
		t.Fatalf("get restored memory: %v", err)
	}
	if restored.Content != "Initial body." {
		t.Fatalf("expected restored content, got %q", restored.Content)
	}

	aliasResult, err := service.Execute(context.Background(), a, Command{Name: "memory", Args: []string{"log", "1"}})
	if err != nil {
		t.Fatalf("memory log alias: %v", err)
	}
	if !strings.Contains(aliasResult.Output, "Durable memory audit log:") {
		t.Fatalf("unexpected alias output: %q", aliasResult.Output)
	}
}

func TestExecuteMemoryDigestCreatesReviewCandidates(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	a := newTestAgent(t, cfg)
	service.SetAnalyzer(func(input insights.Input) (*insights.Report, error) {
		return &insights.Report{
			AgentMDAdditions: []string{
				"Consider capturing this stable collaboration preference in `.godex/AGENT.local.md`: Reply in Chinese when the user writes Chinese.",
				"Consider codifying this recurring workflow in `AGENT.md` or `.godex/rules/*.md`: Run go test after Go changes.",
			},
			Frictions: []string{"Tool permission mismatches are showing up in conversation traces."},
		}, nil
	})

	result, err := service.Execute(context.Background(), a, Command{Name: "memory-digest"})
	if err != nil {
		t.Fatalf("memory digest: %v", err)
	}
	if !result.RefreshSnapshot || result.ArtifactPath == "" {
		t.Fatalf("expected digest artifact and refresh, got %+v", result)
	}
	for _, want := range []string{"Memory digest completed.", "Added 3 durable-memory candidate", "Reply in Chinese", "# Insights"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("expected digest output to contain %q, got %q", want, result.Output)
		}
	}
	candidates, err := a.MemoryMgr().ListCandidates()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %+v", candidates)
	}
}

func TestExecuteMemoryCommandUsageErrors(t *testing.T) {
	cfg := newTestConfig(t)
	service := NewService(cfg)
	a := newTestAgent(t, cfg)

	for _, cmd := range []Command{
		{Name: "memory", Args: []string{"get"}},
		{Name: "memory", Args: []string{"search"}},
		{Name: "memory", Args: []string{"accept"}},
		{Name: "memory", Args: []string{"dismiss"}},
		{Name: "memory-restore"},
	} {
		if _, err := service.Execute(context.Background(), a, cmd); err == nil {
			t.Fatalf("expected usage error for command %+v", cmd)
		}
	}
}

func newTestAgent(t *testing.T, cfg *config.Config) *agent.Agent {
	t.Helper()
	a := agent.New(cfg)
	a.RegisterTools()
	return a
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

func writeTestSkill(t *testing.T, skillsDir, name, body string) {
	t.Helper()
	path := filepath.Join(skillsDir, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
}
