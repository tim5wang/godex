// godex-feature: webpush
// Web Push：VAPID 密钥管理、内存订阅注册表、标准 Web Push 协议通知；
// 中心只中继实时事件，不持久化节点历史。
// 入口：/push/* API、Web Push 订阅
// 文档：docs/node-mesh-design.md、docs/node-center-bridge-design.md

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
	"strings"
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
	subs map[string]registered // keyed by endpoint

	client *http.Client
}

// registered pairs a subscription with the identity that created it. Nudges are
// owner-targeted, so a background job for one user can never deliver a
// notification body (which quotes that user's stream titles) to another user's
// browser. The zero owner means "unattributed" and only receives broadcasts.
type registered struct {
	owner string
	sub   Subscription
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
		subs:       make(map[string]registered),
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
		subs:       make(map[string]registered),
		client:     &http.Client{},
	}, nil
}

// PublicKey returns the VAPID application server key (base64url) that the
// browser uses when subscribing.
func (s *Service) PublicKey() string { return s.publicKey }

// Subscribe registers (or refreshes) a subscription, keyed by endpoint. The
// owner is left empty: unattributed subscriptions receive broadcasts only.
func (s *Service) Subscribe(sub Subscription) error {
	return s.SubscribeFor("", sub)
}

// SubscribeFor registers a subscription owned by one identity.
func (s *Service) SubscribeFor(owner string, sub Subscription) error {
	if sub.Endpoint == "" {
		return fmt.Errorf("subscription endpoint is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[sub.Endpoint] = registered{owner: strings.TrimSpace(owner), sub: sub}
	return nil
}

// OwnerOf returns the identity a registered endpoint belongs to, or "" when the
// endpoint is unknown or unattributed.
func (s *Service) OwnerOf(endpoint string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subs[endpoint].owner
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
// returned total. It is intentionally unfiltered: only the operator-driven test
// path uses it, where the caller has already been authorized as the operator.
func (s *Service) Notify(ctx context.Context, title, body string) (int, error) {
	return s.notify(ctx, "", false, title, body)
}

// NotifyOwner delivers a notification only to the subscriptions registered by
// one identity. This is the path background nudges must use: the body quotes
// stream titles, so broadcasting it would leak one user's work to another.
func (s *Service) NotifyOwner(ctx context.Context, owner, title, body string) (int, error) {
	target := strings.TrimSpace(owner)
	if target == "" {
		// Refusing an empty owner is deliberate: falling back to a broadcast
		// here would turn a scoping bug into a cross-user leak.
		return 0, nil
	}
	return s.notify(ctx, target, true, title, body)
}

func (s *Service) notify(ctx context.Context, owner string, onlyOwner bool, title, body string) (int, error) {
	payload, err := json.Marshal(map[string]string{"title": title, "body": body})
	if err != nil {
		return 0, fmt.Errorf("marshal notification: %w", err)
	}
	s.mu.Lock()
	subs := make([]Subscription, 0, len(s.subs))
	for _, entry := range s.subs {
		if onlyOwner && entry.owner != owner {
			continue
		}
		subs = append(subs, entry.sub)
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
