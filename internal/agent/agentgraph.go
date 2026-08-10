package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/tools"
)

// AgentGraph node types. Every node in the durable workflow runtime is
// executed as a subagent job; the node type is the semantic contract the
// orchestrator uses when planning the graph and interpreting the view.
const (
	agentGraphNodeLLMTask    = "llm_task"     // pure reasoning prompt, no tool access
	agentGraphNodeSubagent   = "subagent_task" // durable subagent job (default)
	agentGraphNodeToolCall   = "tool_call"    // a single tool invocation executed by a narrow agent
	agentGraphNodeUserInput  = "user_input"   // blocks until the orchestrator feeds input (complete_node)
	agentGraphNodeMergePoint = "merge_point"  // waits for all deps, then completes with a merged handoff summary
)

// AgentGraph edge types.
const (
	agentGraphEdgeDataDependency = "data_dependency" // target starts after source completes (start gating + data flow)
	agentGraphEdgeControlFlow    = "control_flow"    // react to source outcome: append a new node on condition (verdict/status)
	agentGraphEdgeHandoff        = "handoff"         // target receives source's bounded summary (implies dependency)
)

// agentGraphNodeInput is the declarative input for a graph node.
type agentGraphNodeInput struct {
	ID              string   `json:"id,omitempty"`
	NodeType        string   `json:"node_type,omitempty"`
	Title           string   `json:"title,omitempty"`
	Prompt          string   `json:"prompt,omitempty"`
	DependsOn       []string `json:"depends_on,omitempty"`
	HandoffFrom     []string `json:"handoff_from,omitempty"`
	HandoffMaxBytes int      `json:"handoff_max_bytes,omitempty"`
	AgentType       string   `json:"agent_type,omitempty"`
	WriteScope      []string `json:"write_scope,omitempty"`
}

// agentGraphEdgeInput is the declarative input for a graph edge.
//
// data_dependency / handoff edges reference two already-declared nodes
// (From -> To) and are compiled into the target node's DependsOn /
// HandoffFrom. control_flow edges carry an inline Append node template and
// become durable workflow edges: when From reaches the When condition the
// Append node is created and scheduled (the dynamic DAG behavior).
type agentGraphEdgeInput struct {
	ID            string               `json:"id,omitempty"`
	EdgeType      string               `json:"edge_type,omitempty"`
	From          string               `json:"from,omitempty"`
	To            string               `json:"to,omitempty"`
	When          string               `json:"when,omitempty"`
	Append        *agentGraphNodeInput `json:"append,omitempty"`
	MaxIterations int                  `json:"max_iterations,omitempty"`
}

// agentGraphEdgeView is the observable edge of a graph: every scheduling
// dependency, handoff, and dynamic control-flow edge with its type.
type agentGraphEdgeView struct {
	ID      string `json:"id"`
	Type    string `json:"edge_type"`
	From    string `json:"from"`
	To      string `json:"to"`
	Status  string `json:"status,omitempty"`
	Verdict string `json:"verdict,omitempty"`
}

// agentGraphView is the observability view of an agent graph. It reuses the
// workflow view (node status/attempt/handoff/verdict/... plus node_type) and
// adds a typed edge list.
type agentGraphView struct {
	WorkflowID string                `json:"workflow_id"`
	Status     string                `json:"status"`
	Total      int                   `json:"total"`
	Pending    int                   `json:"pending"`
	Running    int                   `json:"running"`
	Completed  int                   `json:"completed"`
	Failed     int                   `json:"failed"`
	Nodes      []workflowNodeView    `json:"nodes"`
	Edges      []agentGraphEdgeView  `json:"edges"`
	Started    []string              `json:"started,omitempty"`
	Appended   []string              `json:"appended,omitempty"`
	Wait       *subagentWaitView     `json:"wait,omitempty"`
}

// AgentGraph is the runtime abstraction for a dynamic, parallel, adjustable
// agent DAG (longtask 重构核心, roadmap 2.4). The implementation adapts to the
// durable workflow store: nodes are subagent jobs with bounded handoffs,
// dependencies gate scheduling, and control-flow edges append new nodes at
// runtime. The graph is fully observable through the returned views.
type AgentGraph interface {
	// Create declares a graph from node/edge inputs and persists it.
	Create(ctx context.Context, id string, nodes []agentGraphNodeInput, edges []agentGraphEdgeInput) (agentGraphView, error)
	// GetGraph returns the current observable state of the graph.
	GetGraph(ctx context.Context, id string) (agentGraphView, error)
	// AddNode appends new nodes (and edges) to a live graph.
	AddNode(ctx context.Context, id string, nodes []agentGraphNodeInput, edges []agentGraphEdgeInput, idempotencyKey, parentNodeID, reason string) (agentGraphView, error)
	// AddEdge adds static (data_dependency/handoff) or dynamic (control_flow) edges.
	AddEdge(ctx context.Context, id string, edges []agentGraphEdgeInput) (agentGraphView, error)
	// RemoveNode removes a node and any edges touching it. Nodes that still
	// depend on it block removal with an explicit error.
	RemoveNode(ctx context.Context, id, nodeID string) (agentGraphView, error)
	// CancelNode cancels a pending or running node.
	CancelNode(ctx context.Context, id, nodeID string) (agentGraphView, error)
	// Run starts every ready node (deps complete). merge_point nodes are
	// auto-completed; user_input nodes stay pending for complete_node.
	Run(ctx context.Context, id string) (agentGraphView, error)
	// Wait blocks until the running nodes reach a terminal state.
	Wait(ctx context.Context, id string, mode string, timeoutMS int) (agentGraphView, error)
}

// agentGraph implements AgentGraph on top of the Agent's durable workflow
// store. It is a thin adapter: the graph shares the exact workflow runtime
// (subagent jobs, bounded handoffs, edge processing) so a graph survives
// restarts and is inspectable through the same on-disk artifacts. A wrapper
// type is used because *Agent already declares Run(ctx) (the turn runner),
// which would collide with AgentGraph.Run.
type agentGraph struct {
	agent *Agent
}

// newAgentGraphRuntime binds an AgentGraph implementation to an Agent.
func newAgentGraphRuntime(agent *Agent) AgentGraph {
	return &agentGraph{agent: agent}
}

// compile-time assertion: *agentGraph implements AgentGraph.
var _ AgentGraph = (*agentGraph)(nil)

// agentGraphArgs is the tool argument envelope for the agent_graph tool.
type agentGraphArgs struct {
	Action         string                `json:"action,omitempty"`
	GraphID        string                `json:"graph_id,omitempty"`
	WorkflowID     string                `json:"workflow_id,omitempty"`
	NodeID         string                `json:"node_id,omitempty"`
	Mode           string                `json:"mode,omitempty"`
	TimeoutMS      int                   `json:"timeout_ms,omitempty"`
	Result         string                `json:"result,omitempty"`
	IdempotencyKey string                `json:"idempotency_key,omitempty"`
	ParentNodeID   string                `json:"parent_node_id,omitempty"`
	Reason         string                `json:"reason,omitempty"`
	Nodes          []agentGraphNodeInput `json:"nodes,omitempty"`
	Edges          []agentGraphEdgeInput `json:"edges,omitempty"`
}

func (args agentGraphArgs) graphWorkflowID() string {
	return firstNonEmpty(strings.TrimSpace(args.GraphID), strings.TrimSpace(args.WorkflowID))
}

// newAgentGraphTool exposes the AgentGraph runtime as a typed tool. The
// schema mirrors the workflow tool but speaks graph semantics: node_type /
// edge_type + create/get/add_node/add_edge/remove_node/cancel_node/run/wait.
func newAgentGraphTool(agent *Agent) tools.Tool {
	nodeTypeEnum := []string{agentGraphNodeLLMTask, agentGraphNodeSubagent, agentGraphNodeToolCall, agentGraphNodeUserInput, agentGraphNodeMergePoint}
	edgeTypeEnum := []string{agentGraphEdgeDataDependency, agentGraphEdgeControlFlow, agentGraphEdgeHandoff}
	return tools.NewTypedTool(tools.NewToolSpec("agent_graph", "Create, inspect, extend, cancel, and run a dynamic agent graph DAG. Nodes run as durable subagent jobs with bounded handoffs; dependencies gate scheduling; control-flow edges append new nodes on outcome; merge points auto-synthesize dependency summaries; user_input nodes wait for complete_node. Observability: each node exposes node_type/status/verdict/handoff and each edge exposes edge_type.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{"create", "get", "add_node", "add_edge", "remove_node", "cancel_node", "complete_node", "run", "wait"},
			},
			"graph_id":        map[string]string{"type": "string"},
			"workflow_id":     map[string]string{"type": "string"},
			"node_id":         map[string]string{"type": "string"},
			"idempotency_key": map[string]string{"type": "string"},
			"parent_node_id":  map[string]string{"type": "string"},
			"reason":          map[string]string{"type": "string"},
			"mode":            map[string]interface{}{"type": "string", "enum": []string{"any", "all"}},
			"timeout_ms":      map[string]string{"type": "integer"},
			"result":          map[string]string{"type": "string"},
			"nodes": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":                map[string]string{"type": "string"},
						"node_type":         map[string]interface{}{"type": "string", "enum": nodeTypeEnum},
						"title":             map[string]string{"type": "string"},
						"prompt":            map[string]string{"type": "string"},
						"depends_on":        map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
						"handoff_from":      map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
						"handoff_max_bytes": map[string]string{"type": "integer"},
						"agent_type":        map[string]string{"type": "string"},
						"write_scope":       map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
					},
				},
			},
			"edges": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":             map[string]string{"type": "string"},
						"edge_type":      map[string]interface{}{"type": "string", "enum": edgeTypeEnum},
						"from":           map[string]string{"type": "string"},
						"to":             map[string]string{"type": "string"},
						"when":           map[string]string{"type": "string"},
						"max_iterations": map[string]string{"type": "integer"},
						"append": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"id":                map[string]string{"type": "string"},
								"node_type":         map[string]interface{}{"type": "string", "enum": nodeTypeEnum},
								"title":             map[string]string{"type": "string"},
								"prompt":            map[string]string{"type": "string"},
								"depends_on":        map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
								"handoff_from":      map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
								"handoff_max_bytes": map[string]string{"type": "integer"},
								"agent_type":        map[string]string{"type": "string"},
								"write_scope":       map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
							},
						},
					},
				},
			},
		},
	}, nil), func(ctx context.Context, args agentGraphArgs) (tools.ToolResult, error) {
		action := strings.ToLower(strings.TrimSpace(args.Action))
		if action == "" {
			action = "get"
		}
		id := args.graphWorkflowID()
		graph := newAgentGraphRuntime(agent)
		switch action {
		case "create":
			view, err := graph.Create(ctx, id, args.Nodes, args.Edges)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "get":
			view, err := graph.GetGraph(ctx, id)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "add_node":
			view, err := graph.AddNode(ctx, id, args.Nodes, args.Edges, args.IdempotencyKey, args.ParentNodeID, args.Reason)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "add_edge":
			view, err := graph.AddEdge(ctx, id, args.Edges)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "remove_node":
			view, err := graph.RemoveNode(ctx, id, args.NodeID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "cancel_node":
			view, err := graph.CancelNode(ctx, id, args.NodeID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "complete_node":
			state, err := agent.completeWorkflowNode(id, args.NodeID, args.Result)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: agentGraphViewFromState(state)}, nil
		case "run":
			view, err := graph.Run(ctx, id)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "wait":
			view, err := graph.Wait(ctx, id, args.Mode, args.TimeoutMS)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		default:
			return tools.ToolResult{}, fmt.Errorf("unsupported agent_graph action %q", action)
		}
	})
}

// Create implements AgentGraph.Create.
func (g *agentGraph) Create(ctx context.Context, id string, nodes []agentGraphNodeInput, edges []agentGraphEdgeInput) (agentGraphView, error) {
	if len(nodes) == 0 {
		return agentGraphView{}, fmt.Errorf("missing graph nodes")
	}
	inputs := make([]workflowNodeInput, 0, len(nodes))
	for _, n := range nodes {
		inputs = append(inputs, agentGraphNodeToWorkflowInput(n))
	}
	dynamicEdges, err := compileAgentGraphEdges(&inputs, edges)
	if err != nil {
		return agentGraphView{}, err
	}
	state, err := g.agent.workflows.create(tools.SessionContextFromContext(ctx).SessionID, id, inputs, dynamicEdges)
	if err != nil {
		return agentGraphView{}, err
	}
	_ = g.agent.workflows.appendEvent(state.Summary.ID, map[string]interface{}{
		"event": "agent_graph_created",
		"nodes": workflowNodeIDs(state.Nodes),
		"edges": len(edges),
		"at":    time.Now().UTC(),
	})
	return agentGraphViewFromState(state), nil
}

// GetGraph implements AgentGraph.GetGraph.
func (g *agentGraph) GetGraph(ctx context.Context, id string) (agentGraphView, error) {
	state, err := g.agent.workflowState(id)
	if err != nil {
		return agentGraphView{}, err
	}
	return agentGraphViewFromState(state), nil
}

// AddNode implements AgentGraph.AddNode. Static edges (data_dependency /
// handoff / control_flow without append) are compiled into DependsOn /
// HandoffFrom on their target node; dynamic control_flow edges (with append)
// are stored as durable workflow edges.
func (g *agentGraph) AddNode(ctx context.Context, id string, nodes []agentGraphNodeInput, edges []agentGraphEdgeInput, idempotencyKey, parentNodeID, reason string) (agentGraphView, error) {
	if len(nodes) == 0 && len(edges) == 0 {
		return agentGraphView{}, fmt.Errorf("missing graph nodes or edges")
	}
	inputs := make([]workflowNodeInput, 0, len(nodes))
	for _, n := range nodes {
		inputs = append(inputs, agentGraphNodeToWorkflowInput(n))
	}
	dynamicEdges, staticPatches, err := splitAgentGraphEdges(&inputs, edges)
	if err != nil {
		return agentGraphView{}, err
	}
	view, err := g.agent.appendWorkflowNodes(id, inputs, dynamicEdges, idempotencyKey, parentNodeID, reason)
	if err != nil {
		return agentGraphView{}, err
	}
	if len(staticPatches) > 0 {
		state, err := g.agent.workflows.load(id)
		if err != nil {
			return agentGraphView{}, err
		}
		if err := applyAgentGraphStaticPatches(&state, staticPatches); err != nil {
			return agentGraphView{}, err
		}
		g.agent.refreshWorkflowStatus(&state)
		if _, err := g.agent.processWorkflowEdges(&state); err != nil {
			return agentGraphView{}, err
		}
		if err := g.agent.workflows.save(state); err != nil {
			return agentGraphView{}, err
		}
	}
	state, err := g.agent.workflowState(id)
	if err != nil {
		return agentGraphView{}, err
	}
	out := agentGraphViewFromState(state)
	out.Appended = append([]string{}, view.Appended...)
	return out, nil
}

// AddEdge implements AgentGraph.AddEdge.
func (g *agentGraph) AddEdge(ctx context.Context, id string, edges []agentGraphEdgeInput) (agentGraphView, error) {
	if len(edges) == 0 {
		return agentGraphView{}, fmt.Errorf("missing graph edges")
	}
	var inputs []workflowNodeInput
	dynamicEdges, staticPatches, err := splitAgentGraphEdges(&inputs, edges)
	if err != nil {
		return agentGraphView{}, err
	}
	if len(dynamicEdges) > 0 {
		if _, err := g.agent.appendWorkflowNodes(id, nil, dynamicEdges, "", "", ""); err != nil {
			return agentGraphView{}, err
		}
	}
	if len(staticPatches) > 0 {
		state, err := g.agent.workflows.load(id)
		if err != nil {
			return agentGraphView{}, err
		}
		if err := applyAgentGraphStaticPatches(&state, staticPatches); err != nil {
			return agentGraphView{}, err
		}
		g.agent.refreshWorkflowStatus(&state)
		if _, err := g.agent.processWorkflowEdges(&state); err != nil {
			return agentGraphView{}, err
		}
		if err := g.agent.workflows.save(state); err != nil {
			return agentGraphView{}, err
		}
	}
	state, err := g.agent.workflowState(id)
	if err != nil {
		return agentGraphView{}, err
	}
	return agentGraphViewFromState(state), nil
}

// RemoveNode implements AgentGraph.RemoveNode. A node that other nodes still
// depend on (DependsOn / HandoffFrom) blocks removal with an explicit error
// so the orchestrator rewires first. Running jobs are canceled.
func (g *agentGraph) RemoveNode(ctx context.Context, id, nodeID string) (agentGraphView, error) {
	state, err := g.agent.workflowState(id)
	if err != nil {
		return agentGraphView{}, err
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return agentGraphView{}, fmt.Errorf("missing node_id")
	}
	idx := -1
	for i, n := range state.Nodes {
		if n.ID == nodeID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return agentGraphView{}, fmt.Errorf("graph node not found: %s", nodeID)
	}
	for _, n := range state.Nodes {
		if n.ID == nodeID {
			continue
		}
		if containsString_(normalizeWorkflowStrings(append(append([]string{}, n.DependsOn...), n.HandoffFrom...)), nodeID) {
			return agentGraphView{}, fmt.Errorf("cannot remove node %s: node %s still depends on it; remove or rewire the dependent node first", nodeID, n.ID)
		}
	}
	now := time.Now().UTC()
	if state.Nodes[idx].Status == workflowStatusRunning && state.Nodes[idx].JobID != "" {
		_, _ = g.agent.subagentJobs.Cancel(state.Nodes[idx].JobID)
	}
	state.Nodes = append(state.Nodes[:idx], state.Nodes[idx+1:]...)
	kept := state.Edges[:0]
	for _, e := range state.Edges {
		if e.From == nodeID || e.Append.ID == nodeID {
			continue
		}
		kept = append(kept, e)
	}
	state.Edges = kept
	state.Summary.UpdatedAt = now
	g.agent.refreshWorkflowStatus(&state)
	if _, err := g.agent.processWorkflowEdges(&state); err != nil {
		return agentGraphView{}, err
	}
	if err := g.agent.workflows.save(state); err != nil {
		return agentGraphView{}, err
	}
	_ = g.agent.workflows.appendEvent(state.Summary.ID, map[string]interface{}{"event": "agent_graph_remove_node", "node_id": nodeID, "at": now})
	return agentGraphViewFromState(state), nil
}

// CancelNode implements AgentGraph.CancelNode.
func (g *agentGraph) CancelNode(ctx context.Context, id, nodeID string) (agentGraphView, error) {
	state, err := g.agent.cancelWorkflowNode(ctx, id, nodeID)
	if err != nil {
		return agentGraphView{}, err
	}
	return agentGraphViewFromState(state), nil
}

// Run implements AgentGraph.Run. Every pending node with completed deps is
// started; merge_point nodes are auto-completed with a merged handoff
// summary (token-budget truncated); user_input nodes stay pending until the
// orchestrator feeds them via complete_node.
func (g *agentGraph) Run(ctx context.Context, id string) (agentGraphView, error) {
	a := g.agent
	state, err := a.workflowState(id)
	if err != nil {
		return agentGraphView{}, err
	}
	started := []string{}
	now := time.Now().UTC()
	for i := range state.Nodes {
		node := &state.Nodes[i]
		if node.Status != workflowStatusPending || !workflowDepsCompleted(state.Nodes, node.DependsOn) {
			continue
		}
		switch normalizeAgentGraphNodeType(node.Kind) {
		case agentGraphNodeMergePoint:
			if err := a.completeMergePoint(&state, node); err != nil {
				return agentGraphView{}, err
			}
		case agentGraphNodeUserInput:
			// Blocked on orchestrator input: stays pending.
			continue
		default:
			if _, ok := a.startWorkflowNode(ctx, &state, node); ok {
				started = append(started, node.ID)
			}
		}
	}
	state.Summary.UpdatedAt = now
	a.refreshWorkflowStatus(&state)
	if _, err := a.processWorkflowEdges(&state); err != nil {
		return agentGraphView{}, err
	}
	if err := a.workflows.save(state); err != nil {
		return agentGraphView{}, err
	}
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{"event": "agent_graph_run", "started": started, "at": now})
	view := agentGraphViewFromState(state)
	view.Started = started
	return view, nil
}

// Wait implements AgentGraph.Wait.
func (g *agentGraph) Wait(ctx context.Context, id, mode string, timeoutMS int) (agentGraphView, error) {
	workflow, err := g.agent.waitWorkflow(ctx, id, mode, timeoutMS)
	if err != nil {
		return agentGraphView{}, err
	}
	return agentGraphViewFromWorkflowView(workflow), nil
}

// completeMergePoint finalizes a merge_point node once all its dependencies
// are complete: it synthesizes a merged, token-budget-truncated summary of
// the dependency handoffs and persists a handoff artifact so downstream
// data_dependency/handoff edges can consume it.
func (a *Agent) completeMergePoint(state *workflowState, node *workflowNode) error {
	if state == nil || node == nil || node.Status != workflowStatusPending {
		return nil
	}
	now := time.Now().UTC()
	summary, err := a.mergeNodeHandoffs(*state, *node)
	if err != nil {
		return fmt.Errorf("merge point %s: %w", node.ID, err)
	}
	node.Status = workflowStatusCompleted
	node.Attempt = nextWorkflowAttempt(*node)
	node.ResultPreview = previewSubagentResultForModel(summary)
	node.Verdict = workflowVerdictPass
	node.UpdatedAt = now
	node.FinishedAt = now
	if err := a.finalizeWorkflowNodeHandoff(state, node, nil, summary); err != nil {
		return err
	}
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{
		"event":         "agent_graph_merge_point",
		"node_id":       node.ID,
		"summary_tokens": compressCountTokensForText(summary),
		"at":            now,
	})
	return nil
}

// mergeNodeHandoffs concatenates the handoff summaries of every dependency
// (HandoffFrom, falling back to DependsOn) truncated to the node's handoff
// byte budget. Dependencies without a handoff artifact fall back to their
// result preview.
func (a *Agent) mergeNodeHandoffs(state workflowState, node workflowNode) (string, error) {
	depIDs := normalizeWorkflowHandoffFrom(node.HandoffFrom, node.DependsOn)
	if len(depIDs) == 0 {
		return "", nil
	}
	byID := make(map[string]workflowNode, len(state.Nodes))
	for _, item := range state.Nodes {
		byID[item.ID] = item
	}
	limit := normalizeWorkflowHandoffMaxBytes(node.HandoffMaxBytes)
	var chunks []string
	for _, depID := range depIDs {
		dep, ok := byID[depID]
		if !ok {
			continue
		}
		handoff, err := a.workflows.loadHandoff(state.Summary.ID, dep)
		if err != nil {
			if strings.TrimSpace(dep.ResultPreview) != "" {
				chunks = append(chunks, "- node: "+dep.ID+"\n  summary: "+strings.Join(strings.Fields(dep.ResultPreview), " ")+"\n")
			}
			continue
		}
		chunk := formatWorkflowDependencyHandoff(dep, handoff, workflowHandoffPolicySummaryArtifacts)
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
	}
	return assembleTruncatedHandoffs(chunks, limit), nil
}

// agentGraphNodeToWorkflowInput converts a graph node input into the durable
// workflow node representation. node_type is stored as the node Kind (the
// workflow runtime executes every node as a subagent job); control nodes get
// a descriptive default prompt.
func agentGraphNodeToWorkflowInput(n agentGraphNodeInput) workflowNodeInput {
	nodeType := normalizeAgentGraphNodeType(n.NodeType)
	prompt := strings.TrimSpace(n.Prompt)
	depends := normalizeWorkflowStrings(n.DependsOn)
	handoffFrom := normalizeWorkflowStrings(n.HandoffFrom)
	// handoff implies dependency: the target must wait for the handoff source.
	for _, hf := range handoffFrom {
		if !containsString_(depends, hf) {
			depends = append(depends, hf)
		}
	}
	switch nodeType {
	case agentGraphNodeMergePoint:
		if prompt == "" {
			prompt = "Merge point: synthesize a bounded summary of all completed dependency handoffs."
		}
	case agentGraphNodeUserInput:
		if prompt == "" {
			prompt = "User input required for this node."
		}
	}
	policy := ""
	if len(handoffFrom) > 0 {
		policy = workflowHandoffPolicySummary
	}
	return workflowNodeInput{
		ID:              strings.TrimSpace(n.ID),
		Kind:            nodeType,
		Title:           strings.TrimSpace(n.Title),
		Prompt:          prompt,
		DependsOn:       depends,
		HandoffPolicy:   policy,
		HandoffFrom:     handoffFrom,
		HandoffMaxBytes: n.HandoffMaxBytes,
		AgentType:       strings.TrimSpace(n.AgentType),
		WriteScope:      normalizeWorkflowStrings(n.WriteScope),
	}
}

// staticGraphPatch is a compiled static edge: target node gains a DependsOn
// (or HandoffFrom) entry for the source node.
type staticGraphPatch struct {
	To      string
	From    string
	Handoff bool
}

// compileAgentGraphEdges applies static edges to the node inputs and returns
// the dynamic (control_flow + append) edges for graph creation. The node
// inputs slice is mutated in place.
func compileAgentGraphEdges(inputs *[]workflowNodeInput, edges []agentGraphEdgeInput) ([]workflowEdgeInput, error) {
	patches, dynamic, err := classifyAgentGraphEdges(inputs, edges)
	if err != nil {
		return nil, err
	}
	if err := applyAgentGraphStaticPatchesToInputs(inputs, patches); err != nil {
		return nil, err
	}
	return dynamic, nil
}

// splitAgentGraphEdges separates static edges (compiled into the target
// node's DependsOn/HandoffFrom) from dynamic control_flow edges (kept as
// durable workflow edges). Used by add_node / add_edge where new nodes may be
// absent from the input slice (patches resolve against the persisted state).
func splitAgentGraphEdges(inputs *[]workflowNodeInput, edges []agentGraphEdgeInput) ([]workflowEdgeInput, []staticGraphPatch, error) {
	patches, dynamic, err := classifyAgentGraphEdges(inputs, edges)
	if err != nil {
		return nil, nil, err
	}
	return dynamic, patches, nil
}

func classifyAgentGraphEdges(inputs *[]workflowNodeInput, edges []agentGraphEdgeInput) ([]staticGraphPatch, []workflowEdgeInput, error) {
	var patches []staticGraphPatch
	var dynamic []workflowEdgeInput
	for _, e := range edges {
		edgeType := normalizeAgentGraphEdgeType(e.EdgeType)
		from := strings.TrimSpace(e.From)
		if from == "" {
			return nil, nil, fmt.Errorf("graph edge missing from")
		}
		switch edgeType {
		case agentGraphEdgeHandoff:
			if strings.TrimSpace(e.To) == "" {
				return nil, nil, fmt.Errorf("handoff edge %s missing to", e.ID)
			}
			patches = append(patches, staticGraphPatch{To: strings.TrimSpace(e.To), From: from, Handoff: true})
		case agentGraphEdgeControlFlow:
			if e.Append != nil {
				cond := workflowEdgeConditionFromGraphWhen(e.When)
				appendInput := agentGraphNodeToWorkflowInput(*e.Append)
				dynamic = append(dynamic, workflowEdgeInput{
					ID:            strings.TrimSpace(e.ID),
					From:          from,
					When:          cond,
					Append:        appendInput,
					MaxIterations: e.MaxIterations,
				})
				continue
			}
			if strings.TrimSpace(e.To) == "" {
				return nil, nil, fmt.Errorf("control_flow edge %s missing to or append", e.ID)
			}
			patches = append(patches, staticGraphPatch{To: strings.TrimSpace(e.To), From: from})
		default: // data_dependency
			if strings.TrimSpace(e.To) == "" {
				return nil, nil, fmt.Errorf("data_dependency edge %s missing to", e.ID)
			}
			patches = append(patches, staticGraphPatch{To: strings.TrimSpace(e.To), From: from})
		}
	}
	return patches, dynamic, nil
}

// applyAgentGraphStaticPatchesToInputs applies static patches to the node
// input slice (used during create when all nodes are declared up front).
func applyAgentGraphStaticPatchesToInputs(inputs *[]workflowNodeInput, patches []staticGraphPatch) error {
	byID := make(map[string]int, len(*inputs))
	for i, in := range *inputs {
		byID[in.ID] = i
	}
	for _, p := range patches {
		idx, ok := byID[p.To]
		if !ok {
			return fmt.Errorf("graph edge references unknown node %s", p.To)
		}
		if p.Handoff {
			if !containsString_((*inputs)[idx].HandoffFrom, p.From) {
				(*inputs)[idx].HandoffFrom = append((*inputs)[idx].HandoffFrom, p.From)
			}
			if !containsString_((*inputs)[idx].DependsOn, p.From) {
				(*inputs)[idx].DependsOn = append((*inputs)[idx].DependsOn, p.From)
			}
			(*inputs)[idx].HandoffPolicy = workflowHandoffPolicySummary
		} else if !containsString_((*inputs)[idx].DependsOn, p.From) {
			(*inputs)[idx].DependsOn = append((*inputs)[idx].DependsOn, p.From)
		}
	}
	return nil
}

// applyAgentGraphStaticPatches applies static patches against a persisted
// workflow state (used by add_node / add_edge where the target node may have
// been appended earlier).
func applyAgentGraphStaticPatches(state *workflowState, patches []staticGraphPatch) error {
	byID := make(map[string]int, len(state.Nodes))
	for i, n := range state.Nodes {
		byID[n.ID] = i
	}
	for _, p := range patches {
		idx, ok := byID[p.To]
		if !ok {
			return fmt.Errorf("graph edge references unknown node %s", p.To)
		}
		node := &state.Nodes[idx]
		if p.Handoff {
			if !containsString_(node.HandoffFrom, p.From) {
				node.HandoffFrom = append(node.HandoffFrom, p.From)
			}
			if !containsString_(node.DependsOn, p.From) {
				node.DependsOn = append(node.DependsOn, p.From)
			}
			node.HandoffPolicy = workflowHandoffPolicySummary
		} else if !containsString_(node.DependsOn, p.From) {
			node.DependsOn = append(node.DependsOn, p.From)
		}
		node.UpdatedAt = time.Now().UTC()
	}
	return nil
}

// workflowEdgeConditionFromGraphWhen maps a graph control_flow "when" value
// onto the durable workflow edge condition. Verdicts (pass/fail/blocked/
// needs_fix) gate on outcome; everything else gates on node status.
func workflowEdgeConditionFromGraphWhen(when string) workflowEdgeCondition {
	when = strings.ToLower(strings.TrimSpace(when))
	if when == "" {
		return workflowEdgeCondition{Status: workflowStatusCompleted}
	}
	if verdict := normalizeWorkflowVerdict(when); verdict != "" {
		return workflowEdgeCondition{Verdict: verdict}
	}
	switch when {
	case "completed", "running", "pending", "canceled", "error":
		return workflowEdgeCondition{Status: when}
	default:
		return workflowEdgeCondition{Status: when}
	}
}

// agentGraphViewFromState builds the observable graph view from a workflow
// state. Nodes reuse the workflow view (node_type included); edges are
// derived from scheduling deps, handoffs, and stored dynamic control-flow
// edges.
func agentGraphViewFromState(state workflowState) agentGraphView {
	base := workflowViewFromState(state)
	return agentGraphView{
		WorkflowID: base.WorkflowID,
		Status:     base.Status,
		Total:      base.Total,
		Pending:    base.Pending,
		Running:    base.Running,
		Completed:  base.Completed,
		Failed:     base.Failed,
		Nodes:      base.Nodes,
		Edges:      agentGraphEdgesFromState(state),
	}
}

// agentGraphViewFromWorkflowView converts an already-computed workflow view
// (e.g. from waitWorkflow, which attaches a Wait summary) into a graph view.
func agentGraphViewFromWorkflowView(base workflowView) agentGraphView {
	view := agentGraphView{
		WorkflowID: base.WorkflowID,
		Status:     base.Status,
		Total:      base.Total,
		Pending:    base.Pending,
		Running:    base.Running,
		Completed:  base.Completed,
		Failed:     base.Failed,
		Nodes:      base.Nodes,
		Started:    append([]string{}, base.Started...),
		Appended:   append([]string{}, base.Appended...),
		Wait:       base.Wait,
	}
	state := workflowState{
		Summary: workflowSummary{ID: base.WorkflowID},
		Nodes:   make([]workflowNode, 0, len(base.Nodes)),
	}
	for _, n := range base.Nodes {
		state.Nodes = append(state.Nodes, workflowNode{ID: n.ID, Kind: n.Kind, DependsOn: append([]string{}, n.DependsOn...), HandoffFrom: append([]string{}, n.HandoffFrom...), Status: n.Status})
	}
	for _, e := range base.Edges {
		state.Edges = append(state.Edges, e)
	}
	view.Edges = agentGraphEdgesFromState(state)
	return view
}

// agentGraphEdgesFromState derives the typed edge list for observability:
//   - data_dependency: target.DependsOn (each dependency is a scheduling gate)
//   - handoff: target.HandoffFrom entries not already in DependsOn
//   - control_flow: stored workflow edges (dynamic append on condition)
func agentGraphEdgesFromState(state workflowState) []agentGraphEdgeView {
	var out []agentGraphEdgeView
	seen := make(map[string]struct{})
	add := func(id, edgeType, from, to, status, verdict string) {
		if from == "" || to == "" {
			return
		}
		if id == "" {
			id = fmt.Sprintf("edge_%s_%s_%s", edgeType, from, to)
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, agentGraphEdgeView{ID: id, Type: edgeType, From: from, To: to, Status: status, Verdict: verdict})
	}
	for _, n := range state.Nodes {
		deps := normalizeWorkflowStrings(n.DependsOn)
		handoffs := normalizeWorkflowStrings(n.HandoffFrom)
		for _, dep := range deps {
			// A handoff source is also a scheduling dependency; surface it as
			// the stronger (handoff) edge so the observable graph matches the
			// orchestrator's intent.
			if containsString_(handoffs, dep) {
				add("", agentGraphEdgeHandoff, dep, n.ID, "", "")
			} else {
				add("", agentGraphEdgeDataDependency, dep, n.ID, "", "")
			}
		}
		for _, hf := range handoffs {
			if containsString_(deps, hf) {
				continue
			}
			add("", agentGraphEdgeHandoff, hf, n.ID, "", "")
		}
	}
	for _, e := range state.Edges {
		add(strings.TrimSpace(e.ID), agentGraphEdgeControlFlow, e.From, e.Append.ID, e.When.Status, e.When.Verdict)
	}
	return out
}

// normalizeAgentGraphNodeType canonicalizes a node type. Unknown / empty
// kinds default to subagent_task so the workflow runtime's subagent
// execution matches the observable contract.
func normalizeAgentGraphNodeType(nodeType string) string {
	nodeType = strings.ToLower(strings.TrimSpace(nodeType))
	switch nodeType {
	case agentGraphNodeLLMTask, agentGraphNodeSubagent, agentGraphNodeToolCall, agentGraphNodeUserInput, agentGraphNodeMergePoint:
		return nodeType
	case "", "story", "task", "default":
		return agentGraphNodeSubagent
	default:
		return nodeType
	}
}

// normalizeAgentGraphEdgeType canonicalizes an edge type; unknown / empty
// defaults to data_dependency (the plain scheduling gate).
func normalizeAgentGraphEdgeType(edgeType string) string {
	edgeType = strings.ToLower(strings.TrimSpace(edgeType))
	switch edgeType {
	case agentGraphEdgeDataDependency, agentGraphEdgeControlFlow, agentGraphEdgeHandoff:
		return edgeType
	default:
		return agentGraphEdgeDataDependency
	}
}
