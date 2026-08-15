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

// osFS is the local-OS backed FS using os.Root for sandboxing.
type osFS struct {
	dir       string
	root      *os.Root
	allowlist []string
}

func newOSFS(workspace string, allowlist []string) (FS, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, fmt.Errorf("missing workspace")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open workspace root %s: %w", abs, err)
	}
	allow, err := mergeAllowlists(allowlist)
	if err != nil {
		root.Close()
		return nil, err
	}
	return &osFS{dir: filepath.Clean(abs), root: root, allowlist: allow}, nil
}

func (f *osFS) Close() error {
	if f == nil || f.root == nil {
		return nil
	}
	return f.root.Close()
}

func (f *osFS) Dir() string {
	if f == nil {
		return ""
	}
	return f.dir
}

func (f *osFS) Abs(name string) (string, error) {
	rel, err := f.resolve(name)
	if errors.Is(err, errAllowedExternal) {
		return rel, nil
	}
	if err != nil {
		return "", err
	}
	abs := filepath.Join(f.dir, rel)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		root := f.dir
		if resolvedRoot, err := filepath.EvalSymlinks(f.dir); err == nil {
			root = resolvedRoot
		}
		inside, relErr := inside(root, resolved)
		if relErr != nil {
			return "", relErr
		}
		if !inside {
			return "", fmt.Errorf("path escapes workspace (outside workspace): %s", name)
		}
	}
	return abs, nil
}

func (f *osFS) ReadFile(name string) ([]byte, error) {
	rel, err := f.resolve(name)
	if errors.Is(err, errAllowedExternal) {
		return os.ReadFile(rel)
	}
	if err != nil {
		return nil, err
	}
	data, err := f.root.ReadFile(rel)
	if err != nil {
		return nil, wrapEscape(name, err)
	}
	return data, nil
}

func (f *osFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	rel, err := f.resolve(name)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := f.root.MkdirAll(dir, 0755); err != nil {
			return wrapEscape(name, err)
		}
	}
	file, err := f.root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return wrapEscape(name, err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (f *osFS) Open(name string) (io.ReadSeekCloser, error) {
	rel, err := f.resolve(name)
	if errors.Is(err, errAllowedExternal) {
		return os.Open(rel)
	}
	if err != nil {
		return nil, err
	}
	file, err := f.root.Open(rel)
	if err != nil {
		return nil, wrapEscape(name, err)
	}
	return file, nil
}

func (f *osFS) Stat(name string) (os.FileInfo, error) {
	rel, err := f.resolve(name)
	if errors.Is(err, errAllowedExternal) {
		return os.Stat(rel)
	}
	if err != nil {
		return nil, err
	}
	info, err := f.root.Stat(rel)
	if err != nil {
		return nil, wrapEscape(name, err)
	}
	return info, nil
}

func (f *osFS) ReadDir(name string) ([]fs.DirEntry, error) {
	rel, err := f.resolve(name)
	if errors.Is(err, errAllowedExternal) {
		return os.ReadDir(rel)
	}
	if err != nil {
		return nil, err
	}
	file, err := f.root.Open(rel)
	if err != nil {
		return nil, wrapEscape(name, err)
	}
	defer file.Close()
	items, err := file.ReadDir(-1)
	if err != nil {
		return nil, wrapEscape(name, err)
	}
	return items, nil
}

func (f *osFS) RemoveAll(name string) error {
	rel, err := f.resolve(name)
	if err != nil {
		return err
	}
	return f.root.RemoveAll(rel)
}

func (f *osFS) MkdirAll(name string, perm os.FileMode) error {
	rel, err := f.resolve(name)
	if err != nil {
		return err
	}
	return f.root.MkdirAll(rel, perm)
}

func (f *osFS) Rename(oldname, newname string) error {
	relOld, err := f.resolve(oldname)
	if err != nil {
		return err
	}
	relNew, err := f.resolve(newname)
	if err != nil {
		return err
	}
	return f.root.Rename(relOld, relNew)
}

func (f *osFS) resolve(name string) (string, error) {
	if f == nil || f.root == nil {
		return "", fmt.Errorf("workspace fs unavailable")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("missing path")
	}

	abs := name
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(f.dir, abs)
	}
	abs = filepath.Clean(abs)
	if isInAllowlist(abs, f.allowlist) {
		return abs, errAllowedExternal
	}

	var rel string
	if filepath.IsAbs(name) {
		var err error
		rel, err = filepath.Rel(f.dir, abs)
		if err != nil {
			return "", err
		}
	} else {
		rel = filepath.Clean(name)
	}
	rel = trimDuplicatedWorkspaceBase(f.dir, rel)
	if rel == "." {
		return rel, nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes workspace (outside workspace): %s", name)
	}
	return rel, nil
}

func trimDuplicatedWorkspaceBase(dir, rel string) string {
	base := filepath.Base(dir)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return rel
	}
	if rel == base {
		return "."
	}
	prefix := base + string(filepath.Separator)
	if strings.HasPrefix(rel, prefix) {
		return strings.TrimPrefix(rel, prefix)
	}
	return rel
}
