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
		result, err := runACPAgent(ctx, agent, workspace, prompt, args.TimeoutSeconds)
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

func runACPAgent(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int) (acpRunResult, error) {
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
	promptResp, err := client.readResponse(2)
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
	for c.scanner.Scan() {
		var msg acpRPCMessage
		line := c.scanner.Bytes()
		if err := json.Unmarshal(line, &msg); err != nil {
			c.updates = append(c.updates, string(line))
			continue
		}
		if msg.Method == "session/update" {
			c.captureUpdate(msg.Params)
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
