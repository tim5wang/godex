package backend

import (
	"github.com/tim5wang/godex/internal/core/templates"
)

// Agent template service methods (agent talent market, M2):
// thin wrappers over the core templates Manager so the HTTP API and future
// consumers (taskboard M3, BizAPIKey convergence) share one access path.

func (s *Service) templateManager() *templates.Manager {
	return templates.NewManager(s.cfg.StateDir, s.cfg.SkillsDir)
}

// ListAgentTemplates returns all templates: builtin, user-defined, and
// package-derived read-only ones.
func (s *Service) ListAgentTemplates() ([]templates.AgentTemplate, error) {
	return s.templateManager().List()
}

// GetAgentTemplate resolves one template by ID.
func (s *Service) GetAgentTemplate(id string) (templates.AgentTemplate, error) {
	return s.templateManager().Get(id)
}

// SaveAgentTemplate creates or updates a user-defined template.
func (s *Service) SaveAgentTemplate(t templates.AgentTemplate) error {
	return s.templateManager().Save(t)
}

// DeleteAgentTemplate removes a user-defined template.
func (s *Service) DeleteAgentTemplate(id string) error {
	return s.templateManager().Delete(id)
}

// ValidateAgentTemplate resolves a template and returns it together with
// reference warnings (e.g. skills that are not installed) without applying
// anything.
func (s *Service) ValidateAgentTemplate(id string) (templates.AgentTemplate, []string, error) {
	return s.templateManager().Resolve(id)
}
