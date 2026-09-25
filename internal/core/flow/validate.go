package flow

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const MaxTimeoutSeconds = 30 * 24 * 60 * 60
const MaxNetworkResponseChars = 64 << 20

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
	if err := validateDefinitionHeader(d); err != nil {
		return err
	}
	byID, err := indexDefinitionNodes(d)
	if err != nil {
		return err
	}
	if err := validateFlowOutputSources(d, byID); err != nil {
		return err
	}
	if err := validateStaticNodes(d, byID); err != nil {
		return err
	}
	if err := validateDependencyEdges(d, byID); err != nil {
		return err
	}
	return validateExpandedNodeLimit(d)
}

func validateDefinitionHeader(d *Definition) error {
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
	if d.TimeoutSec < 0 || d.TimeoutSec > MaxTimeoutSeconds {
		return ValidationError{Path: "timeout_sec", Msg: fmt.Sprintf("must be between 0 and %d seconds", MaxTimeoutSeconds)}
	}
	if d.Network != nil {
		if err := validateNetworkPolicy(d.Network); err != nil {
			return err
		}
	}
	if d.OnComplete != nil {
		if strings.TrimSpace(d.OnComplete.URL) == "" {
			return ValidationError{Path: "on_complete.url", Msg: "on_complete requires a callback url"}
		}
	}
	return nil
}

func indexDefinitionNodes(d *Definition) (map[string]Node, error) {
	byID := make(map[string]Node, len(d.Nodes))
	for _, n := range d.Nodes {
		id := strings.TrimSpace(n.ID)
		if id == "" {
			return nil, ValidationError{Path: "nodes", Msg: "node with empty id"}
		}
		if _, dup := byID[id]; dup {
			return nil, ValidationError{Path: "nodes", Msg: fmt.Sprintf("duplicate node id %q", id)}
		}
		byID[id] = n
	}
	return byID, nil
}

func validateStaticNodes(d *Definition, byID map[string]Node) error {
	for _, n := range d.Nodes {
		if err := validateStaticNode(d, n, byID); err != nil {
			return err
		}
	}
	return nil
}

func validateStaticNode(d *Definition, n Node, byID map[string]Node) error {
	p := "nodes[" + n.ID + "]"
	kind := normalizeKind(n.Kind)
	if kind == "" {
		return ValidationError{Path: p, Msg: fmt.Sprintf("unknown node kind %q", n.Kind)}
	}
	if n.TimeoutSec < 0 || n.TimeoutSec > MaxTimeoutSeconds {
		return ValidationError{Path: p + ".timeout_sec", Msg: fmt.Sprintf("must be between 0 and %d seconds", MaxTimeoutSeconds)}
	}
	if n.TimeoutSec > 0 && (kind == KindBranch || kind == KindLoop) {
		return ValidationError{Path: p + ".timeout_sec", Msg: "timeouts are not supported on branch and loop control nodes"}
	}
	if kind != KindBranch && kind != KindLoop && kind != KindFunction && kind != KindService && strings.TrimSpace(n.Prompt) == "" {
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
	if err := validateVarRefs(n.Prompt, p+".prompt", d, byID); err != nil {
		return err
	}
	return validateNodeKindSpec(n, kind, p, d, byID)
}

func validateNodeKindSpec(n Node, kind, path string, d *Definition, byID map[string]Node) error {
	switch kind {
	case KindDecision:
		if n.Decision == nil {
			return ValidationError{Path: path, Msg: "decision node missing decision spec"}
		}
		return validateDecision(n.Decision, path)
	case KindHuman:
		if n.Human == nil {
			return ValidationError{Path: path, Msg: "human node missing human spec"}
		}
		return validateHuman(n.Human, path)
	case KindBranch:
		if n.Branch == nil {
			return ValidationError{Path: path, Msg: "branch node missing branch spec"}
		}
		return validateBranch(n.Branch, path, byID)
	case KindLoop:
		if n.Loop == nil {
			return ValidationError{Path: path, Msg: "loop node missing loop spec"}
		}
		return validateLoop(n.Loop, path, byID)
	case KindFunction:
		return validateFunction(n.Function, path)
	case KindService:
		if err := validateServiceSpec(n.Service, path+".service"); err != nil {
			return err
		}
		return validateServiceVarRefs(n.Service, path+".service", d, byID)
	default:
		return nil
	}
}

func validateDependencyEdges(d *Definition, byID map[string]Node) error {
	depGraph := make(map[string][]string, len(d.Nodes))
	for _, id := range keys(byID) {
		depGraph[id] = nil
	}
	for _, edge := range d.Edges {
		if err := validateDependencyEdge(edge, byID, depGraph); err != nil {
			return err
		}
	}
	if cycle := findCycle(depGraph); cycle != nil {
		return ValidationError{Path: "edges", Msg: fmt.Sprintf("dependency cycle: %s", strings.Join(cycle, " -> "))}
	}
	return nil
}

func validateDependencyEdge(e Edge, byID map[string]Node, depGraph map[string][]string) error {
	path := "edges[" + e.ID + "]"
	if _, ok := byID[e.From]; !ok {
		return ValidationError{Path: path, Msg: fmt.Sprintf("edge from unknown node %q", e.From)}
	}
	if _, ok := byID[e.To]; !ok {
		return ValidationError{Path: path, Msg: fmt.Sprintf("edge to unknown node %q", e.To)}
	}
	switch normalizeEdgeType(e.EdgeType) {
	case EdgeDataDependency, EdgeHandoff:
		if e.When != nil && !ConditionEmpty(*e.When) {
			return ValidationError{Path: path, Msg: "data_dependency/handoff edges must not carry a when predicate"}
		}
		depGraph[e.To] = append(depGraph[e.To], e.From)
	case EdgeCondition:
		if e.When == nil || ConditionEmpty(*e.When) {
			return ValidationError{Path: path, Msg: "condition edge requires a when predicate"}
		}
		return validateCondition(*e.When, path, byID)
	default:
		return ValidationError{Path: path, Msg: fmt.Sprintf("unknown edge type %q", e.EdgeType)}
	}
	return nil
}

func validateExpandedNodeLimit(d *Definition) error {
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
	case KindStep, KindLLM, KindDecision, KindHuman, KindBranch, KindLoop, KindFunction, KindService:
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
		t := strings.ToLower(strings.TrimSpace(v.Type))
		if t != "" {
			switch t {
			case "string", "number", "boolean", "object", "array", "any":
			default:
				return ValidationError{Path: fmt.Sprintf("%s[%d].type", p, i), Msg: fmt.Sprintf("invalid variable type %q", v.Type)}
			}
		}
		if len(v.Schema) > 0 {
			if t != "" && t != "object" && t != "array" && t != "any" {
				return ValidationError{Path: fmt.Sprintf("%s[%d].schema", p, i), Msg: fmt.Sprintf("nested schema only allowed for object/array/any, got %q", t)}
			}
			schemaPath := fmt.Sprintf("%s[%d].schema", p, i)
			schema, err := decodeSchemaDefinition(v.Schema, schemaPath)
			if err != nil {
				return ValidationError{Msg: err.Error()}
			}
			if schema == nil {
				return ValidationError{Path: schemaPath, Msg: "variable schema must be a JSON object"}
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
	if err := validateVarDefs(d.Outputs, "outputs"); err != nil {
		return err
	}
	return nil
}

func validateFlowOutputSources(d *Definition, byID map[string]Node) error {
	for i, output := range d.Outputs {
		source := strings.TrimSpace(output.Source)
		if source == "" {
			if output.Required {
				return ValidationError{Path: fmt.Sprintf("outputs[%d].source", i), Msg: "required flow outputs must declare a source"}
			}
			// An unbound optional output is retained for backward
			// compatibility with definitions that declared metadata before
			// Flow-level output mapping was supported.
			continue
		}
		nodeID, field, ok := ParseOutputSource(source)
		path := fmt.Sprintf("outputs[%d].source", i)
		if !ok {
			return ValidationError{Path: path, Msg: `must use the form "nodes.<node_id>.outputs.<field>"`}
		}
		node, exists := byID[nodeID]
		if !exists {
			return ValidationError{Path: path, Msg: fmt.Sprintf("unknown output node %q", nodeID)}
		}
		sourceType, exists := declaredNodeOutputType(node, field)
		if !exists {
			return ValidationError{Path: path, Msg: fmt.Sprintf("node %q does not declare output %q", nodeID, field)}
		}
		outputType := strings.ToLower(strings.TrimSpace(output.Type))
		sourceType = strings.ToLower(strings.TrimSpace(sourceType))
		if outputType != "" && sourceType != "" && sourceType != "any" && outputType != "any" && outputType != sourceType {
			return ValidationError{Path: path, Msg: fmt.Sprintf("output type %q does not match source type %q", outputType, sourceType)}
		}
	}
	return nil
}

func declaredNodeOutputType(node Node, field string) (string, bool) {
	for _, output := range node.Outputs {
		if strings.TrimSpace(output.Name) == field {
			return output.Type, true
		}
	}
	switch normalizeKind(node.Kind) {
	case KindDecision:
		switch field {
		case "choice", "question", "error", "model":
			return "string", true
		case "confidence", "score", "latency_ms":
			return "number", true
		case "calibrated":
			return "boolean", true
		case "raw":
			return "object", true
		}
	case KindBranch:
		if field == "choice" {
			return "string", true
		}
	case KindHuman:
		if node.Human != nil && strings.TrimSpace(node.Human.ResultVar) == field {
			return "any", true
		}
	}
	return "", false
}

// validateNetworkPolicy checks the Flow-level outbound network policy (E3a):
// policy is allow_all|allowlist, domain lists are non-empty clean strings.
func validateNetworkPolicy(np *NetworkPolicy) error {
	p := strings.ToLower(strings.TrimSpace(np.Policy))
	switch p {
	case "", "allow_all", "allowlist":
	default:
		return ValidationError{Path: "network.policy", Msg: fmt.Sprintf("invalid network policy %q (allow_all|allowlist)", np.Policy)}
	}
	if p == "allowlist" && len(np.AllowedDomains) == 0 {
		return ValidationError{Path: "network.allowed_domains", Msg: "allowlist policy requires at least one allowed domain"}
	}
	if np.AllowPrivateHosts && (p != "allowlist" || len(np.AllowedDomains) == 0) {
		return ValidationError{Path: "network.allow_private_hosts", Msg: "private hosts require an explicit domain allowlist"}
	}
	if np.TimeoutSeconds < 0 || np.TimeoutSeconds > MaxTimeoutSeconds {
		return ValidationError{Path: "network.timeout_seconds", Msg: fmt.Sprintf("must be between 0 and %d seconds", MaxTimeoutSeconds)}
	}
	if np.MaxResponseChars < 0 || np.MaxResponseChars > MaxNetworkResponseChars {
		return ValidationError{Path: "network.max_response_chars", Msg: fmt.Sprintf("must be between 0 and %d", MaxNetworkResponseChars)}
	}
	return nil
}

func validateServiceSpec(spec *ServiceSpec, path string) error {
	if spec == nil {
		return ValidationError{Path: path, Msg: "service node missing service spec"}
	}
	method := strings.ToUpper(strings.TrimSpace(spec.Method))
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return ValidationError{Path: path + ".method", Msg: "must be GET, POST, PUT, PATCH, or DELETE"}
	}
	rawURL := strings.TrimSpace(spec.URL)
	if rawURL == "" {
		return ValidationError{Path: path + ".url", Msg: "service URL is required"}
	}
	probeURL := workflowURLTemplate.ReplaceAllString(rawURL, "godex-value")
	parsed, err := url.Parse(probeURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return ValidationError{Path: path + ".url", Msg: "must be an absolute http or https URL without userinfo"}
	}
	seenHeaders := make(map[string]struct{}, len(spec.Headers))
	for name, value := range spec.Headers {
		if !validHTTPHeaderName(name) {
			return ValidationError{Path: path + ".headers", Msg: fmt.Sprintf("invalid header name %q", name)}
		}
		lowerName := strings.ToLower(name)
		if _, exists := seenHeaders[lowerName]; exists {
			return ValidationError{Path: path + ".headers", Msg: fmt.Sprintf("duplicate header name %q", name)}
		}
		seenHeaders[lowerName] = struct{}{}
		if strings.ContainsAny(value, "\r\n") {
			return ValidationError{Path: path + ".headers." + name, Msg: "header value must not contain newlines"}
		}
		switch lowerName {
		case "authorization", "proxy-authorization", "x-api-key":
			return ValidationError{Path: path + ".headers." + name, Msg: "use service.auth environment reference for credentials"}
		default:
			if isServiceTransportHeader(lowerName) {
				return ValidationError{Path: path + ".headers." + name, Msg: "transport-managed header is not allowed"}
			}
		}
	}
	if len(spec.Body) > 0 && !json.Valid(spec.Body) {
		return ValidationError{Path: path + ".body", Msg: "must be valid JSON"}
	}
	if spec.Auth != nil {
		authType := strings.ToLower(strings.TrimSpace(spec.Auth.Type))
		tokenEnv := strings.TrimSpace(spec.Auth.TokenEnv)
		if !validEnvironmentVariableName(tokenEnv) {
			return ValidationError{Path: path + ".auth.token_env", Msg: "must be a valid environment variable name"}
		}
		switch authType {
		case "bearer":
			if strings.TrimSpace(spec.Auth.HeaderName) != "" {
				return ValidationError{Path: path + ".auth.header_name", Msg: "is only valid for api_key authentication"}
			}
		case "api_key":
			if !validHTTPHeaderName(spec.Auth.HeaderName) {
				return ValidationError{Path: path + ".auth.header_name", Msg: "must be a valid HTTP header name"}
			}
			headerName := strings.ToLower(strings.TrimSpace(spec.Auth.HeaderName))
			if headerName == "authorization" || headerName == "proxy-authorization" || isServiceTransportHeader(headerName) {
				return ValidationError{Path: path + ".auth.header_name", Msg: "must not override an authorization transport header"}
			}
			if _, exists := seenHeaders[headerName]; exists {
				return ValidationError{Path: path + ".auth.header_name", Msg: "must not duplicate a service header"}
			}
		default:
			return ValidationError{Path: path + ".auth.type", Msg: "must be bearer or api_key"}
		}
	}
	return nil
}

// ValidateServiceSpec validates a service-node request contract independently
// of a full Flow Definition.
func ValidateServiceSpec(spec *ServiceSpec) error {
	return validateServiceSpec(spec, "service")
}

func isServiceTransportHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connection", "content-length", "host", "keep-alive", "proxy-connection",
		"te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

var (
	workflowURLTemplate       = regexp.MustCompile(`\{\{\s*[a-zA-Z_][a-zA-Z0-9_.]*\s*\}\}`)
	environmentVariableNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func validEnvironmentVariableName(name string) bool {
	return environmentVariableNameRE.MatchString(name)
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			continue
		}
		return false
	}
	return true
}

func validateServiceVarRefs(spec *ServiceSpec, path string, d *Definition, byID map[string]Node) error {
	if err := validateVarRefs(spec.URL, path+".url", d, byID); err != nil {
		return err
	}
	for name, value := range spec.Headers {
		if err := validateVarRefs(value, path+".headers."+name, d, byID); err != nil {
			return err
		}
	}
	if len(spec.Body) == 0 {
		return nil
	}
	var body any
	if err := json.Unmarshal(spec.Body, &body); err != nil {
		return ValidationError{Path: path + ".body", Msg: "must be valid JSON"}
	}
	var visit func(any, string) error
	visit = func(value any, currentPath string) error {
		switch current := value.(type) {
		case string:
			return validateVarRefs(current, currentPath, d, byID)
		case map[string]any:
			for key, child := range current {
				if err := visit(child, currentPath+"."+key); err != nil {
					return err
				}
			}
		case []any:
			for i, child := range current {
				if err := visit(child, fmt.Sprintf("%s[%d]", currentPath, i)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(body, path+".body")
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
	for _, schema := range []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "input_schema", raw: f.InputSchema},
		{name: "output_schema", raw: f.OutputSchema},
	} {
		if _, err := decodeSchemaDefinition(schema.raw, p+".function."+schema.name); err != nil {
			return ValidationError{Path: p + ".function." + schema.name, Msg: err.Error()}
		}
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
