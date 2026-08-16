package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/tools"
)

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
