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
	// persisted, when non-nil, is returned by ListSessions to simulate a
	// backend that persists ACP sessions across process restarts.
	persisted []acp.SessionInfo
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

// ListSessions lets the fake satisfy the optional sessionListFeatureProvider.
func (f *fakeSessionFeatures) ListSessions(context.Context) ([]acp.SessionInfo, error) {
	if f.persisted == nil {
		return nil, nil
	}
	return append([]acp.SessionInfo{}, f.persisted...), nil
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

func TestAgentForwardsMcpServersToPromptHandler(t *testing.T) {
	var got []acp.McpServer
	a := &Agent{
		Handler: func(_ context.Context, turn PromptTurn) (PromptResult, error) {
			got = append([]acp.McpServer{}, turn.McpServers...)
			return PromptResult{FinalText: "ok", StopReason: acp.StopReasonEndTurn}, nil
		},
	}
	servers := []acp.McpServer{{
		Stdio: &acp.McpServerStdio{Name: "tools", Command: "mcp-tools", Args: []string{"--serve"}, Env: []acp.EnvVariable{{Name: "K", Value: "V"}}},
	}}
	sessResp, err := a.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: servers})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := a.Prompt(context.Background(), acp.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("ping")},
	}); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if len(got) != 1 || got[0].Stdio == nil {
		t.Fatalf("handler did not receive mcpServers, got %+v", got)
	}
	if got[0].Stdio.Name != "tools" || got[0].Stdio.Command != "mcp-tools" {
		t.Fatalf("unexpected mcp server: %+v", got[0].Stdio)
	}
	// The turn must carry a copy, not the caller's slice (mutating one must
	// not corrupt session state).
	servers[0].Stdio.Args[0] = "--mutated"
	if a.sessions[string(sessResp.SessionId)].mcpServers[0].Stdio.Args[0] != "--serve" {
		t.Fatal("session state mutated through the request slice; expected deep copy")
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
}

func TestAgentSetSessionMode(t *testing.T) {
	a := &Agent{}
	sessResp, err := a.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if sessResp.Modes == nil {
		t.Fatal("expected Modes on NewSession response")
	}
	if len(sessResp.Modes.AvailableModes) != 2 {
		t.Fatalf("expected 2 available modes, got %+v", sessResp.Modes.AvailableModes)
	}
	if string(sessResp.Modes.CurrentModeId) != "default" {
		t.Fatalf("CurrentModeId = %q, want default", sessResp.Modes.CurrentModeId)
	}

	// Switch to minimal.
	if _, err := a.SetSessionMode(context.Background(), acp.SetSessionModeRequest{
		SessionId: sessResp.SessionId,
		ModeId:    acp.SessionModeId("minimal"),
	}); err != nil {
		t.Fatalf("SetSessionMode() error = %v", err)
	}
	if st := a.sessions[string(sessResp.SessionId)]; st == nil || st.mode != "minimal" {
		t.Fatalf("session mode = %+v, want minimal", a.sessions[string(sessResp.SessionId)])
	}

	// An unknown mode id is rejected.
	if _, err := a.SetSessionMode(context.Background(), acp.SetSessionModeRequest{
		SessionId: sessResp.SessionId,
		ModeId:    acp.SessionModeId("turbo"),
	}); err == nil {
		t.Fatal("expected error for unknown mode id")
	}

	// Resume reflects the switched mode.
	resumeResp, err := a.ResumeSession(context.Background(), acp.ResumeSessionRequest{
		SessionId: sessResp.SessionId,
		Cwd:       "/tmp",
	})
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if resumeResp.Modes == nil || string(resumeResp.Modes.CurrentModeId) != "minimal" {
		t.Fatalf("ResumeSession Modes = %+v, want current=minimal", resumeResp.Modes)
	}
}

func TestAgentListSessionsMergesPersistedSessions(t *testing.T) {
	features := &fakeSessionFeatures{
		view: backend.ModelsView{DefaultProfileID: "sonnet"},
		persisted: []acp.SessionInfo{
			{SessionId: "sess-persisted-1", Cwd: "/old/workspace"},
			{SessionId: "sess-persisted-2", Cwd: "/other"},
		},
	}
	a := &Agent{Features: features}
	// One in-memory session colliding with a persisted id must win with its
	// live cwd; the other persisted id is merged in.
	sessResp, err := a.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/live/workspace", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	// Re-bind the persisted id through LoadSession so it lands in memory too.
	if _, err := a.LoadSession(context.Background(), acp.LoadSessionRequest{SessionId: acp.SessionId("sess-persisted-1"), Cwd: "/live/workspace"}); err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	listResp, err := a.ListSessions(context.Background(), acp.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	byID := map[string]acp.SessionInfo{}
	for _, info := range listResp.Sessions {
		byID[string(info.SessionId)] = info
	}
	if len(byID) != 3 {
		t.Fatalf("expected 3 merged sessions, got %+v", listResp.Sessions)
	}
	if byID["sess-persisted-1"].Cwd != "/live/workspace" {
		t.Fatalf("in-memory session should win: %+v", byID["sess-persisted-1"])
	}
	if byID["sess-persisted-2"].Cwd != "/other" {
		t.Fatalf("persisted session should be merged: %+v", byID["sess-persisted-2"])
	}
	if _, ok := byID[string(sessResp.SessionId)]; !ok {
		t.Fatalf("fresh in-memory session missing from list: %+v", listResp.Sessions)
	}
}

func TestAgentNewSessionAndLoadSessionReturnModelConfigOptions(t *testing.T) {
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
	options := modelConfigOptionValues(newResp.ConfigOptions)
	if len(options) != 2 {
		t.Fatalf("expected two model config options, got %+v", newResp.ConfigOptions)
	}
	if newResp.ConfigOptions[0].Select == nil || string(newResp.ConfigOptions[0].Select.CurrentValue) != "sonnet" {
		t.Fatalf("unexpected new session model options: %+v", newResp.ConfigOptions[0])
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
	options = modelConfigOptionValues(loadResp.ConfigOptions)
	if len(options) != 2 {
		t.Fatalf("expected two loaded model config options, got %+v", loadResp.ConfigOptions)
	}
	if loadResp.ConfigOptions[0].Select == nil || string(loadResp.ConfigOptions[0].Select.CurrentValue) != "mini" {
		t.Fatalf("unexpected loaded session model options: %+v", loadResp.ConfigOptions[0])
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

	if _, err := a.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: sessResp.SessionId,
			ConfigId:  acp.SessionConfigId("model_profile"),
			Value:     acp.SessionConfigValueId("mini"),
		},
	}); err != nil {
		t.Fatalf("SetSessionConfigOption() error = %v", err)
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

func modelConfigOptionValues(options []acp.SessionConfigOption) []string {
	var out []string
	for _, opt := range options {
		if opt.Select == nil || opt.Select.Options.Ungrouped == nil {
			continue
		}
		for _, item := range *opt.Select.Options.Ungrouped {
			out = append(out, string(item.Value))
		}
	}
	return out
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
