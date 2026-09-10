package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

// registerSandboxRoutes wires the B-side sandbox endpoints that the
// RemoteSandbox (docs/remote-sandbox-design.md M1) on node A calls through the
// center relay tunnel:
//
//	POST /control/sandbox/exec   run one shell command in this node's workspace
//	POST /control/sandbox/fs     file operations (read/write/readdir/stat/...)
//
// Both are protected like every other control endpoint: the node's own web
// token OR the relay trust header (which the center injects when forwarding
// from A) is accepted, so A cannot reach them without the relay channel.
func registerSandboxRoutes(mux *http.ServeMux, manager *config.Manager, protected func(http.Handler) http.Handler) {
	mux.Handle("POST /control/sandbox/exec", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req tooling.ExecRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Command) == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("missing command argument"))
			return
		}
		cfg := manager.Current()
		workspace := resolveSandboxWorkspace(req.Workspace, cfg)
		executor := tooling.NewWorkspaceExecutorWithTempDirAndExecution(workspace, cfg.TempDir, tooling.ExecutionConfig{})
		ctx, cancel := sandboxExecContext(r, req.TimeoutSeconds)
		defer cancel()
		out, err := executor.RunShellBudgetedWithOptions(ctx, req.Command, tooling.ShellCommandOptions{
			WorkspaceDir:          workspace,
			AllowUnlistedCommands: req.AllowUnlisted,
		})
		if err != nil {
			// Non-zero exit is not an error at the HTTP layer: the caller needs
			// the bounded output (stdout/stderr) to reason about the failure.
			writeJSON(w, http.StatusOK, out)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})))

	mux.Handle("POST /control/sandbox/fs", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req fsOpRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cfg := manager.Current()
		workspace := resolveSandboxWorkspace(req.Workspace, cfg)
		fs, err := workspacefs.New(workspace)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		result, err := applyFSOp(fs, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
}

// resolveSandboxWorkspace picks the effective workspace directory for a
// sandbox exec/fs operation on this node. An empty requested workspace falls
// back to the configured workspace dir. A requested workspace that does not
// exist on this node — e.g. a relay session carrying the caller's /root while
// godex was started from a different directory — falls back to this node's
// startup directory (os.Getwd), i.e. where `godex node join` was launched, so
// exec/fs (Files) keep working instead of failing with a missing-directory
// error.
func resolveSandboxWorkspace(requested string, cfg *config.Config) string {
	workspace := strings.TrimSpace(requested)
	if workspace == "" {
		return cfg.WorkspaceDir
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return cfg.WorkspaceDir
	}
	info, statErr := os.Stat(abs)
	if statErr != nil || !info.IsDir() {
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			return cwd
		}
		return cfg.WorkspaceDir
	}
	return abs
}

// sandboxExecContext derives a context with the requested timeout (0 = no
// explicit timeout, inherits the request context).
func sandboxExecContext(r *http.Request, timeoutSeconds int) (context.Context, context.CancelFunc) {
	if timeoutSeconds <= 0 {
		return context.WithCancel(r.Context())
	}
	return context.WithTimeout(r.Context(), time.Duration(timeoutSeconds)*time.Second)
}
type fsOpRequest struct {
	Op        string `json:"op"` // read|write|readdir|stat|mkdir|rm|rename
	Path      string `json:"path"`
	Data      string `json:"data,omitempty"` // base64 for write
	Mode      uint32 `json:"mode,omitempty"`
	NewPath   string `json:"newpath,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

type fsOpResult struct {
	Data    string      `json:"data,omitempty"` // base64 for read
	Entries []fsDirItem `json:"entries,omitempty"`
	Info    *fsFileInfo `json:"info,omitempty"`
	Removed bool        `json:"removed,omitempty"`
}

type fsDirItem struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"`
}

type fsFileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModeStr string `json:"mode"`
}

func applyFSOp(fs workspacefs.FS, req fsOpRequest) (fsOpResult, error) {
	path := strings.TrimSpace(req.Path)
	if path == "" {
		return fsOpResult{}, fmt.Errorf("path is required")
	}
	switch req.Op {
	case "read":
		data, err := fs.ReadFile(path)
		if err != nil {
			return fsOpResult{}, err
		}
		return fsOpResult{Data: base64.StdEncoding.EncodeToString(data)}, nil
	case "write":
		data, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			return fsOpResult{}, fmt.Errorf("write data must be base64: %w", err)
		}
		mode := os.FileMode(req.Mode)
		if mode == 0 {
			mode = 0o644
		}
		if err := fs.WriteFile(path, data, mode); err != nil {
			return fsOpResult{}, err
		}
		return fsOpResult{}, nil
	case "readdir":
		entries, err := fs.ReadDir(path)
		if err != nil {
			return fsOpResult{}, err
		}
		items := make([]fsDirItem, 0, len(entries))
		for _, e := range entries {
			items = append(items, fsDirItem{Name: e.Name(), IsDir: e.IsDir()})
		}
		return fsOpResult{Entries: items}, nil
	case "stat":
		info, err := fs.Stat(path)
		if err != nil {
			return fsOpResult{}, err
		}
		return fsOpResult{Info: &fsFileInfo{
			Name:    info.Name(),
			Size:    info.Size(),
			IsDir:   info.IsDir(),
			ModeStr: info.Mode().String(),
		}}, nil
	case "mkdir":
		mode := os.FileMode(req.Mode)
		if mode == 0 {
			mode = 0o755
		}
		if err := fs.MkdirAll(path, mode); err != nil {
			return fsOpResult{}, err
		}
		return fsOpResult{}, nil
	case "rm":
		if err := fs.RemoveAll(path); err != nil {
			return fsOpResult{}, err
		}
		return fsOpResult{Removed: true}, nil
	case "rename":
		if strings.TrimSpace(req.NewPath) == "" {
			return fsOpResult{}, fmt.Errorf("newpath is required for rename")
		}
		if err := fs.Rename(path, strings.TrimSpace(req.NewPath)); err != nil {
			return fsOpResult{}, err
		}
		return fsOpResult{}, nil
	default:
		return fsOpResult{}, fmt.Errorf("unsupported fs op %q", req.Op)
	}
}
