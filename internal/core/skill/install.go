package skill

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/tim5wang/godex/internal/platform/tooling"
)

// InstallResult summarizes a skill installation into the local skills directory.
type InstallResult struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Status             string         `json:"status"`
	Source             string         `json:"source"`
	SourceOrigin       string         `json:"source_origin,omitempty"`
	Trust              string         `json:"trust,omitempty"`
	Version            string         `json:"version,omitempty"`
	Categories         []string       `json:"categories,omitempty"`
	InstalledPath      string         `json:"installed_path"`
	Description        string         `json:"description,omitempty"`
	Sections           []string       `json:"sections,omitempty"`
	RecommendedBundles []string       `json:"recommended_bundles,omitempty"`
	Compatibility      Compatibility  `json:"compatibility"`
	Warnings           []string       `json:"warnings,omitempty"`
	InstallMemory      *InstallMemory `json:"install_memory,omitempty"`
}

// RemoveResult summarizes deleting one installed skill from the local skills directory.
type RemoveResult struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	RemovedPath string `json:"removed_path"`
}

type installCandidate struct {
	root       string
	name       string
	sourcePath string
}

// Install copies a local or remote skill source into the skills directory and validates it.
// The optional requestedName can point at a specific skill inside a multi-skill repository.
func (l *Loader) Install(source, requestedName string) (InstallResult, error) {
	return l.InstallContext(context.Background(), source, requestedName)
}

// InstallContext installs a skill and cancels remote source preparation when ctx expires.
func (l *Loader) InstallContext(ctx context.Context, source, requestedName string) (InstallResult, error) {
	source = strings.TrimSpace(source)
	requestedName = strings.TrimSpace(requestedName)
	source, requestedName = normalizeInstallRequest(source, requestedName)
	if source == "" {
		return InstallResult{}, newSkillInvalidRequestError("missing source")
	}

	l.installMu.Lock()
	defer l.installMu.Unlock()

	if err := os.MkdirAll(l.skillsDir, 0755); err != nil {
		return InstallResult{}, fmt.Errorf("create skills dir: %w", err)
	}

	sourceRoot, cleanup, err := l.prepareInstallSource(ctx, source)
	if err != nil {
		return InstallResult{}, err
	}
	defer cleanup()
	installMemory := buildInstallMemoryForLoader(l, source, requestedName, "")

	candidate, err := locateInstallCandidate(sourceRoot, requestedName)
	if err != nil {
		return InstallResult{}, err
	}
	targetName := sanitizeSkillDirName(candidate.name)
	if targetName == "" {
		return InstallResult{}, newSkillInvalidRequestError("could not determine installed skill name")
	}
	installMemory = buildInstallMemoryForLoader(l, source, requestedName, targetName)

	targetDir := filepath.Join(l.skillsDir, targetName)
	if _, err := os.Stat(targetDir); err == nil {
		return InstallResult{}, newSkillConflictError("skill %q already exists", targetName)
	} else if !os.IsNotExist(err) {
		return InstallResult{}, fmt.Errorf("stat target skill %q: %w", targetName, err)
	}

	stagingRoot, err := os.MkdirTemp(l.skillsDir, ".skill-install-*")
	if err != nil {
		return InstallResult{}, fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stagingRoot)

	stagingTarget := filepath.Join(stagingRoot, targetName)
	if err := copyInstallTree(candidate.root, stagingTarget); err != nil {
		return InstallResult{}, err
	}

	stageLoader := NewLoader(stagingRoot)
	stageLoader.SetFallbackNormalizer(l.fallbackNormalizer)
	if _, err := stageLoader.loadUncached(targetName, true); err != nil {
		return InstallResult{}, fmt.Errorf("validate installed skill %q: %w", targetName, err)
	}
	if err := writeInstallMemory(stagingTarget, installMemory); err != nil {
		return InstallResult{}, fmt.Errorf("write install metadata for %q: %w", targetName, err)
	}

	if err := os.Rename(stagingTarget, targetDir); err != nil {
		return InstallResult{}, fmt.Errorf("finalize skill install %q: %w", targetName, err)
	}

	l.mu.Lock()
	delete(l.skills, targetName)
	l.mu.Unlock()

	installed, err := l.loadUncached(targetName, true)
	if err != nil {
		return InstallResult{}, err
	}

	l.mu.Lock()
	l.skills[targetName] = installed
	l.mu.Unlock()

	memory := cloneInstallMemory(installed.InstallMemory)
	sourceOrigin := ""
	trust := ""
	if memory != nil {
		sourceOrigin = strings.TrimSpace(memory.SourceOrigin)
		trust = strings.TrimSpace(memory.Trust)
	}
	return InstallResult{
		ID:                 targetName,
		Name:               installed.Name,
		Status:             "installed",
		Source:             source,
		SourceOrigin:       sourceOrigin,
		Trust:              trust,
		Version:            strings.TrimSpace(installed.Version),
		Categories:         append([]string{}, installed.Categories...),
		InstalledPath:      targetDir,
		Description:        installed.Description,
		Sections:           append([]string{}, installed.SectionOrder...),
		RecommendedBundles: append([]string{}, installed.RecommendedBundles...),
		Compatibility:      installed.Compatibility,
		Warnings:           append([]string{}, installed.Warnings...),
		InstallMemory:      memory,
	}, nil
}

// Remove deletes one installed skill directory from the local skills directory.
func (l *Loader) Remove(name string) (RemoveResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return RemoveResult{}, newSkillInvalidRequestError("missing skill name")
	}

	l.installMu.Lock()
	defer l.installMu.Unlock()

	skillID, err := l.ResolveID(name)
	if err != nil {
		return RemoveResult{}, err
	}
	skillPath, err := ResolvePath(l.skillsDir, skillID)
	if err != nil {
		return RemoveResult{}, err
	}
	root := filepath.Dir(skillPath)
	cleanSkillsDir, err := filepath.Abs(l.skillsDir)
	if err != nil {
		return RemoveResult{}, fmt.Errorf("resolve skills dir: %w", err)
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return RemoveResult{}, fmt.Errorf("resolve skill dir: %w", err)
	}
	if filepath.Dir(cleanRoot) != cleanSkillsDir {
		return RemoveResult{}, newSkillInvalidRequestError("refusing to remove skill outside skills directory: %s", skillID)
	}

	displayName := skillID
	if parsed, loadErr := l.loadUncached(skillID, false); loadErr == nil && strings.TrimSpace(parsed.Name) != "" {
		displayName = strings.TrimSpace(parsed.Name)
	}
	if err := os.RemoveAll(cleanRoot); err != nil {
		return RemoveResult{}, fmt.Errorf("remove skill %q: %w", skillID, err)
	}

	l.mu.Lock()
	delete(l.skills, skillID)
	delete(l.normalizedCache, skillPath)
	l.mu.Unlock()

	return RemoveResult{
		ID:          skillID,
		Name:        displayName,
		Status:      "removed",
		RemovedPath: cleanRoot,
	}, nil
}

func buildInstallMemoryForLoader(l *Loader, source, requestedName, targetName string) *InstallMemory {
	workspaceDir := filepath.Dir(l.skillsDir)
	items, err := SourceCatalog(workspaceDir, l.skillsDir)
	if err != nil {
		items = nil
	}
	return buildInstallMemory(items, source, requestedName, targetName, time.Now())
}

func (l *Loader) prepareInstallSource(ctx context.Context, source string) (string, func(), error) {
	if info, err := os.Stat(source); err == nil {
		if info.IsDir() {
			abs, absErr := filepath.Abs(source)
			if absErr != nil {
				return "", func() {}, fmt.Errorf("resolve local source path: %w", absErr)
			}
			return abs, func() {}, nil
		}
		if filepath.Base(source) == "SKILL.md" || strings.HasSuffix(strings.ToLower(source), ".md") {
			abs, absErr := filepath.Abs(source)
			if absErr != nil {
				return "", func() {}, fmt.Errorf("resolve local source path: %w", absErr)
			}
			return abs, func() {}, nil
		}
		return "", func() {}, newSkillInvalidRequestError("unsupported local source: %s", source)
	} else if !os.IsNotExist(err) {
		return "", func() {}, fmt.Errorf("stat source: %w", err)
	}

	cloneURL, err := normalizeGitSource(source)
	if err != nil {
		return "", func() {}, err
	}

	tmpRoot, err := os.MkdirTemp("", "godex-skill-source-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temporary source dir: %w", err)
	}
	repoDir := filepath.Join(tmpRoot, "repo")
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", cloneURL, repoDir)
	if err := tooling.ConfigureCommandProcessGroup(cmd); err != nil {
		_ = os.RemoveAll(tmpRoot)
		return "", func() {}, fmt.Errorf("configure skill clone process: %w", err)
	}
	cmd.Cancel = func() error {
		tooling.KillCommandProcessGroup(cmd)
		return nil
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(tmpRoot)
		return "", func() {}, fmt.Errorf("clone skill source %q: %v (%s)", source, err, strings.TrimSpace(string(output)))
	}
	return repoDir, func() { _ = os.RemoveAll(tmpRoot) }, nil
}

func normalizeInstallRequest(source, requestedName string) (string, string) {
	source = strings.TrimSpace(source)
	requestedName = strings.TrimSpace(requestedName)
	if strings.HasPrefix(source, "skillsh:") {
		source = strings.TrimPrefix(source, "skillsh:")
	}
	if source == "" {
		return source, requestedName
	}
	if filepath.IsAbs(source) || strings.HasPrefix(source, ".") {
		return source, requestedName
	}
	if _, err := os.Stat(source); err == nil || !os.IsNotExist(err) {
		return source, requestedName
	}

	if parsed, err := url.Parse(source); err == nil && parsed.Host == "github.com" {
		parts := splitInstallSourcePath(parsed.Path)
		if len(parts) == 3 {
			parsed.Path = "/" + strings.Join(parts[:2], "/")
			parsed.RawPath = ""
			parsed.RawQuery = ""
			parsed.Fragment = ""
			if requestedName == "" {
				requestedName = parts[2]
			}
			return parsed.String(), requestedName
		}
	}

	trimmed := strings.Trim(source, "/")
	if strings.HasPrefix(trimmed, "github.com/") {
		parts := splitInstallSourcePath(strings.TrimPrefix(trimmed, "github.com/"))
		if len(parts) == 3 {
			if requestedName == "" {
				requestedName = parts[2]
			}
			return "github.com/" + strings.Join(parts[:2], "/"), requestedName
		}
	}

	parts := splitInstallSourcePath(trimmed)
	if len(parts) == 3 && !strings.Contains(parts[0], ".") {
		if requestedName == "" {
			requestedName = parts[2]
		}
		return strings.Join(parts[:2], "/"), requestedName
	}
	return source, requestedName
}

func splitInstallSourcePath(value string) []string {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func normalizeGitSource(source string) (string, error) {
	if strings.HasPrefix(source, "git@") || strings.HasPrefix(source, "ssh://") {
		return source, nil
	}
	if strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://") {
		parsed, err := url.Parse(source)
		if err != nil {
			return "", newSkillInvalidRequestError("parse git source: %v", err)
		}
		if parsed.Host == "" {
			return "", newSkillInvalidRequestError("invalid git source: %s", source)
		}
		return source, nil
	}
	if strings.HasPrefix(source, "github.com/") {
		return "https://" + source, nil
	}
	parts := strings.Split(strings.Trim(source, "/"), "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.Contains(parts[0], ".") {
		return "https://github.com/" + strings.Trim(source, "/"), nil
	}
	return "", newSkillInvalidRequestError("unsupported skill source %q; use a local path, GitHub repo URL, or owner/repo", source)
}

func supportsInstallSource(source string) bool {
	source = strings.TrimSpace(source)
	if source == "" {
		return false
	}
	if info, err := os.Stat(source); err == nil {
		if info.IsDir() {
			return true
		}
		base := filepath.Base(source)
		return filepath.Base(base) == "SKILL.md" || strings.HasSuffix(strings.ToLower(source), ".md")
	}
	_, err := normalizeGitSource(source)
	return err == nil
}

func locateInstallCandidate(sourceRoot, requestedName string) (installCandidate, error) {
	if requestedName != "" {
		if candidate, ok := findNamedInstallCandidate(sourceRoot, requestedName); ok {
			return candidate, nil
		}
		if rootSkill := filepath.Join(sourceRoot, "SKILL.md"); fileExists(rootSkill) {
			return installCandidate{
				root:       sourceRoot,
				name:       requestedName,
				sourcePath: rootSkill,
			}, nil
		}
		return installCandidate{}, newSkillNotFoundError(requestedName)
	}

	candidates, err := discoverInstallCandidates(sourceRoot)
	if err != nil {
		return installCandidate{}, err
	}
	if len(candidates) == 0 {
		return installCandidate{}, newSkillInvalidRequestError("no SKILL.md found in source")
	}
	if len(candidates) > 1 {
		names := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			names = append(names, candidate.name)
		}
		sort.Strings(names)
		return installCandidate{}, newSkillInvalidRequestError("source contains multiple skills (%s); provide a skill name", strings.Join(names, ", "))
	}
	return candidates[0], nil
}

func discoverInstallCandidates(sourceRoot string) ([]installCandidate, error) {
	seen := make(map[string]struct{})
	items := make([]installCandidate, 0, 4)
	appendCandidate := func(root, name, sourcePath string) {
		key := filepath.Clean(root)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		items = append(items, installCandidate{root: root, name: name, sourcePath: sourcePath})
	}

	if strings.HasSuffix(strings.ToLower(sourceRoot), ".md") {
		appendCandidate(sourceRoot, strings.TrimSuffix(filepath.Base(sourceRoot), filepath.Ext(sourceRoot)), sourceRoot)
		return items, nil
	}

	rootSkill := filepath.Join(sourceRoot, "SKILL.md")
	if fileExists(rootSkill) {
		appendCandidate(sourceRoot, filepath.Base(sourceRoot), rootSkill)
	}

	parents, err := installCandidateParents(sourceRoot)
	if err != nil {
		return nil, err
	}
	for _, parent := range parents {
		entries, err := os.ReadDir(parent)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read install source %s: %w", parent, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !entry.IsDir() {
				if parent != sourceRoot && strings.HasSuffix(strings.ToLower(name), ".md") {
					appendCandidate(filepath.Join(parent, name), strings.TrimSuffix(name, filepath.Ext(name)), filepath.Join(parent, name))
				}
				continue
			}
			if strings.HasPrefix(name, ".") {
				continue
			}
			skillFile := filepath.Join(parent, name, "SKILL.md")
			if fileExists(skillFile) {
				appendCandidate(filepath.Join(parent, name), name, skillFile)
			}
		}
	}
	return items, nil
}

func installCandidateParents(sourceRoot string) ([]string, error) {
	parents := []string{sourceRoot, filepath.Join(sourceRoot, "skills"), filepath.Join(sourceRoot, "examples", "skills"), filepath.Join(sourceRoot, ".claude", "skills")}
	err := filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		name := entry.Name()
		switch name {
		case ".git", "node_modules", "vendor":
			if path != sourceRoot {
				return filepath.SkipDir
			}
		}
		if name != "skills" || filepath.Base(filepath.Dir(path)) != ".claude" {
			return nil
		}
		parents = append(parents, path)
		return filepath.SkipDir
	})
	if err != nil {
		return nil, fmt.Errorf("scan install source %s: %w", sourceRoot, err)
	}
	return uniqueInstallCandidateParents(parents), nil
}

func uniqueInstallCandidateParents(parents []string) []string {
	out := make([]string, 0, len(parents))
	seen := make(map[string]struct{}, len(parents))
	for _, parent := range parents {
		key := filepath.Clean(parent)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, parent)
	}
	return out
}

func findNamedInstallCandidate(sourceRoot, requestedName string) (installCandidate, bool) {
	normalized := sanitizeSkillDirName(requestedName)
	candidates := []struct {
		root string
		name string
	}{
		{root: filepath.Join(sourceRoot, ".claude", "skills", requestedName), name: requestedName},
		{root: filepath.Join(sourceRoot, ".claude", "skills", normalized), name: normalized},
		{root: filepath.Join(sourceRoot, "skills", requestedName), name: requestedName},
		{root: filepath.Join(sourceRoot, "skills", normalized), name: normalized},
		{root: filepath.Join(sourceRoot, "examples", "skills", requestedName), name: requestedName},
		{root: filepath.Join(sourceRoot, "examples", "skills", normalized), name: normalized},
		{root: filepath.Join(sourceRoot, "skills", requestedName+".md"), name: requestedName},
		{root: filepath.Join(sourceRoot, "skills", normalized+".md"), name: normalized},
		{root: filepath.Join(sourceRoot, "examples", "skills", requestedName+".md"), name: requestedName},
		{root: filepath.Join(sourceRoot, "examples", "skills", normalized+".md"), name: normalized},
		{root: filepath.Join(sourceRoot, requestedName), name: requestedName},
		{root: filepath.Join(sourceRoot, normalized), name: normalized},
		{root: filepath.Join(sourceRoot, requestedName+".md"), name: requestedName},
		{root: filepath.Join(sourceRoot, normalized+".md"), name: normalized},
	}
	for _, candidate := range candidates {
		skillFile := candidate.root
		if !strings.HasSuffix(strings.ToLower(skillFile), ".md") {
			skillFile = filepath.Join(candidate.root, "SKILL.md")
		}
		if fileExists(skillFile) {
			name := requestedName
			if name == "" {
				name = candidate.name
			}
			return installCandidate{
				root:       candidate.root,
				name:       name,
				sourcePath: skillFile,
			}, true
		}
	}

	discovered, err := discoverInstallCandidates(sourceRoot)
	if err != nil {
		return installCandidate{}, false
	}
	for _, candidate := range discovered {
		if !skillFileMatchesRequestedName(candidate.sourcePath, requestedName) {
			continue
		}
		candidate.name = requestedName
		return candidate, true
	}
	return installCandidate{}, false
}

func skillFileMatchesRequestedName(pathValue, requestedName string) bool {
	raw, err := os.ReadFile(pathValue)
	if err != nil {
		return false
	}
	frontmatter, _, _, err := splitFrontmatter(string(raw))
	if err != nil {
		return false
	}
	name := stringValue(frontmatter["name"])
	return skillNamesMatch(requestedName, name)
}

func skillNamesMatch(requestedName, candidateName string) bool {
	requestedName = strings.TrimSpace(requestedName)
	candidateName = strings.TrimSpace(candidateName)
	if requestedName == "" || candidateName == "" {
		return false
	}
	if strings.EqualFold(requestedName, candidateName) {
		return true
	}
	requestedID := sanitizeSkillDirName(requestedName)
	candidateID := sanitizeSkillDirName(candidateName)
	return requestedID != "" && requestedID == candidateID
}

func copyInstallTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat skill source: %w", err)
	}
	if !info.IsDir() {
		return copyInstallFile(src, filepath.Join(dst, "SKILL.md"))
	}
	sourceRoot, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("resolve skill source: %w", err)
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0755)
		}
		if strings.HasPrefix(rel, ".git") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.Type()&os.ModeSymlink != 0 {
			return copyInstallSymlink(sourceRoot, path, target)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyInstallFile(path, target)
	})
}

func copyInstallSymlink(sourceRoot, src, dst string) error {
	linkTarget, err := os.Readlink(src)
	if err != nil {
		return fmt.Errorf("read skill source symlink: %w", err)
	}
	if filepath.IsAbs(linkTarget) {
		return newSkillInvalidRequestError("refusing to install absolute symlink %s -> %s", src, linkTarget)
	}
	resolvedTarget := filepath.Clean(filepath.Join(filepath.Dir(src), linkTarget))
	relTarget, err := filepath.Rel(sourceRoot, resolvedTarget)
	if err != nil {
		return fmt.Errorf("resolve skill source symlink %s: %w", src, err)
	}
	if relTarget == ".." || strings.HasPrefix(relTarget, ".."+string(os.PathSeparator)) || filepath.IsAbs(relTarget) {
		return newSkillInvalidRequestError("refusing to install symlink outside skill source %s -> %s", src, linkTarget)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.Symlink(linkTarget, dst)
}

func copyInstallFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func sanitizeSkillDirName(value string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	result := strings.Trim(b.String(), "-")
	return result
}
