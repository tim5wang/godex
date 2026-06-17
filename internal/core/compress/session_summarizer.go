package compress

import (
	"context"
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/core/protocol"
)

// FileOperations tracks files read, written, or edited during a session segment.
type FileOperations struct {
	Read    []string `json:"read_files,omitempty"`
	Written []string `json:"written_files,omitempty"`
	Edited  []string `json:"edited_files,omitempty"`
}

// ExtractFileOperationsFromMessages scans messages for tool calls that reference
// file paths and categorizes them as read/write/edit operations.
func ExtractFileOperationsFromMessages(messages []protocol.Message) FileOperations {
	reads := make(map[string]struct{})
	writes := make(map[string]struct{})
	edits := make(map[string]struct{})

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
			opType, known := knownFileToolNames[block.Name]
			switch {
			case opType == "read":
				reads[path] = struct{}{}
			case opType == "write":
				writes[path] = struct{}{}
			case opType == "edit":
				edits[path] = struct{}{}
			case known:
				// Known tool with unrecognized operation type — safe fallback
				reads[path] = struct{}{}
			default:
				// Unknown tool — treat path reference as read (conservative)
				if path != "" {
					reads[path] = struct{}{}
				}
			}
		}
	}

	return FileOperations{
		Read:    sortedKeysSet(reads),
		Written: sortedKeysSet(writes),
		Edited:  sortedKeysSet(edits),
	}
}

// knownFileToolNames maps tool names to file operation categories.
// The keys are tool names used by the godex agent tool catalog.
var knownFileToolNames = map[string]string{
	// Read operations
	"read_file":        "read",
	"read":             "read",
	"read_multiple":    "read",
	"read_files":       "read",
	"grep":             "read",
	"glob":             "read",
	"find":             "read",
	"ls":               "read",
	"search":           "read",
	"history_search":   "read",
	"memory":           "write",
	// Write operations
	"write_file":       "write",
	"write":            "write",
	"overwrite_file":   "write",
	"append_file":      "write",
	// Edit operations
	"edit_file":        "edit",
	"edit":             "edit",
	"edit_files":       "edit",
	"apply_diff":       "edit",
	"patch":            "edit",
	"replace":          "edit",
	"rename":           "edit",
	"move":             "edit",
	"copy":             "write",
	"delete_file":      "edit",
	"delete":           "edit",
	"remove":           "edit",
	"attach_file":      "write",
}

// filePathParamKeys lists common parameter names that hold file paths.
var filePathParamKeys = []string{
	"path", "file_path", "filepath", "file",
	"target", "source", "destination", "src", "dst",
	"old_path", "new_path", "from", "to",
	"dir", "directory", "folder",
}

func extractPathFromToolInput(name string, input map[string]interface{}) string {
	if input == nil {
		return ""
	}
	for _, key := range filePathParamKeys {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func sortedKeysSet(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SessionSummaryRequest carries the context needed by a session summarizer.
// The default implementation is rule-based, but the shape is intentionally
// suitable for a future model-backed summarizer.
type SessionSummaryRequest struct {
	System               string
	History              []protocol.Message
	TokenBreakdown       map[string]int
	TranscriptPath       string
	RecentUserMessages   []string
	ContinuationSnapshot string
	PreviousSummary      string
}

// SessionSummaryResult is the replacement conversation and diagnostics emitted
// by a session summarizer.
type SessionSummaryResult struct {
	Messages       []protocol.Message
	TranscriptRefs []string
	Diagnostics    []string
	RecoveryHint   string
	FileOps        FileOperations
}

// SessionSummarizer produces compacted session history.
type SessionSummarizer interface {
	SummarizeSession(context.Context, SessionSummaryRequest) (SessionSummaryResult, error)
}

// RuleBasedSessionSummarizer adapts the existing semantic compressor to the
// summarizer interface.
type RuleBasedSessionSummarizer struct {
	compressor *Compressor
}

func NewRuleBasedSessionSummarizer(compressor *Compressor) *RuleBasedSessionSummarizer {
	return &RuleBasedSessionSummarizer{compressor: compressor}
}

func (s *RuleBasedSessionSummarizer) SummarizeSession(_ context.Context, req SessionSummaryRequest) (SessionSummaryResult, error) {
	if s == nil || s.compressor == nil {
		return SessionSummaryResult{Messages: protocol.CloneMessages(req.History)}, nil
	}
	messages, err := s.compressor.CompactWithSnapshot(req.History, req.System, req.ContinuationSnapshot)
	if err != nil {
		return SessionSummaryResult{}, err
	}
	fileOps := extractFileOpsFromHistory(req.History)
	return SessionSummaryResult{
		Messages:       messages,
		TranscriptRefs: transcriptRefs(messages),
		FileOps:        fileOps,
	}, nil
}

func transcriptRefs(messages []protocol.Message) []string {
	seen := make(map[string]struct{}, len(messages))
	refs := make([]string, 0, len(messages))
	for _, msg := range messages {
		if msg.Metadata == nil {
			continue
		}
		ref := strings.TrimSpace(msg.Metadata.Transcript)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}
