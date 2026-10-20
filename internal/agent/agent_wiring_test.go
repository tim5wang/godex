package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/tools"
)

func TestDeepSeekToolsClientForConfigReusesLLMKey(t *testing.T) {
	// No key resolved: the deepseek search provider and the fetch fallback stay inert.
	if got := deepseekToolsClientForConfig(nil); got != nil {
		t.Fatal("nil config should yield no client")
	}
	if got := deepseekToolsClientForConfig(&config.Config{}); got != nil {
		t.Fatal("missing deepseek provider should yield no client")
	}
	empty := &config.Config{LLMProviders: map[string]llm.ProviderConfig{"deepseek": {APIKey: "  "}}}
	if got := deepseekToolsClientForConfig(empty); got != nil {
		t.Fatal("blank key should yield no client")
	}

	// The Search/Fetch credential is the DeepSeek LLM provider key, so enabling
	// DeepSeek for chat is enough to opt into the billed tools.
	configured := &config.Config{LLMProviders: map[string]llm.ProviderConfig{"deepseek": {APIKey: "sk-test"}}}
	if got := deepseekToolsClientForConfig(configured); got == nil {
		t.Fatal("expected a client when the deepseek key is configured")
	}
}

func TestProviderOrderIncludes(t *testing.T) {
	if !providerOrderIncludes([]string{"duckduckgo", " DeepSeek "}, "deepseek") {
		t.Error("expected case/space-insensitive match")
	}
	if providerOrderIncludes([]string{"duckduckgo"}, "deepseek") {
		t.Error("unexpected match for an absent provider")
	}
}

func TestPluginHTTPGetRoutesThroughWebFetchPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body><p>plugin fetch ok</p></body></html>"))
	}))
	defer server.Close()

	service := tools.NewWebFetchService(config.WebFetchConfig{
		Enabled:           true,
		MaxChars:          4000,
		TimeoutSeconds:    10,
		Policy:            "allow_all",
		AllowPrivateHosts: true,
	}, t.TempDir())

	a := &Agent{webFetch: service}
	get := a.pluginHTTPGet()
	body, err := get(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("plugin http get: %v", err)
	}
	if !strings.Contains(body, "plugin fetch ok") {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestPluginHTTPGetUnavailable(t *testing.T) {
	a := &Agent{}
	get := a.pluginHTTPGet()
	if _, err := get(context.Background(), "https://example.com"); err == nil {
		t.Fatal("expected error when web fetch unavailable")
	}
}
