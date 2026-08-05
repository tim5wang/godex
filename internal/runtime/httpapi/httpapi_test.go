package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/core/memory"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/message"
	rtchannels "github.com/tim5wang/godex/internal/runtime/channels"
	"github.com/tim5wang/godex/internal/runtime/channels/weixin"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
	"github.com/tim5wang/godex/internal/services/usage"
	"github.com/tim5wang/godex/internal/tools"
)

type stubCaller struct {
	mu        sync.Mutex
	responses []protocol.Response
	calls     int
	started   chan struct{}
	block     chan struct{}
}

type stubServiceRuntime struct {
	status   map[string]any
	restarts int
}

func (s *stubServiceRuntime) Status(ctx context.Context) (any, error) {
	_ = ctx
	return s.status, nil
}

func (s *stubServiceRuntime) Restart(ctx context.Context) error {
	_ = ctx
	s.restarts++
	return nil
}

type stubWeixinAuth struct {
	status weixin.WebAuthStatus
	err    error
}

type stubCronAutomation struct {
	jobs []automation.CronJob
	runs map[string][]automation.CronRunLog
}

func (s *stubCronAutomation) ListJobs() ([]automation.CronJob, error) {
	return append([]automation.CronJob{}, s.jobs...), nil
}
func (s *stubCronAutomation) GetJob(id string) (automation.CronJob, error) {
	for _, job := range s.jobs {
		if job.ID == id {
			return job, nil
		}
	}
	return automation.CronJob{}, nil
}
func (s *stubCronAutomation) CreateJob(input automation.CronCreateInput) (automation.CronJob, error) {
	job := automation.CronJob{
		ID:             "job-created",
		Name:           input.Name,
		Message:        input.Message,
		Timezone:       input.Timezone,
		Schedule:       input.Schedule,
		SessionMode:    input.SessionMode,
		DeliveryTarget: input.DeliveryTarget,
		Enabled:        input.Enabled,
	}
	s.jobs = append(s.jobs, job)
	return job, nil
}
func (s *stubCronAutomation) UpdateJob(input automation.CronUpdateInput) (automation.CronJob, error) {
	job, _ := s.GetJob(input.ID)
	if input.Name != nil {
		job.Name = *input.Name
	}
	if input.Enabled != nil {
		job.Enabled = *input.Enabled
	}
	return job, nil
}
func (s *stubCronAutomation) DeleteJob(id string) error { return nil }
func (s *stubCronAutomation) ToggleJob(id string, enabled bool) (automation.CronJob, error) {
	job, _ := s.GetJob(id)
	job.Enabled = enabled
	return job, nil
}
func (s *stubCronAutomation) RunNow(ctx context.Context, id string) (automation.CronRunLog, error) {
	_ = ctx
	runs := s.runs[id]
	if len(runs) > 0 {
		return runs[0], nil
	}
	return automation.CronRunLog{ID: "run-1", JobID: id, Status: "completed"}, nil
}
func (s *stubCronAutomation) ListRunLogs(id string, limit int) ([]automation.CronRunLog, error) {
	_ = limit
	return append([]automation.CronRunLog{}, s.runs[id]...), nil
}

type stubHeartbeatAutomation struct {
	rule automation.HeartbeatRule
	runs []automation.HeartbeatRunLog
}

func (s stubHeartbeatAutomation) GetRule() (automation.HeartbeatRule, error) { return s.rule, nil }
func (s stubHeartbeatAutomation) SetRule(input automation.HeartbeatSetInput) (automation.HeartbeatRule, error) {
	rule := s.rule
	if input.Enabled != nil {
		rule.Enabled = *input.Enabled
	}
	if input.IntervalSeconds != nil {
		rule.IntervalSeconds = *input.IntervalSeconds
	}
	return rule, nil
}
func (s stubHeartbeatAutomation) Toggle(enabled bool) (automation.HeartbeatRule, error) {
	rule := s.rule
	rule.Enabled = enabled
	return rule, nil
}
func (s stubHeartbeatAutomation) TestNow(ctx context.Context) (automation.HeartbeatRunLog, error) {
	_ = ctx
	if len(s.runs) > 0 {
		return s.runs[0], nil
	}
	return automation.HeartbeatRunLog{ID: "hb-run-1", RuleID: "default", Status: "completed"}, nil
}
func (s stubHeartbeatAutomation) ListRunLogs(limit int) ([]automation.HeartbeatRunLog, error) {
	_ = limit
	return append([]automation.HeartbeatRunLog{}, s.runs...), nil
}

func (s stubWeixinAuth) Status(ctx context.Context, accountID string) (weixin.WebAuthStatus, error) {
	_ = ctx
	_ = accountID
	return s.status, s.err
}

func (s stubWeixinAuth) Start(ctx context.Context, accountID string) (weixin.WebAuthStatus, error) {
	_ = ctx
	_ = accountID
	return s.status, s.err
}

func (s stubWeixinAuth) Logout(ctx context.Context, accountID string) (weixin.WebAuthStatus, error) {
	_ = ctx
	_ = accountID
	return s.status, s.err
}

func (c *stubCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = req
	if c.started != nil {
		select {
		case c.started <- struct{}{}:
		default:
		}
	}
	if c.block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.block:
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.calls
	if idx >= len(c.responses) {
		idx = len(c.responses) - 1
	}
	resp := c.responses[idx]
	c.calls++
	return &resp, nil
}

func TestSessionEndpointsAndSSE(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSession(t, server.URL)

	getResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID)
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected snapshot status 200, got %d", getResp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/sessions/"+opened.SessionID+"/events", nil)
	if err != nil {
		t.Fatalf("new events request: %v", err)
	}
	eventResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open events stream: %v", err)
	}
	defer eventResp.Body.Close()

	eventLine := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(eventResp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") {
				eventLine <- strings.TrimSpace(strings.TrimPrefix(line, "data: "))
				return
			}
		}
	}()

	messageResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]string{"text": "hello"})
	if messageResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected message status 202, got %d", messageResp.StatusCode)
	}
	messageResp.Body.Close()

	select {
	case line := <-eventLine:
		if !strings.Contains(line, `"type":"user_message_accepted"`) {
			t.Fatalf("expected user_message_accepted event, got %s", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE event")
	}
	waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running
	})
	commandResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/commands", map[string]string{"name": "help"})
	if commandResp.StatusCode != http.StatusOK {
		t.Fatalf("expected command status 200, got %d", commandResp.StatusCode)
	}
	commandResp.Body.Close()
}

func TestProviderModelsEndpointDiscoversModels(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected model path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-5.5"},
			},
		})
	}))
	defer modelServer.Close()

	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: map[string]any{
		"api.providers": map[string]any{
			"openai": map[string]any{
				"type":     "openai_compatible",
				"base_url": modelServer.URL,
				"api_key":  "sk-test",
				"models":   map[string]any{},
			},
		},
	}}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	resp := postJSON(t, server.URL+"/providers/openai/models", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected provider models status 200, got %d: %s", resp.StatusCode, body)
	}
	var decoded struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded.Models) != 1 || decoded.Models[0].ID != "gpt-5.5" {
		t.Fatalf("unexpected models response: %#v", decoded.Models)
	}
}

func TestSessionLedgerEndpoints(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "ledger"})
	resp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/ledger", map[string]interface{}{
		"goal":          "ship ledger",
		"current_phase": "planning",
		"next_steps":    []string{"run tests"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected ledger post 200, got %d: %s", resp.StatusCode, string(data))
	}

	getResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/ledger")
	if err != nil {
		t.Fatalf("get ledger: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected ledger get 200, got %d", getResp.StatusCode)
	}
	var ledger backend.ProjectLedger
	if err := json.NewDecoder(getResp.Body).Decode(&ledger); err != nil {
		t.Fatalf("decode ledger: %v", err)
	}
	if ledger.Goal != "ship ledger" || !strings.Contains(ledger.Compact, "run tests") {
		t.Fatalf("unexpected ledger response: %+v", ledger)
	}
}

func TestPackagesQualityEndpoint(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "skills", "qa"), 0755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	manifest := `name: qa-kit
version: 0.1.0
resources:
  skills:
    - skills/qa/SKILL.md
permissions:
  - shell
recommended_bundles:
  - core_code
smoke_tests:
  - name: quick
    command: printf ok
`
	if err := os.WriteFile(filepath.Join(source, pkgregistry.ManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "skills", "qa", "SKILL.md"), []byte("# QA\n"), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	installResp := postJSON(t, server.URL+"/packages/install", map[string]string{"source": source})
	if installResp.StatusCode != http.StatusOK {
		t.Fatalf("install package status %d", installResp.StatusCode)
	}
	installResp.Body.Close()
	resp, err := http.Get(server.URL + "/packages/quality")
	if err != nil {
		t.Fatalf("get package quality: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("quality status %d", resp.StatusCode)
	}
	var report pkgregistry.QualityReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decode quality report: %v", err)
	}
	if report.PackageCount != 1 || len(report.Packages) != 1 || report.Packages[0].Name != "qa-kit" {
		t.Fatalf("unexpected quality report: %+v", report)
	}

	smokeResp := postJSON(t, server.URL+"/packages/qa-kit/smoke/quick", map[string]string{})
	if smokeResp.StatusCode != http.StatusOK {
		t.Fatalf("smoke status %d", smokeResp.StatusCode)
	}
	var smokeRun pkgregistry.SmokeRun
	if err := json.NewDecoder(smokeResp.Body).Decode(&smokeRun); err != nil {
		t.Fatalf("decode smoke run: %v", err)
	}
	smokeResp.Body.Close()
	if smokeRun.Status != "passed" || smokeRun.SessionID == "" {
		t.Fatalf("unexpected smoke run: %+v", smokeRun)
	}

	manifest = `name: qa-kit
version: 0.2.0
resources:
  skills:
    - skills/qa/SKILL.md
smoke_tests:
  - name: quick
    command: printf ok
`
	if err := os.WriteFile(filepath.Join(source, pkgregistry.ManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}
	reinstallResp := postJSON(t, server.URL+"/packages/qa-kit/reinstall", map[string]string{})
	if reinstallResp.StatusCode != http.StatusOK {
		t.Fatalf("reinstall status %d", reinstallResp.StatusCode)
	}
	var reinstalled pkgregistry.Entry
	if err := json.NewDecoder(reinstallResp.Body).Decode(&reinstalled); err != nil {
		t.Fatalf("decode reinstall: %v", err)
	}
	reinstallResp.Body.Close()
	if reinstalled.Version != "0.2.0" {
		t.Fatalf("unexpected reinstall response: %+v", reinstalled)
	}
}

func TestMessagesEndpointAcceptsAsyncTurn(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}},
		started:   make(chan struct{}, 1),
		block:     make(chan struct{}),
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "async-message"})
	messageResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]string{"text": "hello"})
	if messageResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(messageResp.Body)
		t.Fatalf("expected async message status 202, got %d: %s", messageResp.StatusCode, string(body))
	}
	messageResp.Body.Close()

	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}
	running := waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return snapshot.Running
	})
	if len(running.Messages) == 0 || protocol.MessageText(running.Messages[0]) != "hello" {
		t.Fatalf("expected accepted user message in running snapshot, got %+v", running.Messages)
	}

	close(caller.block)
	finished := waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running && len(snapshot.Messages) >= 2
	})
	if got := protocol.MessageText(finished.Messages[len(finished.Messages)-1]); got != "done" {
		t.Fatalf("expected async response to finish, got %q", got)
	}
}

func TestCancelTurnEndpointStopsAsyncTurn(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}},
		started:   make(chan struct{}, 1),
		block:     make(chan struct{}),
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "cancel-turn"})
	messageResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]string{"text": "stop me"})
	if messageResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(messageResp.Body)
		t.Fatalf("expected async message status 202, got %d: %s", messageResp.StatusCode, string(body))
	}
	var submitResult backend.SubmitResult
	if err := json.NewDecoder(messageResp.Body).Decode(&submitResult); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	messageResp.Body.Close()

	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}

	cancelResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/turns/"+submitResult.TurnID+"/cancel", map[string]string{})
	if cancelResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(cancelResp.Body)
		t.Fatalf("expected cancel status 200, got %d: %s", cancelResp.StatusCode, string(body))
	}
	var cancelResult backend.CancelTurnResult
	if err := json.NewDecoder(cancelResp.Body).Decode(&cancelResult); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	cancelResp.Body.Close()
	if cancelResult.Status != "canceling" || cancelResult.TurnID != submitResult.TurnID {
		t.Fatalf("unexpected cancel response: %+v", cancelResult)
	}

	finished := waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running
	})
	if len(finished.Messages) != 1 || protocol.MessageText(finished.Messages[0]) != "stop me" {
		t.Fatalf("expected canceled turn to keep only user message, got %+v", finished.Messages)
	}
}

func TestRetryTurnEndpointReplaysCanceledTurn(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("retried")}}},
		started:   make(chan struct{}, 2),
		block:     make(chan struct{}),
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "retry-turn"})
	messageResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]string{"text": "retry me"})
	if messageResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(messageResp.Body)
		t.Fatalf("expected async message status 202, got %d: %s", messageResp.StatusCode, string(body))
	}
	var submitResult backend.SubmitResult
	if err := json.NewDecoder(messageResp.Body).Decode(&submitResult); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	messageResp.Body.Close()

	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}
	cancelResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/turns/"+submitResult.TurnID+"/cancel", map[string]string{})
	cancelResp.Body.Close()
	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("expected cancel status 200, got %d", cancelResp.StatusCode)
	}
	_ = waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running
	})

	close(caller.block)
	retryResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/turns/"+submitResult.TurnID+"/retry", map[string]string{})
	if retryResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(retryResp.Body)
		t.Fatalf("expected retry status 202, got %d: %s", retryResp.StatusCode, string(body))
	}
	var retryResult backend.SubmitResult
	if err := json.NewDecoder(retryResp.Body).Decode(&retryResult); err != nil {
		t.Fatalf("decode retry response: %v", err)
	}
	retryResp.Body.Close()
	if retryResult.RetryOf != submitResult.TurnID || retryResult.TurnID == "" || retryResult.TurnID == submitResult.TurnID {
		t.Fatalf("unexpected retry response: %+v", retryResult)
	}

	finished := waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running && len(snapshot.Messages) >= 2
	})
	if got := protocol.MessageText(finished.Messages[len(finished.Messages)-1]); got != "retried" {
		t.Fatalf("expected retry response to finish, got %q", got)
	}
}

func TestResumeTurnEndpointContinuesInterruptedCheckpoint(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("continued")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	locator := backend.SessionLocator{Channel: "web", Key: "resume-turn"}
	sessionID := testStableSessionID(locator)
	dir := filepath.Join(cfg.SessionsDir, sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	now := time.Now().Add(-time.Minute)
	envelope := message.NewTextEnvelope(message.SourceWeb, sessionID, cfg.LeadName, "resume me", now)
	state := agent.SessionState{
		Messages: []protocol.Message{
			envelope.ToProtocolMessage(protocol.RoleUser, "", false),
			protocol.NewTextMessage(protocol.RoleAssistant, "partial checkpoint"),
		},
	}
	stateData, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), stateData, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	sum := sha256.Sum256(stateData)
	manifestData, err := json.Marshal(backend.SessionManifest{
		SessionID:      sessionID,
		Locator:        locator,
		StateDigest:    hex.EncodeToString(sum[:]),
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestData, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	turnID := "turn-http-resume"
	turnsData, err := json.Marshal([]backend.TurnRecord{
		{
			ID:                turnID,
			Status:            "interrupted",
			Source:            string(message.SourceWeb),
			Sender:            cfg.LeadName,
			Summary:           "resume me",
			StartedAt:         now,
			UpdatedAt:         now,
			Error:             "Previous process stopped before this turn completed.",
			PriorMessageCount: 0,
			Envelope:          &envelope,
		},
	})
	if err != nil {
		t.Fatalf("marshal turns: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "turns.json"), turnsData, 0644); err != nil {
		t.Fatalf("write turns: %v", err)
	}

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "resume-turn"})
	if opened.SessionID != sessionID {
		t.Fatalf("expected fixture session id %s, got %s", sessionID, opened.SessionID)
	}
	resumeResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/turns/"+turnID+"/resume", map[string]string{})
	if resumeResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resumeResp.Body)
		t.Fatalf("expected resume status 202, got %d: %s", resumeResp.StatusCode, string(body))
	}
	var resumeResult backend.SubmitResult
	if err := json.NewDecoder(resumeResp.Body).Decode(&resumeResult); err != nil {
		t.Fatalf("decode resume response: %v", err)
	}
	resumeResp.Body.Close()
	if resumeResult.TurnID != turnID || resumeResult.Status != "running" {
		t.Fatalf("unexpected resume response: %+v", resumeResult)
	}

	finished := waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running && len(snapshot.Messages) == 3
	})
	if got := protocol.MessageText(finished.Messages[2]); got != "continued" {
		t.Fatalf("expected resumed answer, got %q", got)
	}
}

func TestEventsEndpointReplaysActiveTurn(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{
		responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}},
		started:   make(chan struct{}, 1),
		block:     make(chan struct{}),
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "events-replay"})
	messageResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]string{"text": "replay me"})
	if messageResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(messageResp.Body)
		t.Fatalf("expected async message status 202, got %d: %s", messageResp.StatusCode, string(body))
	}
	messageResp.Body.Close()
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async turn to start")
	}

	eventResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/events?replay=active")
	if err != nil {
		t.Fatalf("open replay events stream: %v", err)
	}
	defer eventResp.Body.Close()
	if eventResp.StatusCode != http.StatusOK {
		t.Fatalf("expected events status 200, got %d", eventResp.StatusCode)
	}
	line := readFirstSSEDataLine(t, eventResp.Body)
	if !strings.Contains(line, `"type":"user_message_accepted"`) {
		t.Fatalf("expected replayed user_message_accepted event, got %s", line)
	}

	close(caller.block)
	_ = waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running
	})
}

func TestListCommandsReturnsBuiltinSlashCommandMetadata(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	resp, err := http.Get(server.URL + "/commands")
	if err != nil {
		t.Fatalf("get commands: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected commands status 200, got %d", resp.StatusCode)
	}

	var items []commands.CommandMetadata
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode commands response: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected non-empty builtin command list")
	}
	byName := make(map[string]commands.CommandMetadata, len(items))
	for _, item := range items {
		if item.Name == "" || item.Description == "" {
			t.Fatalf("expected name and description on every entry, got %+v", item)
		}
		byName[item.Name] = item
	}
	// The web composer slash palette depends on these core commands being
	// discoverable; pin a representative subset so a refactor of
	// AvailableMetadata cannot silently drop them.
	for _, name := range []string{"bash", "clear", "compact", "skills", "model", "help"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("expected builtin command %q in /commands response", name)
		}
	}
	if byName["bash"].InputHint == "" {
		t.Fatalf("expected input hint for /bash, got %+v", byName["bash"])
	}
}

func TestMetaEndpointIsPublic(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	resp, err := http.Get(server.URL + "/meta")
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected meta status 200, got %d", resp.StatusCode)
	}

	var meta map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatalf("decode meta response: %v", err)
	}
	if meta["auth_required"] != true {
		t.Fatalf("expected auth_required=true, got %#v", meta["auth_required"])
	}
}

func TestProtectedEndpointsRequireBearerToken(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	resp, err := http.Post(server.URL+"/sessions", "application/json", strings.NewReader(`{"locator":{"channel":"web","key":"default"}}`))
	if err != nil {
		t.Fatalf("post sessions without token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer token, got %d", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/sessions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	listResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get sessions with token: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with bearer token, got %d", listResp.StatusCode)
	}
}

func TestListSessionsEndpointFiltersByChannel(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	createPersistedSessionWithLocator(t, service, server.URL, map[string]string{"channel": "web", "key": "alpha"})
	createPersistedSessionWithLocator(t, service, server.URL, map[string]string{"channel": "local", "key": "default"})

	resp, err := http.Get(server.URL + "/sessions?channel=web")
	if err != nil {
		t.Fatalf("get filtered sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected session list status 200, got %d", resp.StatusCode)
	}

	var listed []backend.ListedSession
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode session list: %v", err)
	}
	if len(listed) != 1 || listed[0].Locator.Channel != "web" || listed[0].Locator.Key != "alpha" {
		t.Fatalf("unexpected filtered sessions: %#v", listed)
	}
}

func TestListSessionsEndpointIncludesAllChannelsAndReflectsDelete(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
		{Content: []protocol.Block{protocol.TextBlock("done")}},
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	webSession := createPersistedSessionWithLocator(t, service, server.URL, map[string]string{"channel": "web", "key": "alpha"})
	weixinSession := createPersistedSessionWithLocator(t, service, server.URL, map[string]string{"channel": "weixin", "key": "wx-chat"})
	createPersistedSessionWithLocator(t, service, server.URL, map[string]string{"channel": "feishu", "key": "fs-chat"})

	resp, err := http.Get(server.URL + "/sessions")
	if err != nil {
		t.Fatalf("get sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected session list status 200, got %d", resp.StatusCode)
	}

	var listed []backend.ListedSession
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode session list: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("expected all channels to be listed, got %#v", listed)
	}
	channels := map[string]bool{}
	for _, item := range listed {
		channels[item.Locator.Channel] = true
	}
	for _, channel := range []string{"web", "weixin", "feishu"} {
		if !channels[channel] {
			t.Fatalf("expected channel %q in sessions list, got %#v", channel, listed)
		}
	}

	req, err := http.NewRequest(http.MethodDelete, server.URL+"/sessions/"+weixinSession.SessionID, nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	deleteResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete session: %v", err)
	}
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected delete status 204, got %d", deleteResp.StatusCode)
	}

	resp, err = http.Get(server.URL + "/sessions")
	if err != nil {
		t.Fatalf("get sessions after delete: %v", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode session list after delete: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected deleted session to disappear, got %#v", listed)
	}
	for _, item := range listed {
		if item.SessionID == weixinSession.SessionID {
			t.Fatalf("expected deleted weixin session to be removed, got %#v", listed)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionsDir, weixinSession.SessionID)); !os.IsNotExist(err) {
		t.Fatalf("expected deleted session dir to be removed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionsDir, webSession.SessionID)); err != nil {
		t.Fatalf("expected remaining session dir to stay, got %v", err)
	}
}

func TestCommandsEndpointSupportsHistorySearch(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("Logged the rollout note.")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "history-command"})
	messageResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]interface{}{
		"envelope": map[string]interface{}{
			"source":  "web",
			"sender":  cfg.LeadName,
			"text":    "Aurora rollout note is ready.",
			"content": "Aurora rollout note is ready.",
		},
	})
	if messageResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(messageResp.Body)
		t.Fatalf("expected message status 202, got %d: %s", messageResp.StatusCode, string(body))
	}
	messageResp.Body.Close()
	waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running
	})

	commandResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/commands", map[string]interface{}{
		"name": "history",
		"args": []string{"search", "aurora", "role=user"},
	})
	if commandResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(commandResp.Body)
		t.Fatalf("expected command status 200, got %d: %s", commandResp.StatusCode, string(body))
	}
	defer commandResp.Body.Close()

	var result commands.Result
	if err := json.NewDecoder(commandResp.Body).Decode(&result); err != nil {
		t.Fatalf("decode command result: %v", err)
	}
	lower := strings.ToLower(result.Output)
	if !strings.Contains(result.Output, "History search:") || !strings.Contains(lower, "aurora") || !strings.Contains(result.Output, "source=current_session") {
		t.Fatalf("unexpected history search command output: %q", result.Output)
	}
}

func TestAttachmentUploadAndDownloadEndpoints(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "attachments"})
	uploaded := uploadAttachment(t, server.URL+"/sessions/"+opened.SessionID+"/attachments", "notes.txt", "hello attachment")
	if len(uploaded.Attachments) != 1 || uploaded.Attachments[0].ID == "" {
		t.Fatalf("expected uploaded attachment metadata, got %#v", uploaded)
	}

	messageResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]interface{}{
		"envelope": map[string]interface{}{
			"source":      "web",
			"sender":      "lead",
			"text":        "please inspect",
			"content":     "please inspect",
			"attachments": uploaded.Attachments,
		},
	})
	if messageResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected message status 202, got %d", messageResp.StatusCode)
	}
	messageResp.Body.Close()
	waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running
	})

	snapshotResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID)
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	defer snapshotResp.Body.Close()
	var snapshot backend.Snapshot
	if err := json.NewDecoder(snapshotResp.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snapshot.Messages) == 0 || snapshot.Messages[0].Metadata == nil || len(snapshot.Messages[0].Metadata.Attachments) != 1 {
		t.Fatalf("expected attachment metadata in snapshot, got %#v", snapshot.Messages)
	}

	downloadResp, err := http.Get(server.URL + uploaded.Attachments[0].URL)
	if err != nil {
		t.Fatalf("download attachment: %v", err)
	}
	defer downloadResp.Body.Close()
	if downloadResp.StatusCode != http.StatusOK {
		t.Fatalf("expected attachment download status 200, got %d", downloadResp.StatusCode)
	}
	data, err := io.ReadAll(downloadResp.Body)
	if err != nil {
		t.Fatalf("read downloaded attachment: %v", err)
	}
	if string(data) != "hello attachment" {
		t.Fatalf("unexpected attachment contents %q", string(data))
	}
}

func TestAttachmentUploadAndDownloadPDFEndpoints(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "pdf-attachments"})
	uploaded := uploadAttachment(t, server.URL+"/sessions/"+opened.SessionID+"/attachments", "manual.pdf", "%PDF-1.7 fake bytes")
	if len(uploaded.Attachments) != 1 || uploaded.Attachments[0].ID == "" {
		t.Fatalf("expected uploaded attachment metadata, got %#v", uploaded)
	}

	downloadResp, err := http.Get(server.URL + uploaded.Attachments[0].URL)
	if err != nil {
		t.Fatalf("download attachment: %v", err)
	}
	defer downloadResp.Body.Close()
	if downloadResp.StatusCode != http.StatusOK {
		t.Fatalf("expected attachment download status 200, got %d", downloadResp.StatusCode)
	}
	if contentType := downloadResp.Header.Get("Content-Type"); !strings.Contains(contentType, "application/pdf") {
		t.Fatalf("expected pdf content type, got %q", contentType)
	}
	data, err := io.ReadAll(downloadResp.Body)
	if err != nil {
		t.Fatalf("read downloaded attachment: %v", err)
	}
	if string(data) != "%PDF-1.7 fake bytes" {
		t.Fatalf("unexpected attachment contents %q", string(data))
	}
}

func containsTag(tags []string, target string) bool {
	for _, tag := range tags {
		if strings.EqualFold(strings.TrimSpace(tag), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func TestSessionSkillEndpoints(t *testing.T) {
	cfg := newTestConfig(t)
	writeTestSkill(t, cfg.SkillsDir, "review-helper", `---
name: Review Helper Deluxe
description: Review code changes with a checklist
recommended_bundles:
  - background
sections:
  - core
  - workflow
---
## Core
Look for bugs and regressions.

## Workflow
Read the diff first.`)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "skills"})

	catalogResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/skills/catalog")
	if err != nil {
		t.Fatalf("get skill catalog: %v", err)
	}
	defer catalogResp.Body.Close()
	if catalogResp.StatusCode != http.StatusOK {
		t.Fatalf("expected skill catalog 200, got %d", catalogResp.StatusCode)
	}
	var catalog []map[string]any
	if err := json.NewDecoder(catalogResp.Body).Decode(&catalog); err != nil {
		t.Fatalf("decode skill catalog: %v", err)
	}
	if len(catalog) != 1 || catalog[0]["id"] != "review-helper" || catalog[0]["name"] != "Review Helper Deluxe" {
		t.Fatalf("unexpected skill catalog: %#v", catalog)
	}

	loadResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/skills/load", map[string]string{"name": "review-helper"}, "")
	if loadResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(loadResp.Body)
		t.Fatalf("expected skill load 200, got %d: %s", loadResp.StatusCode, string(body))
	}
	_ = loadResp.Body.Close()

	activeResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/skills/active")
	if err != nil {
		t.Fatalf("get active skills: %v", err)
	}
	defer activeResp.Body.Close()
	if activeResp.StatusCode != http.StatusOK {
		t.Fatalf("expected active skills 200, got %d", activeResp.StatusCode)
	}
	var active []map[string]any
	if err := json.NewDecoder(activeResp.Body).Decode(&active); err != nil {
		t.Fatalf("decode active skills: %v", err)
	}
	if len(active) != 1 || active[0]["id"] != "review-helper" || active[0]["name"] != "Review Helper Deluxe" {
		t.Fatalf("unexpected active skills: %#v", active)
	}

	expandResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/skills/expand", map[string]any{
		"name":     "review-helper",
		"sections": []string{"workflow"},
	}, "")
	if expandResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(expandResp.Body)
		t.Fatalf("expected skill expand 200, got %d: %s", expandResp.StatusCode, string(body))
	}
	_ = expandResp.Body.Close()

	getResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/skills/review-helper")
	if err != nil {
		t.Fatalf("get skill detail: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected skill detail 200, got %d", getResp.StatusCode)
	}
	var detail map[string]any
	if err := json.NewDecoder(getResp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode skill detail: %v", err)
	}
	if detail["id"] != "review-helper" || detail["name"] != "Review Helper Deluxe" {
		t.Fatalf("unexpected skill detail: %#v", detail)
	}

	unloadResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/skills/unload", map[string]string{"name": "review-helper"}, "")
	if unloadResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(unloadResp.Body)
		t.Fatalf("expected skill unload 200, got %d: %s", unloadResp.StatusCode, string(body))
	}
	_ = unloadResp.Body.Close()

	loadAgainResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/skills/load", map[string]string{"name": "review-helper"}, "")
	if loadAgainResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(loadAgainResp.Body)
		t.Fatalf("expected skill reload 200, got %d: %s", loadAgainResp.StatusCode, string(body))
	}
	_ = loadAgainResp.Body.Close()
	removeResp := doJSONWithToken(t, http.MethodDelete, server.URL+"/sessions/"+opened.SessionID+"/skills/review-helper", nil, "")
	if removeResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(removeResp.Body)
		t.Fatalf("expected skill remove 200, got %d: %s", removeResp.StatusCode, string(body))
	}
	var removed map[string]any
	if err := json.NewDecoder(removeResp.Body).Decode(&removed); err != nil {
		t.Fatalf("decode skill remove: %v", err)
	}
	_ = removeResp.Body.Close()
	if removed["id"] != "review-helper" || removed["status"] != "removed" || removed["was_active"] != true {
		t.Fatalf("unexpected skill remove result: %#v", removed)
	}

	activeAfterRemoveResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/skills/active")
	if err != nil {
		t.Fatalf("get active skills after remove: %v", err)
	}
	defer activeAfterRemoveResp.Body.Close()
	var activeAfterRemove []map[string]any
	if err := json.NewDecoder(activeAfterRemoveResp.Body).Decode(&activeAfterRemove); err != nil {
		t.Fatalf("decode active skills after remove: %v", err)
	}
	if len(activeAfterRemove) != 0 {
		t.Fatalf("expected removed active skill, got %#v", activeAfterRemove)
	}
}

func TestSessionSkillEndpointsReturnSkillStatusCodes(t *testing.T) {
	cfg := newTestConfig(t)
	writeTestSkill(t, cfg.SkillsDir, "review-helper", `---
name: Review Helper Deluxe
description: Review code changes with a checklist
sections:
  - core
  - workflow
---
## Core
Look for bugs and regressions.

## Workflow
Read the diff first.`)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "skills-status"})

	missingResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/skills/missing-skill")
	if err != nil {
		t.Fatalf("get missing skill: %v", err)
	}
	defer missingResp.Body.Close()
	if missingResp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(missingResp.Body)
		t.Fatalf("expected missing skill 404, got %d: %s", missingResp.StatusCode, string(body))
	}

	expandBeforeLoad := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/skills/expand", map[string]any{
		"name":     "review-helper",
		"sections": []string{"workflow"},
	}, "")
	if expandBeforeLoad.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(expandBeforeLoad.Body)
		t.Fatalf("expected expand-before-load 409, got %d: %s", expandBeforeLoad.StatusCode, string(body))
	}
	_ = expandBeforeLoad.Body.Close()

	loadResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/skills/load", map[string]string{"name": "Review Helper Deluxe"}, "")
	if loadResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(loadResp.Body)
		t.Fatalf("expected load by display name 200, got %d: %s", loadResp.StatusCode, string(body))
	}
	_ = loadResp.Body.Close()

	expandResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/skills/expand", map[string]any{
		"name":     "review-helper",
		"sections": []string{"templates"},
	}, "")
	if expandResp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(expandResp.Body)
		t.Fatalf("expected invalid section 400, got %d: %s", expandResp.StatusCode, string(body))
	}
	_ = expandResp.Body.Close()

	unloadResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/skills/unload", map[string]string{"name": "review-helper"}, "")
	if unloadResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(unloadResp.Body)
		t.Fatalf("expected unload 200, got %d: %s", unloadResp.StatusCode, string(body))
	}
	_ = unloadResp.Body.Close()

	unloadAgainResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/skills/unload", map[string]string{"name": "review-helper"}, "")
	if unloadAgainResp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(unloadAgainResp.Body)
		t.Fatalf("expected unload-after-remove 409, got %d: %s", unloadAgainResp.StatusCode, string(body))
	}
	_ = unloadAgainResp.Body.Close()
}

func TestSessionSkillInstallEndpoint(t *testing.T) {
	cfg := newTestConfig(t)
	sourceDir := filepath.Join(t.TempDir(), "playwright-cli")
	writeTestSkill(t, sourceDir, "", `---
description: Automate screenshots with Playwright
recommended_bundles:
  - web
sections:
  - core
  - workflow
---
## Core
Capture screenshots.

## Workflow
Open then capture.`)

	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "skills-install"})

	installResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/skills/install", map[string]string{
		"source": sourceDir,
		"name":   "playwright-cli",
	}, "")
	if installResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(installResp.Body)
		t.Fatalf("expected skill install 200, got %d: %s", installResp.StatusCode, string(body))
	}
	_ = installResp.Body.Close()

	catalogResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/skills/catalog")
	if err != nil {
		t.Fatalf("get skill catalog: %v", err)
	}
	defer catalogResp.Body.Close()
	if catalogResp.StatusCode != http.StatusOK {
		t.Fatalf("expected skill catalog 200, got %d", catalogResp.StatusCode)
	}
	var catalog []map[string]any
	if err := json.NewDecoder(catalogResp.Body).Decode(&catalog); err != nil {
		t.Fatalf("decode skill catalog: %v", err)
	}
	if len(catalog) != 1 || catalog[0]["name"] != "playwright-cli" {
		t.Fatalf("unexpected installed skill catalog: %#v", catalog)
	}
}

func TestSessionSkillSourcesEndpoint(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "skills-sources"})

	resp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/skills/sources")
	if err != nil {
		t.Fatalf("get skill sources: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected skill sources 200, got %d", resp.StatusCode)
	}
	var sources []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&sources); err != nil {
		t.Fatalf("decode skill sources: %v", err)
	}
	if len(sources) == 0 {
		t.Fatalf("expected curated skill sources, got %#v", sources)
	}
}

func TestSessionSkillSourcesEndpointSupportsSkillsHubSearch(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))

	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "react" {
			t.Fatalf("expected search query %q, got %q", "react", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"skills": []map[string]any{
				{
					"id":       "vercel-labs/agent-skills/vercel-react-best-practices",
					"skillId":  "vercel-react-best-practices",
					"name":     "vercel-react-best-practices",
					"installs": 2413,
					"source":   "vercel-labs/agent-skills",
				},
			},
		})
	}))
	defer searchServer.Close()
	t.Setenv("SKILLS_API_URL", searchServer.URL)

	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "skills-sources-search"})

	resp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/skills/sources?q=react")
	if err != nil {
		t.Fatalf("get searched skill sources: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected searched skill sources 200, got %d", resp.StatusCode)
	}

	var sources []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&sources); err != nil {
		t.Fatalf("decode searched skill sources: %v", err)
	}

	var found bool
	for _, item := range sources {
		if item["origin"] == "skillsh" {
			found = true
			if item["skill_name"] != "vercel-react-best-practices" {
				t.Fatalf("unexpected searched skill name: %#v", item)
			}
			if item["install_supported"] != true {
				t.Fatalf("expected searched skill to be installable, got %#v", item)
			}
			if item["install_source"] != "vercel-labs/agent-skills" || item["install_name"] != "vercel-react-best-practices" {
				t.Fatalf("expected install preview fields, got %#v", item)
			}
		}
	}
	if !found {
		t.Fatalf("expected skills.sh search result in %#v", sources)
	}
}

func TestSessionSkillSourcesEndpointSupportsTrendingMode(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))

	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/trending" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`<!doctype html><html><body>
<a href="/vercel-labs/skills/find-skills"><div><span>1</span><h3>find-skills</h3><p>vercel-labs/skills</p><span>1.2M</span></div></a>
</body></html>`))
	}))
	defer searchServer.Close()
	t.Setenv("SKILLS_API_URL", searchServer.URL)

	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "skills-sources-trending"})

	resp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/skills/sources?mode=trending")
	if err != nil {
		t.Fatalf("get trending skill sources: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected trending skill sources 200, got %d", resp.StatusCode)
	}
	var sources []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&sources); err != nil {
		t.Fatalf("decode trending skill sources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected one trending skill source, got %#v", sources)
	}
	if sources[0]["installs"] != float64(1200000) {
		t.Fatalf("expected installs field on trending item, got %#v", sources[0])
	}
}

func TestSessionTimelineEndpointIncludesTurnAndSkillLifecycle(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	sourceDir := filepath.Join(t.TempDir(), "playwright-cli")
	writeTestSkill(t, sourceDir, "", `---
description: Automate screenshots with Playwright
recommended_bundles:
  - web
sections:
  - core
  - workflow
---
## Core
Capture screenshots.

## Workflow
Open then capture.`)

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "timeline"})

	messageResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]string{"text": "hello"}, "")
	if messageResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(messageResp.Body)
		t.Fatalf("expected message 202, got %d: %s", messageResp.StatusCode, string(body))
	}
	_ = messageResp.Body.Close()
	waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running
	})

	installResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/skills/install", map[string]string{
		"source": sourceDir,
		"name":   "playwright-cli",
	}, "")
	if installResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(installResp.Body)
		t.Fatalf("expected skill install 200, got %d: %s", installResp.StatusCode, string(body))
	}
	_ = installResp.Body.Close()

	loadResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/skills/load", map[string]string{"name": "playwright-cli"}, "")
	if loadResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(loadResp.Body)
		t.Fatalf("expected skill load 200, got %d: %s", loadResp.StatusCode, string(body))
	}
	_ = loadResp.Body.Close()

	resp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/timeline?limit=20")
	if err != nil {
		t.Fatalf("get timeline: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected timeline 200, got %d", resp.StatusCode)
	}

	var timeline []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&timeline); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}

	var (
		sawUserTurn     bool
		sawTurnComplete bool
		sawInstall      bool
		sawActivate     bool
	)
	for _, item := range timeline {
		switch item["type"] {
		case "user_message_accepted":
			sawUserTurn = true
		case "turn_completed":
			sawTurnComplete = true
		case "skill_state_changed":
			payload, _ := item["payload"].(map[string]any)
			switch payload["action"] {
			case "installed":
				sawInstall = true
			case "activated":
				sawActivate = true
			}
		}
	}
	if !sawUserTurn || !sawTurnComplete || !sawInstall || !sawActivate {
		t.Fatalf("expected user turn and skill lifecycle events, got %#v", timeline)
	}
}

func TestSessionTimelinePageEndpointFiltersAndKeepsArrayEndpoint(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("paged reply")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "timeline-page"})
	messageResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]string{"text": "timeline page hello"}, "")
	if messageResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(messageResp.Body)
		t.Fatalf("expected message 202, got %d: %s", messageResp.StatusCode, string(body))
	}
	_ = messageResp.Body.Close()
	waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running
	})

	arrayResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/timeline?limit=20")
	if err != nil {
		t.Fatalf("get legacy timeline: %v", err)
	}
	defer arrayResp.Body.Close()
	if arrayResp.StatusCode != http.StatusOK {
		t.Fatalf("expected legacy timeline 200, got %d", arrayResp.StatusCode)
	}
	var legacy []map[string]any
	if err := json.NewDecoder(arrayResp.Body).Decode(&legacy); err != nil {
		t.Fatalf("decode legacy timeline: %v", err)
	}
	if len(legacy) == 0 {
		t.Fatal("expected legacy timeline array to remain populated")
	}

	pageResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/timeline/page?limit=2&type=user_message_accepted,turn_completed&q=hello")
	if err != nil {
		t.Fatalf("get timeline page: %v", err)
	}
	defer pageResp.Body.Close()
	if pageResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(pageResp.Body)
		t.Fatalf("expected timeline page 200, got %d: %s", pageResp.StatusCode, string(body))
	}
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
		Total      int              `json:"total"`
	}
	if err := json.NewDecoder(pageResp.Body).Decode(&page); err != nil {
		t.Fatalf("decode timeline page: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("expected filtered page with one item, got %+v", page)
	}
	if got := page.Items[0]["type"]; got != "user_message_accepted" {
		t.Fatalf("expected accepted user message event, got %v", got)
	}
}

func TestSessionPermissionEndpointsListAndApprovePendingRequests(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "command -v sh"})}},
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "permissions"})
	messageResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]interface{}{
		"envelope": map[string]interface{}{
			"source":  "web",
			"sender":  cfg.LeadName,
			"text":    "run command -v sh",
			"content": "run command -v sh",
		},
	})
	if messageResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(messageResp.Body)
		t.Fatalf("expected message status 202, got %d: %s", messageResp.StatusCode, string(body))
	}
	messageResp.Body.Close()
	waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return len(snapshot.PendingPermissions) > 0
	})

	listResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID + "/permissions")
	if err != nil {
		t.Fatalf("get permissions: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected permissions status 200, got %d", listResp.StatusCode)
	}
	var pending []tools.PendingPermission
	if err := json.NewDecoder(listResp.Body).Decode(&pending); err != nil {
		t.Fatalf("decode permissions: %v", err)
	}
	if len(pending) != 1 || pending[0].Request.ToolName != "bash" {
		t.Fatalf("unexpected pending permissions: %+v", pending)
	}

	approveResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/permissions/"+pending[0].ID+"/approve", map[string]string{"scope": "once"})
	if approveResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(approveResp.Body)
		t.Fatalf("expected approve status 200, got %d: %s", approveResp.StatusCode, string(body))
	}
	var resolution tools.PermissionResolution
	if err := json.NewDecoder(approveResp.Body).Decode(&resolution); err != nil {
		t.Fatalf("decode approve response: %v", err)
	}
	approveResp.Body.Close()
	if !resolution.Resumed || resolution.ResumeStatus != "completed" {
		t.Fatalf("expected resumed approval response, got %+v", resolution)
	}

	listResp, err = http.Get(server.URL + "/sessions/" + opened.SessionID + "/permissions")
	if err != nil {
		t.Fatalf("get permissions after approval: %v", err)
	}
	defer listResp.Body.Close()
	if err := json.NewDecoder(listResp.Body).Decode(&pending); err != nil {
		t.Fatalf("decode permissions after approval: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected pending permissions to be cleared, got %+v", pending)
	}
	snapshotResp, err := http.Get(server.URL + "/sessions/" + opened.SessionID)
	if err != nil {
		t.Fatalf("get snapshot after approval: %v", err)
	}
	defer snapshotResp.Body.Close()
	var snapshot backend.Snapshot
	if err := json.NewDecoder(snapshotResp.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode snapshot after approval: %v", err)
	}
	if got := protocol.MessageText(snapshot.Messages[len(snapshot.Messages)-1]); got != "done" {
		t.Fatalf("expected resumed assistant output, got %q", got)
	}
}

func TestMemoryEndpoints(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	rememberResp := doJSONWithToken(t, http.MethodPost, server.URL+"/memory/remember", map[string]any{
		"title":       "Project Identity",
		"summary":     "GoDex is a shared backend workspace for Web, TUI, and IM.",
		"content":     "Treat GoDex as a shared backend workspace coordinating Web, TUI, and IM channels.",
		"memory_type": "identity",
		"source":      "manual",
		"tags":        []string{"workspace"},
	}, cfg.WebToken)
	if rememberResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(rememberResp.Body)
		t.Fatalf("expected remember identity memory 200, got %d: %s", rememberResp.StatusCode, string(body))
	}
	rememberResp.Body.Close()

	rememberResp = doJSONWithToken(t, http.MethodPost, server.URL+"/memory/remember", map[string]any{
		"title":       "Chinese Preference",
		"summary":     "Reply in concise Chinese.",
		"content":     "以后请用中文回复，并保持简洁。",
		"memory_type": "user",
		"source":      "manual",
		"tags":        []string{"language", "tone"},
	}, cfg.WebToken)
	if rememberResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(rememberResp.Body)
		t.Fatalf("expected remember memory 200, got %d: %s", rememberResp.StatusCode, string(body))
	}
	rememberResp.Body.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/memory?memory_type=user", nil)
	if err != nil {
		t.Fatalf("new memory request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	memoriesResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	defer memoriesResp.Body.Close()
	var memories []memory.StoredMemory
	if err := json.NewDecoder(memoriesResp.Body).Decode(&memories); err != nil {
		t.Fatalf("decode memories: %v", err)
	}
	if len(memories) != 1 || memories[0].Title != "Chinese Preference" {
		t.Fatalf("unexpected memories: %+v", memories)
	}

	updateResp := doJSONWithToken(t, http.MethodPost, server.URL+"/memory/update", map[string]any{
		"match_file":  memories[0].File,
		"title":       "Chinese Reply Preference",
		"summary":     "Reply in concise Chinese and stay practical.",
		"content":     "以后请用中文回复，并保持务实、简洁。",
		"memory_type": "user",
		"source":      "manual-web",
		"tags":        []string{"language", "tone"},
	}, cfg.WebToken)
	if updateResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(updateResp.Body)
		t.Fatalf("expected update memory 200, got %d: %s", updateResp.StatusCode, string(body))
	}
	updateResp.Body.Close()

	req, err = http.NewRequest(http.MethodGet, server.URL+"/memory/audit?limit=20", nil)
	if err != nil {
		t.Fatalf("new memory audit request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	auditResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list memory audit: %v", err)
	}
	var auditEntries []memory.AuditLogEntry
	if err := json.NewDecoder(auditResp.Body).Decode(&auditEntries); err != nil {
		t.Fatalf("decode memory audit: %v", err)
	}
	auditResp.Body.Close()
	var updateAuditID string
	for _, item := range auditEntries {
		if item.Action == memory.AuditUpdate && item.After != nil && item.After.Title == "Chinese Reply Preference" {
			updateAuditID = item.ID
			break
		}
	}
	if updateAuditID == "" {
		t.Fatalf("expected update audit entry, got %+v", auditEntries)
	}
	restoreResp := doJSONWithToken(t, http.MethodPost, server.URL+"/memory/audit/"+updateAuditID+"/restore", map[string]any{
		"target": "before",
	}, cfg.WebToken)
	if restoreResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(restoreResp.Body)
		t.Fatalf("expected restore audit 200, got %d: %s", restoreResp.StatusCode, string(body))
	}
	restoreResp.Body.Close()

	req, err = http.NewRequest(http.MethodGet, server.URL+"/memory?memory_type=user", nil)
	if err != nil {
		t.Fatalf("new restored memory request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	restoredResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list restored memory: %v", err)
	}
	var restored []memory.StoredMemory
	if err := json.NewDecoder(restoredResp.Body).Decode(&restored); err != nil {
		t.Fatalf("decode restored memories: %v", err)
	}
	restoredResp.Body.Close()
	if len(restored) != 1 || restored[0].Title != "Chinese Preference" || !strings.Contains(restored[0].Content, "保持简洁") {
		t.Fatalf("expected restored pre-update memory, got %+v", restored)
	}

	if err := os.MkdirAll(filepath.Join(cfg.WorkspaceDir, "docs"), 0755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.WorkspaceDir, "README.md"), []byte(`# GoDex

GoDex is a shared backend workspace for Web, TUI, and IM channels.

It centralizes session management and shared tooling.
`), 0644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.WorkspaceDir, "AGENTS.md"), []byte(`# Delivery Workflow

Always run go test ./... before wrapping up runtime changes.

Keep channel and runtime regressions visible.
`), 0644); err != nil {
		t.Fatalf("write agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.WorkspaceDir, "docs", "runtime.md"), []byte(`# Runtime Notes

The runtime coordinates background tasks, approvals, and event delivery.

Use the snapshot timeline to inspect state transitions.
`), 0644); err != nil {
		t.Fatalf("write docs note: %v", err)
	}

	mineResp := doJSONWithToken(t, http.MethodPost, server.URL+"/memory/mine/project", nil, cfg.WebToken)
	if mineResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(mineResp.Body)
		t.Fatalf("expected mine project docs 200, got %d: %s", mineResp.StatusCode, string(body))
	}
	var mined []memory.Candidate
	if err := json.NewDecoder(mineResp.Body).Decode(&mined); err != nil {
		t.Fatalf("decode mine response: %v", err)
	}
	mineResp.Body.Close()
	if len(mined) != 3 {
		t.Fatalf("expected three mined candidates, got %+v", mined)
	}

	extractor := memory.NewExtractor(memory.NewManager(cfg.MemoryDir), cfg.TempDir)
	added, err := extractor.CaptureInsightsReport(&insights.Report{
		Frictions: []string{
			"Model/API timeouts are recurring and should be treated as a first-class runtime friction.",
			"Path resolution and file existence checks remain a recurring source of errors.",
		},
	})
	if err != nil {
		t.Fatalf("seed memory candidates: %v", err)
	}
	if len(added) == 0 {
		t.Fatalf("expected seeded memory candidates")
	}

	if err := os.MkdirAll(cfg.TranscriptsDir, 0755); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}
	transcript := []map[string]any{{
		"content": []map[string]any{{
			"type": "text",
			"text": "context deadline exceeded while running a long model request",
		}},
	}}
	transcriptData, err := json.Marshal(transcript)
	if err != nil {
		t.Fatalf("marshal transcript: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.TranscriptsDir, "transcript_digest.json"), transcriptData, 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	digestResp := doJSONWithToken(t, http.MethodPost, server.URL+"/memory/digest", nil, cfg.WebToken)
	if digestResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(digestResp.Body)
		t.Fatalf("expected memory digest 200, got %d: %s", digestResp.StatusCode, string(body))
	}
	var digest backend.MemoryDigestResult
	if err := json.NewDecoder(digestResp.Body).Decode(&digest); err != nil {
		t.Fatalf("decode memory digest: %v", err)
	}
	digestResp.Body.Close()
	if !strings.Contains(digest.Report, "# Insights") || len(digest.Candidates) == 0 {
		t.Fatalf("expected digest report and candidates, got %+v", digest)
	}

	req, err = http.NewRequest(http.MethodGet, server.URL+"/memory/candidates", nil)
	if err != nil {
		t.Fatalf("new candidates request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	candidatesResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	defer candidatesResp.Body.Close()
	var candidates []memory.Candidate
	if err := json.NewDecoder(candidatesResp.Body).Decode(&candidates); err != nil {
		t.Fatalf("decode candidates: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatalf("expected pending candidates, got %+v", candidates)
	}

	acceptResp := doJSONWithToken(t, http.MethodPost, server.URL+"/memory/candidates/"+candidates[0].Fingerprint+"/accept", map[string]any{
		"always_include": true,
	}, cfg.WebToken)
	if acceptResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(acceptResp.Body)
		t.Fatalf("expected accept candidate 200, got %d: %s", acceptResp.StatusCode, string(body))
	}
	var accepted memory.Entry
	if err := json.NewDecoder(acceptResp.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode accept response: %v", err)
	}
	acceptResp.Body.Close()
	if !containsTag(accepted.Tags, "core") {
		t.Fatalf("expected accept endpoint to apply core tag, got %+v", accepted.Tags)
	}

	dismissTarget := ""
	if len(candidates) > 1 {
		dismissTarget = candidates[1].Fingerprint
	} else {
		added, err = extractor.CaptureInsightsReport(&insights.Report{
			Frictions: []string{
				"Progressive tool loading is discoverable but still produces inactive-tool friction in practice.",
			},
		})
		if err != nil || len(added) == 0 {
			t.Fatalf("seed dismiss candidate: %v %+v", err, added)
		}
		dismissTarget = added[0].Fingerprint
	}
	dismissResp := doJSONWithToken(t, http.MethodPost, server.URL+"/memory/candidates/"+dismissTarget+"/dismiss", nil, cfg.WebToken)
	if dismissResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(dismissResp.Body)
		t.Fatalf("expected dismiss candidate 200, got %d: %s", dismissResp.StatusCode, string(body))
	}
	dismissResp.Body.Close()

	req, err = http.NewRequest(http.MethodGet, server.URL+"/memory/suppressions", nil)
	if err != nil {
		t.Fatalf("new suppressions request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	suppressionsResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list suppressions: %v", err)
	}
	defer suppressionsResp.Body.Close()
	var suppressions []memory.CandidateSuppression
	if err := json.NewDecoder(suppressionsResp.Body).Decode(&suppressions); err != nil {
		t.Fatalf("decode suppressions: %v", err)
	}
	if len(suppressions) == 0 {
		t.Fatalf("expected suppression after dismiss, got %+v", suppressions)
	}

	req, err = http.NewRequest(http.MethodGet, server.URL+"/memory/context?q="+url.QueryEscape("请用中文回复"), nil)
	if err != nil {
		t.Fatalf("new memory context request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	contextResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preview memory context: %v", err)
	}
	defer contextResp.Body.Close()
	contextBody, err := io.ReadAll(contextResp.Body)
	if err != nil {
		t.Fatalf("read memory context: %v", err)
	}
	var contextPayload map[string]any
	if err := json.Unmarshal(contextBody, &contextPayload); err != nil {
		t.Fatalf("decode memory context payload: %v", err)
	}
	for _, key := range []string{"identity", "core", "relevant"} {
		raw, ok := contextPayload[key]
		if !ok {
			t.Fatalf("expected memory context %s key, got %#v", key, contextPayload)
		}
		if _, ok := raw.([]any); !ok {
			t.Fatalf("expected memory context %s array, got %#v", key, raw)
		}
	}
	var layers memory.ContextLayers
	if err := json.Unmarshal(contextBody, &layers); err != nil {
		t.Fatalf("decode memory context: %v", err)
	}
	if len(layers.Identity) == 0 {
		t.Fatalf("expected identity memory context preview, got %+v", layers)
	}
	if len(layers.Core) == 0 {
		t.Fatalf("expected core memory context preview, got %+v", layers)
	}
	foundIdentity := false
	for _, item := range layers.Identity {
		if item.Title == "Project Identity" {
			foundIdentity = true
			break
		}
	}
	foundChinesePreference := false
	foundAcceptedCore := false
	for _, item := range layers.Core {
		if item.Title == "Chinese Preference" {
			foundChinesePreference = true
		}
		if containsTag(item.Tags, "core") {
			foundAcceptedCore = true
		}
	}
	if !foundChinesePreference || !foundAcceptedCore {
		t.Fatalf("expected core preview to include both accepted core memory and Chinese preference, got %+v", layers)
	}
	if !foundIdentity {
		t.Fatalf("expected identity preview to include project identity memory, got %+v", layers.Identity)
	}

	req, err = http.NewRequest(http.MethodGet, server.URL+"/memory?memory_type=user", nil)
	if err != nil {
		t.Fatalf("new updated memory request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	updatedMemoriesResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list updated memories: %v", err)
	}
	defer updatedMemoriesResp.Body.Close()
	if err := json.NewDecoder(updatedMemoriesResp.Body).Decode(&memories); err != nil {
		t.Fatalf("decode updated memories: %v", err)
	}
	if len(memories) != 2 && len(memories) != 1 {
		t.Fatalf("unexpected updated memories count: %+v", memories)
	}
	foundUpdated := false
	for _, item := range memories {
		if item.Title == "Chinese Preference" && strings.Contains(item.Content, "保持简洁") {
			foundUpdated = true
			memories[0] = item
			break
		}
	}
	if !foundUpdated {
		t.Fatalf("expected restored memory payload, got %+v", memories)
	}

	forgetResp := doJSONWithToken(t, http.MethodPost, server.URL+"/memory/forget", map[string]any{
		"file": memories[0].File,
	}, cfg.WebToken)
	if forgetResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(forgetResp.Body)
		t.Fatalf("expected forget memory 200, got %d: %s", forgetResp.StatusCode, string(body))
	}
	forgetResp.Body.Close()
}

func TestSessionContextInspectorEndpoint(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	cfg.CompressThreshold = 1
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}), commands.NewService(cfg))
	if _, err := service.RememberMemory(context.Background(), memory.SaveInput{
		Title:   "Project Identity",
		Summary: "Shared backend workspace.",
		Content: "This workspace routes chat, automation, and IM through a shared Godex backend.",
		Type:    memory.TypeIdentity,
		Source:  "manual-web",
	}); err != nil {
		t.Fatalf("remember identity memory: %v", err)
	}
	if _, err := service.RememberMemory(context.Background(), memory.SaveInput{
		Title:   "Weixin screenshots",
		Summary: "Weixin screenshot delivery needs browser artifact handling.",
		Content: "When the user asks about weixin screenshots, prioritize browser artifact persistence and channel delivery branches.",
		Type:    memory.TypeProject,
		Source:  "project-miner:docs",
		Tags:    []string{"weixin", "browser"},
	}); err != nil {
		t.Fatalf("remember relevant memory: %v", err)
	}
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened, err := service.OpenSession(context.Background(), backend.SessionLocator{Channel: "web", Key: "context-inspector"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "Please capture the current browser state for later.", time.Now())); err != nil {
		t.Fatalf("seed compacted history: %v", err)
	}
	if _, err := service.Submit(context.Background(), opened.SessionID, message.NewTextEnvelope(message.SourceWeb, opened.SessionID, cfg.LeadName, "你之前说过 weixin screenshot 这条链路应该怎么排查？", time.Now())); err != nil {
		t.Fatalf("seed history recall decision: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/sessions/"+opened.SessionID+"/context-inspector", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get context inspector: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected context inspector 200, got %d: %s", resp.StatusCode, string(body))
	}

	var inspector backend.SessionContextInspector
	if err := json.NewDecoder(resp.Body).Decode(&inspector); err != nil {
		t.Fatalf("decode context inspector: %v", err)
	}
	if inspector.Context.SessionID != opened.SessionID {
		t.Fatalf("unexpected context summary: %+v", inspector.Context)
	}
	if inspector.TranscriptRefCount == 0 {
		t.Fatalf("expected transcript refs after compaction, got %+v", inspector)
	}
	if inspector.HistoryRecall == nil || !inspector.HistoryRecall.AllowTool {
		t.Fatalf("expected history recall decision, got %+v", inspector.HistoryRecall)
	}
	if strings.TrimSpace(inspector.RecallQuery) == "" {
		t.Fatalf("expected recall query summary, got %+v", inspector)
	}
	if len(inspector.MemoryPreview.Identity) == 0 {
		t.Fatalf("expected identity preview, got %+v", inspector.MemoryPreview)
	}
}

func TestSessionContextInspectorEndpointIncludesEmptyMemoryPreviewArrays(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened, err := service.OpenSession(context.Background(), backend.SessionLocator{Channel: "web", Key: "context-inspector-empty-arrays"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/sessions/"+opened.SessionID+"/context-inspector", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get context inspector: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected context inspector 200, got %d: %s", resp.StatusCode, string(body))
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode context inspector payload: %v", err)
	}
	memoryPreview, ok := payload["memory_preview"].(map[string]any)
	if !ok {
		t.Fatalf("expected memory_preview object, got %#v", payload["memory_preview"])
	}
	for _, key := range []string{"identity", "core", "relevant"} {
		raw, ok := memoryPreview[key]
		if !ok {
			t.Fatalf("expected memory_preview.%s to be present, got %#v", key, memoryPreview)
		}
		items, ok := raw.([]any)
		if !ok {
			t.Fatalf("expected memory_preview.%s to be array, got %#v", key, raw)
		}
		if len(items) != 0 {
			t.Fatalf("expected memory_preview.%s to be empty, got %#v", key, items)
		}
	}
}

func TestConfigEndpoints(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/config/schema", nil)
	if err != nil {
		t.Fatalf("new schema request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get config schema: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected schema status 200, got %d", resp.StatusCode)
	}

	updateReq := doJSONWithToken(t, http.MethodPut, server.URL+"/config", map[string]interface{}{
		"values": map[string]interface{}{
			"api.providers":              map[string]any{"anthropic": map[string]any{"name": "Anthropic", "type": "anthropic_compatible", "base_url": "https://api.anthropic.com", "api_key": "sk-test", "credential_kind": "api-key", "timeout_seconds": 321, "models": map[string]any{"sonnet": map[string]any{"name": "Claude Sonnet", "model": "claude-opus-test", "max_tokens": 4096, "supports_streaming": true, "supports_vision": true}}}},
			"api.timeout_seconds":        321,
			"logging.level":              "debug",
			"channels.feishu.enabled":    true,
			"channels.feishu.domain":     "feishu",
			"channels.feishu.app_id":     "app-id",
			"channels.feishu.app_secret": "app-secret",
			"team.default_skills":        []string{"alpha", "beta"},
			"paths.skills_dir":           ".godex/skills",
			"team.teammate_work_limit":   77,
			"web.token":                  "rotated-token",
		},
	}, cfg.WebToken)
	if updateReq.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(updateReq.Body)
		t.Fatalf("expected config update 200, got %d: %s", updateReq.StatusCode, string(body))
	}
	_ = updateReq.Body.Close()

	cfgReq, err := http.NewRequest(http.MethodGet, server.URL+"/config", nil)
	if err != nil {
		t.Fatalf("new config request: %v", err)
	}
	cfgReq.Header.Set("Authorization", "Bearer rotated-token")
	cfgResp, err := http.DefaultClient.Do(cfgReq)
	if err != nil {
		t.Fatalf("get config view: %v", err)
	}
	defer cfgResp.Body.Close()
	if cfgResp.StatusCode != http.StatusOK {
		t.Fatalf("expected config status 200, got %d", cfgResp.StatusCode)
	}
	var view config.View
	if err := json.NewDecoder(cfgResp.Body).Decode(&view); err != nil {
		t.Fatalf("decode config view: %v", err)
	}
	providers, ok := view.EffectiveValues["api.providers"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected providers map, got %#v", view.EffectiveValues["api.providers"])
	}
	anthropic, ok := providers["anthropic"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected anthropic provider, got %#v", providers)
	}
	models, ok := anthropic["models"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected provider models, got %#v", anthropic)
	}
	sonnet, ok := models["sonnet"].(map[string]interface{})
	if !ok || sonnet["model"] != "claude-opus-test" {
		t.Fatalf("expected updated provider model, got %#v", models)
	}
	if got := view.EffectiveValues["api.timeout_seconds"]; got != float64(321) && got != 321 {
		t.Fatalf("expected updated API timeout, got %#v", got)
	}
	if got := view.EffectiveValues["web.token"]; got != "********" {
		t.Fatalf("expected masked token, got %#v", got)
	}
	if got := view.LastApply.StorageStatus; got != config.StorageStatusSaved {
		t.Fatalf("expected saved storage status, got %q", got)
	}
	if got := view.LastApply.RuntimeStatus; got != config.RuntimeStatusApplied {
		t.Fatalf("expected applied runtime status, got %q", got)
	}

	metaResp := doJSONWithToken(t, http.MethodGet, server.URL+"/config/meta", nil, "rotated-token")
	if metaResp.StatusCode != http.StatusOK {
		t.Fatalf("expected config meta 200, got %d", metaResp.StatusCode)
	}
	defer metaResp.Body.Close()
	var meta config.Meta
	if err := json.NewDecoder(metaResp.Body).Decode(&meta); err != nil {
		t.Fatalf("decode config meta: %v", err)
	}
	if got := meta.LastApply.StorageStatus; got != config.StorageStatusSaved {
		t.Fatalf("expected saved config meta storage status, got %q", got)
	}
	if got := meta.LastApply.RuntimeStatus; got != config.RuntimeStatusApplied {
		t.Fatalf("expected applied config meta runtime status, got %q", got)
	}

	reveal := doJSONWithToken(t, http.MethodPost, server.URL+"/config/reveal", map[string]interface{}{"path": "web.token"}, "rotated-token")
	if reveal.StatusCode != http.StatusOK {
		t.Fatalf("expected reveal status 200, got %d", reveal.StatusCode)
	}
	defer reveal.Body.Close()
	var revealed map[string]string
	if err := json.NewDecoder(reveal.Body).Decode(&revealed); err != nil {
		t.Fatalf("decode reveal response: %v", err)
	}
	if revealed["value"] != "rotated-token" {
		t.Fatalf("unexpected revealed token: %#v", revealed)
	}
}

func TestConfigReloadAPIReadsDiskAndAppliesRuntime(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	meta := manager.Meta()
	data, err := os.ReadFile(meta.HomeConfigFile)
	if err != nil {
		t.Fatalf("read home config: %v", err)
	}
	next := strings.Replace(string(data), "lead_name: lead", "lead_name: disk-web-lead", 1)
	if next == string(data) {
		t.Fatalf("expected lead_name in home config")
	}
	if err := os.WriteFile(meta.HomeConfigFile, []byte(next), 0600); err != nil {
		t.Fatalf("write home config: %v", err)
	}

	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/config/reload", map[string]interface{}{}, cfg.WebToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected reload 200, got %d: %s", resp.StatusCode, string(body))
	}
	var view config.View
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("decode reload view: %v", err)
	}
	if got := view.EffectiveValues["team.lead_name"]; got != "disk-web-lead" {
		t.Fatalf("expected reloaded lead name, got %#v", got)
	}
	if got := manager.Current().LeadName; got != "disk-web-lead" {
		t.Fatalf("expected runtime config to reload, got %q", got)
	}
}

func TestRuntimeServiceAPIReportsStatusAndAcceptsRestart(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{}), commands.NewService(cfg))
	runtime := &stubServiceRuntime{status: map[string]any{"managed": true, "running": true, "name": "godex"}}
	server := httptest.NewServer(NewHandlerWithRuntime(manager, service, nil, nil, nil, nil, runtime, nil))
	defer server.Close()

	statusResp := doJSONWithToken(t, http.MethodGet, server.URL+"/runtime/service", nil, cfg.WebToken)
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("expected runtime status 200, got %d", statusResp.StatusCode)
	}
	var status map[string]any
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatalf("decode runtime status: %v", err)
	}
	if status["managed"] != true {
		t.Fatalf("expected managed runtime status, got %#v", status)
	}

	restartResp := doJSONWithToken(t, http.MethodPost, server.URL+"/runtime/service/restart", map[string]interface{}{}, cfg.WebToken)
	defer restartResp.Body.Close()
	if restartResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(restartResp.Body)
		t.Fatalf("expected restart 202, got %d: %s", restartResp.StatusCode, string(body))
	}
	deadline := time.Now().Add(time.Second)
	for runtime.restarts == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runtime.restarts != 1 {
		t.Fatalf("expected one service restart, got %d", runtime.restarts)
	}
}

func TestControlNodeRegistryEndpoints(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependencies(cfg), commands.NewService(cfg))
	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new node registry: %v", err)
	}
	server := httptest.NewServer(NewHandlerWithRuntime(manager, service, nil, nil, nil, nil, nil, nil, registry))
	defer server.Close()

	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/control/nodes/register", map[string]any{
		"id":            "node-a",
		"name":          "Local A",
		"workspace_dir": "/repo/a",
		"capabilities":  []string{"chat", "tools"},
	}, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status = %d", resp.StatusCode)
	}

	listResp, err := http.Get(server.URL + "/control/nodes")
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	defer listResp.Body.Close()
	var nodes []noderegistry.NodeView
	if err := json.NewDecoder(listResp.Body).Decode(&nodes); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "node-a" || nodes[0].Status != noderegistry.StatusOnline {
		t.Fatalf("unexpected nodes: %#v", nodes)
	}

	heartbeatResp := doJSONWithToken(t, http.MethodPost, server.URL+"/control/nodes/node-a/heartbeat", map[string]any{
		"version": "dev",
	}, "")
	defer heartbeatResp.Body.Close()
	if heartbeatResp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat status = %d", heartbeatResp.StatusCode)
	}

	getResp, err := http.Get(server.URL + "/control/nodes/node-a")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	defer getResp.Body.Close()
	var node noderegistry.NodeView
	if err := json.NewDecoder(getResp.Body).Decode(&node); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	if node.Version != "dev" {
		t.Fatalf("expected heartbeat version, got %#v", node)
	}
}

func TestControlNodeCredentialIssuance(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependencies(cfg), commands.NewService(cfg))
	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new node registry: %v", err)
	}
	server := httptest.NewServer(NewHandlerWithRuntime(manager, service, nil, nil, nil, nil, nil, nil, registry))
	defer server.Close()

	if _, err := registry.Register(context.Background(), noderegistry.NodeInput{ID: "node-a", Name: "Local A"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/control/nodes/node-a/credential", nil, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("issue credential status = %d body=%s", resp.StatusCode, body)
	}
	var issued struct {
		NodeID     string `json:"node_id"`
		Credential string `json:"credential"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&issued); err != nil {
		t.Fatalf("decode issue response: %v", err)
	}
	if issued.NodeID != "node-a" || !strings.HasPrefix(issued.Credential, "ck_") {
		t.Fatalf("unexpected issue response: %#v", issued)
	}

	// The registry must store the hash, and the plaintext must validate.
	node, err := registry.Get(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.CredentialHash == "" {
		t.Fatal("expected stored credential hash")
	}
	if !relay.ValidateCredential(issued.Credential, node.CredentialHash) {
		t.Fatal("issued credential must validate against stored hash")
	}
	if relay.HashCredential(issued.Credential) != node.CredentialHash {
		t.Fatal("stored hash mismatch")
	}
}

func TestControlNodeCredentialIssuanceUnknownNode(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependencies(cfg), commands.NewService(cfg))
	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new node registry: %v", err)
	}
	server := httptest.NewServer(NewHandlerWithRuntime(manager, service, nil, nil, nil, nil, nil, nil, registry))
	defer server.Close()

	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/control/nodes/ghost/credential", nil, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown node, got %d", resp.StatusCode)
	}
}

func TestAutomationCronEndpoints(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}), commands.NewService(cfg))
	cronRuntime := &stubCronAutomation{
		jobs: []automation.CronJob{
			{
				ID:       "job-1",
				Name:     "daily inbox",
				Message:  "check inbox",
				Timezone: "Asia/Shanghai",
				Schedule: automation.CronSchedule{Type: "every", EverySeconds: 3600},
				Enabled:  true,
			},
		},
		runs: map[string][]automation.CronRunLog{
			"job-1": {{ID: "run-1", JobID: "job-1", Status: "completed"}},
		},
	}
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, cronRuntime, nil, nil))
	defer server.Close()

	listResp := doJSONWithToken(t, http.MethodGet, server.URL+"/automation/cron/jobs", nil, cfg.WebToken)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected cron job list 200, got %d", listResp.StatusCode)
	}
	defer listResp.Body.Close()

	createResp := doJSONWithToken(t, http.MethodPost, server.URL+"/automation/cron/jobs", map[string]any{
		"name":     "nightly report",
		"message":  "generate report",
		"timezone": "Asia/Shanghai",
		"schedule": map[string]any{"type": "every", "every_seconds": 7200},
		"enabled":  true,
	}, cfg.WebToken)
	if createResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(createResp.Body)
		t.Fatalf("expected cron create 200, got %d: %s", createResp.StatusCode, string(body))
	}
	_ = createResp.Body.Close()

	runResp := doJSONWithToken(t, http.MethodPost, server.URL+"/automation/cron/jobs/job-1/run", nil, cfg.WebToken)
	if runResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(runResp.Body)
		t.Fatalf("expected cron run 200, got %d: %s", runResp.StatusCode, string(body))
	}
	_ = runResp.Body.Close()

	logsResp := doJSONWithToken(t, http.MethodGet, server.URL+"/automation/cron/jobs/job-1/runs", nil, cfg.WebToken)
	if logsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected cron logs 200, got %d", logsResp.StatusCode)
	}
	defer logsResp.Body.Close()
}

func TestAutomationHeartbeatEndpoints(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}), commands.NewService(cfg))
	heartbeatRuntime := stubHeartbeatAutomation{
		rule: automation.HeartbeatRule{
			ID:              "default",
			Enabled:         true,
			IntervalSeconds: 1800,
			Timezone:        "Asia/Shanghai",
		},
		runs: []automation.HeartbeatRunLog{
			{ID: "hb-run-1", RuleID: "default", Status: "suppressed", Suppressed: true},
		},
	}
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, heartbeatRuntime, nil))
	defer server.Close()

	getResp := doJSONWithToken(t, http.MethodGet, server.URL+"/automation/heartbeat", nil, cfg.WebToken)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected heartbeat get 200, got %d", getResp.StatusCode)
	}
	defer getResp.Body.Close()

	updateResp := doJSONWithToken(t, http.MethodPut, server.URL+"/automation/heartbeat", map[string]any{
		"enabled":          false,
		"interval_seconds": 600,
	}, cfg.WebToken)
	if updateResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(updateResp.Body)
		t.Fatalf("expected heartbeat update 200, got %d: %s", updateResp.StatusCode, string(body))
	}
	_ = updateResp.Body.Close()

	testResp := doJSONWithToken(t, http.MethodPost, server.URL+"/automation/heartbeat/test", nil, cfg.WebToken)
	if testResp.StatusCode != http.StatusOK {
		t.Fatalf("expected heartbeat test 200, got %d", testResp.StatusCode)
	}
	_ = testResp.Body.Close()

	logsResp := doJSONWithToken(t, http.MethodGet, server.URL+"/automation/heartbeat/logs", nil, cfg.WebToken)
	if logsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected heartbeat logs 200, got %d", logsResp.StatusCode)
	}
	defer logsResp.Body.Close()
}

func TestChannelsStatusEndpoint(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	channelManager := rtchannels.NewManager(cfg, service)
	channelManager.SetStatus("weixin", rtchannels.ChannelStatusUpdate{
		Enabled:     boolPtr(true),
		Running:     boolPtr(true),
		State:       rtchannels.StateRunning,
		Detail:      "polling account=default",
		MarkPoll:    true,
		MarkInbound: true,
	})
	server := httptest.NewServer(NewHandler(manager, service, channelManager, nil, nil, nil, nil))
	defer server.Close()

	resp, err := http.Get(server.URL + "/channels")
	if err != nil {
		t.Fatalf("get channels: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	var report rtchannels.StatusReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decode channels report: %v", err)
	}
	if len(report.Channels) != 1 || report.Channels[0].Name != "weixin" || report.Channels[0].State != rtchannels.StateRunning {
		t.Fatalf("unexpected channels report: %#v", report)
	}
}

func TestWeixinAuthEndpoints(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}), commands.NewService(cfg))
	auth := stubWeixinAuth{status: weixin.WebAuthStatus{
		AccountID:  "default",
		Enabled:    true,
		Configured: true,
		StateDir:   filepath.Join(cfg.StateDir, "channels", "weixin", "default"),
		Login: &weixin.WebAuthLogin{
			Active:       true,
			State:        "pending",
			RawStatus:    "wait",
			Message:      "Scan the QR code in Weixin and confirm the login.",
			QRCode:       "qr-1",
			QRCodeImgURL: "https://example.com/qr.png",
		},
	}}
	server := httptest.NewServer(NewHandler(manager, service, nil, auth, nil, nil, nil))
	defer server.Close()

	statusResp := doJSONWithToken(t, http.MethodGet, server.URL+"/channels/weixin/auth", nil, cfg.WebToken)
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("expected weixin auth status 200, got %d", statusResp.StatusCode)
	}
	defer statusResp.Body.Close()
	var status weixin.WebAuthStatus
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatalf("decode weixin auth status: %v", err)
	}
	if status.AccountID != "default" || status.Login == nil || status.Login.QRCode != "qr-1" {
		t.Fatalf("unexpected weixin auth status: %#v", status)
	}

	startResp := doJSONWithToken(t, http.MethodPost, server.URL+"/channels/weixin/auth/start", map[string]string{"account_id": "default"}, cfg.WebToken)
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("expected weixin auth start 200, got %d", startResp.StatusCode)
	}
	startResp.Body.Close()

	logoutResp := doJSONWithToken(t, http.MethodPost, server.URL+"/channels/weixin/auth/logout", map[string]string{"account_id": "default"}, cfg.WebToken)
	if logoutResp.StatusCode != http.StatusOK {
		t.Fatalf("expected weixin auth logout 200, got %d", logoutResp.StatusCode)
	}
	logoutResp.Body.Close()
}

func TestOpenAICompatibleChatCompletionEndpoint(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("openai reply")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/v1/chat/completions", map[string]interface{}{
		"model": "godex-default",
		"messages": []map[string]interface{}{
			{"role": "system", "content": "ignored by compat layer"},
			{"role": "user", "content": "hello"},
		},
		"metadata": map[string]interface{}{"session_id": "ide-session"},
	}, cfg.WebToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	var completion openAIChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	if completion.Object != "chat.completion" || completion.Model != "godex-default" || len(completion.Choices) != 1 {
		t.Fatalf("unexpected completion envelope: %#v", completion)
	}
	if completion.Choices[0].Message == nil || completion.Choices[0].Message.Content != "openai reply" {
		t.Fatalf("unexpected completion content: %#v", completion.Choices)
	}
	sessions, err := service.ListSessions(context.Background(), backend.SessionListFilter{Channel: "openai_api"})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Locator.Key != "ide-session" {
		t.Fatalf("expected openai_api session, got %#v", sessions)
	}
}

func TestOpenAICompatibleChatCompletionStreaming(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("stream reply")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/v1/chat/completions", map[string]interface{}{
		"model":  "godex-default",
		"stream": true,
		"messages": []map[string]interface{}{
			{"role": "user", "content": []map[string]string{{"type": "text", "text": "hello"}}},
		},
	}, cfg.WebToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, `"object":"chat.completion.chunk"`) || !strings.Contains(text, "stream reply") || !strings.Contains(text, "data: [DONE]") {
		t.Fatalf("unexpected stream body: %s", text)
	}
}

func TestOpenCorruptSessionReturnsConflict(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	locator := backend.SessionLocator{Channel: "web", Key: "corrupt"}
	sessionID := testStableSessionID(locator)
	dir := filepath.Join(cfg.SessionsDir, sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	data, err := json.Marshal(backend.SessionManifest{
		SessionID:      sessionID,
		Locator:        locator,
		StateDigest:    "missing",
		CreatedAt:      time.Now().Add(-time.Hour),
		UpdatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions", map[string]any{
		"locator": map[string]any{
			"channel": "web",
			"key":     "corrupt",
		},
	}, cfg.WebToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected corrupt session to return 409, got %d: %s", resp.StatusCode, string(body))
	}
}

func TestDeleteSessionEndpointRemovesPersistedSession(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "delete-me"})
	messageResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]string{"text": "hello"})
	if messageResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected message status 202, got %d", messageResp.StatusCode)
	}
	messageResp.Body.Close()
	waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running && len(snapshot.Messages) >= 2
	})
	if err := os.WriteFile(filepath.Join(cfg.TranscriptsDir, "transcript_delete_me.json"), []byte("bye"), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	statePath := filepath.Join(cfg.SessionsDir, opened.SessionID, "state.json")
	var state agent.SessionState
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if err := json.Unmarshal(stateData, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	state.TranscriptRefs = []string{"transcript_delete_me.json"}
	stateData, err = json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(statePath, stateData, 0644); err != nil {
		t.Fatalf("rewrite state: %v", err)
	}

	manifestPath := filepath.Join(cfg.SessionsDir, opened.SessionID, "manifest.json")
	var manifest backend.SessionManifest
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	sum := sha256.Sum256(stateData)
	manifest.StateDigest = hex.EncodeToString(sum[:])
	manifestData, err = json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	req, err := http.NewRequest(http.MethodDelete, server.URL+"/sessions/"+opened.SessionID, nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected delete 204, got %d: %s", resp.StatusCode, string(body))
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionsDir, opened.SessionID)); !os.IsNotExist(err) {
		t.Fatalf("expected deleted session dir, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.TranscriptsDir, "transcript_delete_me.json")); !os.IsNotExist(err) {
		t.Fatalf("expected deleted transcript, got %v", err)
	}
}

func TestUsageGatewayChatCompletionInvokesMappedProfile(t *testing.T) {
	var providerCalls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected provider path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode provider body: %v", err)
		}
		if body["model"] != "real-model" {
			t.Fatalf("expected target model real-model, got %#v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"proxied ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`))
	}))
	defer provider.Close()

	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: map[string]any{
		"api.providers": map[string]any{
			"openai": map[string]any{
				"type":     "openai_compatible",
				"base_url": provider.URL,
				"api_key":  "sk-test",
				"models": map[string]any{
					"fast": map[string]any{"model": "real-model", "max_tokens": 1024},
				},
			},
		},
	}}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	usageService := usage.NewService(store)
	keyResp, err := usageService.CreateKey(usage.KeyCreateRequest{Name: "client", BudgetCredits: 1000, AllowedModels: []string{"public-fast"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{PublicModel: "public-fast", TargetProfileID: "openai.fast", TargetModel: "real-model", CreditWeight: 2}); err != nil {
		t.Fatal(err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, usageService))
	defer server.Close()

	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/v1/chat/completions", map[string]any{
		"model": "public-fast",
		"messages": []map[string]any{
			{"role": "user", "content": "hello"},
		},
	}, keyResp.Secret)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected gateway status 200, got %d: %s", resp.StatusCode, body)
	}
	if providerCalls != 1 {
		t.Fatalf("expected one provider call, got %d", providerCalls)
	}
	calls, err := usageService.GetCalls(time.Now().Format("2006-01-02"), keyResp.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Status != "success" || calls[0].InputTokens != 11 || calls[0].OutputTokens != 7 || calls[0].Credits != 36 {
		t.Fatalf("unexpected recorded usage: %+v", calls)
	}
}

func TestUsageGatewayRejectsOverBudgetBeforeProviderCall(t *testing.T) {
	providerCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()

	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: map[string]any{
		"api.providers": map[string]any{
			"openai": map[string]any{
				"type":     "openai_compatible",
				"base_url": provider.URL,
				"api_key":  "sk-test",
				"models": map[string]any{
					"fast": map[string]any{"model": "real-model", "max_tokens": 1024},
				},
			},
		},
	}}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	usageService := usage.NewService(store)
	keyResp, err := usageService.CreateKey(usage.KeyCreateRequest{Name: "client", BudgetCredits: 1, AllowedModels: []string{"public-fast"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usageService.CreateModel(usage.ModelCreateRequest{PublicModel: "public-fast", TargetProfileID: "openai.fast", TargetModel: "real-model", CreditWeight: 1}); err != nil {
		t.Fatal(err)
	}
	if err := usageService.RecordCall(&usage.UsageCall{APIKeyID: keyResp.Key.ID, PublicModel: "public-fast", InputTokens: 2, CreditWeight: 1, Status: "success"}); err != nil {
		t.Fatal(err)
	}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}}), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, usageService))
	defer server.Close()

	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/v1/chat/completions", map[string]any{
		"model": "public-fast",
		"messages": []map[string]any{
			{"role": "user", "content": "hello"},
		},
	}, keyResp.Secret)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired && resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected budget rejection, got %d: %s", resp.StatusCode, body)
	}
	if providerCalls != 0 {
		t.Fatalf("expected provider not to be called, got %d calls", providerCalls)
	}
}

func createSession(t *testing.T, baseURL string) backend.OpenedSession {
	t.Helper()
	return createSessionWithLocator(t, baseURL, map[string]string{"channel": "local", "key": "default"})
}

func createSessionWithLocator(t *testing.T, baseURL string, locator map[string]string) backend.OpenedSession {
	t.Helper()
	resp := postJSON(t, baseURL+"/sessions", map[string]interface{}{"locator": locator})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected open session status 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var opened backend.OpenedSession
	if err := json.NewDecoder(resp.Body).Decode(&opened); err != nil {
		t.Fatalf("decode open session response: %v", err)
	}
	return opened
}

func createPersistedSessionWithLocator(t *testing.T, service *backend.Service, baseURL string, locator map[string]string) backend.OpenedSession {
	t.Helper()
	opened := createSessionWithLocator(t, baseURL, locator)
	resp := postJSON(t, baseURL+"/sessions/"+opened.SessionID+"/messages", map[string]string{"text": "hello"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected message status 202, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	waitForHTTPSnapshot(t, service, opened.SessionID, func(snapshot backend.Snapshot) bool {
		return !snapshot.Running && len(snapshot.Messages) >= 2
	})
	return opened
}

func postJSON(t *testing.T, url string, body interface{}) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	resp, err := http.Post(url, "application/json", strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

func doJSONWithToken(t *testing.T, method, url string, body interface{}, token string) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

func waitForHTTPSnapshot(t *testing.T, service *backend.Service, sessionID string, ready func(backend.Snapshot) bool) backend.Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := service.Snapshot(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if ready(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for snapshot condition, last snapshot: %+v", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readFirstSSEDataLine(t *testing.T, reader io.Reader) string {
	t.Helper()
	lines := make(chan string, 1)
	go func() {
		buffered := bufio.NewReader(reader)
		for {
			line, err := buffered.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") {
				lines <- strings.TrimSpace(strings.TrimPrefix(line, "data: "))
				return
			}
		}
	}()
	select {
	case line := <-lines:
		return line
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE data line")
		return ""
	}
}

func uploadAttachment(t *testing.T, url, name, content string) attachmentListResponse {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", name)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		t.Fatalf("new upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload attachment: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected attachment upload status 200, got %d", resp.StatusCode)
	}

	var uploaded attachmentListResponse
	if err := json.NewDecoder(resp.Body).Decode(&uploaded); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	return uploaded
}

func newTestConfig(t *testing.T) *config.Config {
	t.Helper()
	workspace := t.TempDir()
	cfg := &config.Config{
		Model:             "test-model",
		BaseURL:           "http://127.0.0.1",
		MaxTokens:         1024,
		HomeDir:           filepath.Join(workspace, "home"),
		WorkspaceDir:      workspace,
		ProjectDir:        workspace,
		StateDir:          filepath.Join(workspace, ".godex"),
		TeamDir:           filepath.Join(workspace, ".godex", ".team"),
		TasksDir:          filepath.Join(workspace, ".godex", ".tasks"),
		TodosDir:          filepath.Join(workspace, ".godex", ".todos"),
		MemoryDir:         filepath.Join(workspace, ".godex", "memory"),
		RulesDir:          filepath.Join(workspace, ".godex", "rules"),
		SkillsDir:         filepath.Join(workspace, "home", "skills"),
		MCPConfigPath:     filepath.Join(workspace, ".godex", "mcp.json"),
		TempDir:           filepath.Join(workspace, ".godex", ".tmp"),
		TranscriptsDir:    filepath.Join(workspace, ".godex", ".transcripts"),
		SessionsDir:       filepath.Join(workspace, ".godex", ".sessions"),
		CompressThreshold: 100000,
		LeadName:          "lead",
		TeamName:          "default",
	}
	for _, dir := range []string{
		filepath.Join(cfg.TeamDir, "inbox"),
		cfg.TasksDir,
		cfg.TodosDir,
		cfg.MemoryDir,
		cfg.RulesDir,
		cfg.SkillsDir,
		cfg.TempDir,
		cfg.TranscriptsDir,
		cfg.SessionsDir,
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return cfg
}

func writeTestSkill(t *testing.T, skillsDir, name, body string) {
	t.Helper()
	path := filepath.Join(skillsDir, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
}

func newTestManager(t *testing.T, cfg *config.Config) *config.Manager {
	t.Helper()
	manager, err := config.NewManager(config.Options{
		WorkspaceDir: cfg.WorkspaceDir,
		HomeDir:      cfg.HomeDir,
		ConfigPath:   filepath.Join(cfg.WorkspaceDir, "godex.yaml"),
		EnvFile:      filepath.Join(cfg.WorkspaceDir, ".env"),
	})
	if err != nil {
		t.Fatalf("new test manager: %v", err)
	}
	if cfg.WebToken != "" {
		_, err = manager.Update(context.Background(), config.UpdateRequest{
			Values: map[string]interface{}{
				"web.token": cfg.WebToken,
			},
		})
		if err != nil {
			t.Fatalf("seed manager token: %v", err)
		}
	}
	return manager
}

func boolPtr(v bool) *bool {
	return &v
}

func testStableSessionID(locator backend.SessionLocator) string {
	channel := strings.ToLower(strings.TrimSpace(locator.Channel))
	key := strings.TrimSpace(locator.Key)
	userID := strings.TrimSpace(locator.UserID)
	data, _ := json.Marshal(struct {
		Channel string `json:"channel"`
		Key     string `json:"key,omitempty"`
		UserID  string `json:"user_id,omitempty"`
	}{
		Channel: channel,
		Key:     key,
		UserID:  userID,
	})
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%s-%s", channel, hex.EncodeToString(sum[:8]))
}
