// Package flow defines Flow Spec v1 (docs/business-flow-runtime-design.md §3):
// a declarative, versioned workflow specification that compiles onto the
// durable workflow engine. F1a implements the model, validation and the
// compile step; the version store and /v1/flows API land in F1b/F2.
package flow

import "encoding/json"

// Node kinds (Flow Spec §3.2).
const (
	KindStep     = "step"     // fixed step: subagent_task / tool_call
	KindLLM      = "llm"      // pure reasoning node: llm_task
	KindDecision = "decision" // low-cost structured decision (System-1 model)
	KindHuman    = "human"    // manual fallback: user_input + human task store (F2)
	KindBranch   = "branch"   // no-job gateway: evaluates cases synchronously
	KindLoop     = "loop"     // compiled to control_flow append edges
	KindFunction = "function" // code node: js (goja) or wasm (wasmrt plugin) handler (P3)
)

// Edge types (Flow Spec §3.3).
const (
	EdgeDataDependency = "data_dependency" // From must complete before To starts
	EdgeHandoff        = "handoff"         // To receives From's bounded summary
	EdgeCondition      = "condition"       // when From matches When, To is appended
)

// Definition is one immutable version of a flow (flow.json).
// NetworkPolicy is the Flow-level outbound network security policy (E3a):
// controls what function nodes (js/wasm) may reach. Default allow_all with no
// blocklist. Applied at run time by the sandbox HTTP bridge; also documented
// on the definition for review.
type NetworkPolicy struct {
	// Policy is "allow_all" (default) or "allowlist".
	Policy string `json:"policy,omitempty"`
	// AllowedDomains are exact hosts or *.suffix patterns allowed when
	// Policy == "allowlist" (e.g. "api.openai.com", "*.modelscope.cn").
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	// BlockedDomains are always denied (checked first, both policies).
	BlockedDomains []string `json:"blocked_domains,omitempty"`
	// TimeoutSeconds bounds each outbound request; 0 = 15s default.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// MaxResponseChars caps each response body; 0 = 1 MiB default.
	MaxResponseChars int `json:"max_response_chars,omitempty"`
}

// Definition is a Flow Spec v1 definition.
type Definition struct {
	FlowID      string   `json:"flow_id"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Version     string   `json:"version"`
	Status      string   `json:"status"` // draft | gray | published | deprecated | archived
	TemplateID  string   `json:"template_id,omitempty"`
	Inputs      []VarDef `json:"inputs,omitempty"`
	Outputs     []VarDef `json:"outputs,omitempty"`
	Nodes       []Node   `json:"nodes"`
	Edges       []Edge   `json:"edges"`
	// Network is the outbound network security policy for function nodes
	// (js/wasm) in this flow (E3a). Empty = allow all, no blocklist.
	Network *NetworkPolicy `json:"network,omitempty"`
	// Retry is the default RetryPolicy applied to nodes that do not override it.
	Retry *RetryPolicy `json:"retry,omitempty"`
	// OnComplete, when set, is the webhook POSTed when a run reaches a
	// terminal state (P1.3). The body is the FlowRunView JSON plus
	// x-godex-signature: sha256=<HMAC-SHA256 of body with secret> when secret
	// is non-empty.
	OnComplete *OnCompleteSpec `json:"on_complete,omitempty"`
}

// OnCompleteSpec configures the completion webhook of a flow (P1.3).
type OnCompleteSpec struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"` // HMAC-SHA256 signing secret
}

// VarDef declares one typed input/output variable (Flow Spec §3.4).
// Type is one of string|number|boolean|object|array|any. For object/array a
// nested JSON-Schema-ish fragment may be attached via Schema (e.g.
// {"properties":{...}} or {"items":{...}}) to define the shape — validated
// at save time (validate.go), passed through untouched to consumers.
type VarDef struct {
	Name string          `json:"name"`
	Type string          `json:"type,omitempty"`
	Desc string          `json:"desc,omitempty"`
	// Schema is an optional nested JSON Schema fragment for object/array
	// variables (P2.3 变量 schema). Opaque to the engine beyond JSON validity;
	// it documents/validates the expected shape for callers.
	Schema json.RawMessage `json:"schema,omitempty"`
}

// CanvasPos is editor-only layout metadata stored on a node (canvas x/y).
// It is stripped before compile and never affects runtime semantics.
type CanvasPos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Node is one node in a flow definition.
type Node struct {
	ID         string       `json:"id"`
	Kind       string       `json:"kind"`
	Title      string       `json:"title,omitempty"`
	Prompt     string       `json:"prompt,omitempty"`
	AgentType  string       `json:"agent_type,omitempty"`
	// CanvasPos is editor-only layout metadata (x/y on the FlowGram canvas).
	CanvasPos *CanvasPos `json:"canvas_pos,omitempty"`
	// AgentRef optionally pins this step node to an agent template (talent
	// market) or business key id. At run time the referenced template's
	// capability baseline (bundles/tools/write_scope/mcp/skills/packages) is
	// resolved and injected into the node's subagent (P1.4). Empty = the
	// node/flow defaults apply.
	AgentRef   string       `json:"agent_ref,omitempty"`
	WriteScope []string     `json:"write_scope,omitempty"`
	Retry      *RetryPolicy  `json:"retry,omitempty"`
	Decision   *DecisionSpec `json:"decision,omitempty"`
	Human      *HumanSpec    `json:"human,omitempty"`
	Branch     *BranchSpec   `json:"branch,omitempty"`
	Loop       *LoopSpec     `json:"loop,omitempty"`
	Function   *FunctionSpec `json:"function,omitempty"`
	TimeoutSec int           `json:"timeout_sec,omitempty"`
	// PreScript / PostScript are optional bash scripts executed before / after
	// the node's main work (E3b). They run in the agent's workspace with a
	// short default timeout; stdout/stderr are captured onto the node's
	// output map (script.pre_stdout / post_stdout) so downstream nodes can
	// read them.
	PreScript  string `json:"pre_script,omitempty"`
	PostScript string `json:"post_script,omitempty"`
	// Outputs declares the typed fields this node produces (Flow Spec §3.4).
	// Downstream nodes reference them as {{nodes.<id>.outputs.<field>}}.
	// Empty = no typed outputs (prompt/handoff text only).
	Outputs []VarDef `json:"outputs,omitempty"`
}

// RetryPolicy mirrors Temporal's RetryPolicy (Flow Spec §3.5). Semantic
// failures (verdict=fail, permission denied, schema violations) are never
// retried; they route through branch/repair edges instead.
type RetryPolicy struct {
	MaxAttempts        int      `json:"max_attempts,omitempty"` // including the first attempt; 1 = no retry
	InitialIntervalMS  int      `json:"initial_interval_ms,omitempty"`
	BackoffCoefficient float64  `json:"backoff_coefficient,omitempty"`
	MaxIntervalMS      int      `json:"max_interval_ms,omitempty"`
	Jitter             float64  `json:"jitter,omitempty"`
	RetryOn            []string `json:"retry_on,omitempty"`
	NonRetryable       []string `json:"non_retryable,omitempty"`
}

// DecisionSpec configures a decision node (Flow Spec §3.2).
type DecisionSpec struct {
	Provider      string   `json:"provider,omitempty"`
	DecisionType  string   `json:"decision_type,omitempty"` // choice | boolean | score
	Choices       []Choice `json:"choices,omitempty"`
	TimeoutMS     int      `json:"timeout_ms,omitempty"`
	OnError       string   `json:"on_error,omitempty"` // fail_closed | fail_open | fail
	DefaultChoice string   `json:"default_choice,omitempty"`
}

// Choice is one allowed verdict of a choice-type decision.
type Choice struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// HumanSpec configures a human fallback node (Flow Spec §3.2; task store F2).
type HumanSpec struct {
	Queue          string   `json:"queue"`
	AssigneePolicy string   `json:"assignee_policy,omitempty"` // any | role:<id>
	Form           any      `json:"form,omitempty"`            // ui_card card/form JSON
	Prompt         string   `json:"prompt,omitempty"`
	TimeoutMS      int      `json:"timeout_ms,omitempty"`
	OnTimeout      string   `json:"on_timeout,omitempty"` // escalate:<queue> | llm | fail
	ResultVar      string   `json:"result_var,omitempty"`
}

// BranchSpec is a no-job routing gateway (Flow Spec §3.2). Cases are matched
// in order; the first hit routes to its target. DefaultTo is mandatory.
type BranchSpec struct {
	Cases     []BranchCase `json:"cases"`
	DefaultTo string       `json:"default_to"`
}

// BranchCase is one output port of a branch.
type BranchCase struct {
	Name      string    `json:"name,omitempty"`
	To        string    `json:"to"`
	Condition Condition `json:"condition"`
}

// LoopSpec compiles to control_flow append edges (Flow Spec §3.2). F1a
// supports a single iteration append per exit condition.
type LoopSpec struct {
	Body          []string  `json:"body"`
	ExitWhen      Condition `json:"exit_when"`
	MaxIterations int       `json:"max_iterations"`
	IterationKey  string    `json:"iteration_key,omitempty"`
}

// Function runtime values (P3 node library).
const (
	FunctionRuntimeJS   = "js"   // goja sandbox: no network/fs, ctx read/write + log + utils
	FunctionRuntimeWasm = "wasm" // wasmrt plugin (godex:plugin@0.1 ABI), ref = node-library id
)

// FunctionSpec configures a function (code) node (Flow Spec §3.2, P3): a
// pure compute step that runs a handler against the unified context and
// returns events. It carries either inline JS source or a node-library ref.
type FunctionSpec struct {
	Runtime string `json:"runtime"` // js | wasm
	// Source is the JS handler source for runtime=js:
	//   export function handle(ctx, event) { return [ {...} ] }
	Source string `json:"source,omitempty"`
	// Ref is the node-library entry id for runtime=wasm (or a shared JS lib).
	Ref string `json:"ref,omitempty"`
	// Handler is the entry function name; defaults to "handle".
	Handler string `json:"handler,omitempty"`
	// InputSchema / OutputSchema declare the event/result shape (JSON Schema).
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// Edge connects two nodes. data_dependency/handoff edges reference static
// nodes; condition edges append their target as a template when From matches.
type Edge struct {
	ID            string     `json:"id,omitempty"`
	From          string     `json:"from"`
	To            string     `json:"to"`
	EdgeType      string     `json:"edge_type,omitempty"` // data_dependency (default) | handoff | condition
	When          *Condition `json:"when,omitempty"`
	MaxIterations int        `json:"max_iterations,omitempty"`
	IterationKey  string     `json:"iteration_key,omitempty"`
}

// Condition is a structured predicate over declared node outputs (Flow Spec
// §3.3). No arbitrary expressions are allowed.
type Condition struct {
	Status     string        `json:"status,omitempty"`
	Verdict    string        `json:"verdict,omitempty"`
	Node       string        `json:"node,omitempty"` // predicate target; default = edge From
	Choice     string        `json:"choice,omitempty"`
	Confidence *NumCompare   `json:"confidence,omitempty"`
	Output     *FieldCompare `json:"output,omitempty"`
	// Not negates the whole sub-predicate. Loop exit_when compiles to
	// Not(exit_when) as the continue-iterating condition (P1.5).
	Not *Condition  `json:"not,omitempty"`
	All []Condition `json:"all,omitempty"`
	Any []Condition `json:"any,omitempty"`
}

// NumCompare is a numeric comparison.
type NumCompare struct {
	Op    string  `json:"op"` // gt|gte|lt|lte|eq
	Value float64 `json:"value"`
}

// FieldCompare is a dotted-path field predicate.
type FieldCompare struct {
	Path  string `json:"path"`
	Op    string `json:"op"` // eq|ne|in|not_in|contains|gt|gte|lt|lte
	Value any    `json:"value"`
}

// ConditionEmpty reports whether c carries no predicate at all.
func ConditionEmpty(c Condition) bool {
	return c.Status == "" && c.Verdict == "" && c.Node == "" && c.Choice == "" &&
		c.Confidence == nil && c.Output == nil && c.Not == nil &&
		len(c.All) == 0 && len(c.Any) == 0
}
