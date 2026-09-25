package flow

import (
	"encoding/json"
	"strings"
	"testing"
)

func serviceTestFlow() *Definition {
	body, _ := json.Marshal(map[string]any{"task": "{{inputs.task}}"})
	return &Definition{
		FlowID:  "fl_service",
		Version: "1",
		Status:  "draft",
		Inputs:  []VarDef{{Name: "task", Type: "string"}},
		Nodes: []Node{{
			ID:   "call",
			Kind: KindService,
			Service: &ServiceSpec{
				Method: "POST",
				URL:    "https://api.example.com/v1/tasks",
				Headers: map[string]string{
					"Accept": "application/json",
				},
				Body: body,
				Auth: &ServiceAuthSpec{Type: "bearer", TokenEnv: "TASK_API_TOKEN"},
			},
			Outputs: []VarDef{{Name: "status_code", Type: "number"}, {Name: "body", Type: "object"}},
		}},
	}
}

func TestValidateAndCompileServiceNode(t *testing.T) {
	def := serviceTestFlow()
	if err := Validate(def); err != nil {
		t.Fatalf("validate service flow: %v", err)
	}
	compiled, err := Compile(def)
	if err != nil {
		t.Fatalf("compile service flow: %v", err)
	}
	if len(compiled.Nodes) != 1 {
		t.Fatalf("expected one compiled node, got %d", len(compiled.Nodes))
	}
	node := compiled.Nodes[0]
	if node.Kind != KindService || node.Prompt != "" || node.Service == nil {
		t.Fatalf("service spec was not carried through compile: %+v", node)
	}
	if node.Service.Method != "POST" || node.Service.Auth == nil || node.Service.Auth.TokenEnv != "TASK_API_TOKEN" {
		t.Fatalf("compiled service spec was changed: %+v", node.Service)
	}
}

func TestValidateServiceRejectsUnsafeOrInvalidSpecs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Definition)
		want   string
	}{
		{
			name: "unsupported method",
			mutate: func(d *Definition) {
				d.Nodes[0].Service.Method = "CONNECT"
			},
			want: "service.method",
		},
		{
			name: "non-http URL",
			mutate: func(d *Definition) {
				d.Nodes[0].Service.URL = "file:///etc/passwd"
			},
			want: "service.url",
		},
		{
			name: "literal authorization header",
			mutate: func(d *Definition) {
				d.Nodes[0].Service.Headers["Authorization"] = "secret"
			},
			want: "service.auth",
		},
		{
			name: "invalid credential environment variable",
			mutate: func(d *Definition) {
				d.Nodes[0].Service.Auth.TokenEnv = "TOKEN;echo"
			},
			want: "token_env",
		},
		{
			name: "unknown body variable",
			mutate: func(d *Definition) {
				d.Nodes[0].Service.Body = json.RawMessage(`{"task":"{{inputs.unknown}}"}`)
			},
			want: "unknown input",
		},
		{
			name: "invalid JSON body",
			mutate: func(d *Definition) {
				d.Nodes[0].Service.Body = json.RawMessage(`{"task":`)
			},
			want: "service.body",
		},
		{
			name: "private network without allowlist",
			mutate: func(d *Definition) {
				d.Network = &NetworkPolicy{Policy: "allow_all", AllowPrivateHosts: true}
			},
			want: "network.allow_private_hosts",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := serviceTestFlow()
			tt.mutate(def)
			err := Validate(def)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected validation error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestValidateServiceRejectsAuthHeaderCollision(t *testing.T) {
	def := serviceTestFlow()
	def.Nodes[0].Service.Headers["X-Custom-Auth"] = "not-a-secret"
	def.Nodes[0].Service.Auth = &ServiceAuthSpec{
		Type:       "api_key",
		TokenEnv:   "TASK_API_TOKEN",
		HeaderName: "x-custom-auth",
	}
	if err := Validate(def); err == nil || !strings.Contains(err.Error(), "service.auth.header_name") {
		t.Fatalf("expected auth/header collision to be rejected, got %v", err)
	}
}
