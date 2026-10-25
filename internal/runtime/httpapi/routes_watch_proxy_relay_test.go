package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/watchauth"
)

// newWatchProxyTestServer is newWatchTestServer with a center bridge attached, so
// the watch surface takes the "drive another node" path.
func newWatchProxyTestServer(t *testing.T, centerURL string) (*httptest.Server, string) {
	t.Helper()
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	auth, err := watchauth.New(filepath.Join(cfg.StateDir, "watch", "devices.json"))
	if err != nil {
		t.Fatalf("watchauth.New: %v", err)
	}
	handler := NewHandlerWithDependencies(Dependencies{
		Config:       manager,
		Backend:      service,
		WatchAuth:    auth,
		CenterBridge: NewCenterBridge(centerURL, "center-token"),
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, pairWatch(t, server, "")
}

// TestWatchCommandWithNodeIDGoesToCenterAndNotLocal is the regression guard for
// multi-node remote control: a request naming another node must leave this node.
//
// The dangerous failure is not "forwarding errors out" (that is visible) but a
// request silently executing on the wrong machine — a command meant for the
// desktop running on the paired node instead. So both halves are asserted: the
// center received it, and this node did not act on it.
func TestWatchCommandWithNodeIDGoesToCenterAndNotLocal(t *testing.T) {
	center, got := captureServer(t)
	server, token := newWatchProxyTestServer(t, center.URL)

	resp, body := postWatchJSON(t,
		server.URL+"/watch/command?node_id=nodeb",
		map[string]any{"session_id": "sess-on-b", "text": "hello from wrist"},
		token,
	)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}

	// Reached the center, on the node-proxy path for the target node.
	if want := "/api/control/nodes/nodeb/proxy/watch/command"; got.path != want {
		t.Fatalf("center path=%q want %q", got.path, want)
	}
	// The instruction travelled intact so the target can act on it.
	if !strings.Contains(got.body, "hello from wrist") {
		t.Fatalf("instruction body missing at center: %q", got.body)
	}
	// Authority travelled explicitly, scoped to this call only.
	if scopes := got.headers.Get(WatchScopesHeader); scopes != watchauth.ScopeCommand {
		t.Fatalf("%s=%q want %q", WatchScopesHeader, scopes, watchauth.ScopeCommand)
	}
	// The watch credential must not leave this node.
	if strings.Contains(got.headers.Get("Authorization"), "wch_") {
		t.Fatal("watch credential leaked to the center")
	}
}

// TestWatchCommandLocalStillServesLocally pins that multi-node support did not
// change the single-node path: no node_id (or the local sentinel) is handled
// in-process, with no center traffic at all.
func TestWatchCommandLocalStillServesLocally(t *testing.T) {
	for _, suffix := range []string{"", "?node_id=local"} {
		center, got := captureServer(t)
		server, token := newWatchProxyTestServer(t, center.URL)

		resp, body := postWatchJSON(t,
			server.URL+"/watch/command"+suffix,
			map[string]any{"session_id": "local-sess", "text": "stay here"},
			token,
		)
		// The session does not exist locally, so the local path answers an error —
		// which is the point: it was handled here rather than forwarded.
		if resp.StatusCode == http.StatusAccepted {
			t.Fatalf("suffix %q: local request unexpectedly accepted: %s", suffix, body)
		}
		if got.path != "" {
			t.Fatalf("suffix %q: local request was forwarded to %q", suffix, got.path)
		}
	}
}

// TestWatchScopeOrRelayAuthorization covers the receiving node's decision for a
// relayed request. A node that never saw this wrist cannot validate a wch_
// token, so it must rely on the scope the forwarding node vouched for — and it
// must never widen that to the route's own scope.
func TestWatchScopeOrRelayAuthorization(t *testing.T) {
	auth, err := watchauth.New(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatalf("watchauth.New: %v", err)
	}
	alwaysTrust := func(*http.Request) bool { return true }
	neverTrust := func(*http.Request) bool { return false }

	// Wraps a handler that just records it ran.
	ran := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true })

	cases := []struct {
		name       string
		trust      func(*http.Request) bool
		scopes     string
		required   string
		wantCode   int
		wantServed bool
	}{
		{
			name:       "relayed with the exact scope is served",
			trust:      alwaysTrust,
			scopes:     watchauth.ScopeCommand,
			required:   watchauth.ScopeCommand,
			wantServed: true,
		},
		{
			name:     "relayed but underprivileged is forbidden, not widened",
			trust:    alwaysTrust,
			scopes:   watchauth.ScopeRead,
			required: watchauth.ScopeCommand,
			wantCode: http.StatusForbidden,
		},
		{
			name:     "relayed with no scopes at all is forbidden",
			trust:    alwaysTrust,
			scopes:   "",
			required: watchauth.ScopeCommand,
			wantCode: http.StatusForbidden,
		},
		{
			// Not on the relay channel: the header proves nothing, so this must be
			// treated as an ordinary unauthenticated request.
			name:     "untrusted caller cannot claim scopes via the header",
			trust:    neverTrust,
			scopes:   watchauth.ScopeCommand,
			required: watchauth.ScopeCommand,
			wantCode: http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran = false
			guard := withWatchScopeOrRelay(auth, tc.required, tc.trust)
			r := httptest.NewRequest(http.MethodPost, "/watch/command", nil)
			if tc.scopes != "" {
				r.Header.Set(WatchScopesHeader, tc.scopes)
			}
			w := httptest.NewRecorder()
			guard(next).ServeHTTP(w, r)

			if ran != tc.wantServed {
				t.Fatalf("handler ran=%v want %v (status=%d body=%s)", ran, tc.wantServed, w.Code, w.Body.String())
			}
			if !tc.wantServed {
				if w.Code != tc.wantCode {
					t.Fatalf("status=%d want %d body=%s", w.Code, tc.wantCode, w.Body.String())
				}
				// The body must explain the refusal so a wrist can show something
				// actionable instead of a bare code.
				var payload map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
					t.Fatalf("error body is not JSON: %s", w.Body.String())
				}
				if strings.TrimSpace(payload["error"]) == "" {
					t.Fatalf("error body has no message: %s", w.Body.String())
				}
			}
		})
	}
}
