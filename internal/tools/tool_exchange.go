package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/platform/stringutil"
)

// ToolCatalogManager manages bundle activation state for tool exchange.
type ToolCatalogManager interface {
	Catalog() ToolCatalog
	ActivateBundles(names ...string) []string
	DeactivateBundles(names ...string) (changed []string, blocked []string)
}

type toolExchangeArgs struct {
	EnableBundles  []string `json:"enable_bundles,omitempty"`
	DisableBundles []string `json:"disable_bundles,omitempty"`
	Query          string   `json:"query,omitempty"`
	IncludeTools   bool     `json:"include_tools,omitempty"`
	MaxResults     int      `json:"max_results,omitempty"`
}

type bundleRecommendation struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary,omitempty"`
	Active  bool     `json:"active"`
	Reason  string   `json:"reason,omitempty"`
	Tools   []string `json:"tools,omitempty"`
}

func knownBundleNames(catalog ToolCatalog) map[string]struct{} {
	known := make(map[string]struct{}, len(catalog.Bundles))
	for _, bundle := range catalog.Bundles {
		if bundle.Name == "" {
			continue
		}
		known[bundle.Name] = struct{}{}
	}
	return known
}

// dynamicToolCatalog removes template-pinned bundles from tool_exchange's
// mutable view. The canonical ToolHandler.Catalog still exposes always_on so
// template editors can select it.
func dynamicToolCatalog(catalog ToolCatalog) ToolCatalog {
	activeBundles := make([]string, 0, len(catalog.ActiveBundles))
	for _, name := range catalog.ActiveBundles {
		if name != BundleAlwaysOn {
			activeBundles = append(activeBundles, name)
		}
	}
	bundles := make([]BundleCatalogItem, 0, len(catalog.Bundles))
	for _, bundle := range catalog.Bundles {
		if bundle.Name != BundleAlwaysOn {
			bundles = append(bundles, bundle)
		}
	}
	catalog.ActiveBundles = activeBundles
	catalog.Bundles = bundles
	return catalog
}

func requestsTemplatePinnedBundle(names ...[]string) bool {
	for _, group := range names {
		for _, name := range group {
			if strings.TrimSpace(name) == BundleAlwaysOn {
				return true
			}
		}
	}
	return false
}

func unknownRequestedBundles(known map[string]struct{}, names []string) []string {
	var unknown []string
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	return unknown
}

func alreadyActiveRequestedBundles(active, requested []string) []string {
	activeSet := make(map[string]string, len(active))
	for _, name := range active {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		activeSet[strings.ToLower(trimmed)] = trimmed
	}
	seen := make(map[string]struct{}, len(requested))
	result := make([]string, 0, len(requested))
	for _, name := range requested {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if canonical, ok := activeSet[key]; ok {
			result = append(result, canonical)
		}
	}
	return result
}

func formatAvailableBundles(catalog ToolCatalog) string {
	names := make([]string, 0, len(catalog.Bundles))
	for _, bundle := range catalog.Bundles {
		if bundle.Name != "" {
			names = append(names, bundle.Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func boundedMaxResults(value int) int {
	if value <= 0 {
		return 5
	}
	if value > 20 {
		return 20
	}
	return value
}

func summarizeBundles(catalog ToolCatalog, includeTools bool, maxResults int) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, min(len(catalog.Bundles), maxResults))
	for i, bundle := range catalog.Bundles {
		if i >= maxResults {
			break
		}
		item := map[string]interface{}{
			"name":    bundle.Name,
			"summary": bundle.Summary,
			"active":  bundle.Active,
		}
		if includeTools {
			item["tools"] = bundle.Tools
		}
		items = append(items, item)
	}
	return items
}

func recommendedBundles(catalog ToolCatalog, query string, includeTools bool, maxResults int) []bundleRecommendation {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	type scored struct {
		item  BundleCatalogItem
		score int
		hits  []string
	}
	scoredItems := make([]scored, 0, len(catalog.Bundles))
	for _, bundle := range catalog.Bundles {
		score, hits := scoreBundle(bundle, query)
		if score <= 0 {
			continue
		}
		scoredItems = append(scoredItems, scored{item: bundle, score: score, hits: hits})
	}
	sort.SliceStable(scoredItems, func(i, j int) bool {
		if scoredItems[i].score != scoredItems[j].score {
			return scoredItems[i].score > scoredItems[j].score
		}
		return scoredItems[i].item.Name < scoredItems[j].item.Name
	})
	recs := make([]bundleRecommendation, 0, min(len(scoredItems), maxResults))
	for i, scored := range scoredItems {
		if i >= maxResults {
			break
		}
		rec := bundleRecommendation{
			Name:    scored.item.Name,
			Summary: scored.item.Summary,
			Active:  scored.item.Active,
			Reason:  "matched " + strings.Join(scored.hits, ", "),
		}
		if includeTools {
			rec.Tools = append([]string{}, scored.item.Tools...)
		}
		recs = append(recs, rec)
	}
	return recs
}

func scoreBundle(bundle BundleCatalogItem, query string) (int, []string) {
	fields := []struct {
		label  string
		text   string
		weight int
	}{
		{label: "bundle", text: bundle.Name, weight: 5},
		{label: "summary", text: bundle.Summary, weight: 3},
		{label: "tool", text: strings.Join(bundle.Tools, " "), weight: 4},
	}
	aliases := bundleAliases(bundle.Name)
	score := 0
	hits := make([]string, 0)
	for _, field := range fields {
		text := strings.ToLower(field.text)
		if text == "" {
			continue
		}
		for _, token := range queryTokens(query) {
			if strings.Contains(text, token) {
				score += field.weight
				hits = stringutil.AppendUnique(hits, field.label+":"+token)
			}
		}
	}
	for _, alias := range aliases {
		if strings.Contains(query, alias) {
			score += 6
			hits = stringutil.AppendUnique(hits, "alias:"+alias)
		}
	}
	return score, hits
}

func queryTokens(query string) []string {
	parts := strings.FieldsFunc(query, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ',' || r == ';' || r == '，' || r == '。' || r == '/' || r == '\\'
	})
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.ToLower(part))
		if len([]rune(part)) < 2 {
			continue
		}
		tokens = stringutil.AppendUnique(tokens, part)
	}
	return tokens
}

func bundleAliases(name string) []string {
	switch name {
	case "web":
		return []string{
			"搜索", "联网", "网上", "网页", "当前信息", "实时信息",
			"天气", "天气预报", "气温", "降雨", "空气质量", "新闻", "股价", "股票", "汇率", "价格", "赛程", "航班",
			"search", "internet", "current info", "fetch", "weather", "forecast", "temperature", "rain", "air quality", "news", "stock", "price", "exchange rate", "schedule", "score", "flight",
		}
	case "browser":
		return []string{"浏览器", "登录", "验证码", "点击网页", "browser", "login", "captcha", "dynamic page"}
	case "desktop":
		return []string{"桌面", "弹窗", "剪贴板", "鼠标", "键盘", "desktop", "clipboard", "mouse", "keyboard", "window"}
	case "background":
		return []string{"后台", "长时间", "长期运行", "long-running", "background", "daemon"}
	case "subagent":
		return []string{"并行", "子任务", "worker", "subagent", "parallel", "delegate"}
	case "external_agents":
		return []string{"acp", "外部agent", "其它agent", "codex", "claude", "external agent", "agent client protocol"}
	case "task_board":
		return []string{"任务板", "计划", "todo", "task", "kanban"}
	case "team":
		return []string{"团队", "收件箱", "teammate", "inbox", "approval"}
	case "mcp":
		return []string{"mcp", "resource", "server"}
	case "packages":
		return []string{"package", "prompt", "command", "包", "提示词", "命令"}
	case "planning":
		return []string{"todo", "计划", "步骤", "planning"}
	case "core_code":
		return []string{"代码", "文件", "shell", "bash", "读文件", "写文件", "终端", "命令行", "ssh", "部署", "运维日志", "搜索", "查找", "目录", "code", "file", "terminal", "command line", "deploy", "ops log", "grep", "search", "find", "ls", "list"}
	default:
		return nil
	}
}

func toolExchangeStatus(query string, enabled, disabled, blocked []string, recs []bundleRecommendation) string {
	if len(enabled) > 0 || len(disabled) > 0 || len(blocked) > 0 {
		return "ok"
	}
	if strings.TrimSpace(query) != "" && len(recs) == 0 {
		return "no_match"
	}
	return "ok"
}

func toolExchangeNextAction(query string, enabled, alreadyActive, disabled, blocked []string, recs []bundleRecommendation) string {
	if len(enabled) > 0 {
		return fmt.Sprintf("Enabled bundle(s): %s. Use the newly active tools directly.", strings.Join(enabled, ", "))
	}
	if len(alreadyActive) > 0 && len(disabled) == 0 && len(blocked) == 0 {
		return fmt.Sprintf("Requested bundle(s) already active: %s. Use active tools directly; do not call tool_exchange again for the same bundle.", strings.Join(alreadyActive, ", "))
	}
	if len(disabled) > 0 || len(blocked) > 0 {
		parts := make([]string, 0, 2)
		if len(disabled) > 0 {
			parts = append(parts, "disabled "+strings.Join(disabled, ", "))
		}
		if len(blocked) > 0 {
			parts = append(parts, "blocked "+strings.Join(blocked, ", "))
		}
		return fmt.Sprintf("Updated bundle state (%s). Use active tools directly.", strings.Join(parts, "; "))
	}
	if strings.TrimSpace(query) != "" && len(recs) == 0 {
		return "No matching bundle or inactive tool was found for this query. Do not repeat the same tool_exchange query; use active tools, try a different concrete query, or report that the capability is unavailable."
	}
	if len(recs) > 0 {
		for _, rec := range recs {
			if !rec.Active {
				return "If an inactive recommended bundle is needed, call tool_exchange once with enable_bundles for that bundle; otherwise use active tools directly."
			}
		}
		return "Recommended bundle(s) are already active. Use the matching tools directly."
	}
	return "Use active tools directly, or call tool_exchange with a specific capability query if another bundle is needed."
}

// NewToolExchangeTool creates a new tool exchange tool.
func NewToolExchangeTool(manager ToolCatalogManager) Tool {
	return NewTypedTool(NewToolSpec("tool_exchange", "Load or unload tool bundles and inspect which bundles are active", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Optional natural-language capability query. Use this when the needed bundle name is unknown; if the bundle name is obvious, prefer enable_bundles directly.",
			},
			"enable_bundles": map[string]interface{}{
				"type":  "array",
				"items": map[string]string{"type": "string"},
			},
			"disable_bundles": map[string]interface{}{
				"type":  "array",
				"items": map[string]string{"type": "string"},
			},
			"include_tools": map[string]interface{}{
				"type":        "boolean",
				"description": "Return tool names for matching bundles. Defaults to false to keep context small.",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum bundles to return for query/catalog views. Defaults to 5 and caps at 20.",
			},
		},
	}, nil), func(ctx context.Context, args toolExchangeArgs) (ToolResult, error) {
		_ = ctx
		if requestsTemplatePinnedBundle(args.EnableBundles, args.DisableBundles) {
			return ToolResult{}, fmt.Errorf(
				"tool bundle %q is template-pinned and cannot be enabled or disabled with tool_exchange; select it in the Agent template before starting the session",
				BundleAlwaysOn,
			)
		}
		catalog := dynamicToolCatalog(manager.Catalog())
		maxResults := boundedMaxResults(args.MaxResults)
		known := knownBundleNames(catalog)
		requested := append(append([]string{}, args.EnableBundles...), args.DisableBundles...)
		if unknown := unknownRequestedBundles(known, requested); len(unknown) > 0 {
			return ToolResult{}, fmt.Errorf(
				"unknown tool bundle(s): %s. Available bundles: %s",
				strings.Join(unknown, ", "),
				formatAvailableBundles(catalog),
			)
		}
		alreadyActive := alreadyActiveRequestedBundles(catalog.ActiveBundles, args.EnableBundles)
		enabled := manager.ActivateBundles(args.EnableBundles...)
		disabled, blocked := manager.DeactivateBundles(args.DisableBundles...)
		catalog = dynamicToolCatalog(manager.Catalog())
		recommendations := recommendedBundles(catalog, args.Query, args.IncludeTools, maxResults)
		structured := map[string]interface{}{
			"status":                 toolExchangeStatus(args.Query, enabled, disabled, blocked, recommendations),
			"enabled_bundles":        enabled,
			"disabled_bundles":       disabled,
			"blocked_bundles":        blocked,
			"already_active_bundles": alreadyActive,
			"active_bundles":         catalog.ActiveBundles,
			"always_active_tools":    catalog.AlwaysActiveTools,
			"recommended_bundles":    recommendations,
			"summary": map[string]interface{}{
				"active_bundle_count":    len(catalog.ActiveBundles),
				"available_bundle_count": len(catalog.Bundles),
				"returned_bundle_count":  len(recommendations),
			},
			"next_action": toolExchangeNextAction(args.Query, enabled, alreadyActive, disabled, blocked, recommendations),
		}
		if strings.TrimSpace(args.Query) == "" {
			structured["bundles"] = summarizeBundles(catalog, args.IncludeTools, maxResults)
		}
		return ToolResult{Structured: structured}, nil
	})
}
