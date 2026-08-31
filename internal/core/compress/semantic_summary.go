package compress

import (
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

type semanticSummary struct {
	goals            []string
	constraints      []string
	decisions        []string
	files            map[string]struct{}
	validation       []string
	openItems        []string
	previous         []string
	recentUser       string
	recentAssistant  string
	recentUsers      []string
	recentAssistants []string
	toolNames        map[string]struct{}
	fileOps          FileOperations
}

func collectSemanticSummary(messages []protocol.Message) semanticSummary {
	state := semanticSummary{
		files:     make(map[string]struct{}),
		toolNames: make(map[string]struct{}),
	}
	for _, msg := range messages {
		if msg.Metadata == nil || !msg.Metadata.Ephemeral {
			state.collectMessage(msg)
		}
	}
	return state
}

func (s *semanticSummary) collectMessage(msg protocol.Message) {
	text := messageSemanticText(msg)
	s.collectPaths(text)
	s.collectValidation(text)
	s.collectOpenItems(text)
	for _, block := range msg.Content {
		s.collectBlock(block)
	}

	humanText := strings.TrimSpace(protocol.MessageText(msg))
	if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindSummary {
		addUniqueLimited(&s.previous, normalizeWhitespace(humanText), 3)
		return
	}
	s.collectHumanMessage(msg, humanText)
}

func (s *semanticSummary) collectBlock(block protocol.Block) {
	switch block.Type {
	case protocol.BlockToolUse:
		s.collectToolUse(block)
	case protocol.BlockToolResult:
		s.collectValidation(block.Content)
		s.collectOpenItems(block.Content)
		s.collectPaths(block.Content)
	}
}

func (s *semanticSummary) collectToolUse(block protocol.Block) {
	if strings.TrimSpace(block.Name) != "" {
		s.toolNames[block.Name] = struct{}{}
	}
	visitStructuredStrings(block.Input, func(value string) {
		s.collectPaths(value)
		s.collectValidation(value)
		s.collectOpenItems(value)
	})
	path := extractPathFromToolInput(block.Name, block.Input)
	if path == "" {
		return
	}
	switch knownFileToolNames[block.Name] {
	case "read":
		s.fileOps.Read = addUnique(s.fileOps.Read, path)
	case "write":
		s.fileOps.Written = addUnique(s.fileOps.Written, path)
	case "edit":
		s.fileOps.Edited = addUnique(s.fileOps.Edited, path)
	default:
		s.fileOps.Read = addUnique(s.fileOps.Read, path)
	}
}

func (s *semanticSummary) collectHumanMessage(msg protocol.Message, humanText string) {
	if humanText == "" {
		return
	}
	switch msg.Role {
	case protocol.RoleUser:
		if !hasToolResult(msg) {
			s.collectUserMessage(humanText)
		}
	case protocol.RoleAssistant:
		s.collectAssistantMessage(humanText)
	}
}

func (s *semanticSummary) collectUserMessage(humanText string) {
	normalized := normalizeWhitespace(humanText)
	if isLowSignalAck(normalized) {
		return
	}
	s.recentUser = normalized
	addRecentUserVerbatim(&s.recentUsers, humanText)
	addUniqueLimited(&s.goals, normalized, 64)
	for _, fragment := range splitFragments(humanText) {
		if isConstraintFragment(fragment) {
			addUniqueLimited(&s.constraints, fragment, maxSummaryConstraints)
		}
	}
}

func (s *semanticSummary) collectAssistantMessage(humanText string) {
	s.recentAssistant = normalizeWhitespace(humanText)
	addRecentAssistantVerbatim(&s.recentAssistants, humanText)
	for _, fragment := range splitFragments(humanText) {
		if isDecisionFragment(fragment) {
			addUniqueLimited(&s.decisions, fragment, maxSummaryDecisions)
		}
		if isOpenItemFragment(fragment) {
			addUniqueLimited(&s.openItems, fragment, maxSummaryOpenItems)
		}
	}
}

func buildSemanticSummary(messages []protocol.Message, transcript, continuationSnapshot string) string {
	state := collectSemanticSummary(messages)

	// Compute merged file operations (edited/written files excluded from read-only)
	mergedReads := mergeFileOps(state.fileOps)

	var builder strings.Builder
	builder.WriteString("## Session Compaction Summary\n")
	builder.WriteString("Transcript: ")
	builder.WriteString(transcript)
	builder.WriteString("\n\n")
	builder.WriteString("Use `history_search` with this transcript when exact older details are needed.\n")
	writePinnedContinuationSnapshot(&builder, continuationSnapshot)

	builder.WriteString("\n## Goal\n")
	if len(state.goals) > 0 {
		for _, g := range compactItems(state.goals, maxSummaryGoals) {
			builder.WriteString("- ")
			builder.WriteString(truncateRunes(g, maxSummaryItemRunes))
			builder.WriteString("\n")
		}
	} else {
		builder.WriteString("- (not explicitly stated)\n")
	}

	builder.WriteString("\n## Constraints & Preferences\n")
	if len(state.constraints) > 0 {
		for _, c := range state.constraints {
			builder.WriteString("- ")
			builder.WriteString(c)
			builder.WriteString("\n")
		}
	} else {
		builder.WriteString("- (none explicitly stated)\n")
	}

	builder.WriteString("\n## Progress\n")
	builder.WriteString("### Done\n")
	doneCount := 0
	for _, d := range state.decisions {
		builder.WriteString("- [x] ")
		builder.WriteString(d)
		builder.WriteString("\n")
		doneCount++
	}
	if doneCount == 0 {
		builder.WriteString("- (no completed items recorded)\n")
	}

	builder.WriteString("\n### In Progress\n")
	builder.WriteString("- (see Next Steps)\n")

	builder.WriteString("\n### Blocked\n")
	blockedCount := 0
	for _, item := range state.openItems {
		lower := strings.ToLower(item)
		if strings.Contains(lower, "blocked") || strings.Contains(lower, "阻塞") || strings.Contains(lower, "风险") || strings.Contains(lower, "blocker") {
			builder.WriteString("- ")
			builder.WriteString(item)
			builder.WriteString("\n")
			blockedCount++
		}
	}
	if blockedCount == 0 {
		builder.WriteString("- (none)\n")
	}

	builder.WriteString("\n## Key Decisions\n")
	if len(mergedReads.Edited) > 0 || len(mergedReads.Written) > 0 {
		builder.WriteString("### Files Modified\n")
		for _, f := range mergedReads.Edited {
			builder.WriteString("- (edited) ")
			builder.WriteString(f)
			builder.WriteString("\n")
		}
		for _, f := range mergedReads.Written {
			builder.WriteString("- (written) ")
			builder.WriteString(f)
			builder.WriteString("\n")
		}
	}
	filesSection := sortedKeys(state.files, maxSummaryFiles)
	if len(filesSection) > 0 {
		builder.WriteString("### Files Referenced\n")
		for _, f := range filesSection {
			builder.WriteString("- ")
			builder.WriteString(f)
			builder.WriteString("\n")
		}
	}

	builder.WriteString("\n## Next Steps\n")
	for _, item := range state.openItems {
		lower := strings.ToLower(item)
		if !strings.Contains(lower, "blocked") && !strings.Contains(lower, "阻塞") && !strings.Contains(lower, "blocker") {
			builder.WriteString("- [ ] ")
			builder.WriteString(item)
			builder.WriteString("\n")
		}
	}
	if len(state.validation) > 0 {
		builder.WriteString("\n### Validation / Commands\n")
		for _, v := range state.validation {
			builder.WriteString("- ")
			builder.WriteString(v)
			builder.WriteString("\n")
		}
	}

	builder.WriteString("\n## Critical Context\n")
	if state.recentAssistant != "" {
		builder.WriteString("- Last assistant: ")
		builder.WriteString(truncateRunes(state.recentAssistant, maxSummaryItemRunes))
		builder.WriteString("\n")
	}
	if len(state.toolNames) > 0 {
		builder.WriteString("- Tools used: ")
		builder.WriteString(strings.Join(sortedKeys(state.toolNames, 12), ", "))
		builder.WriteString("\n")
	}

	// Snapshot the structured metadata (goal / constraints / progress /
	// decisions / files / next steps / critical context) before appending the
	// verbatim sections. The metadata is bounded by its own budget so a long
	// goals list cannot starve the raw user instructions and assistant
	// outputs, which are the parts that must survive compaction.
	metaText := truncateRunes(strings.TrimRight(builder.String(), "\n"), maxSummaryMetadataRunes)

	builder.Reset()
	builder.WriteString(metaText)

	// Recent user messages verbatim
	if len(state.recentUsers) > 0 {
		builder.WriteString("\n### Recent User Messages\n")
		for i, m := range state.recentUsers {
			builder.WriteString(fmt.Sprintf("%d. ```text\n%s\n```\n", i+1, m))
		}
	}

	// Recent assistant outputs verbatim
	if len(state.recentAssistants) > 0 {
		builder.WriteString("\n### Recent Assistant Messages\n")
		for i, m := range state.recentAssistants {
			builder.WriteString(fmt.Sprintf("%d. ```text\n%s\n```\n", i+1, m))
		}
	}

	return truncateRunes(strings.TrimRight(builder.String(), "\n"), maxSummaryTotalRunes)
}
