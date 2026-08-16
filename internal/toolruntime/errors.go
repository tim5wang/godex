package toolruntime

import (
	"fmt"
	"strings"
)

// ErrToolNotFound is returned when a tool is not found
type ErrToolNotFound struct {
	Name      string
	Available []string
}

func (e ErrToolNotFound) Error() string {
	if len(e.Available) == 0 {
		return fmt.Sprintf("tool not found: %s", e.Name)
	}
	return fmt.Sprintf("tool not found: %s. Active tools: %s.", e.Name, strings.Join(e.Available, ", "))
}

// ErrToolInvalidInput is returned before execution when arguments cannot match
// a tool's declared input schema.
type ErrToolInvalidInput struct {
	Tool    string
	Missing []string
}

func (e ErrToolInvalidInput) Error() string {
	if len(e.Missing) == 0 {
		return fmt.Sprintf("tool %q received invalid arguments", e.Tool)
	}
	return fmt.Sprintf("tool %q missing required argument(s): %s", e.Tool, strings.Join(e.Missing, ", "))
}

// maxMalformedPartialChars bounds how much of the raw (unrecoverable)
// arguments fragment is echoed back to the model in the error message, so a
// huge truncated payload does not flood the conversation.
const maxMalformedPartialChars = 400

// ErrToolMalformedInput is returned when a tool call's arguments JSON could
// not be parsed even after best-effort repair (see conversation.parseToolArguments).
// Unlike ErrToolInvalidInput (which means the model sent a structurally valid
// object missing required fields), this signals the raw arguments were damaged
// in transit — truncated stream, invalid escapes, or control characters — so the
// model should retry with complete, well-formed JSON rather than "fix" a field.
type ErrToolMalformedInput struct {
	Tool    string
	Reason  string // e.g. "streamed_tool_input_truncated"
	Partial string // raw arguments fragment (bounded in Error())
}

func (e ErrToolMalformedInput) Error() string {
	msg := fmt.Sprintf("tool %q received malformed JSON arguments", e.Tool)
	if e.Reason != "" {
		msg += fmt.Sprintf(" (%s)", e.Reason)
	}
	if e.Partial != "" {
		p := e.Partial
		if len(p) > maxMalformedPartialChars {
			p = p[:maxMalformedPartialChars] + "…"
		}
		msg += fmt.Sprintf("; raw arguments: %s", p)
	}
	msg += "; retry with complete, well-formed JSON arguments"
	return msg
}

// ErrToolInactive is returned when a registered tool is not currently active.
type ErrToolInactive struct {
	Name   string
	Bundle string
}

func (e ErrToolInactive) Error() string {
	if e.Bundle == "" {
		return fmt.Sprintf("tool %q is not active", e.Name)
	}
	return fmt.Sprintf("tool %q is not active; enable bundle %q with tool_exchange", e.Name, e.Bundle)
}

// ErrToolDraining is returned when a tool registration is being torn down
// (dynamic uninstall or reload) and must not accept new calls.
type ErrToolDraining struct {
	Name string
}

func (e ErrToolDraining) Error() string {
	return fmt.Sprintf("tool %q is being unloaded and cannot accept new calls", e.Name)
}

// ErrToolConflict is returned when a tool name is registered by a different
// non-empty owner (e.g. two plugins claim the same tool name).
type ErrToolConflict struct {
	Name  string
	Owner string
}

func (e ErrToolConflict) Error() string {
	return fmt.Sprintf("tool %q is already owned by %q", e.Name, e.Owner)
}

// ErrPermissionDenied is returned when a tool call is blocked by permission policy.
type ErrPermissionDenied struct {
	Tool   string
	Action string
	Reason string
}

func (e ErrPermissionDenied) Error() string {
	switch {
	case e.Action != "" && e.Reason != "":
		return fmt.Sprintf("tool %q action %q denied: %s", e.Tool, e.Action, e.Reason)
	case e.Reason != "":
		return fmt.Sprintf("tool %q denied: %s", e.Tool, e.Reason)
	default:
		return fmt.Sprintf("tool %q denied by permission policy", e.Tool)
	}
}

// ErrPermissionPending is returned when a tool call requires explicit approval.
type ErrPermissionPending struct {
	Tool      string
	Action    string
	RequestID string
	Reason    string
}

func (e ErrPermissionPending) Error() string {
	switch {
	case e.Action != "" && e.RequestID != "" && e.Reason != "":
		return fmt.Sprintf("tool %q action %q requires approval (%s): %s", e.Tool, e.Action, e.RequestID, e.Reason)
	case e.RequestID != "" && e.Reason != "":
		return fmt.Sprintf("tool %q requires approval (%s): %s", e.Tool, e.RequestID, e.Reason)
	case e.Reason != "":
		return fmt.Sprintf("tool %q requires approval: %s", e.Tool, e.Reason)
	default:
		return fmt.Sprintf("tool %q requires approval", e.Tool)
	}
}

func (e ErrPermissionPending) PendingPermissionRequestID() string {
	return strings.TrimSpace(e.RequestID)
}

// StopConversationAfterTool tells the shared runner to halt the current turn
// immediately after this tool result is appended.
func (e ErrPermissionPending) StopConversationAfterTool() bool {
	return true
}
