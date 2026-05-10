package skill

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/tim5wang/godex/internal/platform/stringutil"
	"github.com/tim5wang/godex/internal/platform/textutil"
	"gopkg.in/yaml.v3"
)

// CompatibilityStatus describes how safely a skill can run in the current runtime.
type CompatibilityStatus string

const (
	CompatibilityNativeSupported   CompatibilityStatus = "native_supported"
	CompatibilityDegradedSupported CompatibilityStatus = "degraded_supported"
	CompatibilityUnsupported       CompatibilityStatus = "unsupported"
)

// Compatibility summarizes runtime support expectations for a skill.
type Compatibility struct {
	Status              CompatibilityStatus `json:"status"`
	MissingCapabilities []string            `json:"missing_capabilities,omitempty"`
	MissingDependencies []string            `json:"missing_dependencies,omitempty"`
	Notes               []string            `json:"notes,omitempty"`
}

// Requirements captures runtime capabilities that a skill appears to depend on.
type Requirements struct {
	MCP                 bool     `json:"mcp,omitempty"`
	NamedSubagents      bool     `json:"named_subagents,omitempty"`
	SlashCommandRuntime bool     `json:"slash_command_runtime,omitempty"`
	AllowedTools        []string `json:"allowed_tools,omitempty"`
	Executables         []string `json:"executables,omitempty"`
	ContextFork         bool     `json:"context_fork,omitempty"`
	Hooks               bool     `json:"hooks,omitempty"`
	HookNames           []string `json:"hook_names,omitempty"`
	ShellHints          []string `json:"shell_hints,omitempty"`
	OSHints             []string `json:"os_hints,omitempty"`
}

// CatalogEntry is the lightweight metadata exposed before a skill is activated.
type CatalogEntry struct {
	ID                  string         `json:"id"`
	Name                string         `json:"name"`
	Description         string         `json:"description"`
	WhenToUse           []string       `json:"when_to_use,omitempty"`
	ArgumentHint        string         `json:"argument_hint,omitempty"`
	Version             string         `json:"version,omitempty"`
	Categories          []string       `json:"categories,omitempty"`
	Paths               []string       `json:"paths,omitempty"`
	RecommendedBundles  []string       `json:"recommended_bundles,omitempty"`
	Sections            []string       `json:"sections,omitempty"`
	Requires            Requirements   `json:"requires,omitempty"`
	Compatibility       Compatibility  `json:"compatibility"`
	Warnings            []string       `json:"warnings,omitempty"`
	Path                string         `json:"path,omitempty"`
	InstallMemory       *InstallMemory `json:"install_memory,omitempty"`
	NormalizationStatus string         `json:"normalization_status,omitempty"`
	NormalizationSource string         `json:"normalization_source,omitempty"`
	Normalized          bool           `json:"normalized,omitempty"`
	NeedsNormalization  bool           `json:"needs_normalization,omitempty"`
	CanNormalize        bool           `json:"can_normalize,omitempty"`
	SkillKind           string         `json:"skill_kind,omitempty"`
	SuiteID             string         `json:"suite_id,omitempty"`
	ChildSkillCount     int            `json:"child_skill_count,omitempty"`
	ChildSkillIDs       []string       `json:"child_skill_ids,omitempty"`
	ChildSkillHint      string         `json:"child_skill_hint,omitempty"`
}

// Section is one named portion of a skill body.
type Section struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// Skill represents a loaded skill.
type Skill struct {
	ID                  string
	Name                string
	Description         string
	Content             string
	Body                string
	Core                string
	Path                string
	WhenToUse           []string
	ArgumentHint        string
	Version             string
	Categories          []string
	Paths               []string
	RecommendedBundles  []string
	Sections            map[string]string
	SectionOrder        []string
	Requires            Requirements
	Compatibility       Compatibility
	Warnings            []string
	SourceHash          string
	NormalizationSource string
	HasFrontmatter      bool
	InstallMemory       *InstallMemory
}

// CatalogEntry returns the skill's lightweight metadata.
func (s *Skill) CatalogEntry() CatalogEntry {
	return CatalogEntry{
		ID:                  s.ID,
		Name:                s.Name,
		Description:         s.Description,
		WhenToUse:           append([]string{}, s.WhenToUse...),
		ArgumentHint:        s.ArgumentHint,
		Version:             s.Version,
		Categories:          append([]string{}, s.Categories...),
		Paths:               append([]string{}, s.Paths...),
		RecommendedBundles:  append([]string{}, s.RecommendedBundles...),
		Sections:            append([]string{}, s.SectionOrder...),
		Requires:            s.Requires,
		Compatibility:       s.Compatibility,
		Warnings:            append([]string{}, s.Warnings...),
		Path:                s.Path,
		InstallMemory:       cloneInstallMemory(s.InstallMemory),
		NormalizationStatus: skillNormalizationStatus(s, false),
		NormalizationSource: s.NormalizationSource,
		Normalized:          strings.EqualFold(strings.TrimSpace(s.NormalizationSource), "llm"),
		NeedsNormalization:  skillNeedsLLMNormalization(s),
	}
}

func (l *Loader) CatalogEntryFor(skill *Skill) CatalogEntry {
	entry := skill.CatalogEntry()
	l.decorateNormalization(&entry)
	return entry
}

func (l *Loader) decorateNormalization(entry *CatalogEntry) {
	if entry == nil {
		return
	}
	entry.CanNormalize = l.hasFallbackNormalizer()
	entry.NormalizationStatus = entry.normalizationStatus()
}

func (l *Loader) hasFallbackNormalizer() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.fallbackNormalizer != nil
}

func (entry CatalogEntry) normalizationStatus() string {
	if entry.Normalized {
		return "normalized"
	}
	if entry.NeedsNormalization {
		if entry.CanNormalize {
			return "suggested"
		}
		return "unavailable"
	}
	return "not_needed"
}

func skillNormalizationStatus(skill *Skill, canNormalize bool) string {
	entry := CatalogEntry{
		Normalized:         strings.EqualFold(strings.TrimSpace(skill.NormalizationSource), "llm"),
		NeedsNormalization: skillNeedsLLMNormalization(skill),
		CanNormalize:       canNormalize,
	}
	return entry.normalizationStatus()
}

func skillNeedsLLMNormalization(skill *Skill) bool {
	if skill == nil || strings.EqualFold(strings.TrimSpace(skill.NormalizationSource), "llm") {
		return false
	}
	if !skill.HasFrontmatter {
		return true
	}
	if strings.TrimSpace(skill.Description) == "" {
		return true
	}
	if strings.TrimSpace(skill.Core) == "" {
		return true
	}
	if len(skill.SectionOrder) == 0 {
		return true
	}
	return false
}

// Loader loads and manages skills.
type Loader struct {
	mu                 sync.RWMutex
	installMu          sync.Mutex
	skills             map[string]*Skill
	normalizedCache    map[string]normalizedArtifacts
	skillsDir          string
	fallbackNormalizer FallbackNormalizer
}

// NewLoader creates a new skill loader.
func NewLoader(skillsDir string) *Loader {
	return &Loader{
		skills:          make(map[string]*Skill),
		normalizedCache: make(map[string]normalizedArtifacts),
		skillsDir:       skillsDir,
	}
}

// Load loads and parses a skill by name.
func (l *Loader) Load(name string) (*Skill, error) {
	skillID, err := l.ResolveID(name)
	if err != nil {
		return nil, err
	}

	l.mu.RLock()
	if skill, ok := l.skills[skillID]; ok {
		l.mu.RUnlock()
		return skill, nil
	}
	l.mu.RUnlock()

	parsed, err := l.loadUncached(skillID, true)
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.skills[skillID]; ok {
		return existing, nil
	}
	l.skills[skillID] = parsed
	return parsed, nil
}

func (l *Loader) loadUncached(name string, persistNormalized bool) (*Skill, error) {
	skillPath, err := ResolvePath(l.skillsDir, name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read skill %q: %w", name, err)
	}

	parsed, err := parseSkill(name, skillPath, string(data))
	if err != nil {
		return nil, err
	}
	if err := l.normalizeIfNeeded(skillPath, parsed, persistNormalized); err != nil {
		return nil, err
	}
	applyRuntimeCompatibility(parsed)
	applyInstallMemory(parsed, readInstallMemoryForSkillPath(skillPath))
	return parsed, nil
}

func applyRuntimeCompatibility(parsed *Skill) {
	if parsed == nil {
		return
	}

	missingDeps := missingExecutables(parsed.Requires.Executables)
	if len(missingDeps) > 0 {
		parsed.Compatibility.MissingDependencies = stringutil.Unique(append(parsed.Compatibility.MissingDependencies, missingDeps...))
		if parsed.Compatibility.Status == CompatibilityNativeSupported {
			parsed.Compatibility.Status = CompatibilityDegradedSupported
		}
		note := "Missing local executables: " + strings.Join(parsed.Compatibility.MissingDependencies, ", ")
		parsed.Compatibility.Notes = stringutil.AppendUnique(parsed.Compatibility.Notes, note)
		parsed.Warnings = stringutil.AppendUnique(parsed.Warnings, note)
	}

	if osMismatchNote := runtimeOSMismatch(parsed.Requires.OSHints); osMismatchNote != "" {
		if parsed.Compatibility.Status == CompatibilityNativeSupported {
			parsed.Compatibility.Status = CompatibilityDegradedSupported
		}
		parsed.Compatibility.Notes = stringutil.AppendUnique(parsed.Compatibility.Notes, osMismatchNote)
		parsed.Warnings = stringutil.AppendUnique(parsed.Warnings, osMismatchNote)
	}

	parsed.Compatibility.MissingCapabilities = stringutil.Unique(parsed.Compatibility.MissingCapabilities)
	parsed.Compatibility.MissingDependencies = stringutil.Unique(parsed.Compatibility.MissingDependencies)
	parsed.Compatibility.Notes = stringutil.Unique(parsed.Compatibility.Notes)
	parsed.Warnings = stringutil.Unique(parsed.Warnings)
}

func applyInstallMemory(parsed *Skill, memory *InstallMemory) {
	if parsed == nil || memory == nil {
		return
	}
	parsed.InstallMemory = cloneInstallMemory(memory)
	parsed.Version = strings.TrimSpace(memory.Version)
	parsed.Categories = append([]string{}, memory.Categories...)
}

func cloneInstallMemory(memory *InstallMemory) *InstallMemory {
	if memory == nil {
		return nil
	}
	return &InstallMemory{
		Source:        memory.Source,
		SourceEntryID: memory.SourceEntryID,
		SourceOrigin:  memory.SourceOrigin,
		Trust:         memory.Trust,
		Version:       memory.Version,
		Categories:    append([]string{}, memory.Categories...),
		InstalledAt:   memory.InstalledAt,
	}
}

func missingExecutables(items []string) []string {
	missing := make([]string, 0, len(items))
	for _, item := range stringutil.Unique(items) {
		if strings.TrimSpace(item) == "" {
			continue
		}
		if _, err := exec.LookPath(item); err != nil {
			missing = append(missing, item)
		}
	}
	return missing
}

func runtimeOSMismatch(hints []string) string {
	if len(hints) == 0 {
		return ""
	}
	current := runtime.GOOS
	normalized := normalizeRuntimeOS(current)
	for _, hint := range hints {
		if normalizeRuntimeOS(hint) == normalized {
			return ""
		}
	}
	return fmt.Sprintf("Current runtime OS is %s, but this skill contains %s-specific guidance.", current, strings.Join(stringutil.Unique(hints), ", "))
}

func normalizeRuntimeOS(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "mac", "macos", "osx":
		return "darwin"
	case "win", "windows":
		return "windows"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

// Get returns a loaded skill.
func (l *Loader) Get(name string) (*Skill, error) {
	skillID, err := l.ResolveID(name)
	if err != nil {
		return nil, err
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	if skill, ok := l.skills[skillID]; ok {
		return skill, nil
	}
	return nil, newSkillNotFoundError(name)
}

// List returns all loaded skill names.
func (l *Loader) List() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	names := make([]string, 0, len(l.skills))
	for name := range l.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Discover returns all skill names currently discoverable on disk.
func (l *Loader) Discover() ([]string, error) {
	entries, err := os.ReadDir(l.skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read skills dir: %w", err)
	}

	names := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		discovered, err := l.discoverEntry(entry)
		if err != nil {
			return nil, err
		}
		for _, name := range discovered {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (l *Loader) discoverEntry(entry fs.DirEntry) ([]string, error) {
	name := entry.Name()
	fullPath := filepath.Join(l.skillsDir, name)

	if entry.IsDir() {
		hasRootSkill, err := hasSkillFile(filepath.Join(fullPath, "SKILL.md"))
		if err != nil {
			return nil, err
		}
		if !hasRootSkill {
			return nil, nil
		}

		names := []string{name}
		nested, err := discoverNestedSkillIDs(fullPath, name)
		if err != nil {
			return nil, err
		}
		names = append(names, nested...)
		return names, nil
	}

	return nil, nil
}

const maxNestedSkillDiscoveryDepth = 2

func discoverNestedSkillIDs(rootPath, rootID string) ([]string, error) {
	names := make([]string, 0)
	err := filepath.WalkDir(rootPath, func(currentPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if currentPath == rootPath || !entry.IsDir() {
			return nil
		}
		if shouldSkipNestedSkillDir(entry.Name()) {
			return filepath.SkipDir
		}

		rel, err := filepath.Rel(rootPath, currentPath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || rel == "" {
			return nil
		}
		depth := strings.Count(rel, "/") + 1
		if depth > maxNestedSkillDiscoveryDepth {
			return filepath.SkipDir
		}

		hasSkill, err := hasSkillFile(filepath.Join(currentPath, "SKILL.md"))
		if err != nil {
			return err
		}
		if hasSkill {
			names = append(names, rootID+"/"+rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func hasSkillFile(pathValue string) (bool, error) {
	info, err := os.Stat(pathValue)
	if err == nil {
		return !info.IsDir(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func shouldSkipNestedSkillDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "dist", "build", ".cache":
		return true
	default:
		return strings.HasPrefix(name, ".") && name != ".claude" && name != ".agents"
	}
}

// Catalog returns lightweight skill metadata for discoverable skills that match
// the current workspace context.
func (l *Loader) Catalog(workspaceDir string) ([]CatalogEntry, error) {
	names, err := l.Discover()
	if err != nil {
		return nil, err
	}

	workspaceFiles, err := collectWorkspaceFiles(workspaceDir)
	if err != nil {
		return nil, err
	}

	items := make([]CatalogEntry, 0, len(names))
	for _, name := range names {
		skill, err := l.loadUncached(name, false)
		if err != nil {
			items = append(items, CatalogEntry{
				ID:          name,
				Name:        name,
				Description: "Skill metadata could not be loaded.",
				Compatibility: Compatibility{
					Status: CompatibilityUnsupported,
				},
				Warnings: []string{"Failed to load skill metadata: " + err.Error()},
			})
			continue
		}
		entry := l.CatalogEntryFor(skill)
		if !matchesWorkspace(entry.Paths, workspaceFiles) {
			continue
		}
		items = append(items, entry)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].ID < items[j].ID
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

// GetContent returns the raw on-disk skill content.
func (l *Loader) GetContent(name string) (string, error) {
	skill, err := l.Load(name)
	if err != nil {
		return "", err
	}
	return skill.Content, nil
}

// GetCore returns the lightweight core instructions for a skill.
func (l *Loader) GetCore(name string) (string, error) {
	skill, err := l.Load(name)
	if err != nil {
		return "", err
	}
	return skill.Core, nil
}

// GetSections returns the requested named sections in canonical order.
func (l *Loader) GetSections(name string, requested []string) ([]Section, error) {
	skill, err := l.Load(name)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(requested))
	resolved := make([]string, 0, len(requested))
	for _, sectionName := range requested {
		canonical := canonicalSectionName(sectionName)
		if canonical == "" {
			return nil, newSkillInvalidRequestError("unknown skill section: %s", sectionName)
		}
		if canonical == "core" {
			continue
		}
		if _, ok := skill.Sections[canonical]; !ok {
			return nil, newSkillInvalidRequestError("skill %q does not define section %q", skill.ID, canonical)
		}
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		resolved = append(resolved, canonical)
	}

	sections := make([]Section, 0, len(resolved))
	for _, sectionName := range skill.SectionOrder {
		if _, ok := seen[sectionName]; !ok {
			continue
		}
		sections = append(sections, Section{
			Name:    sectionName,
			Title:   sectionTitle(sectionName),
			Content: skill.Sections[sectionName],
		})
	}
	return sections, nil
}

// ResolveID resolves a stable on-disk skill identifier from an id, alias, or display name.
func (l *Loader) ResolveID(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", newSkillNotFoundError(name)
	}

	if normalized, ok := normalizeSkillID(name); ok {
		if _, err := ResolvePath(l.skillsDir, normalized); err == nil {
			return normalized, nil
		} else if !errors.Is(err, ErrSkillNotFound) {
			return "", err
		}
	}

	names, err := l.Discover()
	if err != nil {
		return "", err
	}
	matches := make([]string, 0, 1)
	for _, skillID := range names {
		if strings.EqualFold(skillID, name) {
			return skillID, nil
		}
		skill, err := l.loadUncached(skillID, false)
		if err != nil {
			continue
		}
		if strings.EqualFold(skill.Name, name) {
			matches = append(matches, skillID)
		}
	}

	switch len(matches) {
	case 0:
		return "", newSkillNotFoundError(name)
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", newSkillInvalidRequestError("skill reference %q matches multiple installed skills: %s", name, strings.Join(matches, ", "))
	}
}

// ResolvePath returns the on-disk path for a skill name.
func ResolvePath(skillsDir, name string) (string, error) {
	normalized, ok := normalizeSkillID(name)
	if !ok {
		return "", newSkillNotFoundError(name)
	}
	candidate := filepath.Join(skillsDir, filepath.FromSlash(normalized), "SKILL.md")
	if !pathWithin(skillsDir, candidate) {
		return "", newSkillNotFoundError(name)
	}
	info, err := os.Stat(candidate)
	if err == nil {
		if info.IsDir() {
			return "", newSkillNotFoundError(name)
		}
		return candidate, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to stat skill %q: %w", name, err)
	}

	return "", newSkillNotFoundError(name)
}

func normalizeSkillID(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.IsAbs(name) || path.IsAbs(name) {
		return "", false
	}
	name = filepath.ToSlash(name)
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	return clean, true
}

func pathWithin(root, candidate string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func parseSkill(name, pathValue, raw string) (*Skill, error) {
	frontmatter, body, hasFrontmatter, err := splitFrontmatter(raw)
	if err != nil {
		return nil, fmt.Errorf("parse skill %q frontmatter: %w", name, err)
	}

	sections, order := extractSections(body)
	core := strings.TrimSpace(sections["core"])
	if core == "" {
		core = strings.TrimSpace(body)
	}

	description := firstNonEmpty(
		stringValue(frontmatter["description"]),
		stringValue(frontmatter["summary"]),
		fmt.Sprintf("Skill: %s", name),
	)

	skillName := firstNonEmpty(stringValue(frontmatter["name"]), name)
	skill := &Skill{
		ID:                 name,
		Name:               skillName,
		Description:        description,
		Content:            raw,
		Body:               strings.TrimSpace(body),
		Core:               core,
		Path:               pathValue,
		WhenToUse:          stringListValue(frontmatter["when_to_use"]),
		ArgumentHint:       stringValue(frontmatter["argument_hint"]),
		Paths:              stringListValue(frontmatter["paths"]),
		RecommendedBundles: stringListValue(frontmatter["recommended_bundles"]),
		Sections:           sections,
		SectionOrder:       order,
		Requires:           Requirements{},
		Compatibility: Compatibility{
			Status: CompatibilityNativeSupported,
		},
		Warnings:       nil,
		SourceHash:     sourceHash(raw),
		HasFrontmatter: hasFrontmatter,
	}

	if declared := stringListValue(frontmatter["sections"]); len(declared) > 0 {
		skill.SectionOrder = normalizeDeclaredSections(declared, sections)
	}
	if len(skill.SectionOrder) == 0 {
		skill.SectionOrder = []string{"core"}
	}
	if skill.Sections == nil {
		skill.Sections = map[string]string{"core": skill.Core}
	}
	return skill, nil
}

func splitFrontmatter(raw string) (map[string]interface{}, string, bool, error) {
	lines := strings.Split(raw, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return map[string]interface{}{}, raw, false, nil
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return map[string]interface{}{}, raw, false, nil
	}

	frontmatterText := strings.Join(lines[1:end], "\n")
	body := strings.Join(lines[end+1:], "\n")
	values := make(map[string]interface{})
	if err := yaml.Unmarshal([]byte(frontmatterText), &values); err != nil {
		return nil, "", false, err
	}
	return values, body, true, nil
}

func extractSections(body string) (map[string]string, []string) {
	lines := strings.Split(body, "\n")
	sections := make(map[string][]string)
	order := make([]string, 0, 6)
	current := "core"
	sections[current] = make([]string, 0, len(lines))

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if sectionName := canonicalSectionName(strings.TrimSpace(strings.TrimPrefix(line, "## "))); sectionName != "" {
				current = sectionName
				if _, ok := sections[current]; !ok {
					sections[current] = []string{}
					order = append(order, current)
				}
				continue
			}
		}
		sections[current] = append(sections[current], line)
	}

	result := make(map[string]string, len(sections))
	if len(order) == 0 {
		order = append(order, "core")
	}
	for _, sectionName := range append([]string{"core"}, order...) {
		lines := sections[sectionName]
		content := strings.TrimSpace(strings.Join(lines, "\n"))
		if content == "" && sectionName != "core" {
			continue
		}
		result[sectionName] = content
	}

	finalOrder := make([]string, 0, len(order))
	if core := strings.TrimSpace(result["core"]); core != "" {
		finalOrder = append(finalOrder, "core")
	}
	for _, name := range order {
		if name == "core" {
			continue
		}
		if strings.TrimSpace(result[name]) == "" {
			continue
		}
		finalOrder = append(finalOrder, name)
	}
	if len(finalOrder) == 0 {
		result["core"] = strings.TrimSpace(body)
		finalOrder = []string{"core"}
	}
	return result, finalOrder
}

func normalizeDeclaredSections(declared []string, actual map[string]string) []string {
	seen := make(map[string]struct{}, len(declared))
	order := make([]string, 0, len(declared))
	for _, item := range declared {
		name := canonicalSectionName(item)
		if name == "" {
			continue
		}
		if _, ok := actual[name]; !ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		order = append(order, name)
	}
	if len(order) == 0 {
		return order
	}
	if _, ok := seen["core"]; !ok {
		if _, hasCore := actual["core"]; hasCore && strings.TrimSpace(actual["core"]) != "" {
			order = append([]string{"core"}, order...)
		}
	}
	return order
}

func stringValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func stringListValue(value interface{}) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []string{strings.TrimSpace(typed)}
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if s := stringValue(item); s != "" {
				result = append(result, s)
			}
		}
		return stringutil.Unique(result)
	case []string:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		return stringutil.Unique(result)
	default:
		if s := stringValue(typed); s != "" && s != "<nil>" {
			return []string{s}
		}
		return nil
	}
}

func collectWorkspaceFiles(workspaceDir string) ([]string, error) {
	if workspaceDir == "" {
		return nil, nil
	}

	relPaths := make([]string, 0, 128)
	if err := filepath.WalkDir(workspaceDir, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if filePath != workspaceDir && strings.HasPrefix(entry.Name(), ".godex") {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(workspaceDir, filePath)
		if err != nil {
			return nil
		}
		relPaths = append(relPaths, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return nil, err
	}
	return relPaths, nil
}

func matchesWorkspace(patterns []string, relPaths []string) bool {
	if len(patterns) == 0 {
		return true
	}
	if len(relPaths) == 0 {
		return false
	}

	for _, rel := range relPaths {
		for _, pattern := range patterns {
			if matchPathPattern(pattern, rel) {
				return true
			}
		}
	}
	return false
}

func matchPathPattern(patternValue, rel string) bool {
	patternValue = filepath.ToSlash(strings.TrimSpace(patternValue))
	rel = filepath.ToSlash(rel)
	if patternValue == "" {
		return false
	}
	return matchPathSegments(strings.Split(patternValue, "/"), strings.Split(rel, "/"))
}

func matchPathSegments(patternSegments, pathSegments []string) bool {
	if len(patternSegments) == 0 {
		return len(pathSegments) == 0
	}
	if patternSegments[0] == "**" {
		if matchPathSegments(patternSegments[1:], pathSegments) {
			return true
		}
		return len(pathSegments) > 0 && matchPathSegments(patternSegments, pathSegments[1:])
	}
	if len(pathSegments) == 0 {
		return false
	}
	ok, err := path.Match(patternSegments[0], pathSegments[0])
	if err != nil || !ok {
		return false
	}
	return matchPathSegments(patternSegments[1:], pathSegments[1:])
}

func canonicalSectionName(input string) string {
	switch strings.ToLower(strings.TrimSpace(strings.TrimSuffix(input, ":"))) {
	case "core":
		return "core"
	case "workflow":
		return "workflow"
	case "references":
		return "references"
	case "templates":
		return "templates"
	case "scripts":
		return "scripts"
	case "fallbacks", "fallback":
		return "fallbacks"
	case "examples", "example":
		return "examples"
	default:
		return ""
	}
}

func sectionTitle(name string) string {
	switch name {
	case "core":
		return "Core"
	case "workflow":
		return "Workflow"
	case "references":
		return "References"
	case "templates":
		return "Templates"
	case "scripts":
		return "Scripts"
	case "fallbacks":
		return "Fallbacks"
	case "examples":
		return "Examples"
	default:
		return textutil.Title(name)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" && strings.TrimSpace(value) != "<nil>" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
