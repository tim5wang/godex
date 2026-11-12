package agent

import (
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/tools"
)

// SetMemoryBoundary is host-only and runs at serialized turn boundaries. The
// writer and capture strategy share the same desktop/session ownership. Shared
// personal knowledge is read-only from this conversation's default tool.
func (a *Agent) SetMemoryBoundary(key string, local, shared *memory.Manager, writer tools.MemoryWriter, strategy memory.Strategy) {
	a.mu.Lock()
	if a.memoryScopeKey == key {
		a.mu.Unlock()
		return
	}
	a.memoryScopeKey = key
	a.memoryWriter = writer
	a.memoryMgr = local
	a.memorySharedMgr = shared
	a.memoryExt = memory.NewExtractor(local, "")
	if strategy == nil {
		a.memoryExt, strategy = buildMemoryStack(a.cfg, a.client, local)
	}
	a.memoryStrategy = strategy
	a.memoryRecallQuery = ""
	a.memoryRecallRevision = ""
	a.memoryRecallLayers = memory.ContextLayers{}
	a.mu.Unlock()
	names := a.toolHandler.ActiveToolNames()
	a.registerToolTo(a.toolHandler, tools.NewMemoryTool(writer), tools.ToolMeta{AlwaysActive: true})
	a.toolHandler.SetActiveToolsExact(names...)
}

func (a *Agent) memoryContextRevision() string {
	revision := a.memoryMgr.ContextRevision()
	if a.memorySharedMgr != nil {
		revision += "|" + a.memorySharedMgr.ContextRevision()
	}
	return revision
}

func (a *Agent) memoryToolWriter() tools.MemoryWriter {
	if a.memoryWriter != nil {
		return a.memoryWriter
	}
	return a.memoryMgr
}

// MemoryWriter gives explicit slash commands the same host-bound default store
// as the model tool, instead of accidentally selecting the personal overlay.
func (a *Agent) MemoryWriter() tools.MemoryWriter { return a.memoryToolWriter() }
