package agent

import (
	"fmt"
	"github.com/tim5wang/godex/internal/agent/subagentpolicy"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"strings"
)

func subagentToolNames(agentType string) []string {
	return subagentpolicy.DefaultToolNames(agentType)
}

func subagentRequiredBundles(prompt string, explicit []string) []string {
	bundles := append([]string{}, explicit...)
	bundles = append(bundles, implicitSubagentBundlesForPrompt(prompt)...)
	if looksLikeWebResearchPrompt(prompt) {
		bundles = append(bundles, bundleWeb)
	}
	return uniqueStrings(bundles)
}

func implicitSubagentBundlesForPrompt(prompt string) []string {
	prompt = strings.ToLower(strings.TrimSpace(prompt))
	if prompt == "" {
		return nil
	}
	if looksLikeExplicitWebQuery(prompt) {
		return []string{bundleWeb}
	}
	return nil
}

func looksLikeWebResearchPrompt(prompt string) bool {
	query := strings.ToLower(strings.TrimSpace(prompt))
	if query == "" {
		return false
	}
	if containsAny(query,
		"网络检索", "网页检索", "联网检索", "网上调研", "网络调研", "联网调研",
		"源头链接", "来源链接", "引用链接", "官方来源", "网页来源",
		"web research", "online research", "internet research", "source links", "official sources", "official pages",
	) {
		return true
	}
	hasResearchCue := containsAny(query, "调研", "研究", "research", "investigate")
	if !hasResearchCue {
		return false
	}
	return containsAny(query, "网页", "网站", "网上", "联网", "搜索", "检索", "链接", "来源", "source", "search", "web", "online", "internet", "url", "link")
}

const boundedWebResearchTimeoutMS = 4 * 60 * 1000

func defaultWebResearchJobTimeout(requested int, webResearch bool) int {
	if requested > 0 || !webResearch {
		return requested
	}
	return boundedWebResearchTimeoutMS
}

func appendBoundedWebResearchInstructions(prompt string) string {
	return strings.TrimSpace(prompt) + "\n\nBounded web-research requirements: make at most 2 web_search calls and at most 3 web_fetch calls. If search backends fail or a site is blocked/JavaScript-only, switch immediately to known official URLs, public JSON APIs, GitHub/registry metadata, or local evidence. Do not retry the same failed query or URL. Stop once evidence is sufficient. Always finish with a concise handoff containing findings and source URLs. If the task specifies an output file, checkpoint it after each completed section and cite its path in the final handoff."
}

func appendRequiredSubagentTools(base, bundles, explicitTools, writeScope []string) []string {
	out := append([]string{}, base...)
	for _, bundle := range bundles {
		out = append(out, subagentToolsForRequiredBundle(bundle, writeScope)...)
	}
	out = append(out, explicitTools...)
	return uniqueStrings(out)
}

func subagentToolsForRequiredBundle(bundle string, writeScope []string) []string {
	switch strings.ToLower(strings.TrimSpace(bundle)) {
	case bundleWeb:
		return []string{"web_search", "web_fetch"}
	case bundleCoreCode:
		return []string{"bash", "read_file", "write_file", "edit_file"}
	case bundleWriting:
		// 4.5: writing 是虚拟能力 bundle——仅当存在有效写 scope 时才展开写工具；
		// 无 scope 则只读降级（不展开写工具）。
		if len(normalizeWriteScope(writeScope)) > 0 {
			return []string{"bash", "write_file", "edit_file"}
		}
		return nil
	default:
		return nil
	}
}

// subagentWriteCapable 判断 bundle 集合是否具备写能力：writing（显式能力声明）
// 或 core_code（隐式 writing，兼容 4.5 之前的调用）。
func subagentWriteCapable(bundles []string) bool {
	for _, bundle := range bundles {
		switch strings.ToLower(strings.TrimSpace(bundle)) {
		case bundleWriting, bundleCoreCode:
			return true
		}
	}
	return false
}

// subagentDefaultToolSurfaceWriteCapable 判断角色默认工具面是否含写工具：
// general-purpose 默认工具面 [bash read_file write_file edit_file] 硬编码含写工具，
// 不依赖 bundle（4.5 之前行为，需保持：显式 scope 对 general-purpose 始终生效）。
func subagentDefaultToolSurfaceWriteCapable(agentType string) bool {
	return normalizeSubagentType(agentType) == "general-purpose"
}

// resolveSubagentWriteScope 统一解析子 agent 写 scope（roadmap 4.5）：
// 优先级为 显式 write_scope > role.WriteScope（package role 声明）> nil。
// 写 scope 在 bundle 层面统一管理：bundle 集合不含 writing/core_code、且角色默认
// 工具面不含写工具时，即使显式传了 scope 也忽略（返回 nil，写工具不展开，天然只读）。
func resolveSubagentWriteScope(agentType string, explicit []string, role pkgregistry.Role, hasRole bool, bundles []string) []string {
	if !subagentWriteCapable(bundles) && !subagentDefaultToolSurfaceWriteCapable(agentType) {
		return nil
	}
	if s := normalizeWriteScope(explicit); len(s) > 0 {
		return s
	}
	if hasRole {
		if s := normalizeWriteScope(role.WriteScope); len(s) > 0 {
			return s
		}
	}
	return nil
}

func (a *Agent) resolveSubagentRole(agentType string) (pkgregistry.Role, bool) {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" || strings.EqualFold(agentType, "Explore") || agentType == "general-purpose" || a == nil || a.cfg == nil {
		return pkgregistry.Role{}, false
	}
	role, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).GetRole(agentType, true)
	if err != nil {
		return pkgregistry.Role{}, false
	}
	return role, true
}

func subagentToolNamesForRole(agentType string, role *pkgregistry.Role) []string {
	if role == nil {
		return subagentToolNames(agentType)
	}
	var tools []string
	if len(role.Tools) > 0 {
		for _, name := range role.Tools {
			name = strings.TrimSpace(name)
			if !supportedDurableSubagentTool(name) {
				continue
			}
			if !role.WriteEnabled && isDurableSubagentWriteTool(name) {
				continue
			}
			tools = append(tools, name)
		}
	} else {
		tools = []string{"bash", "read_file"}
		if role.WriteEnabled {
			tools = append(tools, "write_file", "edit_file")
		}
	}
	if len(tools) == 0 {
		return []string{"bash", "read_file"}
	}
	return uniqueStrings(tools)
}

func supportedDurableSubagentTool(name string) bool {
	return subagentpolicy.SupportedTool(name)
}

func isDurableSubagentInheritedParentTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "web_search", "web_fetch":
		return true
	default:
		return false
	}
}

func isDurableSubagentWriteTool(name string) bool {
	return subagentpolicy.IsWriteTool(name)
}

func narrowSubagentWriteTools(toolNames []string, writeScope []string) []string {
	return subagentpolicy.NarrowWriteTools(toolNames, writeScope)
}

func uniqueStrings(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func (a *Agent) validateSubagentToolInheritance(toolNames []string) error {
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !supportedDurableSubagentTool(name) {
			return fmt.Errorf("capability denied: child subagent requested tool:%s outside parent active tool policy", name)
		}
		if a != nil && a.toolHandler != nil && a.toolHandler.Get(name) != nil && !a.toolHandler.IsActive(name) {
			return fmt.Errorf("capability denied: child subagent requested inactive parent tool:%s", name)
		}
	}
	return nil
}

func (a *Agent) validateSubagentRequiredCapabilities(requiredBundles, requiredTools, writeScope []string) error {
	missingBundles := make([]string, 0)
	for _, bundle := range uniqueStrings(requiredBundles) {
		bundle = strings.TrimSpace(bundle)
		if bundle == "" {
			continue
		}
		for _, toolName := range subagentToolsForRequiredBundle(bundle, writeScope) {
			if !a.subagentParentToolActive(toolName) {
				missingBundles = append(missingBundles, bundle)
				break
			}
		}
	}
	missingTools := make([]string, 0)
	for _, toolName := range uniqueStrings(requiredTools) {
		toolName = strings.TrimSpace(toolName)
		if toolName == "" {
			continue
		}
		if !a.subagentParentToolActive(toolName) {
			missingTools = append(missingTools, toolName)
		}
	}
	if len(missingBundles) == 0 && len(missingTools) == 0 {
		return nil
	}
	return fmt.Errorf(
		"subagent_capability_required: missing active parent capability for bundle(s) %s tool(s) %s; enable required bundle(s) with tool_exchange and retry task",
		strings.Join(uniqueStrings(missingBundles), ","),
		strings.Join(uniqueStrings(missingTools), ","),
	)
}

func (a *Agent) subagentParentToolActive(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || a == nil || a.toolHandler == nil {
		return false
	}
	return a.toolHandler.Get(name) != nil && a.toolHandler.IsActive(name)
}

func roleCapabilitySummary(role pkgregistry.Role, toolNames []string, writeScope []string) []string {
	items := capabilitySummaryForTools(toolNames, writeScope)
	for _, capability := range role.Capabilities {
		if subagentCapabilityAllowed(capability, toolNames, writeScope) {
			items = append(items, strings.TrimSpace(capability))
		}
	}
	if strings.TrimSpace(role.ModelHint) != "" {
		items = append(items, "model:"+strings.TrimSpace(role.ModelHint))
	}
	return uniqueStrings(items)
}

func subagentCapabilityAllowed(capability string, toolNames []string, writeScope []string) bool {
	capability = strings.TrimSpace(capability)
	if capability == "" {
		return false
	}
	if strings.HasPrefix(capability, "tool:") {
		return subagentJobAllowsTool(toolNames, strings.TrimPrefix(capability, "tool:"))
	}
	if strings.HasPrefix(capability, "file:write:") {
		path := strings.TrimPrefix(capability, "file:write:")
		if path == "*" {
			return false
		}
		return pathAllowedByWriteScope(path, writeScope)
	}
	if strings.HasPrefix(capability, "file:read:") {
		return true
	}
	if strings.HasPrefix(capability, "shell:") {
		return subagentJobAllowsTool(toolNames, "bash")
	}
	return false
}

func capabilitySummaryForTools(toolNames []string, writeScope []string) []string {
	items := make([]string, 0, len(toolNames)+len(writeScope)+2)
	hasWriteTool := false
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name != "" {
			items = append(items, "tool:"+name)
		}
		if isDurableSubagentWriteTool(name) {
			hasWriteTool = true
		}
	}
	if len(writeScope) == 0 {
		items = append(items, "file:read:*")
	} else {
		for _, scope := range normalizeWriteScope(writeScope) {
			items = append(items, "file:read:"+scope)
			if hasWriteTool {
				items = append(items, "file:write:"+scope)
			}
		}
	}
	return uniqueStrings(items)
}

func subagentIdentityName(job *subagentJob) string {
	if job == nil {
		return "Subagent"
	}
	if strings.TrimSpace(job.RoleName) != "" {
		return strings.TrimSpace(job.RoleName)
	}
	if strings.TrimSpace(job.AgentType) != "" {
		return strings.TrimSpace(job.AgentType)
	}
	return "Subagent"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneStringAnyMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func normalizeSubagentType(agentType string) string {
	return subagentpolicy.NormalizeType(agentType)
}

func durableSubagentPromptForRole(agentType string, writeScope []string) string {
	lines := []string{
		"You are a durable subagent. Work independently, keep progress concise, and end with a clear handoff summary.",
		"Prefer workspace-relative paths. Do not revert unrelated user changes.",
	}
	scope := normalizeWriteScope(writeScope)
	if len(scope) > 0 {
		lines = append(lines,
			"Your shell and file tools run in an isolated workspace snapshot. Changes are not applied to the main workspace until the lead agent reviews and merges them.",
			"Use rg, sed, or focused read_file ranges to locate evidence before reading large files. Once you have enough evidence, stop exploring and produce the handoff.",
		)
	} else {
		lines = append(lines,
			"This is a read-only assignment. Use read_file with focused path ranges; shell commands are not available for shared read-only subagents.",
			"Once you have enough evidence, stop exploring and produce the handoff.",
		)
	}
	role := normalizeSubagentType(agentType)
	if role != "" && role != "Explore" && role != "general-purpose" {
		lines = append(lines, "Named role: "+role+". Treat this role name as guidance for your perspective and handoff style.")
	}
	if len(scope) > 0 {
		lines = append(lines, "Write scope: "+strings.Join(scope, ", ")+". Only changes under this scope are mergeable.")
	}
	return strings.Join(lines, " ")
}

func subagentBasePromptForRole(role pkgregistry.Role, writeScope []string) string {
	roleID := strings.TrimSpace(role.ID)
	if roleID == "" {
		roleID = role.Name
	}
	base := durableSubagentPromptForRole(roleID, writeScope)
	if strings.TrimSpace(role.Name) != "" && strings.TrimSpace(role.Name) != strings.TrimSpace(roleID) {
		base += " Display role name: " + strings.TrimSpace(role.Name) + "."
	}
	if strings.TrimSpace(role.Description) != "" {
		base += " Role description: " + strings.TrimSpace(role.Description) + "."
	}
	if strings.TrimSpace(role.BasePrompt) != "" {
		base += "\n\nRole instructions:\n" + strings.TrimSpace(role.BasePrompt)
	}
	return base
}

func durableSubagentPrompt(writeScope []string) string {
	return durableSubagentPromptForRole("", writeScope)
}

func normalizeWriteScope(scope []string) []string {
	return subagentpolicy.NormalizeWriteScope(scope)
}

func pathAllowedByWriteScope(path string, scope []string) bool {
	return subagentpolicy.PathAllowed(path, scope)
}
