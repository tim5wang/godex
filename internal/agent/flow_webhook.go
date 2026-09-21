// P1.3 on_complete webhook: when a FlowRun reaches a terminal state and the
// flow definition declares on_complete, POST the run view to the callback
// URL once (idempotent via flowRunRecord.WebhookSent). The body is the
// FlowRunView JSON; when a secret is configured the request carries
// x-godex-signature: sha256=<HMAC-SHA256(secret, body)>.
package agent

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// isTerminalFlowStatus reports whether a run status is terminal (no further
// node progress possible) — these trigger the on_complete webhook.
func isTerminalFlowStatus(status string) bool {
	switch status {
	case workflowStatusCompleted, workflowStatusError, workflowStatusCanceled:
		return true
	default:
		return false
	}
}

// maybeSendOnComplete delivers the run's completion webhook exactly once. It
// is called after every status refresh; WebhookSent guards re-delivery, and a
// missing/empty on_complete url short-circuits. Failures are best-effort
// (recorded as an event) so a webhook outage never wedges the run.
func (a *Agent) maybeSendOnComplete(flowID, runID string, rec *flowRunRecord) {
	if a == nil || a.flows == nil {
		return
	}
	if rec == nil || rec.WebhookSent || !isTerminalFlowStatus(rec.Status) {
		return
	}
	// Resolve the version to read its on_complete spec.
	version, err := a.flows.loadVersion(flowID, rec.Version)
	if err != nil || version.Flow == nil || version.Flow.OnComplete == nil {
		return
	}
	spec := version.Flow.OnComplete
	url := strings.TrimSpace(spec.URL)
	if url == "" {
		return
	}
	// Mark sent before the network call so a concurrent refresh cannot
	// double-deliver (at-most-once per record).
	rec.WebhookSent = true
	_ = a.flows.saveRun(flowID, *rec)

	body, err := json.Marshal(flowRunView(*rec))
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if secret := strings.TrimSpace(spec.Secret); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		req.Header.Set("x-godex-signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		_ = a.workflows.appendEvent(rec.WorkflowID, map[string]interface{}{
			"event": "webhook_failed", "run_id": runID, "error": err.Error(), "at": time.Now().UTC(),
		})
		return
	}
	defer resp.Body.Close()
	_ = a.workflows.appendEvent(rec.WorkflowID, map[string]interface{}{
		"event": "webhook_sent", "run_id": runID, "status_code": resp.StatusCode, "at": time.Now().UTC(),
	})
}
