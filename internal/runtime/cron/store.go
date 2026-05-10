package cron

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
	ListJobs() ([]Job, error)
	GetJob(string) (Job, error)
	SaveJob(Job) error
	DeleteJob(string) error
	AppendRunLog(RunLog) error
	ListRunLogs(jobID string, limit int) ([]RunLog, error)
}

type FileStore struct {
	root string
}

func NewFileStore(stateDir string) *FileStore {
	return &FileStore{root: filepath.Join(strings.TrimSpace(stateDir), "cron")}
}

func (s *FileStore) JobsDir() string {
	return filepath.Join(s.root, "jobs")
}

func (s *FileStore) RunsDir() string {
	return filepath.Join(s.root, "runs")
}

func (s *FileStore) Ensure() error {
	if err := os.MkdirAll(s.JobsDir(), 0755); err != nil {
		return err
	}
	return os.MkdirAll(s.RunsDir(), 0755)
}

func (s *FileStore) ListJobs() ([]Job, error) {
	if err := s.Ensure(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.JobsDir())
	if err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		job, err := s.readJob(filepath.Join(s.JobsDir(), entry.Name()))
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
	return jobs, nil
}

func (s *FileStore) GetJob(jobID string) (Job, error) {
	return s.readJob(s.jobPath(jobID))
}

func (s *FileStore) SaveJob(job Job) error {
	if strings.TrimSpace(job.ID) == "" {
		return errors.New("missing cron job id")
	}
	if err := s.Ensure(); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(s.jobPath(job.ID), job, 0644)
}

func (s *FileStore) DeleteJob(jobID string) error {
	if err := os.Remove(s.jobPath(jobID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	runsDir := filepath.Join(s.RunsDir(), strings.TrimSpace(jobID))
	if err := os.RemoveAll(runsDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *FileStore) AppendRunLog(run RunLog) error {
	if strings.TrimSpace(run.JobID) == "" || strings.TrimSpace(run.ID) == "" {
		return errors.New("missing cron run identifiers")
	}
	dir := filepath.Join(s.RunsDir(), run.JobID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(filepath.Join(dir, run.ID+".json"), run, 0644)
}

func (s *FileStore) ListRunLogs(jobID string, limit int) ([]RunLog, error) {
	dir := filepath.Join(s.RunsDir(), strings.TrimSpace(jobID))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	runs := make([]RunLog, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var run RunLog
		if err := readJSON(filepath.Join(dir, entry.Name()), &run); err != nil {
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

func (s *FileStore) jobPath(jobID string) string {
	return filepath.Join(s.JobsDir(), strings.TrimSpace(jobID)+".json")
}

func (s *FileStore) readJob(path string) (Job, error) {
	var job Job
	if err := readJSON(path, &job); err != nil {
		return Job{}, err
	}
	return job, nil
}

func readJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
