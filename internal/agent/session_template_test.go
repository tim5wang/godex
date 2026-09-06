package agent

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/templates"
)

func researcherLikeTemplate() templates.AgentTemplate {
	return templates.AgentTemplate{
		ID:                "test-researcher",
		Name:              "Test Researcher",
		Bundles:           []string{"web", "browser"},
		Persona:           "You are a thorough test researcher.",
		TrimHeavySections: true,
		Skills:            []string{"alpha", "beta"},
	}
}

func TestApplyTemplateActivatesBundleToolsOnly(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	a.ApplyTemplate(researcherLikeTemplate())

	if got := a.TemplateID(); got != "test-researcher" {
		t.Fatalf("TemplateID = %q, want test-researcher", got)
	}
	// Bundle tools are active.
	for _, name := range []string{"web_search", "web_fetch"} {
		if !a.toolHandler.IsActive(name) {
			t.Fatalf("expected %s active from web bundle", name)
		}
	}
	// Default-active core tools are NOT part of the template's bundle set.
	if a.toolHandler.IsActive("bash") {
		t.Fatal("expected bash inactive for a web/browser-only template")
	}
	// Exact semantics: always-active meta tools are NOT force-preserved;
	// templates that need them list them explicitly (e.g. via always_on).
	if a.toolHandler.IsActive("memory") {
		t.Fatal("expected exact semantics: memory not force-preserved without always_on")
	}
}

func TestApplyTemplateToolsAllowlistWins(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	tpl := templates.AgentTemplate{
		ID:    "allowlist",
		Tools: []string{"read_file"},
	}
	a.ApplyTemplate(tpl)

	if !a.toolHandler.IsActive("read_file") {
		t.Fatal("expected read_file active via tools allowlist")
	}
	if a.toolHandler.IsActive("web_search") {
		t.Fatal("expected web_search inactive: tools allowlist wins over nothing else")
	}
	if a.toolHandler.IsActive("memory") {
		t.Fatal("expected exact semantics: memory not force-preserved by tools allowlist")
	}
}

func TestApplyTemplateExactToolSetReproducesLeanPreset(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	// The reported scenario: a template listing only edit_file + bash must
	// yield exactly those tools — no force-preserved always-active extras.
	a.ApplyTemplate(templates.AgentTemplate{
		ID:    "lean",
		Tools: []string{"edit_file", "bash"},
	})

	for _, name := range []string{"edit_file", "bash"} {
		if !a.toolHandler.IsActive(name) {
			t.Fatalf("expected %s active", name)
		}
	}
	for _, name := range []string{"memory", "web_search", "read_file"} {
		if a.toolHandler.IsActive(name) {
			t.Fatalf("expected %s inactive in exact lean preset", name)
		}
	}
}

func TestApplyTemplateAlwaysOnBundleAndToolUnion(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	// always_on is a selectable virtual bundle of host-resident meta tools.
	a.ApplyTemplate(templates.AgentTemplate{ID: "meta-only", Bundles: []string{"always_on"}})
	if !a.toolHandler.IsActive("memory") {
		t.Fatal("expected always_on bundle to activate meta tools (memory)")
	}
	if a.toolHandler.IsActive("bash") {
		t.Fatal("expected bash inactive for always_on-only template")
	}

	// Tools and bundles combine as a union.
	a.ApplyTemplate(templates.AgentTemplate{ID: "union", Bundles: []string{"core_code"}, Tools: []string{"memory"}})
	if !a.toolHandler.IsActive("bash") || !a.toolHandler.IsActive("memory") {
		t.Fatal("expected union of core_code bundle tools and explicit memory tool")
	}
	if a.toolHandler.IsActive("web_search") {
		t.Fatal("expected web_search inactive for union preset")
	}
}

func TestApplyTemplateDefaultKeepsStockBehavior(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	def, err := templates.NewManager(t.TempDir(), "").Get(templates.BuiltinDefault)
	if err != nil {
		t.Fatalf("Get default template: %v", err)
	}
	a.ApplyTemplate(def)

	if !a.toolHandler.IsActive("grep") {
		t.Fatal("expected default template to keep default-active tools")
	}
	if a.promptTrimHeavySections() {
		t.Fatal("expected default template to keep heavy prompt sections")
	}
}

func TestClearMessagesRestoresTemplateToolBaseline(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(templates.AgentTemplate{
		ID:      "coding-with-meta",
		Bundles: []string{"core_code", "always_on"},
	})

	a.toolHandler.ActivateBundles("web")
	if !a.toolHandler.IsActive("web_search") {
		t.Fatal("expected web bundle to be active before clear")
	}

	a.ClearMessages()

	for _, name := range []string{"bash", "memory", "tool_exchange"} {
		if !a.toolHandler.IsActive(name) {
			t.Fatalf("expected template baseline tool %q active after clear", name)
		}
	}
	if a.toolHandler.IsActive("web_search") {
		t.Fatal("expected transient web bundle to be removed after clear")
	}
}

func TestClearMessagesKeepsExactToolTemplateLean(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(templates.AgentTemplate{
		ID:    "lean",
		Tools: []string{"edit_file", "bash"},
	})

	a.toolHandler.ActivateBundles("web")
	a.ClearMessages()

	for _, name := range []string{"edit_file", "bash"} {
		if !a.toolHandler.IsActive(name) {
			t.Fatalf("expected exact template tool %q active after clear", name)
		}
	}
	for _, name := range []string{"memory", "web_search", "read_file"} {
		if a.toolHandler.IsActive(name) {
			t.Fatalf("expected non-template tool %q inactive after clear", name)
		}
	}
}

func TestApplyTemplateEmptyCustomCapabilitySetIsExact(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	a.ApplyTemplate(templates.AgentTemplate{ID: "persona-only"})

	if got := a.toolHandler.ActiveToolNames(); len(got) != 0 {
		t.Fatalf("expected empty custom template capability set, got %v", got)
	}
}

func TestTemplatePersonaAndBasePromptInSystemPrompt(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(researcherLikeTemplate())

	prompt, err := a.buildDynamicSystemPrompt("")
	if err != nil {
		t.Fatalf("buildDynamicSystemPrompt: %v", err)
	}
	if !strings.Contains(prompt, "thorough test researcher") {
		t.Fatal("expected persona text in dynamic system prompt")
	}

	// Base prompt section is appended when set.
	a.mu.Lock()
	a.templateBasePrompt = "Stay strictly within task scope."
	a.mu.Unlock()
	prompt, err = a.buildDynamicSystemPrompt("")
	if err != nil {
		t.Fatalf("buildDynamicSystemPrompt: %v", err)
	}
	if !strings.Contains(prompt, "Stay strictly within task scope.") {
		t.Fatal("expected template base prompt in dynamic system prompt")
	}
}

func TestTemplateProfileOverridesContextProfile(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	tpl := templates.AgentTemplate{ID: "coding-forced", Profile: "coding"}
	a.ApplyTemplate(tpl)

	if got := a.effectiveTemplateProfile("general"); got != "coding" {
		t.Fatalf("effectiveTemplateProfile = %q, want coding", got)
	}
	if got := a.effectiveTemplateProfile("coding"); got != "coding" {
		t.Fatalf("effectiveTemplateProfile = %q, want coding", got)
	}
}

func TestTemplateTrimHeavySections(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(researcherLikeTemplate())

	if !a.promptTrimHeavySections() {
		t.Fatal("expected trim_heavy_sections template to trim heavy sections")
	}
	sections, err := a.buildDynamicRuntimePromptSections("")
	if err != nil {
		t.Fatalf("buildDynamicRuntimePromptSections: %v", err)
	}
	for _, s := range sections {
		switch s.Key {
		case "skill_catalog", "repo_map", "active_skills":
			t.Fatalf("section %q should be trimmed for lean templates", s.Key)
		}
	}
	// Environment and tool availability must survive trimming.
	keys := map[string]bool{}
	for _, s := range sections {
		keys[s.Key] = true
	}
	if !keys["environment"] || !keys["tool_availability"] {
		t.Fatalf("expected environment and tool_availability to survive, got %v", keys)
	}
}

func TestTemplateSkillsAccessorReturnsCopy(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.ApplyTemplate(researcherLikeTemplate())

	skills := a.TemplateSkills()
	if len(skills) != 2 {
		t.Fatalf("TemplateSkills = %v, want 2 entries", skills)
	}
	skills[0] = "mutated"
	if again := a.TemplateSkills(); again[0] == "mutated" {
		t.Fatal("TemplateSkills must return a defensive copy")
	}
}

func TestRestoreSessionStatePreservesTemplateToolSet(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(templates.AgentTemplate{ID: "lean", Tools: []string{"edit_file", "bash"}})

	state := a.ExportStateForSession("sess-restore")

	// Reload path (the reported scenario): template applied again at load,
	// then the persisted state restored. The tool-granularity active_tools
	// snapshot must reproduce the exact preset — the legacy derived
	// active_bundles marker would re-activate the whole core_code bundle.
	b := newTestAgent(t, 4096)
	b.RegisterTools()
	b.ApplyTemplate(templates.AgentTemplate{ID: "lean", Tools: []string{"edit_file", "bash"}})
	b.RestoreStateForSession("sess-restore", state)

	if !b.toolHandler.IsActive("bash") || !b.toolHandler.IsActive("edit_file") {
		t.Fatal("expected exact tool set preserved after restore")
	}
	if b.toolHandler.IsActive("memory") || b.toolHandler.IsActive("read_file") {
		t.Fatal("expected restore to keep exact tool set (no bundle amplification)")
	}

	// Legacy states written before active_tools existed fall back to the
	// legacy bundle-level restore.
	legacy := state
	legacy.ActiveTools = nil
	legacy.ActiveBundles = []string{"core_code"}
	c := newTestAgent(t, 4096)
	c.RegisterTools()
	c.RestoreStateForSession("sess-legacy", legacy)
	if !c.toolHandler.IsActive("bash") || !c.toolHandler.IsActive("memory") {
		t.Fatal("expected legacy bundle restore to keep working")
	}
}

func TestToolAvailabilityPromptListsExactActiveTools(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(templates.AgentTemplate{ID: "lean", Tools: []string{"edit_file", "bash"}})

	// The reported scenario: the schema set is exactly 2 tools, so the
	// tool_availability prompt's "Active tools" line must match — the old
	// formatter listed AlwaysActiveTools (a registration property) plus every
	// tool of bundles with any active tool, making the agent self-report 18.
	sections, err := a.buildDynamicRuntimePromptSections("")
	if err != nil {
		t.Fatalf("buildDynamicRuntimePromptSections: %v", err)
	}
	var availability string
	for _, s := range sections {
		if s.Key == "tool_availability" {
			availability = s.Text
		}
	}
	if availability == "" {
		t.Fatal("expected tool_availability section")
	}
	for _, line := range strings.Split(availability, "\n") {
		if strings.HasPrefix(line, "- Active tools: ") {
			if got := strings.TrimPrefix(line, "- Active tools: "); got != "bash, edit_file" {
				t.Fatalf("Active tools line = %q, want \"bash, edit_file\"", got)
			}
			return
		}
	}
	t.Fatal("no Active tools line found in tool_availability section")
}

func TestPromptOmitsToolExchangeGuidanceWhenToolNotActive(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(templates.AgentTemplate{ID: "lean", Tools: []string{"edit_file", "bash"}})

	// The reported scenario: a lean template without tool_exchange in its exact
	// active set must not be told to expand via a tool it cannot call, and must
	// not be shown an "available bundles" list it could never activate.
	sections, err := a.buildDynamicRuntimePromptSections("")
	if err != nil {
		t.Fatalf("buildDynamicRuntimePromptSections: %v", err)
	}
	var availability string
	for _, s := range sections {
		if s.Key == "tool_availability" {
			availability = s.Text
		}
	}
	if availability == "" {
		t.Fatal("expected tool_availability section")
	}
	if strings.Contains(availability, "tool_exchange") {
		t.Fatalf("tool_availability must not instruct tool_exchange when inactive: %s", availability)
	}
	if strings.Contains(availability, "Available bundles") {
		t.Fatalf("tool_availability must not advertise available bundles when tool_exchange is absent: %s", availability)
	}
	if !strings.Contains(availability, "not available on demand") {
		t.Fatalf("tool_availability should state the tool set is fixed: %s", availability)
	}

	dynamic, err := a.buildDynamicSystemPrompt("")
	if err != nil {
		t.Fatalf("buildDynamicSystemPrompt: %v", err)
	}
	if strings.Contains(dynamic, "tool_exchange") {
		t.Fatalf("capability_check must not instruct tool_exchange when inactive: %s", dynamic)
	}
}

func TestPromptKeepsToolExchangeGuidanceWhenToolActive(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	// always_on carries the host-resident meta tools including tool_exchange.
	a.ApplyTemplate(templates.AgentTemplate{ID: "meta", Bundles: []string{"always_on"}})

	sections, err := a.buildDynamicRuntimePromptSections("")
	if err != nil {
		t.Fatalf("buildDynamicRuntimePromptSections: %v", err)
	}
	var availability string
	for _, s := range sections {
		if s.Key == "tool_availability" {
			availability = s.Text
		}
	}
	if availability == "" {
		t.Fatal("expected tool_availability section")
	}
	if !strings.Contains(availability, "tool_exchange") {
		t.Fatalf("expected tool_exchange guidance when tool is active: %s", availability)
	}
	if !strings.Contains(availability, "Available bundles") {
		t.Fatalf("expected available bundles list when tool_exchange is active: %s", availability)
	}
	if !strings.Contains(availability, "always_on [template-pinned]") {
		t.Fatalf("expected active always_on bundle to be marked template-pinned: %s", availability)
	}
	available := strings.SplitN(availability, "- Available bundles: ", 2)
	if len(available) == 2 && strings.Contains(strings.SplitN(available[1], "\n", 2)[0], "always_on") {
		t.Fatalf("always_on must not be advertised as dynamically available: %s", availability)
	}
}

func TestApplyTemplateMemoryNoneSkipsInjectionAndCapture(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(templates.AgentTemplate{ID: "no-mem", Memory: "none"})

	if got := a.memoryMode(); got != templates.MemoryNone {
		t.Fatalf("memoryMode = %q, want none", got)
	}
	// Injection side: no durable-memory index message is produced.
	msg, tokens, err := a.buildMemoryIndexPromptMessage()
	if err != nil {
		t.Fatalf("buildMemoryIndexPromptMessage: %v", err)
	}
	if len(msg.Content) != 0 {
		t.Fatal("expected no memory index message for memory:none")
	}
	if tokens != 0 {
		t.Fatalf("expected 0 memory tokens, got %d", tokens)
	}
	// Capture side: candidate extraction is a no-op.
	if err := a.captureMemoryCandidates(); err != nil {
		t.Fatalf("captureMemoryCandidates: %v", err)
	}
	if err := a.CaptureInsightMemoryCandidates(nil); err != nil {
		t.Fatalf("CaptureInsightMemoryCandidates: %v", err)
	}
	// Live recall side: BuildContextLayers-driven memory messages must also be
	// suppressed (the injected memory index gate alone used to leave this path
	// leaking durable memory into the prompt for memory:none templates).
	msgs, layers, err := a.collectMemoryMessages(nil)
	if err != nil {
		t.Fatalf("collectMemoryMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatal("expected no memory recall messages for memory:none")
	}
	if len(layers.Identity) != 0 || len(layers.Core) != 0 || len(layers.Relevant) != 0 {
		t.Fatalf("expected empty memory layers for memory:none, got identity=%d core=%d relevant=%d",
			len(layers.Identity), len(layers.Core), len(layers.Relevant))
	}
}

func TestApplyTemplateMemoryScopedRebuildsManager(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.sessionID = "sess-memory-scoped-test"
	a.ApplyTemplate(templates.AgentTemplate{ID: "scoped", Memory: "scoped"})

	if got := a.memoryMode(); got != templates.MemoryScoped {
		t.Fatalf("memoryMode = %q, want scoped", got)
	}
	// The memory index should now resolve from the session-scoped manager and
	// name the session partition directory.
	msg, _, err := a.buildMemoryIndexPromptMessage()
	if err != nil {
		t.Fatalf("buildMemoryIndexPromptMessage: %v", err)
	}
	if len(msg.Content) == 0 {
		t.Fatal("expected scoped memory index message")
	}
	text := ""
	for _, b := range msg.Content {
		if b.Text != "" {
			text += b.Text
		}
	}
	if !strings.Contains(text, "sess-memory-scoped-test") {
		t.Fatalf("expected scoped memory directory to contain session id, got: %s", text)
	}
	// A session-scoped memory manager must have been installed.
	a.mu.Lock()
	mgr := a.memoryMgr
	a.mu.Unlock()
	if mgr == nil {
		t.Fatal("expected memory manager after scoped apply")
	}
}

func TestApplyTemplateMemorySharedDefault(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(templates.AgentTemplate{ID: "shared"})
	if got := a.memoryMode(); got != templates.MemoryShared {
		t.Fatalf("memoryMode = %q, want shared (default)", got)
	}
}

func TestApplyTemplatePinsRegisteredEngine(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.RegisterHarness("acp:codex", &fakeHarness{id: "acp:codex"})

	a.ApplyTemplate(templates.AgentTemplate{ID: "ext", Engine: "acp:codex"})

	if got := a.TemplateEngine(); got != "acp:codex" {
		t.Fatalf("TemplateEngine = %q, want acp:codex", got)
	}
}

func TestApplyTemplateUnknownEngineFallsBackToGodex(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	a.ApplyTemplate(templates.AgentTemplate{ID: "ext", Engine: "acp:not-registered"})

	if got := a.TemplateEngine(); got != templates.EngineDefault {
		t.Fatalf("TemplateEngine = %q, want %q (unknown engine falls back, never rejects)", got, templates.EngineDefault)
	}
}

func TestApplyTemplateEmptyEngineKeepsGodexDefault(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.ApplyTemplate(templates.AgentTemplate{ID: "plain", Name: "Plain"})
	if got := a.TemplateEngine(); got != templates.EngineDefault {
		t.Fatalf("TemplateEngine = %q, want %q", got, templates.EngineDefault)
	}
}

func TestTemplateEngineDefaultsToGodexWithoutTemplate(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	if got := a.TemplateEngine(); got != templates.EngineDefault {
		t.Fatalf("TemplateEngine = %q, want %q", got, templates.EngineDefault)
	}
}

func TestRegisteredHarnessIDs(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.RegisterHarness("acp:codex", &fakeHarness{id: "acp:codex"})
	a.RegisterHarness("acp:pi", &fakeHarness{id: "acp:pi"})

	ids := a.RegisteredHarnessIDs()
	if len(ids) != 3 {
		t.Fatalf("RegisteredHarnessIDs = %v, want 3 entries (godex + 2)", ids)
	}
	for _, want := range []string{"godex", "acp:codex", "acp:pi"} {
		if !containsString(ids, want) {
			t.Fatalf("RegisteredHarnessIDs = %v, missing %q", ids, want)
		}
	}
}

func TestApplyConfigReconcilesConfiguredACPHarnessIDs(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	first := *a.cfg
	first.ACP.Agents = map[string]config.ACPAgentConfig{
		"codex": {ID: "codex", Command: "/bin/true"},
	}
	a.ApplyConfig(&first, nil)
	if got := strings.Join(a.RegisteredHarnessIDs(), ","); got != "acp:codex,godex" {
		t.Fatalf("harness ids after add = %q", got)
	}
	second := first
	second.ACP.Agents = map[string]config.ACPAgentConfig{
		"pi": {ID: "pi", Command: "/bin/true"},
	}
	a.ApplyConfig(&second, nil)
	if got := strings.Join(a.RegisteredHarnessIDs(), ","); got != "acp:pi,godex" {
		t.Fatalf("harness ids after reconcile = %q", got)
	}
}
