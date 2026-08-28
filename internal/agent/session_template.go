package agent

import (
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/core/templates"
	"github.com/tim5wang/godex/internal/tools"
)

// ApplyTemplate applies an agent talent-market template to the session
// (docs/agent-role-and-bundle-design.md M1). It is called once at session
// load, after ApplySessionMode, so an explicit template wins over the legacy
// mode mapping. Like sessionMode the template is fixed for the session
// lifetime: the persona/base prompt live in the stable system prompt and the
// initial tool set does not change mid-session, keeping the prompt prefix
// (and provider prefix-cache hits) stable.
//
// Tool semantics are EXACT: the session gets precisely Tools ∪ bundle-tools
// (SetActiveToolsExact), with no force-preserved always-active extras. Meta
// tools that must stay reachable must be listed explicitly or via the
// "always_on" virtual bundle. Legacy session modes keep SetActiveTools,
// which preserves always-active tools for backward compatibility.
func (a *Agent) ApplyTemplate(t templates.AgentTemplate) {
	memoryMode := templates.NormalizeMemoryMode(t.Memory)
	a.mu.Lock()
	a.templateID = strings.TrimSpace(t.ID)
	a.templatePersona = strings.TrimSpace(t.Persona)
	a.templateBasePrompt = strings.TrimSpace(t.BasePrompt)
	a.templateProfile = strings.ToLower(strings.TrimSpace(t.Profile))
	a.templateTrimHeavy = t.TrimHeavySections
	a.templateSkills = append([]string(nil), t.Skills...)
	a.templateMemoryMode = memoryMode
	a.mu.Unlock()

	// Scoped template memory pins the session's durable memory to its own
	// partition (even when the global memory.session_scope is disabled).
	// Shared (default) keeps whatever the global config produced.
	if memoryMode == templates.MemoryScoped {
		a.applyScopedMemory()
	}

	switch {
	case len(t.Tools) > 0 && len(t.Bundles) > 0:
		// Union: explicit tools on top of the bundle presets.
		names := toolNamesForBundles(a.toolHandler.Catalog(), t.Bundles)
		a.toolHandler.SetActiveToolsExact(append(names, t.Tools...)...)
	case len(t.Tools) > 0:
		// Exact allowlist: the session gets exactly the listed tools.
		a.toolHandler.SetActiveToolsExact(t.Tools...)
	case len(t.Bundles) > 0:
		if names := toolNamesForBundles(a.toolHandler.Catalog(), t.Bundles); len(names) > 0 {
			a.toolHandler.SetActiveToolsExact(names...)
		}
	}
}

// applyScopedMemory rebuilds the agent's memory manager (and the memory tool)
// against a per-session partition so durable memory never leaks across
// sessions — the template-level equivalent of cfg.Memory.SessionScope.
func (a *Agent) applyScopedMemory() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil || strings.TrimSpace(a.sessionID) == "" {
		return
	}
	scopedMgr := memory.NewScopedManager(a.cfg.MemoryDir, scope.Session(a.sessionID))
	a.memoryMgr = scopedMgr
	a.memoryExt, a.memoryStrategy = buildMemoryStack(a.cfg, a.client, scopedMgr)
	// Rebind the memory tool so reads/writes stay inside the session partition.
	a.registerToolTo(a.toolHandler, tools.NewMemoryTool(scopedMgr), tools.ToolMeta{AlwaysActive: true})
}

// memoryMode returns the normalized template memory scope.
func (a *Agent) memoryMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.templateMemoryMode
}

// ActivateBundles incrementally activates the named bundles on top of the
// current active set (no removal). The taskboard executor uses this to
// guarantee the taskboard tool stays callable in a template-pinned execution
// session whose exact preset may not include the task_board bundle.
func (a *Agent) ActivateBundles(names ...string) {
	a.toolHandler.ActivateBundles(names...)
}

// toolNamesForBundles resolves the union of tool names registered in the
// named bundles, preserving catalog order. Unknown bundle names are ignored.
func toolNamesForBundles(cat tools.ToolCatalog, bundles []string) []string {
	set := make(map[string]struct{}, len(bundles))
	for _, b := range bundles {
		set[strings.ToLower(strings.TrimSpace(b))] = struct{}{}
	}
	names := make([]string, 0, 16)
	for _, b := range cat.Bundles {
		if _, ok := set[strings.ToLower(strings.TrimSpace(b.Name))]; !ok {
			continue
		}
		names = append(names, b.Tools...)
	}
	return names
}

// TemplateID returns the ID of the applied template ("" when none).
func (a *Agent) TemplateID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.templateID
}

// TemplateSkills returns the template's preset skills; the backend loads
// them for new sessions when no explicit per-session skill preset was
// requested.
func (a *Agent) TemplateSkills() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.templateSkills...)
}

// templatePersonaPrompt returns the persona prompt ("" when unset). Persona
// is creation-time fixed, so it safely lives in the stable system prompt.
func (a *Agent) templatePersonaPrompt() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.templatePersona
}

// templateBasePromptSection returns the template's behavioral boundary rules
// ("" when unset), appended after the capability-check section.
func (a *Agent) templateBasePromptSection() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.templateBasePrompt
}

// effectiveTemplateProfile returns the template's profile override when set,
// otherwise the passed-in (context/config) profile.
func (a *Agent) effectiveTemplateProfile(agentProfile string) string {
	profile := config.NormalizeAgentProfile(agentProfile)
	a.mu.Lock()
	tp := a.templateProfile
	a.mu.Unlock()
	if tp != "" {
		profile = config.NormalizeAgentProfile(tp)
	}
	return profile
}

// promptTrimHeavySections reports whether the heavyweight background prompt
// sections (skill catalog / repo map / active skills) should be omitted:
// either the legacy minimal mode or a template with trim_heavy_sections.
func (a *Agent) promptTrimHeavySections() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionMode == SessionModeMinimal || a.templateTrimHeavy
}

// RegisterBaseToolsForCatalog registers the built-in tools into a throwaway
// handler (no package runtime activation, no session state) and returns the
// resulting catalog. It backs the template-form options endpoint so
// bundle/tool references can be picked from the live registration instead
// of typed free-form.
func (a *Agent) RegisterBaseToolsForCatalog() tools.ToolCatalog {
	a.registerToolsWith(a.toolHandler)
	return a.toolHandler.Catalog()
}
