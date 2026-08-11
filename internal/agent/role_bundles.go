package agent

import (
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"strings"
	"sync"
)

// Phase 4.3 — 角色→bundle 运行时映射.
//
// RoleBundleRegistry 集中管理「角色 → 默认 bundle 集合」的映射（roadmap 4.3）。
// 内置角色（orchestrator/worker/reviewer/researcher/planner）按路线图角色分工表
// 提供默认 bundle；package role 安装时可通过 RegisterRole 覆盖或补充。
// subagent 创建时用 BundlesForRole 解析角色应激活的 bundle，再叠加显式
// required_bundles 与 4.4 的父 agent bundle 继承，最终映射为子 agent 可用工具。

// roleBundleRegistry 是角色 → 默认 bundle 的运行时注册表。
type roleBundleRegistry struct {
	mu    sync.RWMutex
	roles map[string][]string // roleID → 默认 bundles
}

// builtinRoleBundles 对齐路线图角色分工表（docs/godex-optimization-roadmap.md）。
// 仅收录子 agent 运行时能落地的 bundle；diff/grep 等工具粒度能力由
// subagentToolNamesForRole / capabilitySummaryForTools 表达。
var builtinRoleBundles = map[string][]string{
	"orchestrator": {bundleCoreCode, bundleLSP, bundlePlanning, bundleSubagent, bundleWeb},
	"worker":       {bundleCoreCode, bundleLSP},
	"reviewer":     {bundleCoreCode, bundleLSP},
	"researcher":   {bundleWeb},
	"planner":      {bundleCoreCode, bundleLSP, bundlePlanning},
}

func newRoleBundleRegistry() *roleBundleRegistry {
	r := &roleBundleRegistry{roles: map[string][]string{}}
	for id, bundles := range builtinRoleBundles {
		r.roles[id] = append([]string{}, bundles...)
	}
	return r
}

// RegisterRole 注册或覆盖一个角色的默认 bundle 集合。空集合删除该角色条目。
func (r *roleBundleRegistry) RegisterRole(roleID string, bundles []string) {
	if r == nil || strings.TrimSpace(roleID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(bundles) == 0 {
		delete(r.roles, strings.ToLower(strings.TrimSpace(roleID)))
		return
	}
	r.roles[strings.ToLower(strings.TrimSpace(roleID))] = uniqueStrings(bundles)
}

// BundlesForRole 解析角色的默认 bundle：显式 roleID 命中优先（package role
// 或内置角色），否则按 agentType 关键词推断内置角色。
func (r *roleBundleRegistry) BundlesForRole(roleID, agentType string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if id := strings.ToLower(strings.TrimSpace(roleID)); id != "" {
		if bundles, ok := r.roles[id]; ok {
			return append([]string{}, bundles...)
		}
	}
	return builtinRoleBundlesForType(agentType)
}

// builtinRoleBundlesForType 按 agentType 关键词推断内置角色 bundle。
func builtinRoleBundlesForType(agentType string) []string {
	t := strings.ToLower(strings.TrimSpace(agentType))
	if t == "" {
		return nil
	}
	for id, bundles := range builtinRoleBundles {
		if strings.Contains(t, id) {
			return append([]string{}, bundles...)
		}
	}
	return nil
}

// roleBundlesFor 解析 subagent 创建时的角色默认 bundles：package role 的
// DefaultBundles 优先，否则回退到 RoleBundleRegistry（内置角色映射）。
func (a *Agent) roleBundlesFor(agentType string, role pkgregistry.Role, hasRole bool) []string {
	if hasRole && len(role.DefaultBundles) > 0 {
		return append([]string{}, role.DefaultBundles...)
	}
	if a != nil && a.roleBundles != nil {
		return a.roleBundles.BundlesForRole(role.ID, agentType)
	}
	return builtinRoleBundlesForType(agentType)
}

// inheritedSubagentBundles 计算 4.4 的 bundle 继承结果：默认继承父 agent
// 当前活跃的 bundle 集合；bundle_overrides 提供时覆盖继承集合；
// deactivate_bundles 从结果中移除指定 bundle。
func (a *Agent) inheritedSubagentBundles(overrides, deactivate []string) []string {
	var inherited []string
	if len(overrides) > 0 {
		inherited = append(inherited, overrides...)
	} else if a != nil && a.toolHandler != nil {
		catalog := a.toolHandler.Catalog()
		inherited = append(inherited, catalog.ActiveBundles...)
	}
	if len(deactivate) > 0 {
		blocked := map[string]struct{}{}
		for _, b := range deactivate {
			blocked[strings.ToLower(strings.TrimSpace(b))] = struct{}{}
		}
		filtered := inherited[:0]
		for _, b := range inherited {
			if _, ok := blocked[strings.ToLower(strings.TrimSpace(b))]; !ok {
				filtered = append(filtered, b)
			}
		}
		inherited = filtered
	}
	return uniqueStrings(inherited)
}
