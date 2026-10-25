package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/services/watchauth"
)

// The watch is paired with exactly one node, so driving another node works by
// forwarding: A → center /control/nodes/{B}/proxy/watch/... → B. These tests
// pin the three things that make that safe and correct: which node a request
// targets, what authority crosses the hop, and that a request aimed at B never
// executes on A.

func TestWatchNodeTargetNormalizesLocal(t *testing.T) {
	cases := map[string]string{
		"":        "", // absent → serve locally (historical behaviour)
		"local":   "", // "local" is the node's own id in the session list
		"  ":      "",
		"nodeb":   "nodeb",
		" nodeb ": "nodeb",
	}
	for query, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/watch/sessions", nil)
		if query != "" {
			q := r.URL.Query()
			q.Set(watchNodeQuery, query)
			r.URL.RawQuery = q.Encode()
		}
		if got := watchNodeTarget(r); got != want {
			t.Fatalf("watchNodeTarget(%q) = %q, want %q", query, got, want)
		}
	}
}

func TestRelayWatchScopesRequiresTrustAndExactScope(t *testing.T) {
	trustAll := func(*http.Request) bool { return true }
	trustNone := func(*http.Request) bool { return false }

	newReq := func(scopes string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/watch/command", nil)
		if scopes != "" {
			r.Header.Set(WatchScopesHeader, scopes)
		}
		return r
	}

	// Not on the relay channel: no authority, and callers must be able to tell it
	// apart from "trusted but underprivileged" so the status code stays honest.
	if granted, onRelay := relayWatchScopes(newReq(watchauth.ScopeCommand), trustNone, watchauth.ScopeCommand); onRelay || len(granted) > 0 {
		t.Fatalf("untrusted request granted scopes=%v relay=%v", granted, onRelay)
	}

	// Trusted but the origin node did not claim the scope this route needs.
	if granted, onRelay := relayWatchScopes(newReq(watchauth.ScopeRead), trustAll, watchauth.ScopeCommand); !onRelay || len(granted) > 0 {
		t.Fatalf("scope escalation: granted=%v relay=%v", granted, onRelay)
	}

	// Trusted with an empty/missing header must not be read as "everything".
	if granted, _ := relayWatchScopes(newReq(""), trustAll, watchauth.ScopeCommand); len(granted) > 0 {
		t.Fatalf("empty header granted %v", granted)
	}

	// The exact scope passes.
	if granted, onRelay := relayWatchScopes(newReq(watchauth.ScopeCommand), trustAll, watchauth.ScopeCommand); !onRelay || len(granted) != 1 {
		t.Fatalf("expected command scope, got %v relay=%v", granted, onRelay)
	}

	// A bundled header grants only the requested scope, never the extras.
	granted, _ := relayWatchScopes(
		newReq(watchauth.ScopeRead+","+watchauth.ScopeCommand+","+watchauth.ScopeApprove),
		trustAll, watchauth.ScopeCommand,
	)
	if len(granted) != 1 || granted[0] != watchauth.ScopeCommand {
		t.Fatalf("expected only command, got %v", granted)
	}

	// Unknown scopes are dropped rather than passed along.
	if got := knownWatchScopes([]string{"watch:root", watchauth.ScopeRead}); len(got) != 1 || got[0] != watchauth.ScopeRead {
		t.Fatalf("unknown scope survived filtering: %v", got)
	}
}

// captureServer records what the center actually receives.
func captureServer(t *testing.T) (*httptest.Server, *capturedRequest) {
	t.Helper()
	cap := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.path = r.URL.Path
		cap.query = r.URL.RawQuery
		cap.headers = r.Header.Clone()
		cap.method = r.Method
		cap.body = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

type capturedRequest struct {
	method  string
	path    string
	query   string
	headers http.Header
	body    string
}

func TestForwardRoutesToCenterProxyAndStripsNodeSelector(t *testing.T) {
	srv, got := captureServer(t)
	forwarder := &watchProxyForwarder{bridge: NewCenterBridge(srv.URL, "center-token")}

	r := httptest.NewRequest(http.MethodPost, "/watch/command?node_id=nodeb&limit=5", strings.NewReader(`{"text":"hi"}`))
	r.Header.Set("Authorization", "Bearer wch_secret")
	w := httptest.NewRecorder()

	if !forwarder.forward(w, r, []string{watchauth.ScopeCommand}) {
		t.Fatal("forward() reported not-forwarded for a remote target")
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	// The proxied path must be the center's node proxy, not the local watch path.
	wantPath := "/api/control/nodes/nodeb/proxy/watch/command"
	if got.path != wantPath {
		t.Fatalf("path=%q want %q", got.path, wantPath)
	}
	// node_id must NOT survive: the target is a plain node and would read it again
	// and forward to itself forever.
	if strings.Contains(got.query, watchNodeQuery) {
		t.Fatalf("node selector leaked into forwarded query: %q", got.query)
	}
	if !strings.Contains(got.query, "limit=5") {
		t.Fatalf("unrelated query params were dropped: %q", got.query)
	}
	// The watch credential is meaningless to the center/target and must not travel.
	if strings.Contains(got.headers.Get("Authorization"), "wch_") {
		t.Fatalf("watch credential leaked to center: %q", got.headers.Get("Authorization"))
	}
	if got.headers.Get("Authorization") != "Bearer center-token" {
		t.Fatalf("center auth header = %q", got.headers.Get("Authorization"))
	}
	// Authority travels explicitly, and only the scope the call actually used.
	if scopes := got.headers.Get(WatchScopesHeader); scopes != watchauth.ScopeCommand {
		t.Fatalf("%s = %q, want %q", WatchScopesHeader, scopes, watchauth.ScopeCommand)
	}
	if got.body != `{"text":"hi"}` {
		t.Fatalf("body not forwarded: %q", got.body)
	}
}

func TestForwardServesLocallyWhenNoTarget(t *testing.T) {
	srv, got := captureServer(t)
	forwarder := &watchProxyForwarder{bridge: NewCenterBridge(srv.URL, "center-token")}

	for _, target := range []string{"", "local"} {
		path := "/watch/sessions"
		if target != "" {
			path += "?node_id=" + target
		}
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if forwarder.forward(httptest.NewRecorder(), r, nil) {
			t.Fatalf("target %q should not be forwarded", target)
		}
	}
	if got.path != "" {
		t.Fatalf("center should not have been called, got %q", got.path)
	}
}

func TestForwardWithoutCenterAnswers503(t *testing.T) {
	// A standalone node has no center and no proxy handler, so it genuinely cannot
	// reach other nodes. It must say so instead of falling back to running the
	// command locally — a wrist command aimed at node B running on node A is a
	// wrong-machine bug.
	for _, f := range []*watchProxyForwarder{nil, {}, {bridge: NewCenterBridge("", "")}} {
		r := httptest.NewRequest(http.MethodPost, "/watch/command?node_id=nodeb", strings.NewReader("{}"))
		w := httptest.NewRecorder()
		if !f.forward(w, r, []string{watchauth.ScopeCommand}) {
			t.Fatal("forward() should consume remote-target requests even without a center")
		}
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d want 503", w.Code)
		}
		if !strings.Contains(w.Body.String(), "nodeb") {
			t.Fatalf("error should name the unreachable node: %s", w.Body.String())
		}
	}
}

// TestForwardUsesLocalProxyHandlerWhenPresent covers the case that made a wrist
// paired with the CENTER fail: the center has no center_url (it *is* the center),
// so its center bridge is disabled and the old code answered 503 even though the
// target node was connected to the center and reachable in-process. The
// instance's own node proxy handler already implements that decision, so it must
// be preferred over the bridge.
func TestForwardUsesLocalProxyHandlerWhenPresent(t *testing.T) {
	holder := NewWatchRelayHolder()
	var gotPath, gotScopes, gotQuery, gotAuth string
	holder.Set(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotScopes = r.Header.Get(WatchScopesHeader)
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))

	forwarder := &watchProxyForwarder{
		local:    holder,
		bridge:   NewCenterBridge("", ""), // center-shaped: no bridge
		webToken: func() string { return "center-web-token" },
		selfID:   "node-self",
	}

	r := httptest.NewRequest(http.MethodGet, "/watch/sessions?node_id=mac&limit=5", nil)
	r.Header.Set("Authorization", "Bearer wch_watchcredential")
	w := httptest.NewRecorder()
	if !forwarder.forward(w, r, []string{watchauth.ScopeRead}) {
		t.Fatal("remote target should be forwarded via the local proxy handler")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (must not answer 503 when a proxy handler exists)", w.Code)
	}
	if want := "/control/nodes/mac/proxy/watch/sessions"; gotPath != want {
		t.Fatalf("path=%q want %q", gotPath, want)
	}
	if strings.Contains(gotQuery, watchNodeQuery) {
		t.Fatalf("node selector leaked: %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "limit=5") {
		t.Fatalf("unrelated query dropped: %q", gotQuery)
	}
	if gotScopes != watchauth.ScopeRead {
		t.Fatalf("%s=%q want %q", WatchScopesHeader, gotScopes, watchauth.ScopeRead)
	}
	// The proxy handler guards itself with the web token; the watch credential is
	// meaningless there and must be replaced, not passed through.
	if gotAuth != "Bearer center-web-token" {
		t.Fatalf("Authorization=%q want the web token", gotAuth)
	}
}

// TestForwardServesLocallyForOwnNodeID pins the loop guard: a client that sends
// this node's real id (instead of the "local" sentinel) must be served locally.
// Forwarding it would re-enter the same API handler through the proxy forever.
func TestForwardServesLocallyForOwnNodeID(t *testing.T) {
	forwarder := &watchProxyForwarder{
		local:  NewWatchRelayHolder(),
		bridge: NewCenterBridge("http://center.invalid", "token"),
		selfID: "node-self",
	}
	r := httptest.NewRequest(http.MethodGet, "/watch/sessions?node_id=node-self", nil)
	if forwarder.forward(httptest.NewRecorder(), r, nil) {
		t.Fatal("a request naming this node must be served locally, not forwarded")
	}
}
