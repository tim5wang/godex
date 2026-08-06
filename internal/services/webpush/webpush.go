// Package webpush provides center-side Web Push support: VAPID key
// management, an in-memory subscription registry, and notification sending
// via the standard Web Push protocol. Subscriptions are intentionally NOT
// persisted — the center only relays live events, matching the node-mesh
// design decision that the center keeps no durable per-node history.
package webpush

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/SherClockHolmes/webpush-go"
)

// Subscription mirrors the browser PushSubscription object.
type Subscription = webpush.Subscription

// Keys are the base64url-encoded p256dh + auth keys of a subscription.
type Keys = webpush.Keys

// DefaultSubscriber is the VAPID "sub" claim used when the operator does not
// configure a contact address.
const DefaultSubscriber = "mailto:admin@godex.local"

// Service manages VAPID credentials and a live set of push subscriptions.
type Service struct {
	privateKey string
	publicKey  string
	subscriber string

	mu   sync.Mutex
	subs map[string]Subscription // keyed by endpoint

	client *http.Client
}

// New creates a service with freshly generated VAPID keys.
func New() (*Service, error) {
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return nil, fmt.Errorf("generate vapid keys: %w", err)
	}
	return &Service{
		privateKey: privateKey,
		publicKey:  publicKey,
		subscriber: DefaultSubscriber,
		subs:       make(map[string]Subscription),
		client:     &http.Client{},
	}, nil
}

// LoadOrCreate restores VAPID keys from stateDir/push_keys.json when present,
// otherwise generates fresh keys and persists them so subscriptions survive
// restarts. The subscription registry itself stays in-memory (not persisted).
func LoadOrCreate(stateDir string) (*Service, error) {
	path := filepath.Join(stateDir, "push_keys.json")
	if data, err := os.ReadFile(path); err == nil {
		var stored struct {
			PrivateKey string `json:"private_key"`
			PublicKey  string `json:"public_key"`
			Subscriber string `json:"subscriber"`
		}
		if err := json.Unmarshal(data, &stored); err == nil && stored.PrivateKey != "" && stored.PublicKey != "" {
			return NewWithKeys(stored.PrivateKey, stored.PublicKey, stored.Subscriber)
		}
	}
	svc, err := New()
	if err != nil {
		return nil, err
	}
	stored := map[string]string{
		"private_key": svc.privateKey,
		"public_key":  svc.publicKey,
		"subscriber":  svc.subscriber,
	}
	data, _ := json.Marshal(stored)
	_ = os.MkdirAll(stateDir, 0o755)
	_ = os.WriteFile(path, data, 0o600)
	return svc, nil
}

// NewWithKeys creates a service from operator-provided VAPID keys.
func NewWithKeys(privateKey, publicKey, subscriber string) (*Service, error) {
	if privateKey == "" || publicKey == "" {
		return nil, fmt.Errorf("vapid private and public keys are required")
	}
	if subscriber == "" {
		subscriber = DefaultSubscriber
	}
	return &Service{
		privateKey: privateKey,
		publicKey:  publicKey,
		subscriber: subscriber,
		subs:       make(map[string]Subscription),
		client:     &http.Client{},
	}, nil
}

// PublicKey returns the VAPID application server key (base64url) that the
// browser uses when subscribing.
func (s *Service) PublicKey() string { return s.publicKey }

// Subscribe registers (or refreshes) a subscription, keyed by endpoint.
func (s *Service) Subscribe(sub Subscription) error {
	if sub.Endpoint == "" {
		return fmt.Errorf("subscription endpoint is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[sub.Endpoint] = sub
	return nil
}

// Unsubscribe removes a subscription by endpoint.
func (s *Service) Unsubscribe(endpoint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, endpoint)
}

// SubscriptionCount returns the number of live subscriptions.
func (s *Service) SubscriptionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// Notify delivers a {title, body} notification to every subscription. Failed
// endpoints are pruned (410 Gone / network errors) and counted out of the
// returned total.
func (s *Service) Notify(ctx context.Context, title, body string) (int, error) {
	payload, err := json.Marshal(map[string]string{"title": title, "body": body})
	if err != nil {
		return 0, fmt.Errorf("marshal notification: %w", err)
	}
	s.mu.Lock()
	subs := make([]Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	if len(subs) == 0 {
		return 0, nil
	}

	options := &webpush.Options{
		HTTPClient:      s.client,
		Subscriber:      s.subscriber,
		VAPIDPublicKey:  s.publicKey,
		VAPIDPrivateKey: s.privateKey,
		TTL:             60,
	}
	notified := 0
	for _, sub := range subs {
		resp, err := webpush.SendNotificationWithContext(ctx, payload, &sub, options)
		if err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			notified++
			continue
		}
		// The push service no longer knows this subscription (410 Gone) or
		// the delivery failed; drop it so the registry stays healthy.
		s.Unsubscribe(sub.Endpoint)
	}
	return notified, nil
}
