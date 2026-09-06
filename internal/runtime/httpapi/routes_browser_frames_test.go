package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/tools"
)

// fakeFrameProvider implements browserFrameProvider for endpoint tests.
type fakeFrameProvider struct {
	pages       []tools.BrowserPage
	allowed     map[string]bool // "sessionID/pageID" -> allowed
	frames      []tools.BrowserFrame
	subscribed  chan string
	validateErr error
}

func (f *fakeFrameProvider) ListPages(sessionID string) []tools.BrowserPage {
	out := make([]tools.BrowserPage, 0, len(f.pages))
	for _, p := range f.pages {
		if p.SessionID == sessionID {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeFrameProvider) ValidateFramePage(sessionID, pageID string) error {
	if f.validateErr != nil {
		return f.validateErr
	}
	if f.allowed != nil && f.allowed[sessionID+"/"+pageID] {
		return nil
	}
	if f.allowed == nil && len(f.pages) > 0 {
		for _, p := range f.pages {
			if p.SessionID == sessionID && p.PageID == pageID {
				return nil
			}
		}
	}
	return &pageNotFoundError{msg: "no such page in session"}
}

func (f *fakeFrameProvider) SubscribeFrames(sessionID, pageID string) (<-chan tools.BrowserFrame, func()) {
	if f.subscribed != nil {
		select {
		case f.subscribed <- sessionID + "/" + pageID:
		default:
		}
	}
	ch := make(chan tools.BrowserFrame, 8)
	go func() {
		for _, fr := range f.frames {
			ch <- fr
		}
		close(ch)
	}()
	return ch, func() {}
}

type pageNotFoundError struct{ msg string }

func (e *pageNotFoundError) Error() string { return e.msg }

// newFramesTestServer wires the frames route into a mux with the fake provider
// and a web-token protected middleware, returning the server URL.
func newFramesTestServer(t *testing.T, provider browserFrameProvider, token string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	registerBrowserFrameRoutesWithProvider(mux, func() browserFrameProvider {
		if provider == nil {
			return nil
		}
		return provider
	}, withBearerAuthProvider(func() string { return token }), func() string { return token })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func dialFrames(t *testing.T, srv *httptest.Server, query, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/browser/frames?" + query
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	return websocket.DefaultDialer.Dial(url, header)
}

func TestBrowserFramesWSRejectsUnauthorized(t *testing.T) {
	provider := &fakeFrameProvider{
		allowed: map[string]bool{"session-1/p1": true},
	}
	srv := newFramesTestServer(t, provider, "secret-token")

	// No token at all.
	conn, resp, err := dialFrames(t, srv, "session=session-1&page=p1", "")
	if err == nil {
		conn.Close()
		t.Fatalf("expected WS handshake to be rejected without token")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	// Wrong token.
	conn, resp, err = dialFrames(t, srv, "session=session-1&page=p1", "wrong")
	if err == nil {
		conn.Close()
		t.Fatalf("expected WS handshake to be rejected with wrong token")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", resp.StatusCode)
	}

	// Query-token auth is allowed (browser WS cannot set headers).
	conn, _, err = dialFrames(t, srv, "session=session-1&page=p1&token=secret-token", "")
	if err != nil {
		t.Fatalf("expected query-token auth to succeed: %v", err)
	}
	conn.Close()
}

func TestBrowserFramesWSRejectsMissingSession(t *testing.T) {
	provider := &fakeFrameProvider{}
	srv := newFramesTestServer(t, provider, "secret-token")

	conn, resp, err := dialFrames(t, srv, "page=p1", "secret-token")
	if err == nil {
		conn.Close()
		t.Fatalf("expected handshake rejection for missing session")
	}
	if resp != nil && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing session, got %d", resp.StatusCode)
	}
}

func TestBrowserFramesWSSessionIsolation(t *testing.T) {
	// The page lives in session A only; requesting it under session B must be
	// rejected with 403 before the WS upgrade.
	provider := &fakeFrameProvider{
		pages: []tools.BrowserPage{
			{PageID: "p1", SessionID: "session-A", URL: "https://a.example.com"},
		},
	}
	srv := newFramesTestServer(t, provider, "secret-token")

	// Owning session works.
	conn, resp, err := dialFrames(t, srv, "session=session-A&page=p1", "secret-token")
	if err != nil {
		t.Fatalf("owning session should upgrade: %v", err)
	}
	conn.Close()
	_ = resp

	// Cross-session access is rejected.
	conn, resp, err = dialFrames(t, srv, "session=session-B&page=p1", "secret-token")
	if err == nil {
		conn.Close()
		t.Fatalf("expected cross-session access to be rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-session access, got %d", resp.StatusCode)
	}
}

func TestBrowserFramesWSStreamsFrames(t *testing.T) {
	provider := &fakeFrameProvider{
		allowed: map[string]bool{"session-1/p1": true},
		frames: []tools.BrowserFrame{
			{PageID: "p1", URL: "https://example.com", Title: "Example", JPEG: []byte("jpeg-bytes-1")},
			{PageID: "p1", URL: "https://example.com", Title: "Example", JPEG: []byte("jpeg-bytes-2")},
		},
	}
	srv := newFramesTestServer(t, provider, "secret-token")

	conn, _, err := dialFrames(t, srv, "session=session-1&page=p1", "secret-token")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var frame tools.BrowserFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if frame.PageID != "p1" || string(frame.JPEG) != "jpeg-bytes-1" {
		t.Fatalf("unexpected frame: %+v", frame)
	}
}

func TestBrowserFramesWSDefaultsToLatestPage(t *testing.T) {
	provider := &fakeFrameProvider{
		pages: []tools.BrowserPage{
			{PageID: "old", SessionID: "session-1"},
			{PageID: "latest", SessionID: "session-1"},
		},
		frames: []tools.BrowserFrame{
			{PageID: "latest", JPEG: []byte("jpeg")},
		},
	}
	srv := newFramesTestServer(t, provider, "secret-token")

	conn, _, err := dialFrames(t, srv, "session=session-1", "secret-token")
	if err != nil {
		t.Fatalf("dial without page should default to latest page: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var frame tools.BrowserFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if frame.PageID != "latest" {
		t.Fatalf("expected latest page, got %+v", frame)
	}
}

// TestBrowserFramesRouteRegisteredInHandler verifies the frames route is
// mounted on the real handler and the JSON error shape is as expected.
func TestBrowserFramesRouteRegisteredInHandler(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	// Authorized request against a session with no browser pages. The route is
	// registered, so the handler itself runs: it reaches the page lookup and
	// returns 404 with a JSON error body (a route miss would return Go's
	// plain "404 page not found" text instead).
	req, err := http.NewRequest(http.MethodGet, server.URL+"/browser/frames?session=no-such-session", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get frames (http probe): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for session without pages, got %d", resp.StatusCode)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body["error"] == nil {
		t.Fatalf("expected JSON error body proving the route is registered, got %s", mustJSON(body))
	}
}
