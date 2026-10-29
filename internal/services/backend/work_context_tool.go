package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/tools"
)

// The tool is bound to an existing persisted session link, not to an arbitrary
// caller-supplied tenant or desktop ID. Knowing an ID grants no extra access.
func (s *Service) newWorkContextTool(sessionID string) tools.Tool {
	type args struct {
		Action     string                 `json:"action"`
		Query      string                 `json:"query"`
		Scope      string                 `json:"scope"`
		IfVersion  int                    `json:"if_version"`
		Checkpoint workcontext.Checkpoint `json:"checkpoint"`
		Fact       workcontext.FactInput  `json:"fact"`
	}
	return tools.NewTypedTool(tools.NewToolSpec("work_context", "Read the explicitly linked work stream, recall related knowledge, save a checkpoint or propose evidence-backed knowledge. Cannot confirm facts, accept tasks, change permissions or query production automatically.", map[string]interface{}{
		"type": "object", "properties": map[string]interface{}{
			"action":     map[string]interface{}{"type": "string", "enum": []string{"get", "brief", "recall", "checkpoint", "propose_fact"}},
			"query":      map[string]string{"type": "string"},
			"scope":      map[string]interface{}{"type": "string", "enum": []string{"current", "related", "all"}},
			"if_version": map[string]string{"type": "integer"},
			"checkpoint": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
				"summary":        map[string]string{"type": "string"},
				"completed":      map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
				"next_actions":   map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
				"blockers":       map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
				"open_questions": map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
				"evidence_refs":  map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
			}},
			"fact": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
				"title": map[string]string{"type": "string"}, "content": map[string]string{"type": "string"}, "kind": map[string]interface{}{"type": "string", "enum": []string{"fact", "decision", "trace"}}, "scope": map[string]string{"type": "string"}, "source": map[string]string{"type": "string"}, "source_excerpt": map[string]string{"type": "string"}, "observed_at": map[string]string{"type": "string"}, "subject": map[string]string{"type": "string"}, "predicate": map[string]string{"type": "string"},
			}},
		}, "required": []string{"action"},
	}, nil), func(ctx context.Context, input args) (tools.ToolResult, error) {
		store, err := s.WorkContexts()
		if err != nil {
			return tools.ToolResult{}, err
		}
		scope, id, err := store.SessionBinding(ctx, sessionID)
		if err != nil {
			return tools.ToolResult{}, err
		}
		if id == "" {
			return tools.ToolResult{}, fmt.Errorf("this session is not linked to a work desktop; link it in Work Desktop first")
		}
		var out any
		switch strings.TrimSpace(input.Action) {
		case "get", "brief":
			out, err = store.Get(ctx, scope, id)
		case "recall":
			rangeScope := input.Scope
			if rangeScope == "" {
				rangeScope = "current"
			}
			out, err = store.Recall(ctx, scope, id, rangeScope, input.Query)
		case "checkpoint":
			out, err = store.SaveCheckpoint(ctx, scope, id, input.IfVersion, input.Checkpoint)
		case "propose_fact":
			input.Fact.Provenance = "derived"
			out, err = store.CreateFact(ctx, scope, id, input.Fact)
		default:
			return tools.ToolResult{}, fmt.Errorf("unknown work_context action")
		}
		if err != nil {
			return tools.ToolResult{}, err
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Text: string(raw), Structured: out}, nil
	})
}
