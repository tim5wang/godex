package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Provider struct {
	Name         string
	EnvVar       string
	AuthURL      string
	TokenURL     string
	ClientID     string
	Scopes       []string
	ExtraParams  map[string]string
	TokenToKey   func(*TokenResponse) string
	TLSPreflight bool
	CodexOAuth   bool
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token"`
	APIKey       string `json:"api_key"`
	APIKeyCamel  string `json:"apiKey"`
	OpenAIAPIKey string `json:"openai_api_key"`
}

type Result struct {
	Provider string
	APIKey   string
	EnvVar   string
	Err      error
}

func OpenAICodexProvider() Provider {
	return Provider{
		Name:     "codex",
		EnvVar:   "GODEX_OPENAI_CODEX_OAUTH_TOKEN",
		AuthURL:  "https://auth.openai.com/oauth/authorize",
		TokenURL: "https://auth.openai.com/oauth/token",
		ClientID: "app_EMoamEEZ73f0CkXaXp7hrann",
		Scopes:   []string{"openid", "profile", "email", "offline_access"},
		ExtraParams: map[string]string{
			"id_token_add_organizations": "true",
			"codex_cli_simplified_flow":  "true",
			"originator":                 "godex",
		},
		TokenToKey: func(tok *TokenResponse) string {
			return strings.TrimSpace(tok.AccessToken)
		},
		TLSPreflight: true,
		CodexOAuth:   true,
	}
}

func PKCEFlow(ctx context.Context, prov Provider, openBrowser func(string) error) (*Result, error) {
	verifier, challenge := generatePKCE()
	listenAddr, callbackHost, callbackPath := callbackConfig(prov)
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("starting callback server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://%s:%d%s", callbackHost, port, callbackPath)
	state := generateState()
	authURL := buildAuthURL(prov, redirectURI, state, challenge)
	codeCh := make(chan codeResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		handleCallback(w, r, state, codeCh)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if openBrowser == nil {
		return nil, fmt.Errorf("browser opener is unavailable")
	}
	if err := openBrowser(authURL); err != nil {
		return nil, fmt.Errorf("opening browser: %w", err)
	}

	select {
	case <-ctx.Done():
		return &Result{Provider: prov.Name, Err: ctx.Err()}, nil
	case callback := <-codeCh:
		if callback.err != nil {
			return &Result{Provider: prov.Name, Err: callback.err}, nil
		}
		tok, err := exchangeCode(ctx, prov, callback.code, redirectURI, verifier)
		if err != nil {
			return &Result{Provider: prov.Name, Err: fmt.Errorf("token exchange: %w", err)}, nil
		}
		apiKey := strings.TrimSpace(prov.TokenToKey(tok))
		if apiKey == "" {
			apiKey = strings.TrimSpace(firstNonEmpty(tok.APIKey, tok.APIKeyCamel, tok.OpenAIAPIKey))
		}
		return &Result{Provider: prov.Name, APIKey: apiKey, EnvVar: prov.EnvVar}, nil
	}
}

type KeyKind string

const (
	KeyKindAPIKey     KeyKind = "api-key"
	KeyKindCodexOAuth KeyKind = "codex-oauth"
	KeyKindUnknown    KeyKind = "unknown"
)

func IdentifyKey(key string) KeyKind {
	key = strings.TrimSpace(key)
	if key == "" {
		return KeyKindUnknown
	}
	if strings.HasPrefix(key, "sk-") || strings.HasPrefix(key, "sk_") {
		return KeyKindAPIKey
	}
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return KeyKindUnknown
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return KeyKindUnknown
		}
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(payload, &raw) != nil {
		return KeyKindUnknown
	}
	if _, ok := raw["https://api.openai.com/auth"]; ok {
		return KeyKindCodexOAuth
	}
	return KeyKindUnknown
}

type TLSPreflightResult struct {
	OK      bool   `json:"ok"`
	Kind    string `json:"kind,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

const openAIAuthProbeURL = "https://auth.openai.com/oauth/authorize?response_type=code&client_id=godex-preflight&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback&scope=openid+profile+email"

func RunTLSPreflight(timeout time.Duration) *TLSPreflightResult {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(openAIAuthProbeURL)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		return &TLSPreflightResult{OK: true}
	}
	kind := "network"
	if isTLSError(err.Error()) {
		kind = "tls-cert"
	}
	return &TLSPreflightResult{OK: false, Kind: kind, Message: err.Error()}
}

func FormatTLSPreflightFix(result *TLSPreflightResult) string {
	if result == nil || result.OK {
		return ""
	}
	if result.Kind != "tls-cert" {
		return fmt.Sprintf("OAuth preflight failed (network error): %s", result.Message)
	}
	return fmt.Sprintf("OAuth preflight failed: TLS certificate validation error. Cause: %s", result.Message)
}

type codeResult struct {
	code string
	err  error
}

func generatePKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	hash := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(hash[:])
	return verifier, challenge
}

func generateState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func callbackConfig(prov Provider) (listenAddr, callbackHost, callbackPath string) {
	if prov.CodexOAuth {
		return "localhost:1455", "localhost", "/auth/callback"
	}
	return "127.0.0.1:0", "127.0.0.1", "/callback"
}

func buildAuthURL(prov Provider, redirectURI, state, challenge string) string {
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {prov.ClientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"scope":                 {strings.Join(prov.Scopes, " ")},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	for key, value := range prov.ExtraParams {
		params.Set(key, value)
	}
	return prov.AuthURL + "?" + params.Encode()
}

func handleCallback(w http.ResponseWriter, r *http.Request, expectedState string, ch chan<- codeResult) {
	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		desc := firstNonEmpty(q.Get("error_description"), errParam)
		ch <- codeResult{err: fmt.Errorf("OAuth error: %s", desc)}
		http.Error(w, "Authentication failed: "+desc, http.StatusBadRequest)
		return
	}
	if q.Get("state") != expectedState {
		ch <- codeResult{err: fmt.Errorf("state mismatch")}
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}
	code := q.Get("code")
	if code == "" {
		ch <- codeResult{err: fmt.Errorf("no authorization code received")}
		http.Error(w, "No code received", http.StatusBadRequest)
		return
	}
	ch <- codeResult{code: code}
	w.Header().Set("Content-Type", "text/html")
	_, _ = fmt.Fprint(w, `<!doctype html><html><body><h2>Authentication successful</h2><p>You can close this tab and return to Godex.</p><script>window.close()</script></body></html>`)
}

func exchangeCode(ctx context.Context, prov Provider, code, redirectURI, verifier string) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {prov.ClientID},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, prov.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, sanitizeErrorBody(body))
	}
	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}
	return &tok, nil
}

var tlsCertErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)unable to get local issuer certificate`),
	regexp.MustCompile(`(?i)unable to verify the first certificate`),
	regexp.MustCompile(`(?i)self[- ]signed certificate`),
	regexp.MustCompile(`(?i)certificate has expired`),
	regexp.MustCompile(`(?i)x509`),
}

func isTLSError(msg string) bool {
	for _, pattern := range tlsCertErrorPatterns {
		if pattern.MatchString(msg) {
			return true
		}
	}
	var tlsErr *tls.CertificateVerificationError
	_ = tlsErr
	return strings.Contains(msg, "certificate") && (strings.Contains(msg, "verify") || strings.Contains(msg, "unknown authority") || strings.Contains(msg, "expired"))
}

func sanitizeErrorBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if strings.HasPrefix(text, "<") || strings.HasPrefix(text, "<!") {
		return "(HTML error page: server returned non-JSON response)"
	}
	if len(text) > 200 {
		return text[:200] + "..."
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
