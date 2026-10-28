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
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
)

// rotateRunner builds a Runner whose config already points at the given center
// and holds a ck_ credential, mirroring a node that joined earlier.
func rotateRunner(t *testing.T, centerURL string) (*Runner, *config.Manager, string) {
	t.Helper()
	// Pin the control env vars: control.* are resolved as env-over-yaml, and other
	// tests in this package legitimately export them via WriteHomeEnvVar (which
	// calls os.Setenv). Without pinning, this test would resolve whichever
	// credential ran last — an order-dependent flake, not a code defect.
	t.Setenv("GODEX_CONTROL_CENTER_URL", centerURL)
	t.Setenv("GODEX_CONTROL_CREDENTIAL", "ck_node_abc")
	t.Setenv("GODEX_CONTROL_CENTER_TOKEN", "")

	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "godex.yaml"), []byte(strings.Join([]string{
		"control:",
		"  center_url: \"" + centerURL + "\"",
		"  node_id: \"my-laptop\"",
		"  credential: \"ck_node_abc\"",
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
	return r, manager, home
}

// TestRunNodeRotateProxyTokenPersistsToHomeEnv is the node half of the
// self-service rotation: the new nk_ must land in the home .env, because that is
// where control.center_token is read from (env overrides godex.yaml). Writing it
// to the YAML would look successful and change nothing.
func TestRunNodeRotateProxyTokenPersistsToHomeEnv(t *testing.T) {
	var gotAuth string
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"node_id": "my-laptop", "node_proxy_token": "nk_rotated_secret",
		})
	}))
	t.Cleanup(center.Close)

	r, _, home := rotateRunner(t, center.URL)
	if err := r.runNodeRotateProxyToken(context.Background(), nil); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if gotAuth != "Bearer ck_node_abc" {
		t.Errorf("center saw %q, want this node's own ck_ credential", gotAuth)
	}
	env, err := os.ReadFile(filepath.Join(home, ".env"))
	if err != nil {
		t.Fatalf("read home .env: %v", err)
	}
	if !strings.Contains(string(env), "GODEX_CONTROL_CENTER_TOKEN=nk_rotated_secret") {
		t.Fatalf("home .env does not carry the rotated credential:\n%s", env)
	}
	// The secret itself must not be echoed to stdout — only a masked form.
	if out := r.Stdout.(*bytes.Buffer).String(); strings.Contains(out, "nk_rotated_secret") {
		t.Errorf("stdout leaked the credential: %q", out)
	}
}

// The command takes no node id, so it cannot be pointed at someone else's node.
func TestRunNodeRotateProxyTokenRequiresJoinedNode(t *testing.T) {
	// Explicitly empty: an inherited credential from another test would look like
	// a joined node and hide the case under test.
	t.Setenv("GODEX_CONTROL_CENTER_URL", "")
	t.Setenv("GODEX_CONTROL_CREDENTIAL", "")
	t.Setenv("GODEX_CONTROL_CENTER_TOKEN", "")

	root := t.TempDir()
	manager, err := config.NewManager(config.Options{
		HomeDir: filepath.Join(root, "home"), WorkspaceDir: filepath.Join(root, "workspace"),
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	r := &Runner{Cfg: manager.Current(), ConfigManager: manager, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}

	err = r.runNodeRotateProxyToken(context.Background(), nil)
	if err == nil {
		t.Fatal("a node with no center_url/credential must refuse rather than call somewhere")
	}
	if !strings.Contains(err.Error(), "center") {
		t.Errorf("error should name the missing center configuration, got %q", err)
	}
}

// A center that rejects the credential must surface as an error, not a silent
// success: the operator needs to know the node was NOT rotated.
func TestRunNodeRotateProxyTokenSurfacesRejection(t *testing.T) {
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(center.Close)

	r, _, home := rotateRunner(t, center.URL)
	err := r.runNodeRotateProxyToken(context.Background(), nil)
	if err == nil {
		t.Fatal("a rejected credential must fail the command")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".env")); statErr == nil {
		data, _ := os.ReadFile(filepath.Join(home, ".env"))
		if strings.Contains(string(data), "GODEX_CONTROL_CENTER_TOKEN") {
			t.Errorf("a failed rotation must not persist a credential:\n%s", data)
		}
	}
}

// An older center without the endpoint must be reported distinctly, so an
// operator knows to upgrade rather than to re-check this node.
func TestRunNodeRotateProxyTokenReportsUnsupportedCenter(t *testing.T) {
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(center.Close)

	r, _, _ := rotateRunner(t, center.URL)
	err := r.runNodeRotateProxyToken(context.Background(), nil)
	if err == nil {
		t.Fatal("an unsupported center must fail the command")
	}
	if !strings.Contains(err.Error(), "does not support per-node credential rotation") {
		t.Errorf("error should name the missing center capability, got %q", err)
	}
}
