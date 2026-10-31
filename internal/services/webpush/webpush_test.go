package webpush

import (
	"context"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// newSub creates a subscription with cryptographically valid keys, so the Web
// Push encryption path actually runs during delivery assertions.
func newSub(t *testing.T, endpoint string) Subscription {
	t.Helper()
	auth, p256dh := validKeys(t)
	return Subscription{Endpoint: endpoint, Keys: Keys{Auth: auth, P256dh: p256dh}}
}

// TestSubscriptionsSurviveRestart is the regression for "a restart silently
// unsubscribes every browser": the registry is persisted and restored, and the
// persistence file exists after a subscribe.
func TestSubscriptionsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	svc, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if err := svc.SubscribeFor("tenant\x00alice", newSub(t, "https://push.example.com/s/alice")); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, subscriptionsFileName)); err != nil {
		t.Fatalf("subscription registry was not persisted: %v", err)
	}

	restarted, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate (restart): %v", err)
	}
	if got := restarted.SubscriptionCount(); got != 1 {
		t.Fatalf("expected the subscription to survive restart, got %d", got)
	}
	// The VAPID keys must also survive, or the restored subscription would be
	// undecryptable by the browser that created it.
	if restarted.PublicKey() != svc.PublicKey() {
		t.Fatal("VAPID keys changed across restart; restored subscriptions would be unusable")
	}
}

// TestOwnerScopingSurvivesRestart pins the security property across a restart:
// a restored subscription still belongs to its owner, so one user's nudge can
// never reach another user's browser.
func TestOwnerScopingSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	svc, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	aliceEndpoint, aliceReceived := fakePushEndpoint(t)
	bobEndpoint, bobReceived := fakePushEndpoint(t)
	if err := svc.SubscribeFor("tenant\x00alice", newSub(t, aliceEndpoint)); err != nil {
		t.Fatal(err)
	}
	if err := svc.SubscribeFor("tenant\x00bob", newSub(t, bobEndpoint)); err != nil {
		t.Fatal(err)
	}

	restarted, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate (restart): %v", err)
	}
	notified, err := restarted.NotifyOwner(context.Background(), "tenant\x00alice", "T", "B")
	if err != nil {
		t.Fatalf("NotifyOwner: %v", err)
	}
	if notified != 1 {
		t.Fatalf("expected exactly alice to be notified, got %d", notified)
	}
	if _, ok := bobReceived.Load("method"); ok {
		t.Fatal("bob's browser received alice's notification after restart")
	}
	if _, ok := aliceReceived.Load("method"); !ok {
		t.Fatal("alice's restored subscription did not receive her notification")
	}
}

// TestUnsubscribeIsPersisted covers the removal path: a browser that
// unsubscribes must not be resurrected by the next restart.
func TestUnsubscribeIsPersisted(t *testing.T) {
	dir := t.TempDir()
	svc, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	endpoint := "https://push.example.com/s/gone"
	if err := svc.SubscribeFor("tenant\x00alice", newSub(t, endpoint)); err != nil {
		t.Fatal(err)
	}
	svc.Unsubscribe(endpoint)

	restarted, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate (restart): %v", err)
	}
	if got := restarted.SubscriptionCount(); got != 0 {
		t.Fatalf("unsubscribed endpoint came back after restart: %d", got)
	}
}

// TestUnattributedSubscriptionNeverReceivesOwnerNudges pins the intended
// semantics of an owner-less subscription (one registered before identity
// existed, or through a surface with no resolver): it gets broadcasts, never a
// specific user's work.
func TestUnattributedSubscriptionNeverReceivesOwnerNudges(t *testing.T) {
	svc, err := New()
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	endpoint, received := fakePushEndpoint(t)
	if err := svc.Subscribe(newSub(t, endpoint)); err != nil {
		t.Fatal(err)
	}
	notified, err := svc.NotifyOwner(context.Background(), "tenant\x00alice", "T", "B")
	if err != nil {
		t.Fatalf("NotifyOwner: %v", err)
	}
	if notified != 0 {
		t.Fatalf("unattributed subscription must not receive an owner nudge, got %d", notified)
	}
	if _, ok := received.Load("method"); ok {
		t.Fatal("unattributed subscription received an owner-targeted notification")
	}
	// A broadcast still reaches it, which is what "unattributed" means.
	if updated, err := svc.Notify(context.Background(), "T", "B"); err != nil || updated != 1 {
		t.Fatalf("broadcast should reach the unattributed subscription: %d %v", updated, err)
	}
}

// TestInMemoryServiceDoesNotTouchDisk documents the other half of the design:
// New/NewWithKeys stay in-memory, so tests and ephemeral runs write nothing.
func TestInMemoryServiceDoesNotTouchDisk(t *testing.T) {
	svc, err := New()
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if svc.subsPath != "" {
		t.Fatalf("New() must not bind a persistence path, got %q", svc.subsPath)
	}
	if err := svc.Subscribe(newSub(t, "https://push.example.com/s/mem")); err != nil {
		t.Fatal(err)
	}
	if got := svc.SubscriptionCount(); got != 1 {
		t.Fatalf("in-memory registry should still work, got %d", got)
	}
}
