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
	message := fmt.Sprintf("tool not found: %s. Active tools: %s.", e.Name, strings.Join(e.Available, ", "))
	if e.Name == "grep" {
		message += ` grep is not a separate tool; use bash with a command such as "rg <pattern> <path>".`
	}
	return message
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

// StopConversationAfterTool tells the shared runner to halt the current turn
// immediately after this tool result is appended.
func (e ErrPermissionPending) StopConversationAfterTool() bool {
	return true
}
