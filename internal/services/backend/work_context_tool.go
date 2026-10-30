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
		Action       string                 `json:"action"`
		Query        string                 `json:"query"`
		Scope        string                 `json:"scope"`
		IfVersion    int                    `json:"if_version"`
		Checkpoint   workcontext.Checkpoint `json:"checkpoint"`
		Fact         workcontext.FactInput  `json:"fact"`
		SupersedesID string                 `json:"supersedes_id"`
		Todo         workcontext.TodoInput  `json:"todo"`
		TodoID       string                 `json:"todo_id"`
		TodoStatus   string                 `json:"todo_status"`
		TodoOwner    string                 `json:"todo_owner"`
		TodoDueAt    string                 `json:"todo_due_at"`
	}
	return tools.NewTypedTool(tools.NewToolSpec("work_context", "Read the work stream this session is linked to, recall related knowledge, save the checkpoint, plan items (todos) and record evidence-backed facts. focus shows what changed since the stream was last seen plus the plan; plan reads or adds a plan item; todo_patch edits one (status/owner/due); ack marks the stream seen after reporting the focus. list_streams searches your other streams by keyword. Use it proactively while working: update the checkpoint as progress happens, keep plan items current (owner/due), propose facts for conclusions with sources, and supersede an outdated fact by id instead of editing it. It cannot confirm facts, accept tasks, change permissions or query production automatically.", map[string]interface{}{
		"type": "object", "properties": map[string]interface{}{
			"action":     map[string]interface{}{"type": "string", "enum": []string{"list_streams", "get", "brief", "recall", "focus", "ack", "checkpoint", "plan", "todo_patch", "propose_fact", "supersede"}},
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
			"todo":        map[string]interface{}{"type": "object", "properties": map[string]interface{}{"text": map[string]string{"type": "string"}, "status": map[string]interface{}{"type": "string", "enum": []string{"todo", "doing", "done", "blocked"}}, "owner": map[string]string{"type": "string"}, "due_at": map[string]string{"type": "string"}, "source": map[string]string{"type": "string"}}},
			"todo_id":     map[string]string{"type": "string"},
			"todo_status": map[string]interface{}{"type": "string", "enum": []string{"todo", "doing", "done", "blocked"}},
			"todo_owner":  map[string]string{"type": "string"},
			"todo_due_at": map[string]string{"type": "string"},
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
		// list_streams searches the caller's own streams and does not need this
		// session to be bound; every other action is scoped to the bound stream.
		if strings.TrimSpace(input.Action) != "list_streams" && id == "" {
			return tools.ToolResult{}, fmt.Errorf("this session is not linked to a work desktop; link it in Work Desktop first")
		}
		var out any
		switch strings.TrimSpace(input.Action) {
		case "list_streams":
			out, err = store.SearchContexts(ctx, scope, input.Query)
		case "get", "brief":
			out, err = store.Get(ctx, scope, id)
		case "recall":
			rangeScope := input.Scope
			if rangeScope == "" {
				rangeScope = "current"
			}
			out, err = store.Recall(ctx, scope, id, rangeScope, input.Query)
		case "focus":
			out, err = store.Focus(ctx, scope, id)
		case "ack":
			// Marking seen is explicit so read-receipts stay truthful: the model acks
			// only after it has actually reported the focus to the user.
			previous, ackErr := store.MarkSeen(ctx, scope, id)
			if ackErr != nil {
				err = ackErr
				break
			}
			out, err = map[string]any{"acknowledged": true, "previous_last_seen_at": previous}, nil
		case "plan":
			if strings.TrimSpace(input.Todo.Text) == "" {
				out, err = store.ListTodos(ctx, scope, id)
				break
			}
			out, err = store.CreateTodo(ctx, scope, id, input.Todo)
		case "todo_patch":
			if strings.TrimSpace(input.TodoID) == "" {
				return tools.ToolResult{}, fmt.Errorf("todo_patch requires todo_id")
			}
			patch := workcontext.TodoPatch{IfVersion: input.IfVersion}
			if text := strings.TrimSpace(input.Todo.Text); text != "" {
				patch.Text = &text
			}
			if status := strings.TrimSpace(input.TodoStatus); status != "" {
				patch.Status = &status
			}
			if owner := strings.TrimSpace(input.TodoOwner); owner != "" {
				patch.Owner = &owner
			}
			if due := strings.TrimSpace(input.TodoDueAt); due != "" {
				patch.DueAt = &due
			}
			out, err = store.PatchTodo(ctx, scope, id, input.TodoID, patch)
		case "checkpoint":
			out, err = store.SaveCheckpoint(ctx, scope, id, input.IfVersion, input.Checkpoint)
		case "propose_fact":
			input.Fact.Provenance = "derived"
			out, err = store.CreateFact(ctx, scope, id, input.Fact)
		case "supersede":
			// Replace an outdated fact by referencing it: the predecessor is only
			// retired once the replacement is reviewed and confirmed, so this is a
			// proposal, not a silent overwrite.
			if strings.TrimSpace(input.SupersedesID) == "" {
				return tools.ToolResult{}, fmt.Errorf("supersede requires supersedes_id")
			}
			input.Fact.Provenance = "derived"
			input.Fact.SupersedesID = strings.TrimSpace(input.SupersedesID)
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
