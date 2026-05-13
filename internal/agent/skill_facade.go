package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/platform/stringutil"
	"github.com/tim5wang/godex/internal/tools"
)

type activeSkillState struct {
	catalog       skill.CatalogEntry
	core          string
	expanded      map[string]string
	expandedOrder []string
}

func (s *activeSkillState) loadedSections() []string {
	sections := make([]string, 0, 1+len(s.expandedOrder))
	if strings.TrimSpace(s.core) != "" {
		sections = append(sections, "core")
	}
	sections = append(sections, s.expandedOrder...)
	return sections
}

// LoadSkill loads a skill into system prompt.
func (a *Agent) LoadSkill(name string) error {
	_, err := a.ActivateSkill(name)
	return err
}

// ActivateSkill loads the skill core into session prompt state if it is not already active.
func (a *Agent) ActivateSkill(name string) (tools.SkillActivation, error) {
	skillDef, err := a.skillLoader.Load(name)
	if err != nil {
		return tools.SkillActivation{}, err
	}
	entry := a.resolveSkillEntry(a.skillLoader.CatalogEntryFor(skillDef))
	skillID := entry.ID
	entry = a.catalogEntryWithSuiteMetadata(skillID, entry)

	a.mu.Lock()
	defer a.mu.Unlock()
	if state, ok := a.activeSkills[skillID]; ok {
		return skillActivationResult(state, "already_active"), nil
	}
	a.activeSkills[skillID] = &activeSkillState{
		catalog:  entry,
		core:     skillDef.Core,
		expanded: make(map[string]string),
	}
	return skillActivationResult(a.activeSkills[skillID], "activated"), nil
}

// InstallSkill installs a new skill source into the workspace skills directory.
func (a *Agent) InstallSkill(source, name string) (tools.SkillInstallResult, error) {
	result, err := a.skillLoader.Install(source, name)
	if err != nil {
		return tools.SkillInstallResult{}, err
	}
	return tools.SkillInstallResult{
		ID:                 result.ID,
		Name:               result.Name,
		Status:             result.Status,
		Source:             result.Source,
		SourceOrigin:       result.SourceOrigin,
		Trust:              result.Trust,
		Version:            result.Version,
		Categories:         append([]string{}, result.Categories...),
		InstalledPath:      result.InstalledPath,
		Description:        result.Description,
		Sections:           append([]string{}, result.Sections...),
		RecommendedBundles: append([]string{}, result.RecommendedBundles...),
		Compatibility:      result.Compatibility,
		Warnings:           append([]string{}, result.Warnings...),
		InstallMemory:      cloneSkillInstallMemory(result.InstallMemory),
	}, nil
}

// NormalizeSkill explicitly enriches one skill with the configured LLM normalizer.
func (a *Agent) NormalizeSkill(ctx context.Context, name string) (skill.CatalogEntry, error) {
	skillDef, err := a.skillLoader.NormalizeSkill(ctx, name)
	if err != nil {
		return skill.CatalogEntry{}, err
	}
	return a.resolveSkillEntry(a.skillLoader.CatalogEntryFor(skillDef)), nil
}

// RemoveSkill deletes an installed skill and removes it from the active session stack.
func (a *Agent) RemoveSkill(name string) (tools.SkillRemoveResult, error) {
	result, err := a.skillLoader.Remove(name)
	if err != nil {
		return tools.SkillRemoveResult{}, err
	}

	wasActive := false
	a.mu.Lock()
	if skillID := a.findActiveSkillKeyLocked(result.ID); skillID != "" {
		if _, ok := a.activeSkills[skillID]; ok {
			delete(a.activeSkills, skillID)
			wasActive = true
		}
	}
	a.mu.Unlock()

	return tools.SkillRemoveResult{
		ID:          result.ID,
		Name:        result.Name,
		Status:      result.Status,
		RemovedPath: result.RemovedPath,
		WasActive:   wasActive,
	}, nil
}

// ExpandSkill loads additional named sections for an already active skill.
func (a *Agent) ExpandSkill(name string, sections []string) (tools.SkillExpansion, error) {
	a.mu.Lock()
	skillID := a.findActiveSkillKeyLocked(name)
	state, ok := a.activeSkills[skillID]
	a.mu.Unlock()
	if !ok {
		return tools.SkillExpansion{}, skillNotActiveError(name)
	}

	resolved, err := a.skillLoader.GetSections(skillID, sections)
	if err != nil {
		return tools.SkillExpansion{}, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	state = a.activeSkills[skillID]
	if state == nil {
		return tools.SkillExpansion{}, skillNotActiveError(name)
	}

	expandedNow := make([]string, 0, len(resolved))
	for _, section := range resolved {
		if _, ok := state.expanded[section.Name]; ok {
			continue
		}
		state.expanded[section.Name] = section.Content
		state.expandedOrder = append(state.expandedOrder, section.Name)
		expandedNow = append(expandedNow, section.Name)
	}

	status := "already_loaded"
	if len(expandedNow) > 0 {
		status = "expanded"
	}
	return tools.SkillExpansion{
		ID:                 state.catalog.ID,
		Name:               state.catalog.Name,
		Status:             status,
		ExpandedSections:   expandedNow,
		LoadedSections:     state.loadedSections(),
		AvailableSections:  append([]string{}, state.catalog.Sections...),
		RecommendedBundles: append([]string{}, state.catalog.RecommendedBundles...),
		Compatibility:      state.catalog.Compatibility,
	}, nil
}

// ListSkills returns the discoverable skill catalog for the current workspace.
func (a *Agent) ListSkills() ([]skill.CatalogEntry, error) {
	items, err := a.skillLoader.Catalog(a.cfg.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	resolved := make([]skill.CatalogEntry, 0, len(items))
	for _, item := range items {
		resolved = append(resolved, a.resolveSkillEntry(item))
	}
	decorateSkillSuiteMetadata(resolved)
	return resolved, nil
}

// ListSkillSources returns curated install sources with current installed-state decoration.
func (a *Agent) ListSkillSources() ([]tools.SkillSourceEntry, error) {
	return a.listSkillSources("")
}

// SearchSkillSources returns curated install sources plus skills.sh matches for the query.
func (a *Agent) SearchSkillSources(query string) ([]tools.SkillSourceEntry, error) {
	return a.listSkillSources(query)
}

func (a *Agent) listSkillSources(query string) ([]tools.SkillSourceEntry, error) {
	items, err := skill.SourceCatalog(a.cfg.WorkspaceDir, a.cfg.SkillsDir)
	if strings.TrimSpace(query) != "" {
		items, err = skill.SearchSourceCatalog(a.cfg.WorkspaceDir, a.cfg.SkillsDir, query)
	}
	if err != nil {
		return nil, err
	}
	result := make([]tools.SkillSourceEntry, 0, len(items))
	for _, item := range items {
		result = append(result, tools.SkillSourceEntry{
			ID:               item.ID,
			Name:             item.Name,
			Summary:          item.Summary,
			Source:           item.Source,
			SkillName:        item.SkillName,
			Tags:             append([]string{}, item.Tags...),
			Categories:       append([]string{}, item.Categories...),
			Version:          item.Version,
			Trust:            item.Trust,
			Origin:           item.Origin,
			Installs:         item.Installs,
			Warnings:         append([]string{}, item.Warnings...),
			InstallSupported: item.InstallSupported,
			InstallSource:    item.InstallSource,
			InstallName:      item.InstallName,
			InstallReason:    item.InstallReason,
			Installed:        item.Installed,
			InstalledPath:    item.InstalledPath,
			InstallMemory:    cloneSkillInstallMemory(item.InstallMemory),
		})
	}
	return result, nil
}

// GetSkill returns one discoverable skill's lightweight metadata.
func (a *Agent) GetSkill(name string) (skill.CatalogEntry, error) {
	if items, err := a.ListSkills(); err == nil {
		for _, item := range items {
			if strings.EqualFold(item.ID, name) || strings.EqualFold(item.Name, name) {
				return item, nil
			}
		}
	}
	skillDef, err := a.skillLoader.Load(name)
	if err != nil {
		return skill.CatalogEntry{}, err
	}
	entry := a.resolveSkillEntry(a.skillLoader.CatalogEntryFor(skillDef))
	decorateSkillSuiteMetadata([]skill.CatalogEntry{entry})
	return entry, nil
}

func (a *Agent) catalogEntryWithSuiteMetadata(id string, fallback skill.CatalogEntry) skill.CatalogEntry {
	items, err := a.ListSkills()
	if err != nil {
		decorateSkillSuiteMetadata([]skill.CatalogEntry{fallback})
		return fallback
	}
	for _, item := range items {
		if strings.EqualFold(item.ID, id) || strings.EqualFold(item.Name, id) {
			return item
		}
	}
	decorateSkillSuiteMetadata([]skill.CatalogEntry{fallback})
	return fallback
}

// ActiveSkills returns detailed state for currently activated skills.
func (a *Agent) ActiveSkills() ([]tools.SkillActivation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	names := make([]string, 0, len(a.activeSkills))
	for name := range a.activeSkills {
		names = append(names, name)
	}
	sort.Strings(names)

	items := make([]tools.SkillActivation, 0, len(names))
	for _, name := range names {
		state := a.activeSkills[name]
		if state == nil {
			continue
		}
		items = append(items, skillActivationResult(state, "active"))
	}
	return items, nil
}

// UnloadSkill removes an active skill from the session prompt state.
func (a *Agent) UnloadSkill(name string) (tools.SkillActivation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	skillID := a.findActiveSkillKeyLocked(name)
	state, ok := a.activeSkills[skillID]
	if !ok || state == nil {
		return tools.SkillActivation{}, skillNotActiveError(name)
	}
	result := skillActivationResult(state, "unloaded")
	result.LoadedSections = nil
	delete(a.activeSkills, skillID)
	return result, nil
}

func skillActivationResult(state *activeSkillState, status string) tools.SkillActivation {
	return tools.SkillActivation{
		ID:                 state.catalog.ID,
		Name:               state.catalog.Name,
		Status:             status,
		Description:        state.catalog.Description,
		LoadedSections:     state.loadedSections(),
		AvailableSections:  append([]string{}, state.catalog.Sections...),
		RecommendedBundles: append([]string{}, state.catalog.RecommendedBundles...),
		Compatibility:      state.catalog.Compatibility,
		SkillKind:          state.catalog.SkillKind,
		SuiteID:            state.catalog.SuiteID,
		ChildSkillCount:    state.catalog.ChildSkillCount,
		ChildSkillIDs:      append([]string{}, state.catalog.ChildSkillIDs...),
		ChildSkillHint:     state.catalog.ChildSkillHint,
	}
}

const maxCatalogChildSkillIDs = 80

func decorateSkillSuiteMetadata(items []skill.CatalogEntry) {
	childrenBySuite := make(map[string][]string)
	for _, item := range items {
		suiteID, ok := splitSuiteSkillID(item.ID)
		if !ok {
			continue
		}
		childrenBySuite[suiteID] = append(childrenBySuite[suiteID], item.ID)
	}
	for suiteID := range childrenBySuite {
		sort.Strings(childrenBySuite[suiteID])
	}

	for i := range items {
		suiteID, isChild := splitSuiteSkillID(items[i].ID)
		if isChild {
			items[i].SkillKind = "child_skill"
			items[i].SuiteID = suiteID
			continue
		}
		children := childrenBySuite[items[i].ID]
		if len(children) == 0 {
			items[i].SkillKind = "root_skill"
			continue
		}
		items[i].SkillKind = "suite_root"
		items[i].ChildSkillCount = len(children)
		items[i].ChildSkillIDs = append([]string{}, children...)
		if len(items[i].ChildSkillIDs) > maxCatalogChildSkillIDs {
			items[i].ChildSkillIDs = items[i].ChildSkillIDs[:maxCatalogChildSkillIDs]
		}
		items[i].ChildSkillHint = fmt.Sprintf("Use list_skills with suite=%q and offset/limit to inspect child details, then load_skill with an exact child id.", items[i].ID)
	}
}

func splitSuiteSkillID(id string) (string, bool) {
	parts := strings.SplitN(strings.TrimSpace(id), "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return parts[0], true
}

func (a *Agent) findActiveSkillKeyLocked(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if _, ok := a.activeSkills[name]; ok {
		return name
	}
	for skillID, state := range a.activeSkills {
		if state == nil {
			continue
		}
		if strings.EqualFold(skillID, name) || strings.EqualFold(state.catalog.ID, name) || strings.EqualFold(state.catalog.Name, name) {
			return skillID
		}
	}
	return name
}

func skillNotActiveError(name string) error {
	return fmt.Errorf("%w: skill %q is not active", skill.ErrSkillConflict, name)
}

func (a *Agent) resolveSkillEntry(entry skill.CatalogEntry) skill.CatalogEntry {
	if a.mcpMgr.HasConfiguredServers() && stringutil.Contains(entry.Compatibility.MissingCapabilities, "mcp") {
		entry.Compatibility.MissingCapabilities = stringutil.Remove(entry.Compatibility.MissingCapabilities, "mcp")
		entry.Compatibility.Notes = stringutil.AppendUnique(entry.Compatibility.Notes, "Configured MCP servers are available via the mcp bundle.")
	}
	if entry.Requires.NamedSubagents && stringutil.Contains(entry.Compatibility.MissingCapabilities, "named_subagents") {
		entry.Compatibility.MissingCapabilities = stringutil.Remove(entry.Compatibility.MissingCapabilities, "named_subagents")
		entry.Compatibility.Notes = stringutil.AppendUnique(entry.Compatibility.Notes, "Named subagent roles are accepted by the durable subagent runtime and preserved in timeline events.")
	}
	if entry.Requires.SlashCommandRuntime && stringutil.Contains(entry.Compatibility.MissingCapabilities, "slash_command_runtime") {
		entry.Compatibility.MissingCapabilities = stringutil.Remove(entry.Compatibility.MissingCapabilities, "slash_command_runtime")
		entry.Compatibility.Notes = stringutil.AppendUnique(entry.Compatibility.Notes, "GoDex provides a native slash-command runtime; third-party command names should be declared through package command specs.")
	}
	if entry.Requires.ContextFork && stringutil.Contains(entry.Compatibility.MissingCapabilities, "context_fork") {
		entry.Compatibility.MissingCapabilities = stringutil.Remove(entry.Compatibility.MissingCapabilities, "context_fork")
		entry.Compatibility.Notes = stringutil.AppendUnique(entry.Compatibility.Notes, "Fork-context skills can be adapted through the existing subagent runtime.")
	}
	if entry.Requires.Hooks && supportsHookWhitelist(entry.Requires.HookNames) && stringutil.Contains(entry.Compatibility.MissingCapabilities, "hooks") {
		entry.Compatibility.MissingCapabilities = stringutil.Remove(entry.Compatibility.MissingCapabilities, "hooks")
		entry.Compatibility.Notes = stringutil.AppendUnique(entry.Compatibility.Notes, "Whitelisted hook hints are recognized as advisory runtime guidance.")
	}
	if len(entry.Compatibility.MissingCapabilities) == 0 && entry.Compatibility.Status == skill.CompatibilityDegradedSupported {
		entry.Compatibility.Status = skill.CompatibilityNativeSupported
	}
	return entry
}

func supportsHookWhitelist(hookNames []string) bool {
	if len(hookNames) == 0 {
		return false
	}
	allowed := map[string]struct{}{
		"on_start":    {},
		"on_complete": {},
		"on_error":    {},
	}
	for _, hookName := range hookNames {
		if _, ok := allowed[hookName]; !ok {
			return false
		}
	}
	return true
}

func cloneSkillInstallMemory(memory *skill.InstallMemory) *skill.InstallMemory {
	if memory == nil {
		return nil
	}
	return &skill.InstallMemory{
		Source:        memory.Source,
		SourceEntryID: memory.SourceEntryID,
		SourceOrigin:  memory.SourceOrigin,
		Trust:         memory.Trust,
		Version:       memory.Version,
		Categories:    append([]string{}, memory.Categories...),
		InstalledAt:   memory.InstalledAt,
	}
}
