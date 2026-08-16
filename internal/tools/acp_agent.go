package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
				"description": "Optional timeout override for this call",
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
		result, err := runACPAgent(ctx, agent, workspace, prompt, args.TimeoutSeconds, nil)
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
// mapping). Kind is one of "plan", "tool_call", "tool_call_update", or
// "message_chunk".
type ACPUpdate struct {
	Kind      string         `json:"kind"`
	Name      string         `json:"name,omitempty"`
	Text      string         `json:"text,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Raw       string         `json:"raw,omitempty"`
}

// UpdateEvents returns the structured session/update events captured during
// the run (empty when the engine sent none).
func (r ACPRunResult) UpdateEvents() []ACPUpdate {
	var out []ACPUpdate
	for _, raw := range r.Updates {
		update, ok := parseACPUpdate(raw)
		if ok {
			out = append(out, update)
		}
	}
	return out
}

// parseACPSessionUpdate extracts the inner `update` object from a raw
// session/update params payload and parses it into a structured ACPUpdate.
func parseACPSessionUpdate(raw json.RawMessage) (ACPUpdate, bool) {
	var payload struct {
		Update map[string]interface{} `json:"update"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ACPUpdate{}, false
	}
	data, err := json.Marshal(payload.Update)
	if err != nil {
		return ACPUpdate{}, false
	}
	return parseACPUpdate(string(data))
}

// parseACPUpdate turns one raw session/update payload into a structured event.
func parseACPUpdate(raw string) (ACPUpdate, bool) {
	var payload struct {
		SessionUpdate string                 `json:"sessionUpdate"`
		Name          string                 `json:"name"`
		Content       map[string]interface{} `json:"content"`
		Input         map[string]any         `json:"input"`
		ToolCallID    string                 `json:"toolCallId"`
		ID            string                 `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ACPUpdate{}, false
	}
	switch payload.SessionUpdate {
	case "agent_message_chunk":
		text := ""
		if content, ok := payload.Content["text"].(string); ok {
			text = content
		}
		return ACPUpdate{Kind: "message_chunk", Text: text, Raw: raw}, true
	case "plan":
		return ACPUpdate{Kind: "plan", Raw: raw}, true
	case "permission_request", "permission_denied":
		return ACPUpdate{Kind: payload.SessionUpdate, Raw: raw}, true
	case "tool_call", "tool_call_update":
		name := payload.Name
		if name == "" {
			name = payload.ToolCallID
		}
		if name == "" {
			name = payload.ID
		}
		kind := "tool_call"
		if payload.SessionUpdate == "tool_call_update" {
			kind = "tool_call_update"
		}
		return ACPUpdate{Kind: kind, Name: name, Input: payload.Input, Raw: raw}, true
	default:
		return ACPUpdate{}, false
	}
}

// RunACPAgent runs one prompt against a configured ACP agent over stdio and
// returns the collected reply text plus session metadata. It is the exported
// form of the acp_agent tool's internal runner so engines can delegate turns.
func RunACPAgent(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int) (ACPRunResult, error) {
	return runACPAgent(ctx, agent, workspace, prompt, timeoutSeconds, nil)
}

// StreamACPAgent runs one prompt against a configured ACP agent, invoking
// onUpdate for every session/update event as it streams in (阶段 C streaming
// handle). The returned result still carries the full reply text and the
// structured update list.
func StreamACPAgent(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int, onUpdate func(ACPUpdate)) (ACPRunResult, error) {
	return runACPAgent(ctx, agent, workspace, prompt, timeoutSeconds, onUpdate)
}

type acpRPCMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpRPCError    `json:"error,omitempty"`
}

type acpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func runACPAgent(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int, onUpdate func(ACPUpdate)) (acpRunResult, error) {
	if strings.TrimSpace(agent.Command) == "" {
		return acpRunResult{}, fmt.Errorf("ACP agent %q has no command", agent.ID)
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = agent.TimeoutSeconds
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 600
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
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
	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	client := acpClient{encoder: encoder, scanner: scanner}

	if err := client.send(0, "initialize", map[string]interface{}{
		"protocolVersion": 1,
		"clientCapabilities": map[string]interface{}{
			"fs": map[string]bool{
				"readTextFile":  false,
				"writeTextFile": false,
			},
			"terminal": false,
		},
		"clientInfo": map[string]string{
			"name":    "godex",
			"title":   "GoDex",
			"version": "dev",
		},
	}); err != nil {
		return acpRunResult{}, err
	}
	if _, err := client.readResponse(0); err != nil {
		_ = cmd.Process.Kill()
		return acpRunResult{}, err
	}
	cwd, _ := filepath.Abs(workspace)
	if err := client.send(1, "session/new", map[string]interface{}{"cwd": cwd, "mcpServers": []interface{}{}}); err != nil {
		return acpRunResult{}, err
	}
	newResp, err := client.readResponse(1)
	if err != nil {
		_ = cmd.Process.Kill()
		return acpRunResult{}, err
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(newResp.Result, &session)
	if strings.TrimSpace(session.SessionID) == "" {
		_ = cmd.Process.Kill()
		return acpRunResult{}, fmt.Errorf("ACP agent %q did not return a sessionId", agent.ID)
	}
	if err := client.send(2, "session/prompt", map[string]interface{}{
		"sessionId": session.SessionID,
		"prompt": []map[string]string{{
			"type": "text",
			"text": prompt,
		}},
	}); err != nil {
		return acpRunResult{}, err
	}
	promptResp, err := client.readResponseWithCallback(2, onUpdate)
	if err != nil {
		_ = cmd.Process.Kill()
		return acpRunResult{}, err
	}
	_ = stdin.Close()
	_ = cmd.Wait()
	stderrText := strings.TrimSpace(<-stderrData)
	if stderrText != "" && strings.TrimSpace(client.text.String()) == "" {
		client.updates = append(client.updates, "stderr: "+stderrText)
	}
	var stop struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(promptResp.Result, &stop)
	return acpRunResult{
		Agent:      agent.ID,
		SessionID:  session.SessionID,
		StopReason: stop.StopReason,
		Text:       strings.TrimSpace(client.text.String()),
		Updates:    append([]string{}, client.updates...),
	}, nil
}

type acpClient struct {
	encoder *json.Encoder
	scanner *bufio.Scanner
	text    strings.Builder
	updates []string
}

func (c *acpClient) send(id int, method string, params any) error {
	return c.encoder.Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
}

func (c *acpClient) readResponse(id int) (acpRPCMessage, error) {
	return c.readResponseWithCallback(id, nil)
}

// readResponseWithCallback reads the response for id, invoking onUpdate with
// each parsed session/update payload as it arrives (streaming handle, 阶段 C).
// The callback must not block the read loop for long.
func (c *acpClient) readResponseWithCallback(id int, onUpdate func(ACPUpdate)) (acpRPCMessage, error) {
	for c.scanner.Scan() {
		var msg acpRPCMessage
		line := c.scanner.Bytes()
		if err := json.Unmarshal(line, &msg); err != nil {
			c.updates = append(c.updates, string(line))
			continue
		}
		if msg.Method == "session/update" {
			c.captureUpdate(msg.Params)
			if onUpdate != nil {
				if update, ok := parseACPSessionUpdate(msg.Params); ok {
					onUpdate(update)
				}
			}
			continue
		}
		if msg.Method != "" && msg.ID != nil {
			_ = c.encoder.Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"error": map[string]interface{}{
					"code":    -32601,
					"message": "GoDex ACP bridge does not implement client method " + msg.Method,
				},
			})
			continue
		}
		if acpIDMatches(msg.ID, id) {
			if msg.Error != nil {
				return msg, fmt.Errorf("ACP error %d: %s", msg.Error.Code, msg.Error.Message)
			}
			return msg, nil
		}
	}
	if err := c.scanner.Err(); err != nil {
		return acpRPCMessage{}, err
	}
	return acpRPCMessage{}, io.ErrUnexpectedEOF
}

func (c *acpClient) captureUpdate(raw json.RawMessage) {
	var payload struct {
		Update map[string]interface{} `json:"update"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	updateType, _ := payload.Update["sessionUpdate"].(string)
	switch updateType {
	case "agent_message_chunk":
		if content, ok := payload.Update["content"].(map[string]interface{}); ok {
			if text, ok := content["text"].(string); ok {
				c.text.WriteString(text)
				data, _ := json.Marshal(payload.Update)
				if len(data) > 0 {
					c.updates = append(c.updates, string(data))
				}
			}
		}
	case "plan", "tool_call", "tool_call_update":
		data, _ := json.Marshal(payload.Update)
		if len(data) > 0 {
			c.updates = append(c.updates, string(data))
		}
	}
}

func acpIDMatches(raw any, want int) bool {
	switch v := raw.(type) {
	case float64:
		return int(v) == want
	case int:
		return v == want
	case string:
		return v == fmt.Sprintf("%d", want)
	default:
		return false
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
