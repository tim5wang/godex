package packages

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/core/skill"
)

// QualityReport summarizes package and tool ecosystem health for the Web UI.
type QualityReport struct {
	GeneratedAt      string            `json:"generated_at"`
	PackageCount     int               `json:"package_count"`
	SkillCount       int               `json:"skill_count"`
	PromptCount      int               `json:"prompt_count"`
	CommandCount     int               `json:"command_count"`
	RoleCount        int               `json:"role_count"`
	HighRiskPackages int               `json:"high_risk_packages"`
	ToolHealth       ToolHealthSummary `json:"tool_health"`
	FailureReasons   []FailureReason   `json:"failure_reasons,omitempty"`
	Packages         []PackageQuality  `json:"packages"`
}

// PackageQuality describes one installed package's declaration health.
type PackageQuality struct {
	Name                   string               `json:"name"`
	Version                string               `json:"version,omitempty"`
	Source                 string               `json:"source"`
	Trust                  string               `json:"trust"`
	Digest                 string               `json:"digest,omitempty"`
	ResourceCounts         map[string]int       `json:"resource_counts"`
	App                    AppManifest          `json:"app,omitempty"`
	AppIssues              []string             `json:"app_issues,omitempty"`
	Permissions            []string             `json:"permissions,omitempty"`
	Capabilities           []string             `json:"capabilities,omitempty"`
	Provides               []string             `json:"provides,omitempty"`
	Requires               []string             `json:"requires,omitempty"`
	DependencyIssues       []string             `json:"dependency_issues,omitempty"`
	ToolPolicy             []string             `json:"tool_policy,omitempty"`
	RecommendedBundles     []string             `json:"recommended_bundles,omitempty"`
	UnknownBundles         []string             `json:"unknown_bundles,omitempty"`
	ManifestIssues         []string             `json:"manifest_issues,omitempty"`
	ResourceIssues         []string             `json:"resource_issues,omitempty"`
	PermissionIssues       []string             `json:"permission_issues,omitempty"`
	CapabilityIssues       []string             `json:"capability_issues,omitempty"`
	ToolPolicyIssues       []string             `json:"tool_policy_issues,omitempty"`
	CommandDiagnostics     []ContractDiagnostic `json:"command_diagnostics,omitempty"`
	RoleDiagnostics        []ContractDiagnostic `json:"role_diagnostics,omitempty"`
	SmokeChecks            []SmokeCheck         `json:"smoke_checks,omitempty"`
	SmokeRuns              []SmokeRun           `json:"smoke_runs,omitempty"`
	InstallHealth          string               `json:"install_health,omitempty"`
	UpgradeHint            string               `json:"upgrade_hint,omitempty"`
	ReinstallAvailableHint string               `json:"reinstall_available_hint,omitempty"`
	RiskLevel              string               `json:"risk_level"`
	Score                  int                  `json:"score"`
}

type ContractDiagnostic struct {
	Type    string   `json:"type"`
	Name    string   `json:"name,omitempty"`
	Path    string   `json:"path,omitempty"`
	Issues  []string `json:"issues,omitempty"`
	Summary []string `json:"summary,omitempty"`
}

type SmokeCheck struct {
	Name       string    `json:"name"`
	Command    string    `json:"command,omitempty"`
	WorkingDir string    `json:"working_dir,omitempty"`
	Status     string    `json:"status"`
	Issues     []string  `json:"issues,omitempty"`
	LastRun    *SmokeRun `json:"last_run,omitempty"`
}

// ToolHealthSummary captures recent runtime tool reliability.
type ToolHealthSummary struct {
	InspectedSessions int        `json:"inspected_sessions"`
	TotalRuns         int        `json:"total_runs"`
	SuccessRuns       int        `json:"success_runs"`
	FailureRuns       int        `json:"failure_runs"`
	SuccessRate       float64    `json:"success_rate"`
	ByTool            []ToolStat `json:"by_tool,omitempty"`
}

// ToolStat is one per-tool aggregate row.
type ToolStat struct {
	Name        string  `json:"name"`
	Total       int     `json:"total"`
	Success     int     `json:"success"`
	Failure     int     `json:"failure"`
	SuccessRate float64 `json:"success_rate"`
	LastFailure string  `json:"last_failure,omitempty"`
}

// FailureReason is one normalized failure bucket.
type FailureReason struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// BuildQualityReport checks installed package declarations.
func (m *Manager) BuildQualityReport(generatedAt string, toolHealth ToolHealthSummary, knownBundles []string) (QualityReport, error) {
	items, err := m.List()
	if err != nil {
		return QualityReport{}, err
	}
	known := map[string]struct{}{}
	for _, bundle := range knownBundles {
		known[strings.TrimSpace(bundle)] = struct{}{}
	}
	report := QualityReport{
		GeneratedAt:    generatedAt,
		PackageCount:   len(items),
		ToolHealth:     toolHealth,
		FailureReasons: topFailureReasons(toolHealth.ByTool, 5),
		Packages:       make([]PackageQuality, 0, len(items)),
	}
	for _, item := range items {
		quality := m.packageQuality(item, known, items)
		report.Packages = append(report.Packages, quality)
		report.SkillCount += len(item.Resources.Skills)
		report.PromptCount += len(item.Resources.Prompts)
		report.CommandCount += len(item.Resources.Commands)
		report.RoleCount += len(item.Resources.Roles)
		if quality.RiskLevel == "high" {
			report.HighRiskPackages++
		}
	}
	return report, nil
}

func (m *Manager) packageQuality(item Entry, knownBundles map[string]struct{}, installed []Entry) PackageQuality {
	quality := PackageQuality{
		Name:                   item.Name,
		Version:                item.Version,
		Source:                 item.Source,
		Trust:                  item.Trust,
		Digest:                 item.Digest,
		ResourceCounts:         resourceCounts(item.Resources),
		App:                    NormalizeAppManifest(item.App),
		Permissions:            append([]string{}, item.Permissions...),
		Capabilities:           append([]string{}, item.Capabilities...),
		Provides:               append([]string{}, item.Provides...),
		Requires:               append([]string{}, item.Requires...),
		ToolPolicy:             append([]string{}, item.ToolPolicy...),
		RecommendedBundles:     append([]string{}, item.RecommendedBundles...),
		InstallHealth:          "installed",
		UpgradeHint:            "reinstall to refresh from recorded source",
		ReinstallAvailableHint: reinstallHint(item),
		RiskLevel:              "low",
		Score:                  100,
	}
	if strings.TrimSpace(item.Name) == "" {
		quality.ManifestIssues = append(quality.ManifestIssues, "missing package name")
	}
	if strings.TrimSpace(item.Version) == "" {
		quality.ManifestIssues = append(quality.ManifestIssues, "missing version")
	}
	if len(item.Resources.Skills)+len(item.Resources.Prompts)+len(item.Resources.Commands)+len(item.Resources.Roles)+len(item.Resources.Docs)+len(item.Resources.Assets) == 0 && AppManifestEmpty(item.App) {
		quality.ManifestIssues = append(quality.ManifestIssues, "no declared resources")
	}
	quality.AppIssues = append(quality.AppIssues, AppManifestIssues(item.App)...)
	for _, bundle := range item.RecommendedBundles {
		if _, ok := knownBundles[strings.TrimSpace(bundle)]; !ok {
			quality.UnknownBundles = append(quality.UnknownBundles, bundle)
		}
	}
	for _, permission := range item.Permissions {
		if strings.TrimSpace(permission) == "" {
			continue
		}
		if highRiskPermission(permission) {
			quality.PermissionIssues = append(quality.PermissionIssues, "high-risk permission: "+permission)
			continue
		}
		if !knownPermission(permission) {
			quality.PermissionIssues = append(quality.PermissionIssues, "unknown permission: "+permission)
		}
	}
	quality.CapabilityIssues = append(quality.CapabilityIssues, capabilityIssues(item.Capabilities)...)
	quality.ToolPolicyIssues = append(quality.ToolPolicyIssues, toolPolicyIssues(item.ToolPolicy)...)
	if len(item.Requires) > 0 {
		report := ValidateCandidateDependencies(item, installed)
		if !report.Empty() {
			for _, missing := range report.Missing {
				quality.DependencyIssues = append(quality.DependencyIssues, "missing "+missing)
			}
			for _, conflict := range report.Conflicts {
				quality.DependencyIssues = append(quality.DependencyIssues, "conflict "+conflict)
			}
			for _, cycle := range report.Cycles {
				quality.DependencyIssues = append(quality.DependencyIssues, "cycle "+joinPath(cycle))
			}
		}
	}
	quality.ResourceIssues = append(quality.ResourceIssues, m.missingResources(item)...)
	quality.ResourceIssues = append(quality.ResourceIssues, m.skillResourceIssues(item)...)
	commandIssues, commandDiagnostics := m.commandResourceDiagnostics(item)
	quality.ResourceIssues = append(quality.ResourceIssues, commandIssues...)
	quality.CommandDiagnostics = commandDiagnostics
	roleIssues, roleDiagnostics := m.roleResourceDiagnostics(item)
	quality.ResourceIssues = append(quality.ResourceIssues, roleIssues...)
	quality.RoleDiagnostics = roleDiagnostics
	quality.SmokeChecks = m.smokeChecks(item)
	for _, check := range quality.SmokeChecks {
		if len(check.Issues) > 0 {
			quality.ResourceIssues = append(quality.ResourceIssues, "smoke "+check.Name+" has quick-check issues")
		}
		if check.LastRun != nil {
			quality.SmokeRuns = append(quality.SmokeRuns, *check.LastRun)
		}
	}
	issueCount := len(quality.ManifestIssues) + len(quality.ResourceIssues) + len(quality.PermissionIssues) + len(quality.CapabilityIssues) + len(quality.ToolPolicyIssues) + len(quality.UnknownBundles) + len(quality.AppIssues) + len(quality.DependencyIssues)
	quality.Score -= issueCount * 15
	if quality.Trust != "local" {
		quality.Score -= 10
	}
	if quality.Score < 0 {
		quality.Score = 0
	}
	if len(quality.PermissionIssues) > 0 || len(quality.ResourceIssues) > 0 || len(quality.DependencyIssues) > 0 || quality.Score < 60 {
		quality.RiskLevel = "high"
	} else if len(quality.ManifestIssues) > 0 || len(quality.AppIssues) > 0 || len(quality.UnknownBundles) > 0 || quality.Trust != "local" || quality.Score < 85 {
		quality.RiskLevel = "medium"
	}
	return quality
}

func (m *Manager) missingResources(item Entry) []string {
	var issues []string
	for _, rel := range allResourcePaths(item.Resources) {
		path := filepath.Join(item.Path, filepath.Clean(rel))
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				issues = append(issues, "missing resource: "+rel)
			} else {
				issues = append(issues, fmt.Sprintf("resource %s: %v", rel, err))
			}
		}
	}
	return issues
}

func (m *Manager) skillResourceIssues(item Entry) []string {
	var issues []string
	loader := skill.NewLoader(m.skillsDir)
	for _, rel := range item.Resources.Skills {
		if filepath.Base(rel) != "SKILL.md" {
			issues = append(issues, "skill resource must point to SKILL.md: "+rel)
			continue
		}
		name := filepath.Base(filepath.Dir(rel))
		if strings.TrimSpace(name) == "" || name == "." {
			issues = append(issues, "invalid skill resource path: "+rel)
			continue
		}
		if _, err := loader.Load(name); err != nil {
			issues = append(issues, "skill not loadable: "+name+": "+err.Error())
		}
	}
	return issues
}

func (m *Manager) commandResourceIssues(item Entry) []string {
	issues, _ := m.commandResourceDiagnostics(item)
	return issues
}

func (m *Manager) commandResourceDiagnostics(item Entry) ([]string, []ContractDiagnostic) {
	var issues []string
	diagnostics := make([]ContractDiagnostic, 0, len(item.Resources.Commands))
	roleIDs := m.packageRoleIDs(item)
	for _, rel := range item.Resources.Commands {
		diagnostic := ContractDiagnostic{Type: "command", Name: resourceName(rel), Path: rel}
		command, err := readCommandResource(item, rel, false)
		if err != nil {
			issue := "command not loadable: " + rel + ": " + err.Error()
			issues = append(issues, issue)
			diagnostic.Issues = append(diagnostic.Issues, issue)
			diagnostics = append(diagnostics, diagnostic)
			continue
		}
		diagnostic.Name = command.Name
		diagnostic.Summary = append(diagnostic.Summary, "mode:"+command.Mode)
		if len(command.Roles) > 0 {
			diagnostic.Summary = append(diagnostic.Summary, "roles:"+strings.Join(command.Roles, ","))
		}
		if len(command.Capabilities) > 0 {
			diagnostic.Summary = append(diagnostic.Summary, "capabilities:"+strings.Join(command.Capabilities, ","))
		}
		if len(command.ToolPolicy) > 0 {
			diagnostic.Summary = append(diagnostic.Summary, "tool_policy:"+strings.Join(command.ToolPolicy, ","))
		}
		if strings.TrimSpace(command.Name) == "" {
			diagnostic.Issues = append(diagnostic.Issues, "command missing name: "+rel)
		}
		if !knownCommandMode(command.Mode) {
			diagnostic.Issues = append(diagnostic.Issues, "command has unknown mode: "+command.Name+": "+command.Mode)
		}
		for _, role := range command.Roles {
			if _, ok := roleIDs[role]; !ok {
				diagnostic.Issues = append(diagnostic.Issues, "command role not found: "+role)
			}
		}
		diagnostic.Issues = append(diagnostic.Issues, capabilityIssues(command.Capabilities)...)
		diagnostic.Issues = append(diagnostic.Issues, toolPolicyIssues(command.ToolPolicy)...)
		for _, issue := range diagnostic.Issues {
			issues = append(issues, issue)
		}
		if strings.TrimSpace(command.PromptPath) != "" {
			promptPath := filepath.Join(item.Path, filepath.Clean(command.PromptPath))
			if _, err := os.Stat(promptPath); err != nil {
				if os.IsNotExist(err) {
					issue := "command prompt missing: " + command.PromptPath
					issues = append(issues, issue)
					diagnostic.Issues = append(diagnostic.Issues, issue)
				} else {
					issue := fmt.Sprintf("command prompt %s: %v", command.PromptPath, err)
					issues = append(issues, issue)
					diagnostic.Issues = append(diagnostic.Issues, issue)
				}
			}
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	return issues, diagnostics
}

func (m *Manager) roleResourceIssues(item Entry) []string {
	issues, _ := m.roleResourceDiagnostics(item)
	return issues
}

func (m *Manager) roleResourceDiagnostics(item Entry) ([]string, []ContractDiagnostic) {
	var issues []string
	diagnostics := make([]ContractDiagnostic, 0, len(item.Resources.Roles))
	for _, rel := range item.Resources.Roles {
		diagnostic := ContractDiagnostic{Type: "role", Name: resourceName(rel), Path: rel}
		role, err := readRoleResource(item, rel, false)
		if err != nil {
			issue := "role not loadable: " + rel + ": " + err.Error()
			issues = append(issues, issue)
			diagnostic.Issues = append(diagnostic.Issues, issue)
			diagnostics = append(diagnostics, diagnostic)
			continue
		}
		diagnostic.Name = role.ID
		if len(role.Capabilities) > 0 {
			diagnostic.Summary = append(diagnostic.Summary, "capabilities:"+strings.Join(role.Capabilities, ","))
		}
		if len(role.ToolPolicy) > 0 {
			diagnostic.Summary = append(diagnostic.Summary, "tool_policy:"+strings.Join(role.ToolPolicy, ","))
		}
		if role.ModelHint != "" {
			diagnostic.Summary = append(diagnostic.Summary, "model:"+role.ModelHint)
		}
		if role.BudgetHint != "" {
			diagnostic.Summary = append(diagnostic.Summary, "budget:"+role.BudgetHint)
		}
		if strings.TrimSpace(role.ID) == "" {
			diagnostic.Issues = append(diagnostic.Issues, "role missing id: "+rel)
		}
		if strings.TrimSpace(role.Name) == "" {
			diagnostic.Issues = append(diagnostic.Issues, "role missing name: "+rel)
		}
		diagnostic.Issues = append(diagnostic.Issues, capabilityIssues(role.Capabilities)...)
		diagnostic.Issues = append(diagnostic.Issues, toolPolicyIssues(role.ToolPolicy)...)
		for _, issue := range diagnostic.Issues {
			issues = append(issues, issue)
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	return issues, diagnostics
}

func (m *Manager) packageRoleIDs(item Entry) map[string]struct{} {
	out := map[string]struct{}{}
	for _, rel := range item.Resources.Roles {
		role, err := readRoleResource(item, rel, false)
		if err != nil {
			continue
		}
		out[role.ID] = struct{}{}
		if role.Name != "" {
			out[role.Name] = struct{}{}
		}
	}
	return out
}

func (m *Manager) smokeChecks(item Entry) []SmokeCheck {
	runs, _ := m.ListSmokeRuns(item.Name)
	lastByName := map[string]SmokeRun{}
	for _, run := range runs {
		if _, ok := lastByName[run.SmokeName]; !ok {
			lastByName[run.SmokeName] = run
		}
	}
	checks := make([]SmokeCheck, 0, len(item.SmokeTests))
	for _, smoke := range item.SmokeTests {
		issues := SmokeQuickCheck(item, smoke)
		status := "ready"
		if len(issues) > 0 {
			status = "invalid"
		}
		check := SmokeCheck{
			Name:       smoke.Name,
			Command:    smoke.Command,
			WorkingDir: smoke.WorkingDir,
			Status:     status,
			Issues:     issues,
		}
		if run, ok := lastByName[smoke.Name]; ok {
			check.LastRun = &run
			if len(issues) == 0 {
				check.Status = run.Status
			}
		}
		checks = append(checks, check)
	}
	return checks
}

func knownCommandMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "", "prompt_only", "agent_turn", "subagent_job", "external_agent":
		return true
	default:
		return false
	}
}

func resourceCounts(resources Resources) map[string]int {
	return map[string]int{
		"skills":   len(resources.Skills),
		"prompts":  len(resources.Prompts),
		"commands": len(resources.Commands),
		"roles":    len(resources.Roles),
		"docs":     len(resources.Docs),
		"assets":   len(resources.Assets),
	}
}

func allResourcePaths(resources Resources) []string {
	var out []string
	out = append(out, resources.Skills...)
	out = append(out, resources.Prompts...)
	out = append(out, resources.Commands...)
	out = append(out, resources.Roles...)
	out = append(out, resources.Docs...)
	out = append(out, resources.Assets...)
	return out
}

func knownPermission(permission string) bool {
	permission = strings.TrimSpace(permission)
	if name, ok := strings.CutPrefix(permission, "credential:"); ok {
		return strings.TrimSpace(name) != ""
	}
	switch permission {
	case "network", "filesystem", "browser", "desktop", "shell", "memory", "packages", "read_file", "write_file", "edit_file", "bash", "background", "subagent", "external_agents", "mcp":
		return true
	default:
		return false
	}
}

// IsPlatformCapability reports whether a capability is supplied by the GoDex
// platform rather than by an installed package runtime.
func IsPlatformCapability(capability string) bool {
	return knownCapability(capability)
}

func knownCapability(capability string) bool {
	capability = strings.TrimSpace(capability)
	if capability == "" {
		return true
	}
	parts := strings.Split(capability, ":")
	switch parts[0] {
	case "tool", "file", "shell", "network", "memory", "package", "role":
		return true
	default:
		return false
	}
}

func capabilityIssues(items []string) []string {
	var issues []string
	for _, item := range items {
		if !knownCapability(item) {
			issues = append(issues, "unknown capability: "+item)
		}
	}
	return issues
}

func knownToolPolicy(item string) bool {
	item = strings.TrimSpace(item)
	if item == "" {
		return true
	}
	parts := strings.Split(item, ":")
	if len(parts) < 3 {
		return false
	}
	switch parts[0] {
	case "tool", "shell", "file", "network":
	default:
		return false
	}
	switch parts[1] {
	case "allow", "deny", "read", "write":
		return true
	default:
		return false
	}
}

func toolPolicyIssues(items []string) []string {
	var issues []string
	for _, item := range items {
		if !knownToolPolicy(item) {
			issues = append(issues, "invalid tool policy: "+item)
		}
	}
	return issues
}

func reinstallHint(item Entry) string {
	if strings.TrimSpace(item.Source) == "" {
		return "no recorded source"
	}
	return "reinstall from recorded source"
}

func joinPath(parts []string) string {
	return strings.Join(parts, " -> ")
}

func highRiskPermission(permission string) bool {
	switch strings.TrimSpace(permission) {
	case "shell", "desktop", "filesystem", "write_file", "edit_file", "bash", "background", "external_agents":
		return true
	default:
		return false
	}
}

func topFailureReasons(rows []ToolStat, limit int) []FailureReason {
	counts := map[string]int{}
	for _, row := range rows {
		if strings.TrimSpace(row.LastFailure) != "" {
			counts[row.LastFailure] += row.Failure
		}
	}
	reasons := make([]FailureReason, 0, len(counts))
	for reason, count := range counts {
		reasons = append(reasons, FailureReason{Reason: reason, Count: count})
	}
	sort.Slice(reasons, func(i, j int) bool {
		if reasons[i].Count == reasons[j].Count {
			return reasons[i].Reason < reasons[j].Reason
		}
		return reasons[i].Count > reasons[j].Count
	})
	if limit > 0 && len(reasons) > limit {
		reasons = reasons[:limit]
	}
	return reasons
}
