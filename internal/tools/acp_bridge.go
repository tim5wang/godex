package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	platformtooling "github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

// ---------------------------------------------------------------------------
// fs bridge: fs/read_text_file and fs/write_text_file
// ---------------------------------------------------------------------------

// acpFSBridge answers the external agent's fs/read_text_file and
// fs/write_text_file requests by operating on the godex workspace through the
// workspace-scoped FS (path escape protection included). It is wired into an
// acpSDKClient when the host wants to expose its workspace files to the agent.
type acpFSBridge struct {
	workspace string
	fs        workspacefs.FS
}

// newACPFSBridge builds an fs bridge rooted at the given absolute workspace.
func newACPFSBridge(workspace string) (*acpFSBridge, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, fmt.Errorf("fs bridge requires a workspace")
	}
	root, err := workspacefs.New(workspace)
	if err != nil {
		return nil, err
	}
	return &acpFSBridge{workspace: workspace, fs: root}, nil
}

// ReadTextFile reads a text file (optionally a line range) from the workspace.
func (b *acpFSBridge) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	_ = ctx
	path := strings.TrimSpace(params.Path)
	if path == "" {
		return acp.ReadTextFileResponse{}, fmt.Errorf("fs/read_text_file: path is required")
	}
	data, err := b.fs.ReadFile(path)
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("fs/read_text_file: %w", err)
	}
	content := selectTextFileRange(string(data), params.Line, params.Limit)
	return acp.ReadTextFileResponse{Content: content}, nil
}

// WriteTextFile writes text content into the workspace, creating parent
// directories as needed. Paths outside the workspace are rejected by the
// workspace FS.
func (b *acpFSBridge) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	_ = ctx
	path := strings.TrimSpace(params.Path)
	if path == "" {
		return acp.WriteTextFileResponse{}, fmt.Errorf("fs/write_text_file: path is required")
	}
	if err := b.fs.WriteFile(path, []byte(params.Content), 0o644); err != nil {
		return acp.WriteTextFileResponse{}, fmt.Errorf("fs/write_text_file: %w", err)
	}
	return acp.WriteTextFileResponse{}, nil
}

// selectTextFileRange slices file content by an optional 1-based start line and
// an optional line limit. Nil/zero values select the whole file (matching the
// ACP protocol semantics: line is 1-based, limit caps the number of lines).
func selectTextFileRange(content string, line, limit *int) string {
	if line == nil && limit == nil {
		return content
	}
	lines := strings.Split(content, "\n")
	start := 0
	if line != nil && *line > 1 {
		start = *line - 1
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if limit != nil && *limit > 0 && start+*limit < end {
		end = start + *limit
	}
	return strings.Join(lines[start:end], "\n")
}

// ---------------------------------------------------------------------------
// terminal bridge: terminal/create, terminal/output, terminal/wait_for_exit,
// terminal/kill, terminal/release
// ---------------------------------------------------------------------------

// acpTerminal is one live command execution managed by the terminal bridge.
// It runs the requested command non-interactively and retains its output, so
// the external agent can poll output and wait for exit without a PTY.
type acpTerminal struct {
	id string

	mu        sync.Mutex
	cmd       *exec.Cmd
	output    bytes.Buffer
	limit     int
	truncated bool
	done      chan struct{}
	exitCode  int
	exitErr   error
	released  bool
}

// acpTerminalManager tracks the terminals created by one ACP client so
// terminal/output, terminal/wait_for_exit, terminal/kill and terminal/release
// can locate them by id.
type acpTerminalManager struct {
	workspace string

	mu        sync.Mutex
	next      int
	terminals map[string]*acpTerminal
}

func newACPTerminalManager(workspace string) *acpTerminalManager {
	return &acpTerminalManager{workspace: workspace, terminals: map[string]*acpTerminal{}}
}

// CreateTerminal starts a command in the host workspace. The agent supplies an
// absolute cwd; when empty the workspace root is used.
func (m *acpTerminalManager) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	command := strings.TrimSpace(params.Command)
	if command == "" {
		return acp.CreateTerminalResponse{}, fmt.Errorf("terminal/create: command is required")
	}
	cwd := ""
	if params.Cwd != nil {
		cwd = strings.TrimSpace(*params.Cwd)
	}
	if cwd == "" {
		cwd = m.workspace
	}
	resolvedCWD, err := acpTerminalCWD(m.workspace, cwd)
	if err != nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf("terminal/create: %w", err)
	}
	cwd = resolvedCWD
	cmd := exec.CommandContext(ctx, command, params.Args...)
	if err := platformtooling.ConfigureCommandProcessGroup(cmd); err != nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf("terminal/create: process group: %w", err)
	}
	cmd.Dir = cwd
	env := os.Environ()
	for _, entry := range params.Env {
		env = append(env, entry.Name+"="+entry.Value)
	}
	cmd.Env = env

	limit := 1 << 20 // 1 MiB default retained output
	if params.OutputByteLimit != nil && *params.OutputByteLimit > 0 {
		limit = *params.OutputByteLimit
	}

	m.mu.Lock()
	m.next++
	id := fmt.Sprintf("term-%d", m.next)
	term := &acpTerminal{id: id, cmd: cmd, limit: limit, done: make(chan struct{})}
	m.terminals[id] = term
	m.mu.Unlock()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf("terminal/create: stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf("terminal/create: stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		m.remove(id)
		return acp.CreateTerminalResponse{}, fmt.Errorf("terminal/create: %w", err)
	}
	// Stream both pipes into the retained buffer.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				term.append(buf[:n])
			}
			if err != nil {
				break
			}
		}
	}()
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				term.append(buf[:n])
			}
			if err != nil {
				break
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		term.mu.Lock()
		term.exitErr = err
		term.exitCode = cmd.ProcessState.ExitCode()
		term.mu.Unlock()
		close(term.done)
	}()
	return acp.CreateTerminalResponse{TerminalId: id}, nil
}

func acpTerminalCWD(workspace, cwd string) (string, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	target := cwd
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if evaluated, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
		root = evaluated
	}
	if evaluated, evalErr := filepath.EvalSymlinks(target); evalErr == nil {
		target = evaluated
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("cwd %q escapes workspace %q", cwd, workspace)
	}
	return target, nil
}

func (t *acpTerminal) append(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	remaining := t.limit - t.output.Len()
	if remaining <= 0 {
		t.truncated = true
		return
	}
	if len(data) > remaining {
		t.output.Write(data[:remaining])
		t.truncated = true
	} else {
		t.output.Write(data)
	}
}

func (m *acpTerminalManager) get(id string) (*acpTerminal, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	term, ok := m.terminals[id]
	return term, ok
}

func (m *acpTerminalManager) remove(id string) {
	m.mu.Lock()
	delete(m.terminals, id)
	m.mu.Unlock()
}

// TerminalOutput returns the retained output and the exit status when the
// command has completed.
func (m *acpTerminalManager) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	_ = ctx
	term, ok := m.get(params.TerminalId)
	if !ok {
		return acp.TerminalOutputResponse{}, fmt.Errorf("terminal/output: unknown terminal %q", params.TerminalId)
	}
	term.mu.Lock()
	output := term.output.String()
	truncated := term.truncated
	done := false
	select {
	case <-term.done:
		done = true
	default:
	}
	exitStatus := (*acp.TerminalExitStatus)(nil)
	if done {
		exitStatus = &acp.TerminalExitStatus{ExitCode: intPtr(term.exitCode)}
	}
	term.mu.Unlock()
	return acp.TerminalOutputResponse{Output: output, Truncated: truncated, ExitStatus: exitStatus}, nil
}

// WaitForTerminalExit blocks until the command exits and returns its exit
// code / signal.
func (m *acpTerminalManager) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	term, ok := m.get(params.TerminalId)
	if !ok {
		return acp.WaitForTerminalExitResponse{}, fmt.Errorf("terminal/wait_for_exit: unknown terminal %q", params.TerminalId)
	}
	select {
	case <-term.done:
	case <-ctx.Done():
		return acp.WaitForTerminalExitResponse{}, ctx.Err()
	}
	term.mu.Lock()
	code := term.exitCode
	term.mu.Unlock()
	return acp.WaitForTerminalExitResponse{ExitCode: intPtr(code)}, nil
}

// KillTerminal terminates a running command without releasing it.
func (m *acpTerminalManager) KillTerminal(ctx context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	_ = ctx
	term, ok := m.get(params.TerminalId)
	if !ok {
		return acp.KillTerminalResponse{}, fmt.Errorf("terminal/kill: unknown terminal %q", params.TerminalId)
	}
	term.mu.Lock()
	cmd := term.cmd
	term.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		platformtooling.KillCommandProcessGroup(cmd)
	}
	return acp.KillTerminalResponse{}, nil
}

// ReleaseTerminal frees a terminal's resources. A running process is killed
// first so no orphan survives the release.
func (m *acpTerminalManager) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	_ = ctx
	term, ok := m.get(params.TerminalId)
	if !ok {
		return acp.ReleaseTerminalResponse{}, nil
	}
	term.mu.Lock()
	cmd := term.cmd
	released := term.released
	term.released = true
	term.mu.Unlock()
	if !released && cmd != nil && cmd.Process != nil {
		platformtooling.KillCommandProcessGroup(cmd)
		select {
		case <-term.done:
		case <-time.After(2 * time.Second):
		}
	}
	m.remove(params.TerminalId)
	return acp.ReleaseTerminalResponse{}, nil
}

// Close kills and releases every terminal the manager owns. Called when the
// ACP session is torn down.
func (m *acpTerminalManager) Close() {
	m.mu.Lock()
	terms := make([]*acpTerminal, 0, len(m.terminals))
	for _, term := range m.terminals {
		terms = append(terms, term)
	}
	m.terminals = map[string]*acpTerminal{}
	m.mu.Unlock()
	for _, term := range terms {
		term.mu.Lock()
		cmd := term.cmd
		term.mu.Unlock()
		if cmd != nil && cmd.Process != nil {
			platformtooling.KillCommandProcessGroup(cmd)
		}
	}
}

func intPtr(value int) *int { return &value }
