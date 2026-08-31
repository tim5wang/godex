package architecture_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const frontendDefaultLineLimit = 1000

// Existing oversized files are migration exceptions, capped at their current
// size. Do not raise these limits: split the relevant feature slice instead.
// Once a file is at or below frontendDefaultLineLimit, the test requires its
// exception to be removed.
var frontendLineExceptions = map[string]int{
	"ui/web/src/features/chat/ChatPage.tsx":           1919,
	"ui/web/src/features/taskboard/TaskBoardPage.tsx": 1457,
	"ui/web/src/i18n/messages.ts":                     2871,
	"ui/web/src/styles.css":                           3251,
}

func TestFrontendSourceLineBudget(t *testing.T) {
	repoRoot := architectureRepoRoot(t)
	sourceRoot := filepath.Join(repoRoot, "ui", "web", "src")
	seenExceptions := make(map[string]bool, len(frontendLineExceptions))
	var violations []string

	err := filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !isFrontendSource(path) {
			return nil
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		limit := frontendDefaultLineLimit
		if exceptionLimit, ok := frontendLineExceptions[rel]; ok {
			seenExceptions[rel] = true
			limit = exceptionLimit
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := sourceLineCount(content)
		if lines > limit {
			violations = append(violations, fmt.Sprintf("%s: %d lines (limit %d)", rel, lines, limit))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan frontend source: %v", err)
	}

	for path := range frontendLineExceptions {
		if !seenExceptions[path] {
			violations = append(violations, path+": remove missing file exception")
			continue
		}
		content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read frontend exception %s: %v", path, err)
		}
		if lines := sourceLineCount(content); lines <= frontendDefaultLineLimit {
			violations = append(violations, fmt.Sprintf("%s: remove resolved exception (%d lines)", path, lines))
		}
	}

	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("frontend source line budget violations:\n%s", strings.Join(violations, "\n"))
	}
}

func architectureRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func isFrontendSource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".css", ".js", ".jsx", ".ts", ".tsx":
		return true
	default:
		return false
	}
}

func sourceLineCount(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	lines := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		lines++
	}
	return lines
}
