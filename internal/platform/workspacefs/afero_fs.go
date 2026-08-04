package workspacefs

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
)

// aferoFS wraps an afero.Fs and implements the workspacefs.FS interface.
// The afero.Fs must already be rooted at the desired workspace (e.g. via
// afero.NewBasePathFs).
type aferoFS struct {
	dir    string
	backend afero.Fs
}

func newAferoFS(cfg Config) (FS, error) {
	if cfg.AferoFs == nil {
		return nil, fmt.Errorf("workspacefs: afero backend requires a non-nil AferoFs")
	}
	backend, ok := cfg.AferoFs.(afero.Fs)
	if !ok {
		return nil, fmt.Errorf("workspacefs: AferoFs must implement afero.Fs")
	}
	dir := filepath.Clean(strings.TrimSpace(cfg.WorkspaceDir))
	if dir == "" {
		dir = "."
	}
	return &aferoFS{dir: dir, backend: backend}, nil
}

func (f *aferoFS) Close() error { return nil }

func (f *aferoFS) Dir() string { return f.dir }

func (f *aferoFS) Abs(name string) (string, error) {
	// For remote/afero backends we can't resolve a real host absolute path,
	// so return the joined workspace-relative path.
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("missing path")
	}
	if filepath.IsAbs(name) {
		return filepath.Clean(name), nil
	}
	return filepath.Join(f.dir, filepath.Clean(name)), nil
}

func (f *aferoFS) ReadFile(name string) ([]byte, error) {
	if err := f.checkPath(name); err != nil {
		return nil, err
	}
	return afero.ReadFile(f.backend, name)
}

func (f *aferoFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	if err := f.checkPath(name); err != nil {
		return err
	}
	dir := filepath.Dir(name)
	if dir != "." {
		if err := f.backend.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return afero.WriteFile(f.backend, name, data, perm)
}

func (f *aferoFS) Open(name string) (io.ReadSeekCloser, error) {
	if err := f.checkPath(name); err != nil {
		return nil, err
	}
	file, err := f.backend.Open(name)
	if err != nil {
		return nil, err
	}
	return &aferoFileWrapper{File: file}, nil
}

func (f *aferoFS) Stat(name string) (os.FileInfo, error) {
	if err := f.checkPath(name); err != nil {
		return nil, err
	}
	return f.backend.Stat(name)
}

func (f *aferoFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := f.checkPath(name); err != nil {
		return nil, err
	}
	file, err := f.backend.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	infos, err := file.Readdir(-1)
	if err != nil {
		return nil, err
	}
	entries := make([]fs.DirEntry, 0, len(infos))
	for _, info := range infos {
		entries = append(entries, fs.FileInfoToDirEntry(info))
	}
	return entries, nil
}

func (f *aferoFS) RemoveAll(name string) error {
	if err := f.checkPath(name); err != nil {
		return err
	}
	return f.backend.RemoveAll(name)
}

func (f *aferoFS) MkdirAll(name string, perm os.FileMode) error {
	if err := f.checkPath(name); err != nil {
		return err
	}
	return f.backend.MkdirAll(name, perm)
}

func (f *aferoFS) Rename(oldname, newname string) error {
	if err := f.checkPath(oldname); err != nil {
		return err
	}
	if err := f.checkPath(newname); err != nil {
		return err
	}
	return f.backend.Rename(oldname, newname)
}

func (f *aferoFS) checkPath(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("missing path")
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes workspace (outside workspace): %s", name)
	}
	return nil
}

// aferoFileWrapper adapts afero.File to io.ReadSeekCloser.
type aferoFileWrapper struct {
	afero.File
}

func (w *aferoFileWrapper) Seek(offset int64, whence int) (int64, error) {
	s, ok := w.File.(io.Seeker)
	if !ok {
		return 0, fmt.Errorf("seek not supported by this file")
	}
	return s.Seek(offset, whence)
}

func (w *aferoFileWrapper) Read(p []byte) (int, error) {
	return w.File.Read(p)
}

func (w *aferoFileWrapper) Close() error {
	return w.File.Close()
}
