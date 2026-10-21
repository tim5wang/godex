// godex-feature: watchauth
// 手表端受限凭证：设备授权码配对（RFC 8628 式）、scope 受限 token、设备吊销。
// 入口：/watch/pair/*、/watch/* （withWatchScope 中间件）
// 文档：docs/wearos-watch-implementation-plan.md §4 W2/W7

// Package watchauth manages paired wrist devices and their restricted
// credentials. A watch is a *client* of godex, never a remote-controlled
// target node, so it does not live in the node registry: it gets its own
// registry with the smallest possible surface.
//
// Design mirrors the existing center node-join flow (one-shot credential,
// explicit trust choice, revocation that takes effect immediately) while
// adding the device-authorization handshake that input-constrained devices
// need: the watch shows a short user code, a trusted endpoint approves it,
// and only then does the watch receive a token.
//
// Tokens are scope-restricted. "watch:admin" is never issued: administrative
// operations (config, node management, credential rotation) stay off-device
// by construction, not by convention.
package watchauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/platform/idgen"
)

// Scope identifiers. Every watch request must carry one of these.
const (
	ScopeRead    = "watch:read"    // read session/node/approval summaries
	ScopeCommand = "watch:command" // send instructions (voice/text)
	ScopeApprove = "watch:approve" // approve/deny pending requests
)

// DefaultScopes is what a watch gets when the approver does not narrow it.
var DefaultScopes = []string{ScopeRead, ScopeCommand, ScopeApprove}

// AllowedScopes is the exhaustive set a device may hold. Anything outside is
// rejected at pairing time rather than silently ignored.
var AllowedScopes = []string{ScopeRead, ScopeCommand, ScopeApprove}

// TokenPrefix marks watch credentials (mirrors ck_/nk_/biz_ naming).
const TokenPrefix = "wk_"

// defaults for the pairing window and token lifetime.
const (
	defaultPairTTL  = 5 * time.Minute
	defaultTokenTTL = 90 * 24 * time.Hour
	userCodeDigits  = 8
)

// Device is one paired watch.
type Device struct {
	DeviceID       string    `json:"device_id"`
	Name           string    `json:"name,omitempty"`
	Kind           string    `json:"kind,omitempty"` // watch
	Scopes         []string  `json:"scopes"`
	CredentialHash string    `json:"credential_hash"`
	CreatedAt      time.Time `json:"created_at"`
	LastSeen       time.Time `json:"last_seen,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	Revoked        bool      `json:"revoked,omitempty"`
}

// HasScope reports whether the device holds scope.
func (d Device) HasScope(scope string) bool {
	for _, s := range d.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// Pairing is one in-flight device-authorization attempt.
type Pairing struct {
	DeviceCode string    `json:"device_code"`
	UserCode   string    `json:"user_code"`
	Name       string    `json:"name,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
	Interval   int       `json:"interval"` // seconds; polling hint for the watch
	Approved   bool      `json:"approved"`
	DeviceID   string    `json:"device_id,omitempty"`
}

// Service holds paired devices and pending pairing requests.
type Service struct {
	mu   sync.Mutex
	path string
	now  func() time.Time

	pairTTL  time.Duration
	tokenTTL time.Duration

	devices  map[string]Device // deviceID -> device
	byHash   map[string]string // sha256(token) -> deviceID
	pairings map[string]*Pairing
	byUser   map[string]string // userCode -> deviceCode

	// pendingToken holds the one-time plaintext token between approve and the
	// watch's exchange poll. In-memory only: a restart between the two steps
	// invalidates the pairing, which is safer than persisting a secret.
	pendingToken map[string]string // deviceID -> plaintext token
}

// New loads (or initialises) an empty watch registry at path.
func New(path string) (*Service, error) {
	s := &Service{
		path:         path,
		now:          time.Now,
		pairTTL:      defaultPairTTL,
		tokenTTL:     defaultTokenTTL,
		devices:      map[string]Device{},
		byHash:       map[string]string{},
		pairings:     map[string]*Pairing{},
		byUser:       map[string]string{},
		pendingToken: map[string]string{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// SetNow overrides the clock (tests).
func (s *Service) SetNow(now func() time.Time) {
	if now == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// StartPairing begins a device-authorization request. The returned pairing
// carries the short user code the watch displays and the long device code it
// polls with.
func (s *Service) StartPairing(name, kind string) (Pairing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()

	if kind == "" {
		kind = "watch"
	}
	p := &Pairing{
		DeviceCode: idgen.New("dc_", 8),
		Name:       strings.TrimSpace(name),
		Kind:       kind,
		ExpiresAt:  s.now().Add(s.pairTTL),
		Interval:   5,
	}
	code, err := s.uniqueUserCodeLocked()
	if err != nil {
		return Pairing{}, err
	}
	p.UserCode = code
	s.pairings[p.DeviceCode] = p
	s.byUser[code] = p.DeviceCode
	return *p, nil
}

// ApprovePairing is called from a trusted endpoint (phone/Web UI). It turns a
// pending user code into a registered device and returns it.
//
// approve: only when the operator is present; scopes are validated here so an
// unknown scope can never reach storage.
func (s *Service) ApprovePairing(userCode string, scopes []string) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()

	code := normalizeUserCode(userCode)
	deviceCode, ok := s.byUser[code]
	if !ok {
		return Device{}, fmt.Errorf("invalid or expired pairing code")
	}
	p := s.pairings[deviceCode]
	if p == nil || s.now().After(p.ExpiresAt) {
		return Device{}, fmt.Errorf("invalid or expired pairing code")
	}
	if p.Approved {
		return Device{}, fmt.Errorf("pairing code already used")
	}

	normalized, err := NormalizeScopes(scopes)
	if err != nil {
		return Device{}, err
	}

	token, hash, err := newToken()
	if err != nil {
		return Device{}, err
	}
	now := s.now()
	device := Device{
		DeviceID:       idgen.New("wch_", 8),
		Name:           p.Name,
		Kind:           p.Kind,
		Scopes:         normalized,
		CredentialHash: hash,
		CreatedAt:      now,
		LastSeen:       now,
		ExpiresAt:      now.Add(s.tokenTTL),
	}
	s.devices[device.DeviceID] = device
	s.byHash[hash] = device.DeviceID

	p.Approved = true
	p.DeviceID = device.DeviceID
	// The plaintext token is handed back exactly once, through the exchange
	// call that the watch is polling. Stash it on the pairing's side channel.
	s.pendingToken[p.DeviceID] = token

	if err := s.saveLocked(); err != nil {
		return Device{}, err
	}
	return device, nil
}

// pendingToken is declared on Service (see above); the token never touches
// disk, so a restart cleanly invalidates any half-finished pairing.

// ExchangeResult is the outcome of a watch token poll.
type ExchangeResult struct {
	Status  string    `json:"status"` // pending | approved | denied | expired
	Device  *Device   `json:"device,omitempty"`
	Token   string    `json:"token,omitempty"`
	Expires time.Time `json:"expires_at,omitempty"`
}

// ExchangeToken implements the watch side of the device flow: it is polled
// until the pairing is approved or expires.
func (s *Service) ExchangeToken(deviceCode string) (ExchangeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()

	p := s.pairings[strings.TrimSpace(deviceCode)]
	if p == nil {
		return ExchangeResult{Status: "expired"}, nil
	}
	if s.now().After(p.ExpiresAt) {
		return ExchangeResult{Status: "expired"}, nil
	}
	if !p.Approved {
		return ExchangeResult{Status: "pending"}, nil
	}
	device, ok := s.devices[p.DeviceID]
	if !ok {
		return ExchangeResult{Status: "expired"}, nil
	}
	token := s.pendingToken[p.DeviceID]
	if token == "" {
		// Already collected once: the code is single-use.
		return ExchangeResult{Status: "expired"}, nil
	}
	delete(s.pendingToken, p.DeviceID)
	// Consume the pairing so the code cannot mint a second token.
	delete(s.pairings, p.DeviceCode)
	delete(s.byUser, p.UserCode)

	return ExchangeResult{
		Status:  "approved",
		Device:  &device,
		Token:   token,
		Expires: device.ExpiresAt,
	}, nil
}

// Authenticate resolves a presented token to a live device.
func (s *Service) Authenticate(token string) (Device, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Device{}, false
	}
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	deviceID, ok := s.byHash[hash]
	if !ok {
		return Device{}, false
	}
	device, ok := s.devices[deviceID]
	if !ok || device.Revoked {
		return Device{}, false
	}
	if !device.ExpiresAt.IsZero() && s.now().After(device.ExpiresAt) {
		return Device{}, false
	}
	return device, true
}

// Touch records activity on a device (best-effort, throttled to 1/minute to
// keep watch polling from turning into write amplification).
func (s *Service) Touch(deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	device, ok := s.devices[deviceID]
	if !ok {
		return
	}
	now := s.now()
	if now.Sub(device.LastSeen) < time.Minute {
		return
	}
	device.LastSeen = now
	s.devices[deviceID] = device
	_ = s.saveLocked()
}

// Revoke invalidates a device immediately (lost watch, user removal).
func (s *Service) Revoke(deviceID string) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	device, ok := s.devices[strings.TrimSpace(deviceID)]
	if !ok {
		return Device{}, fmt.Errorf("unknown watch device %q", deviceID)
	}
	device.Revoked = true
	s.devices[device.DeviceID] = device
	delete(s.byHash, device.CredentialHash)
	if err := s.saveLocked(); err != nil {
		return Device{}, err
	}
	return device, nil
}

// List returns all devices, newest first, for the device-management UI.
func (s *Service) List(_ context.Context) []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Get returns one device.
func (s *Service) Get(deviceID string) (Device, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[strings.TrimSpace(deviceID)]
	return d, ok
}

// NormalizeScopes validates and canonicalises a requested scope set.
//
// A watch request must not be able to widen its own authority, so unknown or
// administrative scopes are an error rather than a silent drop.
func NormalizeScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return append([]string(nil), DefaultScopes...), nil
	}
	allowed := map[string]bool{}
	for _, s := range AllowedScopes {
		allowed[s] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(scopes))
	for _, raw := range scopes {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if !allowed[s] {
			return nil, fmt.Errorf("unsupported watch scope %q (allowed: %s)", s, strings.Join(AllowedScopes, ", "))
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return append([]string(nil), DefaultScopes...), nil
	}
	// Stable order so stored device records diff cleanly.
	sort.Strings(out)
	return out, nil
}

// ---- persistence ----

type persisted struct {
	Devices []Device `json:"devices"`
}

func (s *Service) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("parse watch devices %s: %w", s.path, err)
	}
	for _, d := range p.Devices {
		s.devices[d.DeviceID] = d
		if !d.Revoked && d.CredentialHash != "" {
			s.byHash[d.CredentialHash] = d.DeviceID
		}
	}
	return nil
}

func (s *Service) saveLocked() error {
	items := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		items = append(items, d)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })

	payload, err := json.MarshalIndent(persisted{Devices: items}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	// 0600: device records hold credential hashes.
	if err := os.WriteFile(tmp, append(payload, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// pruneLocked drops expired pairings so the code space cannot be exhausted.
func (s *Service) pruneLocked() {
	now := s.now()
	for code, p := range s.pairings {
		if now.After(p.ExpiresAt) {
			delete(s.pairings, code)
			delete(s.byUser, p.UserCode)
		}
	}
}

// uniqueUserCodeLocked returns an unused 8-digit code. Codes are compared
// digit-normalised so the watch can render them with separators.
func (s *Service) uniqueUserCodeLocked() (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		buf := make([]byte, userCodeDigits)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		var b strings.Builder
		for _, v := range buf {
			b.WriteByte(byte('0' + v%10))
		}
		code := b.String()
		if _, taken := s.byUser[code]; !taken {
			return code, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique pairing code")
}

func normalizeUserCode(code string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(code) {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Service) deviceIDForHash(hash string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byHash[hash]
	return id, ok
}

// newToken mints a watch credential and its storage hash. The plaintext is
// returned exactly once; only the hash is persisted.
func newToken() (token, hash string, err error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = TokenPrefix + hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}
