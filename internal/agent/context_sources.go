package agent

import (
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/memory"
)

func buildContextSources(system string, history []protocol.Message, guidance protocol.Message, promptState, tail []protocol.Message, injected memory.ContextLayers, excluded map[string]int) []protocol.ContextSource {
	sources := []protocol.ContextSource{{Name: "system", EstimatedTokens: compress.CountTokens(system)}}
	addMessages := func(name string, messages []protocol.Message) {
		tokens := 0
		for _, msg := range messages {
			tokens += compress.CountTokens(protocol.MessageText(msg))
		}
		if tokens > 0 {
			sources = append(sources, protocol.ContextSource{Name: name, EstimatedTokens: tokens})
		}
	}
	addMessages("history", history)
	addMessages("memory_guidance", []protocol.Message{guidance})
	addMessages("runtime_prompt", promptState)
	for _, group := range []struct {
		name, note string
		entries    []memory.RelevantMemory
	}{
		{"memory_identity", "stable identity; bounded", injected.Identity},
		{"memory_core", "user preference or explicitly pinned core; bounded", injected.Core},
		{"memory_relevant", "query/scope matched; bounded; injected on this request", injected.Relevant},
	} {
		if len(group.entries) == 0 {
			continue
		}
		source := protocol.ContextSource{Name: group.name, Note: group.note}
		for _, entry := range group.entries {
			source.MemoryIDs = append(source.MemoryIDs, entry.ID)
		}
		layers := memory.ContextLayers{}
		switch group.name {
		case "memory_identity":
			layers.Identity = group.entries
		case "memory_core":
			layers.Core = group.entries
		default:
			layers.Relevant = group.entries
		}
		source.EstimatedTokens = compress.CountTokens(formatMemoryLayers(layers))
		sources = append(sources, source)
	}
	for _, msg := range tail {
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindMemory {
			continue
		}
		addMessages("runtime_tail", []protocol.Message{msg})
	}
	if len(excluded) > 0 {
		sources = append(sources, protocol.ContextSource{Name: "memory_selection", Excluded: excluded, Note: "Selection exclusions, not usefulness scores. Relevant recall may be throttled out of this request."})
	}
	return sources
}
