package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/decision"
	"github.com/tim5wang/godex/internal/core/flow"
)

// flowWebhookTestDef returns a flow with an on_complete callback so a
// terminal run POSTs the run view to the webhook (P1.3).
func flowWebhookTestDef(callbackURL string) *flow.Definition {
	return &flow.Definition{
		FlowID:      "fl_webhook",
		Name:        "Webhook",
		Description: "decision -> end with on_complete",
		Version:     "1",
		Status:      "draft",
		Nodes: []flow.Node{
			{ID: "decide", Kind: flow.KindDecision, Prompt: "go?",
				Decision: &flow.DecisionSpec{DecisionType: "choice", Choices: []flow.Choice{{ID: "yes"}, {ID: "no"}}}},
			{ID: "end", Kind: flow.KindStep, Prompt: "finalize"},
		},
		Edges: []flow.Edge{
			{ID: "e1", From: "decide", To: "end", EdgeType: flow.EdgeDataDependency},
		},
		OnComplete: &flow.OnCompleteSpec{URL: callbackURL, Secret: "s3cret"},
	}
}

// webhookCapture is a minimal httptest receiver that records delivered
// payloads and their signature header.
type webhookCapture struct {
	mu      sync.Mutex
	payload []map[string]any
	raw     [][]byte
	sigs    []string
	count   int
}

func (c *webhookCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	c.sigs = append(c.sigs, r.Header.Get("x-godex-signature"))
	raw := make([]byte, 0, 4096)
	buf := make([]byte, 2048)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			raw = append(raw, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	c.raw = append(c.raw, raw)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err == nil {
		c.payload = append(c.payload, body)
	}
	w.WriteHeader(http.StatusOK)
}

func (c *webhookCapture) snapshotCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

func TestFlowOnCompleteWebhookDeliveredOnce(t *testing.T) {
	capture := &webhookCapture{}
	srv := httptest.NewServer(capture)
	defer srv.Close()

	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.SetDecisionCaller(&scriptedDecisionCaller{result: decision.Result{Choice: "yes", Confidence: 0.9}})

	if _, err := a.CreateFlow(FlowCreateArgs{Def: flowWebhookTestDef(srv.URL)}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if _, err := a.PublishFlow("fl_webhook", "1"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_webhook", "", map[string]any{"order_id": "o-1"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := a.StartFlowRun(ctx, "fl_webhook", run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}

	// Decision completes synchronously; end is a subagent job that needs a
	// worker in tests. Cancel the run to reach a terminal state, then refresh
	// to trigger the webhook (P1.3).
	if _, err := a.CancelFlowRun(ctx, "fl_webhook", run.RunID); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if _, err := a.RefreshFlowRun("fl_webhook", run.RunID); err != nil {
		t.Fatalf("refresh run: %v", err)
	}

	// Give the async webhook delivery a moment, then assert exactly one POST.
	deadline := time.Now().Add(3 * time.Second)
	for capture.snapshotCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := capture.snapshotCount(); got != 1 {
		t.Fatalf("expected exactly 1 webhook delivery, got %d", got)
	}
	capture.mu.Lock()
	payload := capture.payload[0]
	sig := capture.sigs[0]
	capture.mu.Unlock()

	if payload["run_id"] != run.RunID || payload["flow_id"] != "fl_webhook" {
		t.Fatalf("unexpected webhook payload: %+v", payload)
	}
	if payload["status"] != "canceled" {
		t.Fatalf("expected canceled status in payload, got %v", payload["status"])
	}

	// Signature header must be the HMAC-SHA256 of the exact sent body bytes
	// (field order preserved), verified against the raw captured payload.
	mac := hmac.New(sha256.New, []byte("s3cret"))
	_, _ = mac.Write(capture.raw[0])
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig != expected {
		t.Fatalf("signature mismatch: got %q want %q", sig, expected)
	}

	// A second refresh must NOT re-deliver (WebhookSent guard).
	if _, err := a.RefreshFlowRun("fl_webhook", run.RunID); err != nil {
		t.Fatalf("refresh run 2: %v", err)
	}
	if got := capture.snapshotCount(); got != 1 {
		t.Fatalf("expected no re-delivery on refresh, count=%d", got)
	}
}
