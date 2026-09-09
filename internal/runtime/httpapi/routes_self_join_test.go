package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSelfJoinRegistersWithCenterAndPersistsConfig exercises the node-side
// auto-join endpoint end to end against a fake center: it registers the node,
// issues a credential, and persists the control config (which the live-apply
// path then uses to start the relay agent — not exercised here).
func TestSelfJoinRegistersWithCenterAndPersistsConfig(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)

	// Fake center: register + credential endpoints under /api (the webui
	// strips the prefix, mirroring cmd/godex/main.go's root delegation).
	var gotRegisterBody map[string]any
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok_center" {
			http.Error(w, `{"error":"bad token"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/control/nodes/register":
			_ = json.NewDecoder(r.Body).Decode(&gotRegisterBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": gotRegisterBody["id"]})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/credential"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/control/nodes/"), "/credential")
			_ = json.NewEncoder(w).Encode(map[string]string{"node_id": id, "credential": "ck_issued"})
		default:
			http.Error(w, `{"error":"unexpected"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(center.Close)

	mux := http.NewServeMux()
	registerSelfJoinRoute(mux, manager, func(h http.Handler) http.Handler { return h })
	server := httptest.NewServer(mux)
	defer server.Close()

	body := `{"center_url":"` + center.URL + `","token":"tok_center","node_id":"my-laptop","name":"Laptop","trust_level":"trusted"}`
	resp, err := http.Post(server.URL+"/control/self/join", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data := make([]byte, 4096)
		n, _ := resp.Body.Read(data)
		t.Fatalf("join status %d: %s", resp.StatusCode, strings.TrimSpace(string(data[:n])))
	}
	var out struct {
		NodeID string `json:"node_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.NodeID != "my-laptop" {
		t.Fatalf("node_id = %q, want my-laptop", out.NodeID)
	}

	// The config must now carry the joined center + credential + token.
	cfg2 := manager.Current()
	if cfg2.Control.CenterURL != center.URL {
		t.Fatalf("center_url = %q, want %q", cfg2.Control.CenterURL, center.URL)
	}
	if cfg2.Control.NodeID != "my-laptop" {
		t.Fatalf("node_id = %q, want my-laptop", cfg2.Control.NodeID)
	}
	if cfg2.Control.Credential != "ck_issued" {
		t.Fatalf("credential = %q, want ck_issued", cfg2.Control.Credential)
	}
	if cfg2.Control.CenterToken != "tok_center" {
		t.Fatalf("center_token = %q, want tok_center", cfg2.Control.CenterToken)
	}
}

// TestSelfJoinValidatesRequiredFields checks the endpoint rejects missing
// center URL / token before any network call.
func TestSelfJoinValidatesRequiredFields(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	mux := http.NewServeMux()
	registerSelfJoinRoute(mux, manager, func(h http.Handler) http.Handler { return h })
	server := httptest.NewServer(mux)
	defer server.Close()

	for _, body := range []string{
		`{"center_url":"","token":"x"}`,
		`{"center_url":"https://x","token":""}`,
	} {
		resp, err := http.Post(server.URL+"/control/self/join", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("join(%s): %v", body, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("join(%s) status = %d, want 400", body, resp.StatusCode)
		}
	}
}

var _ = context.Background
