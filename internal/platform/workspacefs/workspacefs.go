package workspacefs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FS is a narrow file boundary rooted at one workspace. Production uses
// os.Root so symlink and traversal escapes are rejected by the operating
// system path walk instead of by string cleanup alone.
type FS struct {
	dir  string
	root *os.Root
}

func New(workspace string) (*FS, error) {
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
	return &FS{dir: filepath.Clean(abs), root: root}, nil
}

func (f *FS) Close() error {
	if f == nil || f.root == nil {
		return nil
	}
	return f.root.Close()
}

func (f *FS) Dir() string {
	if f == nil {
		return ""
	}
	return f.dir
}

func (f *FS) Abs(name string) (string, error) {
	rel, err := f.resolve(name)
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

func (f *FS) ReadFile(name string) ([]byte, error) {
	rel, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	data, err := f.root.ReadFile(rel)
	if err != nil {
		return nil, f.wrapEscape(name, err)
	}
	return data, nil
}

func (f *FS) WriteFile(name string, data []byte, perm os.FileMode) error {
	rel, err := f.resolve(name)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := f.root.MkdirAll(dir, 0755); err != nil {
			return f.wrapEscape(name, err)
		}
	}
	file, err := f.root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return f.wrapEscape(name, err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (f *FS) Open(name string) (*os.File, error) {
	rel, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	file, err := f.root.Open(rel)
	if err != nil {
		return nil, f.wrapEscape(name, err)
	}
	return file, nil
}

func (f *FS) Stat(name string) (os.FileInfo, error) {
	rel, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	info, err := f.root.Stat(rel)
	if err != nil {
		return nil, f.wrapEscape(name, err)
	}
	return info, nil
}

func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	rel, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	file, err := f.root.Open(rel)
	if err != nil {
		return nil, f.wrapEscape(name, err)
	}
	defer file.Close()
	items, err := file.ReadDir(-1)
	if err != nil {
		return nil, f.wrapEscape(name, err)
	}
	return items, nil
}

func (f *FS) resolve(name string) (string, error) {
	if f == nil || f.root == nil {
		return "", fmt.Errorf("workspace fs unavailable")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("missing path")
	}
	var rel string
	if filepath.IsAbs(name) {
		var err error
		rel, err = filepath.Rel(f.dir, filepath.Clean(name))
		if err != nil {
			return "", err
		}
	} else {
		rel = filepath.Clean(name)
	}
	rel = f.trimDuplicatedWorkspaceBase(rel)
	if rel == "." {
		return rel, nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes workspace (outside workspace): %s", name)
	}
	return rel, nil
}

func (f *FS) trimDuplicatedWorkspaceBase(rel string) string {
	base := filepath.Base(f.dir)
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

func (f *FS) wrapEscape(name string, err error) error {
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "escape") || strings.Contains(text, "outside") || strings.Contains(text, "invalid argument") {
		return fmt.Errorf("path escapes workspace (outside workspace): %s", name)
	}
	return err
}

func inside(root, target string) (bool, error) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false, err
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel), nil
}
