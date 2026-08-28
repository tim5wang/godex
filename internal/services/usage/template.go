package usage

import (
	"strings"

	"github.com/tim5wang/godex/internal/core/templates"
)

// TemplateFromBizKey derives a user-editable agent template from a business
// key's whitelist fields (M4 P1 convergence migration). Field mapping mirrors
// the §4.2 homogeneity table: DefaultPrompt→BasePrompt, SandboxTools→Tools,
// MCPServers→MCPServers, Skills/Packages/ProjectDir map 1:1. Models stay on
// the key (routing/credit layer, not a template capability boundary).
func TemplateFromBizKey(key *BizAPIKey) templates.AgentTemplate {
	name := strings.TrimSpace(key.Name)
	if name == "" {
		name = key.ID
	}
	desc := "由业务智能体 key " + key.ID + " 派生（M4 P1 收敛，可从 key 覆盖层调整）"
	if key.Description != "" {
		desc = key.Description + "（" + desc + "）"
	}
	return templates.AgentTemplate{
		ID:          "biz-" + strings.TrimSpace(key.Name),
		Name:        name,
		Description: desc,
		BasePrompt:  key.DefaultPrompt,
		Tools:       append([]string{}, key.SandboxTools...),
		MCPServers:  append([]string{}, key.MCPServers...),
		Skills:      append([]string{}, key.Skills...),
		Packages:    append([]string{}, key.Packages...),
		ProjectDir:  key.ProjectDir,
	}
}
