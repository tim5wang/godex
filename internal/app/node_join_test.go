package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/services/usage"
)

func newJoinRunner(t *testing.T) (*Runner, *config.Manager, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	manager, err := config.NewManager(config.Options{HomeDir: home, WorkspaceDir: workspace})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	r := &Runner{
		Cfg:           manager.Current(),
		ConfigManager: manager,
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
	}
	return r, manager, home
}

// TestRunNodeJoinSyncsNodeIDFile verifies that join writes the chosen node id
// into state/node.json so the node registers under the operator-specified id
// (not a stale auto-generated one).
func TestRunNodeJoinSyncsNodeIDFile(t *testing.T) {
	r, manager, _ := newJoinRunner(t)
	stateDir := manager.Current().StateDir
	if err := r.runNodeJoin(context.Background(), []string{
		"https://godex.claw.carc.top",
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
	}); err != nil {
		t.Fatalf("node join: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "node.json"))
	if err != nil {
		t.Fatalf("read state/node.json: %v", err)
	}
	if !strings.Contains(string(data), "my-laptop") {
		t.Fatalf("expected node id synced to node.json, got:\n%s", data)
	}
}

func TestRunNodeJoinWritesControlConfig(t *testing.T) {
	r, manager, home := newJoinRunner(t)
	err := r.runNodeJoin(context.Background(), []string{
		"https://godex.claw.carc.top",
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
		"--trust", "guarded-remote",
		"--name", "dev box",
	})
	if err != nil {
		t.Fatalf("node join: %v", err)
	}
	cfg := manager.Current()
	if cfg.Control.CenterURL != "https://godex.claw.carc.top" {
		t.Fatalf("expected center_url %q, got %q", "https://godex.claw.carc.top", cfg.Control.CenterURL)
	}
	if cfg.Control.Credential != "ck_test_abc123" {
		t.Fatalf("expected credential %q, got %q", "ck_test_abc123", cfg.Control.Credential)
	}
	if cfg.Control.NodeID != "my-laptop" {
		t.Fatalf("expected node_id %q, got %q", "my-laptop", cfg.Control.NodeID)
	}
	if cfg.Control.TrustLevel != "guarded-remote" {
		t.Fatalf("expected trust_level %q, got %q", "guarded-remote", cfg.Control.TrustLevel)
	}
	if cfg.Control.NodeName != "dev box" {
		t.Fatalf("expected node_name %q, got %q", "dev box", cfg.Control.NodeName)
	}

	data, err := os.ReadFile(filepath.Join(home, "godex.yaml"))
	if err != nil {
		t.Fatalf("read home godex.yaml: %v", err)
	}
	for _, want := range []string{"center_url: https://godex.claw.carc.top", "node_id: my-laptop", "trust_level: guarded-remote"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("expected %q in godex.yaml, got:\n%s", want, data)
		}
	}
	// The credential is a secret: it must live in the home .env, not godex.yaml.
	envData, err := os.ReadFile(filepath.Join(home, ".env"))
	if err != nil {
		t.Fatalf("read home .env: %v", err)
	}
	if !strings.Contains(string(envData), "GODEX_CONTROL_CREDENTIAL=ck_test_abc123") {
		t.Fatalf("expected credential in .env, got:\n%s", envData)
	}
}

func TestRunNodeJoinDefaultsTrustToTrusted(t *testing.T) {
	r, manager, _ := newJoinRunner(t)
	err := r.runNodeJoin(context.Background(), []string{
		"https://godex.claw.carc.top",
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
	})
	if err != nil {
		t.Fatalf("node join: %v", err)
	}
	if got := manager.Current().Control.TrustLevel; got != "trusted" {
		t.Fatalf("expected default trust_level trusted, got %q", got)
	}
}

func TestRunNodeJoinDoesNotOverwriteUnrelatedConfig(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "godex.yaml"), []byte(strings.Join([]string{
		"agent:",
		"  profile: coding",
		"control:",
		"  node_name: existing-name",
	}, "\n")), 0600); err != nil {
		t.Fatalf("write home godex.yaml: %v", err)
	}
	manager, err := config.NewManager(config.Options{HomeDir: home, WorkspaceDir: workspace})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	r := &Runner{
		Cfg:           manager.Current(),
		ConfigManager: manager,
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
	}
	if err := r.runNodeJoin(context.Background(), []string{
		"https://godex.claw.carc.top",
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
	}); err != nil {
		t.Fatalf("node join: %v", err)
	}
	cfg := manager.Current()
	if cfg.AgentProfile != "coding" {
		t.Fatalf("expected agent.profile preserved, got %q", cfg.AgentProfile)
	}
	if cfg.Control.NodeName != "existing-name" {
		t.Fatalf("expected existing node_name preserved, got %q", cfg.Control.NodeName)
	}
}

func TestRunNodeJoinValidatesArguments(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing id",
			args: []string{"https://godex.claw.carc.top", "--credential", "ck_x"},
			want: "--id",
		},
		{
			name: "missing credential",
			args: []string{"https://godex.claw.carc.top", "--id", "n1"},
			want: "--credential",
		},
		{
			name: "bad credential prefix",
			args: []string{"https://godex.claw.carc.top", "--id", "n1", "--credential", "secret"},
			want: "ck_",
		},
		{
			name: "invalid id characters",
			args: []string{"https://godex.claw.carc.top", "--id", "bad id!", "--credential", "ck_x"},
			want: "node id",
		},
		{
			name: "invalid center url",
			args: []string{"not-a-url", "--id", "n1", "--credential", "ck_x"},
			want: "center",
		},
		{
			name: "missing center url",
			args: []string{"--id", "n1", "--credential", "ck_x"},
			want: "center",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _ := newJoinRunner(t)
			err := r.runNodeJoin(context.Background(), tc.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("expected error containing %q, got: %v", tc.want, err)
			}
		})
	}
}

// TestRunNodeJoinLLMProxyWithExistingKey verifies that join --llm-proxy
// <gdx_key> writes a local openai_compatible provider pointing at the center
// usage gateway, keeps the key in the home .env (never in godex.yaml), and
// preserves any existing providers.
func TestRunNodeJoinLLMProxyWithExistingKey(t *testing.T) {
	r, manager, home := newJoinRunner(t)
	err := r.runNodeJoin(context.Background(), []string{
		"https://godex.claw.carc.top",
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
		"--llm-proxy", "gdx_test_secret_key",
		"--llm-models", "deepseek-v4-flash,glm-5.3",
	})
	if err != nil {
		t.Fatalf("node join: %v", err)
	}
	cfg := manager.Current()
	provider, ok := cfg.LLMProviders[llmProxyProviderID]
	if !ok {
		t.Fatalf("expected provider %q, got providers: %v", llmProxyProviderID, cfg.LLMProviders)
	}
	if provider.Type != "openai_compatible" {
		t.Fatalf("expected openai_compatible provider, got %q", provider.Type)
	}
	if provider.BaseURL != "https://godex.claw.carc.top/api/v1" {
		t.Fatalf("expected base_url https://godex.claw.carc.top/api/v1, got %q", provider.BaseURL)
	}
	if provider.APIKeyEnv != llmProxyKeyEnv {
		t.Fatalf("expected api_key_env %q, got %q", llmProxyKeyEnv, provider.APIKeyEnv)
	}
	if len(provider.Models) != 2 {
		t.Fatalf("expected 2 models, got %d: %v", len(provider.Models), provider.Models)
	}
	if _, ok := provider.Models["deepseek-v4-flash"]; !ok {
		t.Fatalf("expected deepseek-v4-flash model, got %v", provider.Models)
	}
	// The key must live in the home .env, not godex.yaml.
	envData, err := os.ReadFile(filepath.Join(home, ".env"))
	if err != nil {
		t.Fatalf("read home .env: %v", err)
	}
	if !strings.Contains(string(envData), llmProxyKeyEnv+"=gdx_test_secret_key") {
		t.Fatalf("expected key in .env, got:\n%s", envData)
	}
	yamlData, err := os.ReadFile(filepath.Join(home, "godex.yaml"))
	if err != nil {
		t.Fatalf("read home godex.yaml: %v", err)
	}
	if strings.Contains(string(yamlData), "gdx_test_secret_key") {
		t.Fatalf("key must not be written to godex.yaml, got:\n%s", yamlData)
	}
}

// TestRunNodeJoinLLMProxyPreservesExistingProviders verifies that join
// --llm-proxy merges into the existing provider set instead of replacing it.
func TestRunNodeJoinLLMProxyPreservesExistingProviders(t *testing.T) {
	r, manager, _ := newJoinRunner(t)
	// Seed an existing provider through the same config manager.
	if err := manager.UpdateProviders(map[string]llm.ProviderConfig{
		"existing": {
			Type:    "openai_compatible",
			BaseURL: "https://example.com/v1",
			APIKey:  "sk-existing",
			Models: map[string]llm.ModelConfig{
				"m1": {Model: "m1"},
			},
		},
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	err := r.runNodeJoin(context.Background(), []string{
		"https://godex.claw.carc.top",
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
		"--llm-proxy", "gdx_test_secret_key",
		"--llm-models", "deepseek-v4-flash",
	})
	if err != nil {
		t.Fatalf("node join: %v", err)
	}
	cfg := manager.Current()
	if _, ok := cfg.LLMProviders["existing"]; !ok {
		t.Fatalf("existing provider was dropped: %v", cfg.LLMProviders)
	}
	if _, ok := cfg.LLMProviders[llmProxyProviderID]; !ok {
		t.Fatalf("expected llm proxy provider: %v", cfg.LLMProviders)
	}
}

// TestRunNodeJoinLLMProxyAutoCreatesKey verifies that a bare --llm-proxy
// (normalized to auto) creates a usage key on the center via
// POST /api/usage/keys and fetches the model list from GET /api/v1/models.
func TestRunNodeJoinLLMProxyAutoCreatesKey(t *testing.T) {
	var mu sync.Mutex
	var createdKeyName string
	var createdAllowedModels []string
	var modelsAuth string
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/usage/keys":
			var req usage.KeyCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode key request: %v", err)
			}
			mu.Lock()
			createdKeyName = req.Name
			createdAllowedModels = req.AllowedModels
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(usage.KeyCreateResponse{
				Key:    usage.ProxyAPIKey{ID: "k1", Name: req.Name, KeyPrefix: "gdx_"},
				Secret: "gdx_auto_created_secret",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/models":
			if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer gdx_auto_created_secret") {
				t.Errorf("expected models call authenticated with created key, got auth %q", auth)
			}
			mu.Lock()
			modelsAuth = r.Header.Get("Authorization")
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data": []map[string]any{
					{"id": "deepseek-v4-flash", "object": "model"},
					{"id": "glm-5.3", "object": "model"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer center.Close()

	r, manager, home := newJoinRunner(t)
	err := r.runNodeJoin(context.Background(), []string{
		center.URL,
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
		"--llm-proxy",
		"--token", "web-token-123",
	})
	if err != nil {
		t.Fatalf("node join: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if createdKeyName != "node-my-laptop" {
		t.Fatalf("expected key name node-my-laptop, got %q", createdKeyName)
	}
	if len(createdAllowedModels) != 0 {
		t.Fatalf("expected no allowed models on auto-created key, got %v", createdAllowedModels)
	}
	if !strings.Contains(modelsAuth, "gdx_auto_created_secret") {
		t.Fatalf("models fetch was not authenticated with the created key: %q", modelsAuth)
	}
	cfg := manager.Current()
	provider, ok := cfg.LLMProviders[llmProxyProviderID]
	if !ok {
		t.Fatalf("expected llm proxy provider: %v", cfg.LLMProviders)
	}
	if len(provider.Models) != 2 {
		t.Fatalf("expected 2 models fetched from center, got %d: %v", len(provider.Models), provider.Models)
	}
	envData, err := os.ReadFile(filepath.Join(home, ".env"))
	if err != nil {
		t.Fatalf("read home .env: %v", err)
	}
	if !strings.Contains(string(envData), llmProxyKeyEnv+"=gdx_auto_created_secret") {
		t.Fatalf("expected auto-created key in .env, got:\n%s", envData)
	}
}

// TestRunNodeJoinLLMProxyAutoRequiresToken verifies that --llm-proxy auto
// without a web token fails with a clear error.
func TestRunNodeJoinLLMProxyAutoRequiresToken(t *testing.T) {
	r, _, _ := newJoinRunner(t)
	err := r.runNodeJoin(context.Background(), []string{
		"https://godex.claw.carc.top",
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
		"--llm-proxy", "auto",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "--token") {
		t.Fatalf("expected error mentioning --token, got: %v", err)
	}
}

// TestRunNodeJoinLLMProxyRejectsBadKey verifies that a non-gdx_ value passed
// to --llm-proxy is rejected.
func TestRunNodeJoinLLMProxyRejectsBadKey(t *testing.T) {
	r, _, _ := newJoinRunner(t)
	err := r.runNodeJoin(context.Background(), []string{
		"https://godex.claw.carc.top",
		"--id", "my-laptop",
		"--credential", "ck_test_abc123",
		"--llm-proxy", "sk-not-a-gdx-key",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gdx_") {
		t.Fatalf("expected error mentioning gdx_, got: %v", err)
	}
}

// TestNormalizeLLMProxyFlag verifies that a bare --llm-proxy is rewritten to
// --llm-proxy=auto while a valued form is left untouched.
func TestNormalizeLLMProxyFlag(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"https://x", "--llm-proxy"}, "--llm-proxy=auto"},
		{[]string{"https://x", "--llm-proxy", "--trust", "guarded-remote"}, "--llm-proxy=auto"},
		{[]string{"https://x", "--llm-proxy", "gdx_abc"}, "--llm-proxy"},
		{[]string{"https://x", "--id", "n1"}, ""},
	}
	for _, tc := range cases {
		got := normalizeLLMProxyFlag(tc.in)
		joined := strings.Join(got, " ")
		if tc.want == "" {
			if strings.Contains(joined, "llm-proxy") {
				t.Fatalf("expected no llm-proxy flag in %q, got %q", tc.in, joined)
			}
			continue
		}
		if !strings.Contains(joined, tc.want) {
			t.Fatalf("expected %q in %q for input %v", tc.want, joined, tc.in)
		}
	}
}
