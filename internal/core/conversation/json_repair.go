package conversation

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaptinlin/jsonrepair"
	"github.com/tim5wang/godex/internal/core/protocol"
)

// validJSONEscapes mirrors the JSON string escape set plus "u" for \uXXXX.
var validJSONEscapes = map[byte]bool{
	'"': true, '\\': true, '/': true, 'b': true,
	'f': true, 'n': true, 'r': true, 't': true, 'u': true,
}

func isJSONControlCharacter(b byte) bool {
	return b >= 0x00 && b <= 0x1f
}

func escapeJSONControlCharacter(b byte) string {
	switch b {
	case '\b':
		return "\\b"
	case '\f':
		return "\\f"
	case '\n':
		return "\\n"
	case '\r':
		return "\\r"
	case '\t':
		return "\\t"
	default:
		return fmt.Sprintf("\\u%04x", b)
	}
}

func isHex4(s string) bool {
	if len(s) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// repairJSONString repairs malformed JSON string literals by:
//   - escaping raw control characters inside strings
//   - doubling backslashes before invalid escape characters
//
// This is a faithful Go port of pi's repairJson (temp/pi/packages/ai/src/utils/json-parse.ts).
// It only rewrites characters inside double-quoted string literals and leaves
// everything else (numbers, booleans, structure) untouched.
func repairJSONString(json string) string {
	var repaired strings.Builder
	repaired.Grow(len(json) + 8)
	inString := false

	for i := 0; i < len(json); i++ {
		ch := json[i]

		if !inString {
			repaired.WriteByte(ch)
			if ch == '"' {
				inString = true
			}
			continue
		}

		if ch == '"' {
			repaired.WriteByte(ch)
			inString = false
			continue
		}

		if ch == '\\' {
			if i+1 >= len(json) {
				// Trailing backslash at end of input: escape it.
				repaired.WriteString("\\\\")
				continue
			}
			next := json[i+1]
			if next == 'u' && i+6 <= len(json) && isHex4(json[i+2:i+6]) {
				// Valid \uXXXX escape: keep verbatim.
				repaired.WriteString(json[i : i+6])
				i += 5
				continue
			}
			if validJSONEscapes[next] {
				// Valid escape like \n \" \\: keep verbatim.
				repaired.WriteByte('\\')
				repaired.WriteByte(next)
				i++
				continue
			}
			// Invalid escape (e.g. \x): double the backslash so the
			// literal backslash survives JSON decoding.
			repaired.WriteString("\\\\")
			continue
		}

		if isJSONControlCharacter(ch) {
			repaired.WriteString(escapeJSONControlCharacter(ch))
		} else {
			repaired.WriteByte(ch)
		}
	}

	return repaired.String()
}

// parseToolArguments parses raw tool-call arguments with progressive repair.
// It never returns a nil map. When the raw JSON cannot be recovered even after
// all repair levels, the returned map carries the reserved __error__ /
// __partial__ keys so the tool runtime can surface an accurate diagnostic to
// the model instead of a misleading schema-validation error.
//
// Repair levels:
//  1. strict json.Unmarshal
//  2. jsonrepair.Repair — full-tolerant parser (missing commas/colons/values,
//     unquoted keys, single/smart quotes, Python literals, markdown fences,
//     comments, truncated input); if it yields valid JSON, unmarshal that
//  3. repairJSONString (control characters / invalid escapes) then retry
//  4. recoverPartialToolInput (structural close of truncated strings/braces)
//  5. unrecoverable: return marked map with the raw fragment
func parseToolArguments(raw string) map[string]interface{} {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]interface{}{}
	}

	// Level 1: strict parse.
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &input); err == nil {
		if input == nil {
			input = map[string]interface{}{}
		}
		return input
	}

	// Level 2: full-tolerant repair via kaptinlin/jsonrepair. This handles
	// missing commas/colons/values, unquoted keys, single/smart quotes,
	// Python-style literals, markdown code fences, comments, and truncated
	// input — the common failure modes of LLM-generated arguments.
	if repaired, err := jsonrepair.Repair(trimmed); err == nil {
		if err := json.Unmarshal([]byte(repaired), &input); err == nil {
			if input == nil {
				input = map[string]interface{}{}
			}
			return input
		}
	}

	// Level 3: repair string literals then retry.
	repaired := repairJSONString(trimmed)
	if repaired != trimmed {
		if err := json.Unmarshal([]byte(repaired), &input); err == nil {
			if input == nil {
				input = map[string]interface{}{}
			}
			return input
		}
	}

	// Level 4: structural recovery for truncated streams (Anthropic path
	// already uses this; on success the map is fully usable).
	recovered, degraded := recoverPartialToolInput(repaired)
	if !degraded {
		return recovered
	}

	// Level 5: unrecoverable. Keep the degraded map (it already carries
	// __error__/__partial__ from recoverPartialToolInput) so the tool
	// runtime can report the actual failure to the model.
	return recovered
}

// hasToolInputError reports whether args carries the reserved error marker
// left by parseToolArguments / recoverPartialToolInput when the raw JSON
// could not be recovered.
func hasToolInputError(args map[string]interface{}) (reason string, partial string, ok bool) {
	if args == nil {
		return "", "", false
	}
	if r, exists := args[protocol.ToolInputErrorKey]; exists {
		reason, _ = r.(string)
	}
	if p, exists := args[protocol.ToolInputPartialKey]; exists {
		partial, _ = p.(string)
	}
	return reason, partial, reason != ""
}
