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
	return tools.NewTypedTool(tools.NewToolSpec("flow_design", "Design a Business Flow Spec v1 definition as a chat agent. action='generate' drafts a new flow from a natural-language description; action='amend' applies a natural-language change to an existing definition (multi-turn iterative editing); action='validate' dry-runs flow.Validate on a definition and returns errors. Returns the draft definition (structured) plus a compact text summary. Iterate: generate → validate → amend until coherent, then persist via createFlow.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":     "string",
				"enum":     []string{"generate", "amend", "validate"},
				"required": true,
			},
			"description": map[string]string{"type": "string"},
			"change":      map[string]string{"type": "string"},
			"definition":  map[string]interface{}{"type": "object", "description": "current Flow Spec definition (action=amend/validate)"},
		},
	}, nil), func(ctx context.Context, args flowDesignArgs) (tools.ToolResult, error) {
		action := strings.ToLower(strings.TrimSpace(args.Action))
		switch action {
		case "generate":
			if strings.TrimSpace(args.Description) == "" {
				return tools.ToolResult{}, fmt.Errorf("flow_design generate: missing description")
			}
			def, err := agent.GenerateFlowSpec(ctx, args.Description)
			if err != nil {
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
				return tools.ToolResult{}, err
			}
			return flowDesignResult(def, "amended"), nil

		case "validate":
			def, err := parseFlowDefinitionJSON(args.Definition)
			if err != nil {
				return tools.ToolResult{}, fmt.Errorf("flow_design validate: invalid definition JSON: %w", err)
			}
			if err := flow.Validate(def); err != nil {
				return tools.ToolResult{Structured: map[string]interface{}{
					"valid": false,
					"error": err.Error(),
				}, Text: fmt.Sprintf("flow definition invalid: %v", err)}, nil
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
