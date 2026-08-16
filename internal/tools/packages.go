package tools

import "context"

type PackageRuntime interface {
	ListPackages() ([]PackageEntry, error)
	InstallPackage(source string) (PackageEntry, error)
	RemovePackage(name string) (PackageEntry, error)
	ListPrompts(includeContent bool) ([]PromptEntry, error)
	ListPackageCommands(includeContent bool) ([]PackageCommandEntry, error)
	ListPackageRoles(includeContent bool) ([]PackageRoleEntry, error)
}

type PackageEntry struct {
	Name               string              `json:"name"`
	Version            string              `json:"version"`
	Description        string              `json:"description,omitempty"`
	Source             string              `json:"source"`
	Digest             string              `json:"digest"`
	Path               string              `json:"path"`
	InstalledAt        string              `json:"installed_at"`
	Resources          map[string][]string `json:"resources,omitempty"`
	App                PackageAppEntry     `json:"app,omitempty"`
	Permissions        []string            `json:"permissions,omitempty"`
	Capabilities       []string            `json:"capabilities,omitempty"`
	Provides           []string            `json:"provides,omitempty"`
	Requires           []string            `json:"requires,omitempty"`
	ToolPolicy         []string            `json:"tool_policy,omitempty"`
	RecommendedBundles []string            `json:"recommended_bundles,omitempty"`
	SmokeTests         []PackageSmokeTest  `json:"smoke_tests,omitempty"`
	Trust              string              `json:"trust"`
}

type PackageAppEntry struct {
	Kind   string         `json:"kind,omitempty"`
	ID     string         `json:"id,omitempty"`
	Label  string         `json:"label,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}

type PackageSmokeTest struct {
	Name                string   `json:"name"`
	Command             string   `json:"command"`
	WorkingDir          string   `json:"working_dir,omitempty"`
	TimeoutSeconds      int      `json:"timeout_seconds,omitempty"`
	RequiredPermissions []string `json:"required_permissions,omitempty"`
	ExpectedExitCode    *int     `json:"expected_exit_code,omitempty"`
}

type PromptEntry struct {
	PackageName string `json:"package_name"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Content     string `json:"content,omitempty"`
}

type PackageCommandEntry struct {
	PackageName        string   `json:"package_name"`
	Name               string   `json:"name"`
	Namespace          string   `json:"namespace,omitempty"`
	Description        string   `json:"description,omitempty"`
	Mode               string   `json:"mode,omitempty"`
	PromptPath         string   `json:"prompt_path,omitempty"`
	Prompt             string   `json:"prompt,omitempty"`
	Aliases            []string `json:"aliases,omitempty"`
	Roles              []string `json:"roles,omitempty"`
	WriteScope         []string `json:"write_scope,omitempty"`
	Permissions        []string `json:"permissions,omitempty"`
	Capabilities       []string `json:"capabilities,omitempty"`
	ToolPolicy         []string `json:"tool_policy,omitempty"`
	RecommendedBundles []string `json:"recommended_bundles,omitempty"`
	Path               string   `json:"path"`
}

type PackageRoleEntry struct {
	PackageName    string            `json:"package_name"`
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	BasePrompt     string            `json:"base_prompt,omitempty"`
	DefaultBundles []string          `json:"default_bundles,omitempty"`
	Tools          []string          `json:"tools,omitempty"`
	WriteEnabled   bool              `json:"write_enabled,omitempty"`
	Capabilities   []string          `json:"capabilities,omitempty"`
	ToolPolicy     []string          `json:"tool_policy,omitempty"`
	ModelHint      string            `json:"model_hint,omitempty"`
	BudgetHint     string            `json:"budget_hint,omitempty"`
	Display        map[string]string `json:"display,omitempty"`
	Path           string            `json:"path"`
}

type packageListArgs struct{}

func NewListPackagesTool(runtime PackageRuntime) Tool {
	return NewTypedTool(NewToolSpec("list_packages", "List installed declaration-only Godex packages.", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args packageListArgs) (ToolResult, error) {
		items, err := runtime.ListPackages()
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: items}, nil
	})
}

type installPackageArgs struct {
	Source string `json:"source"`
}

func NewInstallPackageTool(runtime PackageRuntime) Tool {
	return NewTypedTool(NewToolSpec("install_package", "Install a declaration-only Godex package from a local path, Git URL, or owner/repo.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"source": map[string]interface{}{"type": "string"},
		},
		"required": []string{"source"},
	}, nil), func(ctx context.Context, args installPackageArgs) (ToolResult, error) {
		item, err := runtime.InstallPackage(args.Source)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: item}, nil
	})
}

type removePackageArgs struct {
	Name string `json:"name"`
}

func NewRemovePackageTool(runtime PackageRuntime) Tool {
	return NewTypedTool(NewToolSpec("remove_package", "Remove one installed Godex package.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
		"required": []string{"name"},
	}, nil), func(ctx context.Context, args removePackageArgs) (ToolResult, error) {
		item, err := runtime.RemovePackage(args.Name)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: item}, nil
	})
}

type listPromptsArgs struct {
	IncludeContent bool `json:"include_content,omitempty"`
}

func NewListPromptsTool(runtime PackageRuntime) Tool {
	return NewTypedTool(NewToolSpec("list_prompts", "List prompt templates installed through Godex packages.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"include_content": map[string]interface{}{"type": "boolean"},
		},
	}, nil), func(ctx context.Context, args listPromptsArgs) (ToolResult, error) {
		items, err := runtime.ListPrompts(args.IncludeContent)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: items}, nil
	})
}

type listPackageCommandsArgs struct {
	IncludeContent bool `json:"include_content,omitempty"`
}

func NewListPackageCommandsTool(runtime PackageRuntime) Tool {
	return NewTypedTool(NewToolSpec("list_package_commands", "List slash-command workflow declarations installed through Godex packages.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"include_content": map[string]interface{}{"type": "boolean"},
		},
	}, nil), func(ctx context.Context, args listPackageCommandsArgs) (ToolResult, error) {
		items, err := runtime.ListPackageCommands(args.IncludeContent)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: items}, nil
	})
}

type listPackageRolesArgs struct {
	IncludeContent bool `json:"include_content,omitempty"`
}

func NewListPackageRolesTool(runtime PackageRuntime) Tool {
	return NewTypedTool(NewToolSpec("list_package_roles", "List named subagent role declarations installed through Godex packages.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"include_content": map[string]interface{}{"type": "boolean"},
		},
	}, nil), func(ctx context.Context, args listPackageRolesArgs) (ToolResult, error) {
		items, err := runtime.ListPackageRoles(args.IncludeContent)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: items}, nil
	})
}
