package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/templates"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

func newAgentTemplatesTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	t.Cleanup(server.Close)
	return server
}

func doAgentTemplateJSON(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var payload []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		payload = data
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestAgentTemplatesListAndGet(t *testing.T) {
	server := newAgentTemplatesTestServer(t)

	resp, raw := doAgentTemplateJSON(t, http.MethodGet, server.URL+"/agent-templates", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var list []templates.AgentTemplate
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := map[string]templates.AgentTemplate{}
	for _, tpl := range list {
		found[tpl.ID] = tpl
	}
	for _, id := range []string{templates.BuiltinDefault, templates.BuiltinMinimal, templates.BuiltinGeneralAssistant, templates.BuiltinCoder, templates.BuiltinResearcher, templates.BuiltinReviewer, templates.BuiltinPlanner} {
		tpl, ok := found[id]
		if !ok {
			t.Fatalf("expected builtin template %q in list", id)
		}
		if tpl.Source != templates.SourceBuiltin {
			t.Fatalf("template %q source = %q, want builtin", id, tpl.Source)
		}
	}

	resp, raw = doAgentTemplateJSON(t, http.MethodGet, server.URL+"/agent-templates/minimal", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get minimal status = %d", resp.StatusCode)
	}
	var minimal templates.AgentTemplate
	if err := json.Unmarshal(raw, &minimal); err != nil {
		t.Fatalf("decode minimal: %v", err)
	}
	if len(minimal.Tools) != 4 || !minimal.TrimHeavySections {
		t.Fatal("minimal template lost its tool preset / trim flag over the wire")
	}

	resp, _ = doAgentTemplateJSON(t, http.MethodGet, server.URL+"/agent-templates/no-such-template", nil)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("expected non-200 for unknown template")
	}
}

func TestAgentTemplatesOptions(t *testing.T) {
	server := newAgentTemplatesTestServer(t)

	resp, raw := doAgentTemplateJSON(t, http.MethodGet, server.URL+"/agent-templates/options", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("options status = %d", resp.StatusCode)
	}
	var options struct {
		Bundles []struct {
			Name  string   `json:"name"`
			Tools []string `json:"tools"`
		} `json:"bundles"`
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		t.Fatalf("decode options: %v", err)
	}
	bundles := map[string]bool{}
	for _, b := range options.Bundles {
		bundles[b.Name] = true
	}
	if !bundles["core_code"] {
		// The catalog is config-driven (e.g. web/browser bundles only register
		// when the corresponding tools are enabled), so assert the always-on
		// core bundle instead of a fixed bundle set.
		t.Fatalf("expected core_code bundle, got %v", bundles)
	}
	toolSet := map[string]bool{}
	for _, name := range options.Tools {
		toolSet[name] = true
	}
	if !toolSet["bash"] || !toolSet["read_file"] {
		t.Fatalf("expected bash and read_file in tools, got %d tools", len(options.Tools))
	}
}

func TestAgentTemplatesCRUDRoundtrip(t *testing.T) {
	server := newAgentTemplatesTestServer(t)

	tpl := map[string]any{
		"id":        "team-coder",
		"name":      "Team Coder",
		"bundles":   []string{"core_code", "lsp"},
		"persona":   "Scoped, surgical coder.",
		"skills":    []string{"definitely-not-installed"},
		"avatar":    "🛠️",
		"scenarios": []string{"coding"},
	}

	resp, _ := doAgentTemplateJSON(t, http.MethodPost, server.URL+"/agent-templates", tpl)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}

	resp, raw := doAgentTemplateJSON(t, http.MethodGet, server.URL+"/agent-templates/team-coder", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get created template status = %d", resp.StatusCode)
	}
	var created templates.AgentTemplate
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Source != templates.SourceUser {
		t.Fatalf("source = %q, want user", created.Source)
	}

	// Validate surfaces the missing-skill warning without failing.
	resp, raw = doAgentTemplateJSON(t, http.MethodPost, server.URL+"/agent-templates/team-coder/validate", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate status = %d", resp.StatusCode)
	}
	var validation struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &validation); err != nil {
		t.Fatalf("decode validation: %v", err)
	}
	if len(validation.Warnings) != 1 {
		t.Fatalf("expected one validate warning, got %v", validation.Warnings)
	}

	// Update via PUT.
	tpl["name"] = "Team Coder v2"
	resp, _ = doAgentTemplateJSON(t, http.MethodPut, server.URL+"/agent-templates/team-coder", tpl)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d", resp.StatusCode)
	}
	resp, raw = doAgentTemplateJSON(t, http.MethodGet, server.URL+"/agent-templates/team-coder", nil)
	var updated templates.AgentTemplate
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("decode updated: %v", err)
	}
	if updated.Name != "Team Coder v2" {
		t.Fatalf("updated name = %q", updated.Name)
	}

	// Builtin collision is rejected; builtin delete is rejected.
	resp, _ = doAgentTemplateJSON(t, http.MethodPost, server.URL+"/agent-templates", map[string]any{"id": "default", "name": "shadow"})
	if resp.StatusCode == http.StatusCreated {
		t.Fatal("expected builtin ID collision to be rejected")
	}
	resp, _ = doAgentTemplateJSON(t, http.MethodDelete, server.URL+"/agent-templates/team-coder", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	resp, _ = doAgentTemplateJSON(t, http.MethodDelete, server.URL+"/agent-templates/default", nil)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("expected builtin delete to be rejected")
	}
}
