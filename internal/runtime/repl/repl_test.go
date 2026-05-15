package repl

import (
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/tools"
)

func TestRenderPendingApprovalIncludesActionableDetails(t *testing.T) {
	text := renderPendingApproval("perm-1", "session-1", []tools.PendingPermission{
		{
			ID:        "perm-1",
			Status:    tools.PermissionStatusPending,
			Reason:    "Shell command needs approval.",
			ExpiresAt: time.Now().Add(10 * time.Minute),
			Request: tools.PermissionRequest{
				ToolName: "bash",
				Action:   "execute",
				Command:  "rm -rf build",
				Paths:    []string{"."},
			},
		},
	})

	for _, want := range []string{
		"Pending approval required.",
		"Request: perm-1",
		"Session: session-1",
		"Status: pending",
		"Tool: bash",
		"Action: execute",
		"Intent: Agent wants to run shell command",
		"Risk: high risk",
		"Expiry: expires in",
		"Command: rm -rf build",
		"Paths: .",
		"Reason: Shell command needs approval.",
		"Inspect approvals: /approve status",
		"Approve once: /approve",
		"Approve for session: /approve session",
		"Deny: /deny perm-1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected approval text to contain %q, got %q", want, text)
		}
	}
}
