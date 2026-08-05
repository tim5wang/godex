package toolruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/protocol"
)

// TestToolHandlerSurfacesMalformedJSONArguments verifies that when the model
// layer leaves the unrecoverable-arguments marker (truncated stream, invalid
// escapes, control characters), the runtime surfaces an accurate
// ErrToolMalformedInput instead of a misleading "missing required argument".
func TestToolHandlerSurfacesMalformedJSONArguments(t *testing.T) {
	handler := NewToolHandler()
	handler.Register(requiredArgTool{fakeTool{name: "write_file"}})

	_, err := handler.Handle(context.Background(), "write_file", map[string]interface{}{
		protocol.ToolInputErrorKey:   "streamed_tool_input_truncated",
		protocol.ToolInputPartialKey: `{"content": "unterminated`,
	})
	var malformed ErrToolMalformedInput
	if !errors.As(err, &malformed) {
		t.Fatalf("expected ErrToolMalformedInput, got %v", err)
	}
	if malformed.Tool != "write_file" {
		t.Fatalf("unexpected tool: %q", malformed.Tool)
	}
	if malformed.Reason != "streamed_tool_input_truncated" {
		t.Fatalf("unexpected reason: %q", malformed.Reason)
	}
	msg := err.Error()
	for _, want := range []string{"malformed JSON arguments", "streamed_tool_input_truncated", "raw arguments", "retry with complete"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error message %q missing %q", msg, want)
		}
	}
}

// TestToolHandlerStillRejectsMissingFields confirms the malformed-input path
// does not shadow the ordinary schema validation: a well-formed object that
// merely lacks a required field must still yield ErrToolInvalidInput.
func TestToolHandlerStillRejectsMissingFields(t *testing.T) {
	handler := NewToolHandler()
	handler.Register(requiredArgTool{fakeTool{name: "write_file"}})

	_, err := handler.Handle(context.Background(), "write_file", map[string]interface{}{"content": "hello"})
	var invalid ErrToolInvalidInput
	if !errors.As(err, &invalid) {
		t.Fatalf("expected ErrToolInvalidInput for missing field, got %v", err)
	}
	if invalid.Tool != "write_file" || len(invalid.Missing) != 1 || invalid.Missing[0] != "path" {
		t.Fatalf("unexpected invalid input details: %+v", invalid)
	}
}

// TestErrToolMalformedInputBoundsPartial verifies the raw fragment is capped
// so a huge truncated payload does not flood the conversation.
func TestErrToolMalformedInputBoundsPartial(t *testing.T) {
	huge := strings.Repeat("x", maxMalformedPartialChars*2)
	err := ErrToolMalformedInput{Tool: "bash", Reason: "streamed_tool_input_truncated", Partial: huge}
	msg := err.Error()
	if !strings.Contains(msg, "malformed JSON arguments") {
		t.Fatalf("missing header in %q", msg)
	}
	if len(msg) > maxMalformedPartialChars+200 {
		t.Fatalf("error message too large (%d chars), partial not bounded", len(msg))
	}
}
