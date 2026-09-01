package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/agent"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/core/teammate"
	"github.com/tim5wang/godex/internal/platform/stringutil"
	"github.com/tim5wang/godex/internal/tools"
)

func (s *Service) executeSkills(a *agent.Agent, cmd Command) (Result, error) {
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"list"}
	}
	switch strings.ToLower(strings.TrimSpace(cmd.Args[0])) {
	case "list":
		items, err := a.ListSkills()
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "skills", Output: renderSkillCatalog(items)}, nil
	case "sources":
		query := ""
		if len(cmd.Args) > 1 {
			query = strings.Join(cmd.Args[1:], " ")
		}
		var (
			items []tools.SkillSourceEntry
			err   error
		)
		if strings.TrimSpace(query) != "" {
			items, err = a.SearchSkillSources(query)
		} else {
			items, err = a.ListSkillSources()
		}
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "skills", Output: renderSkillSources(items)}, nil
	case "active":
		items, err := a.ActiveSkills()
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "skills", Output: renderActiveSkills(items)}, nil
	case "get":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /skills get <name>")
		}
		entry, err := a.GetSkill(cmd.Args[1])
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "skills", Output: renderSkillEntry(entry)}, nil
	case "install":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /skills install <source> [name]")
		}
		source := strings.TrimSpace(cmd.Args[1])
		name := ""
		if len(cmd.Args) > 2 {
			name = strings.TrimSpace(cmd.Args[2])
		}
		result, err := a.InstallSkill(source, name)
		return Result{Name: "skills", Output: renderSkillInstall(result)}, err
	case "load":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /skills load <name>")
		}
		result, err := a.ActivateSkill(cmd.Args[1])
		return Result{Name: "skills", Output: renderSkillActivation(result), RefreshSnapshot: err == nil}, err
	case "expand":
		if len(cmd.Args) < 3 {
			return Result{}, fmt.Errorf("usage: /skills expand <name> <section...>")
		}
		sections := make([]string, 0, len(cmd.Args)-2)
		for _, arg := range cmd.Args[2:] {
			for _, part := range strings.Split(arg, ",") {
				part = strings.TrimSpace(part)
				if part != "" {
					sections = append(sections, part)
				}
			}
		}
		if len(sections) == 0 {
			return Result{}, fmt.Errorf("usage: /skills expand <name> <section...>")
		}
		result, err := a.ExpandSkill(cmd.Args[1], sections)
		return Result{Name: "skills", Output: renderSkillExpansion(result), RefreshSnapshot: err == nil}, err
	case "unload":
		if len(cmd.Args) < 2 {
			return Result{}, fmt.Errorf("usage: /skills unload <name>")
		}
		result, err := a.UnloadSkill(cmd.Args[1])
		return Result{Name: "skills", Output: renderSkillActivation(result), RefreshSnapshot: err == nil}, err
	default:
		return Result{}, fmt.Errorf("unknown /skills subcommand %q", cmd.Args[0])
	}
}

func (s *Service) executePackages(cmd Command) (Result, error) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	if cfg == nil {
		return Result{Name: "packages", Output: "Package runtime is unavailable in this process."}, nil
	}
	manager := pkgregistry.NewManager(cfg.StateDir, cfg.SkillsDir)
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"list"}
	}
	switch strings.ToLower(strings.TrimSpace(cmd.Args[0])) {
	case "list":
		items, err := manager.List()
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "packages", Output: renderPackageList(items)}, nil
	case "commands":
		items, err := manager.ListCommands(false)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "packages", Output: renderPackageCommands(items)}, nil
	case "roles":
		items, err := manager.ListRoles(false)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "packages", Output: renderPackageRoles(items)}, nil
	case "prompts":
		items, err := manager.ListPrompts(false)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "packages", Output: renderPackagePrompts(items)}, nil
	default:
		return Result{}, fmt.Errorf("unknown /packages subcommand %q", cmd.Args[0])
	}
}

func (s *Service) executePackageCommand(cmd Command) (Result, bool, error) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	if cfg == nil {
		return Result{}, false, nil
	}
	manager := pkgregistry.NewManager(cfg.StateDir, cfg.SkillsDir)
	items, err := manager.ListCommands(true)
	if err != nil {
		return Result{}, false, err
	}
	match, args, ok, err := matchPackageCommand(cmd, items)
	if err != nil {
		return Result{}, true, err
	}
	if !ok {
		return Result{}, false, nil
	}
	prompt := renderPackageCommandPrompt(match, args)
	invocation := packageCommandInvocation(match)
	mode := strings.TrimSpace(match.Mode)
	if mode == "" {
		mode = "prompt_only"
	}
	result := Result{
		Name:           "package_command",
		Output:         fmt.Sprintf("Package command %s resolved in %s mode.", invocation, mode),
		DispatchStatus: "resolved",
	}
	if mode == "prompt_only" {
		result.Output = prompt
		return result, true, nil
	}
	if mode != "agent_turn" && mode != "subagent_job" {
		err := fmt.Errorf("package command %s uses unsupported dispatch mode %q", invocation, mode)
		result.DispatchStatus = "failed"
		result.DispatchError = err.Error()
		result.Diagnostics = append(result.Diagnostics, err.Error())
		return result, true, err
	}
	roleDiagnostics := packageCommandRoleDiagnostics(manager, match)
	if len(roleDiagnostics) > 0 {
		err := errors.New(strings.Join(roleDiagnostics, "; "))
		result.DispatchStatus = "failed"
		result.DispatchError = err.Error()
		result.Diagnostics = append(result.Diagnostics, roleDiagnostics...)
		return result, true, err
	}
	agentType := "Explore"
	if len(match.Roles) > 0 {
		agentType = match.Roles[0]
	}
	result.Dispatch = &PackageCommandDispatch{
		Mode:               mode,
		Prompt:             prompt,
		PackageName:        match.PackageName,
		Namespace:          match.Namespace,
		CommandName:        match.Name,
		Invocation:         invocation,
		Args:               append([]string{}, args...),
		AgentType:          agentType,
		WriteScope:         append([]string{}, match.WriteScope...),
		Roles:              append([]string{}, match.Roles...),
		Permissions:        append([]string{}, match.Permissions...),
		Capabilities:       append([]string{}, match.Capabilities...),
		ToolPolicy:         append([]string{}, match.ToolPolicy...),
		RecommendedBundles: append([]string{}, match.RecommendedBundles...),
	}
	result.DispatchStatus = "pending_dispatch"
	return result, true, nil
}

func packageCommandRoleDiagnostics(manager *pkgregistry.Manager, command pkgregistry.Command) []string {
	if len(command.Roles) == 0 || manager == nil {
		return nil
	}
	roles, err := manager.ListRoles(false)
	if err != nil {
		return []string{"list package roles: " + err.Error()}
	}
	known := map[string]struct{}{}
	for _, role := range roles {
		known[role.ID] = struct{}{}
		if role.Name != "" {
			known[role.Name] = struct{}{}
		}
	}
	var diagnostics []string
	for _, role := range command.Roles {
		if _, ok := known[role]; !ok {
			diagnostics = append(diagnostics, "package command role not found: "+role)
		}
	}
	return diagnostics
}

func matchPackageCommand(cmd Command, items []pkgregistry.Command) (pkgregistry.Command, []string, bool, error) {
	name := strings.ToLower(strings.TrimSpace(cmd.Name))
	if name == "" {
		return pkgregistry.Command{}, nil, false, nil
	}
	type candidate struct {
		item pkgregistry.Command
		args []string
	}
	var matches []candidate
	for _, item := range items {
		namespace := strings.ToLower(strings.TrimSpace(item.Namespace))
		commandName := strings.ToLower(strings.TrimSpace(item.Name))
		if namespace != "" && name == namespace && len(cmd.Args) > 0 && strings.EqualFold(cmd.Args[0], commandName) {
			matches = append(matches, candidate{item: item, args: append([]string{}, cmd.Args[1:]...)})
			continue
		}
		if containsFold(item.Aliases, cmd.Name) {
			matches = append(matches, candidate{item: item, args: append([]string{}, cmd.Args...)})
		}
	}
	if len(matches) == 0 {
		return pkgregistry.Command{}, nil, false, nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, packageCommandInvocation(match.item))
		}
		return pkgregistry.Command{}, nil, true, fmt.Errorf("ambiguous package command /%s: %s", cmd.Name, strings.Join(names, ", "))
	}
	return matches[0].item, matches[0].args, true, nil
}

func containsFold(values []string, want string) bool {
	want = strings.TrimPrefix(strings.TrimSpace(want), "/")
	for _, value := range values {
		value = strings.TrimPrefix(strings.TrimSpace(value), "/")
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

func renderPackageCommandPrompt(item pkgregistry.Command, args []string) string {
	prompt := strings.TrimSpace(item.Prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(item.Description)
	}
	if prompt == "" {
		prompt = "Run package command " + packageCommandInvocation(item) + "."
	}
	rawArgs := strings.Join(args, " ")
	replacements := map[string]string{
		"{{args}}":                rawArgs,
		"{{raw_args}}":            rawArgs,
		"{{command}}":             item.Name,
		"{{namespace}}":           item.Namespace,
		"{{package}}":             item.PackageName,
		"{{roles}}":               strings.Join(item.Roles, ", "),
		"{{recommended_bundles}}": strings.Join(item.RecommendedBundles, ", "),
	}
	rendered := prompt
	for old, replacement := range replacements {
		rendered = strings.ReplaceAll(rendered, old, replacement)
	}
	if rawArgs != "" && !strings.Contains(prompt, "{{args}}") && !strings.Contains(prompt, "{{raw_args}}") {
		rendered += "\n\nUser arguments:\n" + rawArgs
	}
	header := []string{
		"Package command: " + packageCommandInvocation(item),
	}
	if item.Description != "" {
		header = append(header, "Description: "+item.Description)
	}
	if len(item.Roles) > 0 {
		header = append(header, "Roles: "+strings.Join(item.Roles, ", "))
	}
	return strings.Join(header, "\n") + "\n\n" + strings.TrimSpace(rendered)
}

func packageCommandInvocation(item pkgregistry.Command) string {
	namespace := strings.TrimSpace(item.Namespace)
	if namespace == "" {
		namespace = strings.TrimSpace(item.PackageName)
	}
	if namespace == "" {
		return "/" + strings.TrimSpace(item.Name)
	}
	return "/" + namespace + " " + strings.TrimSpace(item.Name)
}

func renderTeam(list []*teammate.Teammate) string {
	if len(list) == 0 {
		return "No teammates."
	}
	lines := make([]string, 0, len(list))
	for _, tm := range list {
		lines = append(lines, fmt.Sprintf("%s (%s): %s", tm.Name, tm.Role, tm.Status))
	}
	return strings.Join(lines, "\n")
}

func renderSkillCatalog(items []skill.CatalogEntry) string {
	if len(items) == 0 {
		return "No skills discovered in the current skills directory."
	}
	lines := []string{"Discoverable skills:"}
	for _, item := range items {
		line := "- " + skillLabel(item.ID, item.Name)
		if item.Description != "" {
			line += " — " + item.Description
		}
		if item.Compatibility.Status != "" {
			line += " [" + string(item.Compatibility.Status) + "]"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderSkillSources(items []tools.SkillSourceEntry) string {
	if len(items) == 0 {
		return "No curated skill sources are configured."
	}
	lines := []string{"Skill sources:"}
	for _, item := range items {
		line := "- " + item.Name
		if item.Summary != "" {
			line += " — " + item.Summary
		}
		if strings.TrimSpace(item.Version) != "" {
			line += " @" + item.Version
		}
		if item.Installed {
			line += " [installed]"
		}
		if !item.InstallSupported {
			line += " [install-unavailable]"
		}
		if strings.TrimSpace(item.Origin) != "" {
			line += " {" + item.Origin + "}"
		}
		if strings.TrimSpace(item.Trust) != "" {
			line += " [trust=" + item.Trust + "]"
		}
		if len(item.Categories) > 0 {
			line += " categories=" + strings.Join(item.Categories, ",")
		}
		if item.Source != "" {
			line += " <" + item.Source + ">"
		}
		lines = append(lines, line)
	}
	sourceWarnings := make([]string, 0, len(items))
	for _, item := range items {
		sourceWarnings = append(sourceWarnings, item.Warnings...)
	}
	for _, warning := range stringutil.Unique(sourceWarnings) {
		lines = append(lines, "warning: "+warning)
	}
	return strings.Join(lines, "\n")
}

func renderPackageList(items []pkgregistry.Entry) string {
	if len(items) == 0 {
		return "No packages installed."
	}
	lines := []string{"Installed packages:"}
	for _, item := range items {
		line := "- " + item.Name
		if item.Version != "" {
			line += " @" + item.Version
		}
		if item.Description != "" {
			line += " — " + item.Description
		}
		line += " [" + item.Trust + "]"
		resources := packageResourceSummary(item.Resources)
		if resources != "" {
			line += " resources=" + resources
		}
		if len(item.Requires) > 0 {
			line += " requires=" + strings.Join(item.Requires, ",")
		}
		if len(item.Provides) > 0 {
			line += " provides=" + strings.Join(item.Provides, ",")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderPackageCommands(items []pkgregistry.Command) string {
	if len(items) == 0 {
		return "No package commands installed."
	}
	lines := []string{"Package commands:"}
	for _, item := range items {
		namespace := item.Namespace
		if namespace == "" {
			namespace = item.PackageName
		}
		line := fmt.Sprintf("- /%s %s", namespace, item.Name)
		if item.Description != "" {
			line += " — " + item.Description
		}
		if item.Mode != "" {
			line += " [" + item.Mode + "]"
		}
		if len(item.Roles) > 0 {
			line += " roles=" + strings.Join(item.Roles, ",")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderPackageRoles(items []pkgregistry.Role) string {
	if len(items) == 0 {
		return "No package roles installed."
	}
	lines := []string{"Package roles:"}
	for _, item := range items {
		line := "- " + item.ID
		if item.Name != "" && item.Name != item.ID {
			line += " (" + item.Name + ")"
		}
		if item.Description != "" {
			line += " — " + item.Description
		}
		if item.WriteEnabled {
			line += " [write]"
		} else {
			line += " [read-only]"
		}
		if len(item.Tools) > 0 {
			line += " tools=" + strings.Join(item.Tools, ",")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderPackagePrompts(items []pkgregistry.Prompt) string {
	if len(items) == 0 {
		return "No package prompts installed."
	}
	lines := []string{"Package prompts:"}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- %s:%s <%s>", item.PackageName, item.Name, item.Path))
	}
	return strings.Join(lines, "\n")
}

func packageResourceSummary(resources pkgregistry.Resources) string {
	parts := []string{}
	if len(resources.Skills) > 0 {
		parts = append(parts, fmt.Sprintf("skills:%d", len(resources.Skills)))
	}
	if len(resources.Prompts) > 0 {
		parts = append(parts, fmt.Sprintf("prompts:%d", len(resources.Prompts)))
	}
	if len(resources.Commands) > 0 {
		parts = append(parts, fmt.Sprintf("commands:%d", len(resources.Commands)))
	}
	if len(resources.Roles) > 0 {
		parts = append(parts, fmt.Sprintf("roles:%d", len(resources.Roles)))
	}
	if len(resources.Docs) > 0 {
		parts = append(parts, fmt.Sprintf("docs:%d", len(resources.Docs)))
	}
	if len(resources.Assets) > 0 {
		parts = append(parts, fmt.Sprintf("assets:%d", len(resources.Assets)))
	}
	return strings.Join(parts, ",")
}

func renderSkillEntry(item skill.CatalogEntry) string {
	lines := []string{skillLabel(item.ID, item.Name)}
	if item.Description != "" {
		lines = append(lines, item.Description)
	}
	if item.Version != "" {
		lines = append(lines, "version: "+item.Version)
	}
	if len(item.Categories) > 0 {
		lines = append(lines, "categories: "+strings.Join(item.Categories, ", "))
	}
	if item.Compatibility.Status != "" {
		lines = append(lines, "compatibility: "+string(item.Compatibility.Status))
	}
	if item.InstallMemory != nil {
		parts := []string{}
		if item.InstallMemory.SourceOrigin != "" {
			parts = append(parts, item.InstallMemory.SourceOrigin)
		}
		if item.InstallMemory.Trust != "" {
			parts = append(parts, item.InstallMemory.Trust)
		}
		if item.InstallMemory.Version != "" {
			parts = append(parts, item.InstallMemory.Version)
		}
		line := "installed from: " + item.InstallMemory.Source
		if len(parts) > 0 {
			line += " (" + strings.Join(parts, ", ") + ")"
		}
		lines = append(lines, line)
	}
	if len(item.Sections) > 0 {
		lines = append(lines, "sections: "+strings.Join(item.Sections, ", "))
	}
	if len(item.RecommendedBundles) > 0 {
		lines = append(lines, "recommended bundles: "+strings.Join(item.RecommendedBundles, ", "))
	}
	if len(item.Compatibility.MissingDependencies) > 0 {
		lines = append(lines, "missing dependencies: "+strings.Join(item.Compatibility.MissingDependencies, ", "))
	}
	if len(item.Warnings) > 0 {
		lines = append(lines, "warnings: "+strings.Join(item.Warnings, " | "))
	}
	return strings.Join(lines, "\n")
}

func renderActiveSkills(items []tools.SkillActivation) string {
	if len(items) == 0 {
		return "No active skills in this session."
	}
	lines := []string{"Active skills:"}
	for _, item := range items {
		line := "- " + skillLabel(item.ID, item.Name)
		if len(item.LoadedSections) > 0 {
			line += " [" + strings.Join(item.LoadedSections, ", ") + "]"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderSkillActivation(item tools.SkillActivation) string {
	lines := []string{fmt.Sprintf("%s: %s", skillLabel(item.ID, item.Name), item.Status)}
	if item.Description != "" {
		lines = append(lines, item.Description)
	}
	if len(item.LoadedSections) > 0 {
		lines = append(lines, "loaded sections: "+strings.Join(item.LoadedSections, ", "))
	}
	if len(item.AvailableSections) > 0 {
		lines = append(lines, "available sections: "+strings.Join(item.AvailableSections, ", "))
	}
	if len(item.RecommendedBundles) > 0 {
		lines = append(lines, "recommended bundles: "+strings.Join(item.RecommendedBundles, ", "))
	}
	if item.Compatibility.Status != "" {
		lines = append(lines, "compatibility: "+string(item.Compatibility.Status))
	}
	return strings.Join(lines, "\n")
}

func renderSkillInstall(item tools.SkillInstallResult) string {
	lines := []string{fmt.Sprintf("%s: %s", skillLabel(item.ID, item.Name), item.Status)}
	if item.Description != "" {
		lines = append(lines, item.Description)
	}
	if item.Source != "" {
		lines = append(lines, "source: "+item.Source)
	}
	if item.SourceOrigin != "" {
		lines = append(lines, "source origin: "+item.SourceOrigin)
	}
	if item.Trust != "" {
		lines = append(lines, "trust: "+item.Trust)
	}
	if item.Version != "" {
		lines = append(lines, "version: "+item.Version)
	}
	if len(item.Categories) > 0 {
		lines = append(lines, "categories: "+strings.Join(item.Categories, ", "))
	}
	if item.InstalledPath != "" {
		lines = append(lines, "installed path: "+item.InstalledPath)
	}
	if len(item.Sections) > 0 {
		lines = append(lines, "sections: "+strings.Join(item.Sections, ", "))
	}
	if len(item.RecommendedBundles) > 0 {
		lines = append(lines, "recommended bundles: "+strings.Join(item.RecommendedBundles, ", "))
	}
	if item.Compatibility.Status != "" {
		lines = append(lines, "compatibility: "+string(item.Compatibility.Status))
	}
	if len(item.Compatibility.MissingDependencies) > 0 {
		lines = append(lines, "missing dependencies: "+strings.Join(item.Compatibility.MissingDependencies, ", "))
	}
	if len(item.Warnings) > 0 {
		lines = append(lines, "warnings: "+strings.Join(item.Warnings, " | "))
	}
	return strings.Join(lines, "\n")
}

func skillLabel(id, name string) string {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" {
		return name
	}
	if name == "" || strings.EqualFold(id, name) {
		return id
	}
	return fmt.Sprintf("%s [%s]", name, id)
}

func renderSkillExpansion(item tools.SkillExpansion) string {
	lines := []string{fmt.Sprintf("%s: %s", skillLabel(item.ID, item.Name), item.Status)}
	if len(item.ExpandedSections) > 0 {
		lines = append(lines, "expanded sections: "+strings.Join(item.ExpandedSections, ", "))
	}
	if len(item.LoadedSections) > 0 {
		lines = append(lines, "loaded sections: "+strings.Join(item.LoadedSections, ", "))
	}
	if len(item.AvailableSections) > 0 {
		lines = append(lines, "available sections: "+strings.Join(item.AvailableSections, ", "))
	}
	if item.Compatibility.Status != "" {
		lines = append(lines, "compatibility: "+string(item.Compatibility.Status))
	}
	return strings.Join(lines, "\n")
}
