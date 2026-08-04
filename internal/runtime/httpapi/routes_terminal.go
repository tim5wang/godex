package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"github.com/tim5wang/godex/internal/platform/logger"
)

// terminalManager owns the in-memory set of active terminal sessions.
// Each session wraps a live bash/sh process with PTY-based I/O
// (github.com/creack/pty). Falls back to pipes on platforms where
// PTY is unavailable.
//
// v3.0 (PTY upgrade): replaces the v2.0 pipe-based I/O with a real
// PTY so the shell sees a true terminal — prompts, ANSI colors,
// Ctrl+C, and interactive programs all work correctly.
type terminalManager struct {
	mu        sync.Mutex
	terminals map[string]*terminalSession
}

type terminalSession struct {
	id        string
	cmd       *exec.Cmd
	ptyFile   io.ReadWriteCloser // PTY master (or pipe wrapper on fallback)
	cancel    context.CancelFunc
	sessionMu sync.Mutex
	buffer    []byte
	cursor    atomic.Int64
	exited    atomic.Bool
	createdAt time.Time
	done      chan struct{}
}

func newTerminalManager() *terminalManager {
	return &terminalManager{
		terminals: make(map[string]*terminalSession),
	}
}

var globalTerminalManager = newTerminalManager()

// create spawns a shell for the requested execution backend.
//   - "local" (default): bash/sh with PTY, falling back to pipes.
//   - "ssh":  ssh -t <target> (the remote side allocates the PTY).
//   - "docker": docker exec -it <container> sh (requires running container).
func (m *terminalManager) create(ctx context.Context, req createTerminalRequest) (*terminalSession, error) {
	mode := strings.ToLower(strings.TrimSpace(req.ExecutionMode))
	switch mode {
	case "ssh":
		return m.createSSH(ctx, req)
	case "docker":
		return m.createDocker(ctx, req)
	default:
		return m.createLocal(ctx, req.WorkspaceDir)
	}
}

// createLocal spawns a local bash (or sh) shell with PTY-based I/O.
func (m *terminalManager) createLocal(ctx context.Context, workspaceDir string) (*terminalSession, error) {
	shell, shellArgs := resolveShell()

	cmd := exec.Command(shell, shellArgs...)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"FORCE_COLOR=1",
		"CLICOLOR=1",
		"CLICOLOR_FORCE=1",
	)
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir != "" {
		if abs, err := filepath.Abs(workspaceDir); err == nil {
			workspaceDir = abs
		}
		cmd.Dir = workspaceDir
		cmd.Env = append(cmd.Env, "HOME="+workspaceDir)
	}

	ctx, cancel := context.WithCancel(ctx)

	// Try PTY first.
	ptyFile, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		// PTY failed — fall back to pipes.
		cancel()
		return m.createWithPipes(cmd, workspaceDir)
	}

	session := m.registerSession(cmd, ptyFile, cancel)
	logger.Infof("terminal %s started (PTY) pid=%d shell=%s", session.id, cmd.Process.Pid, shell)
	return session, nil
}

// createSSH spawns an SSH interactive terminal via `ssh -t target`.
// The remote sshd allocates a PTY on its side, so we pipe stdin/stdout.
func (m *terminalManager) createSSH(ctx context.Context, req createTerminalRequest) (*terminalSession, error) {
	target := strings.TrimSpace(req.SSHTarget)
	if target == "" {
		return nil, fmt.Errorf("ssh terminal requires sshTarget")
	}
	args := append([]string{}, req.SSHOptions...)
	// -t forces pseudo-terminal allocation on the remote side.
	// Use -tt if already requested via options; otherwise plain -t.
	hasT := false
	for _, opt := range args {
		if opt == "-t" || opt == "-tt" {
			hasT = true
			break
		}
	}
	if !hasT {
		args = append(args, "-t")
	}
	args = append(args, target)
	// If a remote workspace is set, cd into it before starting the shell.
	if ws := strings.TrimSpace(req.SSHWorkspace); ws != "" {
		args = append(args, fmt.Sprintf("cd %s && exec $SHELL -l", shellQuoteArg(ws)))
	}

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
	)

	_, cancel := context.WithCancel(ctx)
	return m.startPiped(cmd, cancel)
}

// createDocker spawns a shell inside a running Docker container.
func (m *terminalManager) createDocker(ctx context.Context, req createTerminalRequest) (*terminalSession, error) {
	image := strings.TrimSpace(req.DockerImage)
	if image == "" {
		return nil, fmt.Errorf("docker terminal requires dockerImage (container name or running image)")
	}
	args := []string{"exec", "-it"}
	if net := strings.TrimSpace(req.DockerNetwork); net != "" {
		args = append(args, "--network", net)
	}
	if ws := strings.TrimSpace(req.WorkspaceDir); ws != "" {
		args = append(args, "-w", ws)
	}
	args = append(args, image, "sh", "-i")

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
	)

	_, cancel := context.WithCancel(ctx)
	return m.startPiped(cmd, cancel)
}

// registerSession wires a started cmd+pty into the manager and starts
// the read/wait goroutines. Shared by all backend modes.
func (m *terminalManager) registerSession(cmd *exec.Cmd, ptyFile io.ReadWriteCloser, cancel context.CancelFunc) *terminalSession {
	session := &terminalSession{
		id:        randomTerminalID(),
		cmd:       cmd,
		ptyFile:   ptyFile,
		cancel:    cancel,
		buffer:    make([]byte, 0, defaultTerminalBufferSize),
		createdAt: time.Now(),
		done:      make(chan struct{}),
	}
	session.cursor.Store(0)
	session.exited.Store(false)

	m.mu.Lock()
	m.terminals[session.id] = session
	m.mu.Unlock()

	go session.readLoop(ptyFile)

	go func() {
		_ = cmd.Wait()
		session.exited.Store(true)
		close(session.done)
	}()

	return session
}

// startPiped starts cmd with piped stdin/stdout (no local PTY) and
// registers the session. Used by SSH and Docker backends where the
// remote side (sshd / docker exec -t) already handles PTY allocation.
func (m *terminalManager) startPiped(cmd *exec.Cmd, cancel context.CancelFunc) (*terminalSession, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start: %w", err)
	}

	pw := &pipeWrapper{stdin: stdin, stdout: stdout}
	session := m.registerSession(cmd, pw, cancel)
	logger.Infof("terminal %s started (piped) pid=%d cmd=%s", session.id, cmd.Process.Pid, cmd.String())
	return session, nil
}

// shellQuoteArg quotes a string for safe use as a shell argument.
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// createWithPipes is the fallback when PTY is unavailable.
func (m *terminalManager) createWithPipes(cmd *exec.Cmd, _ string) (*terminalSession, error) {
	_, cancel := context.WithCancel(context.Background())
	return m.startPiped(cmd, cancel)
}

// resolveShell picks bash, falling back to sh with -i for PTY.
func resolveShell() (string, []string) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "bash"
		if _, err := exec.LookPath(shell); err != nil {
			shell = "sh"
		}
	}
	// With PTY we pass -i to get interactive behavior (prompts, job control).
	return shell, []string{"-i"}
}

// output returns buffered output since cursor.
func (m *terminalManager) output(terminalID string, cursor int64) (outputChunk, error) {
	m.mu.Lock()
	session, ok := m.terminals[terminalID]
	m.mu.Unlock()
	if !ok {
		return outputChunk{}, fmt.Errorf("terminal %s not found", terminalID)
	}

	session.sessionMu.Lock()
	defer session.sessionMu.Unlock()

	buf := session.buffer
	currentCursor := session.cursor.Load()
	exited := session.exited.Load()

	if cursor < 0 {
		cursor = 0
	}
	if cursor >= int64(len(buf)) {
		return outputChunk{
			TerminalID: terminalID,
			Cursor:     currentCursor,
			Data:       "",
			Exited:     exited,
		}, nil
	}

	data := string(buf[cursor:])
	return outputChunk{
		TerminalID: terminalID,
		Cursor:     int64(len(buf)),
		Data:       data,
		Exited:     exited,
	}, nil
}

// writeInput sends data to the shell's stdin via the PTY.
func (m *terminalManager) writeInput(terminalID string, data string) error {
	m.mu.Lock()
	session, ok := m.terminals[terminalID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("terminal %s not found", terminalID)
	}
	if session.exited.Load() {
		return fmt.Errorf("terminal %s has exited", terminalID)
	}

	session.sessionMu.Lock()
	_, err := io.WriteString(session.ptyFile, data)
	session.sessionMu.Unlock()
	return err
}

// resize updates the PTY window size. No-op on pipe fallback.
func (m *terminalManager) resize(terminalID string, cols, rows int) error {
	m.mu.Lock()
	session, ok := m.terminals[terminalID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("terminal %s not found", terminalID)
	}

	f, ok := session.ptyFile.(*os.File)
	if !ok || f == nil {
		return nil // pipe fallback — silently ignore
	}
	return pty.Setsize(f, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// kill terminates the terminal session.
func (m *terminalManager) kill(terminalID string) error {
	m.mu.Lock()
	session, ok := m.terminals[terminalID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("terminal %s not found", terminalID)
	}
	delete(m.terminals, terminalID)
	m.mu.Unlock()

	session.cancel()
	if session.ptyFile != nil {
		session.ptyFile.Close()
	}
	return nil
}

// readLoop continuously reads output from the PTY into the buffer.
func (s *terminalSession) readLoop(reader io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			s.sessionMu.Lock()
			s.buffer = append(s.buffer, buf[:n]...)
			if len(s.buffer) > maxTerminalBufferSize {
				overflow := len(s.buffer) - maxTerminalBufferSize
				s.buffer = s.buffer[overflow:]
			}
			s.cursor.Store(int64(len(s.buffer)))
			s.sessionMu.Unlock()
		}
		if err != nil {
			if err != io.EOF {
				logger.Warnf("terminal %s read error: %v", s.id, err)
			}
			return
		}
	}
}

// pipeWrapper combines stdin/stdout pipes into io.ReadWriteCloser.
type pipeWrapper struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

func (pw *pipeWrapper) Read(p []byte) (n int, err error)  { return pw.stdout.Read(p) }
func (pw *pipeWrapper) Write(p []byte) (n int, err error) { return pw.stdin.Write(p) }
func (pw *pipeWrapper) Close() error {
	pw.stdin.Close()
	if c, ok := pw.stdout.(io.Closer); ok {
		c.Close()
	}
	return nil
}

// --- API types ---

type createTerminalRequest struct {
	WorkspaceDir   string   `json:"workspaceDir"`
	ExecutionMode  string   `json:"executionMode,omitempty"`
	SSHTarget      string   `json:"sshTarget,omitempty"`
	SSHWorkspace   string   `json:"sshWorkspace,omitempty"`
	SSHOptions     []string `json:"sshOptions,omitempty"`
	DockerImage    string   `json:"dockerImage,omitempty"`
	DockerNetwork  string   `json:"dockerNetwork,omitempty"`
}

type createTerminalResponse struct {
	TerminalID    string `json:"terminalId"`
	InitialCursor int64  `json:"initialCursor"`
}

type terminalInputRequest struct {
	Data string `json:"data"`
}

type terminalResizeRequest struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

type outputChunk struct {
	TerminalID string `json:"terminalId"`
	Cursor     int64  `json:"cursor"`
	Data       string `json:"data"`
	Exited     bool   `json:"exited"`
}

// --- HTTP handlers ---

func (m *terminalManager) handleCreateTerminal(w http.ResponseWriter, r *http.Request) {
	var req createTerminalRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !isEOFish(err) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid create terminal body: %w", err))
			return
		}
	}
	session, err := m.create(r.Context(), req)
	if err != nil {
		logger.Warnf("terminal create: %v", err)
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to create terminal: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, createTerminalResponse{
		TerminalID:    session.id,
		InitialCursor: 0,
	})
}

func (m *terminalManager) handleTerminalOutput(w http.ResponseWriter, r *http.Request) {
	terminalID := strings.TrimSpace(r.PathValue("id"))
	if terminalID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("missing terminal id"))
		return
	}
	var cursor int64
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &cursor); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid cursor: %s", raw))
			return
		}
	}
	chunk, err := m.output(terminalID, cursor)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, chunk)
}

func (m *terminalManager) handleTerminalInput(w http.ResponseWriter, r *http.Request) {
	terminalID := strings.TrimSpace(r.PathValue("id"))
	if terminalID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("missing terminal id"))
		return
	}
	var req terminalInputRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := m.writeInput(terminalID, req.Data); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminalId": terminalID, "accepted": true})
}

func (m *terminalManager) handleTerminalResize(w http.ResponseWriter, r *http.Request) {
	terminalID := strings.TrimSpace(r.PathValue("id"))
	if terminalID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("missing terminal id"))
		return
	}
	var req terminalResizeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Cols < 1 {
		req.Cols = 80
	}
	if req.Rows < 1 {
		req.Rows = 24
	}
	if err := m.resize(terminalID, req.Cols, req.Rows); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminalId": terminalID, "cols": req.Cols, "rows": req.Rows})
}

func (m *terminalManager) handleTerminalDelete(w http.ResponseWriter, r *http.Request) {
	terminalID := strings.TrimSpace(r.PathValue("id"))
	if terminalID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("missing terminal id"))
		return
	}
	if err := m.kill(terminalID); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminalId": terminalID, "exited": true})
}

func randomTerminalID() string {
	var b [8]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return fmt.Sprintf("term-%d", time.Now().UnixNano())
	}
	return "term-" + hex.EncodeToString(b[:])
}

func isEOFish(err error) bool {
	return err == io.EOF || err == io.ErrUnexpectedEOF
}

const (
	defaultTerminalBufferSize = 64 * 1024       // 64 KB initial
	maxTerminalBufferSize     = 1 * 1024 * 1024 // 1 MB cap
)
