package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func fakeTool(name string) Tool {
	return NewTypedTool(NewToolSpec(name, name+" description", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args struct{}) (ToolResult, error) {
		_ = ctx
		_ = args
		return ToolResult{Text: name + " ok"}, nil
	})
}

func TestToolExchangeReturnsCatalogWhenNoChangesRequested(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool("bash"), ToolMeta{
		Bundle:        "core_code",
		Summary:       "core tools",
		DefaultActive: true,
	})
	handler.RegisterWithMeta(fakeTool("compress"), ToolMeta{AlwaysActive: true})
	handler.ActivateDefaults()

	tool := NewToolExchangeTool(handler)
	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("tool exchange execute: %v", err)
	}

	var parsed struct {
		ActiveBundles     []string            `json:"active_bundles"`
		AlwaysActiveTools []string            `json:"always_active_tools"`
		Bundles           []BundleCatalogItem `json:"bundles"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(parsed.ActiveBundles) != 1 || parsed.ActiveBundles[0] != "core_code" {
		t.Fatalf("expected active bundle core_code, got %+v", parsed.ActiveBundles)
	}
	if len(parsed.AlwaysActiveTools) != 1 || parsed.AlwaysActiveTools[0] != "compress" {
		t.Fatalf("expected always-active tool compress, got %+v", parsed.AlwaysActiveTools)
	}
	if len(parsed.Bundles) != 1 || parsed.Bundles[0].Name != "core_code" || !parsed.Bundles[0].Active {
		t.Fatalf("expected active bundle catalog entry, got %+v", parsed.Bundles)
	}
}

func TestToolExchangeCatalogExcludesTemplatePinnedAlwaysOnBundle(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool("bash"), ToolMeta{
		Bundle:        "core_code",
		Summary:       "core tools",
		DefaultActive: true,
	})
	handler.RegisterWithMeta(fakeTool("compress"), ToolMeta{AlwaysActive: true})
	handler.ActivateDefaults()

	canonical := handler.Catalog()
	if len(canonical.Bundles) != 2 || canonical.Bundles[0].Name != "always_on" {
		t.Fatalf("expected canonical catalog to retain always_on, got %+v", canonical.Bundles)
	}

	result, err := NewToolExchangeTool(handler).Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("tool exchange execute: %v", err)
	}
	var parsed struct {
		ActiveBundles []string            `json:"active_bundles"`
		Bundles       []BundleCatalogItem `json:"bundles"`
		Summary       map[string]int      `json:"summary"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(parsed.ActiveBundles) != 1 || parsed.ActiveBundles[0] != "core_code" {
		t.Fatalf("expected dynamic active bundles to exclude always_on, got %v", parsed.ActiveBundles)
	}
	if len(parsed.Bundles) != 1 || parsed.Bundles[0].Name != "core_code" {
		t.Fatalf("expected dynamic catalog to exclude always_on, got %+v", parsed.Bundles)
	}
	if parsed.Summary["active_bundle_count"] != 1 || parsed.Summary["available_bundle_count"] != 1 {
		t.Fatalf("expected dynamic counts to exclude always_on, got %+v", parsed.Summary)
	}
}

func TestToolExchangeRejectsEnablingTemplatePinnedAlwaysOnBundle(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool("compress"), ToolMeta{AlwaysActive: true})
	handler.SetActiveToolsExact()

	_, err := NewToolExchangeTool(handler).Execute(context.Background(), map[string]interface{}{
		"enable_bundles": []interface{}{"always_on"},
	})
	if err == nil || !containsAll(err.Error(), []string{"always_on", "template-pinned", "Agent template"}) {
		t.Fatalf("expected template-pinned enable error, got %v", err)
	}
	if handler.IsActive("compress") {
		t.Fatal("tool_exchange must not enable always_on outside the template baseline")
	}
}

func TestToolExchangeRejectsDisablingTemplatePinnedAlwaysOnBundle(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool("compress"), ToolMeta{AlwaysActive: true})

	_, err := NewToolExchangeTool(handler).Execute(context.Background(), map[string]interface{}{
		"disable_bundles": []interface{}{"always_on"},
	})
	if err == nil || !containsAll(err.Error(), []string{"always_on", "template-pinned", "Agent template"}) {
		t.Fatalf("expected template-pinned disable error, got %v", err)
	}
	if !handler.IsActive("compress") {
		t.Fatal("tool_exchange must not disable template-pinned always_on tools")
	}
}

func TestToolExchangeQueryReturnsSmallRecommendations(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool("web_search"), ToolMeta{
		Bundle:  "web",
		Summary: "current information lookup and page fetching",
	})
	handler.RegisterWithMeta(fakeTool("desktop"), ToolMeta{
		Bundle:  "desktop",
		Summary: "local desktop screenshots and clipboard automation",
	})
	handler.RegisterWithMeta(fakeTool("compress"), ToolMeta{AlwaysActive: true})
	handler.ActivateDefaults()

	tool := NewToolExchangeTool(handler)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "需要联网搜索当前信息",
	})
	if err != nil {
		t.Fatalf("tool exchange execute: %v", err)
	}

	var parsed struct {
		Recommended []bundleRecommendation `json:"recommended_bundles"`
		Bundles     []BundleCatalogItem    `json:"bundles"`
		Summary     map[string]int         `json:"summary"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(parsed.Recommended) == 0 || parsed.Recommended[0].Name != "web" {
		t.Fatalf("expected web recommendation, got %+v", parsed.Recommended)
	}
	if len(parsed.Recommended[0].Tools) != 0 {
		t.Fatalf("expected compact recommendation without tool list, got %+v", parsed.Recommended[0].Tools)
	}
	if len(parsed.Bundles) != 0 {
		t.Fatalf("expected query result to omit full catalog by default, got %+v", parsed.Bundles)
	}
	if parsed.Summary["available_bundle_count"] != 2 {
		t.Fatalf("unexpected summary: %+v", parsed.Summary)
	}
}

func TestToolExchangeWeatherQueryRecommendsWeb(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool("web_search"), ToolMeta{
		Bundle:  "web",
		Summary: "current information lookup and page fetching",
	})
	handler.RegisterWithMeta(fakeTool("bash"), ToolMeta{
		Bundle:        "core_code",
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	handler.ActivateDefaults()

	tool := NewToolExchangeTool(handler)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "weather API 天气查询",
	})
	if err != nil {
		t.Fatalf("tool exchange execute: %v", err)
	}

	var parsed struct {
		Status      string                 `json:"status"`
		Recommended []bundleRecommendation `json:"recommended_bundles"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if parsed.Status != "ok" {
		t.Fatalf("expected ok status, got %q", parsed.Status)
	}
	if len(parsed.Recommended) == 0 || parsed.Recommended[0].Name != "web" {
		t.Fatalf("expected web recommendation for weather query, got %+v", parsed.Recommended)
	}
}

func TestToolExchangeSSHDeployQueryPointsAtActiveCoreTools(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool("bash"), ToolMeta{
		Bundle:        "core_code",
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	handler.RegisterWithMeta(fakeTool("web_search"), ToolMeta{
		Bundle:  "web",
		Summary: "current information lookup and page fetching",
	})
	handler.ActivateDefaults()

	tool := NewToolExchangeTool(handler)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "ssh deploy",
	})
	if err != nil {
		t.Fatalf("tool exchange execute: %v", err)
	}

	var parsed struct {
		NextAction  string                 `json:"next_action"`
		Recommended []bundleRecommendation `json:"recommended_bundles"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(parsed.Recommended) == 0 || parsed.Recommended[0].Name != "core_code" || !parsed.Recommended[0].Active {
		t.Fatalf("expected active core_code recommendation for ssh deploy, got %+v", parsed.Recommended)
	}
	if !strings.Contains(parsed.NextAction, "already active") {
		t.Fatalf("expected next action to direct active tool use, got %q", parsed.NextAction)
	}
}

func TestToolExchangeReportsRequestedBundleAlreadyActive(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool("web_search"), ToolMeta{
		Bundle:  "web",
		Summary: "current information lookup and page fetching",
	})
	handler.ActivateBundles("web")

	tool := NewToolExchangeTool(handler)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"enable_bundles": []interface{}{"web"},
	})
	if err != nil {
		t.Fatalf("tool exchange execute: %v", err)
	}

	var parsed struct {
		AlreadyActive []string `json:"already_active_bundles"`
		Enabled       []string `json:"enabled_bundles"`
		NextAction    string   `json:"next_action"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(parsed.Enabled) != 0 {
		t.Fatalf("expected no newly enabled bundles, got %+v", parsed.Enabled)
	}
	if len(parsed.AlreadyActive) != 1 || parsed.AlreadyActive[0] != "web" {
		t.Fatalf("expected already active web bundle, got %+v", parsed.AlreadyActive)
	}
	if !strings.Contains(parsed.NextAction, "do not call tool_exchange again") {
		t.Fatalf("expected anti-repeat next action, got %q", parsed.NextAction)
	}
}

func TestToolExchangeCanIncludeToolsButNotSchemas(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool("web_search"), ToolMeta{
		Bundle:  "web",
		Summary: "current information lookup and page fetching",
	})
	handler.ActivateDefaults()

	tool := NewToolExchangeTool(handler)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":           "search current info",
		"include_tools":   true,
		"include_schemas": true,
	})
	if err != nil {
		t.Fatalf("tool exchange execute: %v", err)
	}

	var parsed struct {
		Recommended []bundleRecommendation `json:"recommended_bundles"`
		ToolSchemas json.RawMessage        `json:"tool_schemas"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(parsed.Recommended) != 1 || len(parsed.Recommended[0].Tools) != 1 || parsed.Recommended[0].Tools[0] != "web_search" {
		t.Fatalf("expected recommended web tool list, got %+v", parsed.Recommended)
	}
	if len(parsed.ToolSchemas) != 0 {
		t.Fatalf("expected tool_exchange result not to include tool_schemas, got %s", string(parsed.ToolSchemas))
	}
}

func TestToolExchangeInputSchemaDoesNotExposeIncludeSchemas(t *testing.T) {
	handler := NewToolHandler()
	tool := NewToolExchangeTool(handler)
	schema := tool.Spec().ToolSchema()
	props, ok := schema.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected input schema properties, got %+v", schema.InputSchema)
	}
	if _, ok := props["include_schemas"]; ok {
		t.Fatalf("tool_exchange should not expose include_schemas in input schema: %+v", props)
	}
}

func TestToolExchangeNoMatchDoesNotReturnCatalogOrRepeatInstruction(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool("web_search"), ToolMeta{
		Bundle:  "web",
		Summary: "current information lookup and page fetching",
	})
	handler.RegisterWithMeta(fakeTool("desktop"), ToolMeta{
		Bundle:  "desktop",
		Summary: "local desktop screenshots and clipboard automation",
	})
	handler.ActivateDefaults()

	tool := NewToolExchangeTool(handler)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":         "ssh deploy",
		"include_tools": true,
	})
	if err != nil {
		t.Fatalf("tool exchange execute: %v", err)
	}

	var parsed struct {
		Status      string                 `json:"status"`
		NextAction  string                 `json:"next_action"`
		Recommended []bundleRecommendation `json:"recommended_bundles"`
		Bundles     []BundleCatalogItem    `json:"bundles"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if parsed.Status != "no_match" {
		t.Fatalf("expected no_match status, got %q", parsed.Status)
	}
	if len(parsed.Recommended) != 0 {
		t.Fatalf("expected no recommendations, got %+v", parsed.Recommended)
	}
	if len(parsed.Bundles) != 0 {
		t.Fatalf("expected no-match query to omit catalog even with include_tools, got %+v", parsed.Bundles)
	}
	if !strings.Contains(parsed.NextAction, "Do not repeat") || strings.Contains(parsed.NextAction, "enable_bundles") {
		t.Fatalf("unexpected next_action: %q", parsed.NextAction)
	}
}

func TestToolExchangeEnablesBackgroundBundle(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool("background"), ToolMeta{
		Bundle:  "background",
		Summary: "background tools",
	})
	handler.RegisterWithMeta(fakeTool("background"), ToolMeta{
		Bundle:  "background",
		Summary: "background tools",
	})
	handler.RegisterWithMeta(fakeTool("compress"), ToolMeta{AlwaysActive: true})
	handler.ActivateDefaults()

	tool := NewToolExchangeTool(handler)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"enable_bundles": []interface{}{"background"},
	})
	if err != nil {
		t.Fatalf("tool exchange execute: %v", err)
	}

	var parsed struct {
		EnabledBundles []string `json:"enabled_bundles"`
		ActiveBundles  []string `json:"active_bundles"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(parsed.EnabledBundles) != 1 || parsed.EnabledBundles[0] != "background" {
		t.Fatalf("expected enabled bundle background, got %+v", parsed.EnabledBundles)
	}
	if len(parsed.ActiveBundles) != 1 || parsed.ActiveBundles[0] != "background" {
		t.Fatalf("expected active bundle background, got %+v", parsed.ActiveBundles)
	}
	if !handler.IsActive("background") || !handler.IsActive("background") {
		t.Fatal("expected background tools to become active")
	}
}

func TestToolExchangeRejectsUnknownBundle(t *testing.T) {
	handler := NewToolHandler()
	handler.RegisterWithMeta(fakeTool("bash"), ToolMeta{
		Bundle:        "core_code",
		Summary:       "core tools",
		DefaultActive: true,
	})
	handler.RegisterWithMeta(fakeTool("compress"), ToolMeta{AlwaysActive: true})
	handler.ActivateDefaults()

	tool := NewToolExchangeTool(handler)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"enable_bundles": []interface{}{"file_tree"},
	})
	if err == nil {
		t.Fatal("expected unknown bundle error")
	}
	if got := err.Error(); got == "" || !containsAll(got, []string{"unknown tool bundle(s): file_tree", "Available bundles: core_code"}) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func containsAll(haystack string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}
