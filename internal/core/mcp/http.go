package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpClient is a minimal MCP client for a remote server speaking JSON-RPC 2.0
// over the Streamable HTTP transport (MCP spec 2025-06-18): every request is a
// single POST carrying one JSON-RPC message. The response is either
// application/json or a text/event-stream, from which the terminal message is
// extracted. It is the transport business systems use to register tools with
// the Agent Step Platform (Phase A).
//
// Like stdio, each list/call opens a fresh stateless client — no persistent
// connection and no session state (see ServerConfig.SessionRequired; not yet
// implemented, see httpClient below).
type httpClient struct {
	server ServerConfig
	http   *http.Client
}

// retryableError marks network/5xx failures worth retrying with backoff;
// 4xx and parse errors are not wrapped and are never retried.
type retryableError struct {
	err error
}

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

// startHTTPClient validates the server config and returns a stateless client.
func startHTTPClient(server ServerConfig) (*httpClient, error) {
	if server.Type != ServerTypeHTTP {
		return nil, fmt.Errorf("server %s is not a streamable-http server", server.Name)
	}
	if strings.TrimSpace(server.URL) == "" {
		return nil, fmt.Errorf("mcp server %s missing url", server.Name)
	}
	return &httpClient{
		server: server,
		// Single-request timeout: the Agent Step design caps one tools/call
		// at 10s so a hanging upstream never blocks the business request.
		http: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// close is a no-op for the stateless HTTP client; it exists so both transports
// satisfy the common rpcClient interface used by Manager.
func (c *httpClient) close() error { return nil }

// initialize performs the MCP initialize handshake and returns the negotiated
// protocol version.
func (c *httpClient) initialize(ctx context.Context) (string, error) {
	result, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "godex",
			"version": "1.4.0",
		},
	})
	if err != nil {
		return "", err
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(result, &init); err != nil {
		return "", fmt.Errorf("mcp initialize: %w", err)
	}
	return init.ProtocolVersion, nil
}

// listTools lists the tools exposed by the remote server.
func (c *httpClient) listTools(ctx context.Context) ([]mcpTool, error) {
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

// callTool invokes one tool on the remote server.
func (c *httpClient) callTool(ctx context.Context, name string, args map[string]any) (mcpCallResult, error) {
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

// listPrompts lists the prompts exposed by the remote server.
func (c *httpClient) listPrompts(ctx context.Context) ([]mcpPrompt, error) {
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

// getPrompt renders one prompt on the remote server.
func (c *httpClient) getPrompt(ctx context.Context, name string, arguments map[string]any) ([]mcpPromptMessage, error) {
	result, err := c.request(ctx, "prompts/get", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return nil, err
	}
	var call struct {
		Messages []mcpPromptMessage `json:"messages"`
	}
	if err := json.Unmarshal(result, &call); err != nil {
		return nil, fmt.Errorf("mcp prompts/get: %w", err)
	}
	return call.Messages, nil
}

// request sends one JSON-RPC request and returns the result field, retrying
// transient network/5xx failures with exponential backoff (1s, 2s). 4xx and
// malformed responses are returned immediately and never retried.
func (c *httpClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
		result, err := c.post(ctx, body)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if _, ok := err.(*retryableError); !ok {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

// post performs a single Streamable HTTP POST and decodes the terminal result.
func (c *httpClient) post(ctx context.Context, body []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.server.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	for k, v := range c.server.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &retryableError{err: fmt.Errorf("mcp %s post: %w", c.server.Name, err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return nil, &retryableError{err: fmt.Errorf("mcp %s: upstream status %d", c.server.Name, resp.StatusCode)}
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("mcp %s: upstream status %d", c.server.Name, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &retryableError{err: fmt.Errorf("mcp %s read: %w", c.server.Name, err)}
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return parseSSEMessage(data)
	}
	return parseRPCResult(data)
}

// parseRPCResult decodes a JSON-RPC 2.0 response envelope and returns result.
func parseRPCResult(data []byte) (json.RawMessage, error) {
	var rpc struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &rpc); err != nil {
		return nil, fmt.Errorf("mcp malformed response: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("mcp: %s (code %d)", rpc.Error.Message, rpc.Error.Code)
	}
	return rpc.Result, nil
}

// parseSSEMessage extracts the terminal JSON-RPC message from a Streamable
// HTTP SSE response. Streamable HTTP uses a single `message` event whose
// (possibly multi-line) data field holds the JSON-RPC envelope.
func parseSSEMessage(data []byte) (json.RawMessage, error) {
	inMessage := false
	var payload []byte
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "event: message"):
			inMessage = true
		case strings.HasPrefix(line, "event:"):
			inMessage = false
		case strings.HasPrefix(line, "data:") && inMessage:
			payload = append(payload, strings.TrimPrefix(line, "data:")...)
		}
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("mcp: empty SSE response")
	}
	return parseRPCResult(payload)
}
