package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

// RemoteOptions configures a RemoteSandbox: a sandbox whose shell/file tools
// execute on a REMOTE godex node (sandbox node B) via the center relay tunnel
// instead of the local filesystem (docs/remote-sandbox-design.md M1).
type RemoteOptions struct {
	ID           string
	Scope        scope.Id
	WorkspaceDir string // B-side workspace path (passed through to B)
	TempDir      string // B-side temp dir
	ArtifactDir  string // B-side artifact dir
	// Relay target: center URL + node id + restricted nk_ credential.
	CenterURL string
	NodeID    string
	Token     string
	Execution tooling.ExecutionConfig
	Lifecycle Lifecycle
}

// RemoteSandbox implements sandbox.Sandbox by forwarding shell execution and
// file operations to a remote node. WorkspaceDir/TempDir/ArtifactDir are the
// B-side paths; the A-side agent stores them as opaque strings and every
// operation is relayed.
type RemoteSandbox struct {
	id           string
	scope        scope.Id
	lifecycle    Lifecycle
	workspaceDir string
	tempDir      string
	artifactDir  string
	centerURL    string
	nodeID       string
	token        string
	execution    tooling.ExecutionConfig
}

var _ Sandbox = (*RemoteSandbox)(nil)

func StableRemoteID(centerURL, nodeID string) string {
	return "sandbox:remote:" + nodeID
}

// NewRemote creates a RemoteSandbox targeting node B through centerURL. The
// sandbox id is derived from the target node id so rebuilds keep identity.
func NewRemote(opts RemoteOptions) *RemoteSandbox {
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		id = StableRemoteID(opts.CenterURL, opts.NodeID)
	}
	lifecycle := opts.Lifecycle
	if lifecycle == "" {
		lifecycle = "remote"
	}
	return &RemoteSandbox{
		id:           id,
		scope:        opts.Scope,
		lifecycle:    lifecycle,
		workspaceDir: filepath.Clean(strings.TrimSpace(opts.WorkspaceDir)),
		tempDir:      filepath.Clean(strings.TrimSpace(opts.TempDir)),
		artifactDir:  filepath.Clean(strings.TrimSpace(opts.ArtifactDir)),
		centerURL:    strings.TrimSpace(opts.CenterURL),
		nodeID:       strings.TrimSpace(opts.NodeID),
		token:        strings.TrimSpace(opts.Token),
		execution:    cloneExecution(opts.Execution),
	}
}

func (s *RemoteSandbox) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

func (s *RemoteSandbox) Lifecycle() Lifecycle {
	if s == nil {
		return ""
	}
	return s.lifecycle
}

func (s *RemoteSandbox) WorkspaceDir() string {
	if s == nil {
		return ""
	}
	return s.workspaceDir
}

func (s *RemoteSandbox) TempDir() string {
	if s == nil {
		return ""
	}
	return s.tempDir
}

func (s *RemoteSandbox) ArtifactDir() string {
	if s == nil {
		return ""
	}
	return s.artifactDir
}

func (s *RemoteSandbox) ScopeID() scope.Id {
	if s == nil {
		return ""
	}
	return s.scope
}

// relayClient returns a RelayClient bound to the sandbox target.
func (s *RemoteSandbox) relayClient() *tooling.RelayClient {
	return &tooling.RelayClient{
		CenterURL: s.centerURL,
		NodeID:    s.nodeID,
		Token:     s.token,
	}
}

func (s *RemoteSandbox) ToolBinding() ToolBinding {
	if s == nil {
		return ToolBinding{}
	}
	exec := cloneExecution(s.execution)
	exec.Mode = tooling.ExecutionModeRelay
	exec.RelayCenter = s.centerURL
	exec.RelayNode = s.nodeID
	exec.RelayToken = s.token
	return ToolBinding{
		SandboxID:    s.id,
		WorkspaceDir: s.workspaceDir,
		TempDir:      s.tempDir,
		ArtifactDir:  s.artifactDir,
		Execution:    exec,
	}
}

func (s *RemoteSandbox) Info() Info {
	if s == nil {
		return Info{}
	}
	return Info{
		ID:           s.id,
		Lifecycle:    s.lifecycle,
		WorkspaceDir: s.workspaceDir,
		TempDir:      s.tempDir,
		ArtifactDir:  s.artifactDir,
	}
}

func (s *RemoteSandbox) FileSystem() (workspacefs.FS, error) {
	if s == nil {
		return nil, fmt.Errorf("nil remote sandbox")
	}
	return &RemoteFS{client: s.relayClient(), workspace: s.workspaceDir}, nil
}

func (s *RemoteSandbox) Rebuild() Sandbox {
	if s == nil {
		return nil
	}
	return NewRemote(RemoteOptions{
		ID:           s.id,
		Scope:        s.scope,
		WorkspaceDir: s.workspaceDir,
		TempDir:      s.tempDir,
		ArtifactDir:  s.artifactDir,
		CenterURL:    s.centerURL,
		NodeID:       s.nodeID,
		Token:        s.token,
		Execution:    cloneExecution(s.execution),
		Lifecycle:    s.lifecycle,
	})
}

// RemoteFS implements workspacefs.FS over the relay tunnel: every method is a
// JSON round-trip to B's /control/sandbox/fs endpoint.
type RemoteFS struct {
	client    *tooling.RelayClient
	workspace string
}

var _ workspacefs.FS = (*RemoteFS)(nil)

func (f *RemoteFS) do(ctx context.Context, op, path string, mutate func(*tooling.FSRequest)) (tooling.FSResult, error) {
	req := tooling.FSRequest{Op: op, Path: path, Workspace: f.workspace}
	if mutate != nil {
		mutate(&req)
	}
	return f.client.FS(ctx, req)
}

func (f *RemoteFS) ReadFile(name string) ([]byte, error) {
	res, err := f.do(context.Background(), "read", name, nil)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(res.Data)
}

func (f *RemoteFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	_, err := f.do(context.Background(), "write", name, func(req *tooling.FSRequest) {
		req.Data = base64.StdEncoding.EncodeToString(data)
		req.Mode = uint32(perm)
	})
	return err
}

func (f *RemoteFS) ReadDir(name string) ([]fs.DirEntry, error) {
	res, err := f.do(context.Background(), "readdir", name, nil)
	if err != nil {
		return nil, err
	}
	entries := make([]fs.DirEntry, 0, len(res.Entries))
	for _, e := range res.Entries {
		entries = append(entries, remoteDirEntry{name: e.Name, isDir: e.IsDir, size: e.Size})
	}
	return entries, nil
}

func (f *RemoteFS) Stat(name string) (os.FileInfo, error) {
	res, err := f.do(context.Background(), "stat", name, nil)
	if err != nil {
		return nil, err
	}
	if res.Info == nil {
		return nil, fmt.Errorf("remote stat: no info returned")
	}
	return remoteFileInfo{name: res.Info.Name, size: res.Info.Size, isDir: res.Info.IsDir, modeStr: res.Info.ModeStr}, nil
}

func (f *RemoteFS) Open(name string) (io.ReadSeekCloser, error) {
	res, err := f.do(context.Background(), "read", name, nil)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		return nil, err
	}
	return &bytesReadSeekCloser{Reader: bytes.NewReader(data)}, nil
}

func (f *RemoteFS) Abs(name string) (string, error) {
	if filepath.IsAbs(name) {
		return name, nil
	}
	return filepath.Join(f.workspace, name), nil
}

func (f *RemoteFS) Close() error { return nil }

func (f *RemoteFS) Dir() string { return f.workspace }

func (f *RemoteFS) RemoveAll(name string) error {
	_, err := f.do(context.Background(), "rm", name, nil)
	return err
}

func (f *RemoteFS) MkdirAll(name string, perm os.FileMode) error {
	_, err := f.do(context.Background(), "mkdir", name, func(req *tooling.FSRequest) {
		req.Mode = uint32(perm)
	})
	return err
}

func (f *RemoteFS) Rename(oldname, newname string) error {
	_, err := f.do(context.Background(), "rename", oldname, func(req *tooling.FSRequest) {
		req.NewPath = newname
	})
	return err
}

// bytesReadSeekCloser adapts bytes.Reader to io.ReadSeekCloser.
type bytesReadSeekCloser struct {
	*bytes.Reader
}

func (b *bytesReadSeekCloser) Close() error { return nil }

// remoteDirEntry adapts relay FSDirItem to fs.DirEntry.
type remoteDirEntry struct {
	name  string
	isDir bool
	size  int64
}

func (e remoteDirEntry) Name() string               { return e.name }
func (e remoteDirEntry) IsDir() bool                { return e.isDir }
func (e remoteDirEntry) Type() fs.FileMode          { return 0 }
func (e remoteDirEntry) Info() (fs.FileInfo, error) { return remoteFileInfo{name: e.name, size: e.size, isDir: e.isDir}, nil }

// remoteFileInfo adapts relay FSFileInfo to os.FileInfo.
type remoteFileInfo struct {
	name    string
	size    int64
	isDir   bool
	modeStr string
}

func (i remoteFileInfo) Name() string       { return i.name }
func (i remoteFileInfo) Size() int64        { return i.size }
func (i remoteFileInfo) Mode() os.FileMode  { return 0 }
func (i remoteFileInfo) ModTime() time.Time { return time.Time{} }
func (i remoteFileInfo) IsDir() bool        { return i.isDir }
func (i remoteFileInfo) Sys() interface{}   { return nil }
