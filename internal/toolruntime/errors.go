package toolruntime

import "fmt"

// ErrToolNotFound is returned when a tool is not found
type ErrToolNotFound struct {
	Name string
}

func (e ErrToolNotFound) Error() string {
	return fmt.Sprintf("tool not found: %s", e.Name)
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
