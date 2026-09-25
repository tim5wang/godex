package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaptinlin/jsonrepair"
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
- inputs: only fields the flow actually reads; mark required fields with required=true
- when declaring Flow outputs, map each functional output with source="nodes.<node_id>.outputs.<field>"
- the source field must also be declared in that node's outputs; mark an output required only when every valid path produces it`

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
- inputs: only fields the flow actually reads; mark required fields with required=true
- when declaring Flow outputs, map each functional output with source="nodes.<node_id>.outputs.<field>"
- the source field must also be declared in that node's outputs; mark an output required only when every valid path produces it`

// GenerateFlowSpec drafts a Flow Spec v1 definition from a natural-language
// business description via the LLM (P2.5). It validates the result so a
// malformed draft fails fast instead of being saved. Transient LLM failures
// (empty/garbage response, unparsable JSON) retry once before surfacing.
func (a *Agent) GenerateFlowSpec(ctx context.Context, description string) (*flow.Definition, error) {
	description = strings.TrimSpace(description)
	if description == "" {
		return nil, fmt.Errorf("generate flow: missing description")
	}
	if a.client == nil {
		return nil, fmt.Errorf("generate flow: LLM client unavailable")
	}
	req := protocol.Request{
		Model:  a.cfg.Model,
		System: flowSpecPlanSystemPrompt,
		Messages: []protocol.APIMessage{
			{Role: protocol.RoleUser, Content: []protocol.Block{protocol.TextBlock(description)}},
		},
		MaxTokens: 4096,
	}
	def, err := callFlowSpecLLM(ctx, a, "generate flow", req)
	if err != nil {
		// Validation failures surface as *FlowSpecDraftError carrying the
		// near-correct draft + raw output so the caller can amend instead of
		// restarting; parse/transport failures stay bare errors.
		return nil, err
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
		Model:  a.cfg.Model,
		System: flowSpecAmendSystemPrompt,
		Messages: []protocol.APIMessage{
			{Role: protocol.RoleUser, Content: []protocol.Block{protocol.TextBlock(userText)}},
		},
		MaxTokens: 4096,
	}
	def, err := callFlowSpecLLM(ctx, a, "amend flow", req)
	if err != nil {
		return nil, err
	}
	return def, nil
}

// callFlowSpecLLM runs one LLM call for a Flow Spec draft/amendment and
// parses the response. On a transient failure (transport error, empty text,
// or unparsable JSON) it retries once — models intermittently return empty
// or garbled output, and a single retry usually recovers.
//
// When the response parses but FAILS flow validation, the returned error is a
// *FlowSpecDraftError carrying the near-correct draft + the raw LLM output, so
// callers (flow_design) can hand it back for amend instead of losing it.
func callFlowSpecLLM(ctx context.Context, a *Agent, label string, req protocol.Request) (*flow.Definition, error) {
	attempts := 2
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		resp, err := a.client.Call(ctx, req)
		if err != nil || resp == nil {
			lastErr = fmt.Errorf("%s: LLM call failed: %w", label, err)
			if attempt < attempts {
				continue
			}
			break
		}
		text := strings.TrimSpace(protocol.MessageText(protocol.MessageFromResponse(*resp)))
		def, perr := parseFlowSpecFromLLM(text)
		if perr == nil {
			// Parsed: run validation here so an invalid-but-parseable draft
			// surfaces as a FlowSpecDraftError (draft + raw output preserved)
			// instead of a bare error.
			if vErr := flow.Validate(def); vErr != nil {
				return nil, &FlowSpecDraftError{
					Label:   label,
					Draft:   def,
					Raw:     text,
					Cause:   vErr,
					Attempt: attempt,
				}
			}
			return def, nil
		}
		lastErr = perr
		if attempt == attempts {
			// Parse failure on the last attempt: wrap with the raw LLM output
			// so callers (flow_design) can surface it for diagnostics instead
			// of a bare "no JSON object" with nothing to inspect.
			return nil, &FlowSpecDraftError{
				Label:   label,
				Draft:   nil,
				Raw:     text,
				Cause:   perr,
				Attempt: attempt,
			}
		}
	}
	return nil, lastErr
}

// FlowSpecDraftError wraps a generated-but-invalid Flow Spec draft so the
// caller can continue the generate → validate → amend loop instead of
// restarting from scratch. It carries the near-correct definition and the raw
// LLM output for diagnostics.
type FlowSpecDraftError struct {
	Label string
	// Draft is the parsed-but-invalid definition (may be non-nil only when
	// the LLM response parsed; otherwise nil).
	Draft *flow.Definition
	// Raw is the raw LLM response text (for diagnostics when parsing failed).
	Raw string
	// Cause is the underlying error (validation failure or parse failure).
	Cause error
	// Attempt is the 1-based attempt that produced this result.
	Attempt int
}

func (e *FlowSpecDraftError) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("%s: LLM produced unusable response (attempt %d)", e.Label, e.Attempt)
	}
	if e.Draft != nil {
		return fmt.Sprintf("%s: LLM produced invalid definition: %v", e.Label, e.Cause)
	}
	// Parse failure: keep the message compact but point at the preserved raw
	// output (callers surface e.Raw for diagnostics).
	if e.Raw != "" {
		return fmt.Sprintf("%s: %v (raw LLM output preserved, %d chars)", e.Label, e.Cause, len(e.Raw))
	}
	return fmt.Sprintf("%s: %v", e.Label, e.Cause)
}

// Unwrap lets errors.Is/As reach the underlying validation error.
func (e *FlowSpecDraftError) Unwrap() error { return e.Cause }

// parseFlowSpecFromLLM extracts a Flow Spec JSON object from an LLM response,
// tolerating a markdown code fence and surrounding prose. It tries strict
// JSON first, then jsonrepair (missing commas/colons, unquoted keys, single
// quotes, Python literals, truncated input) so a slightly-malformed LLM
// draft still parses instead of surfacing “no JSON object”.
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
		// Level 0: no JSON object at all — try full-tolerant repair of the
		// whole response before giving up (a model may wrap the object in
		// prose without braces surviving extraction).
		if repaired, err := jsonrepair.Repair(text); err == nil {
			var def flow.Definition
			if jerr := json.Unmarshal([]byte(repaired), &def); jerr == nil && len(def.Nodes) > 0 {
				return &def, nil
			}
		}
		return nil, fmt.Errorf("generate flow: no JSON object in LLM response")
	}
	body := text[start : end+1]
	var def flow.Definition
	if err := json.Unmarshal([]byte(body), &def); err != nil {
		// Level 1: jsonrepair the extracted object (handles truncation,
		// unquoted keys, single quotes, missing commas — the common failure
		// modes of LLM-generated JSON).
		if repaired, rerr := jsonrepair.Repair(body); rerr == nil {
			if uerr := json.Unmarshal([]byte(repaired), &def); uerr == nil {
				return &def, nil
			}
		}
		return nil, fmt.Errorf("generate flow: parse definition JSON: %w", err)
	}
	return &def, nil
}
