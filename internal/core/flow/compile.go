package flow

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// Compiled is the engine-independent compile output of a Flow Definition.
// It mirrors the durable engine's node/edge inputs so the agent layer can map
// it 1:1 onto workflowNodeInput/workflowEdgeInput (design doc §4/§5).
type Compiled struct {
	FlowID     string         `json:"flow_id"`
	Version    string         `json:"version"`
	TimeoutSec int            `json:"timeout_sec,omitempty"`
	Inputs     []VarDef       `json:"inputs,omitempty"`
	Outputs    []VarDef       `json:"outputs,omitempty"`
	Nodes      []CompiledNode `json:"nodes"`
	Edges      []CompiledEdge `json:"edges"`
	Digest     string         `json:"digest"`
	// Network is the Flow-level outbound network policy for function and
	// service nodes; carried onto the durable workflow for runtime enforcement.
	Network *NetworkPolicy `json:"network,omitempty"`
}

// CompiledNode is one engine node after lowering. step/llm/decision compile
// to static nodes; branch compiles to a synchronous gateway node (the
// scheduler evaluates its cases against the dependency source and writes the
// matched case name into outputs.choice). Nodes referenced only by condition
// edges or branch cases are NOT declared statically — they become Append
// templates carried by CompiledEdge (avoid double creation).
type CompiledNode struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Title       string          `json:"title,omitempty"`
	Prompt      string          `json:"prompt,omitempty"`
	AgentType   string          `json:"agent_type,omitempty"`
	AgentRef    string          `json:"agent_ref,omitempty"` // template/biz-key id (P1.4)
	WriteScope  []string        `json:"write_scope,omitempty"`
	TimeoutSec  int             `json:"timeout_sec,omitempty"`
	DependsOn   []string        `json:"depends_on,omitempty"`
	HandoffFrom []string        `json:"handoff_from,omitempty"`
	Retry       *RetryPolicy    `json:"retry,omitempty"`
	Decision    *DecisionSpec   `json:"decision,omitempty"`
	Human       *HumanSpec      `json:"human,omitempty"`
	Branch      *CompiledBranch `json:"branch,omitempty"`
	Function    *FunctionSpec   `json:"function,omitempty"`
	Service     *ServiceSpec    `json:"service,omitempty"`
	// Outputs carries the node's declared typed outputs (P2.3) so the engine
	// can resolve {{nodes.<id>.outputs.<field>}} references at run time.
	Outputs []VarDef `json:"outputs,omitempty"`
	// PreScript / PostScript are optional bash scripts run before/after the
	// node's main work (E3b).
	PreScript  string `json:"pre_script,omitempty"`
	PostScript string `json:"post_script,omitempty"`
}

// CompiledBranch is the lowered branch spec. The engine gateway evaluates
// cases in order against the Source node's outputs; the first hit's route
// name (case.Name, else case.To; default = "default") is written to
// outputs.choice. Routing to the target template happens through CompiledEdge
// with when.choice, so mutual exclusion is structural.
type CompiledBranch struct {
	Source    string               `json:"source"` // dependency source node ID (F1a: single source)
	Cases     []CompiledBranchCase `json:"cases"`
	DefaultTo string               `json:"default_to"`
}

// CompiledBranchCase is one output port (condition + route name).
type CompiledBranchCase struct {
	Name      string    `json:"name,omitempty"`
	To        string    `json:"to"`
	Condition Condition `json:"condition"`
}

// branchDefaultRoute is the reserved route name for the default target.
const branchDefaultRoute = "default"

// CompiledEdge is a control_flow edge: when From reaches When, the Append
// template node is created and scheduled. data_dependency/handoff edges are
// folded into CompiledNode.DependsOn/HandoffFrom and do not appear here.
type CompiledEdge struct {
	ID            string       `json:"id,omitempty"`
	From          string       `json:"from"`
	FromPrefix    string       `json:"from_prefix,omitempty"`
	When          Condition    `json:"when"`
	Append        CompiledNode `json:"append"`
	MaxIterations int          `json:"max_iterations,omitempty"`
	IterationKey  string       `json:"iteration_key,omitempty"`
}

// Compile validates and lowers a Flow Definition to Compiled. Rules (design
// doc §5 mapping; semantics recorded in §14):
//
//   - step/llm/decision nodes            -> static CompiledNode
//   - branch nodes                       -> static CompiledNode (synchronous
//     gateway; its cases carry target templates)
//   - data_dependency edges              -> DependsOn on the static target
//   - handoff edges                      -> DependsOn + HandoffFrom
//   - condition edges                    -> CompiledEdge; the To node becomes an
//     Append template and is NOT statically declared
//   - branch case.To / default_to nodes  -> templates inside the branch spec,
//     NOT statically declared
//   - loop nodes                         -> bounded control-flow templates
//
// A node referenced only as an append target (condition edge or branch case)
// is auto-demoted to a template even if declared in Nodes. A node with both a
// static in-edge and an append-target reference is rejected (mixed use is not
// supported yet).
func Compile(d *Definition) (*Compiled, error) {
	if err := Validate(d); err != nil {
		return nil, err
	}
	byID := make(map[string]Node, len(d.Nodes))
	for _, n := range d.Nodes {
		byID[n.ID] = n
	}

	appendTargets, staticIn := collectCompileTargets(d)
	nodes, nodesByID, err := compileStaticNodes(d, byID, appendTargets, staticIn)
	if err != nil {
		return nil, err
	}
	if err := foldStaticEdges(d.Edges, byID, nodesByID); err != nil {
		return nil, err
	}
	syncCompiledNodes(nodes, nodesByID)

	edges, err := compileConditionEdges(d, byID)
	if err != nil {
		return nil, err
	}
	branchEdges, err := compileBranchEdges(d, byID)
	if err != nil {
		return nil, err
	}
	edges = append(edges, branchEdges...)
	loopEdges, err := compileLoopEdges(d, byID)
	if err != nil {
		return nil, err
	}
	edges = append(edges, loopEdges...)
	if err := validateCompiledEdgeIDs(edges); err != nil {
		return nil, err
	}

	sortNodes(nodes)
	c := &Compiled{
		FlowID:     d.FlowID,
		Version:    d.Version,
		TimeoutSec: d.TimeoutSec,
		Inputs:     append([]VarDef{}, d.Inputs...),
		Outputs:    append([]VarDef{}, d.Outputs...),
		Nodes:      nodes,
		Edges:      edges,
		Network:    d.Network,
	}
	c.Digest = c.computeDigest()
	return c, nil
}

func collectCompileTargets(d *Definition) (map[string]struct{}, map[string][]string) {
	appendTargets := map[string]struct{}{}
	staticIn := map[string][]string{}
	for _, e := range d.Edges {
		et := normalizeEdgeType(e.EdgeType)
		switch et {
		case EdgeCondition:
			appendTargets[e.To] = struct{}{}
		case EdgeDataDependency, EdgeHandoff:
			staticIn[e.To] = append(staticIn[e.To], e.From)
		}
	}
	for _, n := range d.Nodes {
		switch normalizeKind(n.Kind) {
		case KindBranch:
			if n.Branch == nil {
				continue
			}
			for _, c := range n.Branch.Cases {
				appendTargets[c.To] = struct{}{}
			}
			appendTargets[n.Branch.DefaultTo] = struct{}{}
		case KindLoop:
			if n.Loop == nil {
				continue
			}
			for _, id := range n.Loop.Body {
				appendTargets[id] = struct{}{}
			}
			for _, edge := range d.Edges {
				if edge.From == n.ID && normalizeEdgeType(edge.EdgeType) != EdgeCondition {
					appendTargets[edge.To] = struct{}{}
				}
			}
		}
	}
	return appendTargets, staticIn
}

func compileStaticNodes(
	d *Definition,
	byID map[string]Node,
	appendTargets map[string]struct{},
	staticIn map[string][]string,
) ([]CompiledNode, map[string]CompiledNode, error) {
	// Static nodes: everything except branches and append-target-only nodes.
	// A node that is both a static target (has in-edges) and an append target
	// is rejected — F1a does not support mixed use.
	var nodes []CompiledNode
	nodesByID := make(map[string]CompiledNode, len(d.Nodes))
	for _, n := range d.Nodes {
		kind := normalizeKind(n.Kind)
		switch kind {
		case KindBranch:
			// branch gateway node is static (it runs synchronously)
		case KindLoop:
			// loop is compiled to a control_flow append edge below (P1.5);
			// it never becomes a static job node itself.
			continue
		default:
			if _, isTarget := appendTargets[n.ID]; isTarget {
				for _, from := range staticIn[n.ID] {
					// A loop's outgoing edge is lowered to a dynamic exit
					// append below; it does not make the target a static node.
					if normalizeKind(byID[from].Kind) != KindLoop {
						return nil, nil, fmt.Errorf("node %s: mixed use as static target and append target is not supported in F1a", n.ID)
					}
				}
				continue // auto-demoted to append template
			}
		}
		cn := CompiledNode{
			ID:         n.ID,
			Kind:       kind,
			Title:      n.Title,
			Prompt:     n.Prompt,
			AgentType:  n.AgentType,
			AgentRef:   n.AgentRef,
			WriteScope: append([]string{}, n.WriteScope...),
			TimeoutSec: n.TimeoutSec,
			Retry:      n.Retry,
			Decision:   n.Decision,
			Human:      n.Human,
			Function:   n.Function,
			Service:    n.Service,
			Outputs:    append([]VarDef{}, n.Outputs...),
			PreScript:  n.PreScript,
			PostScript: n.PostScript,
		}
		if kind == KindBranch && n.Branch != nil {
			cn.Branch = compileBranch(n.Branch, byID)
			cn.Branch.Source = branchSource(n.ID, d.Edges)
			if cn.Branch.Source == "" {
				return nil, nil, fmt.Errorf("branch %s: requires exactly one data_dependency source", n.ID)
			}
		}
		nodesByID[n.ID] = cn
		nodes = append(nodes, cn)
	}
	return nodes, nodesByID, nil
}

func foldStaticEdges(edges []Edge, byID map[string]Node, nodesByID map[string]CompiledNode) error {
	// Fold static in-edges into DependsOn/HandoffFrom. In-edges of loop nodes
	// are not folded: a loop compiles to a control_flow append edge (its From
	// is derived from the data_dependency source) and never becomes a static
	// job node (P1.5).
	for _, e := range edges {
		et := normalizeEdgeType(e.EdgeType)
		if et == EdgeDataDependency || et == EdgeHandoff {
			if (byID[e.To].ID != "" && normalizeKind(byID[e.To].Kind) == KindLoop) ||
				(byID[e.From].ID != "" && normalizeKind(byID[e.From].Kind) == KindLoop) {
				continue
			}
			cn, ok := nodesByID[e.To]
			if !ok {
				// Static edge into a node that got demoted: reject earlier via
				// staticIn check, but guard here too.
				return fmt.Errorf("edge %s: static edge into append-only node %q", e.ID, e.To)
			}
			cn.DependsOn = appendUnique(cn.DependsOn, e.From)
			if et == EdgeHandoff {
				cn.HandoffFrom = appendUnique(cn.HandoffFrom, e.From)
			}
			nodesByID[e.To] = cn
		}
	}
	return nil
}

func syncCompiledNodes(nodes []CompiledNode, nodesByID map[string]CompiledNode) {
	for i := range nodes {
		if node, ok := nodesByID[nodes[i].ID]; ok {
			nodes[i] = node
		}
	}
}

func compileConditionEdges(d *Definition, byID map[string]Node) ([]CompiledEdge, error) {
	// Condition edges -> CompiledEdge with To as append template.
	var edges []CompiledEdge
	for _, e := range d.Edges {
		if normalizeEdgeType(e.EdgeType) != EdgeCondition {
			continue
		}
		if normalizeKind(byID[e.From].Kind) == KindLoop {
			return nil, fmt.Errorf("loop %s: condition edges from loop nodes are not supported; connect loop exits with data_dependency or handoff edges", e.From)
		}
		toNode, ok := byID[e.To]
		if !ok {
			return nil, fmt.Errorf("edge %s: unknown target %q", e.ID, e.To)
		}
		id := e.ID
		if id == "" {
			id = fmt.Sprintf("edge_%s_to_%s", e.From, e.To)
		}
		edges = append(edges, CompiledEdge{
			ID:            id,
			From:          e.From,
			When:          *e.When,
			Append:        compileTemplate(toNode),
			MaxIterations: e.MaxIterations,
			IterationKey:  e.IterationKey,
		})
	}
	return edges, nil
}

func compileBranchEdges(d *Definition, byID map[string]Node) ([]CompiledEdge, error) {
	// Branch routing edges: one CompiledEdge per case + one default, all
	// sourced from the branch gateway. The gateway writes the matched route
	// name into outputs.choice; each edge matches on when.choice, so exactly
	// one fires (mutual exclusion is structural).
	var edges []CompiledEdge
	for _, n := range d.Nodes {
		if normalizeKind(n.Kind) != KindBranch || n.Branch == nil {
			continue
		}
		for i, c := range n.Branch.Cases {
			target, ok := byID[c.To]
			if !ok {
				return nil, fmt.Errorf("branch %s: case %d unknown target %q", n.ID, i, c.To)
			}
			route := strings.TrimSpace(c.Name)
			if route == "" {
				route = c.To
			}
			edges = append(edges, CompiledEdge{
				ID:     fmt.Sprintf("%s_case_%d", n.ID, i),
				From:   n.ID,
				When:   Condition{Choice: route},
				Append: compileTemplate(target),
			})
		}
		if target, ok := byID[n.Branch.DefaultTo]; ok {
			edges = append(edges, CompiledEdge{
				ID:     fmt.Sprintf("%s_default", n.ID),
				From:   n.ID,
				When:   Condition{Choice: branchDefaultRoute},
				Append: compileTemplate(target),
			})
		}
	}

	return edges, nil
}

func compileLoopEdges(d *Definition, byID map[string]Node) ([]CompiledEdge, error) {
	// Loop compilation currently supports a single-node body. The loop's
	// incoming dependency starts body_1; subsequent body copies continue while
	// exit_when is false. Outgoing data/handoff edges become exit templates
	// and only run after the body satisfies exit_when.
	loopBodyOwners := make(map[string]string)
	loopIterationKeys := make(map[string]string)
	var edges []CompiledEdge
	for _, n := range d.Nodes {
		if normalizeKind(n.Kind) != KindLoop || n.Loop == nil {
			continue
		}
		compiled, err := compileLoop(n, d.Edges, byID, loopBodyOwners, loopIterationKeys)
		if err != nil {
			return nil, err
		}
		edges = append(edges, compiled...)
	}
	return edges, nil
}

func compileLoop(
	n Node,
	definitionEdges []Edge,
	byID map[string]Node,
	loopBodyOwners map[string]string,
	loopIterationKeys map[string]string,
) ([]CompiledEdge, error) {
	if len(n.Loop.Body) != 1 {
		return nil, fmt.Errorf("loop %s: currently requires exactly one body node; multi-node loop bodies are not supported yet", n.ID)
	}
	bodyFirst, ok := byID[n.Loop.Body[0]]
	if !ok {
		return nil, fmt.Errorf("loop %s: body references unknown node %q", n.ID, n.Loop.Body[0])
	}
	bodyKind := normalizeKind(bodyFirst.Kind)
	if bodyKind == KindBranch || bodyKind == KindLoop {
		return nil, fmt.Errorf("loop %s: body node %q has unsupported kind %q", n.ID, bodyFirst.ID, bodyKind)
	}
	if owner, ok := loopBodyOwners[bodyFirst.ID]; ok {
		return nil, fmt.Errorf("loop %s: body node %q is already used by loop %s", n.ID, bodyFirst.ID, owner)
	}
	loopBodyOwners[bodyFirst.ID] = n.ID
	for iteration := 1; iteration <= n.Loop.MaxIterations; iteration++ {
		generatedID := fmt.Sprintf("%s_%d", bodyFirst.ID, iteration)
		if _, collides := byID[generatedID]; collides {
			return nil, fmt.Errorf("loop %s: node id %q conflicts with a generated loop iteration id", n.ID, generatedID)
		}
	}
	from := branchSource(n.ID, definitionEdges)
	if from == "" {
		return nil, fmt.Errorf("loop %s: requires exactly one data_dependency source", n.ID)
	}
	// Continue iterating while the exit condition is NOT met. Not is a
	// full negation of the structured predicate (P1.5).
	exitWhen := n.Loop.ExitWhen
	iterationKey := strings.TrimSpace(n.Loop.IterationKey)
	if iterationKey == "" {
		iterationKey = n.ID
	}
	if owner, ok := loopIterationKeys[iterationKey]; ok {
		return nil, fmt.Errorf("loop %s: iteration_key %q is already used by loop %s", n.ID, iterationKey, owner)
	}
	loopIterationKeys[iterationKey] = n.ID
	bodyTemplate := compileTemplate(bodyFirst)
	bodyTemplate.ID = bodyFirst.ID + "_{iteration}"
	bodyTemplate.DependsOn = []string{"{source}"}
	edges := []CompiledEdge{{
		ID:            fmt.Sprintf("%s_entry", n.ID),
		From:          from,
		When:          Condition{Status: "completed"},
		Append:        bodyTemplate,
		MaxIterations: n.Loop.MaxIterations,
		IterationKey:  iterationKey,
	}, {
		ID:            fmt.Sprintf("%s_continue", n.ID),
		FromPrefix:    bodyFirst.ID + "_",
		When:          Condition{Not: &exitWhen},
		Append:        bodyTemplate,
		MaxIterations: n.Loop.MaxIterations,
		IterationKey:  iterationKey,
	}}
	exits, err := compileLoopExitEdges(n, definitionEdges, bodyFirst, exitWhen, byID)
	if err != nil {
		return nil, err
	}
	return append(edges, exits...), nil
}

func compileLoopExitEdges(n Node, definitionEdges []Edge, body Node, exitWhen Condition, byID map[string]Node) ([]CompiledEdge, error) {
	var edges []CompiledEdge
	for _, e := range definitionEdges {
		if e.From != n.ID {
			continue
		}
		if normalizeEdgeType(e.EdgeType) == EdgeCondition {
			continue // rejected above
		}
		target, ok := byID[e.To]
		if !ok {
			return nil, fmt.Errorf("loop %s: exit edge references unknown node %q", n.ID, e.To)
		}
		if normalizeKind(target.Kind) == KindBranch || normalizeKind(target.Kind) == KindLoop {
			return nil, fmt.Errorf("loop %s: exit target %q has unsupported kind %q", n.ID, target.ID, target.Kind)
		}
		exitTemplate := compileTemplate(target)
		exitTemplate.DependsOn = []string{"{source}"}
		if normalizeEdgeType(e.EdgeType) == EdgeHandoff {
			exitTemplate.HandoffFrom = []string{"{source}"}
		}
		edgeID := e.ID
		if strings.TrimSpace(edgeID) == "" {
			edgeID = fmt.Sprintf("%s_to_%s", n.ID, e.To)
		}
		edges = append(edges, CompiledEdge{
			ID:            fmt.Sprintf("%s_exit_%s", n.ID, edgeID),
			FromPrefix:    body.ID + "_",
			When:          exitWhen,
			Append:        exitTemplate,
			MaxIterations: 1,
			IterationKey:  fmt.Sprintf("%s_exit_%s", n.ID, edgeID),
		})
	}
	return edges, nil
}

func validateCompiledEdgeIDs(edges []CompiledEdge) error {
	seenEdgeIDs := make(map[string]struct{}, len(edges))
	for _, edge := range edges {
		if _, duplicate := seenEdgeIDs[edge.ID]; duplicate {
			return fmt.Errorf("compiled edge id %q is duplicated", edge.ID)
		}
		seenEdgeIDs[edge.ID] = struct{}{}
	}
	return nil
}

// branchSource returns the single data_dependency source of a branch node.
// F1a requires exactly one (the decision/step node it gates on).
func branchSource(id string, edges []Edge) string {
	var src string
	for _, e := range edges {
		if normalizeEdgeType(e.EdgeType) == EdgeDataDependency && e.To == id {
			if src != "" && src != e.From {
				return ""
			}
			src = e.From
		}
	}
	return src
}

// compileTemplate lowers a Flow node to an append-template CompiledNode.
func compileTemplate(n Node) CompiledNode {
	kind := normalizeKind(n.Kind)
	if kind == KindBranch || kind == KindLoop {
		kind = "" // branch/loop cannot be append targets
	}
	return CompiledNode{
		ID:         n.ID,
		Kind:       kind,
		Title:      n.Title,
		Prompt:     n.Prompt,
		AgentType:  n.AgentType,
		AgentRef:   n.AgentRef,
		WriteScope: append([]string{}, n.WriteScope...),
		TimeoutSec: n.TimeoutSec,
		Retry:      n.Retry,
		Decision:   n.Decision,
		Human:      n.Human,
		Function:   n.Function,
		Service:    n.Service,
		Outputs:    append([]VarDef{}, n.Outputs...),
		PreScript:  n.PreScript,
		PostScript: n.PostScript,
	}
}

// compileBranch lowers a BranchSpec. The engine gateway evaluates cases in
// order against the branch's dependency source and writes the matched route
// name into outputs.choice; routing to targets happens via CompiledEdge with
// when.choice (see Compile), so mutual exclusion is structural.
func compileBranch(b *BranchSpec, byID map[string]Node) *CompiledBranch {
	out := &CompiledBranch{DefaultTo: b.DefaultTo}
	for _, c := range b.Cases {
		route := strings.TrimSpace(c.Name)
		if route == "" {
			route = c.To
		}
		out.Cases = append(out.Cases, CompiledBranchCase{
			Name:      route,
			To:        c.To,
			Condition: c.Condition,
		})
	}
	return out
}

// branchRoute returns the route name for a branch case (Name, else To).
func branchRoute(c BranchCase) string {
	if name := strings.TrimSpace(c.Name); name != "" {
		return name
	}
	return c.To
}

func (c *Compiled) computeDigest() string {
	raw, _ := json.Marshal(struct {
		TimeoutSec int            `json:"timeout_sec,omitempty"`
		Inputs     []VarDef       `json:"inputs,omitempty"`
		Outputs    []VarDef       `json:"outputs,omitempty"`
		Network    *NetworkPolicy `json:"network,omitempty"`
		Nodes      []CompiledNode `json:"nodes"`
		Edges      []CompiledEdge `json:"edges"`
	}{c.TimeoutSec, c.Inputs, c.Outputs, c.Network, c.Nodes, c.Edges})
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:16])
}

func appendUnique(items []string, v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return items
	}
	for _, it := range items {
		if it == v {
			return items
		}
	}
	return append(items, v)
}

// sortNodes gives deterministic output ordering by ID.
func sortNodes(nodes []CompiledNode) {
	for i := 1; i < len(nodes); i++ {
		for j := i; j > 0 && nodes[j-1].ID > nodes[j].ID; j-- {
			nodes[j-1], nodes[j] = nodes[j], nodes[j-1]
		}
	}
}
