package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/services/webpush"
)

func newPushTestServer(t *testing.T) (*httptest.Server, *webpush.Service) {
	t.Helper()
	svc, err := webpush.New()
	if err != nil {
		t.Fatalf("new webpush service: %v", err)
	}
	handler := NewPushHandler(svc, nil)
	return httptest.NewServer(handler), svc
}

func TestPushPublicKey(t *testing.T) {
	server, svc := newPushTestServer(t)
	defer server.Close()

	resp, err := http.Get(server.URL + "/push/public-key")
	if err != nil {
		t.Fatalf("get public key: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var payload struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.PublicKey != svc.PublicKey() {
		t.Fatal("public key mismatch")
	}
}

func TestPushSubscribeAndCount(t *testing.T) {
	server, svc := newPushTestServer(t)
	defer server.Close()

	body, _ := json.Marshal(map[string]any{
		"endpoint": "https://push.example.com/s/sub1",
		"keys":     map[string]string{"auth": "a", "p256dh": "d"},
	})
	resp, err := http.Post(server.URL+"/push/subscribe", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := svc.SubscriptionCount(); got != 1 {
		t.Fatalf("expected 1 subscription, got %d", got)
	}
}

func TestPushSubscribeRejectsMissingEndpoint(t *testing.T) {
	server, _ := newPushTestServer(t)
	defer server.Close()

	body, _ := json.Marshal(map[string]any{"keys": map[string]string{"auth": "a", "p256dh": "d"}})
	resp, err := http.Post(server.URL+"/push/subscribe", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestPushUnsubscribe(t *testing.T) {
	server, svc := newPushTestServer(t)
	defer server.Close()

	body, _ := json.Marshal(map[string]any{
		"endpoint": "https://push.example.com/s/sub2",
		"keys":     map[string]string{"auth": "a", "p256dh": "d"},
	})
	resp, _ := http.Post(server.URL+"/push/subscribe", "application/json", bytes.NewReader(body))
	resp.Body.Close()
	if got := svc.SubscriptionCount(); got != 1 {
		t.Fatalf("expected 1 subscription, got %d", got)
	}

	unsub, _ := json.Marshal(map[string]string{"endpoint": "https://push.example.com/s/sub2"})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/push/unsubscribe", bytes.NewReader(unsub))
	unsubResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	unsubResp.Body.Close()
	if unsubResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", unsubResp.StatusCode)
	}
	if got := svc.SubscriptionCount(); got != 0 {
		t.Fatalf("expected 0 subscriptions, got %d", got)
	}
}

func TestPushAuthorizeRejectsUnauthorized(t *testing.T) {
	svc, err := webpush.New()
	if err != nil {
		t.Fatalf("new webpush service: %v", err)
	}
	handler := NewPushHandler(svc, func(r *http.Request) bool {
		return strings.TrimSpace(r.Header.Get("Authorization")) == "Bearer secret"
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/push/public-key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}
}
