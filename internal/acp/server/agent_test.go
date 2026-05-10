package server

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/tim5wang/godex/internal/services/backend"
)

type capturingClient struct {
	messages atomic.Pointer[[]string]
	updates  atomic.Pointer[[]acp.SessionUpdate]
}

func (c *capturingClient) append(text string) {
	for {
		cur := c.messages.Load()
		var next []string
		if cur != nil {
			next = append(next, *cur...)
		}
		next = append(next, text)
		if c.messages.CompareAndSwap(cur, &next) {
			return
		}
	}
}

func (c *capturingClient) appendUpdate(update acp.SessionUpdate) {
	for {
		cur := c.updates.Load()
		var next []acp.SessionUpdate
		if cur != nil {
			next = append(next, *cur...)
		}
		next = append(next, update)
		if c.updates.CompareAndSwap(cur, &next) {
			return
		}
	}
}

func (c *capturingClient) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	c.appendUpdate(n.Update)
	if n.Update.AgentMessageChunk != nil {
		blk := n.Update.AgentMessageChunk.Content
		if blk.Text != nil {
			c.append(blk.Text.Text)
		}
	}
	return nil
}

func (c *capturingClient) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, errors.New("requestPermission not used in skeleton tests")
}

func (c *capturingClient) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errors.New("readTextFile not used in skeleton tests")
}

func (c *capturingClient) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errors.New("writeTextFile not used in skeleton tests")
}

func (c *capturingClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errors.New("createTerminal not used in skeleton tests")
}

func (c *capturingClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errors.New("killTerminal not used in skeleton tests")
}

func (c *capturingClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errors.New("releaseTerminal not used in skeleton tests")
}

func (c *capturingClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errors.New("terminalOutput not used in skeleton tests")
}

func (c *capturingClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errors.New("waitForTerminalExit not used in skeleton tests")
}

func waitForUpdates(t *testing.T, captures *capturingClient) []acp.SessionUpdate {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		updates := captures.updates.Load()
		if updates != nil && len(*updates) > 0 {
			return *updates
		}
		time.Sleep(10 * time.Millisecond)
	}
	updates := captures.updates.Load()
	if updates == nil {
		t.Fatal("expected session updates")
	}
	return *updates
}

func waitForUpdateCount(t *testing.T, captures *capturingClient, count int) []acp.SessionUpdate {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		updates := captures.updates.Load()
		if updates != nil && len(*updates) >= count {
			return *updates
		}
		time.Sleep(10 * time.Millisecond)
	}
	updates := captures.updates.Load()
	if updates == nil {
		t.Fatalf("expected at least %d session updates, got none", count)
	}
	t.Fatalf("expected at least %d session updates, got %d: %+v", count, len(*updates), *updates)
	return nil
}

// pipePair returns two io.ReadWriteClosers cross-wired by a pair of io.Pipe
// endpoints, simulating a bidirectional stdio transport between an ACP client
// and an ACP agent.
func pipePair() (clientRW, agentRW *pipeRW) {
	c2aR, c2aW := io.Pipe() // client -> agent
	a2cR, a2cW := io.Pipe() // agent -> client
	clientRW = &pipeRW{r: a2cR, w: c2aW}
	agentRW = &pipeRW{r: c2aR, w: a2cW}
	return
}

type pipeRW struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p *pipeRW) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeRW) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeRW) Close() error {
	_ = p.r.Close()
	return p.w.Close()
}

// wireAgent wires a new agent-side connection around the supplied Agent using
// the given agent-side pipe, and returns a cleanup func.
func wireAgent(t *testing.T, a *Agent, rw *pipeRW) func() {
	t.Helper()
	conn := acp.NewAgentSideConnection(a, rw, rw)
	a.SetAgentConnection(conn)
	return func() { _ = rw.Close() }
}

type fakeSessionFeatures struct {
	view         backend.ModelsView
	setProfileID string
}

func (f *fakeSessionFeatures) EnsureSession(context.Context, string) (string, error) {
	return "backend-session", nil
}

func (f *fakeSessionFeatures) Models(context.Context, string) (backend.ModelsView, error) {
	return f.view, nil
}

func (f *fakeSessionFeatures) SetSessionModelProfile(_ context.Context, _ string, profileID string) (backend.ModelsView, error) {
	f.setProfileID = profileID
	f.view.SessionProfileID = profileID
	for idx := range f.view.Profiles {
		f.view.Profiles[idx].Selected = f.view.Profiles[idx].ID == profileID
	}
	return f.view, nil
}

func TestAgentInitializeAdvertisesGodex(t *testing.T) {
	clientRW, agentRW := pipePair()
	defer clientRW.Close()

	a := &Agent{AgentInfo: acp.Implementation{Name: "godex-test", Version: "1.2.3"}}
	defer wireAgent(t, a, agentRW)()

	clientConn := acp.NewClientSideConnection(&capturingClient{}, clientRW, clientRW)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := clientConn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersion(acp.ProtocolVersionNumber),
		ClientInfo:      &acp.Implementation{Name: "godex-test-client"},
	})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if resp.AgentInfo == nil {
		t.Fatalf("AgentInfo = nil, want godex info")
	}
	if resp.AgentInfo.Name != "godex-test" || resp.AgentInfo.Version != "1.2.3" {
		t.Fatalf("AgentInfo = %+v, want {Name:godex-test Version:1.2.3}", resp.AgentInfo)
	}
	if int(resp.ProtocolVersion) != acp.ProtocolVersionNumber {
		t.Fatalf("ProtocolVersion = %d, want %d", resp.ProtocolVersion, acp.ProtocolVersionNumber)
	}
	if !resp.AgentCapabilities.PromptCapabilities.EmbeddedContext {
		t.Fatalf("EmbeddedContext capability = false, want true")
	}
	if resp.AgentCapabilities.PromptCapabilities.Image || resp.AgentCapabilities.PromptCapabilities.Audio {
		t.Fatalf("unexpected image/audio capabilities: %+v", resp.AgentCapabilities.PromptCapabilities)
	}
}

func TestAgentNewSessionAndPromptFlow(t *testing.T) {
	clientRW, agentRW := pipePair()
	defer clientRW.Close()

	a := &Agent{}
	defer wireAgent(t, a, agentRW)()

	captures := &capturingClient{}
	clientConn := acp.NewClientSideConnection(captures, clientRW, clientRW)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := clientConn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersion(acp.ProtocolVersionNumber),
	}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	sessResp, err := clientConn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        "/tmp",
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if strings.TrimSpace(string(sessResp.SessionId)) == "" {
		t.Fatalf("empty SessionId")
	}

	promptResp, err := clientConn.Prompt(ctx, acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("hello godex")},
	})
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if promptResp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", promptResp.StopReason, acp.StopReasonEndTurn)
	}

	msgs := captures.messages.Load()
	if msgs == nil || len(*msgs) == 0 {
		t.Fatalf("no session updates captured")
	}
	got := strings.TrimSpace(strings.Join(*msgs, ""))
	if got != "echo: hello godex" {
		t.Fatalf("captured message = %q, want %q", got, "echo: hello godex")
	}
}

func TestAgentNewSessionSendsAvailableCommands(t *testing.T) {
	clientRW, agentRW := pipePair()
	defer clientRW.Close()

	a := &Agent{}
	defer wireAgent(t, a, agentRW)()

	captures := &capturingClient{}
	clientConn := acp.NewClientSideConnection(captures, clientRW, clientRW)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := clientConn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersion(acp.ProtocolVersionNumber)}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if _, err := clientConn.NewSession(ctx, acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	updates := waitForUpdates(t, captures)
	commandsByName := map[string]acp.AvailableCommand{}
	for _, update := range updates {
		if update.AvailableCommandsUpdate == nil {
			continue
		}
		for _, cmd := range update.AvailableCommandsUpdate.AvailableCommands {
			commandsByName[cmd.Name] = cmd
		}
	}
	for _, name := range []string{"model", "approve", "todos"} {
		cmd, ok := commandsByName[name]
		if !ok {
			t.Fatalf("missing ACP command %q in updates: %+v", name, updates)
		}
		if strings.TrimSpace(cmd.Description) == "" {
			t.Fatalf("ACP command %q has empty description", name)
		}
		if cmd.Input == nil || cmd.Input.Unstructured == nil {
			t.Fatalf("ACP command %q missing unstructured input hint", name)
		}
	}
}

func TestAgentPromptResendsAvailableCommands(t *testing.T) {
	clientRW, agentRW := pipePair()
	defer clientRW.Close()

	a := &Agent{}
	defer wireAgent(t, a, agentRW)()

	captures := &capturingClient{}
	clientConn := acp.NewClientSideConnection(captures, clientRW, clientRW)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := clientConn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersion(acp.ProtocolVersionNumber)}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	sessResp, err := clientConn.NewSession(ctx, acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	waitForUpdates(t, captures)
	if _, err := clientConn.Prompt(ctx, acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
	}); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	updates := waitForUpdateCount(t, captures, 2)
	commandUpdates := 0
	for _, update := range updates {
		if update.AvailableCommandsUpdate != nil {
			commandUpdates++
		}
	}
	if commandUpdates < 2 {
		t.Fatalf("expected commands to be sent after session creation and prompt, got %d updates: %+v", commandUpdates, updates)
	}
}

func TestExtractPromptTextHandlesResources(t *testing.T) {
	mime := "application/octet-stream"
	got := extractPromptText([]acp.ContentBlock{
		acp.TextBlock("open "),
		acp.ResourceLinkBlock("README", "file:///workspace/README.md"),
		acp.ResourceBlock(acp.EmbeddedResourceResource{
			TextResourceContents: &acp.TextResourceContents{
				Uri:  "file:///workspace/context.md",
				Text: "embedded context",
			},
		}),
		acp.ResourceBlock(acp.EmbeddedResourceResource{
			BlobResourceContents: &acp.BlobResourceContents{
				Uri:      "file:///workspace/image.bin",
				MimeType: &mime,
				Blob:     "AAAA",
			},
		}),
	})

	for _, want := range []string{
		"open ",
		"[resource: README <file:///workspace/README.md>]",
		"[resource: file:///workspace/context.md]",
		"embedded context",
		"[resource: file:///workspace/image.bin; mime=application/octet-stream]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("extractPromptText() = %q, want substring %q", got, want)
		}
	}
}

func TestExtractPromptTextReadsWorkspaceFileResourceLinkRange(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := strings.Join([]string{
		"package main",
		"",
		"func main() {",
		"\tprintln(\"hello\")",
		"}",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	got := extractPromptText([]acp.ContentBlock{
		acp.TextBlock("review selection\n"),
		acp.ResourceLinkBlock("main.go", "file://"+path+"#L3-L4"),
	}, workspace)

	if !strings.Contains(got, "review selection") {
		t.Fatalf("missing prompt text: %q", got)
	}
	if !strings.Contains(got, "[resource: main.go <file://"+path+"#L3-L4>]") {
		t.Fatalf("missing resource header: %q", got)
	}
	if !strings.Contains(got, "func main() {") || !strings.Contains(got, "\tprintln(\"hello\")") {
		t.Fatalf("missing selected lines: %q", got)
	}
	if strings.Contains(got, "package main") || strings.Contains(got, "\n}") {
		t.Fatalf("included lines outside selection: %q", got)
	}
}

func TestExtractPromptTextDoesNotReadOutsideWorkspaceResourceLink(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	path := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(path, []byte("secret-token"), 0644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	got := extractPromptText([]acp.ContentBlock{
		acp.ResourceLinkBlock("secret.txt", "file://"+path+"#L1"),
	}, workspace)

	if strings.Contains(got, "secret-token") {
		t.Fatalf("outside workspace content leaked: %q", got)
	}
	if !strings.Contains(got, "outside workspace") {
		t.Fatalf("missing outside workspace diagnostic: %q", got)
	}
}

func TestAgentPromptDoesNotRepeatStreamedFinalText(t *testing.T) {
	clientRW, agentRW := pipePair()
	defer clientRW.Close()

	a := &Agent{
		Handler: func(ctx context.Context, turn PromptTurn) (PromptResult, error) {
			if turn.Updater == nil {
				t.Fatal("missing updater")
			}
			if err := turn.Updater.Update(ctx, acp.UpdateAgentMessageText("hello ")); err != nil {
				return PromptResult{}, err
			}
			if err := turn.Updater.Update(ctx, acp.UpdateAgentMessageText("world")); err != nil {
				return PromptResult{}, err
			}
			return PromptResult{FinalText: "hello world", StopReason: acp.StopReasonEndTurn, Streamed: true}, nil
		},
	}
	defer wireAgent(t, a, agentRW)()

	captures := &capturingClient{}
	clientConn := acp.NewClientSideConnection(captures, clientRW, clientRW)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := clientConn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersion(acp.ProtocolVersionNumber)}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	sessResp, err := clientConn.NewSession(ctx, acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := clientConn.Prompt(ctx, acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("stream")},
	}); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	msgs := captures.messages.Load()
	if msgs == nil {
		t.Fatalf("no session updates captured")
	}
	if got := strings.Join(*msgs, ""); got != "hello world" {
		t.Fatalf("captured message = %q, want streamed text once", got)
	}
	if got := len(*msgs); got != 2 {
		t.Fatalf("captured chunks = %d, want only streamed chunks", got)
	}
}

func TestAgentPromptUnknownSessionErrors(t *testing.T) {
	a := &Agent{}
	_, err := a.Prompt(context.Background(), acp.PromptRequest{
		SessionId: acp.SessionId("does-not-exist"),
		Prompt:    []acp.ContentBlock{acp.TextBlock("x")},
	})
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error = %q, want it to mention session id", err.Error())
	}
}

func TestAgentCustomHandlerRuns(t *testing.T) {
	var called atomic.Int32
	a := &Agent{
		Handler: func(_ context.Context, turn PromptTurn) (PromptResult, error) {
			called.Add(1)
			if turn.Prompt != "ping" {
				return PromptResult{}, errors.New("unexpected prompt")
			}
			return PromptResult{FinalText: "pong", StopReason: acp.StopReasonEndTurn}, nil
		},
	}
	sessResp, err := a.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	resp, err := a.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("ping")},
	})
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want end_turn", resp.StopReason)
	}
	if called.Load() != 1 {
		t.Fatalf("handler called %d times, want 1", called.Load())
	}
}

func TestAgentUnsupportedMethodsReturnMethodNotFound(t *testing.T) {
	a := &Agent{}

	// ListSessions is now implemented; verify it returns a valid response.
	sessResp, err := a.ListSessions(context.Background(), acp.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions err = %v, want nil", err)
	}
	if sessResp.Sessions == nil {
		t.Fatalf("ListSessions.Sessions = nil, want empty slice")
	}

	if _, err := a.SetSessionMode(context.Background(), acp.SetSessionModeRequest{}); !isMethodNotFound(err) {
		t.Fatalf("SetSessionMode err = %v, want method-not-found", err)
	}
}

func TestAgentNewSessionAndLoadSessionReturnModelState(t *testing.T) {
	features := &fakeSessionFeatures{view: backend.ModelsView{
		DefaultProfileID: "sonnet",
		Profiles: []backend.ModelProfile{
			{ID: "sonnet", Name: "Claude Sonnet", Provider: "anthropic", Model: "claude-sonnet", Default: true},
			{ID: "mini", Name: "GPT Mini", Provider: "codex", Model: "gpt-5.4-mini"},
		},
	}}
	a := &Agent{Features: features}

	newResp, err := a.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if newResp.Models == nil || newResp.Models.CurrentModelId != acp.ModelId("sonnet") {
		t.Fatalf("unexpected new session models: %+v", newResp.Models)
	}
	if len(newResp.Models.AvailableModels) != 2 {
		t.Fatalf("expected two models, got %+v", newResp.Models.AvailableModels)
	}

	features.view.SessionProfileID = "mini"
	loadResp, err := a.LoadSession(context.Background(), acp.LoadSessionRequest{
		SessionId:  newResp.SessionId,
		Cwd:        "/tmp",
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if loadResp.Models == nil || loadResp.Models.CurrentModelId != acp.ModelId("mini") {
		t.Fatalf("unexpected loaded session models: %+v", loadResp.Models)
	}
}

func TestAgentSetSessionModelAndConfigOption(t *testing.T) {
	features := &fakeSessionFeatures{view: backend.ModelsView{
		DefaultProfileID: "sonnet",
		Profiles: []backend.ModelProfile{
			{ID: "sonnet", Name: "Claude Sonnet", Provider: "anthropic", Model: "claude-sonnet", Default: true},
			{ID: "mini", Name: "GPT Mini", Provider: "codex", Model: "gpt-5.4-mini"},
		},
	}}
	a := &Agent{Features: features}
	sessResp, err := a.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	if _, err := a.UnstableSetSessionModel(context.Background(), acp.UnstableSetSessionModelRequest{
		SessionId: sessResp.SessionId,
		ModelId:   acp.UnstableModelId("mini"),
	}); err != nil {
		t.Fatalf("UnstableSetSessionModel() error = %v", err)
	}
	if features.setProfileID != "mini" {
		t.Fatalf("expected set profile mini, got %q", features.setProfileID)
	}

	if _, err := a.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: sessResp.SessionId,
			ConfigId:  acp.SessionConfigId("model_profile"),
			Value:     acp.SessionConfigValueId("sonnet"),
		},
	}); err != nil {
		t.Fatalf("SetSessionConfigOption() error = %v", err)
	}
	if features.setProfileID != "sonnet" {
		t.Fatalf("expected set profile sonnet, got %q", features.setProfileID)
	}
}

func isMethodNotFound(err error) bool {
	var re *acp.RequestError
	if errors.As(err, &re) {
		return re.Code == -32601
	}
	return false
}

func TestServeRejectsMissingConfig(t *testing.T) {
	if err := Serve(context.Background(), ServeConfig{}); err == nil {
		t.Fatal("expected error when agent is missing")
	}
	if err := Serve(context.Background(), ServeConfig{Agent: &Agent{}}); err == nil {
		t.Fatal("expected error when streams are missing")
	}
}

func TestAgentLoadSession(t *testing.T) {
	a := &Agent{}

	// LoadSession for unknown id creates a new session.
	resp, err := a.LoadSession(context.Background(), acp.LoadSessionRequest{
		SessionId: acp.SessionId("loaded-sess"),
		Cwd:       "/home/user",
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	_ = resp

	// Prompt to the loaded session should succeed.
	promptResp, err := a.Prompt(context.Background(), acp.PromptRequest{
		SessionId: acp.SessionId("loaded-sess"),
		Prompt:    []acp.ContentBlock{acp.TextBlock("test")},
	})
	if err != nil {
		t.Fatalf("Prompt() after LoadSession error = %v", err)
	}
	if promptResp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want end_turn", promptResp.StopReason)
	}
}

func TestAgentCancel(t *testing.T) {
	handlerStarted := make(chan struct{})
	handlerCanceled := make(chan struct{})

	a := &Agent{
		Handler: func(ctx context.Context, turn PromptTurn) (PromptResult, error) {
			close(handlerStarted)
			select {
			case <-ctx.Done():
				close(handlerCanceled)
				return PromptResult{StopReason: acp.StopReasonCancelled}, nil
			case <-time.After(5 * time.Second):
				return PromptResult{FinalText: "timeout", StopReason: acp.StopReasonEndTurn}, nil
			}
		},
	}

	sessResp, err := a.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	promptDone := make(chan acp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		resp, err := a.Prompt(ctx, acp.PromptRequest{
			SessionId: sessResp.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("long task")},
		})
		if err != nil {
			promptErr <- err
			return
		}
		promptDone <- resp
	}()

	<-handlerStarted

	if err := a.Cancel(context.Background(), acp.CancelNotification{
		SessionId: sessResp.SessionId,
	}); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	select {
	case resp := <-promptDone:
		if resp.StopReason != acp.StopReasonCancelled {
			t.Fatalf("StopReason = %q, want cancelled", resp.StopReason)
		}
	case err := <-promptErr:
		t.Fatalf("Prompt() error = %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for cancel to propagate")
	}
}
