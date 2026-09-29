package flow

import (
	"fmt"
	"strings"
	"testing"
)

// sampleFlow builds a "step → decision → (branch: auto/llm)" flow used by
// compile tests. n.auto / n.llm are branch targets (append templates).
func sampleFlow() *Definition {
	return &Definition{
		FlowID:  "fl_order_recovery",
		Version: "1",
		Status:  "draft",
		Nodes: []Node{
			{ID: "start", Kind: KindStep, Prompt: "classify the order issue"},
			{ID: "decide", Kind: KindDecision, Prompt: "should we auto-refund?",
				Decision: &DecisionSpec{DecisionType: "choice", Choices: []Choice{{ID: "auto"}, {ID: "llm"}}}},
			{ID: "auto", Kind: KindStep, Prompt: "auto-refund the order"},
			{ID: "llm", Kind: KindStep, Prompt: "escalate to an agent"},
			{ID: "br", Kind: KindBranch,
				Branch: &BranchSpec{
					Cases: []BranchCase{
						{Name: "auto", To: "auto", Condition: Condition{Choice: "auto"}},
						{Name: "llm", To: "llm", Condition: Condition{Choice: "llm"}},
					},
					DefaultTo: "llm",
				}},
		},
		Edges: []Edge{
			{ID: "e1", From: "start", To: "decide", EdgeType: EdgeDataDependency},
			{ID: "e2", From: "decide", To: "br", EdgeType: EdgeDataDependency},
			{ID: "e3", From: "start", To: "decide", EdgeType: EdgeHandoff},
		},
	}
}

func TestValidateRejectsEmptyFlowID(t *testing.T) {
	d := sampleFlow()
	d.FlowID = ""
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "flow_id") {
		t.Fatalf("expected flow_id error, got %v", err)
	}
}

func TestExecutionModeValidationAndDigest(t *testing.T) {
	d := sampleFlow()
	requestDefault, err := Compile(d)
	if err != nil {
		t.Fatalf("compile default request flow: %v", err)
	}
	d.ExecutionMode = ExecutionModeRequest
	requestExplicit, err := Compile(d)
	if err != nil {
		t.Fatalf("compile explicit request flow: %v", err)
	}
	if requestDefault.Digest != requestExplicit.Digest {
		t.Fatalf("omitted and explicit request modes should keep the same digest: %s != %s", requestDefault.Digest, requestExplicit.Digest)
	}
	if requestExplicit.ExecutionMode != ExecutionModeRequest {
		t.Fatalf("expected compiled request mode, got %q", requestExplicit.ExecutionMode)
	}

	d.ExecutionMode = ExecutionModeSession
	d.SessionWorkflow = &SessionWorkflowSpec{Triggers: []SessionTrigger{
		{EventType: "state.updated", EntryNode: "update", Delivery: SessionDeliveryLatestWins},
	}}
	d.Nodes = []Node{
		{ID: "update", Kind: KindFunction, Function: &FunctionSpec{
			Runtime: FunctionRuntimeJS,
			Source:  `function handle(ctx, event) { return {}; }`,
		}},
	}
	d.Edges = nil
	session, err := Compile(d)
	if err != nil {
		t.Fatalf("compile session flow: %v", err)
	}
	if session.ExecutionMode != ExecutionModeSession || session.Digest == requestDefault.Digest {
		t.Fatalf("session mode must be compiled and digest-distinct: %+v", session)
	}

	d.ExecutionMode = "unknown"
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "execution_mode") {
		t.Fatalf("expected execution_mode validation error, got %v", err)
	}
}

func TestSessionWorkflowTriggerValidationAndDigest(t *testing.T) {
	d := &Definition{
		FlowID:        "fl_session_trigger",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: ExecutionModeSession,
		SessionWorkflow: &SessionWorkflowSpec{Triggers: []SessionTrigger{
			{EventType: "state.updated", EntryNode: "update", LaneID: "fast"},
		}, Lanes: []SessionLane{{
			ID: "fast", Cadence: SessionLaneCadenceEvent, DeadlineMS: 250,
			Class: SessionLaneClassFast,
		}}},
		Nodes: []Node{
			{ID: "update", Kind: KindFunction, Function: &FunctionSpec{
				Runtime: FunctionRuntimeJS,
				Source:  `function handle(ctx, event) { return {}; }`,
			}},
			{ID: "derive", Kind: KindFunction, Function: &FunctionSpec{
				Runtime: FunctionRuntimeJS,
				Source:  `function handle(ctx, event) { return {}; }`,
			}},
		},
		Edges: []Edge{
			{ID: "update_to_derive", From: "update", To: "derive", EdgeType: EdgeDataDependency},
		},
	}
	compiled, err := Compile(d)
	if err != nil {
		t.Fatalf("compile session function region: %v", err)
	}
	if compiled.SessionWorkflow == nil || compiled.SessionWorkflow.Triggers[0].Delivery != SessionDeliveryDurable {
		t.Fatalf("expected normalized durable trigger: %+v", compiled.SessionWorkflow)
	}
	if compiled.SessionWorkflow.Triggers[0].LaneID != "fast" ||
		compiled.SessionWorkflow.Lanes[0].Cadence != SessionLaneCadenceEvent ||
		compiled.SessionWorkflow.Lanes[0].Class != SessionLaneClassFast ||
		compiled.SessionWorkflow.Lanes[0].OverloadPolicy != SessionLaneOverloadCoalesceLatest {
		t.Fatalf("expected normalized lane contract: %+v", compiled.SessionWorkflow)
	}
	changed := *d
	changed.SessionWorkflow = &SessionWorkflowSpec{Triggers: []SessionTrigger{
		{EventType: "state.updated", EntryNode: "update", Delivery: SessionDeliveryLatestWins, LaneID: "fast"},
	}, Lanes: []SessionLane{{
		ID: "fast", Cadence: SessionLaneCadenceEvent, DeadlineMS: 250,
		Class: SessionLaneClassFast,
	}}}
	compiledChanged, err := Compile(&changed)
	if err != nil {
		t.Fatalf("compile latest-wins trigger: %v", err)
	}
	if compiled.Digest == compiledChanged.Digest {
		t.Fatal("trigger delivery policy must participate in the compiled digest")
	}

	invalid := *d
	invalid.SessionWorkflow = nil
	if err := Validate(&invalid); err == nil || !strings.Contains(err.Error(), "session_workflow.triggers") {
		t.Fatalf("expected missing session trigger rejection, got %v", err)
	}
	invalid = *d
	invalid.SessionWorkflow = &SessionWorkflowSpec{Triggers: []SessionTrigger{
		{EventType: "state.updated", EntryNode: "derive"},
	}}
	if err := Validate(&invalid); err == nil || !strings.Contains(err.Error(), "incoming") {
		t.Fatalf("expected a trigger entry with dependencies to be rejected, got %v", err)
	}
}

func TestSessionLaneValidation(t *testing.T) {
	base := &Definition{
		FlowID:        "fl_session_lanes",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: ExecutionModeSession,
		SessionWorkflow: &SessionWorkflowSpec{
			Triggers: []SessionTrigger{{EventType: "state.tick", EntryNode: "update", Delivery: SessionDeliveryLatestWins, LaneID: "fast"}},
			Lanes: []SessionLane{{
				ID: "fast", Cadence: SessionLaneCadenceHybrid, IntervalMS: 100,
				DeadlineMS: 500, Class: SessionLaneClassFast,
			}},
		},
		Nodes: []Node{{ID: "update", Kind: KindFunction, Function: &FunctionSpec{
			Runtime: FunctionRuntimeJS,
			Source:  `function handle(ctx, event) { return {}; }`,
		}}},
	}
	if _, err := Compile(base); err != nil {
		t.Fatalf("compile valid hybrid lane: %v", err)
	}

	tests := []struct {
		name   string
		change func(*Definition)
		want   string
	}{
		{
			name: "unknown lane reference",
			change: func(def *Definition) {
				def.SessionWorkflow.Triggers[0].LaneID = "missing"
			},
			want: "unknown lane",
		},
		{
			name: "periodic lane requires latest wins",
			change: func(def *Definition) {
				def.SessionWorkflow.Triggers[0].Delivery = SessionDeliveryDurable
			},
			want: "require latest_wins",
		},
		{
			name: "periodic lane requires interval",
			change: func(def *Definition) {
				def.SessionWorkflow.Lanes[0].IntervalMS = 0
			},
			want: "interval_ms",
		},
		{
			name: "lane must be assigned once",
			change: func(def *Definition) {
				def.SessionWorkflow.Triggers = append(def.SessionWorkflow.Triggers,
					SessionTrigger{EventType: "state.other", EntryNode: "update", Delivery: SessionDeliveryLatestWins, LaneID: "fast"})
			},
			want: "only one trigger",
		},
		{
			name: "deadline is required",
			change: func(def *Definition) {
				def.SessionWorkflow.Lanes[0].DeadlineMS = 0
			},
			want: "deadline_ms",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			def := *base
			spec := *base.SessionWorkflow
			spec.Triggers = append([]SessionTrigger(nil), base.SessionWorkflow.Triggers...)
			spec.Lanes = append([]SessionLane(nil), base.SessionWorkflow.Lanes...)
			def.SessionWorkflow = &spec
			test.change(&def)
			err := Validate(&def)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected validation error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestSessionServiceTemplateScopesAreSessionOnly(t *testing.T) {
	d := &Definition{
		FlowID:        "fl_session_service_templates",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: ExecutionModeSession,
		SessionWorkflow: &SessionWorkflowSpec{Triggers: []SessionTrigger{
			{EventType: "turn.update", EntryNode: "call"},
		}},
		Nodes: []Node{{
			ID:   "call",
			Kind: KindService,
			Service: &ServiceSpec{
				Method: "POST",
				URL:    "https://api.example.com/{{event.payload.path}}",
				Body:   []byte(`{"dialog":"{{session.state.dialog_id}}","seq":"{{event.sequence}}"}`),
			},
		}},
	}
	if _, err := Compile(d); err != nil {
		t.Fatalf("compile session service templates: %v", err)
	}

	request := *d
	request.ExecutionMode = ExecutionModeRequest
	request.SessionWorkflow = nil
	if err := Validate(&request); err == nil || !strings.Contains(err.Error(), `unknown variable scope "event"`) {
		t.Fatalf("expected request service to reject session event template, got %v", err)
	}

	invalid := *d
	invalid.Nodes = append([]Node(nil), d.Nodes...)
	invalid.Nodes[0].Service = &ServiceSpec{
		Method: "POST",
		URL:    "https://api.example.com/{{event.missing}}",
	}
	if err := Validate(&invalid); err == nil || !strings.Contains(err.Error(), "unknown session event field") {
		t.Fatalf("expected unknown event field rejection, got %v", err)
	}
}

func TestSessionLLMNodesSupportEventAndSessionPromptScopes(t *testing.T) {
	d := &Definition{
		FlowID:        "fl_session_llm_prompts",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: ExecutionModeSession,
		SessionWorkflow: &SessionWorkflowSpec{Triggers: []SessionTrigger{
			{EventType: "turn.update", EntryNode: "answer"},
		}},
		Nodes: []Node{{
			ID:      "answer",
			Kind:    KindLLM,
			Prompt:  "Use {{event.payload.text}} and {{session.state.dialog_id}}.",
			Outputs: []VarDef{{Name: "answer", Type: "string"}},
		}},
	}
	compiled, err := Compile(d)
	if err != nil {
		t.Fatalf("compile session LLM node: %v", err)
	}
	if len(compiled.Nodes) != 1 || compiled.Nodes[0].Kind != KindLLM {
		t.Fatalf("expected the LLM node to remain in the compiled session region, got %+v", compiled.Nodes)
	}

	request := *d
	request.ExecutionMode = ExecutionModeRequest
	request.SessionWorkflow = nil
	if err := Validate(&request); err == nil || !strings.Contains(err.Error(), `unknown variable scope "event"`) {
		t.Fatalf("expected request-mode LLM prompt to reject session context, got %v", err)
	}

	invalid := *d
	invalid.Nodes = append([]Node(nil), d.Nodes...)
	invalid.Nodes[0].Outputs = nil
	if err := Validate(&invalid); err == nil || !strings.Contains(err.Error(), "must declare outputs") {
		t.Fatalf("expected session LLM without outputs to be rejected, got %v", err)
	}
}

func TestSessionAgentNodesRequireDurableDeliveryAndDeclaredOutputs(t *testing.T) {
	d := &Definition{
		FlowID:        "fl_session_agent_nodes",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: ExecutionModeSession,
		SessionWorkflow: &SessionWorkflowSpec{Triggers: []SessionTrigger{
			{EventType: "turn.update", EntryNode: "agent"},
		}},
		Nodes: []Node{{
			ID:      "agent",
			Kind:    KindStep,
			Prompt:  "Handle {{event.payload.text}} for {{inputs.locale}}",
			Outputs: []VarDef{{Name: "answer", Type: "string", Required: true}},
		}},
		Inputs: []VarDef{{Name: "locale", Type: "string", Required: true}},
	}
	compiled, err := Compile(d)
	if err != nil {
		t.Fatalf("compile durable session Agent node: %v", err)
	}
	if len(compiled.Nodes) != 1 || compiled.Nodes[0].Kind != KindStep {
		t.Fatalf("expected durable Agent node in compiled session region, got %+v", compiled.Nodes)
	}

	latestWins := *d
	latestWins.SessionWorkflow = &SessionWorkflowSpec{Triggers: []SessionTrigger{
		{EventType: "turn.update", EntryNode: "agent", Delivery: SessionDeliveryLatestWins},
	}}
	if err := Validate(&latestWins); err == nil || !strings.Contains(err.Error(), "requires durable event delivery") {
		t.Fatalf("expected Agent node on latest_wins trigger to be rejected, got %v", err)
	}

	missingOutputs := *d
	missingOutputs.Nodes = []Node{d.Nodes[0]}
	missingOutputs.Nodes[0].Outputs = nil
	if err := Validate(&missingOutputs); err == nil || !strings.Contains(err.Error(), "must declare outputs") {
		t.Fatalf("expected Agent node without declared outputs to be rejected, got %v", err)
	}
}

func TestSessionBranchRoutesRequireDurableAgentTargetsAndAllowChains(t *testing.T) {
	d := &Definition{
		FlowID:        "fl_session_branch_agent_targets",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: ExecutionModeSession,
		SessionWorkflow: &SessionWorkflowSpec{Triggers: []SessionTrigger{
			{EventType: "turn.update", EntryNode: "classify"},
		}},
		Nodes: []Node{
			{
				ID:   "classify",
				Kind: KindFunction,
				Function: &FunctionSpec{
					Runtime: FunctionRuntimeJS,
					Source:  `function handle() { return {choice: "yes"}; }`,
				},
				Outputs: []VarDef{{Name: "choice", Type: "string"}},
			},
			{
				ID:   "route",
				Kind: KindBranch,
				Branch: &BranchSpec{
					Cases: []BranchCase{{
						Name: "agent",
						To:   "agent",
						Condition: Condition{Output: &FieldCompare{
							Path: "choice", Op: "eq", Value: "yes",
						}},
					}},
					DefaultTo: "agent",
				},
			},
			{
				ID:      "agent",
				Kind:    KindStep,
				Prompt:  "handle the turn",
				Outputs: []VarDef{{Name: "answer", Type: "string", Required: true}},
			},
		},
		Edges: []Edge{{
			ID: "classify-route", From: "classify", To: "route", EdgeType: EdgeDataDependency,
		}},
	}
	if _, err := Compile(d); err != nil {
		t.Fatalf("compile durable branch-to-Agent route: %v", err)
	}

	latestWins := *d
	latestWins.SessionWorkflow = &SessionWorkflowSpec{Triggers: []SessionTrigger{
		{EventType: "turn.update", EntryNode: "classify", Delivery: SessionDeliveryLatestWins},
	}}
	if err := Validate(&latestWins); err == nil || !strings.Contains(err.Error(), "requires durable event delivery") {
		t.Fatalf("expected latest-wins branch Agent target to be rejected, got %v", err)
	}

	nonTerminal := *d
	nonTerminal.Edges = append([]Edge{}, d.Edges...)
	nonTerminal.Edges = append(nonTerminal.Edges, Edge{
		ID: "agent-next", From: "agent", To: "finish", EdgeType: EdgeDataDependency,
	})
	nonTerminal.Nodes = append([]Node{}, d.Nodes...)
	nonTerminal.Nodes = append(nonTerminal.Nodes, Node{
		ID: "finish", Kind: KindFunction,
		Function: &FunctionSpec{Runtime: FunctionRuntimeJS, Source: `function handle() { return {}; }`},
	})
	if err := Validate(&nonTerminal); err != nil {
		t.Fatalf("expected durable Agent branch route chains to be accepted, got %v", err)
	}
}

func TestSessionBranchRouteValidation(t *testing.T) {
	base := &Definition{
		FlowID:        "fl_session_branch_route_names",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: ExecutionModeSession,
		SessionWorkflow: &SessionWorkflowSpec{Triggers: []SessionTrigger{
			{EventType: "turn.update", EntryNode: "classify"},
		}},
		Nodes: []Node{
			{
				ID:   "classify",
				Kind: KindFunction,
				Function: &FunctionSpec{
					Runtime: FunctionRuntimeJS,
					Source:  `function handle() { return {choice: "yes"}; }`,
				},
				Outputs: []VarDef{{Name: "choice", Type: "string"}},
			},
			{
				ID:   "route",
				Kind: KindBranch,
				Branch: &BranchSpec{
					Cases: []BranchCase{{
						Name: "yes",
						To:   "handle",
						Condition: Condition{Output: &FieldCompare{
							Path: "choice", Op: "eq", Value: "yes",
						}},
					}},
					DefaultTo: "handle",
				},
			},
			{
				ID:   "handle",
				Kind: KindFunction,
				Function: &FunctionSpec{
					Runtime: FunctionRuntimeJS,
					Source:  `function handle() { return {}; }`,
				},
			},
		},
		Edges: []Edge{{
			ID: "classify-route", From: "classify", To: "route", EdgeType: EdgeDataDependency,
		}},
	}
	if _, err := Compile(base); err != nil {
		t.Fatalf("compile valid session branch: %v", err)
	}

	for _, test := range []struct {
		name      string
		mutate    func(*Definition)
		wantError string
	}{
		{
			name: "duplicate route name",
			mutate: func(definition *Definition) {
				branch := definition.Nodes[1].Branch
				branch.Cases = append(branch.Cases, branch.Cases[0])
			},
			wantError: "has duplicate route",
		},
		{
			name: "default route name is reserved",
			mutate: func(definition *Definition) {
				branch := definition.Nodes[1].Branch
				branch.Cases[0].Name = " default "
			},
			wantError: `case name "default" is reserved`,
		},
		{
			name: "append target shared across branches",
			mutate: func(definition *Definition) {
				definition.Nodes = append(definition.Nodes, Node{
					ID:   "route_two",
					Kind: KindBranch,
					Branch: &BranchSpec{
						Cases: []BranchCase{{
							Name: "again",
							To:   "handle",
							Condition: Condition{Output: &FieldCompare{
								Path: "choice", Op: "eq", Value: "yes",
							}},
						}},
						DefaultTo: "handle",
					},
				})
				definition.Edges = append(definition.Edges, Edge{
					ID: "classify-route-two", From: "classify", To: "route_two", EdgeType: EdgeDataDependency,
				})
			},
			wantError: `is shared by branch nodes`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := *base
			definition.Nodes = append([]Node(nil), base.Nodes...)
			definition.Edges = append([]Edge(nil), base.Edges...)
			branch := *base.Nodes[1].Branch
			branch.Cases = append([]BranchCase(nil), branch.Cases...)
			definition.Nodes[1].Branch = &branch
			test.mutate(&definition)

			if _, err := Compile(&definition); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("expected %q validation error, got %v", test.wantError, err)
			}
		})
	}
}

func TestValidateRejectsDuplicateNodeID(t *testing.T) {
	d := sampleFlow()
	d.Nodes = append(d.Nodes, Node{ID: "start", Kind: KindStep, Prompt: "dup"})
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "duplicate node id") {
		t.Fatalf("expected duplicate node id error, got %v", err)
	}
}

func TestValidateRejectsMissingPrompt(t *testing.T) {
	d := sampleFlow()
	d.Nodes[0].Prompt = ""
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "requires prompt") {
		t.Fatalf("expected prompt error, got %v", err)
	}
}

func TestValidateRejectsUnknownEdgeSource(t *testing.T) {
	d := sampleFlow()
	d.Edges = append(d.Edges, Edge{ID: "bad", From: "nope", To: "decide"})
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "unknown node") {
		t.Fatalf("expected unknown node error, got %v", err)
	}
}

func TestValidateRejectsDependencyCycle(t *testing.T) {
	d := sampleFlow()
	d.Edges = append(d.Edges,
		Edge{ID: "c1", From: "decide", To: "start"},
		Edge{ID: "c2", From: "br", To: "decide"},
	)
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}

func TestValidateRejectsBranchCaseUnknownTarget(t *testing.T) {
	d := sampleFlow()
	d.Nodes[4].Branch.Cases[0].To = "missing"
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "unknown node") {
		t.Fatalf("expected branch unknown target error, got %v", err)
	}
}

func TestValidateRejectsNodeCapExceeded(t *testing.T) {
	d := sampleFlow()
	for i := 0; i < MaxNodes+1; i++ {
		d.Nodes = append(d.Nodes, Node{ID: fmt.Sprintf("extra_%d", i), Kind: KindStep, Prompt: "x"})
	}
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "exceeds cap") {
		t.Fatalf("expected cap error, got %v", err)
	}
}

func TestCompileLoopProducesNotExitEdge(t *testing.T) {
	d := sampleFlow()
	d.Nodes = append(d.Nodes, Node{ID: "loop1", Kind: KindLoop, Loop: &LoopSpec{
		Body: []string{"auto"}, ExitWhen: Condition{Choice: "done"}, MaxIterations: 3,
	}})
	d.Edges = append(d.Edges, Edge{ID: "loop_e", From: "start", To: "loop1", EdgeType: EdgeDataDependency})
	if err := Validate(d); err != nil {
		t.Fatalf("Validate should allow loop structure, got %v", err)
	}
	c, err := Compile(d)
	if err != nil {
		t.Fatalf("Compile should lower loop to a control_flow edge, got %v", err)
	}
	// The loop compiles to a source-gated entry and a dynamic continuation
	// edge whose when is the NEGATION of exit_when.
	entryFound, continueFound := false, false
	for _, e := range c.Edges {
		switch e.ID {
		case "loop1_entry":
			entryFound = true
			if e.From != "start" || e.When.Status != "completed" {
				t.Fatalf("unexpected loop entry edge: %+v", e)
			}
			if e.Append.ID != "auto_{iteration}" {
				t.Fatalf("expected an iteration-specific body node id, got %+v", e.Append)
			}
		case "loop1_continue":
			continueFound = true
			if e.FromPrefix != "auto_" {
				t.Fatalf("expected loop continuation to match body iterations, got %q", e.FromPrefix)
			}
			if e.When.Not == nil {
				t.Fatal("expected Not(exit_when) condition on loop edge")
			}
			if e.When.Not.Choice != "done" {
				t.Fatalf("expected Not{choice: done}, got %+v", e.When)
			}
			if e.MaxIterations != 3 {
				t.Fatalf("expected max_iterations 3, got %d", e.MaxIterations)
			}
			if e.IterationKey != "loop1" {
				t.Fatalf("expected iteration_key loop1, got %q", e.IterationKey)
			}
		}
	}
	if !entryFound || !continueFound {
		t.Fatalf("expected loop entry and continuation edges in compiled edges: %+v", c.Edges)
	}
}

func TestCompileLoopRequiresBodyAndSource(t *testing.T) {
	d := sampleFlow()
	// No data_dependency source for the loop.
	d.Nodes = append(d.Nodes, Node{ID: "loop1", Kind: KindLoop, Loop: &LoopSpec{
		Body: []string{"auto"}, ExitWhen: Condition{Choice: "done"}, MaxIterations: 3,
	}})
	if _, err := Compile(d); err == nil || !strings.Contains(err.Error(), "data_dependency source") {
		t.Fatalf("expected missing source error, got %v", err)
	}
}

func TestCompileLoopRejectsSharedBodyAndGeneratedIDCollision(t *testing.T) {
	base := func() *Definition {
		return &Definition{
			FlowID: "fl_loop_collision", Version: "1", Status: "draft",
			Nodes: []Node{
				{ID: "start", Kind: KindStep, Prompt: "start"},
				{ID: "body", Kind: KindStep, Prompt: "body"},
				{ID: "loop1", Kind: KindLoop, Loop: &LoopSpec{
					Body: []string{"body"}, ExitWhen: Condition{Status: "completed"}, MaxIterations: 2,
				}},
			},
			Edges: []Edge{{ID: "enter1", From: "start", To: "loop1", EdgeType: EdgeDataDependency}},
		}
	}

	t.Run("generated iteration id", func(t *testing.T) {
		d := base()
		d.Nodes = append(d.Nodes, Node{ID: "body_1", Kind: KindStep, Prompt: "independent node"})
		if _, err := Compile(d); err == nil || !strings.Contains(err.Error(), "generated loop iteration id") {
			t.Fatalf("expected generated ID collision error, got %v", err)
		}
	})

	t.Run("shared loop body", func(t *testing.T) {
		d := base()
		d.Nodes = append(d.Nodes, Node{ID: "loop2", Kind: KindLoop, Loop: &LoopSpec{
			Body: []string{"body"}, ExitWhen: Condition{Status: "completed"}, MaxIterations: 2,
		}})
		d.Edges = append(d.Edges, Edge{ID: "enter2", From: "start", To: "loop2", EdgeType: EdgeDataDependency})
		if _, err := Compile(d); err == nil || !strings.Contains(err.Error(), "already used by loop") {
			t.Fatalf("expected shared loop body error, got %v", err)
		}
	})

	t.Run("compiled edge id collision", func(t *testing.T) {
		d := base()
		d.Nodes = append(d.Nodes, Node{ID: "extra", Kind: KindStep, Prompt: "extra"})
		d.Edges = append(d.Edges, Edge{
			ID: "loop1_entry", From: "start", To: "extra", EdgeType: EdgeCondition,
			When: &Condition{Status: "completed"},
		})
		if _, err := Compile(d); err == nil || !strings.Contains(err.Error(), "compiled edge id") {
			t.Fatalf("expected compiled edge ID collision error, got %v", err)
		}
	})
}

func TestCompileFoldStaticEdges(t *testing.T) {
	c, err := Compile(sampleFlow())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	byID := map[string]CompiledNode{}
	for _, n := range c.Nodes {
		byID[n.ID] = n
	}
	// start -> decide data_dependency + handoff
	decide, ok := byID["decide"]
	if !ok {
		t.Fatalf("missing static node decide: %+v", c.Nodes)
	}
	if !containsStr(decide.DependsOn, "start") {
		t.Errorf("decide.DependsOn missing start: %+v", decide.DependsOn)
	}
	if !containsStr(decide.HandoffFrom, "start") {
		t.Errorf("decide.HandoffFrom missing start: %+v", decide.HandoffFrom)
	}
	// branch is a static gateway node
	br, ok := byID["br"]
	if !ok || br.Branch == nil {
		t.Fatalf("branch gateway not static: %+v", c.Nodes)
	}
	if !containsStr(br.DependsOn, "decide") {
		t.Errorf("branch.DependsOn missing decide: %+v", br.DependsOn)
	}
	// branch targets are demoted (not static)
	if _, ok := byID["auto"]; ok {
		t.Errorf("branch target auto should not be a static node")
	}
	if _, ok := byID["llm"]; ok {
		t.Errorf("branch target llm should not be a static node")
	}
	if c.Digest == "" {
		t.Error("expected non-empty digest")
	}
}

func TestCompileConditionEdgeAppendsTemplate(t *testing.T) {
	d := sampleFlow()
	d.Edges = append(d.Edges, Edge{
		ID: "ce", From: "decide", To: "auto", EdgeType: EdgeCondition,
		When: &Condition{Choice: "auto"},
	})
	c, err := Compile(d)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	found := false
	for _, e := range c.Edges {
		if e.ID == "ce" {
			found = true
			if e.Append.ID != "auto" || e.Append.Kind != KindStep {
				t.Errorf("condition edge append mismatch: %+v", e.Append)
			}
			if e.Append.DependsOn != nil {
				t.Errorf("append template should not carry DependsOn: %+v", e.Append)
			}
		}
	}
	if !found {
		t.Errorf("condition edge not compiled: %+v", c.Edges)
	}
	// 'auto' now referenced by both branch case and condition edge -> mixed
	// append references are fine (both are templates); no static node.
}

func TestCompileRejectsMixedStaticAndAppend(t *testing.T) {
	d := sampleFlow()
	// add a static in-edge into 'auto' (a branch target) -> mixed use
	d.Edges = append(d.Edges, Edge{ID: "mix", From: "start", To: "auto", EdgeType: EdgeDataDependency})
	if _, err := Compile(d); err == nil || !strings.Contains(err.Error(), "mixed use") {
		t.Fatalf("expected mixed-use error, got %v", err)
	}
}

func TestCompileDigestStable(t *testing.T) {
	c1, err := Compile(sampleFlow())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	c2, err := Compile(sampleFlow())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if c1.Digest != c2.Digest {
		t.Errorf("digest not stable: %s vs %s", c1.Digest, c2.Digest)
	}
}

func containsStr(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

// TestValidateFunctionNodeRequiresSpec verifies a function node without a
// spec is rejected, and js/wasm runtime requirements are enforced.
func TestValidateFunctionNodeRequiresSpec(t *testing.T) {
	d := &Definition{
		FlowID: "fl_fn", Version: "1", Status: "draft",
		Nodes: []Node{{ID: "fn", Kind: KindFunction, Prompt: ""}},
	}
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "missing function spec") {
		t.Fatalf("expected missing function spec error, got %v", err)
	}

	// js runtime requires source.
	d.Nodes[0].Function = &FunctionSpec{Runtime: "js"}
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "requires source") {
		t.Fatalf("expected js source error, got %v", err)
	}

	// wasm runtime requires a node-library ref.
	d.Nodes[0].Function = &FunctionSpec{Runtime: "wasm"}
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "requires a node-library ref") {
		t.Fatalf("expected wasm ref error, got %v", err)
	}

	// Unknown runtime rejected.
	d.Nodes[0].Function = &FunctionSpec{Runtime: "python"}
	if err := Validate(d); err == nil || !strings.Contains(err.Error(), "unknown function runtime") {
		t.Fatalf("expected unknown runtime error, got %v", err)
	}
}

// TestValidateFunctionNodeAcceptsValidSpec verifies a valid js function node
// passes validation without a prompt (the handler replaces the prompt).
func TestValidateFunctionNodeAcceptsValidSpec(t *testing.T) {
	d := &Definition{
		FlowID: "fl_fn_ok", Version: "1", Status: "draft",
		Nodes: []Node{{
			ID: "fn", Kind: KindFunction, Prompt: "",
			Function: &FunctionSpec{Runtime: "js", Source: "function handle(ctx, e) { return {}; }"},
		}},
	}
	if err := Validate(d); err != nil {
		t.Fatalf("expected valid function node to pass, got %v", err)
	}
}

// TestCompileFunctionNodeCarriesSpec verifies the function spec survives
// compile (CompiledNode.Function) so the engine can execute it.
func TestCompileFunctionNodeCarriesSpec(t *testing.T) {
	spec := &FunctionSpec{Runtime: "js", Source: "function handle(ctx, e) { return { ok: true }; }", Handler: "handle"}
	d := &Definition{
		FlowID: "fl_fn_compile", Version: "1", Status: "draft",
		Nodes: []Node{{
			ID: "fn", Kind: KindFunction, Prompt: "", Function: spec,
		}},
	}
	c, err := Compile(d)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(c.Nodes) != 1 || c.Nodes[0].Kind != KindFunction {
		t.Fatalf("expected one function compiled node, got %+v", c.Nodes)
	}
	if c.Nodes[0].Function == nil || c.Nodes[0].Function.Source != spec.Source {
		t.Fatalf("expected function spec carried through compile, got %+v", c.Nodes[0].Function)
	}
}
