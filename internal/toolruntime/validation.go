package toolruntime

import (
	"sort"

	"github.com/tim5wang/godex/internal/core/protocol"
)

func validateToolInputSchema(tool string, args map[string]interface{}, schema map[string]interface{}) error {
	// If the raw arguments JSON could not be recovered by the model layer
	// (truncated stream, invalid escapes, control characters), surface that
	// accurate diagnostic instead of a misleading "missing required
	// argument" error. The model should retry with complete, well-formed
	// JSON arguments rather than attempt to patch individual fields.
	if reason, partial, _ := malformedToolInput(args); reason != "" {
		return ErrToolMalformedInput{Tool: tool, Reason: reason, Partial: partial}
	}
	required := requiredToolFields(schema["required"])
	if len(required) == 0 {
		return nil
	}
	missing := make([]string, 0)
	for _, name := range required {
		value, ok := args[name]
		if !ok || value == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return ErrToolInvalidInput{Tool: tool, Missing: missing}
}

// malformedToolInput extracts the reserved damage markers that the model
// layer injects (see conversation.parseToolArguments) when the raw tool
// arguments JSON could not be fully recovered. It returns the reason string
// and the raw fragment; the bool is true when the marker is present.
func malformedToolInput(args map[string]interface{}) (reason, partial string, ok bool) {
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

func requiredToolFields(value interface{}) []string {
	var required []string
	switch typed := value.(type) {
	case []string:
		required = append(required, typed...)
	case []interface{}:
		for _, item := range typed {
			if name, ok := item.(string); ok {
				required = append(required, name)
			}
		}
	}
	sort.Strings(required)
	return required
}
