package workspacefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// Interface
// ---------------------------------------------------------------------------

// FS is a narrow file boundary rooted at one workspace.  Implementations may
// be backed by the local OS (osFS) or a remote filesystem (e.g. afero over
// SFTP — see afero_fs.go).
type FS interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
	ReadDir(name string) ([]fs.DirEntry, error)
	Stat(name string) (os.FileInfo, error)
	Open(name string) (io.ReadSeekCloser, error)
	Abs(name string) (string, error)
	Close() error
	Dir() string

	// Extended operations for file management (Phase 5).
	RemoveAll(name string) error
	MkdirAll(name string, perm os.FileMode) error
	Rename(oldname, newname string) error
}

// Backend selects which FS implementation New creates.
type Backend int

const (
	BackendOS    Backend = iota // local OS filesystem (the default)
	BackendAfero                // afero-backed (local or SFTP)
)

// Config controls FS creation.
type Config struct {
	Backend      Backend
	WorkspaceDir string
	Allowlist    []string
	// AferoFs is only consulted when Backend == BackendAfero.  Pass an
	// afero.Fs that is already rooted at the desired workspace (e.g.
	// afero.NewOsFs() or sftpfs.New() wrapped with BasePathFs).
	AferoFs interface{ Name() string } // afero.Fs interface — we avoid a hard import so callers import afero themselves.
}

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// New opens a local (osFS) workspace with the given allowlist.  This is the
// backward-compatible convenience function.
func New(workspace string, allowlist ...string) (FS, error) {
	return newOSFS(workspace, allowlist)
}

// NewWithConfig opens a workspace FS using the requested backend.
func NewWithConfig(cfg Config) (FS, error) {
	switch cfg.Backend {
	case BackendAfero:
		return newAferoFS(cfg)
	default:
		return newOSFS(cfg.WorkspaceDir, cfg.Allowlist)
	}
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// errAllowedExternal is a sentinel returned by path resolution when the path
// is outside the workspace root but inside one of the configured allowlisted
// directories (e.g. ~/.godex/memory).  Read-only operations (ReadFile, Stat,
// ReadDir) fall back to os.* for these paths; write operations reject them.
var errAllowedExternal = errors.New("path is allowed external")

// DefaultReadAllowlist is a set of absolute directory prefixes that are
// automatically added to every FS's allowlist.  Callers should set it once at
// startup (e.g. to include ~/.godex/memory, ~/.godex/state, ~/.godex/skills,
// ~/.godex/tmp) so that read-only file tools can access godex's own state
// directories even when they sit outside the workspace tree.
var DefaultReadAllowlist []string

func mergeAllowlists(perCall []string) ([]string, error) {
	allow := make([]string, 0, len(DefaultReadAllowlist)+len(perCall))
	for _, a := range DefaultReadAllowlist {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if !filepath.IsAbs(a) {
			var err error
			a, err = filepath.Abs(a)
			if err != nil {
				return nil, fmt.Errorf("workspacefs: default allowlist path %q: %w", a, err)
			}
		}
		allow = append(allow, filepath.Clean(a))
	}
	for _, a := range perCall {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if !filepath.IsAbs(a) {
			var err error
			a, err = filepath.Abs(a)
			if err != nil {
				return nil, fmt.Errorf("workspacefs: resolve allowlist path %q: %w", a, err)
			}
		}
		allow = append(allow, filepath.Clean(a))
	}
	return allow, nil
}

func isInAllowlist(absPath string, allowlist []string) bool {
	for _, prefix := range allowlist {
		if absPath == prefix || strings.HasPrefix(absPath, prefix+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func inside(root, target string) (bool, error) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false, err
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel), nil
}

func wrapEscape(name string, err error) error {
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "escape") || strings.Contains(text, "outside") || strings.Contains(text, "invalid argument") {
		return fmt.Errorf("path escapes workspace (outside workspace): %s", name)
	}
	return err
}
