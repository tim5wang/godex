// Package templates implements the agent template registry ("agent talent
// market"): reusable, named presets of an agent's capability boundary
// (bundles, tools, skills, MCP servers, persona prompt, profile, write scope)
// selected at session creation time. Design: docs/agent-role-and-bundle-design.md.
package templates

import "strings"

// Template source markers surfaced through the API so the UI can group
// builtin / user / package-derived cards.
const (
	SourceBuiltin = "builtin"
	SourceUser    = "user"
	SourcePackage = "package"
)

// ProfileAgent mirrors config.AgentProfile values usable in AgentTemplate.Profile.
const (
	ProfileGeneral = "general"
	ProfileCoding  = "coding"
)

// Memory scopes for AgentTemplate.Memory. Empty string means MemoryShared.
const (
	MemoryNone   = "none"
	MemoryShared = "shared"
	MemoryScoped = "scoped"
)

// NormalizeMemoryMode validates and canonicalizes a memory scope string.
// Empty or unknown values fall back to the default shared scope. The
// returned value is always one of MemoryNone / MemoryShared / MemoryScoped.
func NormalizeMemoryMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case MemoryNone:
		return MemoryNone
	case MemoryScoped:
		return MemoryScoped
	default:
		return MemoryShared
	}
}

// Builtin template IDs. "default" reproduces the legacy standard mode
// behavior (full default-active tool set), "minimal" reproduces the legacy
// minimal mode so the Web UI mode selector can map onto templates without a
// behavior change.
const (
	BuiltinDefault          = "default"
	BuiltinMinimal          = "minimal"
	BuiltinGeneralAssistant = "general-assistant"
	BuiltinCoder            = "coder"
	BuiltinResearcher       = "researcher"
	BuiltinReviewer         = "reviewer"
	BuiltinPlanner          = "planner"
)

// AgentTemplate is a named, creation-time preset of an agent's capability
// boundary. Field naming intentionally mirrors the BizAPIKey whitelist fields
// so the M4 convergence (BizAPIKey.template_id + overrides) needs no rename.
type AgentTemplate struct {
	// Identity.
	ID          string   `json:"id" yaml:"id"`
	Name        string   `json:"name" yaml:"name"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Avatar      string   `json:"avatar,omitempty" yaml:"avatar,omitempty"` // emoji or image URL; empty falls back to initial-letter avatar
	Color       string   `json:"color,omitempty" yaml:"color,omitempty"`   // accent color for the card / letter avatar
	Scenarios   []string `json:"scenarios,omitempty" yaml:"scenarios,omitempty"`

	// Capability boundary.
	Bundles      []string `json:"bundles,omitempty" yaml:"bundles,omitempty"`     // initial active bundles (tools of these bundles)
	Tools        []string `json:"tools,omitempty" yaml:"tools,omitempty"`         // explicit tool allowlist; wins over Bundles
	WriteEnabled bool     `json:"write_enabled,omitempty" yaml:"write_enabled,omitempty"`
	WriteScope   []string `json:"write_scope,omitempty" yaml:"write_scope,omitempty"`
	MCPServers   []string `json:"mcp_servers,omitempty" yaml:"mcp_servers,omitempty"`
	Skills       []string `json:"skills,omitempty" yaml:"skills,omitempty"`
	Packages     []string `json:"packages,omitempty" yaml:"packages,omitempty"`

	// Persona and habits.
	Persona    string `json:"persona,omitempty" yaml:"persona,omitempty"`       // personality prompt, prepended to the dynamic system prompt
	Profile    string `json:"profile,omitempty" yaml:"profile,omitempty"`       // "general" / "coding" agent profile override
	BasePrompt string `json:"base_prompt,omitempty" yaml:"base_prompt,omitempty"` // behavioral boundary rules

	// Resource hints.
	ModelHint  string `json:"model_hint,omitempty" yaml:"model_hint,omitempty"`
	BudgetHint string `json:"budget_hint,omitempty" yaml:"budget_hint,omitempty"`

	// Prompt shaping: omit the heavyweight background sections (skill
	// catalog / repo map / active skills) from the runtime prompt. true on
	// lean templates (e.g. "minimal") to keep the stable prefix small.
	TrimHeavySections bool `json:"trim_heavy_sections,omitempty" yaml:"trim_heavy_sections,omitempty"`

	// Memory scope for this template's sessions: "none" (no memory index
	// injected, no candidate capture), "shared" (default; workspace-level
	// durable memory), or "scoped" (per-session isolated memory partition
	// even when the global memory.session_scope is disabled). Empty = shared.
	Memory string `json:"memory,omitempty" yaml:"memory,omitempty"`

	// Reserved (Q2, project-scope overrides): not resolved in M1.
	ProjectDir string `json:"project_dir,omitempty" yaml:"project_dir,omitempty"`

	// Source is computed by the Manager (builtin / user / package); it is
	// not read from disk.
	Source string `json:"source,omitempty" yaml:"-"`
}

// IsBuiltIn reports whether the template is read-only (builtin or
// package-derived).
func (t AgentTemplate) IsBuiltIn() bool {
	return t.Source != SourceUser
}

// IsZeroish reports whether the template carries no preset at all (used by
// the runtime chain to skip work for the "default" template).
func (t AgentTemplate) IsZeroish() bool {
	return len(t.Bundles) == 0 && len(t.Tools) == 0 && t.Persona == "" &&
		t.Profile == "" && !t.TrimHeavySections && len(t.Skills) == 0
}
