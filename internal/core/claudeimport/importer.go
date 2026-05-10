package claudeimport

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"gopkg.in/yaml.v3"
)

const DefaultPackageName = "claude-import"

type Plan struct {
	Source      string     `json:"source"`
	PackageName string     `json:"package_name"`
	Skills      []Resource `json:"skills,omitempty"`
	Commands    []Resource `json:"commands,omitempty"`
	Roles       []Resource `json:"roles,omitempty"`
	Settings    []string   `json:"settings,omitempty"`
	Warnings    []string   `json:"warnings,omitempty"`
}

type Resource struct {
	Name        string   `json:"name"`
	SourcePath  string   `json:"source_path"`
	TargetPath  string   `json:"target_path"`
	Description string   `json:"description,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

type Options struct {
	Source      string
	PackageName string
}

type frontmatterDocument struct {
	Values map[string]any
	Body   string
}

type claudeCommandSpec struct {
	Name               string   `yaml:"name"`
	Namespace          string   `yaml:"namespace,omitempty"`
	Description        string   `yaml:"description,omitempty"`
	Mode               string   `yaml:"mode"`
	PromptPath         string   `yaml:"prompt_path"`
	Aliases            []string `yaml:"aliases,omitempty"`
	Permissions        []string `yaml:"permissions,omitempty"`
	Capabilities       []string `yaml:"capabilities,omitempty"`
	ToolPolicy         []string `yaml:"tool_policy,omitempty"`
	RecommendedBundles []string `yaml:"recommended_bundles,omitempty"`
}

type claudeRoleSpec struct {
	ID             string   `yaml:"id"`
	Name           string   `yaml:"name"`
	Description    string   `yaml:"description,omitempty"`
	BasePrompt     string   `yaml:"base_prompt"`
	DefaultBundles []string `yaml:"default_bundles,omitempty"`
	Tools          []string `yaml:"tools,omitempty"`
	WriteEnabled   bool     `yaml:"write_enabled,omitempty"`
	Capabilities   []string `yaml:"capabilities,omitempty"`
	ToolPolicy     []string `yaml:"tool_policy,omitempty"`
	ModelHint      string   `yaml:"model_hint,omitempty"`
	BudgetHint     string   `yaml:"budget_hint,omitempty"`
}

func NewPlan(options Options) (Plan, error) {
	source := strings.TrimSpace(options.Source)
	if source == "" {
		source = ".claude"
	}
	source = expandHome(source)
	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		return Plan{}, err
	}
	info, err := os.Stat(sourceAbs)
	if err != nil {
		return Plan{}, fmt.Errorf("stat Claude source %s: %w", source, err)
	}
	if !info.IsDir() {
		return Plan{}, fmt.Errorf("Claude source is not a directory: %s", source)
	}
	packageName := safeName(options.PackageName)
	if packageName == "" {
		packageName = DefaultPackageName
	}
	plan := Plan{Source: sourceAbs, PackageName: packageName}
	if err := scanSkills(&plan); err != nil {
		return Plan{}, err
	}
	if err := scanCommands(&plan); err != nil {
		return Plan{}, err
	}
	if err := scanAgents(&plan); err != nil {
		return Plan{}, err
	}
	if err := scanSettings(&plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func BuildPackage(plan Plan, target string) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("missing target package directory")
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}
	resources := pkgregistry.Resources{}
	for _, item := range plan.Skills {
		if err := copyFile(item.SourcePath, filepath.Join(target, item.TargetPath)); err != nil {
			return err
		}
		resources.Skills = append(resources.Skills, filepath.ToSlash(item.TargetPath))
	}
	for _, item := range plan.Commands {
		doc, err := readFrontmatterDocument(item.SourcePath)
		if err != nil {
			return err
		}
		promptTarget := commandPromptPath(item.TargetPath)
		if err := writeFile(filepath.Join(target, promptTarget), []byte(strings.TrimSpace(doc.Body)+"\n")); err != nil {
			return err
		}
		command := commandSpecFor(plan, item, doc, promptTarget)
		data, err := yaml.Marshal(command)
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(target, item.TargetPath), data); err != nil {
			return err
		}
		resources.Commands = append(resources.Commands, filepath.ToSlash(item.TargetPath))
		resources.Prompts = append(resources.Prompts, filepath.ToSlash(promptTarget))
	}
	for _, item := range plan.Roles {
		doc, err := readFrontmatterDocument(item.SourcePath)
		if err != nil {
			return err
		}
		role := roleSpecFor(plan, item, doc)
		data, err := yaml.Marshal(role)
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(target, item.TargetPath), data); err != nil {
			return err
		}
		resources.Roles = append(resources.Roles, filepath.ToSlash(item.TargetPath))
	}
	manifest := pkgregistry.Manifest{
		Name:        plan.PackageName,
		Version:     "0.1.0",
		Description: "Imported Claude Code skills, commands, and agents.",
		Resources:   resources,
		Permissions: importPermissions(plan),
		Capabilities: []string{
			"package:claude-import",
		},
		ToolPolicy: []string{
			"tool:read:imported-claude-resources",
		},
		RecommendedBundles: []string{"core_code", "planning"},
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(target, pkgregistry.ManifestFileName), data)
}

func scanSkills(plan *Plan) error {
	root := filepath.Join(plan.Source, "skills")
	return walkExisting(root, func(path string, entry os.DirEntry) error {
		if entry.IsDir() || !strings.EqualFold(entry.Name(), "SKILL.md") {
			return nil
		}
		relDir, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		name := safeRelName(relDir)
		if name == "" {
			name = safeName(filepath.Base(filepath.Dir(path)))
		}
		target := filepath.Join("skills", name, "SKILL.md")
		description := ""
		if doc, err := readFrontmatterDocument(path); err == nil {
			description = firstString(doc.Values, "description", "summary")
		}
		plan.Skills = append(plan.Skills, Resource{Name: name, SourcePath: path, TargetPath: target, Description: description})
		return nil
	})
}

func scanCommands(plan *Plan) error {
	root := filepath.Join(plan.Source, "commands")
	return walkExisting(root, func(path string, entry os.DirEntry) error {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		doc, err := readFrontmatterDocument(path)
		if err != nil {
			return err
		}
		name := firstNonEmpty(firstString(doc.Values, "name"), safeName(strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))))
		targetRel := strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel)) + ".yaml"
		tools, warnings := claudeTools(firstString(doc.Values, "allowed-tools", "allowed_tools", "tools", "tool"))
		plan.Commands = append(plan.Commands, Resource{
			Name:        name,
			SourcePath:  path,
			TargetPath:  filepath.Join("commands", filepath.FromSlash(targetRel)),
			Description: firstString(doc.Values, "description"),
			Tools:       tools,
			Warnings:    warnings,
		})
		plan.Warnings = append(plan.Warnings, warnings...)
		return nil
	})
}

func scanAgents(plan *Plan) error {
	root := filepath.Join(plan.Source, "agents")
	return walkExisting(root, func(path string, entry os.DirEntry) error {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		doc, err := readFrontmatterDocument(path)
		if err != nil {
			return err
		}
		name := firstNonEmpty(firstString(doc.Values, "name"), safeName(strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))))
		targetRel := strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel)) + ".yaml"
		tools, warnings := claudeTools(firstString(doc.Values, "tools", "allowed-tools", "allowed_tools"))
		plan.Roles = append(plan.Roles, Resource{
			Name:        name,
			SourcePath:  path,
			TargetPath:  filepath.Join("roles", filepath.FromSlash(targetRel)),
			Description: firstString(doc.Values, "description"),
			Tools:       tools,
			Warnings:    warnings,
		})
		plan.Warnings = append(plan.Warnings, warnings...)
		return nil
	})
}

func scanSettings(plan *Plan) error {
	entries, err := os.ReadDir(plan.Source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "settings") && strings.EqualFold(filepath.Ext(name), ".json") {
			path := filepath.Join(plan.Source, name)
			plan.Settings = append(plan.Settings, path)
			plan.Warnings = append(plan.Warnings, "Claude settings are inspected for diagnostics only and are not executed or enabled: "+path)
		}
	}
	sort.Strings(plan.Settings)
	return nil
}

func walkExisting(root string, fn func(string, os.DirEntry) error) error {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "dist", "build":
				return filepath.SkipDir
			}
		}
		return fn(path, entry)
	})
}

func readFrontmatterDocument(path string) (frontmatterDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return frontmatterDocument{}, err
	}
	values, body, err := splitFrontmatter(string(data))
	if err != nil {
		return frontmatterDocument{}, fmt.Errorf("parse %s frontmatter: %w", path, err)
	}
	return frontmatterDocument{Values: values, Body: body}, nil
}

func splitFrontmatter(raw string) (map[string]any, string, error) {
	lines := strings.Split(raw, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return map[string]any{}, raw, nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return map[string]any{}, raw, nil
	}
	values := map[string]any{}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &values); err != nil {
		return nil, "", err
	}
	return values, strings.Join(lines[end+1:], "\n"), nil
}

func commandSpecFor(plan Plan, item Resource, doc frontmatterDocument, promptPath string) claudeCommandSpec {
	tools := item.Tools
	permissions := permissionsForTools(tools)
	return claudeCommandSpec{
		Name:               item.Name,
		Namespace:          commandNamespace(plan.PackageName, item.TargetPath),
		Description:        firstNonEmpty(item.Description, firstLine(doc.Body)),
		Mode:               "agent_turn",
		PromptPath:         filepath.ToSlash(promptPath),
		Aliases:            stringList(doc.Values["aliases"]),
		Permissions:        permissions,
		Capabilities:       capabilitiesForTools(tools),
		ToolPolicy:         toolPolicyForTools(tools),
		RecommendedBundles: bundlesForTools(tools),
	}
}

func roleSpecFor(plan Plan, item Resource, doc frontmatterDocument) claudeRoleSpec {
	tools := item.Tools
	return claudeRoleSpec{
		ID:             safeName(plan.PackageName) + ":" + item.Name,
		Name:           firstNonEmpty(firstString(doc.Values, "name"), item.Name),
		Description:    firstNonEmpty(item.Description, firstLine(doc.Body)),
		BasePrompt:     strings.TrimSpace(doc.Body),
		DefaultBundles: bundlesForTools(tools),
		Tools:          tools,
		WriteEnabled:   hasWriteTool(tools),
		Capabilities:   capabilitiesForTools(tools),
		ToolPolicy:     toolPolicyForTools(tools),
		ModelHint:      firstString(doc.Values, "model"),
		BudgetHint:     firstString(doc.Values, "budget", "budget_hint"),
	}
}

func commandNamespace(packageName, targetPath string) string {
	rel := strings.TrimPrefix(filepath.ToSlash(targetPath), "commands/")
	parts := strings.Split(rel, "/")
	if len(parts) > 1 {
		if ns := safeName(parts[0]); ns != "" {
			return ns
		}
	}
	return safeName(packageName)
}

func commandPromptPath(commandPath string) string {
	rel := strings.TrimPrefix(filepath.ToSlash(commandPath), "commands/")
	rel = strings.TrimSuffix(rel, filepath.Ext(rel)) + ".md"
	return filepath.Join("prompts", "commands", filepath.FromSlash(rel))
}

func claudeTools(raw string) ([]string, []string) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	seen := map[string]struct{}{}
	var tools []string
	var warnings []string
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		if idx := strings.Index(token, "("); idx >= 0 {
			token = token[:idx]
		}
		mapped, ok := mapClaudeTool(token)
		if !ok {
			warnings = append(warnings, "Unsupported Claude tool is not auto-enabled: "+token)
			continue
		}
		for _, tool := range mapped {
			if _, exists := seen[tool]; exists {
				continue
			}
			seen[tool] = struct{}{}
			tools = append(tools, tool)
		}
	}
	sort.Strings(tools)
	return tools, warnings
}

func mapClaudeTool(tool string) ([]string, bool) {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "read", "ls":
		return []string{"read_file"}, true
	case "grep", "glob":
		return []string{"glob", "read_file"}, true
	case "write":
		return []string{"write_file"}, true
	case "edit", "multiedit":
		return []string{"edit_file"}, true
	case "bash":
		return []string{"bash"}, true
	case "webfetch":
		return []string{"web_fetch"}, true
	case "websearch":
		return []string{"web_search"}, true
	case "task":
		return []string{"subagent"}, true
	default:
		return nil, false
	}
}

func permissionsForTools(tools []string) []string {
	permissions := make([]string, 0, len(tools))
	for _, tool := range tools {
		switch tool {
		case "read_file", "glob":
			permissions = append(permissions, "read_file")
		case "write_file":
			permissions = append(permissions, "write_file")
		case "edit_file":
			permissions = append(permissions, "edit_file")
		case "bash":
			permissions = append(permissions, "bash", "shell")
		case "web_fetch", "web_search":
			permissions = append(permissions, "network")
		case "subagent":
			permissions = append(permissions, "subagent")
		}
	}
	return uniqueStrings(permissions)
}

func capabilitiesForTools(tools []string) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, "tool:"+tool)
	}
	return uniqueStrings(out)
}

func toolPolicyForTools(tools []string) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		switch tool {
		case "write_file", "edit_file", "bash":
			out = append(out, "tool:write:"+tool)
		default:
			out = append(out, "tool:allow:"+tool)
		}
	}
	return uniqueStrings(out)
}

func bundlesForTools(tools []string) []string {
	bundles := []string{"core_code"}
	for _, tool := range tools {
		switch tool {
		case "subagent":
			bundles = append(bundles, "subagent")
		case "bash":
			bundles = append(bundles, "background")
		case "web_fetch", "web_search":
			bundles = append(bundles, "web")
		}
	}
	return uniqueStrings(bundles)
}

func importPermissions(plan Plan) []string {
	var permissions []string
	for _, item := range append(append([]Resource{}, plan.Commands...), plan.Roles...) {
		permissions = append(permissions, permissionsForTools(item.Tools)...)
	}
	return uniqueStrings(permissions)
}

func hasWriteTool(tools []string) bool {
	for _, tool := range tools {
		switch tool {
		case "write_file", "edit_file", "bash":
			return true
		}
	}
	return false
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(fmt.Sprint(values[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

func stringList(value any) []string {
	switch typed := value.(type) {
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" && text != "<nil>" {
				out = append(out, text)
			}
		}
		return out
	case []string:
		return uniqueStrings(typed)
	case string:
		return splitLooseList(typed)
	default:
		return nil
	}
}

func splitLooseList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return uniqueStrings(out)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if line != "" {
			return line
		}
	}
	return ""
}

func safeRelName(rel string) string {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == "/" {
		return ""
	}
	parts := strings.Split(rel, "/")
	for i := range parts {
		parts[i] = safeName(parts[i])
	}
	return safeName(strings.Join(parts, "-"))
}

func safeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	dash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if r == '-' || r == '_' || r == '.' || r == ':' || r == '/' || r == ' ' || r == '\t' {
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func uniqueStrings(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeFile(dst, data)
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
