package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
	"github.com/tim5wang/godex/internal/tools"
)

const (
	workflowSummaryFile = "summary.json"
	workflowNodesFile   = "nodes.json"
	workflowEdgesFile   = "edges.json"
	workflowEventsFile  = "events.jsonl"
	workflowHandoffsDir = "handoffs"

	workflowStatusPending   = "pending"
	workflowStatusRunning   = "running"
	workflowStatusCompleted = "completed"
	workflowStatusCanceled  = "canceled"
	workflowStatusError     = "error"

	workflowVerdictPass     = "pass"
	workflowVerdictFail     = "fail"
	workflowVerdictBlocked  = "blocked"
	workflowVerdictNeedsFix = "needs_fix"

	workflowHandoffPolicyNone             = "none"
	workflowHandoffPolicySummary          = "summary"
	workflowHandoffPolicySummaryArtifacts = "summary_artifacts"
	workflowHandoffPolicySelected         = "selected"
	workflowDefaultHandoffMaxBytes        = 8000
	workflowMaxHandoffMaxBytes            = 32000
	workflowMaxNodes                      = 64
)

type workflowStore struct {
	dir string
	mu  sync.Mutex
}

type workflowSummary struct {
	ID             string              `json:"id"`
	SessionID      string              `json:"session_id,omitempty"`
	Status         string              `json:"status"`
	AppendKeys     map[string][]string `json:"append_keys,omitempty"`
	EdgeIterations map[string]int      `json:"edge_iterations,omitempty"`
	ProcessedEdges map[string]bool     `json:"processed_edges,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

type workflowNode struct {
	ID              string        `json:"id"`
	IdentityID      string        `json:"identity_id,omitempty"`
	AgentIdentity   AgentIdentity `json:"agent_identity,omitempty"`
	Kind            string        `json:"kind,omitempty"`
	Title           string        `json:"title,omitempty"`
	Prompt          string        `json:"prompt,omitempty"`
	DependsOn       []string      `json:"depends_on,omitempty"`
	HandoffPolicy   string        `json:"handoff_policy,omitempty"`
	HandoffFrom     []string      `json:"handoff_from,omitempty"`
	HandoffMaxBytes int           `json:"handoff_max_bytes,omitempty"`
	PreviewMerge    bool          `json:"preview_merge,omitempty"`
	Status          string        `json:"status"`
	AgentType       string        `json:"agent_type,omitempty"`
	WriteScope      []string      `json:"write_scope,omitempty"`
	JobID           string        `json:"job_id,omitempty"`
	Attempt         int           `json:"attempt,omitempty"`
	HandoffRef      string        `json:"handoff_ref,omitempty"`
	HandoffDigest   string        `json:"handoff_digest,omitempty"`
	Verdict         string        `json:"verdict,omitempty"`
	ArtifactRefs    []string      `json:"artifact_refs,omitempty"`
	ResultPreview   string        `json:"result_preview,omitempty"`
	Error           string        `json:"error,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	FinishedAt      time.Time     `json:"finished_at,omitempty"`
}

type workflowState struct {
	Summary workflowSummary `json:"summary"`
	Nodes   []workflowNode  `json:"nodes"`
	Edges   []workflowEdge  `json:"edges,omitempty"`
}

type workflowArgs struct {
	Action         string              `json:"action,omitempty"`
	WorkflowID     string              `json:"workflow_id,omitempty"`
	NodeID         string              `json:"node_id,omitempty"`
	Mode           string              `json:"mode,omitempty"`
	TimeoutMS      int                 `json:"timeout_ms,omitempty"`
	Result         string              `json:"result,omitempty"`
	IdempotencyKey string              `json:"idempotency_key,omitempty"`
	ParentNodeID   string              `json:"parent_node_id,omitempty"`
	Reason         string              `json:"reason,omitempty"`
	Nodes          []workflowNodeInput `json:"nodes,omitempty"`
	Edges          []workflowEdgeInput `json:"edges,omitempty"`
}

type workflowEdgeCondition struct {
	Status  string `json:"status,omitempty"`
	Verdict string `json:"verdict,omitempty"`
}

type workflowEdge struct {
	ID            string                `json:"id"`
	From          string                `json:"from,omitempty"`
	FromKind      string                `json:"from_kind,omitempty"`
	When          workflowEdgeCondition `json:"when,omitempty"`
	Append        workflowNodeInput     `json:"append"`
	MaxIterations int                   `json:"max_iterations,omitempty"`
	IterationKey  string                `json:"iteration_key,omitempty"`
}

type workflowEdgeInput struct {
	ID            string                `json:"id,omitempty"`
	From          string                `json:"from,omitempty"`
	FromKind      string                `json:"from_kind,omitempty"`
	When          workflowEdgeCondition `json:"when,omitempty"`
	Append        workflowNodeInput     `json:"append"`
	MaxIterations int                   `json:"max_iterations,omitempty"`
	IterationKey  string                `json:"iteration_key,omitempty"`
}

type workflowNodeInput struct {
	ID              string   `json:"id,omitempty"`
	Kind            string   `json:"kind,omitempty"`
	Title           string   `json:"title,omitempty"`
	Prompt          string   `json:"prompt,omitempty"`
	DependsOn       []string `json:"depends_on,omitempty"`
	HandoffPolicy   string   `json:"handoff_policy,omitempty"`
	HandoffFrom     []string `json:"handoff_from,omitempty"`
	HandoffMaxBytes int      `json:"handoff_max_bytes,omitempty"`
	PreviewMerge    *bool    `json:"preview_merge,omitempty"`
	AgentType       string   `json:"agent_type,omitempty"`
	WriteScope      []string `json:"write_scope,omitempty"`
}

type workflowNodeView struct {
	ID              string        `json:"id"`
	IdentityID      string        `json:"identity_id,omitempty"`
	AgentIdentity   AgentIdentity `json:"agent_identity,omitempty"`
	Kind            string        `json:"kind,omitempty"`
	NodeType        string        `json:"node_type,omitempty"`
	Title           string        `json:"title,omitempty"`
	DependsOn       []string      `json:"depends_on,omitempty"`
	HandoffPolicy   string        `json:"handoff_policy,omitempty"`
	HandoffFrom     []string      `json:"handoff_from,omitempty"`
	HandoffMaxBytes int           `json:"handoff_max_bytes,omitempty"`
	PreviewMerge    bool          `json:"preview_merge,omitempty"`
	Status          string        `json:"status"`
	AgentType       string        `json:"agent_type,omitempty"`
	WriteScope      []string      `json:"write_scope,omitempty"`
	JobID           string        `json:"job_id,omitempty"`
	Attempt         int           `json:"attempt,omitempty"`
	HandoffRef      string        `json:"handoff_ref,omitempty"`
	HandoffDigest   string        `json:"handoff_digest,omitempty"`
	Verdict         string        `json:"verdict,omitempty"`
	ArtifactRefs    []string      `json:"artifact_refs,omitempty"`
	ResultPreview   string        `json:"result_preview,omitempty"`
	Error           string        `json:"error,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	FinishedAt      time.Time     `json:"finished_at,omitempty"`
}

type workflowView struct {
	WorkflowID string             `json:"workflow_id"`
	Status     string             `json:"status"`
	Total      int                `json:"total"`
	Pending    int                `json:"pending"`
	Running    int                `json:"running"`
	Completed  int                `json:"completed"`
	Failed     int                `json:"failed"`
	Nodes      []workflowNodeView `json:"nodes"`
	Started    []string           `json:"started,omitempty"`
	Appended   []string           `json:"appended,omitempty"`
	Edges      []workflowEdge     `json:"edges,omitempty"`
	Wait       *subagentWaitView  `json:"wait,omitempty"`
}

type workflowNodeHandoff struct {
	WorkflowID    string               `json:"workflow_id"`
	NodeID        string               `json:"node_id"`
	Attempt       int                  `json:"attempt"`
	JobID         string               `json:"job_id,omitempty"`
	Status        string               `json:"status"`
	Verdict       string               `json:"verdict"`
	Summary       string               `json:"summary,omitempty"`
	ResultPreview string               `json:"result_preview,omitempty"`
	Error         string               `json:"error,omitempty"`
	ChangedFiles  []subagentFileChange `json:"changed_files,omitempty"`
	ArtifactRefs  []string             `json:"artifact_refs,omitempty"`
	CreatedAt     time.Time            `json:"created_at"`
	Digest        string               `json:"digest"`
}

func newWorkflowStore(dir string) *workflowStore {
	return &workflowStore{dir: strings.TrimSpace(dir)}
}

func newWorkflowTool(agent *Agent) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("workflow", "Create, start, wait, inspect, cancel, and manually complete durable workflow DAG nodes. Ready nodes are executed as durable subagent jobs.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{"create", "status", "start", "wait", "cancel", "complete_node", "append_node"},
			},
			"workflow_id":     map[string]string{"type": "string"},
			"node_id":         map[string]string{"type": "string"},
			"idempotency_key": map[string]string{"type": "string"},
			"parent_node_id":  map[string]string{"type": "string"},
			"reason":          map[string]string{"type": "string"},
			"mode": map[string]interface{}{
				"type": "string",
				"enum": []string{"any", "all"},
			},
			"timeout_ms": map[string]string{"type": "integer"},
			"result":     map[string]string{"type": "string"},
			"nodes": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":                map[string]string{"type": "string"},
						"kind":              map[string]string{"type": "string"},
						"title":             map[string]string{"type": "string"},
						"prompt":            map[string]string{"type": "string"},
						"depends_on":        map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
						"handoff_policy":    map[string]interface{}{"type": "string", "enum": []string{"none", "summary", "summary_artifacts", "selected"}},
						"handoff_from":      map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
						"handoff_max_bytes": map[string]string{"type": "integer"},
						"preview_merge":     map[string]string{"type": "boolean"},
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
						"from":           map[string]string{"type": "string"},
						"from_kind":      map[string]string{"type": "string"},
						"max_iterations": map[string]string{"type": "integer"},
						"iteration_key":  map[string]string{"type": "string"},
						"when": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"status":  map[string]string{"type": "string"},
								"verdict": map[string]string{"type": "string"},
							},
						},
						"append": map[string]interface{}{
							"type": "object",
						},
					},
				},
			},
		},
	}, nil), func(ctx context.Context, args workflowArgs) (tools.ToolResult, error) {
		action := strings.ToLower(strings.TrimSpace(args.Action))
		if action == "" {
			action = "status"
		}
		switch action {
		case "create":
			state, err := agent.workflows.create(tools.SessionContextFromContext(ctx).SessionID, args.WorkflowID, args.Nodes, args.Edges)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: workflowViewFromState(state)}, nil
		case "status":
			state, err := agent.workflowState(args.WorkflowID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: workflowViewFromState(state)}, nil
		case "append_node":
			view, err := agent.appendWorkflowNodes(args.WorkflowID, args.Nodes, args.Edges, args.IdempotencyKey, args.ParentNodeID, args.Reason)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "start":
			view, err := agent.startWorkflowReadyNodes(ctx, args.WorkflowID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "wait":
			view, err := agent.waitWorkflow(ctx, args.WorkflowID, args.Mode, args.TimeoutMS)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: view}, nil
		case "cancel":
			state, err := agent.cancelWorkflowNode(ctx, args.WorkflowID, args.NodeID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: workflowViewFromState(state)}, nil
		case "complete_node":
			state, err := agent.completeWorkflowNode(args.WorkflowID, args.NodeID, args.Result)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: workflowViewFromState(state)}, nil
		default:
			return tools.ToolResult{}, fmt.Errorf("unsupported workflow action %q", action)
		}
	})
}

func (s *workflowStore) create(sessionID, workflowID string, inputs []workflowNodeInput, edgeInputs []workflowEdgeInput) (workflowState, error) {
	if len(inputs) == 0 {
		return workflowState{}, fmt.Errorf("missing workflow nodes")
	}
	now := time.Now().UTC()
	id := strings.TrimSpace(workflowID)
	if id == "" {
		id = "wf_" + fmt.Sprintf("%d", now.UnixNano())
	} else if err := validateWorkflowID(id); err != nil {
		return workflowState{}, err
	}
	if _, err := os.Stat(filepath.Join(s.dir, id)); err == nil {
		return workflowState{}, fmt.Errorf("workflow already exists: %s", id)
	} else if err != nil && !os.IsNotExist(err) {
		return workflowState{}, err
	}
	nodes, err := workflowNodesFromInputs(inputs, nil, now)
	if err != nil {
		return workflowState{}, err
	}
	if err := validateWorkflowDeps(nodes); err != nil {
		return workflowState{}, err
	}
	edges, err := workflowEdgesFromInputs(edgeInputs)
	if err != nil {
		return workflowState{}, err
	}
	if err := validateWorkflowEdges(edges, nodes); err != nil {
		return workflowState{}, err
	}
	state := workflowState{
		Summary: workflowSummary{
			ID:        id,
			SessionID: strings.TrimSpace(sessionID),
			Status:    workflowStatusPending,
			CreatedAt: now,
			UpdatedAt: now,
		},
		Nodes: nodes,
		Edges: edges,
	}
	if err := s.save(state); err != nil {
		return workflowState{}, err
	}
	_ = s.appendEvent(id, map[string]interface{}{"event": "created", "at": now})
	return state, nil
}

func workflowNodesFromInputs(inputs []workflowNodeInput, existing map[string]struct{}, now time.Time) ([]workflowNode, error) {
	nodes := make([]workflowNode, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for id := range existing {
		seen[id] = struct{}{}
	}
	for _, input := range inputs {
		nodeID := strings.TrimSpace(input.ID)
		if nodeID == "" {
			nodeID = fmt.Sprintf("node_%d", len(seen)+1)
		}
		if _, ok := seen[nodeID]; ok {
			return nil, fmt.Errorf("duplicate node id %q", nodeID)
		}
		seen[nodeID] = struct{}{}
		prompt := strings.TrimSpace(input.Prompt)
		if prompt == "" {
			return nil, fmt.Errorf("node %s missing prompt", nodeID)
		}
		identity := NewAgentIdentity(now, "", "workflow_node", firstNonEmpty(strings.TrimSpace(input.Title), nodeID), strings.TrimSpace(input.AgentType), "", "workflow", capabilitySummaryForTools(subagentToolNames(input.AgentType), input.WriteScope))
		nodes = append(nodes, workflowNode{
			ID:              nodeID,
			IdentityID:      identity.ID,
			AgentIdentity:   identity,
			Kind:            normalizeWorkflowNodeKind(input.Kind, nodeID),
			Title:           strings.TrimSpace(input.Title),
			Prompt:          prompt,
			DependsOn:       normalizeWorkflowStrings(input.DependsOn),
			HandoffPolicy:   normalizeWorkflowHandoffPolicy(input.HandoffPolicy, len(input.DependsOn) > 0),
			// HandoffFrom stays explicit (not defaulted from DependsOn):
			// consumers fall back to DependsOn at use time, and keeping the
			// field empty lets the agent_graph view distinguish declared
			// handoff edges from plain data_dependency edges.
			HandoffFrom:     normalizeWorkflowStrings(input.HandoffFrom),
			HandoffMaxBytes: normalizeWorkflowHandoffMaxBytes(input.HandoffMaxBytes),
			PreviewMerge:    normalizeWorkflowPreviewMerge(input.PreviewMerge),
			Status:          workflowStatusPending,
			AgentType:       strings.TrimSpace(input.AgentType),
			WriteScope:      normalizeWorkflowStrings(input.WriteScope),
			CreatedAt:       now,
			UpdatedAt:       now,
		})
	}
	return nodes, nil
}

func workflowEdgesFromInputs(inputs []workflowEdgeInput) ([]workflowEdge, error) {
	edges := make([]workflowEdge, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for i, input := range inputs {
		id := strings.TrimSpace(input.ID)
		if id == "" {
			id = fmt.Sprintf("edge_%d", i+1)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("duplicate workflow edge id %q", id)
		}
		seen[id] = struct{}{}
		edge := workflowEdge{
			ID:            id,
			From:          strings.TrimSpace(input.From),
			FromKind:      strings.TrimSpace(input.FromKind),
			When:          workflowEdgeCondition{Status: strings.TrimSpace(input.When.Status), Verdict: normalizeWorkflowVerdict(input.When.Verdict)},
			Append:        input.Append,
			MaxIterations: normalizeWorkflowEdgeMaxIterations(input.MaxIterations),
			IterationKey:  strings.TrimSpace(input.IterationKey),
		}
		if edge.IterationKey == "" {
			edge.IterationKey = edge.ID
		}
		if edge.From == "" && edge.FromKind == "" {
			return nil, fmt.Errorf("workflow edge %s missing from or from_kind", edge.ID)
		}
		if edge.When.Status == "" && edge.When.Verdict == "" {
			return nil, fmt.Errorf("workflow edge %s missing when status or verdict", edge.ID)
		}
		if strings.TrimSpace(edge.Append.Prompt) == "" {
			return nil, fmt.Errorf("workflow edge %s append node missing prompt", edge.ID)
		}
		edges = append(edges, edge)
	}
	return edges, nil
}

func (s *workflowStore) load(id string) (workflowState, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return workflowState{}, fmt.Errorf("missing workflow_id")
	}
	if err := validateWorkflowID(id); err != nil {
		return workflowState{}, err
	}
	dir := filepath.Join(s.dir, id)
	var summary workflowSummary
	summaryData, err := os.ReadFile(filepath.Join(dir, workflowSummaryFile))
	if err != nil {
		return workflowState{}, err
	}
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		return workflowState{}, err
	}
	var nodes []workflowNode
	nodesData, err := os.ReadFile(filepath.Join(dir, workflowNodesFile))
	if err != nil {
		return workflowState{}, err
	}
	if err := json.Unmarshal(nodesData, &nodes); err != nil {
		return workflowState{}, err
	}
	for i := range nodes {
		nodes[i].Kind = normalizeWorkflowNodeKind(nodes[i].Kind, nodes[i].ID)
		nodes[i].HandoffPolicy = normalizeWorkflowHandoffPolicy(nodes[i].HandoffPolicy, len(nodes[i].DependsOn) > 0)
		nodes[i].HandoffFrom = normalizeWorkflowStrings(nodes[i].HandoffFrom)
		nodes[i].HandoffMaxBytes = normalizeWorkflowHandoffMaxBytes(nodes[i].HandoffMaxBytes)
		if len(nodes[i].DependsOn) > 0 && nodes[i].HandoffPolicy != workflowHandoffPolicyNone && !nodes[i].PreviewMerge {
			nodes[i].PreviewMerge = true
		}
	}
	var edges []workflowEdge
	edgesData, err := os.ReadFile(filepath.Join(dir, workflowEdgesFile))
	if err == nil {
		if err := json.Unmarshal(edgesData, &edges); err != nil {
			return workflowState{}, err
		}
	} else if !os.IsNotExist(err) {
		return workflowState{}, err
	}
	return workflowState{Summary: summary, Nodes: nodes, Edges: edges}, nil
}

func (s *workflowStore) save(state workflowState) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("workflow store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.dir, state.Summary.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(dir, workflowSummaryFile), state.Summary, 0644); err != nil {
		return err
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(dir, workflowNodesFile), state.Nodes, 0644); err != nil {
		return err
	}
	if len(state.Edges) == 0 {
		_ = os.Remove(filepath.Join(dir, workflowEdgesFile))
		return nil
	}
	return fsutil.WriteJSONAtomic(filepath.Join(dir, workflowEdgesFile), state.Edges, 0644)
}

func (s *workflowStore) appendEvent(id string, event map[string]interface{}) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil
	}
	path := filepath.Join(s.dir, id, workflowEventsFile)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (s *workflowStore) writeHandoff(workflowID string, node workflowNode, job *subagentJob, changedFiles []subagentFileChange, resultText string) (workflowNodeHandoff, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return workflowNodeHandoff{}, fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	attempt := node.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	handoff := workflowNodeHandoff{
		WorkflowID:    workflowID,
		NodeID:        node.ID,
		Attempt:       attempt,
		JobID:         node.JobID,
		Status:        node.Status,
		Verdict:       normalizeWorkflowVerdict(node.Verdict),
		Summary:       workflowHandoffSummary(resultText, node.Error),
		ResultPreview: node.ResultPreview,
		Error:         node.Error,
		ChangedFiles:  append([]subagentFileChange{}, changedFiles...),
		ArtifactRefs:  append([]string{}, node.ArtifactRefs...),
		CreatedAt:     time.Now().UTC(),
	}
	if job != nil && strings.TrimSpace(handoff.JobID) == "" {
		handoff.JobID = job.ID
	}
	if handoff.Verdict == "" {
		handoff.Verdict = workflowVerdictForTerminal(node.Status, resultText, node.Error)
	}
	unsigned := handoff
	unsigned.Digest = ""
	data, err := json.Marshal(unsigned)
	if err != nil {
		return workflowNodeHandoff{}, err
	}
	sum := sha256.Sum256(data)
	handoff.Digest = fmt.Sprintf("%x", sum[:])
	path := s.handoffPath(workflowID, node.ID, attempt)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return workflowNodeHandoff{}, err
	}
	if err := fsutil.WriteJSONAtomic(path, handoff, 0644); err != nil {
		return workflowNodeHandoff{}, err
	}
	return handoff, nil
}

func (s *workflowStore) handoffPath(workflowID, nodeID string, attempt int) string {
	name := fmt.Sprintf("%d.json", attempt)
	return filepath.Join(s.dir, workflowID, workflowHandoffsDir, nodeID, name)
}

func workflowHandoffRef(nodeID string, attempt int) string {
	if attempt <= 0 {
		attempt = 1
	}
	return filepath.ToSlash(filepath.Join(workflowHandoffsDir, nodeID, fmt.Sprintf("%d.json", attempt)))
}

func (a *Agent) workflowState(id string) (workflowState, error) {
	state, err := a.workflows.load(id)
	if err != nil {
		return workflowState{}, err
	}
	changed := a.refreshWorkflowNodes(&state)
	edgeChanged, err := a.processWorkflowEdges(&state)
	if err != nil {
		return workflowState{}, err
	}
	if changed || edgeChanged {
		if err := a.workflows.save(state); err != nil {
			return workflowState{}, err
		}
	}
	return state, nil
}

func (a *Agent) startWorkflowReadyNodes(ctx context.Context, id string) (workflowView, error) {
	state, err := a.workflowState(id)
	if err != nil {
		return workflowView{}, err
	}
	started := []string{}
	for i := range state.Nodes {
		node := &state.Nodes[i]
		if node.Status != workflowStatusPending || !workflowDepsCompleted(state.Nodes, node.DependsOn) {
			continue
		}
		if _, ok := a.startWorkflowNode(ctx, &state, node); ok {
			started = append(started, node.ID)
		}
	}
	now := time.Now().UTC()
	state.Summary.UpdatedAt = now
	a.refreshWorkflowStatus(&state)
	if _, err := a.processWorkflowEdges(&state); err != nil {
		return workflowView{}, err
	}
	if err := a.workflows.save(state); err != nil {
		return workflowView{}, err
	}
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{"event": "start", "started": started, "at": now})
	view := workflowViewFromState(state)
	view.Started = started
	return view, nil
}

// startWorkflowNode attempts to start a single workflow node. It mutates
// state.Nodes[i] in place. Returns the started node id and a boolean
// indicating whether the node transitioned from pending to running.
// On failure the node is moved to error state with the failure captured in
// node.Error and a handoff finalized.
func (a *Agent) startWorkflowNode(ctx context.Context, state *workflowState, node *workflowNode) (string, bool) {
	now := time.Now().UTC()
	if node.Status != workflowStatusPending {
		return "", false
	}
	prompt, err := a.workflowNodePrompt(*state, *node)
	if err != nil {
		node.Status = workflowStatusError
		node.Error = err.Error()
		node.FinishedAt = now
		node.UpdatedAt = now
		if handoffErr := a.finalizeWorkflowNodeHandoff(state, node, nil, ""); handoffErr != nil {
			node.Error = handoffErr.Error()
		}
		return node.ID, false
	}
	job, err := a.startDurableSubagentWithContext(ctx, durableSubagentStartRequest{
		Prompt:        prompt,
		AgentType:     node.AgentType,
		WriteScope:    node.WriteScope,
		PreviewJobIDs: a.workflowPreviewJobIDs(*state, *node),
	})
	if err != nil {
		node.Status = workflowStatusError
		node.Error = err.Error()
		node.FinishedAt = now
		node.UpdatedAt = now
		if handoffErr := a.finalizeWorkflowNodeHandoff(state, node, nil, ""); handoffErr != nil {
			node.Error = handoffErr.Error()
		}
		return node.ID, false
	}
	node.Status = workflowStatusRunning
	node.JobID = job.ID
	node.IdentityID = job.Identity.ID
	node.AgentIdentity = job.Identity
	node.UpdatedAt = now
	return node.ID, true
}

func (a *Agent) waitWorkflow(ctx context.Context, id, mode string, timeoutMS int) (workflowView, error) {
	state, err := a.workflowState(id)
	if err != nil {
		return workflowView{}, err
	}
	var jobIDs []string
	for _, node := range state.Nodes {
		if node.Status == workflowStatusRunning && strings.TrimSpace(node.JobID) != "" {
			jobIDs = append(jobIDs, node.JobID)
		}
	}
	if len(jobIDs) == 0 {
		view := workflowViewFromState(state)
		return view, nil
	}
	waitView, err := waitSubagents(ctx, a, subagentWaitRequest{JobIDs: jobIDs, Mode: mode, TimeoutMS: timeoutMS})
	if err != nil {
		return workflowView{}, err
	}
	state, err = a.workflowState(id)
	if err != nil {
		return workflowView{}, err
	}
	view := workflowViewFromState(state)
	view.Wait = &waitView
	return view, nil
}

func (a *Agent) appendWorkflowNodes(id string, inputs []workflowNodeInput, edgeInputs []workflowEdgeInput, idempotencyKey, parentNodeID, reason string) (workflowView, error) {
	state, err := a.workflowState(id)
	if err != nil {
		return workflowView{}, err
	}
	key := strings.TrimSpace(idempotencyKey)
	if key != "" && len(state.Summary.AppendKeys[key]) > 0 {
		view := workflowViewFromState(state)
		view.Appended = append([]string{}, state.Summary.AppendKeys[key]...)
		return view, nil
	}
	if len(inputs) == 0 && len(edgeInputs) == 0 {
		return workflowView{}, fmt.Errorf("missing workflow nodes or edges")
	}
	if len(state.Nodes)+len(inputs) > workflowMaxNodes {
		return workflowView{}, fmt.Errorf("workflow node limit is %d", workflowMaxNodes)
	}
	now := time.Now().UTC()
	existing := make(map[string]struct{}, len(state.Nodes))
	for _, node := range state.Nodes {
		existing[node.ID] = struct{}{}
	}
	newNodes, err := workflowNodesFromInputs(inputs, existing, now)
	if err != nil {
		return workflowView{}, err
	}
	combined := append(append([]workflowNode{}, state.Nodes...), newNodes...)
	if err := validateWorkflowDeps(combined); err != nil {
		return workflowView{}, err
	}
	newEdges, err := workflowEdgesFromInputs(edgeInputs)
	if err != nil {
		return workflowView{}, err
	}
	combinedEdges := append(append([]workflowEdge{}, state.Edges...), newEdges...)
	if err := validateWorkflowEdges(combinedEdges, combined); err != nil {
		return workflowView{}, err
	}
	state.Nodes = combined
	state.Edges = combinedEdges
	state.Summary.UpdatedAt = now
	if key != "" {
		if state.Summary.AppendKeys == nil {
			state.Summary.AppendKeys = make(map[string][]string)
		}
		state.Summary.AppendKeys[key] = workflowNodeIDs(newNodes)
	}
	a.refreshWorkflowStatus(&state)
	if _, err := a.processWorkflowEdges(&state); err != nil {
		return workflowView{}, err
	}
	if err := a.workflows.save(state); err != nil {
		return workflowView{}, err
	}
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{
		"event":           "append_node",
		"nodes":           workflowNodeIDs(newNodes),
		"edges":           workflowEdgeIDs(newEdges),
		"idempotency_key": key,
		"parent_node_id":  strings.TrimSpace(parentNodeID),
		"reason":          strings.TrimSpace(reason),
		"at":              now,
	})
	view := workflowViewFromState(state)
	view.Appended = workflowNodeIDs(newNodes)
	return view, nil
}

func (a *Agent) cancelWorkflowNode(ctx context.Context, id, nodeID string) (workflowState, error) {
	state, err := a.workflowState(id)
	if err != nil {
		return workflowState{}, err
	}
	now := time.Now().UTC()
	for i := range state.Nodes {
		node := &state.Nodes[i]
		if strings.TrimSpace(nodeID) != "" && node.ID != strings.TrimSpace(nodeID) {
			continue
		}
		switch node.Status {
		case workflowStatusPending:
			node.Status = workflowStatusCanceled
			node.FinishedAt = now
		case workflowStatusRunning:
			if node.JobID != "" {
				_, _ = a.subagentJobs.Cancel(node.JobID)
				subagentEventTargetFromContext(ctx).emit(nil, "canceled", "Workflow node canceled.", "", "", "", "")
			}
			node.Status = workflowStatusCanceled
			node.FinishedAt = now
		}
		node.UpdatedAt = now
	}
	state.Summary.UpdatedAt = now
	a.refreshWorkflowStatus(&state)
	if _, err := a.processWorkflowEdges(&state); err != nil {
		return workflowState{}, err
	}
	return state, a.workflows.save(state)
}

func (a *Agent) completeWorkflowNode(id, nodeID, result string) (workflowState, error) {
	state, err := a.workflowState(id)
	if err != nil {
		return workflowState{}, err
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return workflowState{}, fmt.Errorf("missing node_id")
	}
	now := time.Now().UTC()
	found := false
	for i := range state.Nodes {
		if state.Nodes[i].ID != nodeID {
			continue
		}
		state.Nodes[i].Status = workflowStatusCompleted
		state.Nodes[i].Attempt = nextWorkflowAttempt(state.Nodes[i])
		state.Nodes[i].ResultPreview = previewSubagentResultForModel(result)
		state.Nodes[i].Error = ""
		state.Nodes[i].UpdatedAt = now
		state.Nodes[i].FinishedAt = now
		if err := a.finalizeWorkflowNodeHandoff(&state, &state.Nodes[i], nil, result); err != nil {
			return workflowState{}, err
		}
		found = true
	}
	if !found {
		return workflowState{}, fmt.Errorf("workflow node not found: %s", nodeID)
	}
	state.Summary.UpdatedAt = now
	a.refreshWorkflowStatus(&state)
	if _, err := a.processWorkflowEdges(&state); err != nil {
		return workflowState{}, err
	}
	return state, a.workflows.save(state)
}

func (a *Agent) refreshWorkflowNodes(state *workflowState) bool {
	changed := false
	now := time.Now().UTC()
	for i := range state.Nodes {
		node := &state.Nodes[i]
		if node.Status != workflowStatusRunning || strings.TrimSpace(node.JobID) == "" {
			continue
		}
		job, err := a.subagentJobs.Get(node.JobID)
		if err != nil {
			continue
		}
		if !subagentStatusTerminal(job.Status) {
			continue
		}
		switch job.Status {
		case subagentStatusCompleted:
			node.Status = workflowStatusCompleted
		case subagentStatusCanceled, subagentStatusInterrupted:
			node.Status = workflowStatusCanceled
		default:
			node.Status = workflowStatusError
		}
		node.Attempt = nextWorkflowAttempt(*node)
		node.ResultPreview = previewSubagentResultForModel(job.Result)
		node.Error = job.Error
		node.UpdatedAt = now
		node.FinishedAt = now
		if err := a.finalizeWorkflowNodeHandoff(state, node, job, job.Result); err != nil {
			node.Error = err.Error()
			node.Status = workflowStatusError
		}
		changed = true
	}
	if changed {
		state.Summary.UpdatedAt = now
		a.refreshWorkflowStatus(state)
	}
	return changed
}

func (a *Agent) refreshWorkflowStatus(state *workflowState) {
	if len(state.Nodes) == 0 {
		state.Summary.Status = workflowStatusPending
		return
	}
	completed := 0
	failed := 0
	running := 0
	canceled := 0
	for _, node := range state.Nodes {
		switch node.Status {
		case workflowStatusCompleted:
			completed++
		case workflowStatusError:
			failed++
		case workflowStatusRunning:
			running++
		case workflowStatusCanceled:
			canceled++
		}
	}
	switch {
	case failed > 0:
		state.Summary.Status = workflowStatusError
	case completed == len(state.Nodes):
		state.Summary.Status = workflowStatusCompleted
	case canceled == len(state.Nodes):
		state.Summary.Status = workflowStatusCanceled
	case running > 0 || completed > 0:
		state.Summary.Status = workflowStatusRunning
	default:
		state.Summary.Status = workflowStatusPending
	}
}

func workflowViewFromState(state workflowState) workflowView {
	view := workflowView{
		WorkflowID: state.Summary.ID,
		Status:     state.Summary.Status,
		Total:      len(state.Nodes),
		Nodes:      workflowNodeViews(state.Nodes),
		Edges:      append([]workflowEdge{}, state.Edges...),
	}
	for _, node := range state.Nodes {
		switch node.Status {
		case workflowStatusCompleted:
			view.Completed++
		case workflowStatusRunning:
			view.Running++
		case workflowStatusError:
			view.Failed++
		case workflowStatusPending:
			view.Pending++
		}
	}
	return view
}

func workflowNodeIDs(nodes []workflowNode) []string {
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node.ID)
	}
	return out
}

func workflowEdgeIDs(edges []workflowEdge) []string {
	out := make([]string, 0, len(edges))
	for _, edge := range edges {
		out = append(out, edge.ID)
	}
	return out
}

func workflowNodeViews(nodes []workflowNode) []workflowNodeView {
	out := make([]workflowNodeView, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, workflowNodeView{
			ID:              node.ID,
			IdentityID:      firstNonEmpty(node.IdentityID, node.AgentIdentity.ID),
			AgentIdentity:   node.AgentIdentity,
			Kind:            normalizeWorkflowNodeKind(node.Kind, node.ID),
			NodeType:        normalizeAgentGraphNodeType(node.Kind),
			Title:           node.Title,
			DependsOn:       append([]string{}, node.DependsOn...),
			HandoffPolicy:   node.HandoffPolicy,
			HandoffFrom:     append([]string{}, node.HandoffFrom...),
			HandoffMaxBytes: node.HandoffMaxBytes,
			PreviewMerge:    node.PreviewMerge,
			Status:          node.Status,
			AgentType:       node.AgentType,
			WriteScope:      append([]string{}, node.WriteScope...),
			JobID:           node.JobID,
			Attempt:         node.Attempt,
			HandoffRef:      node.HandoffRef,
			HandoffDigest:   node.HandoffDigest,
			Verdict:         node.Verdict,
			ArtifactRefs:    append([]string{}, node.ArtifactRefs...),
			ResultPreview:   node.ResultPreview,
			Error:           node.Error,
			CreatedAt:       node.CreatedAt,
			UpdatedAt:       node.UpdatedAt,
			FinishedAt:      node.FinishedAt,
		})
	}
	return out
}

func (a *Agent) processWorkflowEdges(state *workflowState) (bool, error) {
	if state == nil || len(state.Edges) == 0 {
		return false, nil
	}
	changed := false
	for _, node := range append([]workflowNode{}, state.Nodes...) {
		if !workflowNodeTerminal(node.Status) {
			continue
		}
		for _, edge := range state.Edges {
			if !workflowEdgeMatchesSource(edge, node) {
				continue
			}
			processedKey := workflowProcessedEdgeKey(edge.ID, node.ID)
			if state.Summary.ProcessedEdges != nil && state.Summary.ProcessedEdges[processedKey] {
				continue
			}
			if state.Summary.ProcessedEdges == nil {
				state.Summary.ProcessedEdges = make(map[string]bool)
			}
			state.Summary.ProcessedEdges[processedKey] = true
			changed = true
			if !workflowEdgeConditionMatches(edge.When, node) {
				continue
			}
			iterationKey := strings.TrimSpace(edge.IterationKey)
			if iterationKey == "" {
				iterationKey = edge.ID
			}
			if state.Summary.EdgeIterations == nil {
				state.Summary.EdgeIterations = make(map[string]int)
			}
			current := state.Summary.EdgeIterations[iterationKey]
			maxIterations := normalizeWorkflowEdgeMaxIterations(edge.MaxIterations)
			if current >= maxIterations {
				markWorkflowNodeError(state, node.ID, fmt.Sprintf("workflow edge %s reached iteration cap %d", edge.ID, maxIterations))
				state.Summary.Status = workflowStatusError
				state.Summary.UpdatedAt = time.Now().UTC()
				_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{
					"event":          "edge_iteration_cap",
					"edge_id":        edge.ID,
					"source_node_id": node.ID,
					"iteration_key":  iterationKey,
					"max_iterations": maxIterations,
					"at":             state.Summary.UpdatedAt,
				})
				continue
			}
			iteration := current + 1
			input := expandWorkflowEdgeAppend(edge.Append, node, iteration)
			if strings.TrimSpace(input.ID) == "" {
				input.ID = fmt.Sprintf("%s_%d", node.ID, iteration)
			}
			newNodes, err := a.appendWorkflowNodesFromEdge(state, []workflowNodeInput{input})
			if err != nil {
				return changed, err
			}
			state.Summary.EdgeIterations[iterationKey] = iteration
			changed = true
			_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{
				"event":          "edge_append",
				"edge_id":        edge.ID,
				"source_node_id": node.ID,
				"iteration_key":  iterationKey,
				"iteration":      iteration,
				"nodes":          workflowNodeIDs(newNodes),
				"at":             time.Now().UTC(),
			})
		}
	}
	if changed {
		state.Summary.UpdatedAt = time.Now().UTC()
		if state.Summary.Status != workflowStatusError {
			a.refreshWorkflowStatus(state)
		}
	}
	return changed, nil
}

func markWorkflowNodeError(state *workflowState, nodeID, message string) {
	if state == nil {
		return
	}
	now := time.Now().UTC()
	for i := range state.Nodes {
		if state.Nodes[i].ID != nodeID {
			continue
		}
		state.Nodes[i].Status = workflowStatusError
		state.Nodes[i].Error = strings.TrimSpace(message)
		state.Nodes[i].UpdatedAt = now
		if state.Nodes[i].FinishedAt.IsZero() {
			state.Nodes[i].FinishedAt = now
		}
		return
	}
}

func (a *Agent) appendWorkflowNodesFromEdge(state *workflowState, inputs []workflowNodeInput) ([]workflowNode, error) {
	if state == nil {
		return nil, fmt.Errorf("missing workflow state")
	}
	if len(state.Nodes)+len(inputs) > workflowMaxNodes {
		return nil, fmt.Errorf("workflow node limit is %d", workflowMaxNodes)
	}
	now := time.Now().UTC()
	existing := make(map[string]struct{}, len(state.Nodes))
	for _, node := range state.Nodes {
		existing[node.ID] = struct{}{}
	}
	newNodes, err := workflowNodesFromInputs(inputs, existing, now)
	if err != nil {
		return nil, err
	}
	combined := append(append([]workflowNode{}, state.Nodes...), newNodes...)
	if err := validateWorkflowDeps(combined); err != nil {
		return nil, err
	}
	if err := validateWorkflowEdges(state.Edges, combined); err != nil {
		return nil, err
	}
	state.Nodes = combined
	return newNodes, nil
}

func workflowNodeTerminal(status string) bool {
	switch status {
	case workflowStatusCompleted, workflowStatusCanceled, workflowStatusError:
		return true
	default:
		return false
	}
}

func workflowEdgeMatchesSource(edge workflowEdge, node workflowNode) bool {
	if edge.From != "" && edge.From != node.ID {
		return false
	}
	if edge.FromKind != "" && edge.FromKind != normalizeWorkflowNodeKind(node.Kind, node.ID) {
		return false
	}
	return edge.From != "" || edge.FromKind != ""
}

func workflowEdgeConditionMatches(condition workflowEdgeCondition, node workflowNode) bool {
	if condition.Status != "" && condition.Status != node.Status {
		return false
	}
	if condition.Verdict != "" && condition.Verdict != normalizeWorkflowVerdict(node.Verdict) {
		return false
	}
	return true
}

func workflowProcessedEdgeKey(edgeID, nodeID string) string {
	return strings.TrimSpace(edgeID) + "|" + strings.TrimSpace(nodeID)
}

func expandWorkflowEdgeAppend(input workflowNodeInput, source workflowNode, iteration int) workflowNodeInput {
	replace := func(text string) string {
		text = strings.ReplaceAll(text, "{source}", source.ID)
		text = strings.ReplaceAll(text, "{iteration}", fmt.Sprintf("%d", iteration))
		return text
	}
	out := input
	out.ID = replace(out.ID)
	out.Kind = replace(out.Kind)
	out.Title = replace(out.Title)
	out.Prompt = replace(out.Prompt)
	out.AgentType = replace(out.AgentType)
	for i := range out.DependsOn {
		out.DependsOn[i] = replace(out.DependsOn[i])
	}
	for i := range out.HandoffFrom {
		out.HandoffFrom[i] = replace(out.HandoffFrom[i])
	}
	for i := range out.WriteScope {
		out.WriteScope[i] = replace(out.WriteScope[i])
	}
	return out
}

func workflowDepsCompleted(nodes []workflowNode, deps []string) bool {
	if len(deps) == 0 {
		return true
	}
	byID := make(map[string]string, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node.Status
	}
	for _, dep := range deps {
		if byID[strings.TrimSpace(dep)] != workflowStatusCompleted {
			return false
		}
	}
	return true
}

func validateWorkflowDeps(nodes []workflowNode) error {
	byID := make(map[string]workflowNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	for _, node := range nodes {
		for _, dep := range append(append([]string{}, node.DependsOn...), node.HandoffFrom...) {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if dep == node.ID {
				return fmt.Errorf("workflow node %s depends on itself", node.ID)
			}
			if _, ok := byID[dep]; !ok {
				return fmt.Errorf("workflow node %s depends on unknown node %s", node.ID, dep)
			}
		}
	}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visited[id] {
			return nil
		}
		if visiting[id] {
			return fmt.Errorf("workflow dependency cycle includes %s", id)
		}
		visiting[id] = true
		for _, dep := range byID[id].DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for _, node := range nodes {
		if err := visit(node.ID); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowEdges(edges []workflowEdge, nodes []workflowNode) error {
	if len(edges) == 0 {
		return nil
	}
	byID := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(edges))
	for _, edge := range edges {
		if strings.TrimSpace(edge.ID) == "" {
			return fmt.Errorf("workflow edge missing id")
		}
		if _, ok := seen[edge.ID]; ok {
			return fmt.Errorf("duplicate workflow edge id %q", edge.ID)
		}
		seen[edge.ID] = struct{}{}
		if edge.From != "" {
			if _, ok := byID[edge.From]; !ok {
				return fmt.Errorf("workflow edge %s references unknown from node %s", edge.ID, edge.From)
			}
		}
	}
	return nil
}

func validateWorkflowID(id string) error {
	if id == "" {
		return fmt.Errorf("missing workflow_id")
	}
	if id == "." || strings.Contains(id, "..") {
		return fmt.Errorf("invalid workflow_id %q", id)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return fmt.Errorf("invalid workflow_id %q", id)
		}
	}
	return nil
}

func (a *Agent) workflowNodePrompt(state workflowState, node workflowNode) (string, error) {
	var builder strings.Builder
	if node.Title != "" {
		builder.WriteString(node.Title)
		builder.WriteString("\n\n")
	}
	builder.WriteString(node.Prompt)
	if len(node.DependsOn) > 0 {
		builder.WriteString("\n\nDependencies completed: ")
		builder.WriteString(strings.Join(node.DependsOn, ", "))
	}
	handoffText, err := a.workflowDependencyHandoffText(state, node)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(handoffText) != "" {
		builder.WriteString("\n\nDependency handoffs:\n")
		builder.WriteString(handoffText)
	}
	return builder.String(), nil
}

func (a *Agent) workflowDependencyHandoffText(state workflowState, node workflowNode) (string, error) {
	policy := normalizeWorkflowHandoffPolicy(node.HandoffPolicy, len(node.DependsOn) > 0)
	if policy == workflowHandoffPolicyNone {
		return "", nil
	}
	depIDs := normalizeWorkflowHandoffFrom(node.HandoffFrom, node.DependsOn)
	if len(depIDs) == 0 {
		return "", nil
	}
	byID := make(map[string]workflowNode, len(state.Nodes))
	for _, item := range state.Nodes {
		byID[item.ID] = item
	}
	chunks := make([]string, 0, len(depIDs))
	for _, depID := range depIDs {
		dep, ok := byID[depID]
		if !ok {
			return "", fmt.Errorf("workflow node %s references unknown handoff dependency %s", node.ID, depID)
		}
		handoff, err := a.workflows.loadHandoff(state.Summary.ID, dep)
		if err != nil {
			return "", err
		}
		chunk := formatWorkflowDependencyHandoff(dep, handoff, policy)
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
	}
	// Phase 2.3: assemble under the byte ceiling with the shared truncation
	// path (same behavior as merge-point synthesis).
	return assembleTruncatedHandoffs(chunks, normalizeWorkflowHandoffMaxBytes(node.HandoffMaxBytes)), nil
}

func (s *workflowStore) loadHandoff(workflowID string, node workflowNode) (workflowNodeHandoff, error) {
	if strings.TrimSpace(node.HandoffRef) == "" {
		return workflowNodeHandoff{}, fmt.Errorf("workflow dependency %s has no handoff artifact", node.ID)
	}
	path := filepath.Join(s.dir, workflowID, filepath.FromSlash(node.HandoffRef))
	var handoff workflowNodeHandoff
	if err := readJSONFile(path, &handoff); err != nil {
		return workflowNodeHandoff{}, fmt.Errorf("read workflow handoff %s: %w", node.HandoffRef, err)
	}
	return handoff, nil
}

func formatWorkflowDependencyHandoff(node workflowNode, handoff workflowNodeHandoff, policy string) string {
	var builder strings.Builder
	builder.WriteString("- node: ")
	builder.WriteString(node.ID)
	builder.WriteString("\n  status: ")
	builder.WriteString(handoff.Status)
	builder.WriteString("\n  verdict: ")
	builder.WriteString(handoff.Verdict)
	if handoff.Summary != "" {
		builder.WriteString("\n  summary: ")
		builder.WriteString(strings.Join(strings.Fields(handoff.Summary), " "))
	}
	if len(handoff.ChangedFiles) > 0 {
		builder.WriteString("\n  changed_files:")
		for _, change := range handoff.ChangedFiles {
			builder.WriteString("\n    - ")
			builder.WriteString(change.Path)
			if change.Status != "" {
				builder.WriteString(" (")
				builder.WriteString(change.Status)
				builder.WriteString(")")
			}
		}
	}
	if policy == workflowHandoffPolicySummaryArtifacts || policy == workflowHandoffPolicySelected {
		if handoff.Digest != "" {
			builder.WriteString("\n  digest: ")
			builder.WriteString(handoff.Digest)
		}
		if len(handoff.ArtifactRefs) > 0 {
			builder.WriteString("\n  artifact_refs:")
			for _, ref := range handoff.ArtifactRefs {
				builder.WriteString("\n    - ")
				builder.WriteString(ref)
			}
		}
	}
	builder.WriteString("\n")
	return builder.String()
}

func (a *Agent) workflowPreviewJobIDs(state workflowState, node workflowNode) []string {
	if !node.PreviewMerge {
		return nil
	}
	depIDs := normalizeWorkflowHandoffFrom(node.HandoffFrom, node.DependsOn)
	if len(depIDs) == 0 {
		return nil
	}
	byID := make(map[string]workflowNode, len(state.Nodes))
	for _, item := range state.Nodes {
		byID[item.ID] = item
	}
	var out []string
	for _, depID := range depIDs {
		dep := byID[depID]
		if dep.Status == workflowStatusCompleted && strings.TrimSpace(dep.JobID) != "" && len(dep.WriteScope) > 0 {
			out = append(out, dep.JobID)
		}
	}
	return normalizeWorkflowStrings(out)
}

func (a *Agent) finalizeWorkflowNodeHandoff(state *workflowState, node *workflowNode, job *subagentJob, resultText string) error {
	if state == nil || node == nil {
		return nil
	}
	if strings.TrimSpace(node.HandoffRef) != "" {
		return nil
	}
	if node.Attempt <= 0 {
		node.Attempt = 1
	}
	node.Verdict = workflowVerdictForTerminal(node.Status, resultText, node.Error)
	changes := []subagentFileChange{}
	if job != nil && len(job.WriteScope) > 0 {
		if review, err := reviewSubagentJob(job); err == nil {
			changes = review.Changes
		}
	}
	handoff, err := a.workflows.writeHandoff(state.Summary.ID, *node, job, changes, resultText)
	if err != nil {
		return err
	}
	node.HandoffRef = workflowHandoffRef(node.ID, node.Attempt)
	node.HandoffDigest = handoff.Digest
	node.Verdict = handoff.Verdict
	node.ArtifactRefs = append([]string{}, handoff.ArtifactRefs...)
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{"event": "handoff_written", "node_id": node.ID, "attempt": node.Attempt, "handoff_ref": node.HandoffRef, "at": handoff.CreatedAt})
	return nil
}

func nextWorkflowAttempt(node workflowNode) int {
	if node.Attempt > 0 {
		return node.Attempt
	}
	return 1
}

func workflowVerdictForTerminal(status, resultText, errorText string) string {
	if verdict := parseWorkflowVerdict(resultText); verdict != "" {
		return verdict
	}
	if verdict := parseWorkflowVerdict(errorText); verdict != "" {
		return verdict
	}
	switch status {
	case workflowStatusCompleted:
		return workflowVerdictPass
	case workflowStatusCanceled:
		return workflowVerdictBlocked
	default:
		return workflowVerdictFail
	}
}

func parseWorkflowVerdict(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var payload struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err == nil {
		if verdict := normalizeWorkflowVerdict(payload.Verdict); verdict != "" {
			return verdict
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < len("verdict:") || !strings.EqualFold(line[:len("verdict:")], "verdict:") {
			continue
		}
		return normalizeWorkflowVerdict(strings.TrimSpace(line[len("verdict:"):]))
	}
	return ""
}

func normalizeWorkflowVerdict(verdict string) string {
	verdict = strings.ToLower(strings.TrimSpace(verdict))
	verdict = strings.Trim(verdict, " .,:;\"'")
	verdict = strings.ReplaceAll(verdict, "-", "_")
	switch verdict {
	case workflowVerdictPass, "passed", "success", "succeeded", "ok":
		return workflowVerdictPass
	case workflowVerdictFail, "failed", "failure", "error":
		return workflowVerdictFail
	case workflowVerdictBlocked, "blocker":
		return workflowVerdictBlocked
	case workflowVerdictNeedsFix, "needsfix", "fix", "needs_change", "needs_changes":
		return workflowVerdictNeedsFix
	default:
		return ""
	}
}

func workflowHandoffSummary(resultText, errorText string) string {
	text := strings.TrimSpace(resultText)
	if text == "" {
		text = strings.TrimSpace(errorText)
	}
	preview := previewSubagentResultForModel(text)
	// Phase 2.3: cap the stored summary at a token budget, not just a char
	// limit, so CJK-heavy results do not blow the child's context.
	return truncateTextToTokenBudget(preview, workflowHandoffSummaryTokenBudget)
}

func normalizeWorkflowNodeKind(kind, fallback string) string {
	kind = strings.TrimSpace(kind)
	if kind != "" {
		return kind
	}
	return strings.TrimSpace(fallback)
}

func normalizeWorkflowHandoffPolicy(policy string, hasDeps bool) string {
	policy = strings.ToLower(strings.TrimSpace(policy))
	switch policy {
	case workflowHandoffPolicyNone, workflowHandoffPolicySummary, workflowHandoffPolicySummaryArtifacts, workflowHandoffPolicySelected:
		return policy
	default:
		if hasDeps {
			return workflowHandoffPolicySummary
		}
		return workflowHandoffPolicyNone
	}
}

func normalizeWorkflowHandoffFrom(handoffFrom, deps []string) []string {
	out := normalizeWorkflowStrings(handoffFrom)
	if len(out) > 0 {
		return out
	}
	return normalizeWorkflowStrings(deps)
}

func normalizeWorkflowHandoffMaxBytes(value int) int {
	if value <= 0 {
		return workflowDefaultHandoffMaxBytes
	}
	if value > workflowMaxHandoffMaxBytes {
		return workflowMaxHandoffMaxBytes
	}
	return value
}

func normalizeWorkflowPreviewMerge(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

func normalizeWorkflowEdgeMaxIterations(value int) int {
	if value <= 0 {
		return 3
	}
	return value
}

func normalizeWorkflowStrings(input []string) []string {
	out := make([]string, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, item := range input {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func readWorkflowEvents(path string) []map[string]interface{} {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	var out []map[string]interface{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var item map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &item); err == nil {
			out = append(out, item)
		}
	}
	return out
}
