package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/watchauth"
)

// newWatchTestServer wires the real handler with an explicit watch registry so
// tests can drive the pairing handshake end to end.
func newWatchTestServer(t *testing.T) (*httptest.Server, *watchauth.Service, string) {
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
		Config:    manager,
		Backend:   service,
		WatchAuth: auth,
	})
	// newTestConfig leaves WebToken empty, which makes the auth middleware a
	// pass-through; that is what lets the pairing-approval calls run here.
	token := ""
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, auth, token
}

func postWatchJSON(t *testing.T, url string, body any, token string) (*http.Response, []byte) {
	t.Helper()
	var payload []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		payload = data
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, buf.Bytes()
}

func getWatch(t *testing.T, url, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, buf.Bytes()
}

// pairWatch walks start → approve → token and returns the device credential.
func pairWatch(t *testing.T, server *httptest.Server, webToken string) string {
	t.Helper()
	resp, body := postWatchJSON(t, server.URL+"/watch/pair/start", map[string]any{
		"name": "Pixel Watch", "kind": "watch",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair/start status=%d body=%s", resp.StatusCode, body)
	}
	var pairing struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	if err := json.Unmarshal(body, &pairing); err != nil {
		t.Fatalf("decode pairing: %v (%s)", err, body)
	}
	if pairing.DeviceCode == "" || pairing.UserCode == "" {
		t.Fatalf("pairing missing codes: %s", body)
	}

	// Polling before approval must report pending, not an error.
	resp, body = postWatchJSON(t, server.URL+"/watch/pair/token", map[string]any{
		"device_code": pairing.DeviceCode,
	}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair/token(pending) status=%d body=%s", resp.StatusCode, body)
	}
	var pending struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &pending); err != nil {
		t.Fatalf("decode pending: %v", err)
	}
	if pending.Status != "pending" {
		t.Fatalf("expected pending, got %q", pending.Status)
	}

	resp, body = postWatchJSON(t, server.URL+"/watch/pair/approve", map[string]any{
		"user_code": pairing.UserCode,
	}, webToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair/approve status=%d body=%s", resp.StatusCode, body)
	}

	resp, body = postWatchJSON(t, server.URL+"/watch/pair/token", map[string]any{
		"device_code": pairing.DeviceCode,
	}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair/token status=%d body=%s", resp.StatusCode, body)
	}
	var approved struct {
		Status string `json:"status"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(body, &approved); err != nil {
		t.Fatalf("decode approved: %v", err)
	}
	if approved.Status != "approved" || approved.Token == "" {
		t.Fatalf("expected approved with token, got %s", body)
	}
	return approved.Token
}

func TestWatchPairingHandshakeIssuesWorkingToken(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	// The issued credential must open a scoped route.
	resp, body := getWatch(t, server.URL+"/watch/sessions", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("watch/sessions status=%d body=%s", resp.StatusCode, body)
	}
}

func TestWatchRoutesRejectMissingCredential(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	for _, path := range []string{"/watch/sessions", "/watch/nodes", "/watch/approvals"} {
		resp, _ := getWatch(t, server.URL+path, "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without credential: status=%d, want 401", path, resp.StatusCode)
		}
	}
}

func TestWatchRoutesRejectUnknownCredential(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	resp, _ := getWatch(t, server.URL+"/watch/sessions", watchauth.TokenPrefix+"not-a-real-token")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown credential: status=%d, want 401", resp.StatusCode)
	}
}

func TestWatchScopeIsEnforcedPerRoute(t *testing.T) {
	server, _, _ := newWatchTestServer(t)

	// Pair with read only, then confirm the command surface is closed off.
	resp, body := postWatchJSON(t, server.URL+"/watch/pair/start", map[string]any{"name": "ReadOnly"}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair/start: %d %s", resp.StatusCode, body)
	}
	var pairing struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	if err := json.Unmarshal(body, &pairing); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp, body = postWatchJSON(t, server.URL+"/watch/pair/approve", map[string]any{
		"user_code": pairing.UserCode,
		"scopes":    []string{"watch:read"},
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair/approve(read-only): %d %s", resp.StatusCode, body)
	}
	resp, body = postWatchJSON(t, server.URL+"/watch/pair/token", map[string]any{"device_code": pairing.DeviceCode}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair/token: %d %s", resp.StatusCode, body)
	}
	var approved struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &approved); err != nil {
		t.Fatalf("decode approved: %v", err)
	}

	// read is allowed
	if resp, _ := getWatch(t, server.URL+"/watch/sessions", approved.Token); resp.StatusCode != http.StatusOK {
		t.Fatalf("read-only token should read sessions, got %d", resp.StatusCode)
	}
	// command is not
	resp, body = postWatchJSON(t, server.URL+"/watch/command", map[string]any{
		"session_id": "s1", "text": "hello",
	}, approved.Token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-only token must not command: status=%d body=%s", resp.StatusCode, body)
	}
}

func TestWatchCommandRequiresSessionAndText(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	resp, _ := postWatchJSON(t, server.URL+"/watch/command", map[string]any{"text": "hi"}, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing session_id: status=%d, want 400", resp.StatusCode)
	}
	resp, _ = postWatchJSON(t, server.URL+"/watch/command", map[string]any{"session_id": "s1"}, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing text: status=%d, want 400", resp.StatusCode)
	}
}

func TestWatchCommandRejectsUnknownSession(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	resp, body := postWatchJSON(t, server.URL+"/watch/command", map[string]any{
		"session_id": "sess-does-not-exist", "text": "hello",
	}, token)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session: status=%d body=%s (want 404)", resp.StatusCode, body)
	}
}

func TestWatchApprovalsListIsEmptyNotNil(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	resp, body := getWatch(t, server.URL+"/watch/approvals", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approvals status=%d body=%s", resp.StatusCode, body)
	}
	// A JSON `null` would force the watch client to null-check; an empty array
	// keeps the rendering path unconditional.
	if trimmed := string(bytes.TrimSpace(body)); trimmed != "[]" {
		t.Fatalf("expected empty JSON array, got %q", trimmed)
	}
}

func TestWatchDevicesListShowsPairedDevice(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	resp, body := getWatch(t, server.URL+"/watch/devices", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("devices status=%d body=%s", resp.StatusCode, body)
	}
	var devices []watchauth.Device
	if err := json.Unmarshal(body, &devices); err != nil {
		t.Fatalf("decode devices: %v (%s)", err, body)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 paired device, got %d (%s)", len(devices), body)
	}
	if devices[0].Name != "Pixel Watch" {
		t.Fatalf("unexpected device: %+v", devices[0])
	}
}

func TestWatchRevokeStopsTokenWorking(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	// Find the device id via the management surface.
	_, body := getWatch(t, server.URL+"/watch/devices", token)
	var devices []watchauth.Device
	if err := json.Unmarshal(body, &devices); err != nil {
		t.Fatalf("decode devices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	deviceID := devices[0].DeviceID

	req, err := http.NewRequest(http.MethodDelete, server.URL+"/watch/devices/"+deviceID, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d, want 200", resp.StatusCode)
	}

	// The revoked credential must be refused immediately.
	if resp, _ := getWatch(t, server.URL+"/watch/sessions", token); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token should be 401, got %d", resp.StatusCode)
	}
}

func TestWatchPairApproveRejectsBadCode(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	resp, _ := postWatchJSON(t, server.URL+"/watch/pair/approve", map[string]any{"user_code": "00000000"}, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad user code: status=%d, want 400", resp.StatusCode)
	}
}

func TestWatchPairApproveRejectsMissingCode(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	resp, body := postWatchJSON(t, server.URL+"/watch/pair/approve", map[string]any{}, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing user code: status=%d body=%s, want 400", resp.StatusCode, body)
	}
}

func TestWatchASRReportsUnavailableWhenVoiceDisabled(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	// Voice is disabled in the test config, so the bridge must not be wired and
	// the endpoint must fail loudly rather than silently returning empty text.
	req, err := http.NewRequest(http.MethodPost, server.URL+"/watch/asr", bytes.NewReader([]byte("not-audio")))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("asr with voice disabled: status=%d, want 503", resp.StatusCode)
	}
}

func TestWatchSessionDetailUnknownSessionIs404(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")
	resp, body := getWatch(t, server.URL+"/watch/sessions/nope", token)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session detail: status=%d body=%s, want 404", resp.StatusCode, body)
	}
}

func TestWatchSessionsLimitIsBounded(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	// An absurd limit must be clamped rather than honoured (the watch has a
	// tiny screen; unbounded lists are always a client bug).
	resp, body := getWatch(t, server.URL+"/watch/sessions?limit=100000", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sessions status=%d body=%s", resp.StatusCode, body)
	}
	var items []watchSessionItem
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("decode sessions: %v (%s)", err, body)
	}
	if len(items) > 100 {
		t.Fatalf("limit was not clamped: got %d items", len(items))
	}
}

func TestWatchCrossDeviceIsAlwaysAnArray(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	// The watch renders this directly; a null would crash the client parser,
	// so an empty result must still be [].
	resp, body := getWatch(t, server.URL+"/watch/cross-device", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cross-device status=%d body=%s", resp.StatusCode, body)
	}
	var items []watchCrossDeviceItem
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("decode cross-device: %v (%s)", err, body)
	}
	if items == nil {
		t.Fatalf("cross-device must be [] not null, got %s", body)
	}
}

func TestWatchCrossDeviceRequiresReadScope(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	resp, _ := getWatch(t, server.URL+"/watch/cross-device", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-device without credential: status=%d, want 401", resp.StatusCode)
	}
}

func TestWatchStopNeedsCommandScope(t *testing.T) {
	server, _, _ := newWatchTestServer(t)

	// A read-only credential must not be able to abort a running turn.
	resp, body := postWatchJSON(t, server.URL+"/watch/pair/start", map[string]any{"name": "ReadOnly"}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair/start: %d %s", resp.StatusCode, body)
	}
	var pairing struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	if err := json.Unmarshal(body, &pairing); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp, body := postWatchJSON(t, server.URL+"/watch/pair/approve", map[string]any{
		"user_code": pairing.UserCode,
		"scopes":    []string{"watch:read"},
	}, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair/approve: %d %s", resp.StatusCode, body)
	}
	resp, body = postWatchJSON(t, server.URL+"/watch/pair/token", map[string]any{"device_code": pairing.DeviceCode}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair/token: %d %s", resp.StatusCode, body)
	}
	var approved struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &approved); err != nil {
		t.Fatalf("decode approved: %v", err)
	}

	resp, body = postWatchJSON(t, server.URL+"/watch/sessions/s1/stop", map[string]any{}, approved.Token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-only stop: status=%d body=%s, want 403", resp.StatusCode, body)
	}

	// With the full scope set the request reaches the service. Stopping an
	// unknown session must be a 404, which proves it was routed and authorised
	// rather than rejected by the credential check.
	full := pairWatch(t, server, "")
	resp, body = postWatchJSON(t, server.URL+"/watch/sessions/sess-nope/stop", map[string]any{}, full)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stop unknown session: status=%d body=%s, want 404", resp.StatusCode, body)
	}
}

func TestWatchSessionDetailProjectsStepsAndProgress(t *testing.T) {
	// The step projection is pure, so it is tested directly: fabricating a full
	// backend snapshot with todos is far more brittle than calling the helper.
	steps := watchSteps([]todo.Item{
		{ID: 1, Content: "读取配置", Status: todo.StatusCompleted},
		{ID: 2, Content: "重构中间件", ActiveForm: "正在重构中间件", Status: todo.StatusInProgress},
		{ID: 3, Content: "跑测试", Status: todo.StatusPending},
		{ID: 4, Content: "已删步骤", Status: todo.StatusDeleted},
		{ID: 5, Content: "   ", Status: todo.StatusPending},
	})
	if len(steps) != 3 {
		t.Fatalf("deleted/blank steps must be dropped, got %d: %+v", len(steps), steps)
	}
	// The in-progress step shows the present-tense form, which is what the agent
	// is doing right now.
	if steps[1].Content != "正在重构中间件" {
		t.Fatalf("in-progress step should use ActiveForm, got %q", steps[1].Content)
	}
	done := 0
	for _, s := range steps {
		if s.Status == string(todo.StatusCompleted) {
			done++
		}
	}
	if done != 1 {
		t.Fatalf("completed count = %d, want 1", done)
	}
}

// TestRecentToolEventsFoldsStartAndFinish covers the data behind mockup 05's tool
// log. Started and finished events for one call must collapse into a single entry
// carrying the outcome, and a started event with no finish must be reported as
// still pending — otherwise the wrist would show a finished tool as running, or
// the same call twice with only half the information each time.
func TestRecentToolEventsFoldsStartAndFinish(t *testing.T) {
	timeline := []events.Event{
		{Type: events.EventToolCallStarted, Payload: events.ToolCallPayload{Name: "read_file"}},
		{Type: events.EventToolCallFinished, Payload: events.ToolCallPayload{Name: "read_file", DurationMS: 12}},
		{Type: events.EventToolCallStarted, Payload: events.ToolCallPayload{Name: "bash"}},
		{Type: events.EventToolCallFinished, Payload: events.ToolCallPayload{Name: "bash", Error: "exit 1", DurationMS: 800}},
		{Type: events.EventToolCallStarted, Payload: events.ToolCallPayload{Name: "edit_file"}},
	}

	got := recentToolEvents(timeline, maxWatchToolEvents)
	if len(got) != 3 {
		t.Fatalf("want 3 folded entries, got %d: %+v", len(got), got)
	}

	if got[0].Name != "read_file" || got[0].Failed || got[0].Pending || got[0].DurationMS != 12 {
		t.Fatalf("read_file entry wrong: %+v", got[0])
	}
	if got[1].Name != "bash" || !got[1].Failed || got[1].Pending {
		t.Fatalf("failed bash entry wrong: %+v", got[1])
	}
	if got[2].Name != "edit_file" || !got[2].Pending {
		t.Fatalf("edit_file should be pending: %+v", got[2])
	}
}

// TestRecentToolEventsKeepsMostRecentAndHandlesLoneFinish pins the two bounds a
// wrist depends on: the list is capped to the newest entries (a long turn can hold
// hundreds of calls) and a finish event without a recorded start is still shown
// rather than quietly dropped.
func TestRecentToolEventsKeepsMostRecentAndHandlesLoneFinish(t *testing.T) {
	timeline := []events.Event{{Type: events.EventToolCallFinished, Payload: events.ToolCallPayload{Name: "orphan", DurationMS: 5}}}
	for _, name := range []string{"t1", "t2", "t3", "t4", "t5", "t6"} {
		timeline = append(timeline, events.Event{Type: events.EventToolCallStarted, Payload: events.ToolCallPayload{Name: name}})
	}

	got := recentToolEvents(timeline, 3)
	if len(got) != 3 {
		t.Fatalf("want 3 entries after limit, got %d", len(got))
	}
	// The newest three started calls survive; the older ones and the orphan fall off.
	for i, want := range []string{"t4", "t5", "t6"} {
		if got[i].Name != want {
			t.Fatalf("entry %d = %q, want %q", i, got[i].Name, want)
		}
	}

	lone := recentToolEvents([]events.Event{
		{Type: events.EventToolCallFinished, Payload: events.ToolCallPayload{Name: "orphan", DurationMS: 5}},
	}, maxWatchToolEvents)
	if len(lone) != 1 || lone[0].Name != "orphan" || lone[0].Pending {
		t.Fatalf("lone finish should be kept as settled: %+v", lone)
	}
}

func TestToolNameFromPayloadHandlesBothShapes(t *testing.T) {
	// In-process snapshots carry the struct; rehydrated ones carry a map.
	if got := toolNameFromPayload(events.ToolCallPayload{Name: "edit_file"}); got != "edit_file" {
		t.Fatalf("struct payload = %q", got)
	}
	if got := toolNameFromPayload(map[string]any{"name": "bash"}); got != "bash" {
		t.Fatalf("map payload = %q", got)
	}
	if got := toolNameFromPayload(map[string]any{"other": 1}); got != "" {
		t.Fatalf("unrelated payload should yield empty, got %q", got)
	}
}

// TestWatchApprovalExposesCommandAndRiskForDetailScreen covers the data contract
// behind mockup 06: the single-approval screen must show the untruncated command,
// the risk verdict and the node before the user authorises execution.
//
// It drives a real high-risk bash request through the real permission gate rather
// than hand-building the payload, so a change to either the gate or the
// aggregation shows up here.
func TestWatchApprovalExposesCommandAndRiskForDetailScreen(t *testing.T) {
	const command = "rm -rf ./build && npm run build"

	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": command})}},
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
	}))
	t.Cleanup(server.Close)
	token := pairWatch(t, server, "")

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "approval-detail"})
	msgResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]interface{}{
		"envelope": map[string]interface{}{
			"source":  "web",
			"sender":  cfg.LeadName,
			"text":    "build the project",
			"content": "build the project",
		},
	})
	if msgResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(msgResp.Body)
		t.Fatalf("message status=%d body=%s", msgResp.StatusCode, string(body))
	}
	msgResp.Body.Close()
	waitForHTTPSnapshot(t, service, opened.SessionID, func(s backend.Snapshot) bool {
		return len(s.PendingPermissions) > 0
	})

	resp, body := getWatch(t, server.URL+"/watch/approvals", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approvals status=%d body=%s", resp.StatusCode, body)
	}
	var items []watchApprovalItem
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("decode approvals: %v (%s)", err, body)
	}
	if len(items) != 1 {
		t.Fatalf("expected one pending approval, got %d: %s", len(items), body)
	}

	got := items[0]
	if got.Kind != "permission" {
		t.Fatalf("kind = %q, want permission", got.Kind)
	}
	if got.SessionID != opened.SessionID {
		t.Fatalf("session_id = %q, want %q", got.SessionID, opened.SessionID)
	}
	// The detail screen authorises execution, so the full command must survive
	// aggregation; the 140-rune list summary is not enough to decide on.
	if got.Command != command {
		t.Fatalf("command = %q, want %q", got.Command, command)
	}
	if got.RiskLevel != "high" {
		t.Fatalf("risk_level = %q, want high (rm -rf is classified high)", got.RiskLevel)
	}
	if !strings.HasPrefix(got.Risk, "high risk") {
		t.Fatalf("risk = %q, want a high-risk verdict from the shared classifier", got.Risk)
	}
	if got.NodeName == "" {
		t.Fatalf("node_name is empty; the screen must say where the command will run")
	}
	if got.ToolName != "bash" {
		t.Fatalf("tool_name = %q, want bash", got.ToolName)
	}

	// Drain the turn before returning. The stub caller finishes its second response
	// asynchronously and the session store keeps writing checkpoints into
	// cfg.StateDir; without this wait those writes race t.TempDir()'s cleanup and the
	// test fails intermittently with "RemoveAll cleanup: directory not empty" (seen in
	// ~4 of 5 isolated runs). Waiting for the turn to end synchronises them, which is
	// how the rest of this package does it.
	waitForHTTPSnapshot(t, service, opened.SessionID, func(s backend.Snapshot) bool {
		return !s.Running
	})
}

// TestWatchNodesCarryKind covers the wrist-side half of W1: the point of storing a
// node kind is that a client can label a target, so the field has to survive the
// registry → /watch/nodes round trip. A registry-level test alone would not catch a
// handler that builds its response item field-by-field and forgets to copy it.
func TestWatchNodesCarryKind(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))

	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	// One node of each kind, plus one that declares nothing (defaulted).
	for _, input := range []noderegistry.NodeInput{
		{ID: "node-pc", Name: "desktop", Kind: noderegistry.KindPC},
		{ID: "node-cloud", Name: "server", Kind: noderegistry.KindCloud},
		{ID: "node-phone", Name: "phone-bridge", Kind: noderegistry.KindPhone},
		{ID: "node-unspecified", Name: "legacy"},
	} {
		if _, err := registry.Register(context.Background(), input); err != nil {
			t.Fatalf("register %s: %v", input.ID, err)
		}
	}

	auth, err := watchauth.New(filepath.Join(cfg.StateDir, "watch", "devices.json"))
	if err != nil {
		t.Fatalf("watchauth.New: %v", err)
	}
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:          manager,
		Backend:         service,
		WatchAuth:       auth,
		ControlRegistry: registry,
	}))
	t.Cleanup(server.Close)
	token := pairWatch(t, server, "")

	resp, body := getWatch(t, server.URL+"/watch/nodes", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("nodes status=%d body=%s", resp.StatusCode, body)
	}
	var nodes []struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(body, &nodes); err != nil {
		t.Fatalf("decode nodes: %v body=%s", err, body)
	}
	if len(nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d (%s)", len(nodes), body)
	}
	want := map[string]string{
		"node-pc":          noderegistry.KindPC,
		"node-cloud":       noderegistry.KindCloud,
		"node-phone":       noderegistry.KindPhone,
		"node-unspecified": noderegistry.DefaultKind,
	}
	for _, node := range nodes {
		if want[node.ID] != node.Kind {
			t.Errorf("node %s kind = %q, want %q", node.ID, node.Kind, want[node.ID])
		}
	}
}

// TestWatchApprovalRiskLevelMapsEveryVerdict keeps the wrist label aligned with
// the shared risk classifier: an unrecognised verdict must not read as "safe".
func TestWatchApprovalRiskLevelMapsEveryVerdict(t *testing.T) {
	cases := map[string]string{
		"high risk: recursive deletion command": "high",
		"medium risk: shell execution":          "medium",
		"low risk: protected read":              "low",
		"something unexpected":                  "",
	}
	for summary, want := range cases {
		if got := watchRiskLevel(summary); got != want {
			t.Fatalf("watchRiskLevel(%q) = %q, want %q", summary, got, want)
		}
	}
}
