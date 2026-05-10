package repl

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/tools"
)

func TestRenderPendingApprovalIncludesActionableDetails(t *testing.T) {
	text := renderPendingApproval("perm-1", "session-1", []tools.PendingPermission{
		{
			ID:     "perm-1",
			Reason: "Shell command needs approval.",
			Request: tools.PermissionRequest{
				ToolName: "bash",
				Action:   "execute",
				Command:  "git status --short",
				Paths:    []string{"."},
			},
		},
	})

	for _, want := range []string{
		"Pending approval required.",
		"Request: perm-1",
		"Session: session-1",
		"Tool: bash",
		"Action: execute",
		"Command: git status --short",
		"Paths: .",
		"Reason: Shell command needs approval.",
		"Approve once: /approve",
		"Approve for session: /approve session",
		"Deny: /deny perm-1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected approval text to contain %q, got %q", want, text)
		}
	}
}
