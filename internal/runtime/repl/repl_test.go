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

	for _, snippet := range []string{
		"pending",
		"perm-1",
		"Status: pending",
		"Tool: bash",
		"Action: execute",
		"Command: rm -rf build",
		"Paths: .",
		"Shell command needs approval.",
		"Expiry: ",
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("Expected %q in output, got:\n%s", snippet, text)
		}
	}
}
