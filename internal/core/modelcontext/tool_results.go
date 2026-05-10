package modelcontext

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

const (
	MaxVisibleToolResultBytes = 32 * 1024
	ToolResultPreviewHead     = 3 * 1024
	ToolResultPreviewTail     = 1 * 1024
)

// LargeToolResultSummary is the compact model-visible replacement for a large
// tool result. The full result should live in an artifact or transcript.
type LargeToolResultSummary struct {
	Status       string `json:"status"`
	ToolName     string `json:"tool_name,omitempty"`
	ToolUseID    string `json:"tool_use_id,omitempty"`
	Bytes        int    `json:"bytes"`
	SHA256       string `json:"sha256"`
	ArtifactPath string `json:"artifact_path,omitempty"`
	Transcript   string `json:"transcript,omitempty"`
	Preview      string `json:"preview,omitempty"`
	Note         string `json:"note,omitempty"`
}

func TooLargeForModel(text string) bool {
	return len([]byte(text)) > MaxVisibleToolResultBytes
}

func SHA256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", sum[:])
}

func TruncatedPreview(text string) string {
	if len([]byte(text)) <= ToolResultPreviewHead+ToolResultPreviewTail {
		return text
	}
	head := validPrefix(text, ToolResultPreviewHead)
	tail := validSuffix(text, ToolResultPreviewTail)
	return head + "\n\n...[tool result truncated]...\n\n" + tail
}

func SummaryJSON(summary LargeToolResultSummary) string {
	if summary.Status == "" {
		summary.Status = "tool_result_truncated"
	}
	if summary.Note == "" {
		summary.Note = "Large tool result was removed from model-visible context; use the referenced artifact or transcript for full output."
	}
	data, err := json.Marshal(summary)
	if err != nil {
		return fmt.Sprintf(`{"status":"tool_result_truncated","bytes":%d,"sha256":%q}`, summary.Bytes, summary.SHA256)
	}
	if len(data) > MaxVisibleToolResultBytes && summary.Preview != "" {
		summary.Preview = validPrefix(summary.Preview, 2048)
		data, err = json.Marshal(summary)
		if err != nil {
			return fmt.Sprintf(`{"status":"tool_result_truncated","bytes":%d,"sha256":%q}`, summary.Bytes, summary.SHA256)
		}
	}
	if len(data) > MaxVisibleToolResultBytes {
		summary.Preview = ""
		data, err = json.Marshal(summary)
		if err != nil {
			return fmt.Sprintf(`{"status":"tool_result_truncated","bytes":%d,"sha256":%q}`, summary.Bytes, summary.SHA256)
		}
	}
	return string(data)
}

func validPrefix(text string, maxBytes int) string {
	if len([]byte(text)) <= maxBytes {
		return text
	}
	if maxBytes <= 0 {
		return ""
	}
	end := maxBytes
	if end > len(text) {
		end = len(text)
	}
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end]
}

func validSuffix(text string, maxBytes int) string {
	if len([]byte(text)) <= maxBytes {
		return text
	}
	if maxBytes <= 0 {
		return ""
	}
	start := len(text) - maxBytes
	if start < 0 {
		start = 0
	}
	for start < len(text) && !utf8.ValidString(text[start:]) {
		start++
	}
	return text[start:]
}
