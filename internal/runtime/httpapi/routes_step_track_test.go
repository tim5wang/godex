package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/services/usage"
)

// TestStepLocatorCarriesProjectDir verifies the working directory is part of
// the step session identity so POST/GET/cancel resolve the same session.
func TestStepLocatorCarriesProjectDir(t *testing.T) {
	without := stepLocator("stp_1", "")
	if _, ok := without.Metadata["project_dir"]; ok {
		t.Fatalf("expected no project_dir in metadata when empty: %+v", without.Metadata)
	}
	with := stepLocator("stp_1", "/srv/orders")
	if got := with.Metadata["project_dir"]; got != "/srv/orders" {
		t.Fatalf("expected project_dir=/srv/orders, got %q", got)
	}
	if without.Channel != with.Channel || without.Key != with.Key {
		t.Fatalf("channel/key must stay identical across project_dir variants")
	}
}

// TestStepTrackRequiresBizKey verifies the tracking endpoints reject missing
// biz keys before touching the backend.
func TestStepTrackRequiresBizKey(t *testing.T) {
	handler, _ := mustBizHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/agent-steps/stp_1"},
		{http.MethodPost, "/v1/agent-steps/stp_1/cancel"},
		{http.MethodPost, "/v1/agent-steps/stp_1/reply"},
	} {
		req, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s: expected 401 without biz key, got %d", tc.method, tc.path, resp.StatusCode)
		}
	}
}

// TestStepTrackGetUnknownStep verifies GET on a never-created step still
// resolves a (fresh) session via the deterministic locator and returns 200
// with a not-running status.
func TestStepTrackGetUnknownStep(t *testing.T) {
	handler, usageService := mustBizHandler(t)
	created, err := usageService.CreateBizKey(usage.BizKeyCreateRequest{Name: "sales", Pin: "123456"})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/agent-steps/never_ran", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, readAll(t, resp))
	}
}

// TestStepTrackReplyRequiresValue verifies the reply endpoint rejects a
// missing interaction value with 400 before touching the session.
func TestStepTrackReplyRequiresValue(t *testing.T) {
	handler, usageService := mustBizHandler(t)
	created, err := usageService.CreateBizKey(usage.BizKeyCreateRequest{Name: "sales", Pin: "123456"})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agent-steps/stp_1/reply",
		strings.NewReader(`{}`))
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
		t.Fatalf("expected 400 for empty value, got %d: %s", resp.StatusCode, readAll(t, resp))
	}
}

// TestStepTrackCancelNotRunning verifies cancel on a step with no active turn
// returns 409 (nothing to cancel).
func TestStepTrackCancelNotRunning(t *testing.T) {
	handler, usageService := mustBizHandler(t)
	created, err := usageService.CreateBizKey(usage.BizKeyCreateRequest{Name: "sales", Pin: "123456"})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agent-steps/never_ran/cancel", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for non-running step, got %d: %s", resp.StatusCode, readAll(t, resp))
	}
}
