package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/services/usage"
)

// TestStepEndpointRequiresBizKey verifies POST /v1/agent-steps rejects
// missing / invalid biz keys before touching the agent backend.
func TestStepEndpointRequiresBizKey(t *testing.T) {
	handler, _ := mustBizHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()

	body := bytes.NewBufferString(`{"prompt":"hi"}`)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agent-steps", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without biz key, got %d: %s", resp.StatusCode, readAll(t, resp))
	}
}

// TestStepEndpointRejectsBadRequest verifies schema validation happens before
// session creation (no biz key needed since auth short-circuits, but we still
// need a valid key to reach validation).
func TestStepEndpointRejectsBadRequest(t *testing.T) {
	handler, usageService := mustBizHandler(t)
	created, err := usageService.CreateBizKey(usage.BizKeyCreateRequest{Name: "sales"})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	// Missing prompt.
	body := bytes.NewBufferString(`{}`)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agent-steps", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing prompt, got %d: %s", resp.StatusCode, readAll(t, resp))
	}
}

// TestStepBuildPromptIsolatesInputs verifies the business-input block is
// wrapped in explicit markers (prompt-injection defense).
func TestStepBuildPromptIsolatesInputs(t *testing.T) {
	got := buildStepPrompt("analyze the order", map[string]any{"order_id": "ORD-1"})
	for _, want := range []string{"analyze the order", "业务输入", "业务输入结束", `"order_id": "ORD-1"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q:\n%s", want, got)
		}
	}
	// Without inputs, prompt is returned unchanged.
	if got := buildStepPrompt("plain", nil); got != "plain" {
		t.Fatalf("expected unchanged prompt, got %q", got)
	}
}

// TestStepToolKindClassifiesMCPVsSandbox verifies the mcp/sandbox classifier.
func TestStepToolKindClassifiesMCPVsSandbox(t *testing.T) {
	if got := toolKindFor("crm__get_order"); got != "mcp" {
		t.Fatalf("expected mcp, got %q", got)
	}
	if got := toolKindFor("read_file"); got != "sandbox" {
		t.Fatalf("expected sandbox, got %q", got)
	}
}