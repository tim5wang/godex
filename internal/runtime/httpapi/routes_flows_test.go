package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/flow"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

func newFlowsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	t.Cleanup(server.Close)
	return server
}

// newFlowsTestServerWithCaller is newFlowsTestServer with a custom LLM caller
// (used to drive the natural-language flow generation endpoint).
func newFlowsTestServerWithCaller(t *testing.T, caller conversation.Caller) *httptest.Server {
	t.Helper()
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	t.Cleanup(server.Close)
	return server
}

func doFlowJSON(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var payload []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		payload = data
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func flowTestHTTPDef() *flow.Definition {
	return &flow.Definition{
		FlowID:  "fl_http_e2e",
		Name:    "HTTP E2E",
		Version: "1",
		Status:  "draft",
		Nodes: []flow.Node{
			{ID: "decide", Kind: flow.KindDecision, Prompt: "auto?",
				Decision: &flow.DecisionSpec{DecisionType: "choice", Choices: []flow.Choice{{ID: "auto"}, {ID: "llm"}}}},
			{ID: "auto_run", Kind: flow.KindHuman, Prompt: "approve the refund",
				Human: &flow.HumanSpec{Queue: "ops", ResultVar: "approved"}},
			{ID: "br", Kind: flow.KindBranch,
				Branch: &flow.BranchSpec{
					Cases:     []flow.BranchCase{{Name: "auto", To: "auto_run", Condition: flow.Condition{Choice: "auto"}}},
					DefaultTo: "auto_run",
				}},
		},
		Edges: []flow.Edge{
			{ID: "e1", From: "decide", To: "br", EdgeType: flow.EdgeDataDependency},
		},
	}
}

func TestFlowsCreateListPublishRuns(t *testing.T) {
	server := newFlowsTestServer(t)

	// POST /v1/flows — create draft.
	resp, raw := doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows", map[string]any{
		"flow_id":    "fl_http_e2e",
		"version":    "1",
		"status":     "draft",
		"definition": flowTestHTTPDef(),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body: %s", resp.StatusCode, raw)
	}
	var created agent.FlowVersionView
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.FlowID != "fl_http_e2e" || created.Status != "draft" || created.Digest == "" {
		t.Fatalf("unexpected created: %+v", created)
	}

	// GET /v1/flows — list.
	resp, raw = doFlowJSON(t, http.MethodGet, server.URL+"/v1/flows", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var summaries []agent.FlowSummaryView
	if err := json.Unmarshal(raw, &summaries); err != nil {
		t.Fatalf("decode summaries: %v", err)
	}
	if len(summaries) != 1 || summaries[0].FlowID != "fl_http_e2e" {
		t.Fatalf("unexpected summaries: %+v", summaries)
	}

	// POST publish.
	resp, raw = doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows/fl_http_e2e/versions/1/publish", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish status = %d, body: %s", resp.StatusCode, raw)
	}
	var pub agent.FlowVersionView
	if err := json.Unmarshal(raw, &pub); err != nil {
		t.Fatalf("decode published: %v", err)
	}
	if pub.Status != "published" {
		t.Fatalf("expected published, got %q", pub.Status)
	}

	// POST /v1/flows/{id}/runs — create + start.
	resp, raw = doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows/fl_http_e2e/runs", map[string]any{
		"inputs": map[string]any{"order_id": "o-42"},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d, body: %s", resp.StatusCode, raw)
	}
	var run agent.FlowRunView
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.FlowID != "fl_http_e2e" || run.RunID == "" || run.WorkflowID == "" {
		t.Fatalf("unexpected run: %+v", run)
	}

	// GET /v1/flows/{id}/runs — list.
	resp, raw = doFlowJSON(t, http.MethodGet, server.URL+"/v1/flows/fl_http_e2e/runs", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("runs list status = %d", resp.StatusCode)
	}
	var runs []agent.FlowRunView
	if err := json.Unmarshal(raw, &runs); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}

	// POST /v1/flow-runs/{runID}/cancel.
	resp, raw = doFlowJSON(t, http.MethodPost, server.URL+"/v1/flow-runs/"+run.RunID+"/cancel?flow_id=fl_http_e2e", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, body: %s", resp.StatusCode, raw)
	}
	var canceled agent.FlowRunView
	if err := json.Unmarshal(raw, &canceled); err != nil {
		t.Fatalf("decode canceled: %v", err)
	}
	if canceled.Status != "canceled" {
		t.Fatalf("expected canceled, got %q", canceled.Status)
	}
}

func TestFlowsValidateRejectsBadDefinition(t *testing.T) {
	server := newFlowsTestServer(t)
	def := flowTestHTTPDef()
	def.Nodes[0].Prompt = "" // decision requires prompt
	resp, raw := doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows/fl_http_e2e/versions/1/validate", def)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("validate status = %d, body: %s", resp.StatusCode, raw)
	}
}

// flowHumanHTTPDef returns a flow whose second node is a human fallback, so
// running it creates a human task that can be listed and replied to.
func flowHumanHTTPDef() *flow.Definition {
	return &flow.Definition{
		FlowID:  "fl_http_human",
		Name:    "HTTP Human",
		Version: "1",
		Status:  "draft",
		Nodes: []flow.Node{
			{ID: "decide", Kind: flow.KindDecision, Prompt: "approve?",
				Decision: &flow.DecisionSpec{DecisionType: "choice", Choices: []flow.Choice{{ID: "yes"}, {ID: "no"}}}},
			{ID: "approve", Kind: flow.KindHuman, Prompt: "Approve this?",
				Human: &flow.HumanSpec{Queue: "ops", ResultVar: "approved"}},
			{ID: "end", Kind: flow.KindStep, Prompt: "finalize"},
		},
		Edges: []flow.Edge{
			{ID: "e1", From: "decide", To: "approve", EdgeType: flow.EdgeDataDependency},
			{ID: "e2", From: "approve", To: "end", EdgeType: flow.EdgeDataDependency},
		},
	}
}

func TestFlowsHumanTaskListAndReply(t *testing.T) {
	server := newFlowsTestServer(t)

	// Create + publish the human flow.
	resp, raw := doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows", map[string]any{
		"flow_id":    "fl_http_human",
		"version":    "1",
		"status":     "draft",
		"definition": flowHumanHTTPDef(),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body: %s", resp.StatusCode, raw)
	}
	resp, raw = doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows/fl_http_human/versions/1/publish", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish status = %d, body: %s", resp.StatusCode, raw)
	}

	// Run it: the human node registers a task and the run waits.
	resp, raw = doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows/fl_http_human/runs", map[string]any{
		"inputs": map[string]any{"order_id": "o-1"},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d, body: %s", resp.StatusCode, raw)
	}
	var run agent.FlowRunView
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}

	// GET /v1/human-tasks — the pending task is listed.
	resp, raw = doFlowJSON(t, http.MethodGet, server.URL+"/v1/human-tasks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("human tasks status = %d", resp.StatusCode)
	}
	var tasks []agent.HumanTaskView
	if err := json.Unmarshal(raw, &tasks); err != nil {
		t.Fatalf("decode tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].NodeID != "approve" || tasks[0].Status != "pending" {
		t.Fatalf("unexpected human tasks: %+v", tasks)
	}

	// POST /v1/flow-runs/{runID}/human/{nodeID}/reply — complete + continue.
	resp, raw = doFlowJSON(t, http.MethodPost, server.URL+"/v1/flow-runs/"+run.RunID+"/human/approve/reply?flow_id=fl_http_human", map[string]any{
		"value": map[string]any{"ok": true},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reply status = %d, body: %s", resp.StatusCode, raw)
	}
	var replied agent.FlowRunView
	if err := json.Unmarshal(raw, &replied); err != nil {
		t.Fatalf("decode replied: %v", err)
	}
	if replied.Status == "error" {
		t.Fatalf("run errored after reply: %+v", replied)
	}

	// The task is now replied.
	resp, raw = doFlowJSON(t, http.MethodGet, server.URL+"/v1/human-tasks?queue=ops", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("human tasks status = %d", resp.StatusCode)
	}
	var after []agent.HumanTaskView
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatalf("decode tasks: %v", err)
	}
	if len(after) != 1 || after[0].Status != "replied" {
		t.Fatalf("expected task replied, got %+v", after)
	}
}

func TestFlowsRunEventsSSE(t *testing.T) {
	server := newFlowsTestServer(t)

	// Create + publish a human flow and start a run (the human node keeps the
	// run alive while we read the event stream).
	resp, raw := doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows", map[string]any{
		"flow_id":    "fl_http_human",
		"version":    "1",
		"status":     "draft",
		"definition": flowHumanHTTPDef(),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body: %s", resp.StatusCode, raw)
	}
	resp, raw = doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows/fl_http_human/versions/1/publish", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish status = %d, body: %s", resp.StatusCode, raw)
	}
	resp, raw = doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows/fl_http_human/runs", map[string]any{
		"inputs": map[string]any{"order_id": "o-1"},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d, body: %s", resp.StatusCode, raw)
	}
	var run agent.FlowRunView
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}

	// Open the SSE stream and read the first chunk of events.
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/flow-runs/"+run.RunID+"/events?flow_id=fl_http_human", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	respStream, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	defer respStream.Body.Close()
	if ct := respStream.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	// Read enough of the stream to observe at least one event line. The run is
	// alive (waiting on the human node), so the stream stays open; we read a
	// bounded prefix and assert the "created"/"start" events appear.
	buf := make([]byte, 64*1024)
	n, _ := respStream.Body.Read(buf)
	if n == 0 {
		t.Fatal("expected SSE data, got none")
	}
	chunk := string(buf[:n])
	if !strings.Contains(chunk, "created") && !strings.Contains(chunk, "start") {
		t.Fatalf("expected workflow events in SSE stream, got: %s", chunk)
	}
}

// TestFlowsGenerateEndpoint verifies POST /v1/flows/generate drafts a Flow
// Spec from a natural-language description via the LLM and returns it without
// saving (P2.5).
func TestFlowsGenerateEndpoint(t *testing.T) {
	flowJSON := `{"flow_id": "fl_gen", "version": "1", "status": "draft",
	  "nodes": [
	    {"id": "classify", "kind": "step", "prompt": "classify the request"},
	    {"id": "decide", "kind": "decision", "prompt": "auto?",
	      "decision": {"decision_type": "choice", "choices": [{"id": "auto"}, {"id": "manual"}]}},
	    {"id": "br", "kind": "branch",
	      "branch": {"cases": [{"name": "auto", "to": "done", "condition": {"choice": "auto"}}], "default_to": "done"}},
	    {"id": "done", "kind": "step", "prompt": "finalize"}
	  ],
	  "edges": [
	    {"id": "e1", "from": "classify", "to": "decide", "edge_type": "data_dependency"},
	    {"id": "e2", "from": "decide", "to": "br", "edge_type": "data_dependency"}
	  ]
	}`
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock(flowJSON)}},
	}}
	server := newFlowsTestServerWithCaller(t, caller)

	resp, raw := doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows/generate", map[string]any{
		"description": "退款流程：自动退款或转人工",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("generate status = %d, body: %s", resp.StatusCode, raw)
	}
	var def flow.Definition
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatalf("decode definition: %v", err)
	}
	if def.FlowID != "fl_gen" || len(def.Nodes) != 4 {
		t.Fatalf("unexpected generated definition: %+v", def)
	}

	// The generated draft is NOT saved: the flow list stays empty.
	listResp, listRaw := doFlowJSON(t, http.MethodGet, server.URL+"/v1/flows", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", listResp.StatusCode)
	}
	var items []agent.FlowSummaryView
	if err := json.Unmarshal(listRaw, &items); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no flows saved after generate, got %+v", items)
	}
}

// TestFlowsGenerateInvalidDraftCarriesDraft verifies POST /v1/flows/generate
// surfaces a *FlowSpecDraftError as a JSON body with the near-correct draft +
// raw LLM output (8ebefd4 + 复盘 #4), so an HTTP caller can amend instead of
// losing the work — never a bare text error.
func TestFlowsGenerateInvalidDraftCarriesDraft(t *testing.T) {
	// Parseable but invalid: edge targets an unknown node.
	raw := `{"flow_id": "fl_bad", "version": "1", "status": "draft",
	  "nodes": [{"id": "a", "kind": "step", "prompt": "do"}],
	  "edges": [{"id": "e1", "from": "a", "to": "ghost", "edge_type": "data_dependency"}]
	}`
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock(raw)}},
	}}
	server := newFlowsTestServerWithCaller(t, caller)

	resp, body := doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows/generate", map[string]any{
		"description": "bad flow",
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", resp.StatusCode, body)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, body)
	}
	if payload["stage"] != "draft" {
		t.Fatalf("expected stage=draft in error body, got %+v", payload)
	}
	if payload["draft"] == nil {
		t.Fatal("expected near-correct draft in error body, got none")
	}
	rawOut, _ := payload["raw_output"].(string)
	if !strings.Contains(rawOut, "fl_bad") {
		t.Fatalf("expected raw_output preserved in error body, got %q", rawOut)
	}
	if msg, _ := payload["error"].(string); !strings.Contains(msg, "unknown node") {
		t.Fatalf("expected validation message in error body, got %q", msg)
	}
}

// TestFlowsCreateEmptyDraft verifies creating a flow with only basic identity
// (no definition) succeeds and the flow shows up in the list (P2.5 create
// object first, fill in content later).
func TestFlowsCreateEmptyDraft(t *testing.T) {
	server := newFlowsTestServer(t)

	resp, raw := doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows", map[string]any{
		"flow_id": "fl_empty_http",
		"version": "1",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create empty status = %d, body: %s", resp.StatusCode, raw)
	}
	var view agent.FlowVersionView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if view.Nodes != 0 {
		t.Fatalf("expected 0 nodes, got %d", view.Nodes)
	}

	listResp, listRaw := doFlowJSON(t, http.MethodGet, server.URL+"/v1/flows", nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", listResp.StatusCode)
	}
	var items []agent.FlowSummaryView
	if err := json.Unmarshal(listRaw, &items); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, it := range items {
		if it.FlowID == "fl_empty_http" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected fl_empty_http in flow list, got %+v", items)
	}
}

func TestFlowsRunEventsPoll(t *testing.T) {
	server := newFlowsTestServer(t)

	resp, raw := doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows", map[string]any{
		"flow_id":    "fl_http_poll",
		"version":    "1",
		"status":     "draft",
		"definition": flowHumanHTTPDef(),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body: %s", resp.StatusCode, raw)
	}
	resp, raw = doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows/fl_http_poll/versions/1/publish", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish status = %d, body: %s", resp.StatusCode, raw)
	}
	resp, raw = doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows/fl_http_poll/runs", map[string]any{
		"inputs": map[string]any{"order_id": "o-poll"},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d, body: %s", resp.StatusCode, raw)
	}
	var run agent.FlowRunView
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/flow-runs/"+run.RunID+"/events?flow_id=fl_http_poll&poll=1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	respEvents, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	defer respEvents.Body.Close()
	if ct := respEvents.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected application/json for poll=1, got %q", ct)
	}
	body, _ := io.ReadAll(respEvents.Body)
	var events []map[string]any
	if err := json.Unmarshal(body, &events); err != nil {
		t.Fatalf("decode events: %v (body: %s)", err, body)
	}
	seen := false
	for _, ev := range events {
		if ev["event"] == "created" || ev["event"] == "start" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("expected created/start in poll events, got %s", body)
	}
}
