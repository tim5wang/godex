package tooling

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

// RelayClient executes shell commands / file operations on a REMOTE godex node
// (sandbox node B) by forwarding HTTP requests through the center bridge:
//
//	POST {CenterURL}/api/control/nodes/{NodeID}/proxy/control/sandbox/exec
//
// The center validates the restricted nk_ credential (Bearer) and relays the
// request over the node's outbound WSS channel to B's local httpapi. This is
// the RemoteSandbox execution path (docs/remote-sandbox-design.md M1); it
// reuses the existing center-bridge proxy tunnel, so no new network layer is
// added.
type RelayClient struct {
	CenterURL string
	NodeID    string
	Token     string // restricted nk_ credential (control.center_token)

	HTTP *http.Client
}

func (c *RelayClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (c *RelayClient) base() string {
	base := strings.TrimRight(strings.TrimSpace(c.CenterURL), "/")
	if strings.HasSuffix(base, "/api") {
		base = strings.TrimSuffix(base, "/api")
	}
	return base
}

// ExecRequest is the payload B's /control/sandbox/exec endpoint accepts.
type ExecRequest struct {
	Command          string `json:"command"`
	Workspace        string `json:"workspace,omitempty"`
	TimeoutSeconds   int    `json:"timeout_seconds,omitempty"`
	AllowUnlisted    bool   `json:"allow_unlisted,omitempty"`
}

// FSRequest mirrors the RemoteFS method surface over JSON; Data is base64 for
// read/write payloads.
type FSRequest struct {
	Op        string `json:"op"` // read|write|readdir|stat|mkdir|rm|rename
	Path      string `json:"path"`
	Data      string `json:"data,omitempty"`
	Mode      uint32 `json:"mode,omitempty"`
	NewPath   string `json:"newpath,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// FSResult is the decoded response from B's /control/sandbox/fs.
type FSResult struct {
	Data    string      `json:"data,omitempty"`
	Entries []FSDirItem `json:"entries,omitempty"`
	Info    *FSFileInfo `json:"info,omitempty"`
	Removed bool        `json:"removed,omitempty"`
}

type FSDirItem struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"`
}

type FSFileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModeStr string `json:"mode"`
}

// FS performs one file operation on the remote sandbox node.
func (c *RelayClient) FS(ctx context.Context, req FSRequest) (FSResult, error) {
	if strings.TrimSpace(c.NodeID) == "" || strings.TrimSpace(c.CenterURL) == "" {
		return FSResult{}, fmt.Errorf("relay execution backend requires relay center and node id")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return FSResult{}, err
	}
	url := c.base() + "/api/control/nodes/" + c.NodeID + "/proxy/control/sandbox/fs"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return FSResult{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(hreq)
	if err != nil {
		return FSResult{}, fmt.Errorf("relay fs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return FSResult{}, fmt.Errorf("relay fs: center/node returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var out FSResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return FSResult{}, fmt.Errorf("relay fs decode: %w", err)
	}
	return out, nil
}

// Exec runs one shell command on the remote node and returns the bounded
// output result (mirror of WorkspaceExecutor.RunShellBudgeted).
func (c *RelayClient) Exec(ctx context.Context, req ExecRequest) (CommandOutputResult, error) {
	if strings.TrimSpace(c.NodeID) == "" || strings.TrimSpace(c.CenterURL) == "" {
		return CommandOutputResult{}, fmt.Errorf("relay execution backend requires relay center and node id")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return CommandOutputResult{}, err
	}
	url := c.base() + "/api/control/nodes/" + c.NodeID + "/proxy/control/sandbox/exec"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return CommandOutputResult{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(hreq)
	if err != nil {
		return CommandOutputResult{}, fmt.Errorf("relay exec: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return CommandOutputResult{}, fmt.Errorf("relay exec: center/node returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var out CommandOutputResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CommandOutputResult{}, fmt.Errorf("relay exec decode: %w", err)
	}
	return out, nil
}
