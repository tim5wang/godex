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
	"github.com/tim5wang/godex/internal/core/skill"
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

func (a *Agent) buildDynamicSystemPrompt(agentProfile string) (string, error) {
	instructionPrompt, err := a.buildInstructionPrompt()
	if err != nil {
		return "", err
	}
	memoryPrompt, err := a.memoryMgr.BuildPromptSection()
	if err != nil {
		return "", err
	}
	profile := config.NormalizeAgentProfile(agentProfile)
	skillCatalogPrompt := ""
	if profile != config.AgentProfileCoding {
		skillCatalogPrompt, err = a.buildSkillCatalogPrompt()
		if err != nil {
			return "", err
		}
	}
	return strings.Join(filterNonEmpty(
		instructionPrompt,
		memoryPrompt,
		skillCatalogPrompt,
		buildCodingProfilePrompt(profile),
		buildActiveSkillsPrompt(a.activeSkillStates()),
		buildEnvironmentPrompt(a.environmentPromptInput()),
		buildCapabilityCheckPromptForProfile(a.toolHandler.Catalog(), profile),
		buildToolAvailabilityPromptForProfile(a.toolHandler.Catalog(), profile),
	), "\n\n"), nil
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
		fmt.Sprintf("- Local date: %s", input.Now.Format("2006-01-02")),
		fmt.Sprintf("- Weekday: %s", input.Now.Weekday()),
		fmt.Sprintf("- Timezone: %s", input.Timezone),
		"- File tools use workspace-relative paths.",
		"- Inbox and background updates are ephemeral runtime context and are not persisted automatically.",
		"- Conversation history may be compacted automatically when it grows large.",
	}
	return strings.Join(lines, "\n")
}

func buildCapabilityCheckPrompt(catalog tools.ToolCatalog) string {
	lines := []string{
		"# Capability Check",
		"Check what is already configured in this workspace before calling a capability unavailable.",
		"- Start with relevant skills and active tools, then use tool_exchange if another bundle would help.",
		"- Keep the active tool workspace small: when calling tool_exchange, disable bundles that are clearly irrelevant to the current conversation before or after enabling new ones.",
		"- Prefer web for current information, browser for dynamic pages, and glob for broad file discovery.",
		"- For obvious current-information requests such as weather, news, prices, stocks, exchange rates, scores, schedules, flights, or latest/recent status, use web_search directly when web is active; if web is inactive, call tool_exchange once with enable_bundles=[\"web\"] rather than querying for a weather/search tool.",
		"- After web_search or web_fetch returns useful results, synthesize from ranked results, fetched previews, metadata, and chunks; fetch one specific new URL only when more detail is needed. If web_fetch reports needs_browser, consider the browser tool for dynamic pages. Do not repeat the same search query or fetch the same URL.",
		"- If tool_exchange returns no match, do not repeat similar capability queries; switch to active tools or say the capability is unavailable.",
		"- When the user explicitly asks you to read, inspect, review, or verify specific workspace files or code paths, use the relevant file or shell tools before giving findings.",
		"- When using durable subagents, use task wait for any/all completion instead of repeatedly polling task status; use task logs only for bounded diagnostics.",
		"- When a tool generates a local file such as a screenshot or export, treat it as a generated artifact. In supported runtimes the artifact may be attached automatically, so do not claim you can only provide a local path unless the user explicitly asks for the path.",
		"- When the user wants a local file sent or attached without reading its contents, prefer attach_file instead of read_file.",
		"- Do not use read_file for binary or large artifacts such as PDFs, images, media, or archives when the user only wants them copied, attached, or sent.",
		"- Use extracted attachment content directly when available; if parsing is unavailable for an attached file type, say so plainly.",
		"- When the user asks what was said earlier, previously, or before, and the current context no longer holds the detail, use history_search to retrieve short snippets from current_session or session_archive.",
		"- Do not use history_search as general knowledge lookup, and do not turn history_search hits into durable memory automatically.",
		"- Skip canned self-introductions and stay focused on the request.",
	}
	if catalogHasTool(catalog, "cron") || catalogHasTool(catalog, "heartbeat") {
		lines = append(lines, "- After changing cron or heartbeat, report the resulting schedule, timezone, and enabled state.")
	}
	return strings.Join(lines, "\n")
}

func buildCapabilityCheckPromptForProfile(catalog tools.ToolCatalog, profile string) string {
	if config.NormalizeAgentProfile(profile) != config.AgentProfileCoding {
		return buildCapabilityCheckPrompt(catalog)
	}
	lines := []string{
		"# Coding Profile",
		"Default to the lean coding workflow for this turn.",
		"- Keep user-visible replies compact like a coding agent: lead with the result, changed files, blockers, or next action.",
		"- Avoid narration such as \"let me check\", broad progress commentary, or restating obvious tool outputs. Mention process only when it changes the user's decision.",
		"- Read the relevant code first, make focused edits, then run the smallest useful verification.",
		"- Use todo tools for multi-step coding work, but keep plans short and update them as work changes.",
		"- If the user asks for current web information, use tool_exchange to enable the web bundle, then use web_search or web_fetch. Do not use bash with curl/wget as a substitute for the web tools.",
		"- Enable browser, subagent, background, package, skill, memory, MCP, or external agent bundles only when the user explicitly asks for that capability or the active task clearly requires it.",
		"- If tool_exchange returns no match, continue with active coding tools or state the missing capability plainly.",
	}
	if catalogHasTool(catalog, "cron") || catalogHasTool(catalog, "heartbeat") {
		lines = append(lines, "- Automation tools may be available through tool_exchange; only use them for explicit scheduling or heartbeat requests.")
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

	lines := []string{
		"# Tool Availability",
		"Only active tools are callable right now.",
		"Use tool_exchange with a short query to discover or change bundle state when needed.",
		"If a listed bundle is clearly needed by name, call tool_exchange with enable_bundles for that bundle directly; use query only when the bundle name is unclear.",
		"Do not ask tool_exchange for tool schemas; enabled bundle tools appear in the next function list automatically.",
		"Do not use bash/curl/python/node as a substitute for web_search or web_fetch when the web bundle is active.",
		"Keep the active tool workspace tidy: use disable_bundles for active bundles that this conversation no longer needs.",
	}

	if active := formatBundleSummary(catalog.Bundles, true); active != "" {
		lines = append(lines, "- Active bundles: "+active)
	}
	if available := formatBundleSummary(catalog.Bundles, false); available != "" {
		lines = append(lines, "- Available bundles: "+available)
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
		"Only active tools are callable right now. Use tool_exchange with enable_bundles for heavier capabilities when needed.",
		"Do not ask tool_exchange for tool schemas; enabled bundle tools appear in the next function list automatically.",
		"Do not use bash/curl/python/node to replace web_search, web_fetch, browser, package, or external-agent tools when those bundles are the right capability.",
	}
	if active := formatBundleSummary(catalog.Bundles, true); active != "" {
		lines = append(lines, "- Active bundles: "+active)
	}
	if available := formatBundleNames(catalog.Bundles, false); available != "" {
		lines = append(lines, "- Available bundles: "+available)
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
		"Installed and discoverable skills. Use list_skills or list_skill_sources for more detail, install_skill to add one, load_skill to activate it, expand_skill for extra sections, and unload_skill when it is no longer helpful. If the user says find-skills or asks to find a skill, use list_skill_sources with a query; do not use tool_exchange for skill search.",
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
			lines = append(lines, "- Additional skills omitted from the system prompt. Use list_skills for the complete catalog.")
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
			lines = append(lines, "- Additional skill suites omitted from the system prompt. Use list_skills for the complete catalog.")
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
	line += fmt.Sprintf(". Use list_skills with suite=%q and offset/limit to inspect child details on demand, then load_skill with an exact id.", suiteID)
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
