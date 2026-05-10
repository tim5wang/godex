package memory

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	maxProjectMinerAdds  = 8
	maxProjectMinerFiles = 24
	maxMinedDocBytes     = 128 * 1024
	maxMinedSummaryRunes = 160
	maxMinedContentRunes = 720
)

// CaptureProjectDocs mines high-signal project documents into memory
// candidates. Results still flow through the normal candidate inbox, dedupe,
// and suppression rules.
func (e *Extractor) CaptureProjectDocs(workspaceDir string) ([]Candidate, error) {
	if e == nil || e.manager == nil {
		return nil, nil
	}
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return nil, fmt.Errorf("missing workspace dir")
	}

	paths, err := discoverProjectMinerFiles(workspaceDir)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}

	candidates := make([]Candidate, 0, len(paths))
	for _, path := range paths {
		candidate, ok := mineProjectDocumentCandidate(workspaceDir, path)
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return e.captureCandidates(limitCandidates(dedupeCandidates(candidates), maxProjectMinerAdds))
}

func discoverProjectMinerFiles(workspaceDir string) ([]string, error) {
	files := make([]string, 0, maxProjectMinerFiles)

	readmes, err := filepath.Glob(filepath.Join(workspaceDir, "README*"))
	if err != nil {
		return nil, err
	}
	for _, path := range readmes {
		if shouldMineProjectPath(path) {
			files = append(files, path)
		}
	}

	rootAgents := filepath.Join(workspaceDir, "AGENTS.md")
	if info, err := os.Stat(rootAgents); err == nil && !info.IsDir() {
		files = append(files, rootAgents)
	}

	docsRoot := filepath.Join(workspaceDir, "docs")
	if info, err := os.Stat(docsRoot); err == nil && info.IsDir() {
		err = filepath.WalkDir(docsRoot, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if len(files) >= maxProjectMinerFiles {
				return fs.SkipAll
			}
			if shouldMineProjectPath(path) {
				files = append(files, path)
			}
			return nil
		})
		if err != nil && err != fs.SkipAll {
			return nil, err
		}
	}

	sort.Strings(files)
	if len(files) > maxProjectMinerFiles {
		files = files[:maxProjectMinerFiles]
	}
	return files, nil
}

func shouldMineProjectPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, "readme") {
		return true
	}
	if base == "agents.md" {
		return true
	}
	ext := strings.ToLower(filepath.Ext(base))
	return ext == ".md" || ext == ".mdx"
}

func mineProjectDocumentCandidate(workspaceDir, path string) (Candidate, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 || info.Size() > maxMinedDocBytes {
		return Candidate{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Candidate{}, false
	}
	title, summary, content := extractProjectDocFields(string(data))
	if title == "" || summary == "" || content == "" {
		return Candidate{}, false
	}

	relPath, err := filepath.Rel(workspaceDir, path)
	if err != nil {
		relPath = filepath.Base(path)
	}
	relPath = filepath.ToSlash(relPath)
	source := projectMinerSourceForPath(relPath)
	memoryType := projectMinerTypeForPath(relPath)
	content = fmt.Sprintf("Source document: %s\n%s", relPath, content)

	return newCandidate(title, summary, content, memoryType, source), true
}

func extractProjectDocFields(raw string) (title, summary, content string) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	cleaned := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence || trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") && title == "" {
			title = normalizeProjectDocLine(strings.TrimLeft(trimmed, "#"))
			continue
		}
		if strings.HasPrefix(trimmed, "---") || strings.HasPrefix(trimmed, "===") {
			continue
		}
		cleanedLine := normalizeProjectDocLine(trimmed)
		if cleanedLine == "" {
			continue
		}
		cleaned = append(cleaned, cleanedLine)
	}
	if title == "" && len(cleaned) > 0 {
		title = cleaned[0]
		cleaned = cleaned[1:]
	}
	if title == "" {
		return "", "", ""
	}
	if len(cleaned) == 0 {
		return "", "", ""
	}
	summary = truncateRunes(cleaned[0], maxMinedSummaryRunes)
	if summary == "" {
		return "", "", ""
	}
	maxLines := 6
	if len(cleaned) < maxLines {
		maxLines = len(cleaned)
	}
	content = truncateRunes(strings.Join(cleaned[:maxLines], "\n"), maxMinedContentRunes)
	return strings.TrimSpace(title), strings.TrimSpace(summary), strings.TrimSpace(content)
}

func normalizeProjectDocLine(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimLeftFunc(line, func(r rune) bool {
		switch {
		case r == '#', r == '-', r == '*', r == '>', r == '`':
			return true
		case unicode.IsDigit(r):
			return true
		case r == '.' || r == ')' || r == '[' || r == ']':
			return true
		default:
			return false
		}
	})
	line = strings.TrimSpace(line)
	line = strings.Join(strings.Fields(line), " ")
	return strings.TrimSpace(line)
}

func truncateRunes(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "..."
}

func projectMinerSourceForPath(relPath string) string {
	base := strings.ToLower(filepath.Base(relPath))
	switch {
	case strings.HasPrefix(base, "readme"):
		return "project-miner:readme"
	case base == "agents.md":
		return "project-miner:agents"
	default:
		return "project-miner:docs"
	}
}

func projectMinerTypeForPath(relPath string) Type {
	base := strings.ToLower(filepath.Base(relPath))
	if base == "agents.md" {
		return TypeWorkflow
	}
	switch {
	case strings.Contains(base, "runbook"),
		strings.Contains(base, "workflow"),
		strings.Contains(base, "guide"),
		strings.Contains(base, "playbook"),
		strings.Contains(base, "checklist"):
		return TypeWorkflow
	default:
		return TypeProject
	}
}
