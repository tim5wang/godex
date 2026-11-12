package agent

import (
	"context"
	"testing"

	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/templates"
)

func TestMemoryBoundarySurvivesToolConfigAndTemplateRebuilds(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	writer := memory.NewManager(t.TempDir())
	personal := memory.NewManager(t.TempDir())
	a.SetMemoryBoundary("session:host-bound", writer, personal, writer, nil)
	a.ApplyTemplate(templates.AgentTemplate{ID: "test", Memory: templates.MemoryScoped, Bundles: []string{"always_on"}})
	a.RegisterTools()
	a.ApplyConfig(a.cfg, nil)
	a.RegisterTools()
	if a.memoryMgr != writer || a.memorySharedMgr != personal || a.memoryToolWriter() != writer {
		t.Fatal("host memory boundary lost")
	}
	_, err := a.toolHandler.Handle(context.Background(), "memory", map[string]interface{}{"action": "remember", "title": "scoped", "summary": "scoped evidence", "content": "scoped content", "memory_type": "work_fact"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := writer.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("tool wrote outside host store: %+v %v", rows, err)
	}
}
