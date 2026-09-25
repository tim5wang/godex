package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/flow"
)

func serviceFlowDefinition(serverURL string) *flow.Definition {
	return &flow.Definition{
		FlowID:  "fl_http_service",
		Version: "1",
		Status:  "draft",
		Inputs: []flow.VarDef{
			{Name: "task", Type: "string", Required: true},
			{Name: "meta", Type: "object"},
			{Name: "trace", Type: "string"},
		},
		Outputs: []flow.VarDef{{
			Name:   "result",
			Type:   "object",
			Source: "nodes.call.outputs.body",
		}},
		Network: &flow.NetworkPolicy{
			Policy:            "allowlist",
			AllowedDomains:    []string{"127.0.0.1"},
			AllowPrivateHosts: true,
			TimeoutSeconds:    2,
			MaxResponseChars:  2048,
		},
		Nodes: []flow.Node{{
			ID:   "call",
			Kind: flow.KindService,
			Service: &flow.ServiceSpec{
				Method: "POST",
				URL:    serverURL + "/v1/tasks?q={{inputs.task}}",
				Headers: map[string]string{
					"X-Trace": "{{inputs.trace}}",
				},
				Body: json.RawMessage(`{"task":"{{inputs.task}}","meta":"{{inputs.meta}}"}`),
				Auth: &flow.ServiceAuthSpec{
					Type:     "bearer",
					TokenEnv: "GODEX_TEST_SERVICE_TOKEN",
				},
			},
			Outputs: []flow.VarDef{
				{Name: "status_code", Type: "number"},
				{Name: "body", Type: "object"},
			},
		}},
	}
}

func createAndStartServiceFlow(
	t *testing.T,
	a *Agent,
	def *flow.Definition,
	inputs map[string]any,
) (FlowRunView, string) {
	t.Helper()
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	run, err := a.CreateFlowRun(context.Background(), def.FlowID, "", inputs)
	if err != nil {
		t.Fatalf("create FlowRun: %v", err)
	}
	started, err := a.StartFlowRun(context.Background(), def.FlowID, run.RunID)
	if err != nil {
		t.Fatalf("start FlowRun: %v", err)
	}
	return started, run.WorkflowID
}

func TestWorkflowServiceNodeInterpolatesAndKeepsCredentialsOutOfEvents(t *testing.T) {
	const token = "service-token-never-log"
	t.Setenv("GODEX_TEST_SERVICE_TOKEN", token)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("unexpected Authorization header: %q", got)
		}
		if got := r.Header.Get("X-Trace"); got != "trace-42" {
			t.Errorf("expected interpolated trace header, got %q", got)
		}
		if got := r.URL.Query().Get("q"); got != "transcribe" {
			t.Errorf("expected interpolated query, got %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if body["task"] != "transcribe" {
			t.Errorf("expected task interpolation, got %#v", body["task"])
		}
		meta, ok := body["meta"].(map[string]any)
		if !ok || meta["language"] != "en" {
			t.Errorf("expected typed object interpolation, got %#v", body["meta"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true,"count":3}`))
	}))
	defer server.Close()

	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	started, workflowID := createAndStartServiceFlow(t, a, serviceFlowDefinition(server.URL), map[string]any{
		"task":  "transcribe",
		"meta":  map[string]any{"language": "en"},
		"trace": "trace-42",
	})
	if started.Status != workflowStatusCompleted {
		t.Fatalf("expected completed FlowRun, got %q (%s)", started.Status, started.Error)
	}
	if got := started.Outputs["result"]; got == nil {
		t.Fatalf("expected mapped Flow output, got %#v", started.Outputs)
	}

	state, err := a.workflowState(workflowID)
	if err != nil {
		t.Fatalf("load workflow state: %v", err)
	}
	node := workflowNodeByID(state.Nodes, "call")
	if node == nil || node.Status != workflowStatusCompleted {
		t.Fatalf("expected service node completed, got %+v", node)
	}
	if node.Outputs["status_code"] != float64(http.StatusOK) {
		t.Fatalf("expected status_code output 200, got %#v", node.Outputs["status_code"])
	}
	body, ok := node.Outputs["body"].(map[string]any)
	if !ok || body["accepted"] != true {
		t.Fatalf("expected decoded JSON response body, got %#v", node.Outputs["body"])
	}

	events, err := os.ReadFile(filepath.Join(a.workflows.dir, workflowID, workflowEventsFile))
	if err != nil {
		t.Fatalf("read workflow events: %v", err)
	}
	eventText := string(events)
	for _, sensitive := range []string{token, "transcribe", "trace-42", "accepted"} {
		if strings.Contains(eventText, sensitive) {
			t.Errorf("workflow events unexpectedly contain sensitive/request/response value %q: %s", sensitive, eventText)
		}
	}
}

func TestWorkflowServicePipelineKeepsConcurrentRequestsIndependent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			VideoURL string `json:"video_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode download request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"audio_url": "audio://" + request.VideoURL,
		})
	})
	mux.HandleFunc("/asr", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			AudioURL string `json:"audio_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode ASR request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"transcript": "transcript:" + request.AudioURL,
		})
	})
	mux.HandleFunc("/moderate", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Transcript string `json:"transcript"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode moderation request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"transcript":          request.Transcript,
			"contains_prohibited": strings.Contains(request.Transcript, "video-b"),
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	serviceNode := func(id, path, body string) flow.Node {
		return flow.Node{
			ID:   id,
			Kind: flow.KindService,
			Service: &flow.ServiceSpec{
				Method: "POST",
				URL:    server.URL + path,
				Body:   json.RawMessage(body),
			},
			Outputs: []flow.VarDef{
				{Name: "status_code", Type: "number"},
				{Name: "body", Type: "object"},
			},
		}
	}
	def := &flow.Definition{
		FlowID:  "fl_http_moderation_pipeline",
		Version: "1",
		Status:  "draft",
		Inputs:  []flow.VarDef{{Name: "video_url", Type: "string", Required: true}},
		Outputs: []flow.VarDef{{
			Name:     "moderation",
			Type:     "object",
			Required: true,
			Source:   "nodes.moderate.outputs.body",
		}},
		Network: &flow.NetworkPolicy{
			Policy:            "allowlist",
			AllowedDomains:    []string{"127.0.0.1"},
			AllowPrivateHosts: true,
			TimeoutSeconds:    2,
		},
		Nodes: []flow.Node{
			serviceNode("download", "/download", `{"video_url":"{{inputs.video_url}}"}`),
			serviceNode("asr", "/asr", `{"audio_url":"{{nodes.download.outputs.body.audio_url}}"}`),
			serviceNode("moderate", "/moderate", `{"transcript":"{{nodes.asr.outputs.body.transcript}}"}`),
		},
		Edges: []flow.Edge{
			{ID: "download-to-asr", From: "download", To: "asr", EdgeType: flow.EdgeDataDependency},
			{ID: "asr-to-moderate", From: "asr", To: "moderate", EdgeType: flow.EdgeDataDependency},
		},
	}
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create moderation flow: %v", err)
	}

	type runResult struct {
		view FlowRunView
		err  error
	}
	inputs := []map[string]any{
		{"video_url": "video-a"},
		{"video_url": "video-b"},
	}
	results := make(chan runResult, len(inputs))
	for _, runInputs := range inputs {
		runInputs := runInputs
		go func() {
			run, err := a.CreateFlowRun(context.Background(), def.FlowID, "", runInputs)
			if err != nil {
				results <- runResult{err: err}
				return
			}
			view, err := a.StartFlowRun(context.Background(), def.FlowID, run.RunID)
			results <- runResult{view: view, err: err}
		}()
	}

	seenRunIDs := make(map[string]struct{}, len(inputs))
	moderationByTranscript := make(map[string]bool, len(inputs))
	for range inputs {
		result := <-results
		if result.err != nil {
			t.Fatalf("execute moderation flow: %v", result.err)
		}
		if result.view.Status != workflowStatusCompleted {
			t.Fatalf("expected completed moderation run, got %q (%s)", result.view.Status, result.view.Error)
		}
		if _, duplicate := seenRunIDs[result.view.RunID]; duplicate {
			t.Fatalf("independent requests shared run id %q", result.view.RunID)
		}
		seenRunIDs[result.view.RunID] = struct{}{}
		output, ok := result.view.Outputs["moderation"].(map[string]any)
		if !ok {
			t.Fatalf("expected moderation output object, got %#v", result.view.Outputs["moderation"])
		}
		transcript, _ := output["transcript"].(string)
		if transcript == "" {
			t.Fatalf("missing moderation transcript in output: %#v", output)
		}
		containsProhibited, ok := output["contains_prohibited"].(bool)
		if !ok {
			t.Fatalf("missing moderation verdict in output: %#v", output)
		}
		moderationByTranscript[transcript] = containsProhibited
	}
	if len(seenRunIDs) != len(inputs) || len(moderationByTranscript) != len(inputs) ||
		moderationByTranscript["transcript:audio://video-a"] ||
		!moderationByTranscript["transcript:audio://video-b"] {
		t.Fatalf("requests did not complete as isolated pipelines: runIDs=%v results=%v", seenRunIDs, moderationByTranscript)
	}
}

func TestWorkflowServiceNodeUsesAPIKeyEnvironmentReference(t *testing.T) {
	const key = "api-key-never-log"
	t.Setenv("GODEX_TEST_SERVICE_KEY", key)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Workspace-Key"); got != key {
			t.Errorf("expected API key environment value, got %q", got)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	def := serviceFlowDefinition(server.URL)
	def.FlowID = "fl_http_service_api_key"
	def.Nodes[0].Service.Auth = &flow.ServiceAuthSpec{
		Type:       "api_key",
		TokenEnv:   "GODEX_TEST_SERVICE_KEY",
		HeaderName: "X-Workspace-Key",
	}
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	started, workflowID := createAndStartServiceFlow(t, a, def, map[string]any{
		"task": "classify", "meta": map[string]any{}, "trace": "trace-1",
	})
	if started.Status != workflowStatusCompleted {
		t.Fatalf("expected API-key service call to complete, got %q (%s)", started.Status, started.Error)
	}
	events, err := os.ReadFile(filepath.Join(a.workflows.dir, workflowID, workflowEventsFile))
	if err != nil {
		t.Fatalf("read workflow events: %v", err)
	}
	if strings.Contains(string(events), key) {
		t.Fatalf("API key leaked into workflow events: %s", events)
	}
}

func TestWorkflowServiceNodeRetriesTransientHTTPStatus(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"private":"response-body-not-logged"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	def := serviceFlowDefinition(server.URL)
	def.FlowID = "fl_http_service_retry"
	def.Nodes[0].Service.Auth = nil
	def.Nodes[0].Retry = &flow.RetryPolicy{
		MaxAttempts:        2,
		InitialIntervalMS:  1,
		BackoffCoefficient: 1,
		MaxIntervalMS:      1,
	}
	started, workflowID := createAndStartServiceFlow(t, a, def, map[string]any{
		"task": "classify", "meta": map[string]any{}, "trace": "trace-1",
	})
	if started.Status == workflowStatusCompleted {
		t.Fatal("expected the initial 503 response to schedule a retry")
	}
	time.Sleep(10 * time.Millisecond)
	started, err := a.AdvanceFlowRun(context.Background(), def.FlowID, started.RunID)
	if err != nil {
		t.Fatalf("advance FlowRun for retry: %v", err)
	}
	if started.Status != workflowStatusCompleted || requests.Load() != 2 {
		t.Fatalf("expected retry to complete after two requests, status=%q requests=%d error=%s", started.Status, requests.Load(), started.Error)
	}

	events, err := os.ReadFile(filepath.Join(a.workflows.dir, workflowID, workflowEventsFile))
	if err != nil {
		t.Fatalf("read workflow events: %v", err)
	}
	if strings.Contains(string(events), "response-body-not-logged") {
		t.Fatalf("failed HTTP response body leaked into workflow events: %s", events)
	}
}

func TestWorkflowServiceNodeBlocksPrivateHostUnlessExplicitlyAllowed(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	def := serviceFlowDefinition(server.URL)
	def.FlowID = "fl_http_service_private_blocked"
	def.Network.AllowPrivateHosts = false
	def.Nodes[0].Service.Auth = nil
	started, _ := createAndStartServiceFlow(t, a, def, map[string]any{
		"task": "classify", "meta": map[string]any{}, "trace": "trace-1",
	})
	if started.Status != workflowStatusError {
		t.Fatalf("expected private destination to be blocked, got %q (%s)", started.Status, started.Error)
	}
	if requests.Load() != 0 {
		t.Fatalf("private request reached test server despite default deny: %d", requests.Load())
	}
}

func TestWorkflowServiceNodeHonorsNodeTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			_, _ = w.Write([]byte(`{"too_late":true}`))
		}
	}))
	defer server.Close()

	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	def := serviceFlowDefinition(server.URL)
	def.FlowID = "fl_http_service_timeout"
	def.Network.TimeoutSeconds = 3
	def.Nodes[0].TimeoutSec = 1
	def.Nodes[0].Service.Auth = nil
	started, workflowID := createAndStartServiceFlow(t, a, def, map[string]any{
		"task": "classify", "meta": map[string]any{}, "trace": "trace-1",
	})
	if started.Status != workflowStatusError || !strings.Contains(started.Error, "timed out") {
		t.Fatalf("expected node deadline to fail the service call, status=%q error=%q", started.Status, started.Error)
	}
	state, err := a.workflowState(workflowID)
	if err != nil {
		t.Fatalf("load workflow state: %v", err)
	}
	node := workflowNodeByID(state.Nodes, "call")
	if node == nil || !strings.Contains(node.Error, "timed out") {
		t.Fatalf("expected timeout summary on service node, got %+v", node)
	}
}
