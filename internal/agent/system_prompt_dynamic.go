package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/instructions"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/core/templates"
	"github.com/tim5wang/godex/internal/platform/textutil"
	"github.com/tim5wang/godex/internal/tools"
	"github.com/tim5wang/godex/internal/version"
)

type EnvironmentPromptInput struct {
	Version      version.Info
	WorkspaceDir string
	SkillsDir    string
	StateDir     string
	TempDir      string
	Shell        string
	Platform     string
	Timezone     string
	Now          time.Time
}

const (
	activeSkillsPromptTokenBudget = 6000
	activeSkillBlockTokenBudget   = 1800
	skillCatalogPromptTokenBudget = 1800
)

type runtimePromptSection struct {
	Key    string
	Kind   protocol.MessageKind
	Text   string
	Tokens int
}

// quasiStableSectionKeys change infrequently (daily rollover, bundle/skill
// activation, memory writes) and are placed BEFORE conversation history so
// the history prefix cache survives per-turn runtime churn. Volatile sections
// (todos, notifications, inbox, ledger) are appended AFTER history instead.
func quasiStableSectionKey(key string) bool {
	switch key {
	case "skill_catalog", "repo_map", "active_skills", "environment", "tool_availability":
		return true
	default:
		return false
	}
}

func (a *Agent) buildDynamicSystemPrompt(agentProfile string) (string, error) {
	instructionPrompt, err := a.buildInstructionPrompt()
	if err != nil {
		return "", err
	}
	profile := a.effectiveTemplateProfile(agentProfile)
	return strings.Join(filterNonEmpty(
		a.templatePersonaPrompt(),
		instructionPrompt,
		buildCodingProfilePrompt(profile),
		buildCapabilityCheckPromptForProfile(a.toolHandler.Catalog(), profile),
		a.templateBasePromptSection(),
	), "\n\n"), nil
}

func (a *Agent) buildDynamicRuntimePromptSections(agentProfile string) ([]runtimePromptSection, error) {
	profile := a.effectiveTemplateProfile(agentProfile)
	sections := make([]runtimePromptSection, 0, 5)

	// Minimal mode (roadmap new-chat mode): omit the heavyweight background
	// sections (repo map / skill catalog, active skills) so the prompt stays
	// small and focused on file/shell work. Environment and tool availability
	// are always kept because tools must know where they run.
	if !a.promptTrimHeavySections() {
		if profile != config.AgentProfileCoding {
			skillCatalogPrompt, err := a.buildSkillCatalogPrompt()
			if err != nil {
				return nil, err
			}
			sections = appendRuntimePromptSection(sections, "skill_catalog", protocol.KindBackground, skillCatalogPrompt)
		} else {
			sections = appendRuntimePromptSection(sections, "repo_map", protocol.KindBackground, a.repoMapSnapshotText())
		}
		sections = appendRuntimePromptSection(sections, "active_skills", protocol.KindBackground, buildActiveSkillsPrompt(a.activeSkillStates()))
	}
	sections = appendRuntimePromptSection(sections, "environment", protocol.KindBackground, buildEnvironmentPrompt(a.environmentPromptInput()))
	sections = appendRuntimePromptSection(sections, "tool_availability", protocol.KindBackground, buildToolAvailabilityPromptForProfile(a.toolHandler.Catalog(), profile))
	// P4 prompt/context contributor: append sections contributed by active
	// plugins (e.g. WASM plugins via pluginrt Manager.PromptSections). The
	// default host contributes nothing; wiring sets the provider.
	sections = append(sections, a.pluginPromptSections()...)

	return sections, nil
}

// pluginPromptSections returns prompt/context contributions from active
// plugins. The host (agent wiring) installs the provider; the default is empty
// so the section list is unchanged when no plugin manager is configured.
func (a *Agent) pluginPromptSections() []runtimePromptSection {
	if a == nil || a.pluginPromptProvider == nil {
		return nil
	}
	return a.pluginPromptProvider()
}

// SetPluginPromptProvider installs the hook that feeds plugin-contributed
// prompt sections (P4 prompt/context contributor) into the runtime prompt.
func (a *Agent) SetPluginPromptProvider(provider func() []runtimePromptSection) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pluginPromptProvider = provider
}

// buildMemoryIndexPromptMessage returns the durable memory index as a
// standalone quasi-stable message. It changes only on memory writes, so it
// travels ahead of conversation history alongside the other quasi-stable
// runtime sections instead of being rebuilt per-turn as dynamic content.
func (a *Agent) buildMemoryIndexPromptMessage() (protocol.Message, int, error) {
	// A template with memory: none injects no durable-memory index at all.
	if a.memoryMode() == templates.MemoryNone {
		return protocol.Message{}, 0, nil
	}
	memoryPrompt, err := a.memoryMgr.BuildPromptSection()
	if err != nil {
		return protocol.Message{}, 0, err
	}
	memoryPrompt = strings.TrimSpace(memoryPrompt)
	if memoryPrompt == "" {
		return protocol.Message{}, 0, nil
	}
	return protocol.NewEphemeralTextMessage(protocol.KindMemory, memoryPrompt), compress.CountTokens(memoryPrompt), nil
}

func appendRuntimePromptSection(sections []runtimePromptSection, key string, kind protocol.MessageKind, text string) []runtimePromptSection {
	text = strings.TrimSpace(text)
	if text == "" {
		return sections
	}
	return append(sections, runtimePromptSection{
		Key:    key,
		Kind:   kind,
		Text:   text,
		Tokens: compress.CountTokens(text),
	})
}

func runtimePromptMessages(sections []runtimePromptSection) []protocol.Message {
	messages := make([]protocol.Message, 0, 2)
	backgroundSections := make([]string, 0, len(sections))
	for _, section := range sections {
		switch section.Kind {
		case protocol.KindMemory:
			messages = append(messages, protocol.NewEphemeralTextMessage(protocol.KindMemory, section.Text))
		default:
			backgroundSections = append(backgroundSections, section.Text)
		}
	}
	if len(backgroundSections) > 0 {
		text := "# Runtime Prompt State\n\n" + strings.Join(backgroundSections, "\n\n")
		messages = append(messages, protocol.NewEphemeralTextMessage(protocol.KindBackground, text))
	}
	return messages
}

func runtimePromptSectionTokenMap(sections []runtimePromptSection) map[string]int {
	if len(sections) == 0 {
		return nil
	}
	out := make(map[string]int, len(sections))
	for _, section := range sections {
		out[section.Key] += section.Tokens
	}
	return out
}

func (a *Agent) environmentPromptInput() EnvironmentPromptInput {
	now := a.now()
	timezone := "Local"
	if loc := now.Location(); loc != nil && loc.String() != "" {
		timezone = loc.String()
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "unknown"
	} else {
		shell = filepath.Base(shell)
	}

	return EnvironmentPromptInput{
		Version:      version.Current(),
		WorkspaceDir: a.cfg.WorkspaceDir,
		SkillsDir:    a.cfg.SkillsDir,
		StateDir:     a.cfg.StateDir,
		TempDir:      a.cfg.TempDir,
		Shell:        shell,
		Platform:     runtime.GOOS,
		Timezone:     timezone,
		Now:          now,
	}
}

func buildEnvironmentPrompt(input EnvironmentPromptInput) string {
	lines := []string{
		"# Environment",
		"This is optional runtime context. Use it only when it is relevant to the task.",
		fmt.Sprintf("- GoDex version: %s", input.Version.Version),
		fmt.Sprintf("- Workspace root: %s", input.WorkspaceDir),
		fmt.Sprintf("- Skills directory: %s", input.SkillsDir),
		fmt.Sprintf("- State directory: %s", input.StateDir),
		fmt.Sprintf("- Temporary files directory: %s", input.TempDir),
		fmt.Sprintf("- Shell: %s", input.Shell),
		fmt.Sprintf("- Platform: %s", input.Platform),
		fmt.Sprintf("- Timezone: %s", input.Timezone),
		"- File tools use workspace-relative paths.",
		"- Inbox and background updates are ephemeral runtime context and are not persisted automatically.",
		"- Conversation history may be compacted automatically when it grows large.",
	}
	return strings.Join(lines, "\n")
}

// buildEnvironmentDatePrompt renders the volatile date/weekday line. It is kept
// OUT of the stable # Environment section (which sits before conversation
// history): the daily rollover would invalidate the provider prefix cache for
// the whole history. It is appended in the volatile tail instead, where its
// churn is uncached but tiny.
func buildEnvironmentDatePrompt(now time.Time) string {
	return fmt.Sprintf("- Local date: %s\n- Weekday: %s", now.Format("2006-01-02"), now.Weekday())
}

func buildCapabilityCheckPrompt(catalog tools.ToolCatalog) string {
	// tool_exchange drives on-demand bundle activation. When it is not in the
	// exact active tool set (lean template presets), guidance must not tell
	// the model to use it; swap those lines for an honest "not available".
	hasToolExchange := catalogHasActiveTool(catalog, "tool_exchange")
	// Only mention web/browser tooling when those tools are actually active:
	// naming inactive tools teaches the model to advertise them as "unavailable
	// capabilities" in its replies (the exact noise observed in lean-template
	// sessions' self-introductions).
	hasWeb := catalogHasActiveTool(catalog, "web_search") || catalogHasActiveTool(catalog, "web_fetch")
	hasBrowser := catalogHasActiveTool(catalog, "browser")

	lines := []string{
		"# Capability Check",
		"Check what is already configured in this workspace before calling a capability unavailable.",
	}
	if hasToolExchange {
		lines = append(lines,
			"- Start with relevant skills and active tools, then use tool_exchange if another bundle would help.",
			"- Keep the active tool workspace small: when calling tool_exchange, disable bundles that are clearly irrelevant to the current conversation before or after enabling new ones.",
			"- For obvious current-information requests such as weather, news, prices, stocks, exchange rates, scores, schedules, flights, or latest/recent status, use web_search directly when web is active; if web is inactive, call tool_exchange once with enable_bundles=[\"web\"] rather than querying for a weather/search tool.",
			"- If tool_exchange returns no match, do not repeat similar capability queries; switch to active tools or say the capability is unavailable.",
			"- When delegating web or current-information research to durable subagents, pass required_bundles=[\"web\"] after web is active; if subagent reports subagent_capability_required, enable the missing bundle with tool_exchange and retry once.",
		)
	} else {
		lines = append(lines,
			"- This session's tool set is fixed; capabilities not listed below are not available on demand.",
		)
		if hasWeb {
			lines = append(lines,
				"- For obvious current-information requests such as weather, news, prices, stocks, exchange rates, scores, schedules, flights, or latest/recent status, use web_search directly when web is active; if web is inactive, state that web search is not available in this session.",
			)
		}
	}
	if hasWeb || hasBrowser {
		lines = append(lines,
			"- Prefer web for current information, browser for dynamic pages, and glob for broad file discovery.",
			"- After web_search or web_fetch returns useful results, synthesize from ranked results, fetched previews, metadata, and chunks; fetch one specific new URL only when more detail is needed. If web_fetch reports needs_browser, use the provided fallback_hint (a proven curl/GitHub-API/npm-registry bypass, see docs/tools_issues.md) or the browser tool for dynamic pages. Do not repeat the same search query or fetch the same URL.",
		)
	} else {
		lines = append(lines, "- Prefer glob for broad file discovery.")
	}
	lines = append(lines,
		"- When the user explicitly asks you to read, inspect, review, or verify specific workspace files or code paths, use the relevant file or shell tools before giving findings.",
		"- When using durable subagents, use subagent wait for any/all completion instead of repeatedly polling subagent status; use subagent logs only for bounded diagnostics.",
		"- When a tool generates a local file such as a screenshot or export, treat it as a generated artifact. In supported runtimes the artifact may be attached automatically, so do not claim you can only provide a local path unless the user explicitly asks for the path.",
		"- When the user wants a local file sent or attached without reading its contents, prefer attach_file instead of read_file.",
		"- Do not use read_file for binary or large artifacts such as PDFs, images, media, or archives when the user only wants them copied, attached, or sent.",
		"- Use extracted attachment content directly when available; if parsing is unavailable for an attached file type, say so plainly.",
		"- When the user asks what was said earlier, previously, or before, and the current context no longer holds the detail, use history_search to retrieve short snippets from current_session or session_archive.",
		"- Do not use history_search as general knowledge lookup, and do not turn history_search hits into durable memory automatically.",
		"- When making multiple independent edits, batch them: use one edit_file call with edits[] for several changes to the same file, or files[] for coordinated changes across multiple files (all-or-nothing validation); also batch read_file/grep/ls calls that have no dependencies into one message.",
		"- For large structural deletions in big files, first locate boundaries with one dry-run grep/bash that prints candidate anchors with line numbers, confirm them, then cut once; do not cut by trial and error.",
		"- After deleting or renaming a symbol, check for leftover references (lsp references or grep) and fix them in the same batch before moving on.",
		"- Run verification that mirrors the project's real build/test command (check Makefile/package.json scripts); incremental or partial checkers can miss errors that the real build catches.",
		"- Skip canned self-introductions and stay focused on the request.",
	)
	if catalogHasTool(catalog, "cron") || catalogHasTool(catalog, "heartbeat") {
		lines = append(lines, "- After changing cron or heartbeat, report the resulting schedule, timezone, and enabled state.")
	}
	return strings.Join(lines, "\n")
}

func buildCapabilityCheckPromptForProfile(catalog tools.ToolCatalog, profile string) string {
	if config.NormalizeAgentProfile(profile) != config.AgentProfileCoding {
		return buildCapabilityCheckPrompt(catalog)
	}
	// tool_exchange drives on-demand bundle activation; when it is not in the
	// exact active tool set (lean template presets), do not tell the model to
	// use it — swap those lines for an honest "not available" note.
	hasToolExchange := catalogHasActiveTool(catalog, "tool_exchange")
	hasLSP := catalogHasActiveTool(catalog, "lsp")
	lines := []string{
		"# Coding Profile",
		"Default to the lean coding workflow for this turn.",
		"- Keep user-visible replies compact like a coding agent: lead with the result, changed files, blockers, or next action.",
		"- Avoid narration such as \"let me check\", broad progress commentary, or restating obvious tool outputs. Mention process only when it changes the user's decision.",
		"- Read the relevant code first, make focused edits, then run the smallest useful verification.",
	}
	if hasLSP {
		lines = append(lines, "- For precise code intelligence (symbol definitions, references, type info), prefer the lsp tool. Use grep for full-text search across files.")
	} else {
		lines = append(lines, "- Use grep for full-text search across files.")
	}
	if hasToolExchange {
		lines = append(lines,
			"- If the user asks for current web information, use tool_exchange to enable the web bundle, then use web_search or web_fetch. Do not use bash with curl/wget as a substitute for the web tools.",
			"- When delegating web or current-information research to durable subagents, pass required_bundles=[\"web\"] after web is active; if subagent reports subagent_capability_required, enable the missing bundle with tool_exchange and retry once.",
			"- Enable browser, subagent, background, package, skill, memory, MCP, or external agent bundles only when the user explicitly asks for that capability or the active task clearly requires it.",
			"- If tool_exchange returns no match, continue with active coding tools or state the missing capability plainly.",
		)
	} else {
		lines = append(lines,
			"- This session's tool set is fixed; capabilities not listed below are not available on demand.",
			"- Stay within the active coding tools and verified workspace files; state plainly when a requested capability is not available.",
		)
	}
	if catalogHasTool(catalog, "cron") || catalogHasTool(catalog, "heartbeat") {
		if hasToolExchange {
			lines = append(lines, "- Automation tools may be available through tool_exchange; only use them for explicit scheduling or heartbeat requests.")
		} else {
			lines = append(lines, "- Automation tools are only usable when they appear in the active tool set; use them for explicit scheduling or heartbeat requests.")
		}
	}
	return strings.Join(lines, "\n")
}

func (a *Agent) buildSkillCatalogPrompt() (string, error) {
	catalog, err := a.ListSkills()
	if err != nil {
		return "", err
	}
	return buildSkillCatalogPrompt(catalog), nil
}

func (a *Agent) buildInstructionPrompt() (string, error) {
	sources, err := a.instrLoader.Load(a.cfg.WorkspaceDir, a.cfg.StateDir)
	if err != nil {
		return "", err
	}
	return buildInstructionPrompt(sources, a.cfg.WorkspaceDir, a.cfg.StateDir), nil
}

func buildInstructionPrompt(sources []instructions.InstructionSource, workspaceDir, stateDir string) string {
	if len(sources) == 0 {
		return ""
	}

	lines := []string{
		"# Instructions",
		"These are persistent project and local instructions. Follow them when they are relevant to the task.",
	}
	for _, source := range sources {
		label := instructionLabel(source.Path, workspaceDir, stateDir)
		lines = append(lines, fmt.Sprintf("## %s", label))
		lines = append(lines, source.Content)
	}
	return strings.Join(lines, "\n")
}

func instructionLabel(path, workspaceDir, stateDir string) string {
	if rel, err := filepath.Rel(workspaceDir, path); err == nil && rel == "AGENT.md" {
		return rel
	}
	if rel, err := filepath.Rel(stateDir, path); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
		return filepath.Join(".godex", rel)
	}
	return path
}

func buildToolAvailabilityPrompt(catalog tools.ToolCatalog) string {
	if len(catalog.Bundles) == 0 && len(catalog.AlwaysActiveTools) == 0 {
		return ""
	}

	// tool_exchange drives on-demand bundle activation. When it is not in the
	// exact active tool set (lean template presets), do not instruct the model
	// to call a tool it does not have, and omit the "available bundles" list
	// it could never activate.
	hasToolExchange := catalogHasActiveTool(catalog, "tool_exchange")

	lines := []string{
		"# Tool Availability",
		"Only active tools are callable right now.",
	}
	if hasToolExchange {
		lines = append(lines,
			"Use tool_exchange with a short query to discover or change bundle state when needed.",
			"If a listed bundle is clearly needed by name, call tool_exchange with enable_bundles for that bundle directly; use query only when the bundle name is unclear.",
			"Do not ask tool_exchange for tool schemas; enabled bundle tools appear in the next function list automatically.",
			"Keep the active tool workspace tidy: use disable_bundles for active bundles that this conversation no longer needs.",
		)
	} else {
		lines = append(lines, "This session's tool set is fixed; capabilities not listed below are not available on demand.")
	}
	if catalogHasActiveTool(catalog, "web_search") || catalogHasActiveTool(catalog, "web_fetch") {
		lines = append(lines, "Do not use bash/curl/python/node as a substitute for web_search or web_fetch when the web bundle is active.")
	}

	if active := formatBundleSummary(catalog.Bundles, true); active != "" {
		lines = append(lines, "- Active bundles: "+active)
	}
	if activeTools := formatActiveToolNames(catalog); activeTools != "" {
		lines = append(lines, "- Active tools: "+activeTools)
	}
	if hasToolExchange {
		if available := formatBundleSummary(catalog.Bundles, false); available != "" {
			lines = append(lines, "- Available bundles: "+available)
		}
	}
	if catalogHasActiveTool(catalog, "lsp") {
		lines = append(lines, "For precise code intelligence (symbol definitions, references, type info, hover docs), prefer the lsp tool over grep. Use grep for full-text search across files, find for file lookup, and ls for directory listing.")
	} else {
		lines = append(lines, "Use grep for full-text search across files, find for file lookup, and ls for directory listing.")
	}

	return strings.Join(lines, "\n")
}

func buildToolAvailabilityPromptForProfile(catalog tools.ToolCatalog, profile string) string {
	if config.NormalizeAgentProfile(profile) != config.AgentProfileCoding {
		return buildToolAvailabilityPrompt(catalog)
	}
	if len(catalog.Bundles) == 0 && len(catalog.AlwaysActiveTools) == 0 {
		return ""
	}
	lines := []string{
		"# Tool Availability",
		"Only active tools are callable right now.",
	}
	// tool_exchange drives on-demand bundle activation; when it is not in the
	// exact active set (lean template presets), do not reference it and omit
	// the "available bundles" list the model could never activate.
	hasToolExchange := catalogHasActiveTool(catalog, "tool_exchange")
	if hasToolExchange {
		lines = append(lines,
			"Use tool_exchange with enable_bundles for heavier capabilities when needed.",
			"Do not ask tool_exchange for tool schemas; enabled bundle tools appear in the next function list automatically.",
		)
	} else {
		lines = append(lines, "This session's tool set is fixed; capabilities not listed below are not available on demand.")
	}
	if forbidden := activeReplacementForbiddenTools(catalog); forbidden != "" {
		lines = append(lines, "Do not use bash/curl/python/node to substitute for "+forbidden+".")
	}
	if active := formatBundleSummary(catalog.Bundles, true); active != "" {
		lines = append(lines, "- Active bundles: "+active)
	}
	if activeTools := formatActiveToolNames(catalog); activeTools != "" {
		lines = append(lines, "- Active tools: "+activeTools)
	}
	if hasToolExchange {
		if available := formatBundleNames(catalog.Bundles, false); available != "" {
			lines = append(lines, "- Available bundles: "+available)
		}
	}
	if catalogHasActiveTool(catalog, "lsp") {
		lines = append(lines, "For precise code intelligence (symbol definitions, references, type info, hover docs), prefer the lsp tool over grep. Use grep for full-text search across files, find for file lookup, and ls for directory listing.")
	} else {
		lines = append(lines, "Use grep for full-text search across files, find for file lookup, and ls for directory listing.")
	}
	return strings.Join(lines, "\n")
}

func buildCodingProfilePrompt(profile string) string {
	if config.NormalizeAgentProfile(profile) != config.AgentProfileCoding {
		return ""
	}
	return strings.Join([]string{
		"# Agent Profile",
		"Effective profile: coding.",
		"Prefer direct code inspection, small edits, and verification. Keep broad skill catalogs and heavyweight delegation out of context until requested.",
		"Response style: concise by default. Do not narrate routine steps or print long summaries unless the user asks for detail.",
	}, "\n")
}

func buildSkillCatalogPrompt(catalog []skill.CatalogEntry) string {
	if len(catalog) == 0 {
		return ""
	}

	lines := []string{
		"# Skill Availability",
		"Installed and discoverable skills. Use skill with action=list/sources/install/load/expand/unload. If the user says find-skills or asks to find a skill, use skill with action=sources and query; do not use tool_exchange for skill search.",
	}

	regular, suites := splitSkillCatalogSuites(catalog)
	describedSuites := make(map[string]bool, len(suites))
	for _, item := range regular {
		line := formatSkillCatalogLine(item)
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = strings.TrimSpace(item.Name)
		}
		if children := suites[id]; len(children) > 0 {
			line += " " + formatSkillSuiteDetails(id, children)
			describedSuites[id] = true
		}
		if !appendBudgetedCatalogLine(&lines, line) {
			lines = append(lines, "- Additional skills omitted from the system prompt. Use skill with action=list for the complete catalog.")
			return strings.Join(lines, "\n")
		}
	}
	for _, suiteID := range skillSuiteIDs(suites) {
		if describedSuites[suiteID] {
			continue
		}
		items := suites[suiteID]
		if len(items) == 0 {
			continue
		}
		line := formatSkillSuiteCatalogLine(suiteID, items)
		if !appendBudgetedCatalogLine(&lines, line) {
			lines = append(lines, "- Additional skill suites omitted from the system prompt. Use skill with action=list for the complete catalog.")
			return strings.Join(lines, "\n")
		}
	}
	return strings.Join(lines, "\n")
}

func splitSkillCatalogSuites(catalog []skill.CatalogEntry) ([]skill.CatalogEntry, map[string][]skill.CatalogEntry) {
	regular := make([]skill.CatalogEntry, 0, len(catalog))
	suites := make(map[string][]skill.CatalogEntry)
	for _, item := range catalog {
		id := strings.TrimSpace(item.ID)
		if slash := strings.Index(id, "/"); slash > 0 {
			suiteID := id[:slash]
			suites[suiteID] = append(suites[suiteID], item)
			continue
		}
		regular = append(regular, item)
	}
	return regular, suites
}

func skillSuiteIDs(suites map[string][]skill.CatalogEntry) []string {
	ids := make([]string, 0, len(suites))
	for suiteID := range suites {
		ids = append(ids, suiteID)
	}
	sort.Strings(ids)
	return ids
}

func formatSkillCatalogLine(item skill.CatalogEntry) string {
	label := strings.TrimSpace(item.Name)
	if label == "" {
		label = strings.TrimSpace(item.ID)
	}
	if strings.TrimSpace(item.ID) != "" && !strings.EqualFold(strings.TrimSpace(item.ID), label) {
		label += " (id: " + strings.TrimSpace(item.ID) + ")"
	}
	line := fmt.Sprintf("- %s: %s", label, item.Description)
	if whenToUse := promptWhenToUse(item); len(whenToUse) > 0 {
		line += " When to use: " + strings.Join(whenToUse, "; ") + "."
	}
	if item.Compatibility.Status != "" && item.Compatibility.Status != skill.CompatibilityNativeSupported {
		line += " Compatibility: " + string(item.Compatibility.Status) + "."
	}
	if len(item.RecommendedBundles) > 0 {
		line += " Recommended bundles: " + strings.Join(item.RecommendedBundles, ", ") + "."
	}
	if len(item.Compatibility.MissingCapabilities) > 0 {
		line += " Missing capabilities: " + strings.Join(item.Compatibility.MissingCapabilities, ", ") + "."
	}
	if warnings := promptWarnings(item.Warnings); len(warnings) > 0 {
		line += " Warnings: " + strings.Join(warnings, "; ") + "."
	}
	if len(item.Sections) > 1 || (len(item.Sections) == 1 && item.Sections[0] != "core") {
		line += " Sections: " + strings.Join(item.Sections, ", ") + "."
	}
	return line
}

func formatSkillSuiteCatalogLine(suiteID string, items []skill.CatalogEntry) string {
	return fmt.Sprintf("- %s suite: %s", suiteID, formatSkillSuiteDetails(suiteID, items))
}

func formatSkillSuiteDetails(suiteID string, items []skill.CatalogEntry) string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = strings.TrimSpace(item.Name)
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	maxIDs := 80
	if len(ids) > maxIDs {
		ids = ids[:maxIDs]
	}
	line := fmt.Sprintf("%d nested skills available", len(items))
	if len(ids) > 0 {
		line += ": " + strings.Join(ids, ", ")
		if len(items) > len(ids) {
			line += fmt.Sprintf(", plus %d more", len(items)-len(ids))
		}
	}
	line += fmt.Sprintf(". Use skill with action=list and suite=%q, then skill with action=load and name with an exact id.", suiteID)
	return line
}

func appendBudgetedCatalogLine(lines *[]string, line string) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	next := strings.Join(append(append([]string{}, (*lines)...), line), "\n")
	if compress.CountTokens(next) > skillCatalogPromptTokenBudget {
		return false
	}
	*lines = append(*lines, line)
	return true
}

func formatBundleSummary(items []tools.BundleCatalogItem, active bool) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if item.Active != active {
			continue
		}
		part := item.Name
		if strings.TrimSpace(item.Summary) != "" {
			part += " (" + item.Summary + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

func formatBundleNames(items []tools.BundleCatalogItem, active bool) string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item.Active == active {
			names = append(names, item.Name)
		}
	}
	return strings.Join(names, ", ")
}

func formatActiveToolNames(catalog tools.ToolCatalog) string {
	seen := map[string]struct{}{}
	// catalog.ActiveTools is the exact currently-active set. AlwaysActiveTools
	// is a registration property (not activation state), and a bundle marked
	// Active only means "any of its tools is active" — enumerating either
	// would advertise tools the model cannot actually call.
	names := append([]string{}, catalog.ActiveTools...)
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func catalogHasTool(catalog tools.ToolCatalog, name string) bool {
	for _, item := range catalog.AlwaysActiveTools {
		if item == name {
			return true
		}
	}
	for _, bundle := range catalog.Bundles {
		for _, toolName := range bundle.Tools {
			if toolName == name {
				return true
			}
		}
	}
	return false
}

// catalogHasActiveTool reports whether a tool is in the exact currently-active
// set. Registration (catalogHasTool) is NOT activation: a lean template may
// register tool_exchange but not activate it, so prompt guidance must not
// tell the model to call tools it cannot actually call.
func catalogHasActiveTool(catalog tools.ToolCatalog, name string) bool {
	for _, n := range catalog.ActiveTools {
		if n == name {
			return true
		}
	}
	return false
}

// catalogHasAnyActiveTool reports whether any of the given tool names is in
// the exact currently-active set.
func catalogHasAnyActiveTool(catalog tools.ToolCatalog, names ...string) bool {
	for _, n := range names {
		if catalogHasActiveTool(catalog, n) {
			return true
		}
	}
	return false
}

// activeReplacementForbiddenTools lists the capability families that are
// actually ACTIVE and must not be replaced with bash/curl/python hacks. Only
// active families are listed: naming inactive tools (web/browser/package/
// external-agent in a lean template) teaches the model to advertise them as
// "unavailable capabilities" in its replies, which is noise the user never
// asked for.
func activeReplacementForbiddenTools(catalog tools.ToolCatalog) string {
	families := make([]string, 0, 4)
	if catalogHasActiveTool(catalog, "web_search") || catalogHasActiveTool(catalog, "web_fetch") {
		families = append(families, "web_search/web_fetch")
	}
	if catalogHasActiveTool(catalog, "browser") {
		families = append(families, "browser")
	}
	if catalogHasAnyActiveTool(catalog, "list_packages", "install_package", "remove_package", "list_prompts", "list_package_commands", "list_package_roles") {
		families = append(families, "package")
	}
	if catalogHasActiveTool(catalog, "acp_agent") {
		families = append(families, "external-agent")
	}
	return strings.Join(families, ", ")
}

func promptWhenToUse(item skill.CatalogEntry) []string {
	if len(item.WhenToUse) == 0 {
		return nil
	}
	description := strings.TrimSpace(item.Description)
	result := make([]string, 0, len(item.WhenToUse))
	for _, value := range item.WhenToUse {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if description != "" && strings.EqualFold(value, description) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func promptWarnings(warnings []string) []string {
	result := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		if strings.HasPrefix(warning, "LLM normalization fallback failed:") {
			continue
		}
		result = append(result, warning)
	}
	return result
}

func (a *Agent) activeSkillStates() []activeSkillState {
	a.mu.Lock()
	defer a.mu.Unlock()

	names := make([]string, 0, len(a.activeSkills))
	for name := range a.activeSkills {
		names = append(names, name)
	}
	sort.Strings(names)

	states := make([]activeSkillState, 0, len(names))
	for _, name := range names {
		state := a.activeSkills[name]
		if state == nil {
			continue
		}
		copyState := activeSkillState{
			catalog:  state.catalog,
			core:     state.core,
			expanded: make(map[string]string, len(state.expanded)),
		}
		copyState.expandedOrder = append([]string{}, state.expandedOrder...)
		for sectionName, content := range state.expanded {
			copyState.expanded[sectionName] = content
		}
		states = append(states, copyState)
	}
	return states
}

func buildActiveSkillsPrompt(states []activeSkillState) string {
	if len(states) == 0 {
		return ""
	}

	lines := []string{
		"# Active Skills",
		"These skill instructions are active for the current session. Prefer the loaded sections and only expand more sections when needed.",
	}
	for _, state := range states {
		lines = append(lines, fmt.Sprintf("## %s", state.catalog.Name))
		if state.catalog.Description != "" {
			lines = append(lines, fmt.Sprintf("Description: %s", state.catalog.Description))
		}
		if len(state.catalog.RecommendedBundles) > 0 {
			lines = append(lines, fmt.Sprintf("Recommended bundles: %s", strings.Join(state.catalog.RecommendedBundles, ", ")))
		}
		if len(state.loadedSections()) > 0 {
			lines = append(lines, fmt.Sprintf("Loaded sections: %s", strings.Join(state.loadedSections(), ", ")))
		}
		truncated := make([]string, 0)
		if strings.TrimSpace(state.core) != "" {
			if !appendBudgetedSkillBlock(&lines, "### Core", state.core, &truncated) {
				truncated = append(truncated, fmt.Sprintf("%s core omitted because the active skill context budget is exhausted.", state.catalog.Name))
			}
		}
		for _, sectionName := range state.expandedOrder {
			content := strings.TrimSpace(state.expanded[sectionName])
			if content == "" {
				continue
			}
			if !appendBudgetedSkillBlock(&lines, "### "+textutil.Title(sectionName), content, &truncated) {
				truncated = append(truncated, fmt.Sprintf("%s section %q omitted because the active skill context budget is exhausted.", state.catalog.Name, sectionName))
			}
		}
		if len(truncated) > 0 {
			lines = append(lines, "### Context Budget Notes")
			lines = append(lines, truncated...)
		}
	}
	return strings.Join(lines, "\n")
}

func appendBudgetedSkillBlock(lines *[]string, title, content string, truncated *[]string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return true
	}
	currentTokens := compress.CountTokens(strings.Join(*lines, "\n"))
	remainingTotal := activeSkillsPromptTokenBudget - currentTokens - compress.CountTokens(title) - 20
	if remainingTotal <= 0 {
		return false
	}
	blockBudget := activeSkillBlockTokenBudget
	if remainingTotal < blockBudget {
		blockBudget = remainingTotal
	}
	trimmed, didTrim := trimTextToTokenBudget(content, blockBudget)
	*lines = append(*lines, title, trimmed)
	if didTrim {
		*truncated = append(*truncated, fmt.Sprintf("%s truncated to fit active skill context budget.", strings.TrimPrefix(title, "### ")))
	}
	return true
}

func trimTextToTokenBudget(text string, budget int) (string, bool) {
	text = strings.TrimSpace(text)
	if budget <= 0 {
		return "", text != ""
	}
	if compress.CountTokens(text) <= budget {
		return text, false
	}
	runes := []rune(text)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if compress.CountTokens(string(runes[:mid])) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo <= 0 {
		return "[skill section truncated]", true
	}
	return strings.TrimSpace(string(runes[:lo])) + "\n\n[skill section truncated]", true
}

func filterNonEmpty(parts ...string) []string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		filtered = append(filtered, part)
	}
	return filtered
}
