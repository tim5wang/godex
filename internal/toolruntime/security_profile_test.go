package toolruntime

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/domain/message"
)

func TestSecurityProfilePreventsYOLOHostPrivilege(t *testing.T) {
	policy := PermissionPolicyForSecurityProfile(SecurityProfileHostPrivileged, InteractiveApprovalModeYOLO)
	manager := NewPermissionManagerForPolicy(policy)
	result := manager.Evaluate(PermissionRequest{
		SessionID: "session",
		Source:    string(message.SourceWeb),
		ToolName:  "bash",
		Action:    "execute",
		Command:   "ssh prod.example.com uptime",
		Mutation:  true,
	})
	if result.Decision == PermissionAllow {
		t.Fatalf("host-privileged profile must not silently auto-approve yolo shell commands: %+v", result)
	}
	if result.Decision != PermissionPending {
		t.Fatalf("expected host-privileged yolo shell command to require approval, got %+v", result)
	}
}

func TestSecurityProfileStrictDeniesHostPrivilegedTools(t *testing.T) {
	policy := PermissionPolicyForSecurityProfile(SecurityProfileStrict, InteractiveApprovalModeManual)
	manager := NewPermissionManagerForPolicy(policy)
	result := manager.Evaluate(PermissionRequest{
		SessionID: "session",
		Source:    string(message.SourceWeixin),
		ToolName:  "bash",
		Action:    "execute",
		Command:   "go test ./...",
		Mutation:  true,
	})
	if result.Decision != PermissionDeny {
		t.Fatalf("expected strict remote shell command to be denied, got %+v", result)
	}
}

func TestSecurityProfileDevRepairAllowsDiagnosticShellCommands(t *testing.T) {
	policy := PermissionPolicyForSecurityProfile("dev-repair", InteractiveApprovalModeManual)
	manager := NewPermissionManagerForPolicy(policy)
	result := manager.Evaluate(PermissionRequest{
		SessionID: "session",
		Source:    string(message.SourceCLI),
		ToolName:  "bash",
		Action:    "execute",
		Command:   "command -v godex",
		Mutation:  false,
	})
	if result.Decision != PermissionAbstain {
		t.Fatalf("expected dev/repair diagnostic command to avoid shell allowlist approval, got %+v", result)
	}
	if policy.SecurityProfile != SecurityProfileDevRepair {
		t.Fatalf("expected dev-repair alias to normalize to %q, got %q", SecurityProfileDevRepair, policy.SecurityProfile)
	}
}

func TestSecurityProfileDevRepairRequiresApprovalForInlineExecution(t *testing.T) {
	policy := PermissionPolicyForSecurityProfile(SecurityProfileDevRepair, InteractiveApprovalModeManual)
	manager := NewPermissionManagerForPolicy(policy)
	result := manager.Evaluate(PermissionRequest{
		SessionID: "session",
		Source:    string(message.SourceWeb),
		ToolName:  "bash",
		Action:    "execute",
		Command:   `node -e 'require("child_process").execSync("id")'`,
		Mutation:  true,
	})
	if result.Decision != PermissionPending {
		t.Fatalf("expected inline node execution to require approval, got %+v", result)
	}
	if !strings.Contains(result.Reason, "dev/repair") {
		t.Fatalf("expected repair-specific approval reason, got %q", result.Reason)
	}
}
