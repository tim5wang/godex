package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/tools"
)

// ---------------------------------------------------------------------------
// create_flow — persist a Flow Spec draft as a new version.
//
// flow_design generates/validates/amends definitions in the conversation but
// does not save them. This tool closes the loop: an Agent session (e.g. the
// business-flow designer chat) stores the approved definition as a new draft
// version of the flow, which the canvas then picks up.
// ---------------------------------------------------------------------------

// createFlowArgs is the typed args of the create_flow agent tool.
type createFlowArgs struct {
	FlowID     string          `json:"flow_id,omitempty"`
	Version    string          `json:"version,omitempty"`
	Status     string          `json:"status,omitempty"`
	Definition json.RawMessage `json:"definition,omitempty"`
}

// newCreateFlowTool registers the create_flow agent tool: persist a validated
// Flow Spec definition as a new draft version of the named flow.
func newCreateFlowTool(agent *Agent) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("create_flow", "Persist a Flow Spec v1 definition as a NEW draft version of an existing flow (validated + compiled at save). The flow_id and version are pinned by the calling page — keep them as given. Returns the saved version view (version / nodes / edges / digest). Use after flow_design generate/amend when the user approves the draft.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"flow_id":    map[string]string{"type": "string"},
			"version":    map[string]string{"type": "string"},
			"status":     map[string]string{"type": "string", "description": "default draft"},
			"definition": map[string]interface{}{"type": "object", "description": "the Flow Spec definition to persist"},
		},
	}, nil), func(ctx context.Context, args createFlowArgs) (tools.ToolResult, error) {
		sessionID := flowSessionID(ctx)
		flowID := strings.TrimSpace(args.FlowID)
		if flowID == "" {
			return tools.ToolResult{}, fmt.Errorf("create_flow: missing flow_id")
		}
		def, err := parseFlowDefinitionJSON(args.Definition)
		if err != nil {
			return tools.ToolResult{}, fmt.Errorf("create_flow: invalid definition: %w", err)
		}
		status := strings.TrimSpace(args.Status)
		if status == "" {
			status = "draft"
		}
		view, err := agent.CreateFlow(FlowCreateArgs{
			FlowID:    flowID,
			Version:   strings.TrimSpace(args.Version),
			Status:    status,
			Def:       def,
			SessionID: sessionID,
		})
		if err != nil {
			return tools.ToolResult{}, fmt.Errorf("create_flow: %w", err)
		}
		return tools.ToolResult{
			Structured: view,
			Text:       fmt.Sprintf("已保存 %s v%s 草稿（nodes=%d, edges=%d）", view.FlowID, view.Version, view.Nodes, view.Edges),
		}, nil
	})
}
