package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/tools"
)

func newLongTaskTool(agent *Agent) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("longtask", "Create and drive Ralph-style long tasks by compiling prioritized user stories into a durable workflow. Each story runs as a fresh durable subagent node with bounded handoffs.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{"create", "plan", "status", "start", "wait", "cancel", "complete_story", "finalize_story", "run", "run_status"},
			},
			"longtask_id":           map[string]string{"type": "string"},
			"workflow_id":           map[string]string{"type": "string"},
			"project":               map[string]string{"type": "string"},
			"branch_name":           map[string]string{"type": "string"},
			"description":           map[string]string{"type": "string"},
			"quality_checks":        map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
			"validation_timeout_ms": map[string]string{"type": "integer"},
			"merge_policy":          map[string]interface{}{"type": "string", "enum": []string{"auto_merge", "review_only"}},
			"commit_policy":         map[string]interface{}{"type": "string", "enum": []string{"auto_commit", "none"}},
			"node_id":               map[string]string{"type": "string"},
			"result":                map[string]string{"type": "string"},
			"mode":                  map[string]interface{}{"type": "string", "enum": []string{"any", "all"}},
			"timeout_ms":            map[string]string{"type": "integer"},
			"max_iterations":        map[string]string{"type": "integer"},
			"wait_timeout_ms":       map[string]string{"type": "integer"},
			"stop_on_failure":       map[string]string{"type": "boolean"},
			"auto_repair":           map[string]string{"type": "boolean"},
			"max_repair_attempts":   map[string]string{"type": "integer"},
			"async":                 map[string]string{"type": "boolean"},
			"stories": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":                  map[string]string{"type": "string"},
						"title":               map[string]string{"type": "string"},
						"description":         map[string]string{"type": "string"},
						"acceptance_criteria": map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
						"priority":            map[string]string{"type": "integer"},
						"agent_type":          map[string]string{"type": "string"},
						"write_scope":         map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
						"handoff_policy":      map[string]interface{}{"type": "string", "enum": []string{"none", "summary", "summary_artifacts", "selected"}},
						"handoff_max_bytes":   map[string]string{"type": "integer"},
					},
				},
			},
		},
	}, nil), func(ctx context.Context, args longTaskArgs) (tools.ToolResult, error) {
		action := strings.ToLower(strings.TrimSpace(args.Action))
		if action == "" {
			action = "status"
		}
		switch action {
		case "create":
			view, err := agent.createLongTask(tools.SessionContextFromContext(ctx).SessionID, args)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "plan":
			view, err := agent.planLongTask(ctx, args)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "status":
			view, err := agent.longTaskStatus(args.longTaskWorkflowID())
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "start":
			view, err := agent.startLongTask(ctx, args.longTaskWorkflowID())
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "wait":
			view, err := agent.waitLongTask(ctx, args.longTaskWorkflowID(), args.Mode, args.TimeoutMS)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "cancel":
			view, err := agent.cancelLongTask(ctx, args)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "complete_story":
			state, err := agent.completeWorkflowNode(args.longTaskWorkflowID(), args.NodeID, args.Result)
			if err != nil {
				return tools.ToolResult{}, err
			}
			view, err := agent.longTaskViewForState(state)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "finalize_story":
			view, err := agent.finalizeLongTaskStory(ctx, args.longTaskWorkflowID(), args.NodeID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "run":
			view, err := agent.runLongTask(ctx, args.longTaskWorkflowID(), args)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "run_status":
			view, err := agent.longTaskRunStatus(args.longTaskWorkflowID())
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "lookup":
			entries, err := agent.LongTaskLookupByCommit(strings.TrimSpace(args.CommitHash), args.longTaskWorkflowID())
			if err != nil {
				return tools.ToolResult{}, err
			}
			agent.appendLongTaskLookupReflux(strings.TrimSpace(args.CommitHash), entries)
			return tools.ToolResult{Structured: map[string]interface{}{
				"commit":   strings.TrimSpace(args.CommitHash),
				"longtask": args.longTaskWorkflowID(),
				"matches":  entries,
			}}, nil
		case "rollback":
			result, err := agent.RollbackLongTaskStory(ctx, args.longTaskWorkflowID(), args.NodeID, args.RollbackReason)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: result}, nil
		case "gc":
			olderThan := time.Unix(0, 0)
			if args.OlderThanSeconds > 0 {
				olderThan = time.Now().UTC().Add(-time.Duration(args.OlderThanSeconds) * time.Second)
			}
			result, err := agent.SweepLongTaskArtifacts(args.longTaskWorkflowID(), olderThan, args.ApplyGC)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: result}, nil
		default:
			return tools.ToolResult{}, fmt.Errorf("unsupported longtask action %q", action)
		}
	})
}

func (args longTaskArgs) longTaskWorkflowID() string {
	if id := strings.TrimSpace(args.WorkflowID); id != "" {
		return id
	}
	return strings.TrimSpace(args.LongTaskID)
}

// ListLongTasks returns LongTasks known to this agent workspace.
func (a *Agent) ListLongTasks(sessionIDs ...string) ([]LongTaskView, error) {
	if a == nil || a.workflows == nil || strings.TrimSpace(a.workflows.dir) == "" {
		return nil, fmt.Errorf("workflow store is unavailable")
	}
	sessionID := ""
	if len(sessionIDs) > 0 {
		sessionID = strings.TrimSpace(sessionIDs[0])
	}
	entries, err := os.ReadDir(a.workflows.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var items []LongTaskView
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workflowID := entry.Name()
		if _, err := os.Stat(filepath.Join(a.workflows.dir, workflowID, longTaskSpecFile)); err != nil {
			continue
		}
		if sessionID != "" {
			state, err := a.workflows.load(workflowID)
			if err != nil || strings.TrimSpace(state.Summary.SessionID) != sessionID {
				continue
			}
		}
		view, err := a.longTaskStatus(workflowID)
		if err != nil {
			continue
		}
		items = append(items, view)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].WorkflowID < items[j].WorkflowID
	})
	return items, nil
}

// GetLongTask returns one LongTask by workflow id.
func (a *Agent) GetLongTask(workflowID string) (LongTaskView, error) {
	return a.longTaskStatus(workflowID)
}

// CreateLongTask creates a LongTask spec and backing workflow.
func (a *Agent) CreateLongTask(sessionID string, args LongTaskArgs) (LongTaskView, error) {
	return a.createLongTask(sessionID, args)
}

// RunLongTask drives a LongTask until it completes, blocks, stalls, or reaches max iterations.
func (a *Agent) RunLongTask(ctx context.Context, workflowID string, args LongTaskArgs) (LongTaskView, error) {
	return a.runLongTask(ctx, workflowID, args)
}

// CancelLongTask cancels a LongTask workflow node.
// CancelLongTask cancels one LongTask workflow node or, with CancelAll,
// every story in the workflow. The public signature is unchanged for
// single-node cancellation; the cascade path is selected by passing
// `args.CancelAll = true`.
func (a *Agent) CancelLongTask(ctx context.Context, workflowID, nodeID string) (LongTaskView, error) {
	return a.cancelLongTask(ctx, longTaskArgs{WorkflowID: workflowID, NodeID: nodeID})
}

// CancelLongTaskAll cancels every story in the workflow. Exposed
// separately so the backend service and HTTP layer do not have to
// poke at the args struct directly.
func (a *Agent) CancelLongTaskAll(ctx context.Context, workflowID string) (LongTaskView, error) {
	return a.cancelLongTask(ctx, longTaskArgs{WorkflowID: workflowID, CancelAll: true})
}

// FinalizeLongTaskStory validates and finalizes one completed story node.
func (a *Agent) FinalizeLongTaskStory(ctx context.Context, workflowID, nodeID string) (LongTaskView, error) {
	return a.finalizeLongTaskStory(ctx, workflowID, nodeID)
}
