package backend

import (
	"github.com/tim5wang/godex/internal/agent"
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

// ToolBundleOption is one registered bundle with its tool names, for the
// template editor's bundle/tool pickers.
type ToolBundleOption struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary,omitempty"`
	Tools   []string `json:"tools,omitempty"`
}

// TemplateFormOptions is the authoritative choice list for the template
// editor form. Bundles/tools come from a throwaway agent's base tool
// registration (live source of truth); engines come from the same agent's
// registered harness registry; skills, MCP servers and packages come from
// their own list endpoints on the frontend.
type TemplateFormOptions struct {
	Bundles []ToolBundleOption `json:"bundles"`
	Tools   []string           `json:"tools"`
	Engines []string           `json:"engines"`
}

// AgentTemplateFormOptions builds the bundle/tool/engine choice lists without
// touching session state or activating package runtimes.
func (s *Service) AgentTemplateFormOptions() *TemplateFormOptions {
	probe := agent.NewForSession(s.cfg, s.shared, "")
	cat := probe.RegisterBaseToolsForCatalog()
	out := &TemplateFormOptions{Bundles: []ToolBundleOption{}, Tools: []string{}}
	seen := map[string]bool{}
	for _, b := range cat.Bundles {
		out.Bundles = append(out.Bundles, ToolBundleOption{Name: b.Name, Summary: b.Summary, Tools: b.Tools})
		for _, name := range b.Tools {
			if !seen[name] {
				seen[name] = true
				out.Tools = append(out.Tools, name)
			}
		}
	}
	// Engines: the built-in godex engine plus every harness registered on the
	// probe agent (e.g. "acp:codex" from config.acp.agents). The probe is a
	// fresh agent whose ACP harnesses are wired exactly like a live session's,
	// so the dropdown mirrors the real runtime registry.
	out.Engines = probe.RegisteredHarnessIDs()
	return out
}
