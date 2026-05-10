package weixin

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/tim5wang/godex/internal/core/config"
)

type reconcileFunc func(context.Context, *config.Config) error

// WebAuthStatus is the web-facing Weixin login status view.
type WebAuthStatus struct {
	AccountID  string          `json:"account_id"`
	Enabled    bool            `json:"enabled"`
	Configured bool            `json:"configured"`
	StateDir   string          `json:"state_dir"`
	Account    *WebAuthAccount `json:"account,omitempty"`
	Login      *WebAuthLogin   `json:"login,omitempty"`
}

type WebAuthAccount struct {
	BaseURL     string    `json:"base_url,omitempty"`
	CDNBaseURL  string    `json:"cdn_base_url,omitempty"`
	ILinkBotID  string    `json:"ilink_bot_id,omitempty"`
	ILinkUserID string    `json:"ilink_user_id,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

type WebAuthLogin struct {
	Active         bool      `json:"active"`
	State          string    `json:"state"`
	RawStatus      string    `json:"raw_status,omitempty"`
	Message        string    `json:"message,omitempty"`
	QRCode         string    `json:"qr_code,omitempty"`
	QRCodeImgURL   string    `json:"qr_code_img_url,omitempty"`
	QRCodeImgValue string    `json:"qr_code_img_value,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	LastCheckedAt  time.Time `json:"last_checked_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

type pendingWebLogin struct {
	accountID     string
	transport     transport
	qr            qrCodeResponse
	active        bool
	state         string
	rawStatus     string
	message       string
	startedAt     time.Time
	lastCheckedAt time.Time
	updatedAt     time.Time
}

// WebAuth coordinates Weixin QR login flows for the web settings page.
type WebAuth struct {
	cfgProvider func() *config.Config
	reconcile   reconcileFunc

	mu       sync.Mutex
	sessions map[string]*pendingWebLogin
}

func NewWebAuth(cfgProvider func() *config.Config, reconcile reconcileFunc) *WebAuth {
	return &WebAuth{
		cfgProvider: cfgProvider,
		reconcile:   reconcile,
		sessions:    map[string]*pendingWebLogin{},
	}
}

func (w *WebAuth) Status(ctx context.Context, accountID string) (WebAuthStatus, error) {
	cfg, resolvedAccountID, err := w.currentConfig(accountID)
	if err != nil {
		return WebAuthStatus{}, err
	}
	store := newStateStore(cfg.StateDir, resolvedAccountID)

	var login *pendingWebLogin
	w.mu.Lock()
	login = w.clonePendingLocked(resolvedAccountID)
	w.mu.Unlock()

	if login != nil && login.active {
		login = w.refreshPending(ctx, cfg, login)
	}
	return w.buildStatus(cfg, resolvedAccountID, store, login)
}

func (w *WebAuth) Start(ctx context.Context, accountID string) (WebAuthStatus, error) {
	cfg, resolvedAccountID, err := w.currentConfig(accountID)
	if err != nil {
		return WebAuthStatus{}, err
	}
	loginCfg := cfg.Weixin
	loginCfg.AccountID = resolvedAccountID
	client, err := newTransport(loginCfg, "")
	if err != nil {
		return WebAuthStatus{}, err
	}
	qr, err := client.GetBotQRCode(ctx)
	if err != nil {
		return WebAuthStatus{}, fmt.Errorf("request weixin login QR code: %w", err)
	}
	if strings.TrimSpace(qr.QRCode) == "" {
		return WebAuthStatus{}, fmt.Errorf("weixin login QR response did not include a qrcode token")
	}

	now := time.Now()
	pending := &pendingWebLogin{
		accountID: resolvedAccountID,
		transport: client,
		qr:        qr,
		active:    true,
		state:     "pending",
		rawStatus: "wait",
		message:   "Scan the QR code in Weixin and confirm the login.",
		startedAt: now,
		updatedAt: now,
	}

	w.mu.Lock()
	w.sessions[resolvedAccountID] = pending
	w.mu.Unlock()

	return w.buildStatus(cfg, resolvedAccountID, newStateStore(cfg.StateDir, resolvedAccountID), pending)
}

func (w *WebAuth) Logout(ctx context.Context, accountID string) (WebAuthStatus, error) {
	cfg, resolvedAccountID, err := w.currentConfig(accountID)
	if err != nil {
		return WebAuthStatus{}, err
	}
	store := newStateStore(cfg.StateDir, resolvedAccountID)
	if err := store.RemoveAll(); err != nil {
		return WebAuthStatus{}, err
	}

	w.mu.Lock()
	delete(w.sessions, resolvedAccountID)
	w.mu.Unlock()

	w.reconcileRuntime(cfg)
	return w.buildStatus(cfg, resolvedAccountID, store, nil)
}

func (w *WebAuth) currentConfig(accountID string) (*config.Config, string, error) {
	if w == nil || w.cfgProvider == nil {
		return nil, "", fmt.Errorf("weixin web auth is unavailable")
	}
	cfg := w.cfgProvider()
	if cfg == nil {
		return nil, "", fmt.Errorf("missing current config")
	}
	cloned := cfg.Clone()
	cloned.Weixin.AccountID = normalizeAccountID(firstNonEmpty(accountID, cloned.Weixin.AccountID))
	return cloned, cloned.Weixin.AccountID, nil
}

func (w *WebAuth) buildStatus(cfg *config.Config, accountID string, store *stateStore, login *pendingWebLogin) (WebAuthStatus, error) {
	status := WebAuthStatus{
		AccountID: accountID,
		Enabled:   cfg != nil && cfg.Weixin.Enabled,
		StateDir:  store.Root(),
	}
	account, err := store.LoadAccount()
	if err != nil {
		return WebAuthStatus{}, err
	}
	if account != nil && strings.TrimSpace(account.BotToken) != "" {
		status.Configured = true
		status.Account = &WebAuthAccount{
			BaseURL:     strings.TrimSpace(account.BaseURL),
			CDNBaseURL:  strings.TrimSpace(account.CDNBaseURL),
			ILinkBotID:  strings.TrimSpace(account.ILinkBotID),
			ILinkUserID: strings.TrimSpace(account.ILinkUserID),
			UpdatedAt:   account.UpdatedAt,
		}
	}
	if login != nil {
		renderedQRCode := renderQRCodePreview(login.qr)
		status.Login = &WebAuthLogin{
			Active:         login.active,
			State:          login.state,
			RawStatus:      login.rawStatus,
			Message:        login.message,
			QRCode:         strings.TrimSpace(login.qr.QRCode),
			QRCodeImgURL:   strings.TrimSpace(login.qr.QRCodeImgURL),
			QRCodeImgValue: renderedQRCode,
			StartedAt:      login.startedAt,
			LastCheckedAt:  login.lastCheckedAt,
			UpdatedAt:      login.updatedAt,
		}
	}
	return status, nil
}

func (w *WebAuth) clonePendingLocked(accountID string) *pendingWebLogin {
	current, ok := w.sessions[accountID]
	if !ok || current == nil {
		return nil
	}
	copy := *current
	return &copy
}

func (w *WebAuth) refreshPending(ctx context.Context, cfg *config.Config, login *pendingWebLogin) *pendingWebLogin {
	if login == nil || !login.active || login.transport == nil {
		return login
	}
	status, err := login.transport.GetQRCodeStatus(ctx, login.qr.QRCode)
	now := time.Now()
	if err != nil {
		login.active = false
		login.state = "error"
		login.message = err.Error()
		login.lastCheckedAt = now
		login.updatedAt = now
		w.mu.Lock()
		w.sessions[login.accountID] = login
		w.mu.Unlock()
		return login
	}

	login.rawStatus = strings.TrimSpace(status.Status)
	login.lastCheckedAt = now
	login.updatedAt = now
	switch login.rawStatus {
	case "confirmed":
		baseURL := strings.TrimSpace(status.BaseURL)
		if baseURL == "" {
			baseURL = strings.TrimSpace(cfg.Weixin.BaseURL)
		}
		if baseURL == "" {
			baseURL = defaultBaseURL
		}
		store := newStateStore(cfg.StateDir, login.accountID)
		if err := store.SaveAccount(&accountState{
			BotToken:    strings.TrimSpace(status.BotToken),
			BaseURL:     baseURL,
			CDNBaseURL:  defaultIfEmpty(strings.TrimSpace(cfg.Weixin.CDNBaseURL), defaultCDNBaseURL),
			ILinkBotID:  strings.TrimSpace(status.ILinkBotID),
			ILinkUserID: strings.TrimSpace(status.ILinkUserID),
			UpdatedAt:   now,
		}); err == nil {
			_ = store.SaveCursor("")
			_ = store.writeJSON(store.ContextTokensPath(), contextTokensState{
				Tokens:    map[string]string{},
				UpdatedAt: now,
			})
			login.active = false
			login.state = "confirmed"
			login.message = "Weixin login completed."
			w.reconcileRuntime(cfg)
		} else {
			login.active = false
			login.state = "error"
			login.message = err.Error()
		}
	case "expired":
		login.active = false
		login.state = "expired"
		login.message = "The Weixin QR code expired. Start login again."
	case "wait", "scanned", "":
		login.active = true
		login.state = "pending"
		login.message = pendingLoginMessage(login.rawStatus)
	default:
		login.active = true
		login.state = "pending"
		login.message = pendingLoginMessage(login.rawStatus)
	}
	w.mu.Lock()
	w.sessions[login.accountID] = login
	w.mu.Unlock()
	return login
}

func (w *WebAuth) reconcileRuntime(cfg *config.Config) {
	if w == nil || w.reconcile == nil || cfg == nil {
		return
	}
	reconcileCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = w.reconcile(reconcileCtx, cfg.Clone())
}

func pendingLoginMessage(rawStatus string) string {
	switch strings.TrimSpace(rawStatus) {
	case "scanned":
		return "QR scanned. Confirm the login in Weixin."
	case "wait", "":
		return "Scan the QR code in Weixin and confirm the login."
	default:
		return "Waiting for Weixin login confirmation."
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func renderQRCodePreview(qr qrCodeResponse) string {
	raw := strings.TrimSpace(firstNonEmpty(qr.QRCodeImgContent, qr.QRCodeImgURL))
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "data:image/") {
		return raw
	}
	if strings.HasPrefix(raw, "<svg") || strings.HasPrefix(raw, "<?xml") {
		return "data:image/svg+xml;utf8," + url.PathEscape(raw)
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		png, err := qrcode.Encode(raw, qrcode.Medium, 320)
		if err != nil {
			return raw
		}
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	return "data:image/png;base64," + raw
}
