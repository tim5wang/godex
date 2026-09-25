package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/core/flow"
	"github.com/tim5wang/godex/internal/tools"
)

// ---------------------------------------------------------------------------
// flow_design — the Agent method set for designing Business Flows (P2.5+).
//
// A chat Agent (or the natural-language tab's backend) uses this toolset to
// understand / design / debug Flows iteratively instead of a bare one-shot
// LLM JSON call:
//   - action=generate  natural-language description → Flow Spec draft
//   - action=amend     natural-language change on an existing definition
//   - action=validate  dry-run Validate on a definition (fast feedback loop)
//
// It returns the draft definition (Structured) + a compact text summary, so
// the Agent can iterate: generate → validate → amend until the flow is
// coherent, then hand it to createFlow for persistence.
// ---------------------------------------------------------------------------

// flowDesignArgs is the typed args of the flow_design agent tool.
type flowDesignArgs struct {
	Action      string          `json:"action,omitempty"`
	FlowID      string          `json:"flow_id,omitempty"`
	Description string          `json:"description,omitempty"`
	Change      string          `json:"change,omitempty"`
	Definition  json.RawMessage `json:"definition,omitempty"`
}

// newFlowDesignTool registers the flow_design agent tool: an agent session
// can generate a Flow Spec draft from a natural-language description, amend
// an existing definition iteratively, or validate a definition — the "method
// set" an Agent needs to act as a Flow designer (the natural-language tab
// also routes through the same Agent methods).
func newFlowDesignTool(agent *Agent) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("flow_design", "Design a Business Flow Spec v1 definition as a chat agent. action='generate' drafts a new flow from a natural-language description; action='read' (flow_id) returns the current flow's draft definition; action='amend' applies a natural-language change to an existing definition (multi-turn iterative editing); action='validate' dry-runs flow.Validate on a definition and returns errors. Returns the draft definition (structured) plus a compact text summary. Iterate: generate → validate → amend until coherent, then persist via createFlow.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":     "string",
				"enum":     []string{"generate", "read", "amend", "validate"},
				"required": true,
			},
			"flow_id":     map[string]string{"type": "string", "description": "flow to read (action=read)"},
			"description": map[string]string{"type": "string"},
			"change":      map[string]string{"type": "string"},
			"definition":  map[string]interface{}{"type": "object", "description": "current Flow Spec definition (action=amend/validate)"},
		},
	}, nil), func(ctx context.Context, args flowDesignArgs) (tools.ToolResult, error) {
		action := strings.ToLower(strings.TrimSpace(args.Action))
		switch action {
		case "read":
			flowID := strings.TrimSpace(args.FlowID)
			if flowID == "" {
				return tools.ToolResult{}, fmt.Errorf("flow_design read: missing flow_id")
			}
			view, err := agent.GetFlowDraft(flowID)
			if err != nil {
				return tools.ToolResult{}, fmt.Errorf("flow_design read: %w", err)
			}
			if view.Definition == nil || len(view.Definition.Nodes) == 0 {
				return tools.ToolResult{Text: fmt.Sprintf("flow %s 还没有草稿定义（可先用 generate 生成）", flowID)}, nil
			}
			return flowDesignResult(view.Definition, "read"), nil

		case "generate":
			if strings.TrimSpace(args.Description) == "" {
				return tools.ToolResult{}, fmt.Errorf("flow_design generate: missing description")
			}
			def, err := agent.GenerateFlowSpec(ctx, args.Description)
			if err != nil {
				// A generated-but-invalid draft comes back as a draft error:
				// hand the near-correct draft + errors to the Agent so it can
				// amend instead of restarting.
				if draftErr, ok := err.(*FlowSpecDraftError); ok {
					return flowDesignDraftErrorResult(draftErr), nil
				}
				return tools.ToolResult{}, err
			}
			return flowDesignResult(def, "generated"), nil

		case "amend":
			if strings.TrimSpace(args.Change) == "" {
				return tools.ToolResult{}, fmt.Errorf("flow_design amend: missing change")
			}
			current, err := parseFlowDefinitionJSON(args.Definition)
			if err != nil {
				return tools.ToolResult{}, fmt.Errorf("flow_design amend: invalid current definition: %w", err)
			}
			def, err := agent.AmendFlowSpec(ctx, current, args.Change)
			if err != nil {
				if draftErr, ok := err.(*FlowSpecDraftError); ok {
					return flowDesignDraftErrorResult(draftErr), nil
				}
				return tools.ToolResult{}, err
			}
			return flowDesignResult(def, "amended"), nil

		case "validate":
			def, err := parseFlowDefinitionJSON(args.Definition)
			if err != nil {
				return tools.ToolResult{}, fmt.Errorf("flow_design validate: invalid definition JSON: %w", err)
			}
			if err := flow.Validate(def); err != nil {
				return flowInvalidResult(def, err), nil
			}
			return tools.ToolResult{Structured: map[string]interface{}{
				"valid": true,
			}, Text: fmt.Sprintf("flow definition valid (nodes=%d, edges=%d)", len(def.Nodes), len(def.Edges))}, nil

		default:
			return tools.ToolResult{}, fmt.Errorf("unsupported flow_design action %q", action)
		}
	})
}

// parseFlowDefinitionJSON decodes a Flow Spec definition from tool args.
func parseFlowDefinitionJSON(raw json.RawMessage) (*flow.Definition, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("missing definition")
	}
	var def flow.Definition
	if err := json.Unmarshal(raw, &def); err != nil {
		return nil, err
	}
	if def.FlowID == "" && len(def.Nodes) == 0 {
		return nil, fmt.Errorf("definition has no flow_id/nodes")
	}
	return &def, nil
}

// flowInvalidResult converts a flow.Validate failure into a ToolResult with
// ACTIONABLE context: the concrete error plus a compact inventory of what IS
// declared (node kinds + edge targets) so the Agent can spot the mismatch
// (e.g. a branch case pointing at a node id that doesn't exist) instead of
// guessing from the error line alone.
func flowInvalidResult(def *flow.Definition, vErr error) tools.ToolResult {
	var nodeKinds []string
	ids := map[string]string{} // id -> kind
	for _, n := range def.Nodes {
		k := n.Kind
		if k == "" {
			k = "step"
		}
		nodeKinds = append(nodeKinds, fmt.Sprintf("%s(%s)", n.ID, k))
		ids[n.ID] = k
	}
	var edges []string
	for _, e := range def.Edges {
		et := e.EdgeType
		if et == "" {
			et = "data_dependency"
		}
		eid := e.ID
		if eid == "" {
			eid = "(no-id)"
		}
		edges = append(edges, fmt.Sprintf("%s:%s→%s:%s", eid, e.From, e.To, et))
	}
	hint := fmt.Sprintf(
		"flow definition invalid: %v\n已声明节点：%s\n已声明边：%s\n提示：branch case 的 to / edge 的 from,to 必须指向上面列出的节点 id；如需补全分支目标节点请用 flow_design amend。",
		vErr,
		func() string {
			if len(nodeKinds) == 0 {
				return "（无）"
			}
			return strings.Join(nodeKinds, ", ")
		}(),
		func() string {
			if len(edges) == 0 {
				return "（无）"
			}
			return strings.Join(edges, ", ")
		}(),
	)
	return tools.ToolResult{
		Structured: map[string]interface{}{
			"valid":       false,
			"error":       vErr.Error(),
			"nodes":       nodeKinds,
			"edges":       edges,
			"declared_ids": ids,
		},
		Text: hint,
	}
}

// flowDesignResult wraps a draft definition into a ToolResult: the structured
// payload carries the full definition for downstream tools, and the text
// summary keeps the chat response compact.
func flowDesignResult(def *flow.Definition, verb string) tools.ToolResult {
	nodes := len(def.Nodes)
	edges := len(def.Edges)
	kinds := map[string]int{}
	for _, n := range def.Nodes {
		k := n.Kind
		if k == "" {
			k = "step"
		}
		kinds[k]++
	}
	var parts []string
	for _, k := range []string{"step", "llm", "decision", "human", "branch", "loop", "function"} {
		if kinds[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s×%d", k, kinds[k]))
		}
	}
	summary := strings.Join(parts, "、")
	return tools.ToolResult{
		Structured: def,
		Text: fmt.Sprintf("%s Flow Spec draft %q: %d nodes / %d edges%s",
			verb, def.FlowID, nodes, edges, func() string {
				if summary != "" {
					return "（" + summary + "）"
				}
				return ""
			}()),
	}
}

// flowDesignDraftErrorResult converts a *FlowSpecDraftError into a ToolResult
// the Agent can CONTINUE from. IMPORTANT: the tool-result wire format only
// passes result.Text to the model (Structured is dropped by OutputString), so
// the near-correct draft / raw output must be embedded in Text for amend to
// actually receive it.
//   - validation failure (Draft != nil): full draft JSON is embedded so amend
//     can fix the references — the generate → validate → amend loop stays
//     alive instead of forcing a full restart;
//   - parse failure (Draft == nil): the raw LLM output is embedded so the
//     Agent can judge whether a targeted re-generate is worth it.
func flowDesignDraftErrorResult(draftErr *FlowSpecDraftError) tools.ToolResult {
	structured := map[string]interface{}{
		"ok":         false,
		"stage":      "draft",
		"attempt":    draftErr.Attempt,
		"raw_output": draftErr.Raw,
	}
	if draftErr.Cause != nil {
		structured["error"] = draftErr.Cause.Error()
	}
	var errorsList []string
	if draftErr.Cause != nil {
		errorsList = append(errorsList, draftErr.Cause.Error())
	}
	if draftErr.Draft != nil {
		structured["draft"] = draftErr.Draft
	}

	var text string
	switch {
	case draftErr.Draft != nil:
		// Embed the full draft JSON so the model can pass it back verbatim
		// to action=amend without re-deriving it.
		if raw, err := json.Marshal(draftErr.Draft); err == nil {
			text = fmt.Sprintf("%s：草稿已生成但未通过校验（errors：%s）。\n请用 flow_design action=amend 修正，draft 如下（直接使用，无需重新生成）：\n%s",
				draftErr.Label, strings.Join(errorsList, "；"), string(raw))
		} else {
			text = fmt.Sprintf("%s：草稿已生成但未通过校验（errors：%s），且 draft 序列化失败：%v",
				draftErr.Label, strings.Join(errorsList, "；"), err)
		}
	case draftErr.Raw != "":
		text = fmt.Sprintf("%s：LLM 响应未解析出 JSON（无可用草稿），errors：%s。\nraw_output 保留如下供诊断（可用它判断是重试还是手工构造定义走 validate）：\n%s",
			draftErr.Label, strings.Join(errorsList, "；"), draftErr.Raw)
	default:
		text = fmt.Sprintf("%s：LLM 响应未解析出 JSON（无可用草稿），errors：%s。\n请重新 generate，或依据 Flow Spec v1 schema（godex_docs get flow-spec）手工构造定义后走 validate。",
			draftErr.Label, strings.Join(errorsList, "；"))
	}
	return tools.ToolResult{
		Structured: structured,
		Text:       text,
	}
}
