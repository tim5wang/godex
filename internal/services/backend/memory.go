package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/core/memory"
)

// MemoryDigestResult is the output of an auditable memory digest run.
type MemoryDigestResult struct {
	Candidates []memory.Candidate `json:"candidates"`
	Report     string             `json:"report"`
	ReportPath string             `json:"report_path,omitempty"`
}

func (s *Service) memoryManager() *memory.Manager {
	s.mu.Lock()
	defer s.mu.Unlock()
	return memory.NewManager(s.cfg.MemoryDir)
}

// ListMemory returns durable memory entries with optional metadata filters.
func (s *Service) ListMemory(_ context.Context, opts memory.SearchOptions) ([]memory.StoredMemory, error) {
	return s.memoryManager().Search(opts)
}

// ListMemoryCandidates returns pending durable-memory candidates.
func (s *Service) ListMemoryCandidates(_ context.Context) ([]memory.Candidate, error) {
	return s.memoryManager().ListCandidates()
}

// ListMemorySuppressions returns dismissed-candidate suppressions that are
// still active for automatic bridge dedupe.
func (s *Service) ListMemorySuppressions(_ context.Context) ([]memory.CandidateSuppression, error) {
	return s.memoryManager().ListSuppressions()
}

// ListMemoryAudit returns recent durable-memory audit entries.
func (s *Service) ListMemoryAudit(_ context.Context, limit int) ([]memory.AuditLogEntry, error) {
	return s.memoryManager().ListAudit(limit)
}

// PreviewMemoryContext returns the layered memory context for a candidate query.
func (s *Service) PreviewMemoryContext(_ context.Context, query string) (memory.ContextLayers, error) {
	layers, err := s.memoryManager().BuildContextLayers(query)
	if err != nil {
		return memory.ContextLayers{}, err
	}
	layers.Identity = append([]memory.RelevantMemory{}, layers.Identity...)
	layers.Core = append([]memory.RelevantMemory{}, layers.Core...)
	layers.Relevant = append([]memory.RelevantMemory{}, layers.Relevant...)
	return layers, nil
}

// RememberMemory stores one durable memory entry.
func (s *Service) RememberMemory(_ context.Context, input memory.SaveInput) (*memory.Entry, error) {
	return s.memoryManager().Remember(input)
}

// UpdateMemory modifies one durable memory entry.
func (s *Service) UpdateMemory(_ context.Context, input memory.UpdateInput) (*memory.Entry, error) {
	return s.memoryManager().Update(input)
}

// ForgetMemory deletes one durable memory entry.
func (s *Service) ForgetMemory(_ context.Context, input memory.ForgetInput) (*memory.Entry, error) {
	return s.memoryManager().Forget(input)
}

// AcceptMemoryCandidate promotes one pending candidate into durable memory.
func (s *Service) AcceptMemoryCandidate(_ context.Context, input memory.AcceptCandidateInput) (*memory.Entry, error) {
	input.Fingerprint = strings.TrimSpace(input.Fingerprint)
	return s.memoryManager().AcceptCandidateWithOptions(input)
}

// DismissMemoryCandidate removes one pending candidate without storing it.
func (s *Service) DismissMemoryCandidate(_ context.Context, fingerprint string) (*memory.Candidate, error) {
	return s.memoryManager().DismissCandidate(strings.TrimSpace(fingerprint))
}

// RestoreMemoryAudit restores one durable-memory audit snapshot.
func (s *Service) RestoreMemoryAudit(_ context.Context, auditID, target string) (*memory.AuditLogEntry, error) {
	return s.memoryManager().RestoreAudit(auditID, target)
}

// MineProjectMemoryCandidates scans high-signal project docs and turns them
// into reviewed memory candidates.
func (s *Service) MineProjectMemoryCandidates(_ context.Context) ([]memory.Candidate, error) {
	extractor := memory.NewExtractor(s.memoryManager(), s.cfg.TempDir)
	return extractor.CaptureProjectDocs(s.cfg.WorkspaceDir)
}

// DigestMemory analyzes transcript/candidate signals and adds reviewable memory
// candidates without writing durable memory directly.
func (s *Service) DigestMemory(_ context.Context) (MemoryDigestResult, error) {
	analyze := s.analyze
	if analyze == nil {
		analyze = func(input insights.Input) (*insights.Report, error) {
			return insights.NewAnalyzer(s.cfg.TranscriptsDir, s.cfg.TempDir, s.cfg.MemoryDir).Analyze(input)
		}
	}
	report, err := analyze(insights.Input{})
	if err != nil {
		return MemoryDigestResult{}, err
	}
	extractor := memory.NewExtractor(s.memoryManager(), s.cfg.TempDir)
	added, err := extractor.CaptureInsightsReport(report)
	if err != nil {
		return MemoryDigestResult{}, err
	}
	markdown := report.Markdown()
	result := MemoryDigestResult{Candidates: added, Report: markdown}
	if strings.TrimSpace(s.cfg.TempDir) != "" {
		path := filepath.Join(s.cfg.TempDir, "memory-digest-latest.md")
		if err := os.MkdirAll(s.cfg.TempDir, 0755); err == nil {
			if writeErr := os.WriteFile(path, []byte(markdown), 0644); writeErr == nil {
				result.ReportPath = path
			}
		}
	}
	return result, nil
}
