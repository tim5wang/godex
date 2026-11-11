package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/tools"
)

func TestEnvironmentUsesExecutionBindingAndHostDeliveryDefaults(t *testing.T) {
	a := newTestAgent(t, 100000)
	actual := a.SandboxBinding()
	// A session sandbox can differ from the base configuration.
	a.cfg.WorkspaceDir = filepath.Join(t.TempDir(), "not-the-executor")
	a.cfg.Cron.DefaultTimezone = "UTC"
	a.cfg.Heartbeat.DefaultTimezone = "Asia/Tokyo"
	ctx := tools.WithSessionContext(context.Background(), automation.SessionContext{Source: "web", ApprovalMode: "ask", SecurityProfile: "strict", DefaultDelivery: automation.DeliveryTarget{Kind: "session", SessionID: "private-target-id"}})
	build, err := a.buildContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prefix := ""
	for _, msg := range build.Messages {
		prefix += protocol.MessageText(msg)
	}
	for _, want := range []string{actual.WorkspaceDir, "Agent scratch directory: " + tooling.AgentScratchDir, "Host output directory: " + actual.TempDir, "Approval mode: ask", "Security profile: strict", "Interactive UI cards supported: true", "omit delivery_target to inherit", "cron=UTC; heartbeat=Asia/Tokyo"} {
		if !strings.Contains(prefix, want) {
			t.Fatalf("missing %q: %s", want, prefix)
		}
	}
	if strings.Contains(prefix, "private-target-id") || strings.Contains(prefix, "not-the-executor") {
		t.Fatalf("invented execution path or exposed target: %s", prefix)
	}
	if !strings.Contains(build.RuntimeTail, "Current local time (RFC3339)") || strings.Contains(prefix, "Current local time (RFC3339)") {
		t.Fatal("clock must live only in volatile tail")
	}
}

func TestScratchAndHostOutputHaveDifferentWriteBoundaries(t *testing.T) {
	workspace, hostOutput := t.TempDir(), t.TempDir()
	executor := tooling.NewWorkspaceExecutorWithTempDir(workspace, hostOutput)
	if _, err := executor.WriteFile(filepath.Join(tooling.AgentScratchDir, "probe.txt"), "scratch"); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.WriteFile(filepath.Join(hostOutput, "probe.txt"), "outside"); err == nil {
		t.Fatal("host output directory became an extra agent write root")
	}
}

func TestRuntimeTimezoneResolvesLocalWithoutGuessingFromOffset(t *testing.T) {
	t.Setenv("TZ", "Asia/Shanghai")
	local := time.Date(2026, 11, 11, 18, 0, 0, 0, time.FixedZone("Local", 8*3600))
	if got := runtimeTimezone(local); got != "Asia/Shanghai" {
		t.Fatalf("zone=%q", got)
	}
	unknown := time.Date(2026, 11, 11, 18, 0, 0, 0, time.FixedZone("custom-zone", 8*3600))
	if got := runtimeTimezone(unknown); got == "Asia/Shanghai" || !strings.Contains(got, "offset only") {
		t.Fatalf("guessed IANA zone: %q", got)
	}
	if got := zoneNameFromPath("/var/db/timezone/zoneinfo/Asia/Shanghai"); got != "Asia/Shanghai" {
		t.Fatalf("macOS zone path: %q", got)
	}
}
