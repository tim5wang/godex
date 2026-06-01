package compress

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tim5wang/godex/internal/core/modelcontext"
	"github.com/tim5wang/godex/internal/core/protocol"
)

const (
	maxSummaryGoals       = 8
	maxSummaryConstraints = 8
	maxSummaryDecisions   = 10
	maxSummaryFiles       = 24
	maxSummaryValidation  = 8
	maxSummaryOpenItems   = 8
	maxSummaryItemRunes   = 220
	maxSummaryTotalRunes  = 6500
	maxRecentUserMessages = 6
	maxRecentUserRunes    = 1200
	maxRecentUserTotal    = 5000
)

var (
	pathTokenPattern      = regexp.MustCompile(`[A-Za-z0-9_.@+\-]*\.{0,2}/[A-Za-z0-9_./@+\-]+|/[A-Za-z0-9_./@+\-]+`)
	numberedBulletPattern = regexp.MustCompile(`^\d+[\.)]\s+`)
)

// Compressor handles message compression.
type Compressor struct {
	transcriptsDir string
	mu             sync.Mutex
	lastHash       [32]byte
	lastResult     []protocol.Message
	hasCached      bool
}

// NewCompressor creates a new compressor.
func NewCompressor(transcriptsDir string) *Compressor {
	return &Compressor{transcriptsDir: transcriptsDir}
}

// CountTokens estimates token count using character-class ratios.
//
// Ratios calibrated for Claude/GPT tokenizers (approximate):
//   - ASCII letters/digits: 4 chars per token
//   - CJK / Hangul / Kana: 2 chars per token (not 1:1)
//   - Whitespace: 8 chars per token (mostly overhead)
//   - Other (punctuation, symbols, etc.): 3 chars per token
func CountTokens(text string) int {
	if text == "" {
		return 0
	}

	asciiAlpha := 0
	digits := 0
	whitespace := 0
	cjk := 0
	other := 0

	for _, r := range text {
		switch {
		case r > utf8.RuneSelf && (unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)):
			cjk++
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			whitespace++
		case r >= '0' && r <= '9':
			digits++
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z' || r == '_'):
			asciiAlpha++
		case r <= utf8.RuneSelf:
			other++
		default:
			other++
		}
	}

	total := (asciiAlpha+3)/4 + (digits+2)/3 + whitespace/8 + (cjk+1)/2 + (other+2)/3
	if total == 0 {
		return 1
	}
	return total
}

// Compact saves the current transcript, emits a structured summary message, and keeps recent history.
func (c *Compressor) Compact(messages []protocol.Message, _ string) ([]protocol.Message, error) {
	return c.CompactWithSnapshot(messages, "", "")
}

// CompactWithSnapshot saves the transcript and pins deterministic continuation
// state into the summary before semantic history reduction.
func (c *Compressor) CompactWithSnapshot(messages []protocol.Message, _ string, continuationSnapshot string) ([]protocol.Message, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	hash, err := c.hashMessagesWithSnapshot(messages, continuationSnapshot)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if c.hasCached && hash == c.lastHash {
		cached := protocol.CloneMessages(c.lastResult)
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	filename, err := c.saveTranscript(messages)
	if err != nil {
		return nil, err
	}

	summaryText := buildSemanticSummary(messages, filename, time.Now(), continuationSnapshot)
	compact := []protocol.Message{protocol.NewSummaryMessage(summaryText, filename)}

	const keepLast = 10
	recent := messages
	if len(messages) > keepLast {
		recent = messages[len(messages)-keepLast:]
	}

	for _, msg := range recent {
		compact = append(compact, sanitizeRecentMessageForContext(msg, filename))
	}

	c.mu.Lock()
	c.lastHash = hash
	c.lastResult = protocol.CloneMessages(compact)
	c.hasCached = true
	c.mu.Unlock()

	return compact, nil
}

func sanitizeRecentMessageForContext(msg protocol.Message, transcript string) protocol.Message {
	cloned := msg.Clone()
	for i, block := range cloned.Content {
		if block.Type != protocol.BlockToolResult || !modelcontext.TooLargeForModel(block.Content) {
			continue
		}
		cloned.Content[i].Content = modelcontext.SummaryJSON(modelcontext.LargeToolResultSummary{
			ToolUseID:  block.ToolUseID,
			Bytes:      len([]byte(block.Content)),
			SHA256:     modelcontext.SHA256Hex(block.Content),
			Transcript: transcript,
			Preview:    modelcontext.TruncatedPreview(block.Content),
			Note:       "Large tool result was removed from compacted model-visible context; the full output is in the saved transcript.",
		})
	}
	return cloned
}

func (c *Compressor) getTranscriptFilename() string {
	now := time.Now()
	return fmt.Sprintf("transcript_%s_%d.json", now.Format("20060102_150405"), now.UnixNano())
}

func (c *Compressor) saveTranscript(messages []protocol.Message) (string, error) {
	filename := c.getTranscriptFilename()
	path := filepath.Join(c.transcriptsDir, filename)

	data, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(c.transcriptsDir, 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return filename, nil
}

func (c *Compressor) hashMessages(messages []protocol.Message) ([32]byte, error) {
	return c.hashMessagesWithSnapshot(messages, "")
}

func (c *Compressor) hashMessagesWithSnapshot(messages []protocol.Message, continuationSnapshot string) ([32]byte, error) {
	if strings.TrimSpace(continuationSnapshot) == "" {
		data, err := json.Marshal(messages)
		if err != nil {
			return [32]byte{}, err
		}
		return sha256.Sum256(data), nil
	}
	payload := struct {
		Messages             []protocol.Message `json:"messages"`
		ContinuationSnapshot string             `json:"continuation_snapshot,omitempty"`
	}{Messages: messages, ContinuationSnapshot: strings.TrimSpace(continuationSnapshot)}
	data, err := json.Marshal(payload)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(data), nil
}

type semanticSummary struct {
	goals           []string
	constraints     []string
	decisions       []string
	files           map[string]struct{}
	validation      []string
	openItems       []string
	previous        []string
	recentUser      string
	recentAssistant string
	recentUsers     []string
	toolNames       map[string]struct{}
	fileOps         FileOperations
}

func buildSemanticSummary(messages []protocol.Message, transcript string, at time.Time, continuationSnapshot string) string {
	state := semanticSummary{
		files:     make(map[string]struct{}),
		toolNames: make(map[string]struct{}),
	}

	for _, msg := range messages {
		if msg.Metadata != nil && msg.Metadata.Ephemeral {
			continue
		}
		text := messageSemanticText(msg)
		state.collectPaths(text)
		state.collectValidation(text)
		state.collectOpenItems(text)

		for _, block := range msg.Content {
			switch block.Type {
			case protocol.BlockToolUse:
				if strings.TrimSpace(block.Name) != "" {
					state.toolNames[block.Name] = struct{}{}
				}
				visitStructuredStrings(block.Input, func(value string) {
					state.collectPaths(value)
					state.collectValidation(value)
					state.collectOpenItems(value)
				})
				// Collect file operations from tool calls
				if path := extractPathFromToolInput(block.Name, block.Input); path != "" {
					switch block.Name {
					case "read_file", "read", "read_multiple", "read_files":
						state.fileOps.Read = addUnique(state.fileOps.Read, path)
					case "write_file", "write":
						state.fileOps.Written = addUnique(state.fileOps.Written, path)
					case "edit_file", "edit", "edit_files":
						state.fileOps.Edited = addUnique(state.fileOps.Edited, path)
					default:
						state.fileOps.Read = addUnique(state.fileOps.Read, path)
					}
				}
			case protocol.BlockToolResult:
				state.collectValidation(block.Content)
				state.collectOpenItems(block.Content)
				state.collectPaths(block.Content)
			}
		}

		humanText := strings.TrimSpace(protocol.MessageText(msg))
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindSummary {
			addUniqueLimited(&state.previous, normalizeWhitespace(humanText), 3)
			continue
		}

		switch msg.Role {
		case protocol.RoleUser:
			if humanText == "" || hasToolResult(msg) {
				continue
			}
			normalized := normalizeWhitespace(humanText)
			if !isLowSignalAck(normalized) {
				state.recentUser = normalized
				addRecentUserVerbatim(&state.recentUsers, humanText)
				addUniqueLimited(&state.goals, normalized, 64)
				for _, fragment := range splitFragments(humanText) {
					if isConstraintFragment(fragment) {
						addUniqueLimited(&state.constraints, fragment, maxSummaryConstraints)
					}
				}
			}
		case protocol.RoleAssistant:
			if humanText == "" {
				continue
			}
			normalized := normalizeWhitespace(humanText)
			state.recentAssistant = normalized
			for _, fragment := range splitFragments(humanText) {
				if isDecisionFragment(fragment) {
					addUniqueLimited(&state.decisions, fragment, maxSummaryDecisions)
				}
				if isOpenItemFragment(fragment) {
					addUniqueLimited(&state.openItems, fragment, maxSummaryOpenItems)
				}
			}
		}
	}

	// Compute merged file operations (edited/written files excluded from read-only)
	mergedReads := mergeFileOps(state.fileOps)

	var builder strings.Builder
	builder.WriteString("## Session Compaction Summary\n")
	builder.WriteString("Compressed at: ")
	builder.WriteString(at.Format("2006-01-02 15:04"))
	builder.WriteString("\nTranscript: ")
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

	// Recent user messages verbatim
	if len(state.recentUsers) > 0 {
		builder.WriteString("\n### Recent User Messages\n")
		for i, m := range state.recentUsers {
			builder.WriteString(fmt.Sprintf("%d. ```text\n%s\n```\n", i+1, m))
		}
	}

	return truncateRunes(strings.TrimRight(builder.String(), "\n"), maxSummaryTotalRunes)
}

// addUnique appends value to slice if not already present, returning the slice.
func addUnique(slice []string, value string) []string {
	for _, s := range slice {
		if s == value {
			return slice
		}
	}
	return append(slice, value)
}

// mergeFileOps removes read-only entries that overlap with edited/written files.
func mergeFileOps(ops FileOperations) FileOperations {
	modified := make(map[string]struct{}, len(ops.Edited)+len(ops.Written))
	for _, f := range ops.Edited {
		modified[f] = struct{}{}
	}
	for _, f := range ops.Written {
		modified[f] = struct{}{}
	}
	readOnly := make([]string, 0, len(ops.Read))
	for _, f := range ops.Read {
		if _, ok := modified[f]; !ok {
			readOnly = append(readOnly, f)
		}
	}
	ops.Read = readOnly
	return ops
}

func (s *semanticSummary) collectPaths(text string) {
	for _, path := range extractPathTokens(text) {
		s.files[path] = struct{}{}
	}
}

func (s *semanticSummary) collectValidation(text string) {
	for _, fragment := range splitFragments(text) {
		if isValidationFragment(fragment) {
			addUniqueLimited(&s.validation, fragment, maxSummaryValidation)
		}
	}
}

func (s *semanticSummary) collectOpenItems(text string) {
	for _, fragment := range splitFragments(text) {
		if isOpenItemFragment(fragment) {
			addUniqueLimited(&s.openItems, fragment, maxSummaryOpenItems)
		}
	}
}

func messageSemanticText(msg protocol.Message) string {
	parts := make([]string, 0, len(msg.Content))
	for _, block := range msg.Content {
		switch block.Type {
		case protocol.BlockText:
			parts = append(parts, block.Text)
		case protocol.BlockToolUse:
			input := ""
			if len(block.Input) > 0 {
				if data, err := json.Marshal(block.Input); err == nil {
					input = string(data)
				}
			}
			parts = append(parts, strings.TrimSpace("Tool use "+block.Name+" "+input))
		case protocol.BlockToolResult:
			parts = append(parts, "Tool result "+block.ToolUseID+": "+truncateRunes(block.Content, 2000))
		}
	}
	return strings.Join(parts, "\n")
}

// extractFileOpsFromHistory extracts FileOperations from a message history.
func extractFileOpsFromHistory(messages []protocol.Message) FileOperations {
	var ops FileOperations
	for _, msg := range messages {
		if msg.Role != protocol.RoleAssistant {
			continue
		}
		for _, block := range msg.Content {
			if block.Type != protocol.BlockToolUse {
				continue
			}
			path := extractPathFromToolInput(block.Name, block.Input)
			if path == "" {
				continue
			}
			switch block.Name {
			case "read_file", "read", "read_multiple", "read_files":
				ops.Read = addUnique(ops.Read, path)
			case "write_file", "write":
				ops.Written = addUnique(ops.Written, path)
			case "edit_file", "edit", "edit_files":
				ops.Edited = addUnique(ops.Edited, path)
			default:
				ops.Read = addUnique(ops.Read, path)
			}
		}
	}
	return mergeFileOps(ops)
}

// ExtractFileOpsSummary returns a formatted string of file operations.
func ExtractFileOpsSummary(ops FileOperations) string {
	if len(ops.Read) == 0 && len(ops.Written) == 0 && len(ops.Edited) == 0 {
		return ""
	}
	var b strings.Builder
	if len(ops.Edited) > 0 {
		b.WriteString("\nEdited files:\n")
		for _, f := range ops.Edited {
			b.WriteString("- ")
			b.WriteString(f)
			b.WriteString("\n")
		}
	}
	if len(ops.Written) > 0 {
		b.WriteString("\nWritten files:\n")
		for _, f := range ops.Written {
			b.WriteString("- ")
			b.WriteString(f)
			b.WriteString("\n")
		}
	}
	if len(ops.Read) > 0 {
		b.WriteString("\nReferenced files (read):\n")
		for _, f := range ops.Read {
			b.WriteString("- ")
			b.WriteString(f)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func hasToolResult(msg protocol.Message) bool {
	for _, block := range msg.Content {
		if block.Type == protocol.BlockToolResult {
			return true
		}
	}
	return false
}

func visitStructuredStrings(value interface{}, visit func(string)) {
	switch typed := value.(type) {
	case string:
		visit(typed)
	case []interface{}:
		for _, item := range typed {
			visitStructuredStrings(item, visit)
		}
	case []string:
		for _, item := range typed {
			visit(item)
		}
	case map[string]interface{}:
		for key, item := range typed {
			visit(key)
			visitStructuredStrings(item, visit)
		}
	case map[string]string:
		for key, item := range typed {
			visit(key)
			visit(item)
		}
	}
}

func writeSection(builder *strings.Builder, title string, items []string) {
	builder.WriteString("\n")
	builder.WriteString(title)
	builder.WriteString(":\n")
	if len(items) == 0 {
		builder.WriteString("- Not captured in compacted history.\n")
		return
	}
	for _, item := range items {
		item = normalizeWhitespace(item)
		if item == "" {
			continue
		}
		builder.WriteString("- ")
		builder.WriteString(truncateRunes(item, maxSummaryItemRunes))
		builder.WriteString("\n")
	}
}

func writePinnedContinuationSnapshot(builder *strings.Builder, snapshot string) {
	snapshot = strings.TrimSpace(snapshot)
	if snapshot == "" {
		return
	}
	builder.WriteString("\nPinned continuation state:\n")
	builder.WriteString(limitRunes(snapshot, 5000))
	builder.WriteString("\n")
}

func writeVerbatimSection(builder *strings.Builder, title string, items []string) {
	builder.WriteString("\n")
	builder.WriteString(title)
	builder.WriteString(":\n")
	if len(items) == 0 {
		builder.WriteString("- Not captured in compacted history.\n")
		return
	}
	for i, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		builder.WriteString(fmt.Sprintf("%d. ```text\n", i+1))
		builder.WriteString(limitRunes(item, maxRecentUserRunes))
		builder.WriteString("\n```\n")
	}
}

func addRecentUserVerbatim(items *[]string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	value = limitRunes(value, maxRecentUserRunes)
	for _, existing := range *items {
		if existing == value {
			return
		}
	}
	*items = append(*items, value)
	if len(*items) > maxRecentUserMessages {
		*items = (*items)[len(*items)-maxRecentUserMessages:]
	}
	total := 0
	start := 0
	for i := len(*items) - 1; i >= 0; i-- {
		total += utf8.RuneCountInString((*items)[i])
		if total > maxRecentUserTotal {
			start = i + 1
			break
		}
	}
	if start > 0 {
		*items = (*items)[start:]
	}
}

func splitFragments(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	replacer := strings.NewReplacer("。", "。\n", "！", "！\n", "？", "？\n", "; ", ";\n", ". ", ".\n")
	text = replacer.Replace(text)

	lines := strings.Split(text, "\n")
	fragments := make([]string, 0, len(lines))
	for _, line := range lines {
		line = trimListPrefix(strings.TrimSpace(line))
		line = normalizeWhitespace(line)
		if line == "" {
			continue
		}
		fragments = append(fragments, truncateRunes(line, maxSummaryItemRunes))
	}
	return fragments
}

func trimListPrefix(line string) string {
	line = strings.TrimSpace(line)
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return strings.TrimSpace(numberedBulletPattern.ReplaceAllString(line, ""))
}

func normalizeWhitespace(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func isLowSignalAck(text string) bool {
	normalized := strings.ToLower(strings.Trim(strings.TrimSpace(text), " .,!?:;，。！？：；"))
	switch normalized {
	case "", "ok", "okay", "yes", "yep", "好的", "好", "可以", "继续", "下一步", "执行下一步", "好，继续", "好,继续", "好，执行下一步", "ok，下一步", "ok,下一步":
		return true
	}
	runeCount := utf8.RuneCountInString(normalized)
	if runeCount <= 16 && (strings.Contains(normalized, "下一步") || strings.Contains(normalized, "继续完善") || strings.Contains(normalized, "继续")) {
		return true
	}
	return false
}

func isConstraintFragment(text string) bool {
	lower := strings.ToLower(text)
	return containsAny(lower,
		"must", "should", "need to", "keep", "preserve", "do not", "don't", "avoid", "require", "constraint", "prefer", "goal",
		"必须", "需要", "保留", "不要", "不能", "避免", "希望", "目标", "优先", "约束",
	)
}

func isDecisionFragment(text string) bool {
	lower := strings.ToLower(text)
	return containsAny(lower,
		"implemented", "added", "fixed", "updated", "changed", "removed", "wired", "enabled", "refactored", "completed", "now uses", "switched",
		"实现", "新增", "修复", "更新", "改为", "接入", "支持", "完成", "迁移", "删除",
	)
}

func isValidationFragment(text string) bool {
	lower := strings.ToLower(text)
	return containsAny(lower,
		"go test", "pnpm build", "pnpm test", "pnpm install", "npm test", "npm run", "yarn test", "make test", "pytest", "cargo test", "git diff --check", "go vet", "./scripts/",
		"test plan", "tests passed", "passed", "pass ", "failed", "build failed", "测试", "验证", "构建",
	)
}

func isOpenItemFragment(text string) bool {
	if isLowSignalAck(text) {
		return false
	}
	lower := strings.ToLower(text)
	return containsAny(lower,
		"todo", "next", "remaining", "pending", "blocked", "blocker", "open issue", "risk", "follow-up", "not yet", "failed", "error",
		"下一步", "待", "剩余", "未解决", "风险", "阻塞", "失败", "错误", "继续",
	)
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func extractPathTokens(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	matches := pathTokenPattern.FindAllString(text, -1)
	paths := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		path := strings.Trim(match, " \t\r\n\"'`()[]{}<>.,;:")
		if !isUsefulPathToken(path) {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}

func isUsefulPathToken(path string) bool {
	if path == "" || len(path) > 180 {
		return false
	}
	if strings.Contains(path, "://") || strings.Contains(path, "...") {
		return false
	}
	if !strings.Contains(path, "/") {
		return false
	}
	if path == "./" || path == "../" || path == "/" {
		return false
	}
	return true
}

func addUniqueLimited(items *[]string, value string, limit int) {
	value = normalizeWhitespace(value)
	if value == "" {
		return
	}
	value = truncateRunes(value, maxSummaryItemRunes)
	for _, existing := range *items {
		if existing == value {
			return
		}
	}
	*items = append(*items, value)
	if limit > 0 && len(*items) > limit {
		*items = (*items)[len(*items)-limit:]
	}
}

func compactItems(items []string, limit int) []string {
	if limit <= 0 || len(items) <= limit {
		return append([]string{}, items...)
	}
	headCount := 2
	if limit <= headCount {
		return append([]string{}, items[len(items)-limit:]...)
	}
	result := append([]string{}, items[:headCount]...)
	tail := items[len(items)-(limit-headCount):]
	for _, item := range tail {
		duplicate := false
		for _, existing := range result {
			if existing == item {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, item)
		}
	}
	return result
}

func sortedKeys(values map[string]struct{}, limit int) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	return keys
}

func truncateRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "..."
}
