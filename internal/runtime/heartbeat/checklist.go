package heartbeat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type checklistSource struct {
	Path    string
	Content string
}

func loadChecklist(cfg Config) (checklistSource, error) {
	candidates := []string{}
	if path := strings.TrimSpace(cfg.ChecklistPath); path != "" {
		candidates = append(candidates, path)
	}
	if strings.TrimSpace(cfg.WorkspaceDir) != "" {
		candidates = append(candidates, filepath.Join(cfg.WorkspaceDir, "HEARTBEAT.md"))
	}
	if strings.TrimSpace(cfg.StateDir) != "" {
		candidates = append(candidates, filepath.Join(cfg.StateDir, "HEARTBEAT.md"))
	}

	seen := make(map[string]struct{}, len(candidates))
	for _, path := range candidates {
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			return checklistSource{}, fmt.Errorf("heartbeat checklist %s is empty", path)
		}
		return checklistSource{Path: path, Content: content}, nil
	}
	return checklistSource{}, fmt.Errorf("heartbeat checklist not found; looked in configured path, workspace HEARTBEAT.md, and GODEX_HOME state HEARTBEAT.md")
}
