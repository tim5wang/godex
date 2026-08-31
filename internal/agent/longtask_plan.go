package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/tools"
)

// longTaskPlanSystemPrompt instructs the model to decompose a natural-language
// task description into prioritized, dependency-aware stories (roadmap 6.5).
const longTaskPlanSystemPrompt = `You are a task decomposition planner. Break the user's task description into a prioritized list of stories for a durable multi-agent workflow. Each story becomes an independent subagent node in a DAG.

Respond with ONLY a JSON array (no markdown fences, no commentary). Each element has exactly this shape:
{"id": "US-001", "title": "...", "description": "...", "acceptance_criteria": ["..."], "priority": 1, "agent_type": "general-purpose", "write_scope": [], "depends_on": []}

Rules:
- 2-8 stories, each independently verifiable
- "depends_on" references earlier story IDs; stories with no depends_on run in parallel
- priority 1 = highest
- agent_type: general-purpose, coder, researcher, or reviewer
- write_scope: workspace-relative paths, empty means unrestricted`

// planLongTask is the roadmap 6.5 entry point: a natural-language task
// description is decomposed into stories by the LLM, then compiled into a
// longtask workflow through the existing create path.
func (a *Agent) planLongTask(ctx context.Context, args longTaskArgs) (longTaskView, error) {
	description := strings.TrimSpace(args.Description)
	if description == "" {
		return longTaskView{}, fmt.Errorf("plan: missing task description")
	}
	if a.client == nil {
		return longTaskView{}, fmt.Errorf("plan: LLM client unavailable")
	}
	req := protocol.Request{
		System: longTaskPlanSystemPrompt,
		Messages: []protocol.APIMessage{
			{Role: protocol.RoleUser, Content: []protocol.Block{protocol.TextBlock(description)}},
		},
		MaxTokens: 2048,
	}
	resp, err := a.client.Call(ctx, req)
	if err != nil || resp == nil {
		return longTaskView{}, fmt.Errorf("plan: LLM call failed: %w", err)
	}
	text := strings.TrimSpace(protocol.MessageText(protocol.MessageFromResponse(*resp)))
	stories, err := parseLongTaskPlanStories(text)
	if err != nil {
		return longTaskView{}, err
	}
	if len(stories) == 0 {
		return longTaskView{}, fmt.Errorf("plan: LLM returned no stories")
	}
	args.Stories = stories
	return a.createLongTask(tools.SessionContextFromContext(ctx).SessionID, args)
}

// parseLongTaskPlanStories extracts a stories JSON array from an LLM response,
// tolerating a markdown code fence and surrounding prose.
func parseLongTaskPlanStories(text string) ([]longTaskStoryInput, error) {
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
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("plan: no JSON array in LLM response")
	}
	var stories []longTaskStoryInput
	if err := json.Unmarshal([]byte(text[start:end+1]), &stories); err != nil {
		return nil, fmt.Errorf("plan: parse stories JSON: %w", err)
	}
	return stories, nil
}
