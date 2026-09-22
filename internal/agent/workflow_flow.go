// F0 flow runtime primitives on top of the durable workflow engine:
//   - decision nodes (low-cost structured decision models, Jev/Laya-class)
//   - structured edge predicates (choice / confidence / output fields)
//   - node-level RetryPolicy for transient failures (Temporal-style backoff)
//
// See docs/business-flow-runtime-design.md §3 (Flow Spec v1). F0 implements
// the engine kernel; the Flow Spec compiler, version store and FlowGram
// adapter land in F1/F3.
package agent

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/decision"
)

const (
	// workflowNodeKindDecision is a synchronous structured-decision node. It
	// starts no subagent job, writes no transcript and consumes no context
	// budget; it calls the decision Caller and records {choice, confidence}.
	workflowNodeKindDecision = "decision"

	// workflowNodeKindBranch is a synchronous routing gateway node (Flow Spec
	// §3.2). It evaluates its cases against the dependency source node's
	// outputs and writes the matched route name into outputs.choice; routing
	// to targets happens through condition edges with when.choice, so mutual
	// exclusion is structural (no NOT conditions needed).
	workflowNodeKindBranch = "branch"

	// workflowNodeKindUserInput is the engine node kind for a blocked-wait
	// node (P1.1 human node lowering): it registers a human task and stays
	// pending until the orchestrator feeds input via complete_node.
	workflowNodeKindUserInput = "user_input"

	// workflowNodeKindFunction is the engine node kind for a code node (P3):
	// a pure compute step executed synchronously in the scheduler via the
	// jsrt (goja) or wasmrt runtime — never starts a subagent job.
	workflowNodeKindFunction = "function"

	// branchDefaultRoute is the reserved route name for a branch default.
	branchDefaultRoute = "default"

	// decisionErrorChoice is the reserved choice emitted by fail_closed when
	// the decision model fails after retries. Flows branch on it to route to
	// the llm fallback node.
	decisionErrorChoice = "__decision_error__"

	// decision on-error modes.
	decisionOnErrorFailClosed = "fail_closed" // default: route to llm fallback
	decisionOnErrorFailOpen   = "fail_open"   // continue with default_choice
	decisionOnErrorFail       = "fail"        // fail the run

	// retryable failure kinds, aligned with Flow Spec RetryPolicy.RetryOn.
	retryKindProviderTimeout = "provider_timeout"
	retryKindProviderError   = "provider_error"
	retryKindToolTransient   = "tool_transient"
	retryKindDecisionError   = "decision_model_error"
)

// workflowRetryPolicy mirrors Temporal's RetryPolicy. Semantic failures
// (verdict=fail, permission denied, schema violations) are never retried;
// they route through branch/repair edges instead.
type workflowRetryPolicy struct {
	MaxAttempts        int      `json:"max_attempts,omitempty"` // including the first attempt; 1 = no retry
	InitialIntervalMS  int      `json:"initial_interval_ms,omitempty"`
	BackoffCoefficient float64  `json:"backoff_coefficient,omitempty"`
	MaxIntervalMS      int      `json:"max_interval_ms,omitempty"`
	Jitter             float64  `json:"jitter,omitempty"`
	RetryOn            []string `json:"retry_on,omitempty"`
	NonRetryable       []string `json:"non_retryable,omitempty"`
}

// workflowDecisionSpec is the node-level decision configuration (the engine
// projection of Flow Spec DecisionSpec).
type workflowDecisionSpec struct {
	Provider      string                   `json:"provider,omitempty"`
	Question      string                   `json:"question,omitempty"`
	DecisionType  string                   `json:"decision_type,omitempty"`
	Choices       []workflowDecisionChoice `json:"choices,omitempty"`
	TimeoutMS     int                      `json:"timeout_ms,omitempty"`
	OnError       string                   `json:"on_error,omitempty"`
	DefaultChoice string                   `json:"default_choice,omitempty"`
}

type workflowDecisionChoice struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// workflowBranchSpec is the engine projection of Flow Spec BranchSpec. The
// gateway evaluates cases in order against the Source node's outputs; the
// first hit routes by name (case.Name, else case.To), else default.
type workflowBranchSpec struct {
	Source    string               `json:"source,omitempty"` // dependency source node ID (F1a: single source)
	Cases     []workflowBranchCase `json:"cases"`
	DefaultTo string               `json:"default_to"`
}

// workflowBranchCase is one output port of a branch gateway.
type workflowBranchCase struct {
	Name      string                `json:"name,omitempty"`
	To        string                `json:"to"`
	Condition workflowEdgeCondition `json:"condition"`
}

// workflowDecisionResult is the persisted standard decision output.
type workflowDecisionResult struct {
	Choice     string         `json:"choice"`
	Confidence float64        `json:"confidence"`
	Calibrated bool           `json:"calibrated"`
	Score      float64        `json:"score,omitempty"`
	Model      string         `json:"model,omitempty"`
	LatencyMS  int64          `json:"latency_ms"`
	Error      string         `json:"error,omitempty"`
	Raw        map[string]any `json:"raw,omitempty"`
}

type workflowNumCompare struct {
	Op    string  `json:"op"`
	Value float64 `json:"value"`
}

type workflowFieldCompare struct {
	Path  string `json:"path"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

// ---------------------------------------------------------------------------
// RetryPolicy
// ---------------------------------------------------------------------------

func normalizeWorkflowRetryPolicy(p *workflowRetryPolicy) *workflowRetryPolicy {
	if p == nil {
		return nil
	}
	out := *p
	if out.MaxAttempts < 0 {
		out.MaxAttempts = 0
	}
	if out.InitialIntervalMS <= 0 {
		out.InitialIntervalMS = 500
	}
	if out.BackoffCoefficient <= 0 {
		out.BackoffCoefficient = 2.0
	}
	if out.MaxIntervalMS <= 0 {
		out.MaxIntervalMS = 30000
	}
	if out.Jitter < 0 {
		out.Jitter = 0
	}
	if out.Jitter > 1 {
		out.Jitter = 1
	}
	if out.Jitter == 0 {
		out.Jitter = 0.2
	}
	if len(out.RetryOn) == 0 {
		out.RetryOn = []string{retryKindProviderTimeout, retryKindProviderError, retryKindToolTransient, retryKindDecisionError}
	}
	return &out
}

// workflowRetryDelay computes the backoff before retry number retryNo
// (0-based: 0 is the first retry). Jitter is deterministic when rng is nil.
func workflowRetryDelay(p *workflowRetryPolicy, retryNo int, rng *rand.Rand) time.Duration {
	if p == nil {
		return 0
	}
	initial := time.Duration(p.InitialIntervalMS) * time.Millisecond
	max := time.Duration(p.MaxIntervalMS) * time.Millisecond
	coeff := p.BackoffCoefficient
	if coeff <= 0 {
		coeff = 2.0
	}
	delay := float64(initial) * math.Pow(coeff, float64(retryNo))
	if delay > float64(max) {
		delay = float64(max)
	}
	if p.Jitter > 0 {
		// ±Jitter fraction around the computed delay.
		factor := 1 + (rngFloat(rng)*2-1)*p.Jitter
		if factor < 0 {
			factor = 0
		}
		delay *= factor
	}
	if delay < 0 {
		delay = 0
	}
	return time.Duration(delay)
}

func rngFloat(rng *rand.Rand) float64 {
	if rng == nil {
		return 0.5
	}
	return rng.Float64()
}

// shouldWorkflowRetry decides whether a terminal failure is retried.
// executedAttempts is the node.Attempt value after the failed attempt was
// counted. Returns false for nil policy, unknown/non-retryable failure kinds
// and exhausted attempts.
func shouldWorkflowRetry(p *workflowRetryPolicy, executedAttempts int, kind string) (bool, time.Duration) {
	if p == nil || kind == "" {
		return false, 0
	}
	maxAttempts := p.MaxAttempts
	if maxAttempts <= 1 {
		return false, 0
	}
	if containsWorkflowString(p.NonRetryable, kind) {
		return false, 0
	}
	if len(p.RetryOn) > 0 && !containsWorkflowString(p.RetryOn, kind) {
		return false, 0
	}
	if executedAttempts >= maxAttempts {
		return false, 0
	}
	return true, workflowRetryDelay(p, executedAttempts-1, nil)
}

func containsWorkflowString(items []string, want string) bool {
	for _, item := range items {
		if strings.TrimSpace(item) == want {
			return true
		}
	}
	return false
}

// classifyWorkflowFailure maps a terminal job status/error to a retryable
// failure kind. It is deliberately conservative: anything not clearly
// transient returns "" (no auto-retry), so semantic failures never loop.
func classifyWorkflowFailure(jobStatus, errText string) string {
	switch strings.TrimSpace(jobStatus) {
	case "timeout":
		return retryKindProviderTimeout
	case "canceled", "interrupted":
		return ""
	}
	text := strings.ToLower(strings.TrimSpace(errText))
	if text == "" {
		return ""
	}
	switch {
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline exceeded"),
		strings.Contains(text, "context deadline"):
		return retryKindProviderTimeout
	case strings.Contains(text, "429"), strings.Contains(text, "rate limit"),
		strings.Contains(text, "too many requests"):
		return retryKindProviderError
	case strings.Contains(text, "502"), strings.Contains(text, "503"), strings.Contains(text, "504"),
		strings.Contains(text, "bad gateway"), strings.Contains(text, "service unavailable"),
		strings.Contains(text, "gateway timeout"), strings.Contains(text, "internal server error"):
		return retryKindProviderError
	case strings.Contains(text, "connection reset"), strings.Contains(text, "connection refused"),
		strings.Contains(text, "connection aborted"), strings.Contains(text, "network is unreachable"),
		strings.Contains(text, "no such host"), strings.Contains(text, "broken pipe"),
		strings.Contains(text, "eof"), strings.Contains(text, "temporar"),
		strings.Contains(text, "try again"):
		return retryKindToolTransient
	case strings.Contains(text, "permission"), strings.Contains(text, "denied"),
		strings.Contains(text, "forbidden"), strings.Contains(text, "verdict"),
		strings.Contains(text, "schema"), strings.Contains(text, "invalid choice"),
		strings.Contains(text, "invalid argument"):
		return ""
	default:
		return ""
	}
}

// classifyDecisionFailure maps a decision Caller error to a retryable kind.
// Unknown decision errors default to decision_model_error (retryable when
// the node policy allows it).
func classifyDecisionFailure(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline exceeded"):
		return retryKindProviderTimeout
	case strings.Contains(text, "no decision caller configured"):
		// Configuration gap: retrying within one run cannot fix it.
		return ""
	default:
		return retryKindDecisionError
	}
}

// ---------------------------------------------------------------------------
// Decision spec normalization
// ---------------------------------------------------------------------------

func normalizeWorkflowDecisionSpec(spec *workflowDecisionSpec) (*workflowDecisionSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("decision node missing decision spec")
	}
	out := *spec
	out.Question = strings.TrimSpace(out.Question)
	out.Provider = strings.TrimSpace(out.Provider)
	out.DecisionType = strings.ToLower(strings.TrimSpace(out.DecisionType))
	if out.DecisionType == "" {
		out.DecisionType = decision.TypeChoice
	}
	switch out.DecisionType {
	case decision.TypeChoice, decision.TypeBoolean, decision.TypeScore:
	default:
		return nil, fmt.Errorf("decision node has invalid decision_type %q", out.DecisionType)
	}
	if out.DecisionType == decision.TypeChoice {
		if len(out.Choices) == 0 {
			return nil, fmt.Errorf("choice decision node requires at least one choice")
		}
		seen := map[string]struct{}{}
		choices := make([]workflowDecisionChoice, 0, len(out.Choices))
		for _, ch := range out.Choices {
			id := strings.TrimSpace(ch.ID)
			if id == "" {
				return nil, fmt.Errorf("decision node contains empty choice id")
			}
			if _, ok := seen[id]; ok {
				return nil, fmt.Errorf("decision node contains duplicate choice id %q", id)
			}
			seen[id] = struct{}{}
			choices = append(choices, workflowDecisionChoice{ID: id, Label: strings.TrimSpace(ch.Label)})
		}
		out.Choices = choices
	}
	out.OnError = strings.ToLower(strings.TrimSpace(out.OnError))
	switch out.OnError {
	case "":
		out.OnError = decisionOnErrorFailClosed
	case decisionOnErrorFailClosed, decisionOnErrorFailOpen, decisionOnErrorFail:
	default:
		return nil, fmt.Errorf("decision node has invalid on_error %q", out.OnError)
	}
	out.DefaultChoice = strings.TrimSpace(out.DefaultChoice)
	if out.OnError == decisionOnErrorFailOpen && out.DefaultChoice == "" {
		return nil, fmt.Errorf("decision node on_error=fail_open requires default_choice")
	}
	return &out, nil
}

// normalizeWorkflowBranchSpec validates and normalizes a branch gateway spec.
// The gateway evaluates cases in order against Source's outputs and writes the
// matched route name into outputs.choice; routing happens via when.choice
// condition edges (mutual exclusion is structural).
func normalizeWorkflowBranchSpec(spec *workflowBranchSpec) (*workflowBranchSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("branch node missing branch spec")
	}
	out := workflowBranchSpec{
		Source:    strings.TrimSpace(spec.Source),
		DefaultTo: strings.TrimSpace(spec.DefaultTo),
	}
	if out.Source == "" {
		return nil, fmt.Errorf("branch node missing source")
	}
	if out.DefaultTo == "" {
		return nil, fmt.Errorf("branch node missing default_to")
	}
	if len(spec.Cases) == 0 {
		return nil, fmt.Errorf("branch node requires at least one case")
	}
	seen := map[string]struct{}{}
	for i, c := range spec.Cases {
		route := strings.TrimSpace(c.Name)
		if route == "" {
			route = strings.TrimSpace(c.To)
		}
		if route == "" {
			return nil, fmt.Errorf("branch case %d missing name/to", i)
		}
		if _, dup := seen[route]; dup {
			return nil, fmt.Errorf("branch case duplicate route %q", route)
		}
		seen[route] = struct{}{}
		if strings.TrimSpace(c.To) == "" {
			return nil, fmt.Errorf("branch case %d missing target", i)
		}
		if workflowConditionEmpty(c.Condition) {
			return nil, fmt.Errorf("branch case %d missing condition", i)
		}
		out.Cases = append(out.Cases, workflowBranchCase{
			Name:      route,
			To:        strings.TrimSpace(c.To),
			Condition: normalizeWorkflowEdgeCondition(c.Condition),
		})
	}
	return &out, nil
}

// ---------------------------------------------------------------------------
// Edge condition predicates
// ---------------------------------------------------------------------------

func normalizeWorkflowEdgeCondition(c workflowEdgeCondition) workflowEdgeCondition {
	out := workflowEdgeCondition{
		Status:     strings.TrimSpace(c.Status),
		Verdict:    normalizeWorkflowVerdict(c.Verdict),
		Node:       strings.TrimSpace(c.Node),
		Choice:     strings.TrimSpace(c.Choice),
		Confidence: c.Confidence,
		Output:     c.Output,
	}
	for _, sub := range c.All {
		out.All = append(out.All, normalizeWorkflowEdgeCondition(sub))
	}
	for _, sub := range c.Any {
		out.Any = append(out.Any, normalizeWorkflowEdgeCondition(sub))
	}
	return out
}

func workflowConditionEmpty(c workflowEdgeCondition) bool {
	return c.Status == "" && c.Verdict == "" && c.Node == "" && c.Choice == "" &&
		c.Confidence == nil && c.Output == nil && c.Not == nil &&
		len(c.All) == 0 && len(c.Any) == 0
}

// workflowEdgeConditionMatchesState evaluates a condition, resolving
// cond.Node against the workflow state (default: the edge source node).
func workflowEdgeConditionMatchesState(state *workflowState, cond workflowEdgeCondition, source workflowNode) bool {
	target := source
	if cond.Node != "" && state != nil {
		for _, n := range state.Nodes {
			if n.ID == cond.Node {
				target = n
				break
			}
		}
	}
	return workflowConditionMatchesNode(cond, target)
}

func workflowConditionMatchesNode(cond workflowEdgeCondition, node workflowNode) bool {
	if cond.Not != nil {
		// NOT predicate (loop exit_when negation, P1.5): matches when the
		// negated sub-predicate does NOT match the same node.
		return !workflowConditionMatchesNode(*cond.Not, node)
	}
	if cond.Status != "" && cond.Status != node.Status {
		return false
	}
	if cond.Verdict != "" && cond.Verdict != normalizeWorkflowVerdict(node.Verdict) {
		return false
	}
	if cond.Choice != "" && workflowNodeChoice(node) != cond.Choice {
		return false
	}
	if cond.Confidence != nil && !numCompare(cond.Confidence, workflowNodeConfidence(node)) {
		return false
	}
	if cond.Output != nil && !fieldCompare(cond.Output, node.Outputs) {
		return false
	}
	if len(cond.All) > 0 {
		for _, sub := range cond.All {
			if !workflowConditionMatchesNode(sub, node) {
				return false
			}
		}
	}
	if len(cond.Any) > 0 {
		matched := false
		for _, sub := range cond.Any {
			if workflowConditionMatchesNode(sub, node) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func workflowNodeChoice(node workflowNode) string {
	if node.Decision != nil && strings.TrimSpace(node.Decision.Choice) != "" {
		return node.Decision.Choice
	}
	if v, ok := node.Outputs["choice"].(string); ok {
		return v
	}
	return ""
}

func workflowNodeConfidence(node workflowNode) float64 {
	if node.Decision != nil {
		return node.Decision.Confidence
	}
	if v, ok := node.Outputs["confidence"].(float64); ok {
		return v
	}
	return math.NaN()
}

func numCompare(c *workflowNumCompare, got float64) bool {
	if math.IsNaN(got) {
		return false
	}
	switch c.Op {
	case "gt":
		return got > c.Value
	case "gte":
		return got >= c.Value
	case "lt":
		return got < c.Value
	case "lte":
		return got <= c.Value
	case "eq":
		return got == c.Value
	default:
		return false
	}
}

func fieldCompare(c *workflowFieldCompare, outputs map[string]any) bool {
	if c == nil {
		return false
	}
	got, ok := workflowOutputValue(outputs, c.Path)
	if !ok {
		return false
	}
	switch c.Op {
	case "eq", "ne":
		eq := valuesEqual(got, c.Value)
		return eq == (c.Op == "eq")
	case "in", "not_in":
		found := false
		for _, item := range toAnySlice(c.Value) {
			if valuesEqual(got, item) {
				found = true
				break
			}
		}
		return found == (c.Op == "in")
	case "contains":
		return valueContains(got, c.Value)
	case "gt", "gte", "lt", "lte":
		a, aok := toFloat(got)
		b, bok := toFloat(c.Value)
		if !aok || !bok {
			return false
		}
		switch c.Op {
		case "gt":
			return a > b
		case "gte":
			return a >= b
		case "lt":
			return a < b
		default:
			return a <= b
		}
	default:
		return false
	}
}

// workflowOutputValue reads a dotted path from node outputs. A leading
// "outputs." segment is tolerated to match Flow Spec path spelling.
func workflowOutputValue(outputs map[string]any, path string) (any, bool) {
	parts := strings.Split(strings.TrimSpace(path), ".")
	if len(parts) > 0 && parts[0] == "outputs" {
		parts = parts[1:]
	}
	var current any = outputs
	for _, part := range parts {
		if part == "" {
			continue
		}
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func valuesEqual(a, b any) bool {
	if af, ok := toFloat(a); ok {
		if bf, ok := toFloat(b); ok {
			return af == bf
		}
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func toAnySlice(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case []string:
		out := make([]any, 0, len(s))
		for _, item := range s {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func valueContains(container, item any) bool {
	switch c := container.(type) {
	case string:
		return strings.Contains(c, fmt.Sprint(item))
	case []any:
		for _, v := range c {
			if valuesEqual(v, item) {
				return true
			}
		}
	case []string:
		for _, v := range c {
			if v == fmt.Sprint(item) {
				return true
			}
		}
	}
	return false
}
