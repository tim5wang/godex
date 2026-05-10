package workspacepath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type candidate struct {
	path    string
	trimmed bool
}

// Resolve returns a normalized path rooted in the workspace.
// It accepts absolute and relative paths, prefers existing files when multiple
// candidates are possible, and tolerates accidental duplication of the
// workspace basename (for example "godex/skill/skill.go" inside the godex repo).
func Resolve(workspaceRoot, rawPath string) (string, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("missing path argument")
	}

	absWorkspaceRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", err
	}

	candidates, err := buildCandidates(absWorkspaceRoot, rawPath)
	if err != nil {
		return "", err
	}

	validated := make([]candidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, item := range candidates {
		absPath, err := filepath.Abs(item.path)
		if err != nil {
			continue
		}
		absPath = filepath.Clean(absPath)

		rel, err := filepath.Rel(absWorkspaceRoot, absPath)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			continue
		}
		if _, ok := seen[absPath]; ok {
			continue
		}
		seen[absPath] = struct{}{}
		validated = append(validated, candidate{
			path:    absPath,
			trimmed: item.trimmed,
		})
	}

	if len(validated) == 0 {
		return "", fmt.Errorf("path outside workspace: %s", rawPath)
	}

	for _, item := range validated {
		if pathExists(item.path) {
			return item.path, nil
		}
	}

	if shouldPreferTrimmedFallback(absWorkspaceRoot, rawPath) {
		for _, item := range validated {
			if item.trimmed {
				return item.path, nil
			}
		}
	}

	return validated[0].path, nil
}

func buildCandidates(workspaceRoot, rawPath string) ([]candidate, error) {
	cleaned := filepath.Clean(rawPath)
	candidates := make([]candidate, 0, 2)

	if filepath.IsAbs(rawPath) {
		candidates = append(candidates, candidate{path: cleaned})
	} else {
		candidates = append(candidates, candidate{path: filepath.Join(workspaceRoot, cleaned)})
	}

	relPath := cleaned
	if filepath.IsAbs(rawPath) {
		rel, err := filepath.Rel(workspaceRoot, cleaned)
		if err != nil {
			return candidates, nil
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return candidates, nil
		}
		relPath = rel
	}

	base := filepath.Base(workspaceRoot)
	if relPath == base || strings.HasPrefix(relPath, base+string(os.PathSeparator)) {
		trimmed := strings.TrimPrefix(relPath, base)
		trimmed = strings.TrimPrefix(trimmed, string(os.PathSeparator))
		if trimmed != "" && trimmed != "." {
			candidates = append(candidates, candidate{
				path:    filepath.Join(workspaceRoot, trimmed),
				trimmed: true,
			})
		}
	}

	return candidates, nil
}

func shouldPreferTrimmedFallback(workspaceRoot, rawPath string) bool {
	relPath, ok := workspaceRelativePath(workspaceRoot, rawPath)
	if !ok {
		return false
	}

	base := filepath.Base(workspaceRoot)
	if relPath != base && !strings.HasPrefix(relPath, base+string(os.PathSeparator)) {
		return false
	}

	firstSegment := relPath
	if idx := strings.IndexRune(relPath, os.PathSeparator); idx >= 0 {
		firstSegment = relPath[:idx]
	}

	info, err := os.Stat(filepath.Join(workspaceRoot, firstSegment))
	if os.IsNotExist(err) {
		return true
	}
	return err == nil && !info.IsDir()
}

func workspaceRelativePath(workspaceRoot, rawPath string) (string, bool) {
	cleaned := filepath.Clean(strings.TrimSpace(rawPath))
	if cleaned == "" {
		return "", false
	}

	if filepath.IsAbs(cleaned) {
		rel, err := filepath.Rel(workspaceRoot, cleaned)
		if err != nil {
			return "", false
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", false
		}
		return rel, true
	}

	return cleaned, true
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
