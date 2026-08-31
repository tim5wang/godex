package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
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
		{"worker", []string{bundleCoreCode, bundleLSP, bundleWriting}},
		{"reviewer", []string{bundleCoreCode, bundleLSP, bundleWriting}},
		{"planner", []string{bundleCoreCode, bundleLSP, bundlePlanning, bundleWriting}},
		{"orchestrator", []string{bundleCoreCode, bundleLSP, bundlePlanning, bundleSubagent, bundleWeb, bundleWriting}},
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

// ---- Phase 4.5: 写 scope 与 bundle 联动 ----

func TestResolveSubagentWriteScopePriority(t *testing.T) {
	// 无 writing/core_code bundle 且非 general-purpose 默认工具面 → 显式 scope 被忽略（天然只读）。
	if got := resolveSubagentWriteScope("researcher", []string{"src/"}, pkgregistry.Role{}, false, []string{bundleWeb}); got != nil {
		t.Fatalf("scope without write bundle should be ignored, got %v", got)
	}
	// general-purpose 默认工具面含写工具 → 显式生效（即使 bundle 集合为空）。
	got := resolveSubagentWriteScope("general-purpose", []string{"src/"}, pkgregistry.Role{}, false, nil)
	if !stringSlicesEqual(got, []string{"src"}) {
		t.Fatalf("explicit scope with general-purpose default surface = %v, want [src]", got)
	}
	// core_code 隐式 writing → 显式生效（normalizeWriteScope 去首尾斜杠）。
	got = resolveSubagentWriteScope("worker", []string{"src/"}, pkgregistry.Role{}, false, []string{bundleCoreCode})
	if !stringSlicesEqual(got, []string{"src"}) {
		t.Fatalf("explicit scope with core_code = %v, want [src]", got)
	}
	// writing bundle → 显式生效。
	got = resolveSubagentWriteScope("worker", []string{"src/"}, pkgregistry.Role{}, false, []string{bundleWriting})
	if !stringSlicesEqual(got, []string{"src"}) {
		t.Fatalf("explicit scope with writing = %v, want [src]", got)
	}
	// 无显式 scope → 回退 role.WriteScope（package role 声明）。
	role := pkgregistry.Role{WriteScope: []string{"docs/"}}
	got = resolveSubagentWriteScope("worker", nil, role, true, []string{bundleWriting})
	if !stringSlicesEqual(got, []string{"docs"}) {
		t.Fatalf("role.WriteScope fallback = %v, want [docs]", got)
	}
	// 显式优先于 role.WriteScope。
	got = resolveSubagentWriteScope("worker", []string{"src/"}, role, true, []string{bundleWriting})
	if !stringSlicesEqual(got, []string{"src"}) {
		t.Fatalf("explicit should beat role scope, got %v", got)
	}
	// 有 writing 但无任何 scope → nil（只读降级）。
	if got := resolveSubagentWriteScope("worker", nil, pkgregistry.Role{}, false, []string{bundleWriting}); got != nil {
		t.Fatalf("writing without scope should degrade to read-only, got %v", got)
	}
}

func TestSubagentWriteCapable(t *testing.T) {
	cases := []struct {
		bundles []string
		want    bool
	}{
		{[]string{bundleWriting}, true},
		{[]string{bundleCoreCode}, true},
		{[]string{bundleWeb, bundleWriting}, true},
		{[]string{bundleWeb}, false},
		{[]string{bundleLSP}, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := subagentWriteCapable(tc.bundles); got != tc.want {
			t.Fatalf("subagentWriteCapable(%v) = %v, want %v", tc.bundles, got, tc.want)
		}
	}
}

func TestSubagentToolsForRequiredBundleWritingBranch(t *testing.T) {
	// writing + 有效 scope → 展开写工具。
	got := subagentToolsForRequiredBundle(bundleWriting, []string{"src/"})
	if !stringSlicesEqual(got, []string{"bash", "write_file", "edit_file"}) {
		t.Fatalf("writing with scope = %v, want [bash write_file edit_file]", got)
	}
	// writing + 无 scope → 只读降级（不展开写工具）。
	if got := subagentToolsForRequiredBundle(bundleWriting, nil); got != nil {
		t.Fatalf("writing without scope should expand nothing, got %v", got)
	}
	// core_code 保持原展开（隐式 writing，含 read_file）。
	got = subagentToolsForRequiredBundle(bundleCoreCode, nil)
	if !stringSlicesEqual(got, []string{"bash", "read_file", "write_file", "edit_file"}) {
		t.Fatalf("core_code expansion = %v", got)
	}
}

func TestSubagentStartWorkerScopeExpandsWriteTools(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = repeatedTextCaller("done")
	job, err := a.startDurableSubagentWithContext(subagentTargetCtx(), durableSubagentStartRequest{
		Prompt:     "implement the module",
		AgentType:  "worker", // 内置 [core_code lsp writing]
		WriteScope: []string{"src/"},
	})
	if err != nil {
		t.Fatalf("start worker subagent: %v", err)
	}
	if !stringSlicesEqual(job.WriteScope, []string{"src"}) {
		t.Fatalf("job.WriteScope = %v, want [src]", job.WriteScope)
	}
	if !containsString(job.DefaultBundles, bundleWriting) {
		t.Fatalf("worker job should include writing bundle, got %v", job.DefaultBundles)
	}
	if !containsString(job.ToolNames, "bash") || !containsString(job.ToolNames, "write_file") || !containsString(job.ToolNames, "edit_file") {
		t.Fatalf("worker with scope should expose write tools, got %v", job.ToolNames)
	}
	waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
}

func TestSubagentStartWorkerWithoutScopeReadOnly(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = repeatedTextCaller("done")
	job, err := a.startDurableSubagentWithContext(subagentTargetCtx(), durableSubagentStartRequest{
		Prompt:    "implement the module",
		AgentType: "worker", // 内置 [core_code lsp writing] 但无 scope → 只读降级
	})
	if err != nil {
		t.Fatalf("start worker subagent: %v", err)
	}
	if len(job.WriteScope) != 0 {
		t.Fatalf("worker without scope should keep empty write scope, got %v", job.WriteScope)
	}
	if containsString(job.ToolNames, "bash") || containsString(job.ToolNames, "write_file") || containsString(job.ToolNames, "edit_file") {
		t.Fatalf("worker without scope should be read-only, got %v", job.ToolNames)
	}
	waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
}

func TestSubagentStartScopeIgnoredWithoutWriteBundle(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = repeatedTextCaller("done")
	// researcher 内置 [web]，且 bundle_overrides=[web] 覆盖继承 → 无 writing/core_code。
	job, err := a.startDurableSubagentWithContext(subagentTargetCtx(), durableSubagentStartRequest{
		Prompt:          "summarize the release notes",
		AgentType:       "researcher",
		WriteScope:      []string{"src/"},
		BundleOverrides: []string{bundleWeb},
	})
	if err != nil {
		t.Fatalf("start researcher subagent: %v", err)
	}
	if len(job.WriteScope) != 0 {
		t.Fatalf("scope without write bundle should be ignored, got %v", job.WriteScope)
	}
	if containsString(job.ToolNames, "bash") || containsString(job.ToolNames, "write_file") {
		t.Fatalf("no write bundle should mean no write tools, got %v", job.ToolNames)
	}
	waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
}

func TestReopenForIterationWithUpdateAppliesConfig(t *testing.T) {
	store := newSubagentJobStore(filepath.Join(t.TempDir(), "subagents"))
	job, err := store.StartWithOptions(subagentStartOptions{
		AgentType:     "general-purpose",
		Prompt:        "work",
		ToolNames:     []string{"bash", "read_file", "write_file", "edit_file"},
		WriteScope:    []string{"src/"},
		MaxTurns:      1,
		WorkerID:      localGoDexWorkerID,
		SandboxID:     "sandbox:local:test",
		BasePrompt:    "base",
		ParentID:      "turn-1",
		ContextBudget: 100000,
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	if _, err := store.Finish(job.ID, subagentStatusCompleted, "first result", ""); err != nil {
		t.Fatalf("finish job: %v", err)
	}
	reopened, err := store.ReopenForIterationWithUpdate(job.ID, []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "now focus on docs"),
	}, subagentReopenUpdate{
		AgentType:      "researcher",
		WriteScope:     []string{"docs/"},
		DefaultBundles: []string{bundleWeb},
		ToolNames:      []string{"read_file", "web_search", "web_fetch"},
	})
	if err != nil {
		t.Fatalf("reopen with update: %v", err)
	}
	if reopened.AgentType != "researcher" || !stringSlicesEqual(reopened.WriteScope, []string{"docs/"}) {
		t.Fatalf("reopened config not updated, got agent_type=%q write_scope=%v", reopened.AgentType, reopened.WriteScope)
	}
	if !stringSlicesEqual(reopened.DefaultBundles, []string{bundleWeb}) {
		t.Fatalf("reopened DefaultBundles = %v, want [web]", reopened.DefaultBundles)
	}
	if !containsString(reopened.ToolNames, "web_search") || containsString(reopened.ToolNames, "bash") {
		t.Fatalf("reopened ToolNames = %v, want web tools without bash", reopened.ToolNames)
	}
	if len(reopened.PendingInputs) != 1 || !strings.Contains(protocol.MessageText(reopened.PendingInputs[0]), "focus on docs") {
		t.Fatalf("expected feedback queued, got %+v", reopened.PendingInputs)
	}
}

func TestIterateDurableSubagentWithUpdateReconfiguresJob(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = repeatedTextCaller("done")
	job, err := a.startDurableSubagentWithContext(subagentTargetCtx(), durableSubagentStartRequest{
		Prompt:     "write the module",
		AgentType:  "general-purpose",
		WriteScope: []string{"src/"},
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	// iterate 时切换角色为 researcher 并覆盖 bundle → 写能力消失。
	updated, err := a.IterateDurableSubagentWithUpdate(subagentTargetCtx(), job.ID, "switch to research mode", subagentReopenUpdate{
		AgentType:       "researcher",
		BundleOverrides: []string{bundleWeb},
	})
	if err != nil {
		t.Fatalf("iterate with update: %v", err)
	}
	if updated.AgentType != "researcher" {
		t.Fatalf("expected agent_type researcher after iterate, got %q", updated.AgentType)
	}
	if containsString(updated.DefaultBundles, bundleCoreCode) || containsString(updated.DefaultBundles, bundleWriting) {
		t.Fatalf("researcher override should drop write bundles, got %v", updated.DefaultBundles)
	}
	if containsString(updated.ToolNames, "bash") || containsString(updated.ToolNames, "write_file") {
		t.Fatalf("researcher override should remove write tools, got %v", updated.ToolNames)
	}
	waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
}
