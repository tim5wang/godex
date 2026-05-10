package toolruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type runtimeNestedArgs struct {
	Count int `json:"count"`
}

type runtimeArgs struct {
	Query   string            `json:"query"`
	Limit   int               `json:"limit"`
	Enabled bool              `json:"enabled"`
	Tags    []int             `json:"tags"`
	Target  runtimeNestedArgs `json:"target"`
}

func TestTypedToolAliasAndCoercion(t *testing.T) {
	tool := NewTypedTool(NewToolSpec("runtime_echo", "echo runtime args", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query":   map[string]interface{}{"type": "string"},
			"limit":   map[string]interface{}{"type": "integer"},
			"enabled": map[string]interface{}{"type": "boolean"},
			"tags": map[string]interface{}{
				"type":  "array",
				"items": map[string]interface{}{"type": "integer"},
			},
			"target": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"count": map[string]interface{}{"type": "integer"},
				},
			},
		},
	}, map[string]string{"q": "query"}), func(ctx context.Context, args runtimeArgs) (ToolResult, error) {
		_ = ctx
		return ToolResult{Structured: args}, nil
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"q":       "stocks",
		"limit":   "10",
		"enabled": "true",
		"tags":    "[\"1\",\"2\",\"3\"]",
		"target":  "{\"count\":\"7\"}",
	})
	if err != nil {
		t.Fatalf("execute tool: %v", err)
	}

	expected := `{"query":"stocks","limit":10,"enabled":true,"tags":[1,2,3],"target":{"count":7}}`
	if result != expected {
		t.Fatalf("unexpected normalized result:\nwant %s\ngot  %s", expected, result)
	}
}

func TestExecuteToolRuntimeBeforeCanModifyInputAndAfterCanReplaceResult(t *testing.T) {
	sequence := make([]string, 0, 4)
	tool := NewTypedTool(NewToolSpec("runtime_pipeline", "pipeline tool", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{"type": "string"},
			"limit": map[string]interface{}{"type": "integer"},
		},
	}, nil), func(ctx context.Context, args runtimeArgs) (ToolResult, error) {
		_ = ctx
		sequence = append(sequence, "run")
		return ToolResult{Structured: map[string]interface{}{
			"query": args.Query,
			"limit": args.Limit,
		}}, nil
	})

	before := []BeforeInterceptor{
		func(ctx context.Context, call *ToolCall) (*ToolResult, error) {
			_ = ctx
			sequence = append(sequence, "before1")
			call.NormalizedInput["limit"] = "12"
			return nil, nil
		},
		func(ctx context.Context, call *ToolCall) (*ToolResult, error) {
			_ = ctx
			sequence = append(sequence, "before2")
			call.NormalizedInput["query"] = "patched"
			return nil, nil
		},
	}
	after := []AfterInterceptor{
		func(ctx context.Context, call *ToolCall, result ToolResult, err error) (ToolResult, error) {
			_ = ctx
			if err != nil {
				return result, err
			}
			sequence = append(sequence, "after1")
			result.Text = "patched-result"
			result.Structured = nil
			if call.DecodedInput == nil {
				t.Fatal("expected decoded input to be available in after interceptor")
			}
			return result, nil
		},
		func(ctx context.Context, call *ToolCall, result ToolResult, err error) (ToolResult, error) {
			_ = ctx
			_ = call
			sequence = append(sequence, "after2")
			return result, err
		},
	}

	result, err := executeToolRuntime(context.Background(), tool, map[string]interface{}{
		"query": "original",
		"limit": "3",
	}, before, after)
	if err != nil {
		t.Fatalf("execute tool runtime: %v", err)
	}
	if result != "patched-result" {
		t.Fatalf("expected after interceptor to replace result, got %q", result)
	}

	expected := []string{"before1", "before2", "run", "after1", "after2"}
	if !reflect.DeepEqual(sequence, expected) {
		t.Fatalf("unexpected interceptor order: want %v got %v", expected, sequence)
	}
}

func TestExecuteToolRuntimeBeforeShortCircuitSkipsToolBody(t *testing.T) {
	sequence := make([]string, 0, 3)
	tool := NewTypedTool(NewToolSpec("runtime_short", "short-circuit tool", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args struct{}) (ToolResult, error) {
		_ = ctx
		_ = args
		sequence = append(sequence, "run")
		return ToolResult{Text: "run"}, nil
	})

	result, err := executeToolRuntime(context.Background(), tool, map[string]interface{}{}, []BeforeInterceptor{
		func(ctx context.Context, call *ToolCall) (*ToolResult, error) {
			_ = ctx
			_ = call
			sequence = append(sequence, "before")
			return &ToolResult{Text: "short-circuit"}, nil
		},
	}, []AfterInterceptor{
		func(ctx context.Context, call *ToolCall, result ToolResult, err error) (ToolResult, error) {
			_ = ctx
			_ = call
			sequence = append(sequence, "after")
			return result, err
		},
	})
	if err != nil {
		t.Fatalf("execute tool runtime: %v", err)
	}
	if result != "short-circuit" {
		t.Fatalf("unexpected short-circuit result %q", result)
	}

	expected := []string{"before", "after"}
	if !reflect.DeepEqual(sequence, expected) {
		t.Fatalf("unexpected short-circuit order: want %v got %v", expected, sequence)
	}
}

func TestExecuteToolRuntimeBeforeErrorStopsExecution(t *testing.T) {
	sequence := make([]string, 0, 2)
	tool := NewTypedTool(NewToolSpec("runtime_error", "error tool", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args struct{}) (ToolResult, error) {
		_ = ctx
		_ = args
		sequence = append(sequence, "run")
		return ToolResult{Text: "run"}, nil
	})

	boom := errors.New("boom")
	_, err := executeToolRuntime(context.Background(), tool, map[string]interface{}{}, []BeforeInterceptor{
		func(ctx context.Context, call *ToolCall) (*ToolResult, error) {
			_ = ctx
			_ = call
			sequence = append(sequence, "before")
			return nil, boom
		},
	}, []AfterInterceptor{
		func(ctx context.Context, call *ToolCall, result ToolResult, err error) (ToolResult, error) {
			_ = ctx
			_ = call
			sequence = append(sequence, "after")
			return result, err
		},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected before error, got %v", err)
	}

	expected := []string{"before", "after"}
	if !reflect.DeepEqual(sequence, expected) {
		t.Fatalf("unexpected error order: want %v got %v", expected, sequence)
	}
}

func TestExecuteToolRuntimeAfterCanReplaceError(t *testing.T) {
	tool := NewTypedTool(NewToolSpec("runtime_replace_error", "replace error", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args struct{}) (ToolResult, error) {
		_ = ctx
		_ = args
		return ToolResult{}, errors.New("original")
	})

	result, err := executeToolRuntime(context.Background(), tool, map[string]interface{}{}, nil, []AfterInterceptor{
		func(ctx context.Context, call *ToolCall, result ToolResult, err error) (ToolResult, error) {
			_ = ctx
			_ = call
			if err == nil {
				t.Fatal("expected original error")
			}
			return ToolResult{Text: "recovered"}, nil
		},
	})
	if err != nil {
		t.Fatalf("expected error replacement, got %v", err)
	}
	if result != "recovered" {
		t.Fatalf("expected recovered result, got %q", result)
	}
}

func TestScopedInterceptorsOnlyAffectMatchingTools(t *testing.T) {
	handler := NewToolHandler()
	handler.Register(NewTypedTool(NewToolSpec("alpha", "alpha tool", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args struct{}) (ToolResult, error) {
		_ = ctx
		_ = args
		return ToolResult{Text: "alpha"}, nil
	}))
	handler.Register(NewTypedTool(NewToolSpec("beta", "beta tool", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args struct{}) (ToolResult, error) {
		_ = ctx
		_ = args
		return ToolResult{Text: "beta"}, nil
	}))

	sequence := make([]string, 0, 4)
	handler.AddBeforeInterceptorsForTools([]string{"alpha"}, func(ctx context.Context, call *ToolCall) (*ToolResult, error) {
		_ = ctx
		sequence = append(sequence, "before:"+call.Name)
		return nil, nil
	})
	handler.AddAfterInterceptorsForTools([]string{"alpha"}, func(ctx context.Context, call *ToolCall, result ToolResult, err error) (ToolResult, error) {
		_ = ctx
		if err != nil {
			return result, err
		}
		sequence = append(sequence, "after:"+call.Name)
		return result, nil
	})

	if _, err := handler.Handle(context.Background(), "alpha", map[string]interface{}{}); err != nil {
		t.Fatalf("handle alpha: %v", err)
	}
	if _, err := handler.Handle(context.Background(), "beta", map[string]interface{}{}); err != nil {
		t.Fatalf("handle beta: %v", err)
	}

	expected := []string{"before:alpha", "after:alpha"}
	if !reflect.DeepEqual(sequence, expected) {
		t.Fatalf("unexpected scoped interceptor sequence: want %v got %v", expected, sequence)
	}
}
