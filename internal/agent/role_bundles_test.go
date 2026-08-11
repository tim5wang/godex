package agent

import (
	"context"
	"testing"

	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
)

// Phase 4.3/4.4 tests: role→bundle runtime mapping and child-agent bundle
// inheritance. These cover the pure registry helpers plus the durable
// subagent start wiring that merges role defaults, explicit required bundles,
// parent-active bundle inheritance, bundle_overrides and deactivate_bundles.

func TestRoleBundleRegistryBuiltinRoles(t *testing.T) {
	r := newRoleBundleRegistry()
	cases := []struct {
		agentType string
		want      []string
	}{
		{"researcher", []string{bundleWeb}},
		{"worker", []string{bundleCoreCode, bundleLSP}},
		{"reviewer", []string{bundleCoreCode, bundleLSP}},
		{"planner", []string{bundleCoreCode, bundleLSP, bundlePlanning}},
		{"orchestrator", []string{bundleCoreCode, bundleLSP, bundlePlanning, bundleSubagent, bundleWeb}},
	}
	for _, tc := range cases {
		got := r.BundlesForRole("", tc.agentType)
		if !stringSlicesEqual(got, tc.want) {
			t.Fatalf("BundlesForRole(\"\", %q) = %v, want %v", tc.agentType, got, tc.want)
		}
	}
}

func TestRoleBundleRegistryRegisterOverridesBuiltin(t *testing.T) {
	r := newRoleBundleRegistry()
	r.RegisterRole("researcher", []string{bundleCoreCode})
	got := r.BundlesForRole("researcher", "researcher")
	if !stringSlicesEqual(got, []string{bundleCoreCode}) {
		t.Fatalf("BundlesForRole after RegisterRole = %v, want [core_code]", got)
	}
	// 显式 roleID 优先于 agentType 关键词推断。
	r.RegisterRole("custom-review", []string{bundleWeb})
	got = r.BundlesForRole("custom-review", "reviewer")
	if !stringSlicesEqual(got, []string{bundleWeb}) {
		t.Fatalf("BundlesForRole custom role = %v, want [web]", got)
	}
}

func TestRoleBundleRegistryUnknownRoleEmpty(t *testing.T) {
	r := newRoleBundleRegistry()
	if got := r.BundlesForRole("no-such-role", ""); got != nil {
		t.Fatalf("BundlesForRole unknown = %v, want nil", got)
	}
}

func TestRoleBundlesForPrefersRoleDefaultBundles(t *testing.T) {
	a := newTestAgent(t, 4096)
	role := pkgregistry.Role{ID: "worker", Name: "worker", DefaultBundles: []string{bundleWeb, bundleCoreCode}}
	got := a.roleBundlesFor("worker", role, true)
	if !stringSlicesEqual(got, []string{bundleWeb, bundleCoreCode}) {
		t.Fatalf("roleBundlesFor with role.DefaultBundles = %v, want [web core_code]", got)
	}
	// 无 role（hasRole=false）时回退到内置映射。
	got = a.roleBundlesFor("researcher", pkgregistry.Role{}, false)
	if !stringSlicesEqual(got, []string{bundleWeb}) {
		t.Fatalf("roleBundlesFor fallback = %v, want [web]", got)
	}
}

func TestInheritedSubagentBundlesDefaultsToParentActive(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleWeb)
	got := a.inheritedSubagentBundles(nil, nil)
	if !containsString(got, bundleWeb) {
		t.Fatalf("inherited bundles should include parent-active web, got %v", got)
	}
}

func TestInheritedSubagentBundlesOverridesReplace(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleWeb)
	got := a.inheritedSubagentBundles([]string{bundleCoreCode}, nil)
	if !stringSlicesEqual(got, []string{bundleCoreCode}) {
		t.Fatalf("bundle_overrides should replace inheritance, got %v", got)
	}
}

func TestInheritedSubagentBundlesDeactivateRemoves(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleWeb)
	a.toolHandler.ActivateBundles(bundleCoreCode)
	got := a.inheritedSubagentBundles(nil, []string{bundleWeb})
	if containsString(got, bundleWeb) {
		t.Fatalf("deactivate_bundles should remove web, got %v", got)
	}
	if !containsString(got, bundleCoreCode) {
		t.Fatalf("deactivate_bundles should keep core_code, got %v", got)
	}
}

func subagentTargetCtx() context.Context {
	return withSubagentEventTarget(context.Background(), subagentEventTarget{
		sessionID: "session-bundles",
		turnID:    "turn-bundles",
	})
}

func TestSubagentStartResolvesRoleBundles(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = repeatedTextCaller("done")
	// researcher 角色 → 内置 [web] → web_search/web_fetch 工具。
	job, err := a.startDurableSubagentWithContext(subagentTargetCtx(), durableSubagentStartRequest{
		Prompt:    "summarize the release notes",
		AgentType: "researcher",
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	if !containsString(job.DefaultBundles, bundleWeb) {
		t.Fatalf("job.DefaultBundles should include web, got %v", job.DefaultBundles)
	}
	if !containsString(job.ToolNames, "web_search") || !containsString(job.ToolNames, "web_fetch") {
		t.Fatalf("researcher job should expose web tools, got %v", job.ToolNames)
	}
	waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
}

func TestSubagentStartBundleOverridesAndDeactivate(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = repeatedTextCaller("done")
	a.toolHandler.ActivateBundles(bundleWeb)
	// bundle_overrides 覆盖继承的 web → 只保留 core_code。
	job, err := a.startDurableSubagentWithContext(subagentTargetCtx(), durableSubagentStartRequest{
		Prompt:          "write the module",
		AgentType:       "general-purpose",
		WriteScope:      []string{"src/"},
		BundleOverrides: []string{bundleCoreCode},
	})
	if err != nil {
		t.Fatalf("start subagent with overrides: %v", err)
	}
	if containsString(job.DefaultBundles, bundleWeb) {
		t.Fatalf("bundle_overrides should drop inherited web, got %v", job.DefaultBundles)
	}
	if !stringSlicesEqual(job.BundleOverrides, []string{bundleCoreCode}) {
		t.Fatalf("job.BundleOverrides = %v, want [core_code]", job.BundleOverrides)
	}
	waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	// deactivate_bundles 移除继承的 web bundle → web 工具不再暴露，
	// 但 general-purpose 默认工具面（bash 等）保留。
	job2, err := a.startDurableSubagentWithContext(subagentTargetCtx(), durableSubagentStartRequest{
		Prompt:            "write the module",
		AgentType:         "general-purpose",
		WriteScope:        []string{"src/"},
		DeactivateBundles: []string{bundleWeb},
	})
	if err != nil {
		t.Fatalf("start subagent with deactivate: %v", err)
	}
	if containsString(job2.DefaultBundles, bundleWeb) {
		t.Fatalf("deactivate_bundles should remove web, got %v", job2.DefaultBundles)
	}
	if !stringSlicesEqual(job2.DeactivateBundles, []string{bundleWeb}) {
		t.Fatalf("job.DeactivateBundles = %v, want [web]", job2.DeactivateBundles)
	}
	if containsString(job2.ToolNames, "web_search") || containsString(job2.ToolNames, "web_fetch") {
		t.Fatalf("deactivated web should remove web tools, got %v", job2.ToolNames)
	}
	if !containsString(job2.ToolNames, "bash") {
		t.Fatalf("general-purpose default tools should remain, got %v", job2.ToolNames)
	}
	waitForSubagentStatus(t, a.subagentJobs, job2.ID, subagentStatusCompleted)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
