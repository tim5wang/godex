package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/watchauth"
)

// stubWatchTTS records what was asked to be spoken, so the tests can assert on the
// text rather than only on the HTTP envelope. Synthesis itself is voice-engine's
// concern; what this route decides is WHICH text gets spoken.
type stubWatchTTS struct {
	got []string
	err error
	wav []byte
}

func (s *stubWatchTTS) Synthesize(_ context.Context, text string) ([]byte, error) {
	s.got = append(s.got, text)
	if s.err != nil {
		return nil, s.err
	}
	if s.wav != nil {
		return s.wav, nil
	}
	// A minimal valid WAV, so the response is a real audio payload.
	return buildWAV(false, []byte{1, 2, 3, 4}, -1), nil
}

// newWatchTTSTestServer wires the real handler with an injected synthesiser, so the
// route is exercised without a running voice-engine.
func newWatchTTSTestServer(t *testing.T, tts watchTTSProvider) (*httptest.Server, *backend.Service, string) {
	t.Helper()
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("已重构 auth 中间件，测试通过。")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))

	auth, err := watchauth.New(filepath.Join(cfg.StateDir, "watch", "devices.json"))
	if err != nil {
		t.Fatalf("watchauth.New: %v", err)
	}
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:    manager,
		Backend:   service,
		WatchAuth: auth,
		WatchTTS:  tts,
	}))
	t.Cleanup(server.Close)
	return server, service, pairWatch(t, server, "")
}

func postWatchTTSText(t *testing.T, url, token, text, sessionID string) (*http.Response, []byte) {
	t.Helper()
	body := map[string]any{}
	if text != "" {
		body["text"] = text
	}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	return postWatchJSON(t, url+"/watch/tts", body, token)
}

// TestWatchTTSSpeaksExplicitText is the basic contract: text in, WAV out.
func TestWatchTTSSpeaksExplicitText(t *testing.T) {
	tts := &stubWatchTTS{}
	server, _, token := newWatchTTSTestServer(t, tts)

	resp, body := postWatchTTSText(t, server.URL, token, "你好，请汇报进度", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tts status=%d body=%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("content-type = %q, want audio/wav", ct)
	}
	if len(body) < 4 || string(body[:4]) != "RIFF" {
		t.Fatalf("expected a RIFF payload, got %q", string(body[:min(12, len(body))]))
	}
	if len(tts.got) != 1 || tts.got[0] != "你好，请汇报进度" {
		t.Fatalf("synthesiser received %q", tts.got)
	}
}

// TestWatchTTSSpeaksSessionSummary covers the common wrist case ("hear what it just
// said"): no text is sent, and the node speaks the session's latest reply.
func TestWatchTTSSpeaksSessionSummary(t *testing.T) {
	tts := &stubWatchTTS{}
	server, service, token := newWatchTTSTestServer(t, tts)

	// Drive a real turn so the session holds an assistant reply.
	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "tts-summary"})
	msgResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]interface{}{
		"envelope": map[string]interface{}{
			"source": "web", "sender": "tester",
			"text": "重构 auth", "content": "重构 auth",
		},
	})
	if msgResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(msgResp.Body)
		t.Fatalf("message status=%d body=%s", msgResp.StatusCode, string(body))
	}
	msgResp.Body.Close()
	waitForHTTPSnapshot(t, service, opened.SessionID, func(s backend.Snapshot) bool {
		return !s.Running && len(s.Messages) > 0
	})
	// Drain before TempDir cleanup: the session store keeps writing checkpoints,
	// which otherwise races t.TempDir() removal (the documented flake here).
	t.Cleanup(func() {
		waitForHTTPSnapshot(t, service, opened.SessionID, func(s backend.Snapshot) bool { return !s.Running })
	})

	resp, body := postWatchTTSText(t, server.URL, token, "", opened.SessionID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tts(session) status=%d body=%s", resp.StatusCode, body)
	}
	if len(tts.got) != 1 {
		t.Fatalf("expected exactly one synthesis, got %q", tts.got)
	}
	spoken := tts.got[0]
	if spoken == "" {
		t.Fatal("nothing was spoken for a session that has a reply")
	}
	// It must speak the ASSISTANT's reply, not the user's instruction.
	if spoken == "重构 auth" {
		t.Fatalf("spoke the user's instruction instead of the reply: %q", spoken)
	}
	if len([]rune(spoken)) > maxWatchReplyRunes+1 {
		t.Fatalf("spoken text is not wrist-shortened: %d runes", len([]rune(spoken)))
	}
}

// TestWatchTTSRejectsMissingInput keeps the error honest: with neither field there
// is nothing to speak, and inventing silence would be worse than a 400.
func TestWatchTTSRejectsMissingInput(t *testing.T) {
	tts := &stubWatchTTS{}
	server, _, token := newWatchTTSTestServer(t, tts)

	resp, body := postWatchTTSText(t, server.URL, token, "", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (body=%s)", resp.StatusCode, body)
	}
	if len(tts.got) != 0 {
		t.Fatalf("synthesiser must not be called: %q", tts.got)
	}
}

// TestWatchTTSUnknownSessionIs404 mirrors the other session routes.
func TestWatchTTSUnknownSessionIs404(t *testing.T) {
	tts := &stubWatchTTS{}
	server, _, token := newWatchTTSTestServer(t, tts)

	resp, body := postWatchTTSText(t, server.URL, token, "", "sess-does-not-exist")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 (body=%s)", resp.StatusCode, body)
	}
	if len(tts.got) != 0 {
		t.Fatalf("synthesiser must not be called: %q", tts.got)
	}
}

// TestWatchTTSNoReplyIs404: a session that exists but has no reply must not produce
// an empty audio clip the watch would play as silence.
func TestWatchTTSNoReplyIs404(t *testing.T) {
	tts := &stubWatchTTS{}
	server, _, token := newWatchTTSTestServer(t, tts)

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "tts-empty"})
	resp, body := postWatchTTSText(t, server.URL, token, "", opened.SessionID)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 (body=%s)", resp.StatusCode, body)
	}
	if len(tts.got) != 0 {
		t.Fatalf("synthesiser must not be called: %q", tts.got)
	}
}

// TestWatchTTSUnavailableWhenVoiceDisabled: without a provider the route reports
// 503 rather than panicking, and the rest of the watch surface keeps working.
func TestWatchTTSUnavailableWhenVoiceDisabled(t *testing.T) {
	server, _, token := newWatchTTSTestServer(t, nil)

	resp, body := postWatchTTSText(t, server.URL, token, "hello", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503 (body=%s)", resp.StatusCode, body)
	}
	// Other watch routes are unaffected by voice being off.
	if sessionsResp, _ := getWatch(t, server.URL+"/watch/sessions", token); sessionsResp.StatusCode != http.StatusOK {
		t.Fatalf("sessions should still work, got %d", sessionsResp.StatusCode)
	}
}

// TestWatchTTSRequiresCommandScope: synthesis burns CPU on caller-supplied text, so
// a read-only device must not reach it (it can read reply_summary instead).
func TestWatchTTSRequiresCommandScope(t *testing.T) {
	tts := &stubWatchTTS{}
	server, _, _ := newWatchTTSTestServer(t, tts)

	// Pair a second device with read only.
	resp, body := postWatchJSON(t, server.URL+"/watch/pair/start", map[string]any{"name": "RO"}, "")
	var pairing struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	if err := json.Unmarshal(body, &pairing); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	postWatchJSON(t, server.URL+"/watch/pair/approve", map[string]any{
		"user_code": pairing.UserCode, "scopes": []string{"watch:read"},
	}, "")
	if resp, body = postWatchJSON(t, server.URL+"/watch/pair/token",
		map[string]any{"device_code": pairing.DeviceCode}, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("pair/token: %d %s", resp.StatusCode, body)
	}
	var approved struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &approved); err != nil {
		t.Fatalf("decode token: %v", err)
	}

	resp, body = postWatchTTSText(t, server.URL, approved.Token, "帮我念一段", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-only watch must not synthesise: status=%d body=%s", resp.StatusCode, body)
	}
	if len(tts.got) != 0 {
		t.Fatalf("synthesiser must not be called: %q", tts.got)
	}
}

// TestWatchTTSEngineFailureIsBadGateway: an unreachable engine is an upstream
// failure (502), not a client error — the request itself was well formed.
func TestWatchTTSEngineFailureIsBadGateway(t *testing.T) {
	tts := &stubWatchTTS{err: errors.New("voice engine unreachable")}
	server, _, token := newWatchTTSTestServer(t, tts)

	resp, body := postWatchTTSText(t, server.URL, token, "你好", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502 (body=%s)", resp.StatusCode, body)
	}
}
