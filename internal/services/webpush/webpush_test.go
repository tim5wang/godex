package webpush

import (
	"context"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// validKeys returns cryptographically plausible subscription keys: a 65-byte
// P-256 public key (p256dh) and a 16-byte auth secret, base64url-encoded.
func validKeys(t *testing.T) (auth, p256dh string) {
	t.Helper()
	_, x, y, err := elliptic.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate p256 key: %v", err)
	}
	pub := elliptic.Marshal(elliptic.P256(), x, y)
	p256dh = base64.RawURLEncoding.EncodeToString(pub)
	authBytes := make([]byte, 16)
	if _, err := rand.Read(authBytes); err != nil {
		t.Fatalf("generate auth: %v", err)
	}
	auth = base64.RawURLEncoding.EncodeToString(authBytes)
	return auth, p256dh
}

// fakePushEndpoint mimics a browser push service (e.g. FCM/Mozilla): it
// records the POSTed request and replies 201 like a real endpoint. The Web
// Push protocol encrypts the payload (aes128gcm), so the body is binary —
// we only assert presence/length, never JSON contents.
func fakePushEndpoint(t *testing.T) (string, *sync.Map) {
	t.Helper()
	received := &sync.Map{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Store("method", r.Method)
		received.Store("ttl", r.Header.Get("TTL"))
		received.Store("auth", r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		received.Store("bodyLen", len(body))
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, received
}

func TestServiceGeneratesVAPIDKeys(t *testing.T) {
	svc, err := New()
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if svc.PublicKey() == "" {
		t.Fatal("expected non-empty VAPID public key")
	}
	if svc.PublicKey() == svc.privateKey {
		t.Fatal("public and private keys must differ")
	}
}

func TestSubscribeUnsubscribeRoundTrip(t *testing.T) {
	svc, err := New()
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	sub := Subscription{
		Endpoint: "https://push.example.com/s/aaa",
		Keys:     Keys{Auth: "auth1", P256dh: "dh1"},
	}
	if err := svc.Subscribe(sub); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if got := svc.SubscriptionCount(); got != 1 {
		t.Fatalf("expected 1 subscription, got %d", got)
	}
	// Duplicate subscribe is idempotent.
	if err := svc.Subscribe(sub); err != nil {
		t.Fatalf("re-subscribe: %v", err)
	}
	if got := svc.SubscriptionCount(); got != 1 {
		t.Fatalf("expected 1 subscription after duplicate, got %d", got)
	}
	svc.Unsubscribe(sub.Endpoint)
	if got := svc.SubscriptionCount(); got != 0 {
		t.Fatalf("expected 0 subscriptions after unsubscribe, got %d", got)
	}
}

func TestNotifyPostsToSubscribedEndpoints(t *testing.T) {
	endpoint, received := fakePushEndpoint(t)
	svc, err := New()
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	auth, dh := validKeys(t)
	if err := svc.Subscribe(Subscription{Endpoint: endpoint, Keys: Keys{Auth: auth, P256dh: dh}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	notified, err := svc.Notify(context.Background(), "Title", "Body")
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if notified != 1 {
		t.Fatalf("expected 1 notified endpoint, got %d", notified)
	}
	if m, _ := received.Load("method"); m != http.MethodPost {
		t.Fatalf("expected POST, got %v", m)
	}
	if a, _ := received.Load("auth"); !strings.HasPrefix(a.(string), "vapid t=") {
		t.Fatalf("expected VAPID auth header, got %v", a)
	}
	// The Web Push payload is encrypted (aes128gcm); assert it is non-empty
	// rather than decoding JSON.
	if l, _ := received.Load("bodyLen"); l.(int) == 0 {
		t.Fatal("expected non-empty encrypted payload body")
	}
}

func TestNotifySkipsFailedEndpoints(t *testing.T) {
	goodEndpoint, _ := fakePushEndpoint(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(bad.Close)

	svc, err := New()
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	auth, dh := validKeys(t)
	_ = svc.Subscribe(Subscription{Endpoint: bad.URL, Keys: Keys{Auth: auth, P256dh: dh}})
	_ = svc.Subscribe(Subscription{Endpoint: goodEndpoint, Keys: Keys{Auth: auth, P256dh: dh}})

	notified, err := svc.Notify(context.Background(), "T", "B")
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	// Only the healthy endpoint counts; the failed one is dropped.
	if notified != 1 {
		t.Fatalf("expected 1 notified, got %d", notified)
	}
	if got := svc.SubscriptionCount(); got != 1 {
		t.Fatalf("expected failed endpoint pruned, got %d subscriptions", got)
	}
}

func TestNotifyNoSubscribers(t *testing.T) {
	svc, err := New()
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	notified, err := svc.Notify(context.Background(), "T", "B")
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if notified != 0 {
		t.Fatalf("expected 0 notified, got %d", notified)
	}
}
