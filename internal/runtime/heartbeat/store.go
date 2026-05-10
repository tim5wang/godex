package heartbeat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

type Store interface {
	GetRule() (Rule, error)
	SaveRule(Rule) error
	AppendRunLog(RunLog) error
	ListRunLogs(limit int) ([]RunLog, error)
}

type FileStore struct {
	root string
}

func NewFileStore(stateDir string) *FileStore {
	return &FileStore{root: filepath.Join(strings.TrimSpace(stateDir), "heartbeat")}
}

func (s *FileStore) RulePath() string {
	return filepath.Join(s.root, "rule.json")
}

func (s *FileStore) RunsDir() string {
	return filepath.Join(s.root, "runs")
}

func (s *FileStore) Ensure() error {
	if err := os.MkdirAll(filepath.Dir(s.RulePath()), 0755); err != nil {
		return err
	}
	return os.MkdirAll(s.RunsDir(), 0755)
}

func (s *FileStore) GetRule() (Rule, error) {
	var rule Rule
	if err := readJSON(s.RulePath(), &rule); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

func (s *FileStore) SaveRule(rule Rule) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(s.RulePath(), rule, 0644)
}

func (s *FileStore) AppendRunLog(run RunLog) error {
	if strings.TrimSpace(run.ID) == "" {
		return errors.New("missing heartbeat run id")
	}
	if err := s.Ensure(); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(filepath.Join(s.RunsDir(), run.ID+".json"), run, 0644)
}

func (s *FileStore) ListRunLogs(limit int) ([]RunLog, error) {
	if err := s.Ensure(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.RunsDir())
	if err != nil {
		return nil, err
	}
	runs := make([]RunLog, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var run RunLog
		if err := readJSON(filepath.Join(s.RunsDir(), entry.Name()), &run); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].StartedAt.Equal(runs[j].StartedAt) {
			return runs[i].ID > runs[j].ID
		}
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func readJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
