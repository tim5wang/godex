package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/watchauth"
)

// newWatchASRRouteServer wires the REAL transcription bridge with voice enabled and
// no reachable engine.
//
// The real bridge is used rather than a stub because the behaviour under test is the
// route's error MAPPING, and that depends on the sentinel the real bridge returns
// for a caller-side rejection. A stub returning the sentinel would test the mapping
// but not that the real code path actually produces it.
//
// GODEX_VOICE_ENGINE_ADDR points at a closed port so the engine-dependent branch
// fails deterministically instead of reaching whatever happens to be running on the
// developer's machine.
func newWatchASRRouteServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	t.Setenv("GODEX_VOICE_ENGINE_ADDR", "127.0.0.1:1")

	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	if _, err := manager.Update(context.Background(), config.UpdateRequest{
		Values: map[string]any{"media.audio.voice_enabled": true},
	}); err != nil {
		t.Fatalf("enable voice: %v", err)
	}
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
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
		WatchASR:  newWatchASRBridge(manager),
	}))
	t.Cleanup(server.Close)
	return server, pairWatch(t, server, "")
}

// postWatchAudio uploads one audio part with a chosen content type.
func postWatchAudio(t *testing.T, url, token string, audio []byte, mime string) (*http.Response, []byte) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="audio"; filename="clip.bin"`},
		"Content-Type":        {mime},
	})
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(audio); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, url+"/watch/asr", &body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

// TestWatchASRUnsupportedTypeIsBadRequest covers a defect the end-to-end run found:
// an unsupported container was answered with 502, which tells the watch the node or
// engine is broken and invites a retry — but the retry would re-send the same
// unsupported bytes forever. The format is the caller's mistake, so it is a 400.
func TestWatchASRUnsupportedTypeIsBadRequest(t *testing.T) {
	server, token := newWatchASRRouteServer(t)

	resp, body := postWatchAudio(t, server.URL, token, bytes.Repeat([]byte{0x21}, 512), "audio/mpeg")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported container: status=%d, want 400 (body=%s)", resp.StatusCode, body)
	}
	// The message must say what to send instead, since the caller has to change.
	if !bytes.Contains(body, []byte("audio/wav")) {
		t.Fatalf("error should name the accepted types, got %s", body)
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Error == "" {
		t.Fatalf("expected a JSON error payload, got %s", body)
	}
}

// TestWatchASREngineFailureIsBadGateway pins the other half of the mapping: a
// well-formed request that fails because the engine is unreachable IS an upstream
// failure, and must stay 502. Without this, "make unsupported types 400" could be
// over-applied into "everything is 400", which would hide a down engine from the
// watch.
func TestWatchASREngineFailureIsBadGateway(t *testing.T) {
	server, token := newWatchASRRouteServer(t)

	// A valid WAV passes format validation, so the request proceeds to the engine —
	// which is not listening on the configured port.
	resp, body := postWatchAudio(t, server.URL, token, buildWAV(false, bytes.Repeat([]byte{1, 2}, 160), -1), "audio/wav")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("unreachable engine: status=%d, want 502 (body=%s)", resp.StatusCode, body)
	}
}

// TestWatchASREmptyAudioIsBadRequest keeps the empty-buffer guard covered at the
// route level (it was previously only reachable through the bridge).
func TestWatchASREmptyAudioIsBadRequest(t *testing.T) {
	server, token := newWatchASRRouteServer(t)

	resp, body := postWatchAudio(t, server.URL, token, nil, "audio/wav")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty audio: status=%d, want 400 (body=%s)", resp.StatusCode, body)
	}
}
