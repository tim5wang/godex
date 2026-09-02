package agent

import (
	"strings"

	"github.com/tim5wang/godex/internal/agent/activation"
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
// capability baseline remains fixed for the session. tool_exchange may change
// ordinary bundles, but cannot add or remove the template-pinned always_on
// bundle; ClearMessages restores the fixed baseline.
//
// Tool semantics are EXACT: the session gets precisely Tools ∪ bundle-tools
// (SetActiveToolsExact), with no force-preserved always-active extras. Meta
// tools that must stay reachable must be listed explicitly or via the
// "always_on" virtual bundle. The empty built-in default template is the sole
// compatibility exception and restores registration defaults. Legacy session
// modes keep SetActiveTools, which preserves always-active tools.
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
	a.templateEngine = a.resolveTemplateEngine(t.Engine)
	a.mu.Unlock()

	// Scoped template memory pins the session's durable memory to its own
	// partition (even when the global memory.session_scope is disabled).
	// Shared (default) keeps whatever the global config produced.
	if memoryMode == templates.MemoryScoped {
		a.applyScopedMemory()
	}

	plan := activation.Resolve(t, a.toolHandler.Catalog())
	if plan.Mode == activation.RegistrationDefaults {
		a.toolHandler.ResetActiveToolsToDefaults()
	} else {
		a.toolHandler.SetActiveToolsExact(plan.ToolNames...)
	}

	baseline := a.toolHandler.ActiveToolNames()
	a.mu.Lock()
	a.templateToolBaseline = baseline
	a.templateToolBaselineSet = true
	a.mu.Unlock()
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

// resolveTemplateEngine canonicalizes a template engine request against the
// engines actually registered on this agent. Caller must hold a.mu. An empty
// or "godex" value keeps the default engine; a non-godex id must be present
// in extraHarnesses (e.g. "acp:codex" registered by
// RegisterConfiguredACPHarnesses), otherwise it falls back to godex so an
// unknown engine never rejects session creation.
func (a *Agent) resolveTemplateEngine(raw string) string {
	id := templates.NormalizeEngineID(raw)
	if id == templates.EngineDefault {
		return templates.EngineDefault
	}
	if a.extraHarnesses != nil {
		if _, ok := a.extraHarnesses[id]; ok {
			return id
		}
	}
	return templates.EngineDefault
}

// TemplateEngine returns the template-pinned run kernel (harness id), or
// "godex" when no template (or no engine) pins one. Per-turn explicit requests
// from the caller may override this for a single turn (roadmap 6.4).
func (a *Agent) TemplateEngine() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(a.templateEngine) == "" {
		return templates.EngineDefault
	}
	return a.templateEngine
}

// ActivateBundles incrementally activates the named bundles on top of the
// current active set (no removal). The taskboard executor uses this to
// guarantee the taskboard tool stays callable in a template-pinned execution
// session whose exact preset may not include the task_board bundle.
func (a *Agent) ActivateBundles(names ...string) {
	a.toolHandler.ActivateBundles(names...)
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
