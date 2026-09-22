package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/flow"
)

// flowSpecPlanSystemPrompt instructs the model to convert a natural-language
// business description into a Flow Spec v1 definition (P2.5 natural-language
// flow creation). The response must be a single JSON object, no fences.
const flowSpecPlanSystemPrompt = `You are a workflow designer. Convert the user's business process description into a Flow Spec v1 JSON definition.

Respond with ONLY a JSON object (no markdown fences, no commentary) with exactly this shape:
{
  "flow_id": "fl_<lowercase_slug>",
  "name": "short human name",
  "description": "one-line summary",
  "version": "1",
  "status": "draft",
  "inputs": [{"name": "order_id", "type": "string", "desc": "..."}],
  "nodes": [
    {"id": "classify", "kind": "step", "title": "...", "prompt": "..."},
    {"id": "decide", "kind": "decision", "title": "...", "prompt": "...", "decision": {"decision_type": "choice", "choices": [{"id": "auto"}, {"id": "manual"}]}},
    {"id": "br", "kind": "branch", "branch": {"cases": [{"name": "auto", "to": "auto_node", "condition": {"choice": "auto"}}], "default_to": "manual_node"}},
    {"id": "auto_node", "kind": "step", "title": "...", "prompt": "..."},
    {"id": "manual_node", "kind": "human", "title": "...", "prompt": "...", "human": {"queue": "ops", "assignee_policy": "any", "result_var": "approved"}}
  ],
  "edges": [
    {"id": "e1", "from": "classify", "to": "decide", "edge_type": "data_dependency"},
    {"id": "e2", "from": "decide", "to": "br", "edge_type": "data_dependency"}
  ]
}

Rules:
- 2-8 nodes; ids are short slugs (letters/digits/underscore, no spaces)
- node kinds: step | llm | decision | human | branch | loop
- a decision node must be followed (via a data_dependency edge) by a branch node whose cases route by the decision's choice ids; branch default_to is mandatory
- human nodes need human.queue ("ops"/"support"/"finance") and human.result_var
- edges: "data_dependency" for plain ordering; "handoff" to pass the upstream summary; "condition" edges are only for loop/branch internals
- prompts may reference declared inputs ONLY as {{inputs.<name>}}; never reference undeclared names
- inputs: only fields the flow actually reads`

// flowSpecAmendSystemPrompt instructs the model to MODIFY an existing Flow
// Spec v1 definition according to a natural-language change request (multi-
// turn incremental editing). The response must be the FULL modified
// definition, never a diff or patch.
const flowSpecAmendSystemPrompt = `You are a workflow designer. The user wants to MODIFY an existing Flow Spec v1 JSON definition. You receive the CURRENT definition and a CHANGE REQUEST.

Respond with ONLY a JSON object (no markdown fences, no commentary): the FULL corrected Flow Spec v1 JSON definition after applying the change — never a partial diff or patch.

Rules:
- Keep flow_id/version/status unchanged unless the change explicitly requires otherwise.
- Keep existing node ids stable unless the change renames/removes them; when removing a node, also remove its edges and fix all references.
- node kinds: step | llm | decision | human | branch | loop | function
- a decision node must be followed (via a data_dependency edge) by a branch node whose cases route by the decision's choice ids; branch default_to is mandatory
- human nodes need human.queue ("ops"/"support"/"finance") and human.result_var
- edges: "data_dependency" for plain ordering; "handoff" to pass the upstream summary; "condition" edges are only for loop/branch internals
- prompts may reference declared inputs ONLY as {{inputs.<name>}}; never reference undeclared names
- inputs: only fields the flow actually reads`

// GenerateFlowSpec drafts a Flow Spec v1 definition from a natural-language
// business description via the LLM (P2.5). It validates the result so a
// malformed draft fails fast instead of being saved.
func (a *Agent) GenerateFlowSpec(ctx context.Context, description string) (*flow.Definition, error) {
	description = strings.TrimSpace(description)
	if description == "" {
		return nil, fmt.Errorf("generate flow: missing description")
	}
	if a.client == nil {
		return nil, fmt.Errorf("generate flow: LLM client unavailable")
	}
	req := protocol.Request{
		Model:     a.cfg.Model,
		System:    flowSpecPlanSystemPrompt,
		Messages: []protocol.APIMessage{
			{Role: protocol.RoleUser, Content: []protocol.Block{protocol.TextBlock(description)}},
		},
		MaxTokens: 4096,
	}
	resp, err := a.client.Call(ctx, req)
	if err != nil || resp == nil {
		return nil, fmt.Errorf("generate flow: LLM call failed: %w", err)
	}
	text := strings.TrimSpace(protocol.MessageText(protocol.MessageFromResponse(*resp)))
	def, err := parseFlowSpecFromLLM(text)
	if err != nil {
		return nil, err
	}
	if len(def.Nodes) == 0 {
		return nil, fmt.Errorf("generate flow: LLM returned no nodes")
	}
	if def.FlowID == "" {
		return nil, fmt.Errorf("generate flow: LLM returned no flow_id")
	}
	if err := flow.Validate(def); err != nil {
		return nil, fmt.Errorf("generate flow: LLM produced invalid definition: %w", err)
	}
	return def, nil
}

// AmendFlowSpec modifies an existing Flow Spec v1 definition according to a
// natural-language change request (multi-turn incremental editing, P3 余项).
// The current definition + the change are sent to the LLM; the FULL modified
// definition is returned, validated but NOT saved (caller persists it as a
// new version).
func (a *Agent) AmendFlowSpec(ctx context.Context, current *flow.Definition, change string) (*flow.Definition, error) {
	change = strings.TrimSpace(change)
	if change == "" {
		return nil, fmt.Errorf("amend flow: missing change request")
	}
	if current == nil {
		return nil, fmt.Errorf("amend flow: missing current definition")
	}
	if a.client == nil {
		return nil, fmt.Errorf("amend flow: LLM client unavailable")
	}
	currentJSON, err := json.Marshal(current)
	if err != nil {
		return nil, fmt.Errorf("amend flow: marshal current definition: %w", err)
	}
	userText := fmt.Sprintf("CURRENT definition:\n%s\n\nCHANGE REQUEST:\n%s", currentJSON, change)
	req := protocol.Request{
		Model:     a.cfg.Model,
		System:    flowSpecAmendSystemPrompt,
		Messages: []protocol.APIMessage{
			{Role: protocol.RoleUser, Content: []protocol.Block{protocol.TextBlock(userText)}},
		},
		MaxTokens: 4096,
	}
	resp, err := a.client.Call(ctx, req)
	if err != nil || resp == nil {
		return nil, fmt.Errorf("amend flow: LLM call failed: %w", err)
	}
	text := strings.TrimSpace(protocol.MessageText(protocol.MessageFromResponse(*resp)))
	def, err := parseFlowSpecFromLLM(text)
	if err != nil {
		return nil, err
	}
	if len(def.Nodes) == 0 {
		return nil, fmt.Errorf("amend flow: LLM returned no nodes")
	}
	if err := flow.Validate(def); err != nil {
		return nil, fmt.Errorf("amend flow: LLM produced invalid definition: %w", err)
	}
	return def, nil
}

// parseFlowSpecFromLLM extracts a Flow Spec JSON object from an LLM response,
// tolerating a markdown code fence and surrounding prose.
func parseFlowSpecFromLLM(text string) (*flow.Definition, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) > 1 {
			lines = lines[1:]
		}
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "```" {
			lines = lines[:len(lines)-1]
		}
		text = strings.TrimSpace(strings.Join(lines, "\n"))
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("generate flow: no JSON object in LLM response")
	}
	var def flow.Definition
	if err := json.Unmarshal([]byte(text[start:end+1]), &def); err != nil {
		return nil, fmt.Errorf("generate flow: parse definition JSON: %w", err)
	}
	return &def, nil
}
