package agent

import (
	"strings"
	"time"

	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/tools"
)

// ListPackages lists installed Godex packages.
func (a *Agent) ListPackages() ([]tools.PackageEntry, error) {
	items, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).List()
	if err != nil {
		return nil, err
	}
	return packageEntriesFromRegistry(items), nil
}

// InstallPackage installs one declaration-only Godex package.
func (a *Agent) InstallPackage(source string) (tools.PackageEntry, error) {
	item, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).Install(source)
	if err != nil {
		return tools.PackageEntry{}, err
	}
	return packageEntryFromRegistry(item), nil
}

// RemovePackage removes one installed Godex package.
func (a *Agent) RemovePackage(name string) (tools.PackageEntry, error) {
	item, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).Remove(name)
	if err != nil {
		return tools.PackageEntry{}, err
	}
	return packageEntryFromRegistry(item), nil
}

// ListPrompts lists prompt templates installed through packages.
func (a *Agent) ListPrompts(includeContent bool) ([]tools.PromptEntry, error) {
	items, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).ListPrompts(includeContent)
	if err != nil {
		return nil, err
	}
	out := make([]tools.PromptEntry, 0, len(items))
	for _, item := range items {
		out = append(out, tools.PromptEntry{
			PackageName: item.PackageName,
			Name:        item.Name,
			Path:        item.Path,
			Content:     item.Content,
		})
	}
	return out, nil
}

// ListPackageCommands lists slash-command workflow declarations installed through packages.
func (a *Agent) ListPackageCommands(includeContent bool) ([]tools.PackageCommandEntry, error) {
	items, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).ListCommands(includeContent)
	if err != nil {
		return nil, err
	}
	out := make([]tools.PackageCommandEntry, 0, len(items))
	for _, item := range items {
		out = append(out, tools.PackageCommandEntry{
			PackageName:        item.PackageName,
			Name:               item.Name,
			Namespace:          item.Namespace,
			Description:        item.Description,
			Mode:               item.Mode,
			PromptPath:         item.PromptPath,
			Prompt:             item.Prompt,
			Aliases:            append([]string{}, item.Aliases...),
			Roles:              append([]string{}, item.Roles...),
			WriteScope:         append([]string{}, item.WriteScope...),
			Permissions:        append([]string{}, item.Permissions...),
			Capabilities:       append([]string{}, item.Capabilities...),
			ToolPolicy:         append([]string{}, item.ToolPolicy...),
			RecommendedBundles: append([]string{}, item.RecommendedBundles...),
			Path:               item.Path,
		})
	}
	return out, nil
}

// ListPackageRoles lists named subagent roles installed through packages.
func (a *Agent) ListPackageRoles(includeContent bool) ([]tools.PackageRoleEntry, error) {
	items, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).ListRoles(includeContent)
	if err != nil {
		return nil, err
	}
	out := make([]tools.PackageRoleEntry, 0, len(items))
	for _, item := range items {
		out = append(out, tools.PackageRoleEntry{
			PackageName:    item.PackageName,
			ID:             item.ID,
			Name:           item.Name,
			Description:    item.Description,
			BasePrompt:     item.BasePrompt,
			DefaultBundles: append([]string{}, item.DefaultBundles...),
			Tools:          append([]string{}, item.Tools...),
			WriteEnabled:   item.WriteEnabled,
			Capabilities:   append([]string{}, item.Capabilities...),
			ToolPolicy:     append([]string{}, item.ToolPolicy...),
			ModelHint:      item.ModelHint,
			BudgetHint:     item.BudgetHint,
			Display:        roleDisplayMap(item.Display),
			Path:           item.Path,
		})
	}
	return out, nil
}

func roleDisplayMap(display pkgregistry.Display) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(display.Label) != "" {
		out["label"] = strings.TrimSpace(display.Label)
	}
	if strings.TrimSpace(display.Color) != "" {
		out["color"] = strings.TrimSpace(display.Color)
	}
	if strings.TrimSpace(display.Icon) != "" {
		out["icon"] = strings.TrimSpace(display.Icon)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func packageEntriesFromRegistry(items []pkgregistry.Entry) []tools.PackageEntry {
	out := make([]tools.PackageEntry, 0, len(items))
	for _, item := range items {
		out = append(out, packageEntryFromRegistry(item))
	}
	return out
}

func packageEntryFromRegistry(item pkgregistry.Entry) tools.PackageEntry {
	return tools.PackageEntry{
		Name:               item.Name,
		Version:            item.Version,
		Description:        item.Description,
		Source:             item.Source,
		Digest:             item.Digest,
		Path:               item.Path,
		InstalledAt:        item.InstalledAt.Format(time.RFC3339),
		Resources:          packageResources(item.Resources),
		App:                packageAppFromRegistry(item.App),
		Permissions:        append([]string{}, item.Permissions...),
		Capabilities:       append([]string{}, item.Capabilities...),
		Provides:           append([]string{}, item.Provides...),
		Requires:           append([]string{}, item.Requires...),
		ToolPolicy:         append([]string{}, item.ToolPolicy...),
		SmokeTests:         packageSmokeTests(item.SmokeTests),
		RecommendedBundles: append([]string{}, item.RecommendedBundles...),
		Trust:              item.Trust,
	}
}

func packageAppFromRegistry(item pkgregistry.AppManifest) tools.PackageAppEntry {
	item = pkgregistry.NormalizeAppManifest(item)
	if pkgregistry.AppManifestEmpty(item) {
		return tools.PackageAppEntry{}
	}
	config := make(map[string]any, len(item.Config))
	for key, value := range item.Config {
		config[key] = value
	}
	return tools.PackageAppEntry{
		Kind:   item.Kind,
		ID:     item.ID,
		Label:  item.Label,
		Config: config,
	}
}

func packageSmokeTests(items []pkgregistry.SmokeTest) []tools.PackageSmokeTest {
	out := make([]tools.PackageSmokeTest, 0, len(items))
	for _, item := range items {
		out = append(out, tools.PackageSmokeTest{
			Name:                item.Name,
			Command:             item.Command,
			WorkingDir:          item.WorkingDir,
			TimeoutSeconds:      item.TimeoutSeconds,
			RequiredPermissions: append([]string{}, item.RequiredPermissions...),
			ExpectedExitCode:    item.ExpectedExitCode,
		})
	}
	return out
}

func packageResources(resources pkgregistry.Resources) map[string][]string {
	out := map[string][]string{}
	if len(resources.Skills) > 0 {
		out["skills"] = append([]string{}, resources.Skills...)
	}
	if len(resources.Prompts) > 0 {
		out["prompts"] = append([]string{}, resources.Prompts...)
	}
	if len(resources.Commands) > 0 {
		out["commands"] = append([]string{}, resources.Commands...)
	}
	if len(resources.Roles) > 0 {
		out["roles"] = append([]string{}, resources.Roles...)
	}
	if len(resources.Docs) > 0 {
		out["docs"] = append([]string{}, resources.Docs...)
	}
	if len(resources.Assets) > 0 {
		out["assets"] = append([]string{}, resources.Assets...)
	}
	return out
}
