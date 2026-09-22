package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/flow"
)

// ---------------------------------------------------------------------------
// Flow diagnosis (P3 Agent 闭环, design doc §22.2)
//
// 运行 → 观测 → 建议 → 优化 → 新版本 闭环的「诊断」环节：把一次失败 run 的
// 事件日志 + 定义打包给 LLM，产出根因定位 + 修改建议 + （可选）修改后的
// 完整定义。修改后的定义经 flow.Validate 校验后由调用方决定是否落为新版本
// （走既有 createFlow 链路，不在这里保存）。
// ---------------------------------------------------------------------------

// FlowDiagnosis is the structured output of one failed-run diagnosis.
type FlowDiagnosis struct {
	RunID           string           `json:"run_id"`
	FlowID          string           `json:"flow_id"`
	Version         string           `json:"version"`
	RootCause       string           `json:"root_cause"`
	Summary         string           `json:"summary,omitempty"`
	Suggestions     []string         `json:"suggestions"`
	FixedDefinition *flow.Definition `json:"fixed_definition,omitempty"`
}

// flowDiagnosisSystemPrompt instructs the model to analyze a failed flow run
// and return root cause + suggestions + an optional fixed definition.
const flowDiagnosisSystemPrompt = `You are a workflow diagnostician. Analyze the failed flow run: you receive the flow definition (Flow Spec v1 JSON) and the run's event log. Identify the root cause and propose fixes.

Respond with ONLY a JSON object (no markdown fences, no commentary) with exactly this shape:
{
  "root_cause": "one-paragraph root cause analysis",
  "summary": "one-line summary for the UI",
  "suggestions": ["actionable fix 1", "actionable fix 2"],
  "fixed_definition": { ... optional: the FULL corrected Flow Spec v1 definition ... }
}

Rules:
- fixed_definition, when present, must be a complete valid Flow Spec v1 definition (same shape as the input flow.json: flow_id, version, status, nodes, edges) — never a partial diff. Keep flow_id/version/status unchanged unless a new version is required.
- node kinds: step | llm | decision | human | branch | loop | function
- If the root cause is external (LLM provider down, missing credentials, transient) and the definition is fine, OMIT fixed_definition (only root_cause + suggestions).
- Suggestions must be concrete and actionable (which node/edge to change and how).`

// DiagnoseFlowRun packages a failed run's event log + definition, asks the LLM
// for a diagnosis, and returns the structured result. The returned
// FixedDefinition (when present) is validated but NOT saved — callers persist
// it as a new version through the existing createFlow path.
func (a *Agent) DiagnoseFlowRun(ctx context.Context, flowID, runID string) (*FlowDiagnosis, error) {
	if a == nil || a.flows == nil || a.client == nil {
		return nil, fmt.Errorf("flow diagnosis unavailable (LLM client or store missing)")
	}
	run, err := a.flows.loadRun(flowID, runID)
	if err != nil {
		return nil, fmt.Errorf("diagnose: load run: %w", err)
	}
	rec, err := a.ResolveRunVersion(flowID, run.Version)
	if err != nil {
		return nil, fmt.Errorf("diagnose: resolve version %s: %w", run.Version, err)
	}
	// Inputs: definition + event log (bounded).
	defJSON, _ := json.Marshal(rec.Flow)
	if len(defJSON) > 20000 {
		defJSON = summarizeDefinition(rec.Flow)
	}
	events, err := a.FlowRunEvents(flowID, runID)
	if err != nil {
		return nil, fmt.Errorf("diagnose: events: %w", err)
	}
	if len(events) > 300 {
		events = events[len(events)-300:]
	}
	eventsJSON, _ := json.Marshal(events)

	userText := fmt.Sprintf("Flow definition:\n%s\n\nRun event log:\n%s", defJSON, eventsJSON)
	req := protocol.Request{
		Model:     a.cfg.Model,
		System:    flowDiagnosisSystemPrompt,
		Messages: []protocol.APIMessage{
			{Role: protocol.RoleUser, Content: []protocol.Block{protocol.TextBlock(userText)}},
		},
		MaxTokens: 8192,
	}
	resp, err := a.client.Call(ctx, req)
	if err != nil || resp == nil {
		return nil, fmt.Errorf("diagnose: LLM call failed: %w", err)
	}
	text := strings.TrimSpace(protocol.MessageText(protocol.MessageFromResponse(*resp)))
	diag, err := parseFlowDiagnosisFromLLM(text)
	if err != nil {
		return nil, err
	}
	diag.RunID = runID
	diag.FlowID = flowID
	diag.Version = run.Version
	if diag.FixedDefinition != nil {
		if err := flow.Validate(diag.FixedDefinition); err != nil {
			return nil, fmt.Errorf("diagnose: LLM produced invalid fixed definition: %w", err)
		}
	}
	return diag, nil
}

// parseFlowDiagnosisFromLLM extracts the diagnosis JSON object, tolerating a
// markdown code fence and surrounding prose.
func parseFlowDiagnosisFromLLM(text string) (*FlowDiagnosis, error) {
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
		return nil, fmt.Errorf("diagnose: no JSON object in LLM response")
	}
	var raw struct {
		RootCause   string            `json:"root_cause"`
		Summary     string            `json:"summary"`
		Suggestions []string          `json:"suggestions"`
		FixedDef    json.RawMessage   `json:"fixed_definition"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &raw); err != nil {
		return nil, fmt.Errorf("diagnose: parse JSON: %w", err)
	}
	out := &FlowDiagnosis{
		RootCause:   strings.TrimSpace(raw.RootCause),
		Summary:     strings.TrimSpace(raw.Summary),
		Suggestions: raw.Suggestions,
	}
	if out.Suggestions == nil {
		out.Suggestions = []string{}
	}
	if len(raw.FixedDef) > 0 && string(raw.FixedDef) != "null" {
		var def flow.Definition
		if err := json.Unmarshal(raw.FixedDef, &def); err != nil {
			return nil, fmt.Errorf("diagnose: parse fixed definition: %w", err)
		}
		out.FixedDefinition = &def
	}
	return out, nil
}

// summarizeDefinition produces a compact node/edge digest when the full
// definition exceeds the LLM context budget (ids/kinds/titles + edge shape).
func summarizeDefinition(def *flow.Definition) []byte {
	if def == nil {
		return []byte("{}")
	}
	lines := []string{fmt.Sprintf(`{"flow_id":%q,"version":%q,"status":%q,"nodes":[`, def.FlowID, def.Version, def.Status)}
	for i, n := range def.Nodes {
		if i > 0 {
			lines = append(lines, ",")
		}
		prompt := n.Prompt
		if len(prompt) > 300 {
			prompt = prompt[:300] + "…"
		}
		lines = append(lines, fmt.Sprintf(`{"id":%q,"kind":%q,"title":%q,"prompt":%q}`, n.ID, n.Kind, n.Title, prompt))
	}
	lines = append(lines, `],"edges":[`)
	for i, e := range def.Edges {
		if i > 0 {
			lines = append(lines, ",")
		}
		lines = append(lines, fmt.Sprintf(`{"from":%q,"to":%q,"edge_type":%q}`, e.From, e.To, e.EdgeType))
	}
	lines = append(lines, `]}`)
	return []byte(strings.Join(lines, ""))
}
