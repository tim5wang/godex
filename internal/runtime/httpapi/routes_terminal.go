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

	"github.com/tim5wang/godex/internal/platform/logger"
)

// terminalManager owns the in-memory set of active terminal sessions.
// Each session wraps a live bash/sh process with pipe-based I/O.
// Output is continuously read from the process stdout/stderr combined
// and appended to a ring buffer so polling clients can request data
// since a given cursor.
//
// v2.0 ships with pipe-based I/O (works for basic commands like ls,
// pwd, cat).  A future upgrade can replace pipe I/O with a real PTY
// (e.g. github.com/creack/pty) to get interactive-capable terminals
// with ANSI color support.
type terminalManager struct {
	mu        sync.Mutex
	terminals map[string]*terminalSession
}

type terminalSession struct {
	id        string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	cancel    context.CancelFunc
	sessionMu sync.Mutex
	buffer    []byte
	cursor    atomic.Int64 // monotonically increasing output cursor
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

// create spawns a bash (or sh) shell with pipe-based I/O and starts a
// goroutine that continuously reads output into the ring buffer.
func (m *terminalManager) create(ctx context.Context, workspaceDir string) (*terminalSession, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "bash"
		if _, err := exec.LookPath(shell); err != nil {
			shell = "sh"
		}
	}
	cmd := exec.Command(shell)
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

	// Attach pipes for stdin / combined stdout+stderr.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	ctx, cancel := context.WithCancel(ctx)
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start shell: %w", err)
	}

	session := &terminalSession{
		id:        randomTerminalID(),
		cmd:       cmd,
		stdin:     stdin,
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

	// Background goroutine: read combined stdout/stderr into the ring
	// buffer and track the exit state.
	go session.readLoop(stdout)

	// Background goroutine: wait for the process to exit and mark it.
	go func() {
		_ = cmd.Wait()
		session.exited.Store(true)
		close(session.done)
	}()

	return session, nil
}

// output returns buffered output since cursor, together with the
// latest cursor and exit flag. Returns empty data string if nothing
// new is available.
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

// writeInput sends data to the shell's stdin. It does NOT echo the
// data back — the shell's own echo (or the program being run) will
// produce output that appears in the next poll response.
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
	_, err := io.WriteString(session.stdin, data)
	session.sessionMu.Unlock()
	return err
}

// kill terminates the terminal session and removes it from the manager.
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
	session.stdin.Close()
	return nil
}

// readLoop continuously reads from stdout into the session buffer.
func (s *terminalSession) readLoop(reader io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			s.sessionMu.Lock()
			s.buffer = append(s.buffer, buf[:n]...)
			// Trim the buffer if it grows too large (keep last 1 MB).
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

// --- API types ---

type createTerminalRequest struct {
	WorkspaceDir string `json:"workspaceDir"`
}

type createTerminalResponse struct {
	TerminalID    string `json:"terminalId"`
	InitialCursor int64  `json:"initialCursor"`
}

type terminalInputRequest struct {
	Data string `json:"data"`
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
	session, err := m.create(r.Context(), req.WorkspaceDir)
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
