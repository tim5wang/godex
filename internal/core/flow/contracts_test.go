package flow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateInputValues(t *testing.T) {
	t.Run("legacy flow keeps open inputs", func(t *testing.T) {
		if err := ValidateInputValues(&Definition{}, map[string]any{"extra": true}); err != nil {
			t.Fatalf("expected legacy inputs to remain open, got %v", err)
		}
	})

	def := &Definition{Inputs: []VarDef{
		{
			Name:     "ticket",
			Type:     "object",
			Required: true,
			Schema:   json.RawMessage(`{"properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
		},
		{Name: "priority", Type: "number"},
	}}
	valid := map[string]any{
		"ticket":   map[string]any{"id": "T-42"},
		"priority": 2,
	}
	if err := ValidateInputValues(def, valid); err != nil {
		t.Fatalf("expected valid declared inputs, got %v", err)
	}

	tests := []struct {
		name   string
		values map[string]any
		want   string
	}{
		{name: "required", values: map[string]any{}, want: "inputs.ticket is required"},
		{name: "unknown", values: map[string]any{"ticket": map[string]any{"id": "T-42"}, "extra": true}, want: "inputs.extra is not declared"},
		{name: "type", values: map[string]any{"ticket": map[string]any{"id": "T-42"}, "priority": "high"}, want: "inputs.priority must be number"},
		{name: "nested required", values: map[string]any{"ticket": map[string]any{}}, want: "inputs.ticket.id is required"},
		{name: "additional property", values: map[string]any{"ticket": map[string]any{"id": "T-42", "extra": true}}, want: "inputs.ticket.extra is not allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInputValues(def, tt.values)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestJSONSchemaNestingDepthLimit(t *testing.T) {
	nestedSchema := func(depth int) json.RawMessage {
		var schema strings.Builder
		for range depth {
			schema.WriteString(`{"properties":{"next":`)
		}
		schema.WriteString(`{"type":"string"}`)
		for range depth {
			schema.WriteString(`}}`)
		}
		return json.RawMessage(schema.String())
	}
	nestedValue := func(depth int) any {
		var value any = "ok"
		for range depth {
			value = map[string]any{"next": value}
		}
		return value
	}

	atLimit := nestedSchema(maxJSONSchemaNestingDepth)
	if err := ValidateJSONSchemaValue(nestedValue(maxJSONSchemaNestingDepth), atLimit, "value"); err != nil {
		t.Fatalf("expected schema at maximum nesting depth to validate, got %v", err)
	}
	if err := validateVarDefs([]VarDef{{Name: "value", Type: "object", Schema: atLimit}}, "inputs"); err != nil {
		t.Fatalf("expected variable schema at maximum nesting depth to validate, got %v", err)
	}

	overLimit := nestedSchema(maxJSONSchemaNestingDepth + 1)
	if err := ValidateJSONSchemaValue(nestedValue(maxJSONSchemaNestingDepth+1), overLimit, "value"); err == nil ||
		!strings.Contains(err.Error(), "maximum schema nesting depth") {
		t.Fatalf("expected over-deep schema to fail value validation, got %v", err)
	}
	if err := validateVarDefs([]VarDef{{Name: "value", Type: "object", Schema: overLimit}}, "inputs"); err == nil ||
		!strings.Contains(err.Error(), "maximum schema nesting depth") {
		t.Fatalf("expected over-deep variable schema to fail definition validation, got %v", err)
	}
}

func TestValidateFlowOutputSources(t *testing.T) {
	base := func() *Definition {
		return &Definition{
			FlowID:  "fl_output_contract",
			Version: "1",
			Status:  "draft",
			Nodes: []Node{{
				ID: "finish", Kind: KindStep, Prompt: "return a summary",
				Outputs: []VarDef{{Name: "summary", Type: "string"}},
			}},
			Outputs: []VarDef{{
				Name: "summary", Type: "string", Required: true,
				Source: "nodes.finish.outputs.summary",
			}},
		}
	}

	if err := Validate(base()); err != nil {
		t.Fatalf("expected declared output source to validate, got %v", err)
	}

	t.Run("legacy unbound optional output", func(t *testing.T) {
		def := base()
		def.Outputs[0].Source = ""
		def.Outputs[0].Required = false
		if err := Validate(def); err != nil {
			t.Fatalf("expected unbound optional output to remain compatible, got %v", err)
		}
	})

	tests := []struct {
		name   string
		source string
		typ    string
		want   string
	}{
		{name: "missing required source", source: "", typ: "string", want: "required flow outputs must declare a source"},
		{name: "unknown node", source: "nodes.missing.outputs.summary", typ: "string", want: "unknown output node"},
		{name: "unknown field", source: "nodes.finish.outputs.other", typ: "string", want: "does not declare output"},
		{name: "type mismatch", source: "nodes.finish.outputs.summary", typ: "number", want: "does not match source type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := base()
			def.Outputs[0].Source = tt.source
			def.Outputs[0].Type = tt.typ
			err := Validate(def)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestCompileCarriesContractsAndTimeoutsIntoDigest(t *testing.T) {
	def := &Definition{
		FlowID:     "fl_compiled_contract",
		Version:    "1",
		Status:     "draft",
		TimeoutSec: 90,
		Inputs:     []VarDef{{Name: "request", Type: "string", Required: true}},
		Nodes: []Node{{
			ID: "finish", Kind: KindStep, Prompt: "return a summary", TimeoutSec: 15,
			Outputs: []VarDef{{Name: "summary", Type: "string", Required: true}},
		}},
		Outputs: []VarDef{{
			Name: "summary", Type: "string", Required: true,
			Source: "nodes.finish.outputs.summary",
		}},
	}
	compiled, err := Compile(def)
	if err != nil {
		t.Fatalf("compile flow: %v", err)
	}
	if compiled.TimeoutSec != 90 || len(compiled.Inputs) != 1 || len(compiled.Outputs) != 1 {
		t.Fatalf("compiled Flow contract was not preserved: %+v", compiled)
	}
	if len(compiled.Nodes) != 1 || compiled.Nodes[0].TimeoutSec != 15 ||
		!compiled.Nodes[0].Outputs[0].Required {
		t.Fatalf("compiled node contract was not preserved: %+v", compiled.Nodes)
	}

	changed := *def
	changed.Outputs = append([]VarDef{}, def.Outputs...)
	changed.Outputs[0].Required = false
	compiledChanged, err := Compile(&changed)
	if err != nil {
		t.Fatalf("compile changed flow: %v", err)
	}
	if compiled.Digest == compiledChanged.Digest {
		t.Fatal("expected contract changes to alter the compiled digest")
	}
}

func TestValidateFunctionRejectsUnsupportedSchemaKeyword(t *testing.T) {
	def := &Definition{
		FlowID: "fl_bad_function_schema", Version: "1", Status: "draft",
		Nodes: []Node{{
			ID: "fn", Kind: KindFunction,
			Function: &FunctionSpec{
				Runtime:     FunctionRuntimeJS,
				Source:      `function handle(ctx, event) { return {}; }`,
				InputSchema: json.RawMessage(`{"type":"object","minProperties":1}`),
			},
		}},
	}
	if err := Validate(def); err == nil || !strings.Contains(err.Error(), "minProperties is not supported") {
		t.Fatalf("expected unsupported schema keyword to fail validation, got %v", err)
	}
}
