package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/flow"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/usage"
)

// newFlowGatewayTestServer builds a handler with both the backend service
// (flows + steps) and a usage service (biz keys), mirroring production wiring.
func newFlowGatewayTestServer(t *testing.T) (*httptest.Server, *usage.Service) {
	t.Helper()
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, usageService))
	t.Cleanup(server.Close)
	return server, usageService
}

// gatewayFlowDef is a tiny flow that completes without a live worker: a
// decision node that auto-chooses "ok" then a step node.
func gatewayFlowDef() *flow.Definition {
	return &flow.Definition{
		FlowID:  "fl_gateway_e2e",
		Name:    "Gateway E2E",
		Version: "1",
		Status:  "draft",
		Nodes: []flow.Node{
			{ID: "decide", Kind: flow.KindDecision, Prompt: "ok?",
				Decision: &flow.DecisionSpec{DecisionType: "choice", Choices: []flow.Choice{{ID: "ok"}}}},
			{ID: "work", Kind: flow.KindStep, Prompt: "finalize"},
		},
		Edges: []flow.Edge{
			{ID: "e1", From: "decide", To: "work", EdgeType: flow.EdgeDataDependency},
		},
	}
}

func seedPublishedFlow(t *testing.T, server *httptest.Server) {
	t.Helper()
	def := gatewayFlowDef()
	resp, body := doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows", map[string]any{
		"flow_id":    def.FlowID,
		"version":    def.Version,
		"status":     def.Status,
		"definition": def,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create flow: %d %s", resp.StatusCode, body)
	}
	resp, body = doFlowJSON(t, http.MethodPost, server.URL+"/v1/flows/"+def.FlowID+"/versions/1/publish", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish flow: %d %s", resp.StatusCode, body)
	}
}

func doGateway(t *testing.T, server *httptest.Server, secret, route string, body any) (*http.Response, []byte) {
	t.Helper()
	var payload []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal gateway body: %v", err)
		}
		payload = data
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/gateway/"+route, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new gateway request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gateway call: %v", err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

// TestFlowGatewayDispatchesFlowMode verifies a biz key bound to a published
// flow routes POST /v1/gateway/{route} to the flow runtime and returns the
// unified run view.
func TestFlowGatewayDispatchesFlowMode(t *testing.T) {
	server, usageService := newFlowGatewayTestServer(t)
	seedPublishedFlow(t, server)

	created, err := usageService.CreateBizKey(usage.BizKeyCreateRequest{
		Name: "refund-flow", FlowID: "fl_gateway_e2e", Pin: "123456",
	})
	if err != nil {
		t.Fatalf("create biz key: %v", err)
	}

	resp, body := doGateway(t, server, created.Secret, "refund", map[string]any{
		"inputs": map[string]any{"order_id": "o-1"},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, body)
	}
	var view map[string]any
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("unmarshal run view: %v\n%s", err, body)
	}
	if view["run_id"] == nil || view["run_id"] == "" {
		t.Fatalf("expected run_id in gateway response: %s", body)
	}
	if view["flow_id"] != "fl_gateway_e2e" {
		t.Fatalf("expected flow_id, got %v", view["flow_id"])
	}
	if view["status"] != "running" && view["status"] != "completed" {
		t.Fatalf("expected running/completed, got %v", view["status"])
	}
}

// TestFlowGatewayDispatchStepMode verifies a biz key without FlowID routes to
// the single-step agent path (prompt-driven, step response envelope).
func TestFlowGatewayDispatchStepMode(t *testing.T) {
	server, usageService := newFlowGatewayTestServer(t)

	created, err := usageService.CreateBizKey(usage.BizKeyCreateRequest{Name: "query", Pin: "123456"})
	if err != nil {
		t.Fatalf("create biz key: %v", err)
	}

	resp, body := doGateway(t, server, created.Secret, "query", map[string]any{
		"prompt": "look up order 42",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for step dispatch, got %d: %s", resp.StatusCode, body)
	}
	var stepResp map[string]any
	if err := json.Unmarshal(body, &stepResp); err != nil {
		t.Fatalf("unmarshal step response: %v\n%s", err, body)
	}
	if stepResp["step_id"] == nil || stepResp["step_id"] == "" {
		t.Fatalf("expected step_id in gateway step response: %s", body)
	}
}

// TestFlowGatewayRejectsBadKey verifies biz-key auth rejects invalid secrets.
func TestFlowGatewayRejectsBadKey(t *testing.T) {
	server, _ := newFlowGatewayTestServer(t)
	for _, secret := range []string{"", "biz_nope", "gdx_abc"} {
		resp, body := doGateway(t, server, secret, "whatever", map[string]any{"prompt": "hi"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("secret %q: expected 401, got %d: %s", secret, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "error") {
			t.Fatalf("expected error envelope, got %s", body)
		}
	}
}

// TestFlowGatewayIdempotencyKeyDedups verifies flow mode dedups repeated
// idempotency keys to the same run instead of starting a second one.
func TestFlowGatewayIdempotencyKeyDedups(t *testing.T) {
	server, usageService := newFlowGatewayTestServer(t)
	seedPublishedFlow(t, server)

	created, err := usageService.CreateBizKey(usage.BizKeyCreateRequest{
		Name: "refund-idem", FlowID: "fl_gateway_e2e", Pin: "123456",
	})
	if err != nil {
		t.Fatalf("create biz key: %v", err)
	}

	body := map[string]any{"idempotency_key": "dup-1", "inputs": map[string]any{"order_id": "o-1"}}
	resp1, b1 := doGateway(t, server, created.Secret, "refund", body)
	if resp1.StatusCode != http.StatusAccepted {
		t.Fatalf("first call: %d %s", resp1.StatusCode, b1)
	}
	var v1 map[string]any
	if err := json.Unmarshal(b1, &v1); err != nil {
		t.Fatalf("unmarshal first: %v\n%s", err, b1)
	}
	runID1, _ := v1["run_id"].(string)

	// Second call with the same idempotency key must return the same run.
	resp2, b2 := doGateway(t, server, created.Secret, "refund", body)
	if resp2.StatusCode != http.StatusOK && resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("second call: %d %s", resp2.StatusCode, b2)
	}
	var v2 map[string]any
	if err := json.Unmarshal(b2, &v2); err != nil {
		t.Fatalf("unmarshal second: %v\n%s", err, b2)
	}
	if runID2, _ := v2["run_id"].(string); runID2 != runID1 {
		t.Fatalf("expected same run_id for idempotent call: first=%s second=%s", runID1, runID2)
	}
}

// TestBizKeyFlowIDPersistsRoundTrip verifies the FlowID binding survives
// create -> list -> get (store round trip, P2.1).
func TestBizKeyFlowIDPersistsRoundTrip(t *testing.T) {
	_, usageService := newFlowGatewayTestServer(t)
	created, err := usageService.CreateBizKey(usage.BizKeyCreateRequest{
		Name: "flow-persist", FlowID: "fl_crm_onboard", Pin: "123456",
	})
	if err != nil {
		t.Fatalf("create biz key: %v", err)
	}
	if created.Key.FlowID != "fl_crm_onboard" {
		t.Fatalf("expected FlowID on create response, got %q", created.Key.FlowID)
	}
	got, err := usageService.GetBizKey(created.Key.ID)
	if err != nil {
		t.Fatalf("get biz key: %v", err)
	}
	if got.FlowID != "fl_crm_onboard" {
		t.Fatalf("expected FlowID persisted, got %q", got.FlowID)
	}

	// List also carries it.
	keys, err := usageService.ListBizKeys()
	if err != nil {
		t.Fatalf("list biz keys: %v", err)
	}
	found := false
	for _, k := range keys {
		if k.ID == created.Key.ID && k.FlowID == "fl_crm_onboard" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected FlowID in list, got %+v", keys)
	}

	// Update can change the binding.
	unbind := ""
	updated, err := usageService.UpdateBizKey(created.Key.ID, usage.BizKeyUpdateRequest{FlowID: &unbind})
	if err != nil {
		t.Fatalf("update biz key: %v", err)
	}
	if updated.FlowID != "" {
		t.Fatalf("expected FlowID cleared, got %q", updated.FlowID)
	}
}
