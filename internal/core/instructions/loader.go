package instructions

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	scopeProject = "project"
	scopeRule    = "rule"
	scopeLocal   = "local"
)

// Loader discovers AGENT.md instruction files for the current workspace.
type Loader struct{}

// NewLoader creates a new instruction loader.
func NewLoader() *Loader {
	return &Loader{}
}

// Load returns instruction sources ordered from low to high priority.
func (l *Loader) Load(workspaceDir, stateDir string) ([]InstructionSource, error) {
	sources := make([]InstructionSource, 0, 3)

	projectPath := filepath.Join(workspaceDir, "AGENT.md")
	projectSource, err := loadOptionalFile(projectPath, scopeProject, 1)
	if err != nil {
		return nil, err
	}
	if projectSource != nil {
		sources = append(sources, *projectSource)
	}

	ruleDir := filepath.Join(stateDir, "rules")
	ruleSources, err := loadRuleFiles(ruleDir)
	if err != nil {
		return nil, err
	}
	sources = append(sources, ruleSources...)

	localPath := filepath.Join(stateDir, "AGENT.local.md")
	localSource, err := loadOptionalFile(localPath, scopeLocal, 3)
	if err != nil {
		return nil, err
	}
	if localSource != nil {
		sources = append(sources, *localSource)
	}

	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].Priority == sources[j].Priority {
			return sources[i].Path < sources[j].Path
		}
		return sources[i].Priority < sources[j].Priority
	})

	return sources, nil
}

func loadOptionalFile(path, scope string, priority int) (*InstructionSource, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		return nil, nil
	}

	return &InstructionSource{
		Path:     path,
		Scope:    scope,
		Priority: priority,
		Content:  content,
	}, nil
}

func loadRuleFiles(ruleDir string) ([]InstructionSource, error) {
	entries, err := os.ReadDir(ruleDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		paths = append(paths, filepath.Join(ruleDir, entry.Name()))
	}
	sort.Strings(paths)

	sources := make([]InstructionSource, 0, len(paths))
	for _, path := range paths {
		source, err := loadOptionalFile(path, scopeRule, 2)
		if err != nil {
			return nil, err
		}
		if source != nil {
			sources = append(sources, *source)
		}
	}
	return sources, nil
}
