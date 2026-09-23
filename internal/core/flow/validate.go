package flow

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ValidationError is a single rejected condition with a locator.
type ValidationError struct {
	Path string
	Msg  string
}

func (e ValidationError) Error() string {
	if e.Path == "" {
		return e.Msg
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Msg)
}

// Validate checks a Flow Definition against Flow Spec v1 invariants (design
// doc §11.1-11.2): duplicate/empty ids, unknown node references, cycles,
// missing prompts, choice-set validity, branch constraints, loop closure and
// the 64-node worst-case expansion cap.
func Validate(d *Definition) error {
	if d == nil {
		return ValidationError{Msg: "nil flow definition"}
	}
	if strings.TrimSpace(d.FlowID) == "" {
		return ValidationError{Path: "flow_id", Msg: "missing flow_id"}
	}
	if len(d.Nodes) == 0 {
		return ValidationError{Path: "nodes", Msg: "flow has no nodes"}
	}
	if err := validateIODecls(d); err != nil {
		return err
	}
	if d.OnComplete != nil {
		if strings.TrimSpace(d.OnComplete.URL) == "" {
			return ValidationError{Path: "on_complete.url", Msg: "on_complete requires a callback url"}
		}
	}

	byID := make(map[string]Node, len(d.Nodes))
	for _, n := range d.Nodes {
		id := strings.TrimSpace(n.ID)
		if id == "" {
			return ValidationError{Path: "nodes", Msg: "node with empty id"}
		}
		if _, dup := byID[id]; dup {
			return ValidationError{Path: "nodes", Msg: fmt.Sprintf("duplicate node id %q", id)}
		}
		byID[id] = n
	}

	// Static node validation.
	for _, n := range d.Nodes {
		p := "nodes[" + n.ID + "]"
		kind := normalizeKind(n.Kind)
		if kind == "" {
			return ValidationError{Path: p, Msg: fmt.Sprintf("unknown node kind %q", n.Kind)}
		}
		if kind != KindBranch && kind != KindLoop && kind != KindFunction && strings.TrimSpace(n.Prompt) == "" {
			return ValidationError{Path: p, Msg: "node requires prompt (branch/loop nodes carry cases/exit instead)"}
		}
		if n.Retry != nil {
			if err := validateRetry(n.Retry, p); err != nil {
				return err
			}
		}
		if err := validateVarDefs(n.Outputs, p+".outputs"); err != nil {
			return err
		}
		// P2.3: resolve {{inputs.*}} / {{nodes.*.outputs.*}} references in the
		// node prompt against the definition at compile time.
		if err := validateVarRefs(n.Prompt, p+".prompt", d, byID); err != nil {
			return err
		}
		switch kind {
		case KindDecision:
			if n.Decision == nil {
				return ValidationError{Path: p, Msg: "decision node missing decision spec"}
			}
			if err := validateDecision(n.Decision, p); err != nil {
				return err
			}
		case KindHuman:
			if n.Human == nil {
				return ValidationError{Path: p, Msg: "human node missing human spec"}
			}
			if err := validateHuman(n.Human, p); err != nil {
				return err
			}
		case KindBranch:
			if n.Branch == nil {
				return ValidationError{Path: p, Msg: "branch node missing branch spec"}
			}
			if err := validateBranch(n.Branch, p, byID); err != nil {
				return err
			}
		case KindLoop:
			if n.Loop == nil {
				return ValidationError{Path: p, Msg: "loop node missing loop spec"}
			}
			if err := validateLoop(n.Loop, p, byID); err != nil {
				return err
			}
		case KindFunction:
			if err := validateFunction(n.Function, p); err != nil {
				return err
			}
		}
	}

	// Edge validation + dependency graph for cycle detection.
	depGraph := make(map[string][]string, len(d.Nodes))
	for _, id := range keys(byID) {
		depGraph[id] = nil
	}
	for _, e := range d.Edges {
		p := "edges[" + e.ID + "]"
		if _, ok := byID[e.From]; !ok {
			return ValidationError{Path: p, Msg: fmt.Sprintf("edge from unknown node %q", e.From)}
		}
		if _, ok := byID[e.To]; !ok {
			return ValidationError{Path: p, Msg: fmt.Sprintf("edge to unknown node %q", e.To)}
		}
		et := normalizeEdgeType(e.EdgeType)
		switch et {
		case EdgeDataDependency, EdgeHandoff:
			if e.When != nil && !ConditionEmpty(*e.When) {
				return ValidationError{Path: p, Msg: "data_dependency/handoff edges must not carry a when predicate"}
			}
			depGraph[e.To] = append(depGraph[e.To], e.From)
		case EdgeCondition:
			if e.When == nil || ConditionEmpty(*e.When) {
				return ValidationError{Path: p, Msg: "condition edge requires a when predicate"}
			}
			if err := validateCondition(*e.When, p, byID); err != nil {
				return err
			}
		default:
			return ValidationError{Path: p, Msg: fmt.Sprintf("unknown edge type %q", e.EdgeType)}
		}
	}

	if cyc := findCycle(depGraph); cyc != nil {
		return ValidationError{Path: "edges", Msg: fmt.Sprintf("dependency cycle: %s", strings.Join(cyc, " -> "))}
	}

	// 64-node worst-case expansion cap (§11.2): static nodes + loop
	// max_iterations × body size (nested loops counted at one level).
	expanded := 0
	for _, n := range d.Nodes {
		expanded++
		if n.Loop != nil && n.Loop.MaxIterations > 0 {
			expanded += n.Loop.MaxIterations * len(n.Loop.Body)
		}
	}
	if expanded > MaxNodes {
		return ValidationError{Path: "nodes", Msg: fmt.Sprintf("worst-case expanded node count %d exceeds cap %d", expanded, MaxNodes)}
	}
	return nil
}

// MaxNodes is the durable engine node cap (workflowMaxNodes).
const MaxNodes = 64

func normalizeKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case KindStep, KindLLM, KindDecision, KindHuman, KindBranch, KindLoop, KindFunction:
		return strings.ToLower(strings.TrimSpace(kind))
	default:
		return ""
	}
}

func normalizeEdgeType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "", EdgeDataDependency:
		return EdgeDataDependency
	case EdgeHandoff, EdgeCondition:
		return strings.ToLower(strings.TrimSpace(t))
	default:
		return t
	}
}

// validateVarDefs checks a typed variable declaration list (Definition
// Inputs/Outputs or node Outputs, Flow Spec §3.4): names must be non-empty,
// unique and carry a known type.
func validateVarDefs(defs []VarDef, p string) error {
	seen := make(map[string]struct{}, len(defs))
	for i, v := range defs {
		name := strings.TrimSpace(v.Name)
		if name == "" {
			return ValidationError{Path: fmt.Sprintf("%s[%d].name", p, i), Msg: "variable name is required"}
		}
		if _, dup := seen[name]; dup {
			return ValidationError{Path: p, Msg: fmt.Sprintf("duplicate variable %q", name)}
		}
		seen[name] = struct{}{}
		if t := strings.ToLower(strings.TrimSpace(v.Type)); t != "" {
			switch t {
			case "string", "number", "boolean", "object", "array", "any":
			default:
				return ValidationError{Path: fmt.Sprintf("%s[%d].type", p, i), Msg: fmt.Sprintf("invalid variable type %q", v.Type)}
			}
			// object/array may carry a nested JSON Schema fragment; anything else
			// must be plain JSON when present (opaque but valid).
			if len(v.Schema) > 0 {
				if !json.Valid(v.Schema) {
					return ValidationError{Path: fmt.Sprintf("%s[%d].schema", p, i), Msg: "variable schema must be valid JSON"}
				}
				if t != "object" && t != "array" && t != "any" {
					return ValidationError{Path: fmt.Sprintf("%s[%d].schema", p, i), Msg: fmt.Sprintf("nested schema only allowed for object/array/any, got %q", t)}
				}
			}
		}
	}
	return nil
}

// validateIODecls validates the Flow-level Inputs/Outputs declarations
// (names unique + known types) used by prompt variable references.
func validateIODecls(d *Definition) error {
	if err := validateVarDefs(d.Inputs, "inputs"); err != nil {
		return err
	}
	return validateVarDefs(d.Outputs, "outputs")
}

// validateVarRefs parses {{...}} references in a node's prompt and validates
// each reference path against the definition (Flow Spec §3.4):
//   - {{inputs.<name>}}             -> Flow-level Inputs declaration
//   - {{nodes.<id>.outputs.<field>}} -> a node's declared Outputs (or the
//     standard decision/human fields)
//
// Unresolvable references are rejected at compile time so a flow that reads a
// variable that does not exist fails fast instead of silently rendering empty.
func validateVarRefs(prompt string, p string, d *Definition, byID map[string]Node) error {
	if strings.TrimSpace(prompt) == "" {
		return nil
	}
	// {{...}} reference grammar: inputs.<name> or nodes.<id>.outputs.<field>.
	exprRe := regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)
	for _, m := range exprRe.FindAllStringSubmatch(prompt, -1) {
		if len(m) != 2 {
			continue
		}
		expr := strings.TrimSpace(m[1])
		segments := strings.Split(expr, ".")
		if len(segments) < 2 {
			return ValidationError{Path: p, Msg: fmt.Sprintf("invalid variable reference %q", expr)}
		}
		switch segments[0] {
		case "inputs":
			name := segments[1]
			if !flowVarDefExists(d.Inputs, name) {
				return ValidationError{Path: p, Msg: fmt.Sprintf("prompt references unknown input %q (declare in flow inputs)", name)}
			}
		case "nodes":
			nodeID := segments[1]
			target, ok := byID[nodeID]
			if !ok {
				return ValidationError{Path: p, Msg: fmt.Sprintf("prompt references unknown node %q", nodeID)}
			}
			if len(segments) < 4 || segments[2] != "outputs" {
				return ValidationError{Path: p, Msg: fmt.Sprintf("prompt variable %q must reference a node output (nodes.<id>.outputs.<field>)", expr)}
			}
			field := segments[3]
			if !nodeOutputsField(target, field) {
				return ValidationError{Path: p, Msg: fmt.Sprintf("node %q does not declare output %q", nodeID, field)}
			}
		default:
			return ValidationError{Path: p, Msg: fmt.Sprintf("unknown variable scope %q (expected inputs or nodes)", segments[0])}
		}
	}
	return nil
}

func flowVarDefExists(defs []VarDef, name string) bool {
	for _, v := range defs {
		if strings.TrimSpace(v.Name) == name {
			return true
		}
	}
	return false
}

// nodeOutputsField reports whether a node declares the given output field.
// Decision nodes expose the standard choice/confidence/... fields and human
// nodes expose their result_var even when not listed in Outputs.
func nodeOutputsField(n Node, field string) bool {
	for _, v := range n.Outputs {
		if strings.TrimSpace(v.Name) == field {
			return true
		}
	}
	switch normalizeKind(n.Kind) {
	case KindDecision:
		switch field {
		case "choice", "confidence", "calibrated", "score", "model", "latency_ms", "error":
			return true
		}
	case KindHuman:
		if n.Human != nil && strings.TrimSpace(n.Human.ResultVar) == field {
			return true
		}
	}
	return false
}

func validateRetry(r *RetryPolicy, p string) error {
	if r.MaxAttempts < 1 {
		return ValidationError{Path: p + ".retry", Msg: fmt.Sprintf("max_attempts must be >= 1, got %d", r.MaxAttempts)}
	}
	if r.InitialIntervalMS < 0 || r.MaxIntervalMS < 0 || r.Jitter < 0 || r.Jitter > 1 {
		return ValidationError{Path: p + ".retry", Msg: "interval/jitter values out of range"}
	}
	return nil
}

func validateDecision(s *DecisionSpec, p string) error {
	dt := strings.ToLower(strings.TrimSpace(s.DecisionType))
	if dt == "" {
		dt = "choice"
	}
	switch dt {
	case "choice", "boolean", "score":
	default:
		return ValidationError{Path: p + ".decision", Msg: fmt.Sprintf("invalid decision_type %q", s.DecisionType)}
	}
	if dt == "choice" {
		if len(s.Choices) == 0 {
			return ValidationError{Path: p + ".decision", Msg: "choice decision requires at least one choice"}
		}
		seen := map[string]struct{}{}
		for _, c := range s.Choices {
			id := strings.TrimSpace(c.ID)
			if id == "" {
				return ValidationError{Path: p + ".decision", Msg: "choice with empty id"}
			}
			if _, dup := seen[id]; dup {
				return ValidationError{Path: p + ".decision", Msg: fmt.Sprintf("duplicate choice id %q", id)}
			}
			seen[id] = struct{}{}
		}
	}
	oe := strings.ToLower(strings.TrimSpace(s.OnError))
	switch oe {
	case "", "fail_closed", "fail_open", "fail":
	default:
		return ValidationError{Path: p + ".decision", Msg: fmt.Sprintf("invalid on_error %q", s.OnError)}
	}
	if oe == "fail_open" && strings.TrimSpace(s.DefaultChoice) == "" {
		return ValidationError{Path: p + ".decision", Msg: "on_error=fail_open requires default_choice"}
	}
	return nil
}

// Human node on_timeout modes (Flow Spec §3.2, P1.1).
const (
	humanOnTimeoutFail     = "fail"
	humanOnTimeoutLLM      = "llm"
	humanOnTimeoutEscalate = "escalate"
)

// normalizeHumanOnTimeout lowercases and trims the on_timeout value while
// preserving the escalate:<queue> target.
func normalizeHumanOnTimeout(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if strings.HasPrefix(v, humanOnTimeoutEscalate) {
		target := strings.TrimSpace(strings.TrimPrefix(v, humanOnTimeoutEscalate))
		target = strings.TrimPrefix(target, ":")
		if strings.TrimSpace(target) != "" {
			return humanOnTimeoutEscalate + ":" + strings.TrimSpace(target)
		}
		return humanOnTimeoutEscalate
	}
	return v
}

// validateHuman checks the human node spec (P1.1): a queue is required, the
// on_timeout value must be one of fail|llm|escalate:<queue>, and timeout_ms
// must not be negative. The node prompt carries the human instruction.
func validateHuman(s *HumanSpec, p string) error {
	if s == nil {
		return ValidationError{Path: p + ".human", Msg: "human node missing human spec"}
	}
	if strings.TrimSpace(s.Queue) == "" {
		return ValidationError{Path: p + ".human", Msg: "human spec missing queue"}
	}
	if s.TimeoutMS < 0 {
		return ValidationError{Path: p + ".human", Msg: "human timeout_ms must not be negative"}
	}
	ot := normalizeHumanOnTimeout(s.OnTimeout)
	switch ot {
	case "", humanOnTimeoutFail, humanOnTimeoutLLM:
		// valid
	case humanOnTimeoutEscalate:
		return ValidationError{Path: p + ".human", Msg: "human on_timeout escalate requires a target queue (escalate:<queue>)"}
	default:
		if !strings.HasPrefix(ot, humanOnTimeoutEscalate+":") {
			return ValidationError{Path: p + ".human", Msg: fmt.Sprintf("invalid on_timeout %q (want fail|llm|escalate:<queue>)", s.OnTimeout)}
		}
	}
	return nil
}

func validateBranch(b *BranchSpec, p string, byID map[string]Node) error {
	if len(b.Cases) == 0 {
		return ValidationError{Path: p + ".branch", Msg: "branch requires at least one case"}
	}
	for i, c := range b.Cases {
		cp := fmt.Sprintf("%s.branch.cases[%d]", p, i)
		if _, ok := byID[strings.TrimSpace(c.To)]; !ok {
			return ValidationError{Path: cp, Msg: fmt.Sprintf("branch case targets unknown node %q", c.To)}
		}
		if ConditionEmpty(c.Condition) {
			return ValidationError{Path: cp, Msg: "branch case requires a condition"}
		}
		if err := validateCondition(c.Condition, cp, byID); err != nil {
			return err
		}
	}
	if _, ok := byID[strings.TrimSpace(b.DefaultTo)]; !ok {
		return ValidationError{Path: p + ".branch", Msg: fmt.Sprintf("branch default_to unknown node %q", b.DefaultTo)}
	}
	return nil
}

func validateLoop(l *LoopSpec, p string, byID map[string]Node) error {
	if len(l.Body) == 0 {
		return ValidationError{Path: p + ".loop", Msg: "loop requires a non-empty body"}
	}
	for _, id := range l.Body {
		if _, ok := byID[strings.TrimSpace(id)]; !ok {
			return ValidationError{Path: p + ".loop", Msg: fmt.Sprintf("loop body references unknown node %q", id)}
		}
	}
	if ConditionEmpty(l.ExitWhen) {
		return ValidationError{Path: p + ".loop", Msg: "loop requires exit_when condition"}
	}
	if err := validateCondition(l.ExitWhen, p+".loop", byID); err != nil {
		return err
	}
	if l.MaxIterations < 1 {
		return ValidationError{Path: p + ".loop", Msg: fmt.Sprintf("loop max_iterations must be >= 1, got %d", l.MaxIterations)}
	}
	return nil
}

// validateFunction checks a function (code) node: runtime must be js|wasm;
// js requires inline source, wasm requires a node-library ref.
func validateFunction(f *FunctionSpec, p string) error {
	if f == nil {
		return ValidationError{Path: p, Msg: "function node missing function spec"}
	}
	switch strings.ToLower(strings.TrimSpace(f.Runtime)) {
	case FunctionRuntimeJS:
		if strings.TrimSpace(f.Source) == "" {
			return ValidationError{Path: p + ".function", Msg: "js function node requires source"}
		}
	case FunctionRuntimeWasm:
		if strings.TrimSpace(f.Ref) == "" {
			return ValidationError{Path: p + ".function", Msg: "wasm function node requires a node-library ref"}
		}
	default:
		return ValidationError{Path: p + ".function", Msg: fmt.Sprintf("unknown function runtime %q (js|wasm)", f.Runtime)}
	}
	return nil
}

// validateCondition checks the structured predicate against the node set and
// the standard decision fields. Only declared outputs and the standard
// choice/confidence/status/verdict sources are readable.
func validateCondition(c Condition, p string, byID map[string]Node) error {
	if c.Node != "" {
		if _, ok := byID[strings.TrimSpace(c.Node)]; !ok {
			return ValidationError{Path: p, Msg: fmt.Sprintf("condition targets unknown node %q", c.Node)}
		}
	}
	if c.Confidence != nil {
		switch c.Confidence.Op {
		case "gt", "gte", "lt", "lte", "eq":
		default:
			return ValidationError{Path: p + ".confidence", Msg: fmt.Sprintf("invalid numeric op %q", c.Confidence.Op)}
		}
	}
	if c.Output != nil {
		switch c.Output.Op {
		case "eq", "ne", "in", "not_in", "contains", "gt", "gte", "lt", "lte":
		default:
			return ValidationError{Path: p + ".output", Msg: fmt.Sprintf("invalid field op %q", c.Output.Op)}
		}
		if strings.TrimSpace(c.Output.Path) == "" {
			return ValidationError{Path: p + ".output", Msg: "output predicate requires a path"}
		}
	}
	if c.Not != nil {
		if err := validateCondition(*c.Not, p+".not", byID); err != nil {
			return err
		}
	}
	for i, sub := range c.All {
		if err := validateCondition(sub, fmt.Sprintf("%s.all[%d]", p, i), byID); err != nil {
			return err
		}
	}
	for i, sub := range c.Any {
		if err := validateCondition(sub, fmt.Sprintf("%s.any[%d]", p, i), byID); err != nil {
			return err
		}
	}
	return nil
}

// findCycle returns a cycle path in the dependency graph (DFS), or nil.
func findCycle(graph map[string][]string) []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(graph))
	var stack []string
	var visit func(id string) []string
	visit = func(id string) []string {
		color[id] = gray
		stack = append(stack, id)
		for _, dep := range graph[id] {
			switch color[dep] {
			case gray:
				// Found a back edge: slice the cycle from first occurrence.
				for i, s := range stack {
					if s == dep {
						return append(append([]string{}, stack[i:]...), dep)
					}
				}
			case white:
				if cyc := visit(dep); cyc != nil {
					return cyc
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return nil
	}
	for id := range graph {
		if color[id] == white {
			if cyc := visit(id); cyc != nil {
				return cyc
			}
		}
	}
	return nil
}

func keys(m map[string]Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
