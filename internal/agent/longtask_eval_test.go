package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
)

func TestLongTaskDevelopmentSmokeFixesFixtureRepo(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	writeLongTaskFixtureRepo(t, a.cfg.WorkspaceDir)

	a.AddMessage("Implement the calculator requirement end to end: run tests, fix the bug, rerun tests, and summarize the result.")
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.TextBlock("I will run the test suite first."),
			protocol.ToolUseBlock("tool-test-before", "bash", map[string]interface{}{"command": "go test ./..."}),
		}},
		{Content: []protocol.Block{
			protocol.TextBlock("The failing test shows Add subtracts instead of adding. I will patch the implementation."),
			protocol.ToolUseBlock("tool-fix", "edit_file", map[string]interface{}{
				"path":     "calc/calc.go",
				"old_text": "return a - b",
				"new_text": "return a + b",
			}),
		}},
		{Content: []protocol.Block{
			protocol.TextBlock("Now I will rerun the tests."),
			protocol.ToolUseBlock("tool-test-after", "bash", map[string]interface{}{"command": "go test ./..."}),
		}},
		{Content: []protocol.Block{protocol.TextBlock("Implemented Add correctly and verified go test ./... passes.")}},
	}}

	var finished []events.ToolCallPayload
	err := a.RunWithOptions(context.Background(), RunOptions{
		SessionID: "session-longtask-dev",
		TurnID:    "turn-longtask-dev",
		Sink: events.SinkFunc(func(event events.Event) {
			if event.Type != events.EventToolCallFinished {
				return
			}
			payload, ok := event.Payload.(events.ToolCallPayload)
			if ok {
				finished = append(finished, payload)
			}
		}),
	})
	if err != nil {
		t.Fatalf("run longtask development smoke: %v", err)
	}

	if len(finished) != 3 {
		t.Fatalf("expected three completed tool calls, got %+v", finished)
	}
	if finished[0].Name != "bash" || finished[0].Error == "" || !strings.Contains(finished[0].Output, "FAIL") {
		t.Fatalf("expected first test run to fail with diagnostic output, got %+v", finished[0])
	}
	if finished[1].Name != "edit_file" || finished[1].Error != "" {
		t.Fatalf("expected edit_file fix to succeed, got %+v", finished[1])
	}
	if finished[2].Name != "bash" || finished[2].Error != "" || !strings.Contains(finished[2].Output, "ok") {
		t.Fatalf("expected final test run to pass, got %+v", finished[2])
	}

	data, err := os.ReadFile(filepath.Join(a.cfg.WorkspaceDir, "calc", "calc.go"))
	if err != nil {
		t.Fatalf("read fixed source: %v", err)
	}
	if !strings.Contains(string(data), "return a + b") {
		t.Fatalf("expected source to be fixed, got:\n%s", string(data))
	}
	if got := protocol.MessageText(a.GetMessages()[len(a.GetMessages())-1]); !strings.Contains(got, "go test ./... passes") {
		t.Fatalf("expected final handoff summary to mention verification, got %q", got)
	}
}

func writeLongTaskFixtureRepo(t *testing.T, workspace string) {
	t.Helper()
	files := map[string]string{
		"go.mod": `module example.com/longtaskfixture

go 1.22
`,
		"calc/calc.go": `package calc

func Add(a, b int) int {
	return a - b
}
`,
		"calc/calc_test.go": `package calc

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("expected 5, got %d", got)
	}
}
`,
	}
	for path, content := range files {
		abs := filepath.Join(workspace, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatalf("mkdir fixture path %s: %v", path, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
			t.Fatalf("write fixture file %s: %v", path, err)
		}
	}
}
