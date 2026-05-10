package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/stringutil"
)

const installMetadataFileName = "SKILL.install.json"

// InstallMemory records where a skill was installed from so catalog and market
// views can retain source, trust, version, and category context.
type InstallMemory struct {
	Source        string   `json:"source"`
	SourceEntryID string   `json:"source_entry_id,omitempty"`
	SourceOrigin  string   `json:"source_origin,omitempty"`
	Trust         string   `json:"trust,omitempty"`
	Version       string   `json:"version,omitempty"`
	Categories    []string `json:"categories,omitempty"`
	InstalledAt   string   `json:"installed_at,omitempty"`
}

func buildInstallMemory(items []SourceEntry, source, requestedName, targetName string, installedAt time.Time) *InstallMemory {
	source = strings.TrimSpace(source)
	memory := &InstallMemory{
		Source:      source,
		InstalledAt: installedAt.UTC().Format(time.RFC3339),
	}
	if entry, ok := matchInstallSource(items, source, requestedName, targetName); ok {
		memory.SourceEntryID = strings.TrimSpace(entry.ID)
		memory.SourceOrigin = strings.TrimSpace(entry.Origin)
		memory.Trust = strings.TrimSpace(entry.Trust)
		memory.Version = strings.TrimSpace(entry.Version)
		memory.Categories = append([]string{}, entry.Categories...)
	}
	if memory.SourceOrigin == "" {
		memory.SourceOrigin = inferSourceOrigin(source)
	}
	if memory.Trust == "" {
		memory.Trust = normalizeSourceTrust("", memory.SourceOrigin)
	}
	memory.Categories = stringutil.Unique(memory.Categories)
	return memory
}

func matchInstallSource(items []SourceEntry, source, requestedName, targetName string) (SourceEntry, bool) {
	source = strings.TrimSpace(source)
	requestedName = strings.TrimSpace(requestedName)
	targetName = strings.TrimSpace(targetName)
	normalizedSource := normalizeComparableSource(source)
	byName := func(value string, item SourceEntry) bool {
		value = strings.TrimSpace(value)
		if value == "" {
			return false
		}
		return strings.EqualFold(value, item.SkillName) || strings.EqualFold(value, item.Name) || strings.EqualFold(value, item.ID)
	}
	for _, item := range items {
		if normalizedSource != "" && normalizeComparableSource(item.Source) == normalizedSource {
			return item, true
		}
	}
	for _, item := range items {
		if byName(requestedName, item) || byName(targetName, item) {
			return item, true
		}
	}
	return SourceEntry{}, false
}

func normalizeComparableSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if normalized, err := normalizeGitSource(source); err == nil {
		return strings.TrimSuffix(strings.ToLower(normalized), "/")
	}
	if abs, err := filepath.Abs(source); err == nil {
		return filepath.Clean(strings.ToLower(abs))
	}
	return strings.TrimSuffix(strings.ToLower(source), "/")
}

func inferSourceOrigin(source string) string {
	if source == "" {
		return "community"
	}
	if strings.HasPrefix(source, ".") || strings.HasPrefix(source, "/") {
		return "local"
	}
	if _, err := os.Stat(source); err == nil {
		return "local"
	}
	return "community"
}

func installMetadataPathForSkillPath(skillPath string) string {
	base := filepath.Base(skillPath)
	if strings.EqualFold(base, "SKILL.md") {
		return filepath.Join(filepath.Dir(skillPath), installMetadataFileName)
	}
	return skillPath + ".install.json"
}

func readInstallMemoryForSkillPath(skillPath string) *InstallMemory {
	path := installMetadataPathForSkillPath(skillPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var memory InstallMemory
	if err := json.Unmarshal(data, &memory); err != nil {
		return nil
	}
	memory.Source = strings.TrimSpace(memory.Source)
	memory.SourceEntryID = strings.TrimSpace(memory.SourceEntryID)
	memory.SourceOrigin = strings.TrimSpace(memory.SourceOrigin)
	memory.Trust = normalizeSourceTrust(memory.Trust, memory.SourceOrigin)
	memory.Version = strings.TrimSpace(memory.Version)
	memory.Categories = stringutil.Unique(memory.Categories)
	if memory.Source == "" {
		return nil
	}
	return &memory
}

func writeInstallMemory(skillRoot string, memory *InstallMemory) error {
	if memory == nil {
		return nil
	}
	data, err := json.MarshalIndent(memory, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(skillRoot, installMetadataFileName), data, 0644)
}
