package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// stdioClient is a minimal MCP client for a server process speaking JSON-RPC
// 2.0 over stdio. It implements the subset needed for the cross-runtime plugin
// bridge: initialize, tools/list, tools/call, and (optionally) prompts/list.
//
// Wire format: one JSON-RPC 2.0 message per line (newline-delimited JSON),
// request/response correlated by integer id.
type stdioClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader

	mu       sync.Mutex
	nextID   int
	pending  map[int]chan json.RawMessage
	closed   bool
	protocol string
}

// mcpTool is the tools/list tool descriptor.
type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// mcpCallResult is the tools/call result envelope.
type mcpCallResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

const mcpProtocolVersion = "2024-11-05"

// startStdioClient spawns the server process and performs the initialize
// handshake. On any failure the process is killed and the error returned.
func startStdioClient(ctx context.Context, server ServerConfig) (*stdioClient, error) {
	if err := validateStdioServer(server); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, server.Command, server.Args...)
	cmd.Env = envForServer(server)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("mcp stdio stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("mcp stdio start %s: %w", server.Command, err)
	}
	client := &stdioClient{
		cmd:     cmd,
		stdin:   stdin,
		reader:  bufio.NewReader(stdout),
		pending: make(map[int]chan json.RawMessage),
	}
	go client.readLoop()

	protocol, err := client.initialize(ctx)
	if err != nil {
		_ = client.close()
		return nil, err
	}
	client.protocol = protocol
	return client, nil
}

func validateStdioServer(server ServerConfig) error {
	if server.Type != ServerTypeStdio {
		return fmt.Errorf("server %s is not a stdio server", server.Name)
	}
	if server.Command == "" {
		return fmt.Errorf("mcp server %s missing command", server.Name)
	}
	return nil
}

func envForServer(server ServerConfig) []string {
	out := make([]string, 0, len(server.Env))
	for key, value := range server.Env {
		out = append(out, key+"="+value)
	}
	return out
}

func (c *stdioClient) initialize(ctx context.Context) (string, error) {
	params := map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "godex",
			"version": "1.3.0",
		},
	}
	result, err := c.request(ctx, "initialize", params)
	if err != nil {
		return "", err
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(result, &init); err != nil {
		return "", fmt.Errorf("mcp initialize: %w", err)
	}
	// Notify the server that initialization is complete (fire-and-forget).
	_ = c.notify("notifications/initialized", map[string]any{})
	return init.ProtocolVersion, nil
}

func (c *stdioClient) listTools(ctx context.Context) ([]mcpTool, error) {
	result, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var list struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(result, &list); err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}
	return list.Tools, nil
}

// mcpPrompt is the prompts/list prompt descriptor.
type mcpPrompt struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
}

// mcpPromptMessage is one message in a prompts/get result.
type mcpPromptMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func (c *stdioClient) listPrompts(ctx context.Context) ([]mcpPrompt, error) {
	result, err := c.request(ctx, "prompts/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var list struct {
		Prompts []mcpPrompt `json:"prompts"`
	}
	if err := json.Unmarshal(result, &list); err != nil {
		return nil, fmt.Errorf("mcp prompts/list: %w", err)
	}
	return list.Prompts, nil
}

func (c *stdioClient) getPrompt(ctx context.Context, name string, arguments map[string]any) ([]mcpPromptMessage, error) {
	result, err := c.request(ctx, "prompts/get", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return nil, err
	}
	var got struct {
		Messages []mcpPromptMessage `json:"messages"`
	}
	if err := json.Unmarshal(result, &got); err != nil {
		return nil, fmt.Errorf("mcp prompts/get: %w", err)
	}
	return got.Messages, nil
}

func (c *stdioClient) callTool(ctx context.Context, name string, args map[string]any) (mcpCallResult, error) {
	result, err := c.request(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return mcpCallResult{}, err
	}
	var call mcpCallResult
	if err := json.Unmarshal(result, &call); err != nil {
		return mcpCallResult{}, fmt.Errorf("mcp tools/call: %w", err)
	}
	return call, nil
}

// request sends a JSON-RPC request and waits for the correlated response.
func (c *stdioClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp client closed")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	line, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("mcp write %s: %w", method, err)
	}

	select {
	case raw, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("mcp %s: connection closed", method)
		}
		var rpc struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &rpc); err != nil {
			return nil, fmt.Errorf("mcp %s: malformed response: %w", method, err)
		}
		if rpc.Error != nil {
			return nil, fmt.Errorf("mcp %s: %s (code %d)", method, rpc.Error.Message, rpc.Error.Code)
		}
		return rpc.Result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("mcp %s: %w", method, ctx.Err())
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("mcp %s: timeout", method)
	}
}

func (c *stdioClient) notify(method string, params any) error {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	line, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(line, '\n'))
	return err
}

func (c *stdioClient) readLoop() {
	scanner := bufio.NewScanner(c.reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		var id int
		if err := json.Unmarshal(msg.ID, &id); err != nil {
			continue // server-initiated notification, ignore
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		c.mu.Unlock()
		if !ok {
			continue
		}
		ch <- append(json.RawMessage{}, line...)
	}
	c.mu.Lock()
	closed := c.closed
	c.closed = true
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = make(map[int]chan json.RawMessage)
	c.mu.Unlock()
	if !closed {
		_ = c.cmd.Process.Kill()
	}
}

func (c *stdioClient) close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = make(map[int]chan json.RawMessage)
	c.mu.Unlock()
	_ = c.stdin.Close()
	done := make(chan struct{})
	go func() {
		_ = c.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
	return nil
}
