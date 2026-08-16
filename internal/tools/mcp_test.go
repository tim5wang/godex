package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/mcp"
)

type fakeMCPReader struct{}

func (fakeMCPReader) ListResources() ([]mcp.Resource, error) {
	return []mcp.Resource{{
		Server:   "docs",
		URI:      "guide.md",
		Name:     "guide.md",
		MIMEType: "text/markdown",
	}}, nil
}

func (fakeMCPReader) ReadResource(serverName, uri string) (*mcp.ReadResult, error) {
	return &mcp.ReadResult{
		Server:   serverName,
		URI:      uri,
		MIMEType: "text/markdown",
		Text:     "# guide",
	}, nil
}

func TestListMCPResourcesToolReturnsResources(t *testing.T) {
	tool := NewListMCPResourcesTool(fakeMCPReader{})

	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("list mcp resources: %v", err)
	}
	for _, want := range []string{`"resources"`, `"server":"docs"`, `"uri":"guide.md"`} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected result to contain %q, got %q", want, result)
		}
	}
}

func TestReadMCPResourceToolReturnsContent(t *testing.T) {
	tool := NewReadMCPResourceTool(fakeMCPReader{})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"server": "docs",
		"uri":    "guide.md",
	})
	if err != nil {
		t.Fatalf("read mcp resource: %v", err)
	}
	for _, want := range []string{`"server":"docs"`, `"uri":"guide.md"`, `"text":"# guide"`} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected result to contain %q, got %q", want, result)
		}
	}
}

type fakeMCPToolRunner struct {
	tools []mcp.Tool
	call  *mcp.CallResult
}

func (f *fakeMCPToolRunner) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	return f.tools, nil
}

func (f *fakeMCPToolRunner) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*mcp.CallResult, error) {
	return f.call, nil
}

func TestListMCPToolsTool(t *testing.T) {
	runner := &fakeMCPToolRunner{tools: []mcp.Tool{{
		Server:      "fake",
		Name:        "echo",
		Description: "echo back",
	}}}
	tool := NewListMCPToolsTool(runner)
	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result, "fake") || !strings.Contains(result, "echo") {
		t.Fatalf("unexpected output: %q", result)
	}
}

func TestCallMCPToolTool(t *testing.T) {
	runner := &fakeMCPToolRunner{call: &mcp.CallResult{
		Server: "fake",
		Tool:   "echo",
		Text:   "echo: hi",
	}}
	tool := NewCallMCPToolTool(runner)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"server": "fake",
		"tool":   "echo",
		"arguments": map[string]interface{}{
			"message": "hi",
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result, "echo: hi") {
		t.Fatalf("unexpected output: %q", result)
	}

	// Missing required args.
	if _, err := tool.Execute(context.Background(), map[string]interface{}{}); err == nil {
		t.Fatal("expected error for missing server/tool")
	}

	// isError result surfaces as an error.
	runner.call.IsError = true
	runner.call.Text = "boom failed"
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"server": "fake",
		"tool":   "boom",
	}); err == nil {
		t.Fatal("expected error for isError result")
	}
}

type fakeMCPPromptRunner struct {
	prompts []mcp.Prompt
	got     *mcp.GetPromptResult
}

func (f *fakeMCPPromptRunner) ListPrompts(ctx context.Context) ([]mcp.Prompt, error) {
	return f.prompts, nil
}

func (f *fakeMCPPromptRunner) GetPrompt(ctx context.Context, serverName, promptName string, arguments map[string]any) (*mcp.GetPromptResult, error) {
	return f.got, nil
}

func TestListMCPPromptsTool(t *testing.T) {
	runner := &fakeMCPPromptRunner{prompts: []mcp.Prompt{{
		Server: "fake",
		Name:   "review",
	}}}
	tool := NewListMCPPromptsTool(runner)
	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result, "review") {
		t.Fatalf("unexpected output: %q", result)
	}
}

func TestGetMCPPromptTool(t *testing.T) {
	runner := &fakeMCPPromptRunner{got: &mcp.GetPromptResult{
		Server: "fake",
		Prompt: "review",
		Messages: []mcp.PromptMessage{
			{Role: "user", Content: "Please review the code."},
			{Role: "assistant", Content: "Here is my review."},
		},
	}}
	tool := NewGetMCPPromptTool(runner)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"server": "fake",
		"prompt": "review",
		"arguments": map[string]interface{}{
			"file": "main.go",
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result, "Please review the code.") || !strings.Contains(result, "Here is my review.") {
		t.Fatalf("unexpected output: %q", result)
	}
	// Missing required args.
	if _, err := tool.Execute(context.Background(), map[string]interface{}{}); err == nil {
		t.Fatal("expected error for missing server/prompt")
	}
}
