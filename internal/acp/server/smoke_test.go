package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type acpMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpErr         `json:"error,omitempty"`
}

type acpErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func send(enc *json.Encoder, id int, method string, params any) error {
	return enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
}

func readUntilResponse(scanner *bufio.Scanner, wantID int) (*acpMsg, error) {
	for scanner.Scan() {
		var msg acpMsg
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		// Skip notifications (method set, no id matching our request)
		if msg.Method != "" {
			continue
		}
		if msg.ID != nil {
			var id int
			if err := json.Unmarshal(msg.ID, &id); err == nil && id == wantID {
				return &msg, nil
			}
		}
	}
	return nil, io.ErrUnexpectedEOF
}

func TestACPSmokeEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ACP smoke test in short mode")
	}

	// Build the binary once.
	bin := t.TempDir() + "/godex-smoke"
	build := exec.Command("go", "build", "-o", bin, "./cmd/godex")
	build.Dir = findRepoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "acp-server")
	cmd.Dir = t.TempDir()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Drain stderr asynchronously.
	go func() {
		data, _ := io.ReadAll(stderr)
		if len(data) > 0 {
			t.Logf("stderr: %s", string(data))
		}
	}()

	enc := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Step 1: initialize
	if err := send(enc, 0, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo": map[string]string{
			"name": "acp-smoke-test",
		},
	}); err != nil {
		t.Fatalf("send initialize: %v", err)
	}
	initResp, err := readUntilResponse(scanner, 0)
	if err != nil {
		t.Fatalf("read initialize response: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("initialize error: %d %s", initResp.Error.Code, initResp.Error.Message)
	}

	var initResult struct {
		AgentInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"agentInfo"`
		AgentCapabilities struct {
			LoadSession        bool `json:"loadSession"`
			PromptCapabilities struct {
				EmbeddedContext bool `json:"embeddedContext"`
			} `json:"promptCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(initResp.Result, &initResult); err != nil {
		t.Fatalf("unmarshal initialize: %v", err)
	}
	if initResult.AgentInfo.Name != "godex" {
		t.Fatalf("agent name = %q, want godex", initResult.AgentInfo.Name)
	}
	if !initResult.AgentCapabilities.LoadSession {
		t.Fatal("expected LoadSession capability")
	}
	if !initResult.AgentCapabilities.PromptCapabilities.EmbeddedContext {
		t.Fatal("expected EmbeddedContext capability")
	}

	// Step 2: session/new
	if err := send(enc, 1, "session/new", map[string]any{
		"cwd":        t.TempDir(),
		"mcpServers": []any{},
	}); err != nil {
		t.Fatalf("send session/new: %v", err)
	}
	sessResp, err := readUntilResponse(scanner, 1)
	if err != nil {
		t.Fatalf("read session/new response: %v", err)
	}
	if sessResp.Error != nil {
		t.Fatalf("session/new error: %d %s", sessResp.Error.Code, sessResp.Error.Message)
	}

	var sessResult struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(sessResp.Result, &sessResult); err != nil {
		t.Fatalf("unmarshal session/new: %v", err)
	}
	if strings.TrimSpace(sessResult.SessionID) == "" {
		t.Fatal("empty sessionId from session/new")
	}

	// Step 3: session/prompt through the built-in command path. Keep the smoke
	// test independent from external model/provider availability.
	if err := send(enc, 2, "session/prompt", map[string]any{
		"sessionId": sessResult.SessionID,
		"prompt": []map[string]string{{
			"type": "text",
			"text": "/help",
		}},
	}); err != nil {
		t.Fatalf("send session/prompt: %v", err)
	}

	promptResp, err := readUntilResponse(scanner, 2)
	if err != nil {
		t.Fatalf("read session/prompt response: %v", err)
	}
	if promptResp.Error != nil {
		t.Fatalf("session/prompt error: %d %s", promptResp.Error.Code, promptResp.Error.Message)
	}

	var promptResult struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(promptResp.Result, &promptResult); err != nil {
		t.Fatalf("unmarshal session/prompt: %v", err)
	}
	if promptResult.StopReason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", promptResult.StopReason)
	}

	// Step 4: session/list
	if err := send(enc, 3, "session/list", map[string]any{}); err != nil {
		t.Fatalf("send session/list: %v", err)
	}
	listResp, err := readUntilResponse(scanner, 3)
	if err != nil {
		t.Fatalf("read session/list response: %v", err)
	}
	if listResp.Error != nil {
		t.Fatalf("session/list error: %d %s", listResp.Error.Code, listResp.Error.Message)
	}

	var listResult struct {
		Sessions []struct {
			SessionId string `json:"sessionId"`
			Cwd       string `json:"cwd"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(listResp.Result, &listResult); err != nil {
		t.Fatalf("unmarshal session/list: %v", err)
	}
	if len(listResult.Sessions) < 1 {
		t.Fatalf("session/list returned %d sessions, want at least 1 new session", len(listResult.Sessions))
	}

	// Step 5: session/load (LoadSession)
	if err := send(enc, 4, "session/load", map[string]any{
		"sessionId":  "smoke-loaded-session",
		"cwd":        "/tmp/loaded",
		"mcpServers": []any{},
	}); err != nil {
		t.Fatalf("send session/load: %v", err)
	}
	loadResp, err := readUntilResponse(scanner, 4)
	if err != nil {
		t.Fatalf("read session/load response: %v", err)
	}
	if loadResp.Error != nil {
		t.Fatalf("session/load error: %d %s", loadResp.Error.Code, loadResp.Error.Message)
	}

	// Step 6: session/list includes both newly-created and loaded sessions.
	if err := send(enc, 5, "session/list", map[string]any{}); err != nil {
		t.Fatalf("send post-load session/list: %v", err)
	}
	postLoadListResp, err := readUntilResponse(scanner, 5)
	if err != nil {
		t.Fatalf("read post-load session/list response: %v", err)
	}
	if postLoadListResp.Error != nil {
		t.Fatalf("post-load session/list error: %d %s", postLoadListResp.Error.Code, postLoadListResp.Error.Message)
	}
	var postLoadListResult struct {
		Sessions []struct {
			SessionId string `json:"sessionId"`
			Cwd       string `json:"cwd"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(postLoadListResp.Result, &postLoadListResult); err != nil {
		t.Fatalf("unmarshal post-load session/list: %v", err)
	}
	if len(postLoadListResult.Sessions) < 2 {
		t.Fatalf("post-load session/list returned %d sessions, want at least 2 (new + load)", len(postLoadListResult.Sessions))
	}

	t.Log("ACP smoke test passed: initialize → session/new → session/prompt → session/list → session/load")
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	// go test sets CWD to the package dir. Walk up to find go.mod.
	out, err := exec.Command("go", "env", "GOMOD").CombinedOutput()
	if err != nil {
		t.Fatalf("cannot find repo root: %v", err)
	}
	modFile := strings.TrimSpace(string(out))
	if modFile == "" || modFile == "/dev/null" {
		t.Fatal("GOMOD not set")
	}
	return strings.TrimSuffix(modFile, "/go.mod")
}
