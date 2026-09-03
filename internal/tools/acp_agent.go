package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/tim5wang/godex/internal/core/config"
)

type acpAgentArgs struct {
	Action         string `json:"action,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Prompt         string `json:"prompt,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// NewACPAgentTool creates a small ACP stdio client for configured external agents.
func NewACPAgentTool(agents map[string]config.ACPAgentConfig, workspace string) Tool {
	runtime := cloneACPAgents(agents)
	return NewTypedTool(NewToolSpec("acp_agent", "Call an external Agent Client Protocol agent over stdio. Use action=list to inspect configured agents, or action=run with agent and prompt.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{"list", "run"},
			},
			"agent": map[string]string{
				"type":        "string",
				"description": "Configured ACP agent id",
			},
			"prompt": map[string]string{
				"type":        "string",
				"description": "Prompt to send with session/prompt",
			},
			"timeout_seconds": map[string]string{
				"type":        "integer",
				"description": "Optional idle-timeout override for this call (seconds without any agent output)",
			},
		},
	}, nil), func(ctx context.Context, args acpAgentArgs) (ToolResult, error) {
		action := strings.TrimSpace(args.Action)
		if action == "" {
			action = "run"
		}
		switch action {
		case "list":
			return ToolResult{Structured: formatACPAgents(runtime)}, nil
		case "run":
		default:
			return ToolResult{}, fmt.Errorf("unsupported acp_agent action %q", action)
		}
		id := strings.TrimSpace(args.Agent)
		if id == "" {
			return ToolResult{}, fmt.Errorf("missing agent argument")
		}
		agent, ok := runtime[id]
		if !ok {
			return ToolResult{}, fmt.Errorf("unknown ACP agent %q", id)
		}
		prompt := strings.TrimSpace(args.Prompt)
		if prompt == "" {
			return ToolResult{}, fmt.Errorf("missing prompt argument")
		}
		result, err := runACPAgent(ctx, agent, workspace, prompt, args.TimeoutSeconds, nil, "")
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{
			Text:       result.Text,
			Structured: result,
			Metadata: map[string]interface{}{
				"agent":       id,
				"stop_reason": result.StopReason,
			},
		}, nil
	})
}

type acpRunResult struct {
	Agent      string   `json:"agent"`
	SessionID  string   `json:"session_id,omitempty"`
	StopReason string   `json:"stop_reason,omitempty"`
	Text       string   `json:"text,omitempty"`
	Updates    []string `json:"updates,omitempty"`
}

// ACPRunResult is the exported result of one external ACP agent run, reused by
// the ACP harness (阶段 C: Pi/其他 ACP agent 的 Harness adapter).
type ACPRunResult = acpRunResult

// ACPUpdate is one structured session/update event captured from the external
// engine, mapped onto GoDex events by the harness (P2 #4 unified event
// mapping). Kind is one of "plan", "tool_call", "tool_call_update",
// "thought_chunk", or "message_chunk".
type ACPUpdate struct {
	Kind       string         `json:"kind"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"toolCallId,omitempty"`
	Text       string         `json:"text,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	Raw        string         `json:"raw,omitempty"`
}

// RunACPAgent runs one prompt against a configured ACP agent over stdio and
// returns the collected reply text plus session metadata. It is the exported
// form of the acp_agent tool's internal runner so engines can delegate turns.
func RunACPAgent(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int) (ACPRunResult, error) {
	return runACPAgent(ctx, agent, workspace, prompt, timeoutSeconds, nil, "")
}

// StreamACPAgent runs one prompt against a configured ACP agent, invoking
// onUpdate for every session/update event as it streams in (阶段 C streaming
// handle). The returned result still carries the full reply text and the
// structured update list.
func StreamACPAgent(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int, onUpdate func(ACPUpdate)) (ACPRunResult, error) {
	return runACPAgent(ctx, agent, workspace, prompt, timeoutSeconds, onUpdate, "")
}

// StreamACPAgentModel runs one prompt against a configured ACP agent with an
// optional model override applied through the standard session config
// mechanism (SetSessionConfigOption, config id "model").
func StreamACPAgentModel(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int, model string, onUpdate func(ACPUpdate)) (ACPRunResult, error) {
	return runACPAgent(ctx, agent, workspace, prompt, timeoutSeconds, onUpdate, model)
}

// acpSDKClient implements the acp.Client interface for the SDK's
// ClientSideConnection. It forwards every standard SessionUpdate event to the
// godex onUpdate callback (streaming) and accumulates the reply text.
type acpSDKClient struct {
	onUpdate func(ACPUpdate)

	mu      sync.Mutex
	text    strings.Builder
	updates []string
}

var _ acp.Client = (*acpSDKClient)(nil)

func (c *acpSDKClient) addUpdate(u ACPUpdate) {
	c.mu.Lock()
	if u.Kind == "message_chunk" && u.Text != "" {
		c.text.WriteString(u.Text)
	}
	if u.Raw != "" {
		c.updates = append(c.updates, u.Raw)
	}
	c.mu.Unlock()
	if c.onUpdate != nil {
		c.onUpdate(u)
	}
}

// SessionUpdate is the SDK callback for every session/update notification the
// agent sends while processing a prompt.
func (c *acpSDKClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	u := params.Update
	raw, _ := json.Marshal(u)
	rawStr := string(raw)
	switch {
	case u.AgentMessageChunk != nil:
		text := ""
		if t := u.AgentMessageChunk.Content.Text; t != nil {
			text = t.Text
		}
		c.addUpdate(ACPUpdate{Kind: "message_chunk", Text: text, Raw: rawStr})
	case u.AgentThoughtChunk != nil:
		text := ""
		if t := u.AgentThoughtChunk.Content.Text; t != nil {
			text = t.Text
		}
		c.addUpdate(ACPUpdate{Kind: "thought_chunk", Text: text, Raw: rawStr})
	case u.ToolCall != nil:
		c.addUpdate(ACPUpdate{
			Kind:       "tool_call",
			Name:       u.ToolCall.Title,
			ToolCallID: string(u.ToolCall.ToolCallId),
			Input:      rawToMap(u.ToolCall.RawInput),
			Raw:        rawStr,
		})
	case u.ToolCallUpdate != nil:
		// Prefer the update's title (the agent may refresh it as the call
		// progresses); fall back to the call id so the event still has a
		// stable identifier when no title is sent.
		name := ""
		if u.ToolCallUpdate.Title != nil {
			name = *u.ToolCallUpdate.Title
		}
		if name == "" {
			name = string(u.ToolCallUpdate.ToolCallId)
		}
		c.addUpdate(ACPUpdate{
			Kind:       "tool_call_update",
			Name:       name,
			ToolCallID: string(u.ToolCallUpdate.ToolCallId),
			Input:      rawToMap(u.ToolCallUpdate.RawOutput),
			Raw:        rawStr,
		})
	case u.Plan != nil:
		c.addUpdate(ACPUpdate{Kind: "plan", Raw: rawStr})
	}
	return nil
}

func (c *acpSDKClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, fmt.Errorf("godex ACP client does not implement session/request_permission")
}

func (c *acpSDKClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, fmt.Errorf("godex ACP client does not implement fs/read_text_file")
}

func (c *acpSDKClient) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, fmt.Errorf("godex ACP client does not implement fs/write_text_file")
}

func (c *acpSDKClient) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, fmt.Errorf("godex ACP client does not implement terminal/create")
}

func (c *acpSDKClient) KillTerminal(ctx context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, fmt.Errorf("godex ACP client does not implement terminal/kill")
}

func (c *acpSDKClient) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, fmt.Errorf("godex ACP client does not implement terminal/output")
}

func (c *acpSDKClient) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, fmt.Errorf("godex ACP client does not implement terminal/release")
}

func (c *acpSDKClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, fmt.Errorf("godex ACP client does not implement terminal/wait_for_exit")
}

// rawToMap converts an arbitrary JSON value into a map for tool-call payloads.
// Object values pass through; string values are parsed as JSON (some agents
// serialize rawInput/rawOutput as a JSON string); anything else is nil (empty
// input).
func rawToMap(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(t), &m) == nil {
			return m
		}
	}
	return nil
}

// ACPModelOption is one selectable model value advertised by an ACP agent's
// session config (configOptions, id "model").
type ACPModelOption struct {
	Value string `json:"value"`
	Name  string `json:"name"`
}

// DiscoverACPAgentModelOptions connects to a configured ACP agent, creates a
// throwaway session, reads the agent's configOptions (the standard mechanism
// agents use to advertise selectable models) and returns the model list. The
// process is torn down before returning.
func DiscoverACPAgentModelOptions(ctx context.Context, agent config.ACPAgentConfig, workspace string) ([]ACPModelOption, error) {
	if strings.TrimSpace(agent.Command) == "" {
		return nil, fmt.Errorf("ACP agent %q has no command", agent.ID)
	}
	// ACP agents (dsh included) reject relative cwd on session/new; make sure
	// the workspace we hand to the subprocess is absolute.
	if !filepath.IsAbs(workspace) {
		abs, err := filepath.Abs(workspace)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace %q: %w", workspace, err)
		}
		workspace = abs
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, agent.Command, agent.Args...)
	cmd.Dir = workspace
	cmd.Env = os.Environ()
	for key, value := range agent.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	stderrData := make(chan string, 1)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrData <- string(data)
	}()

	client := &acpSDKClient{}
	conn := acp.NewClientSideConnection(client, stdin, stdout)

	title := "GoDex"
	if _, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs:       acp.FileSystemCapabilities{},
			Terminal: false,
		},
		ClientInfo: &acp.Implementation{Name: "godex", Title: &title},
	}); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("acp initialize: %w", err)
	}
	newSess, err := conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        workspace,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("acp session/new: %w", err)
	}
	// Tear down the throwaway process: closing stdin signals EOF, and the
	// explicit cancel guarantees the process is killed even if it never
	// exits on EOF (a misbehaving agent must not hang the HTTP endpoint).
	_ = stdin.Close()
	cancel()
	_ = cmd.Wait()
	_ = <-stderrData

	var out []ACPModelOption
	for _, opt := range newSess.ConfigOptions {
		sel := opt.Select
		if sel == nil || sel.Id != "model" {
			continue
		}
		// dsh (and other agents) may advertise grouped options:
		// [{group, name, options:[{value,name,description}]}], or a flat
		// ungrouped list. Handle both so the settings dropdown populates.
		if sel.Options.Ungrouped != nil {
			for _, option := range *sel.Options.Ungrouped {
				name := option.Name
				if name == "" {
					name = string(option.Value)
				}
				out = append(out, ACPModelOption{Value: string(option.Value), Name: name})
			}
		}
		if sel.Options.Grouped != nil {
			for _, group := range *sel.Options.Grouped {
				for _, option := range group.Options {
					name := option.Name
					if name == "" {
						name = string(option.Value)
					}
					out = append(out, ACPModelOption{Value: string(option.Value), Name: name})
				}
			}
		}
	}
	return out, nil
}

func runACPAgent(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int, onUpdate func(ACPUpdate), model string) (acpRunResult, error) {
	if strings.TrimSpace(agent.Command) == "" {
		return acpRunResult{}, fmt.Errorf("ACP agent %q has no command", agent.ID)
	}
	// ACP agents (dsh included) reject relative cwd on session/new; make sure
	// the workspace we hand to the subprocess and session is absolute.
	if !filepath.IsAbs(workspace) {
		abs, err := filepath.Abs(workspace)
		if err != nil {
			return acpRunResult{}, fmt.Errorf("resolve workspace %q: %w", workspace, err)
		}
		workspace = abs
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = agent.TimeoutSeconds
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 600
	}
	// timeout_seconds is the idle timeout: the turn is aborted only when the
	// agent produces no output for that long. Active streaming resets the
	// timer (standard ACP semantics instead of a total-process cap).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, agent.Command, agent.Args...)
	cmd.Dir = workspace
	cmd.Env = os.Environ()
	for key, value := range agent.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return acpRunResult{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return acpRunResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return acpRunResult{}, err
	}
	stderrData := make(chan string, 1)
	if err := cmd.Start(); err != nil {
		return acpRunResult{}, err
	}
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrData <- string(data)
	}()

	client := &acpSDKClient{onUpdate: onUpdate}
	conn := acp.NewClientSideConnection(client, stdin, stdout)

	// Idle watchdog: reset on every session/update event (via lastActivity),
	// abort the turn when the agent stays silent for timeout_seconds.
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, lastActivity.Load())) > time.Duration(timeoutSeconds)*time.Second {
					cancel()
					return
				}
			}
		}
	}()
	origOnUpdate := client.onUpdate
	client.onUpdate = func(u ACPUpdate) {
		lastActivity.Store(time.Now().UnixNano())
		if origOnUpdate != nil {
			origOnUpdate(u)
		}
	}

	title := "GoDex"
	initResp, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapabilities{
				ReadTextFile:  false,
				WriteTextFile: false,
			},
			Terminal: false,
		},
		ClientInfo: &acp.Implementation{
			Name:  "godex",
			Title: &title,
		},
	})
	if err != nil {
		_ = cmd.Process.Kill()
		return acpRunResult{}, fmt.Errorf("acp initialize: %w", err)
	}
	_ = initResp

	newSess, err := conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        workspace,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		_ = cmd.Process.Kill()
		return acpRunResult{}, fmt.Errorf("acp session/new: %w", err)
	}

	// Standard model selection: apply the override through the session config
	// option the agent advertised (config id "model").
	if model != "" {
		if _, err := conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{
				SessionId: newSess.SessionId,
				ConfigId:  "model",
				Value:     acp.SessionConfigValueId(model),
			},
		}); err != nil {
			_ = cmd.Process.Kill()
			return acpRunResult{}, fmt.Errorf("acp set model option %q: %w", model, err)
		}
	}

	promptResp, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: newSess.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock(prompt)},
	})
	if err != nil {
		_ = cmd.Process.Kill()
		return acpRunResult{}, fmt.Errorf("acp session/prompt: %w", err)
	}

	// Normal completion: stop the idle watchdog and tear down the process.
	// stdin EOF signals a graceful exit; the explicit cancel is the fallback
	// that kills the process via exec.CommandContext, so Wait cannot hang
	// even if the agent ignores the EOF (worst case today is blocking for
	// the full watchdog timeout).
	_ = stdin.Close()
	cancel()
	_ = cmd.Wait()
	<-watchdogDone
	stderrText := strings.TrimSpace(<-stderrData)

	client.mu.Lock()
	text := client.text.String()
	updates := append([]string{}, client.updates...)
	client.mu.Unlock()

	if stderrText != "" && strings.TrimSpace(text) == "" {
		updates = append(updates, "stderr: "+stderrText)
	}

	return acpRunResult{
		Agent:      agent.ID,
		SessionID:  string(newSess.SessionId),
		StopReason: string(promptResp.StopReason),
		Text:       strings.TrimSpace(text),
		Updates:    updates,
	}, nil
}

// ACPSession is a persistent stdio connection to one ACP agent process with a
// live session id, so consecutive prompts continue the same external
// conversation instead of opening a fresh session per turn. When the
// underlying process dies (e.g. after a network drop), the caller can reopen
// with the recorded session id and the agent's session/load (or the unstable
// session/resume) restores the original conversation.
type ACPSession struct {
	agent     config.ACPAgentConfig
	workspace string
	model     string
	timeout   int

	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	conn       *acp.ClientSideConnection
	client     *acpSDKClient
	sessionID  acp.SessionId
	closed     bool
	stderr     strings.Builder
	procCancel context.CancelFunc // cancels the process lifetime context
}

// OpenACPSession spawns the agent process, initializes it, and creates a
// session. When resumeSessionID is non-empty the agent's session/load is tried
// first, then the unstable session/resume, falling back to a fresh session/new.
func OpenACPSession(ctx context.Context, agent config.ACPAgentConfig, workspace, model string, timeoutSeconds int, resumeSessionID string) (*ACPSession, error) {
	if strings.TrimSpace(agent.Command) == "" {
		return nil, fmt.Errorf("ACP agent %q has no command", agent.ID)
	}
	// ACP agents (dsh included) reject relative cwd on session/new; make sure
	// the workspace we hand to the subprocess and session is absolute.
	if !filepath.IsAbs(workspace) {
		abs, err := filepath.Abs(workspace)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace %q: %w", workspace, err)
		}
		workspace = abs
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = agent.TimeoutSeconds
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 600
	}
	procCtx, cancelProc := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, agent.Command, agent.Args...)
	cmd.Dir = workspace
	cmd.Env = os.Environ()
	for key, value := range agent.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancelProc()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancelProc()
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancelProc()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancelProc()
		return nil, err
	}
	s := &ACPSession{
		agent:      agent,
		workspace:  workspace,
		model:      model,
		timeout:    timeoutSeconds,
		cmd:        cmd,
		stdin:      stdin,
		client:     &acpSDKClient{},
		procCancel: cancelProc,
	}
	go func() {
		data, _ := io.ReadAll(stderrPipe)
		s.mu.Lock()
		s.stderr.Write(data)
		s.mu.Unlock()
	}()
	s.conn = acp.NewClientSideConnection(s.client, stdin, stdout)

	title := "GoDex"
	if _, err := s.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs:       acp.FileSystemCapabilities{},
			Terminal: false,
		},
		ClientInfo: &acp.Implementation{Name: "godex", Title: &title},
	}); err != nil {
		s.killProcess(cancelProc)
		return nil, fmt.Errorf("acp initialize: %w", err)
	}
	openSession := func() (acp.SessionId, error) {
		newSess, err := s.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: workspace, McpServers: []acp.McpServer{}})
		if err != nil {
			return "", err
		}
		return newSess.SessionId, nil
	}
	if resumeSessionID != "" {
		if _, err := s.conn.LoadSession(ctx, acp.LoadSessionRequest{SessionId: acp.SessionId(resumeSessionID), Cwd: workspace, McpServers: []acp.McpServer{}}); err == nil {
			s.sessionID = acp.SessionId(resumeSessionID)
		} else if _, err := s.conn.UnstableResumeSession(ctx, acp.UnstableResumeSessionRequest{SessionId: acp.SessionId(resumeSessionID), Cwd: workspace, McpServers: []acp.McpServer{}}); err == nil {
			s.sessionID = acp.SessionId(resumeSessionID)
		} else {
			id, err := openSession()
			if err != nil {
				s.killProcess(cancelProc)
				return nil, fmt.Errorf("acp session/new: %w", err)
			}
			s.sessionID = id
		}
	} else {
		id, err := openSession()
		if err != nil {
			s.killProcess(cancelProc)
			return nil, fmt.Errorf("acp session/new: %w", err)
		}
		s.sessionID = id
	}
	if model != "" {
		if _, err := s.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{
				SessionId: s.sessionID,
				ConfigId:  "model",
				Value:     acp.SessionConfigValueId(model),
			},
		}); err != nil {
			s.killProcess(cancelProc)
			return nil, fmt.Errorf("acp set model option %q: %w", model, err)
		}
	}
	return s, nil
}

// SessionID returns the live session id (empty until the session is open).
func (s *ACPSession) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.sessionID)
}

// Done closes when the peer (agent process) disconnects. The session becomes
// unusable afterwards; callers reopen with SessionID to resume the conversation.
func (s *ACPSession) Done() <-chan struct{} { return s.conn.Done() }

// Prompt sends one prompt on the live session and streams updates. The idle
// watchdog aborts the prompt when the agent stays silent for timeout_seconds;
// the process and session are kept alive across prompts.
func (s *ACPSession) Prompt(ctx context.Context, prompt string, onUpdate func(ACPUpdate)) (acpRunResult, error) {
	s.mu.Lock()
	if s.closed || s.conn == nil {
		s.mu.Unlock()
		return acpRunResult{}, fmt.Errorf("ACP session is closed")
	}
	conn := s.conn
	client := s.client
	sessionID := s.sessionID
	s.mu.Unlock()

	promptCtx, cancelPrompt := context.WithCancel(ctx)
	defer cancelPrompt()
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-promptCtx.Done():
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, lastActivity.Load())) > time.Duration(s.timeout)*time.Second {
					cancelPrompt()
					return
				}
			}
		}
	}()
	client.mu.Lock()
	client.text.Reset()
	client.updates = nil
	client.onUpdate = func(u ACPUpdate) {
		lastActivity.Store(time.Now().UnixNano())
		if onUpdate != nil {
			onUpdate(u)
		}
	}
	client.mu.Unlock()
	defer func() {
		client.mu.Lock()
		client.onUpdate = nil
		client.mu.Unlock()
	}()

	promptResp, err := conn.Prompt(promptCtx, acp.PromptRequest{SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock(prompt)}})
	// Stop the idle watchdog before waiting for it to wind down (it only exits
	// when promptCtx is cancelled; the deferred cancel runs at return, which
	// would deadlock the wait below).
	cancelPrompt()
	<-watchdogDone
	if err != nil {
		// Surface any stderr the agent produced while failing so the caller
		// can diagnose crashes / init errors without extra plumbing.
		s.mu.Lock()
		stderrText := strings.TrimSpace(s.stderr.String())
		s.mu.Unlock()
		if stderrText != "" {
			return acpRunResult{}, fmt.Errorf("acp session/prompt: %w (stderr: %s)", err, stderrText)
		}
		return acpRunResult{}, fmt.Errorf("acp session/prompt: %w", err)
	}
	client.mu.Lock()
	text := client.text.String()
	updates := append([]string{}, client.updates...)
	client.mu.Unlock()
	return acpRunResult{
		Agent:      s.agent.ID,
		SessionID:  string(sessionID),
		StopReason: string(promptResp.StopReason),
		Text:       strings.TrimSpace(text),
		Updates:    updates,
	}, nil
}

// Close kills the agent process and releases the stdio pipes.
func (s *ACPSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cmd := s.cmd
	stdin := s.stdin
	cancel := s.procCancel
	s.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return nil
}

func (s *ACPSession) killProcess(cancelProc context.CancelFunc) {
	cancelProc()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}

func cloneACPAgents(agents map[string]config.ACPAgentConfig) map[string]config.ACPAgentConfig {
	out := make(map[string]config.ACPAgentConfig, len(agents))
	for id, agent := range agents {
		if len(agent.Args) > 0 {
			agent.Args = append([]string{}, agent.Args...)
		}
		if len(agent.Env) > 0 {
			env := make(map[string]string, len(agent.Env))
			for key, value := range agent.Env {
				env[key] = value
			}
			agent.Env = env
		}
		out[id] = agent
	}
	return out
}

func formatACPAgents(agents map[string]config.ACPAgentConfig) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(agents))
	for id, agent := range agents {
		out = append(out, map[string]interface{}{
			"id":              id,
			"command":         agent.Command,
			"args":            append([]string{}, agent.Args...),
			"description":     agent.Description,
			"timeout_seconds": agent.TimeoutSeconds,
		})
	}
	return out
}
