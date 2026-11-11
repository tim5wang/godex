package backend

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/sandbox"
)

// A snapshot contains only dirty paths, not a recursive workspace scan. Hashes
// distinguish edits from pre-existing dirty files with an unchanged Git status.
type ledgerWorkspaceSnapshot struct {
	root      string
	workspace string
	head      string
	files     map[string]string
	tempDir   string
}

func captureLedgerWorkspace(binding sandbox.ToolBinding) *ledgerWorkspaceSnapshot {
	if binding.WorkspaceDir == "" || (binding.Execution.Mode != "" && binding.Execution.Mode != tooling.ExecutionModeLocal) {
		return nil // Never inspect the host checkout for a remote execution.
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	root, err := ledgerGit(ctx, binding.WorkspaceDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil
	}
	workspace, err := filepath.Abs(binding.WorkspaceDir)
	if err != nil {
		return nil
	}
	originalWorkspace := workspace
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil
	}
	tempDir := binding.TempDir
	if tempDir != "" && filepath.IsAbs(tempDir) {
		if rel, err := filepath.Rel(originalWorkspace, tempDir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			tempDir = filepath.Join(workspace, rel)
		}
	}
	snapshot := &ledgerWorkspaceSnapshot{root: strings.TrimRight(string(root), "\r\n"), workspace: workspace, tempDir: tempDir}
	head, _ := ledgerGit(ctx, snapshot.root, "rev-parse", "--verify", "HEAD")
	snapshot.head = strings.TrimSpace(string(head))
	snapshot.files, err = snapshot.dirtyFiles(ctx)
	if err != nil {
		return nil
	}
	return snapshot
}

func ledgerWorkspaceChanges(before *ledgerWorkspaceSnapshot) ([]string, bool) {
	if before == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	after, err := before.dirtyFiles(ctx)
	if err != nil {
		return nil, false
	}
	candidates := make(map[string]struct{}, len(before.files)+len(after))
	for path := range before.files {
		candidates[path] = struct{}{}
	}
	for path := range after {
		candidates[path] = struct{}{}
	}
	// Include files committed during the turn: they no longer appear in status.
	var committed []byte
	if before.head != "" {
		committed, err = ledgerGit(ctx, before.root, "diff", "--name-only", "-z", "--no-renames", before.head, "HEAD", "--")
		if err != nil {
			return nil, false
		}
	} else if _, headErr := ledgerGit(ctx, before.root, "rev-parse", "--verify", "HEAD"); headErr == nil {
		committed, err = ledgerGit(ctx, before.root, "ls-tree", "-r", "--name-only", "-z", "HEAD")
		if err != nil {
			return nil, false
		}
	}
	for _, path := range strings.Split(string(committed), "\x00") {
		if path != "" {
			candidates[path] = struct{}{}
		}
	}
	var changed []string
	for path := range candidates {
		if before.temporary(path) {
			continue
		}
		current, ok := after[path]
		if !ok {
			current, err = ledgerPathFingerprint(filepath.Join(before.root, path))
			if err != nil {
				return nil, false
			}
		}
		if previous, existed := before.files[path]; !existed || previous != current {
			rel, err := filepath.Rel(before.workspace, filepath.Join(before.root, path))
			if err != nil {
				return nil, false
			}
			changed = append(changed, filepath.ToSlash(rel))
		}
	}
	sort.Strings(changed)
	return changed, true
}

func (s *ledgerWorkspaceSnapshot) dirtyFiles(ctx context.Context) (map[string]string, error) {
	data, err := ledgerGit(ctx, s.root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return nil, err
	}
	files := make(map[string]string)
	rows := strings.Split(string(data), "\x00")
	for i := 0; i < len(rows); i++ {
		row := rows[i]
		if len(row) < 4 {
			continue
		}
		paths := []string{row[3:]}
		if strings.ContainsAny(row[:2], "RC") && i+1 < len(rows) {
			i++
			paths = append(paths, rows[i])
		}
		for _, path := range paths {
			if s.temporary(path) {
				continue
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			fingerprint, err := ledgerPathFingerprint(filepath.Join(s.root, path))
			if err != nil {
				return nil, err
			}
			files[path] = fingerprint
		}
	}
	return files, nil
}

func (s *ledgerWorkspaceSnapshot) temporary(path string) bool {
	fullPath := filepath.Join(s.root, path)
	rel, err := filepath.Rel(s.workspace, fullPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	for _, dir := range []string{"tmp", "state", "memory", "logs", ".tmp", ".sessions", ".transcripts", ".team", ".tasks", ".todos"} {
		if strings.HasPrefix(filepath.ToSlash(rel), ".godex/"+dir+"/") {
			return true
		}
	}
	if s.tempDir != "" {
		temp := s.tempDir
		if !filepath.IsAbs(temp) {
			temp = filepath.Join(s.workspace, temp)
		}
		rel, err := filepath.Rel(temp, filepath.Join(s.root, path))
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return false
}

func ledgerPathFingerprint(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "deleted", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		return "link:" + target, err
	}
	if !info.Mode().IsRegular() {
		return fmt.Sprint(info.Mode()), nil
	}
	// Bound hashing work for large generated artifacts. Their filesystem
	// metadata still detects ordinary changes without reading gigabytes.
	if info.Size() > 4<<20 {
		return fmt.Sprintf("%s:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano()), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%x", info.Mode(), hash.Sum(nil)), nil
}

func ledgerGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	return cmd.Output()
}
