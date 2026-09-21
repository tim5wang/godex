// P1.4 agent_ref capability inheritance: a flow step node may reference an
// agent template (or business key id) via agent_ref; at run time the
// referenced template's capability baseline (bundles/tools/write_scope/mcp/
// skills/packages) is resolved and injected into the node's durable subagent
// start request. The reference is resolved lazily at node start so template
// updates apply to new runs; unresolved refs fail the node (fast, like the
// step-template error path).
package agent

import (
	"fmt"
	"strings"
)

// agentRefCapabilities is the resolved capability override for one node.
type agentRefCapabilities struct {
	Bundles    []string
	Tools      []string
	WriteScope []string
	MCPServers []string
	Skills     []string
	Packages   []string
	ModelHint  string
	BudgetHint string
}

// resolveAgentRef resolves the agent_ref on a workflow node into capability
// overrides. It returns zero-value capabilities when the node has no
// agent_ref or the manager is unavailable. A missing template is an error so
// a flow that pins a deleted template fails fast instead of silently running
// with the default surface.
func (a *Agent) resolveAgentRef(node *workflowNode) (agentRefCapabilities, error) {
	var out agentRefCapabilities
	if node == nil || strings.TrimSpace(node.AgentRef) == "" {
		return out, nil
	}
	if a == nil || a.templateMgr == nil {
		return out, nil
	}
	ref := strings.TrimSpace(node.AgentRef)
	tpl, _, err := a.templateMgr.Resolve(ref)
	if err != nil {
		return out, fmt.Errorf("step agent_ref %q: %w", ref, err)
	}
	out.Bundles = append([]string(nil), tpl.Bundles...)
	out.Tools = append([]string(nil), tpl.Tools...)
	out.WriteScope = append([]string(nil), tpl.WriteScope...)
	out.MCPServers = append([]string(nil), tpl.MCPServers...)
	out.Skills = append([]string(nil), tpl.Skills...)
	out.Packages = append([]string(nil), tpl.Packages...)
	out.ModelHint = strings.TrimSpace(tpl.ModelHint)
	out.BudgetHint = strings.TrimSpace(tpl.BudgetHint)
	return out, nil
}

// injectAgentRefIntoRequest merges resolved agent_ref capabilities into a
// durable subagent start request (the node's explicit AgentType/WriteScope
// still take precedence; template bundles/tools are appended).
func injectAgentRefIntoRequest(req *durableSubagentStartRequest, caps agentRefCapabilities) {
	if req == nil {
		return
	}
	req.RequiredBundles = append(req.RequiredBundles, caps.Bundles...)
	req.RequiredTools = append(req.RequiredTools, caps.Tools...)
	if len(req.WriteScope) == 0 {
		req.WriteScope = append(req.WriteScope, caps.WriteScope...)
	}
	// mcp/skills/packages/model/budget hints are carried on the node via
	// extra fields on the request (see resolveSubagentRole for role hints);
	// bundles/tools/write_scope are the capability gate here.
}
