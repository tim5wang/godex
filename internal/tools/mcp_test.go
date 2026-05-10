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
