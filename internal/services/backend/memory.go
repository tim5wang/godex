package backend

import (
	"context"
	"fmt"
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

func (s *Service) memoryManager(contexts ...context.Context) *memory.Manager {
	if len(contexts) > 0 {
		if owner, ok := memory.OwnerFromContext(contexts[0]); ok {
			return s.personalMemory(owner)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return memory.NewManager(s.cfg.MemoryDir)
}

// ListMemory returns durable memory entries with optional metadata filters.
func (s *Service) ListMemory(ctx context.Context, opts memory.SearchOptions) ([]memory.StoredMemory, error) {
	return s.memoryManager(ctx).Search(opts)
}

// ListMemoryCandidates returns pending durable-memory candidates.
func (s *Service) ListMemoryCandidates(ctx context.Context) ([]memory.Candidate, error) {
	return s.memoryManager(ctx).ListCandidates()
}

// ListMemorySuppressions returns dismissed-candidate suppressions that are
// still active for automatic bridge dedupe.
func (s *Service) ListMemorySuppressions(ctx context.Context) ([]memory.CandidateSuppression, error) {
	return s.memoryManager(ctx).ListSuppressions()
}

// ListMemoryAudit returns recent durable-memory audit entries.
func (s *Service) ListMemoryAudit(ctx context.Context, limit int) ([]memory.AuditLogEntry, error) {
	return s.memoryManager(ctx).ListAudit(limit)
}

// PreviewMemoryContext returns the layered memory context for a candidate query.
func (s *Service) PreviewMemoryContext(ctx context.Context, query string) (memory.ContextLayers, error) {
	layers, err := s.memoryManager(ctx).PreviewContextLayers(query)
	if err != nil {
		return memory.ContextLayers{}, err
	}
	layers.Identity = append([]memory.RelevantMemory{}, layers.Identity...)
	layers.Core = append([]memory.RelevantMemory{}, layers.Core...)
	layers.Relevant = append([]memory.RelevantMemory{}, layers.Relevant...)
	return layers, nil
}

// RememberMemory stores one durable memory entry.
func (s *Service) RememberMemory(ctx context.Context, input memory.SaveInput) (*memory.Entry, error) {
	return s.memoryManager(ctx).Remember(input)
}

// UpdateMemory modifies one durable memory entry.
func (s *Service) UpdateMemory(ctx context.Context, input memory.UpdateInput) (*memory.Entry, error) {
	return s.memoryManager(ctx).Update(input)
}

// ForgetMemory deletes one durable memory entry.
func (s *Service) ForgetMemory(ctx context.Context, input memory.ForgetInput) (*memory.Entry, error) {
	return s.memoryManager(ctx).Forget(input)
}

// ArchiveMemory soft-hides one durable memory entry from recall/injection.
func (s *Service) ArchiveMemory(ctx context.Context, input memory.ForgetInput) (*memory.Entry, error) {
	return s.memoryManager(ctx).Archive(input)
}

// RestoreMemory un-archives one durable memory entry.
func (s *Service) RestoreMemory(ctx context.Context, input memory.ForgetInput) (*memory.Entry, error) {
	return s.memoryManager(ctx).Restore(input)
}

// ListMilestoneMemories returns active project memories whose titles read as
// completed implementation milestones.
func (s *Service) ListMilestoneMemories(ctx context.Context) ([]memory.Entry, error) {
	return s.memoryManager(ctx).ListMilestoneMemories()
}

// ArchiveMilestones archives all completed-milestone memories in one shot.
func (s *Service) ArchiveMilestones(ctx context.Context) ([]memory.Entry, error) {
	return s.memoryManager(ctx).ArchiveMilestones()
}

// RemoveMemorySuppression deletes one dismissed-candidate suppression.
func (s *Service) RemoveMemorySuppression(ctx context.Context, keyOrFingerprint string) error {
	return s.memoryManager(ctx).RemoveSuppression(keyOrFingerprint)
}

// AcceptMemoryCandidate promotes one pending candidate into durable memory.
func (s *Service) AcceptMemoryCandidate(ctx context.Context, input memory.AcceptCandidateInput) (*memory.Entry, error) {
	input.Fingerprint = strings.TrimSpace(input.Fingerprint)
	return s.memoryManager(ctx).AcceptCandidateWithOptions(input)
}

// DismissMemoryCandidate removes one pending candidate without storing it.
func (s *Service) DismissMemoryCandidate(ctx context.Context, fingerprint string) (*memory.Candidate, error) {
	return s.memoryManager(ctx).DismissCandidate(strings.TrimSpace(fingerprint))
}

// RestoreMemoryAudit restores one durable-memory audit snapshot.
func (s *Service) RestoreMemoryAudit(ctx context.Context, auditID, target string) (*memory.AuditLogEntry, error) {
	return s.memoryManager(ctx).RestoreAudit(auditID, target)
}

// MineProjectMemoryCandidates scans high-signal project docs and turns them
// into reviewed memory candidates.
func (s *Service) MineProjectMemoryCandidates(ctx context.Context) ([]memory.Candidate, error) {
	extractor := memory.NewExtractor(s.memoryManager(ctx), "")
	return extractor.CaptureProjectDocs(s.cfg.WorkspaceDir)
}

// DigestMemory analyzes transcript/candidate signals and adds reviewable memory
// candidates without writing durable memory directly.
func (s *Service) DigestMemory(ctx context.Context) (MemoryDigestResult, error) {
	if owner, ok := memory.OwnerFromContext(ctx); ok && (owner.TenantID != "" || owner.UserID != "local-operator") {
		return MemoryDigestResult{}, fmt.Errorf("use desktop-scoped Dream to organize work memory")
	}
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
	extractor := memory.NewExtractor(s.memoryManager(ctx), "")
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

// ListLegacyMemory is explicit operator-only history; it is never a recall fallback.
func (s *Service) ListLegacyMemory(ctx context.Context, opts memory.SearchOptions) ([]memory.StoredMemory, error) {
	if owner, ok := memory.OwnerFromContext(ctx); ok && (owner.TenantID != "" || owner.UserID != "local-operator") {
		return nil, fmt.Errorf("legacy memory belongs to the local operator")
	}
	return memory.NewManager(s.cfg.MemoryDir).Search(opts)
}
