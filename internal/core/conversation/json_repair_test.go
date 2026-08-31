package conversation

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestRepairJSONString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"valid json unchanged", `{"a": 1}`, `{"a": 1}`},
		{"string content untouched", `{"a": "hello"}`, `{"a": "hello"}`},
		{"raw newline inside string escaped", "{\"a\": \"line1\nline2\"}", "{\"a\": \"line1\\nline2\"}"},
		{"raw tab inside string escaped", "{\"a\": \"x\t y\"}", "{\"a\": \"x\\t y\"}"},
		{"invalid escape doubled", `{"a": "\x41"}`, `{"a": "\\x41"}`},
		{"valid unicode escape kept", `{"a": "\u4f60"}`, `{"a": "\u4f60"}`},
		{"valid short escapes kept", `{"a": "\n\t\"\\"}`, `{"a": "\n\t\"\\"}`},
		{"trailing backslash doubled", `{"a": "x\`, `{"a": "x\\`},
		{"control char in key escaped", "{\"a\\u0001b\": 1}", "{\"a\\u0001b\": 1}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := repairJSONString(tc.in)
			if got != tc.want {
				t.Fatalf("repairJSONString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseToolArguments(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantField string // key whose value we assert
		wantValue interface{}
		wantErr   bool // carries __error__ marker
	}{
		{"empty", "", "", nil, false},
		{"valid object", `{"path": "a.ts", "content": "x"}`, "path", "a.ts", false},
		// Array root is not a valid tool-arguments object; strict parse
		// fails and repair cannot turn it into an object, so it is
		// marked rather than silently flattened.
		{"array root marked", `[1,2,3]`, "", nil, true},
		{"raw newline repaired", "{\"content\": \"line1\nline2\"}", "content", "line1\nline2", false},
		// jsonrepair removes the invalid \x escape, keeping the raw text.
		{"invalid escape repaired", `{"content": "\x41"}`, "content", "x41", false},
		{"truncated object recovered", `{"content": "hello`, "content", "hello", false},
		// jsonrepair fills a truncated key name with null rather than
		// abandoning the whole object.
		{"truncated mid-key recovered", `{"path": "a.ts", "con`, "path", "a.ts", false},
		{"garbage marked", `not json at all`, "", nil, true},
		// jsonrepair balances mismatched brackets. JSON numbers decode as
		// float64, so the expected slice uses float64 elements.
		{"mismatched braces recovered", `{"a": [1, 2}`, "a", []interface{}{float64(1), float64(2)}, false},
		// ---- jsonrepair-only capabilities ----
		{"unquoted key + single quotes", `{name: 'John'}`, "name", "John", false},
		{"missing comma between keys", `{"a":1 "b":2}`, "b", float64(2), false},
		{"python-style literals", `{'a': None, 'b': True}`, "b", true, false},
		{"markdown code fence", "```json\n{\"a\": 1}\n```", "a", float64(1), false},
		{"missing object value", `{"a": }`, "a", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseToolArguments(tc.raw)
			if got == nil {
				t.Fatal("parseToolArguments returned nil map")
			}
			reason, _, marked := hasToolInputError(got)
			if marked != tc.wantErr {
				t.Fatalf("hasToolInputError = %v (reason %q), want %v", marked, reason, tc.wantErr)
			}
			if marked {
				return
			}
			if tc.wantField != "" {
				v, ok := got[tc.wantField]
				if !ok {
					t.Fatalf("parsed args missing %q: %v", tc.wantField, got)
				}
				if !reflect.DeepEqual(v, tc.wantValue) {
					t.Fatalf("args[%q] = %v, want %v (full: %v)", tc.wantField, v, tc.wantValue, got)
				}
			}
		})
	}
}

func TestParseToolArgumentsPreservesRepairedControlChars(t *testing.T) {
	// A literal newline inside a JSON string value must survive the repair
	// round-trip as the real newline character (not the two-byte escape).
	raw := "{\"content\": \"line1\nline2\"}"
	got := parseToolArguments(raw)
	content, _ := got["content"].(string)
	if !strings.Contains(content, "\n") {
		t.Fatalf("expected real newline in repaired value, got %q", content)
	}
	if strings.Contains(content, `\n`) {
		t.Fatalf("expected unescaped newline, got escaped %q", content)
	}
}

func TestParseToolArgumentsNeverMutatesOnStrictPath(t *testing.T) {
	raw := `{"path": "a.ts", "content": "ok"}`
	got := parseToolArguments(raw)
	if reason, _, marked := hasToolInputError(got); marked {
		t.Fatalf("strict JSON unexpectedly marked: %q", reason)
	}
	if got["path"] != "a.ts" {
		t.Fatalf("unexpected path value: %v", got["path"])
	}
}

func TestToolInputErrorMarkerConstantsMatchProtocol(t *testing.T) {
	if protocol.ToolInputErrorKey != "__error__" || protocol.ToolInputPartialKey != "__partial__" {
		t.Fatalf("protocol constants changed unexpectedly: %q %q", protocol.ToolInputErrorKey, protocol.ToolInputPartialKey)
	}
}
