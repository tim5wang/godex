package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/usage"
)

// ---------------------------------------------------------------------------
// Flow Gateway (P2.1, design doc §16 decision 8)
//
// POST /v1/gateway/{route} is the single business-facing entry: one prefix
// for both single-step (agent-step) and multi-step (flow) dispatch. The
// authenticated biz key decides the target:
//
//   - key.FlowID set  -> multi-step flow run (created, started, optionally
//     waited on); response is agent.FlowRunView {run_id, status, outputs,
//     ...} (design: "{run_id, status, outputs|callback}").
//   - otherwise       -> single-step agent call (same behavior as
//     POST /v1/agent-steps); response is stepResponse {step_id, status,
//     output, ...}.
//
// Responsibilities implemented here: routing, biz-key auth (withBizKeyAuth),
// durable idempotency-key dedup (flow mode), and a unified error envelope.
// ---------------------------------------------------------------------------

// gatewayRequest is the union of step and flow call fields. Flow mode uses
// Inputs/WaitMS/IdempotencyKey; step mode uses the embedded step fields
// (handled by handleAgentStep which reads the same body).
type gatewayRequest struct {
	StepID           string                `json:"step_id,omitempty"`
	SessionID        string                `json:"session_id,omitempty"`
	Prompt           string                `json:"prompt,omitempty"`
	Inputs           map[string]any        `json:"inputs,omitempty"`
	Context          *stepContext          `json:"context,omitempty"`
	Tools            *stepTools            `json:"tools,omitempty"`
	Model            string                `json:"model,omitempty"`
	TimeoutSec       int                   `json:"timeout_seconds,omitempty"`
	StructuredOutput *stepStructuredOutput `json:"structured_output,omitempty"`
	// Flow fields.
	Version string `json:"version,omitempty"` // explicit flow version; empty = published
	WaitMS  int    `json:"wait_ms,omitempty"` // synchronous wait for flow completion
	// IdempotencyKey dedups flow-mode calls: a repeated key returns the same
	// durable run instead of starting a second one.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// gatewayErrorBody is the unified error envelope.
type gatewayErrorBody struct {
	Error gatewayErrorDetail `json:"error"`
}

type gatewayErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Route   string `json:"route,omitempty"`
	RunID   string `json:"run_id,omitempty"`
	StepID  string `json:"step_id,omitempty"`
}

func writeGatewayError(w http.ResponseWriter, status int, code string, err error, route, runID, stepID string) {
	writeJSON(w, status, gatewayErrorBody{Error: gatewayErrorDetail{
		Code:    code,
		Message: err.Error(),
		Route:   route,
		RunID:   runID,
		StepID:  stepID,
	}})
}

// gatewayIdemStore serializes same-key requests inside one process. The
// durable replay identity itself lives in the FlowRun record.
type gatewayIdemStore struct {
	mu    sync.Mutex
	locks map[string]*gatewayIdemLock
}

type gatewayIdemLock struct {
	mu   sync.Mutex
	refs int
}

func newGatewayIdemStore() *gatewayIdemStore {
	return &gatewayIdemStore{locks: make(map[string]*gatewayIdemLock)}
}

func (s *gatewayIdemStore) lock(key string) func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	lock := s.locks[key]
	if lock == nil {
		lock = &gatewayIdemLock{}
		s.locks[key] = lock
	}
	lock.refs++
	s.mu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.locks, key)
		}
		s.mu.Unlock()
	}
}

// registerFlowGatewayRoutes registers POST /v1/gateway/{route} (biz-key
// auth). route is the business-facing route name (idempotency namespace +
// audit); the dispatch target comes from the authenticated key's FlowID
// binding. Named flowGatewayRoutes to avoid clashing with the usage gateway
// registerGatewayRoutes (OpenAI chat completions proxy).
func registerFlowGatewayRoutes(mux *http.ServeMux, usageService *usage.Service, service *backend.Service) {
	if service == nil || usageService == nil {
		return
	}
	idem := newGatewayIdemStore()
	mux.Handle("POST /v1/gateway/{route}", withBizKeyAuth(usageService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleFlowGateway(w, r, service, idem)
	})))
}

func handleFlowGateway(w http.ResponseWriter, r *http.Request, service *backend.Service, idem *gatewayIdemStore) {
	route := r.PathValue("route")
	key := BizKeyFromContext(r.Context())
	if key == nil {
		writeGatewayError(w, http.StatusUnauthorized, "unauthorized", fmt.Errorf("missing biz key"), route, "", "")
		return
	}
	if flowID := strings.TrimSpace(key.FlowID); flowID != "" {
		handleFlowGatewayFlow(w, r, service, idem, route, flowID, key.ID)
		return
	}
	// Single-step mode: same behavior as POST /v1/agent-steps (biz key in
	// context drives capability assembly inside handleAgentStep).
	handleAgentStep(w, r, service)
}

// handleFlowGatewayFlow dispatches to the multi-step flow runtime: create a
// run for the key-bound flow, start it, optionally wait synchronously, and
// honor idempotency-key dedup. The response is the flow run view
// {run_id, status, outputs, ...}.
func handleFlowGatewayFlow(w http.ResponseWriter, r *http.Request, service *backend.Service, idem *gatewayIdemStore, route, flowID, bizKeyID string) {
	var req gatewayRequest
	if err := decodeJSONAllowEmpty(r, &req); err != nil {
		writeGatewayError(w, http.StatusBadRequest, "invalid_request", err, route, "", "")
		return
	}
	ctx := r.Context()

	idemKey := strings.TrimSpace(req.IdempotencyKey)
	keyHash, requestHash := "", ""
	if idemKey != "" {
		keyHash = gatewayIdempotencyKeyHash(route, bizKeyID, flowID, idemKey)
		unlock := idem.lock(keyHash)
		defer unlock()
		var err error
		requestHash, err = gatewayFlowRequestHash(req.Version, req.Inputs)
		if err != nil {
			writeGatewayError(w, http.StatusBadRequest, "invalid_request", err, route, "", "")
			return
		}
	}

	run, created, err := service.CreateFlowRunIdempotent(ctx, flowID, req.Version, req.Inputs, keyHash, requestHash)
	if err != nil {
		if errors.Is(err, agent.ErrFlowIdempotencyConflict) {
			writeGatewayError(w, http.StatusConflict, "idempotency_conflict", err, route, "", "")
			return
		}
		writeGatewayError(w, statusForFlowError(err), "flow_create_failed", err, route, "", "")
		return
	}
	started := run
	if created || run.Status == "pending" {
		started, err = service.StartFlowRun(ctx, flowID, run.RunID)
	} else {
		started, err = service.RefreshFlowRun(flowID, run.RunID)
	}
	if err != nil {
		writeGatewayError(w, statusForFlowError(err), "flow_start_failed", err, route, run.RunID, "")
		return
	}
	if req.WaitMS > 0 {
		started, err = service.WaitFlowRun(ctx, flowID, run.RunID, req.WaitMS)
		if err != nil {
			writeGatewayError(w, statusForFlowError(err), "flow_wait_failed", err, route, run.RunID, "")
			return
		}
	}
	status := http.StatusAccepted
	if !created || (req.WaitMS > 0 && isTerminalFlowRunStatus(started.Status)) {
		status = http.StatusOK
	}
	writeJSON(w, status, started)
}

func isTerminalFlowRunStatus(status string) bool {
	switch status {
	case "completed", "failed", "error", "canceled", "cancelled":
		return true
	default:
		return false
	}
}

func gatewayIdempotencyKeyHash(route, bizKeyID, flowID, key string) string {
	sum := sha256.Sum256([]byte(route + "\x00" + bizKeyID + "\x00" + flowID + "\x00" + key))
	return hex.EncodeToString(sum[:])
}

func gatewayFlowRequestHash(version string, inputs map[string]any) (string, error) {
	if inputs == nil {
		inputs = map[string]any{}
	}
	raw, err := json.Marshal(struct {
		Version string         `json:"version"`
		Inputs  map[string]any `json:"inputs"`
	}{strings.TrimSpace(version), inputs})
	if err != nil {
		return "", fmt.Errorf("encode idempotency request fingerprint: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
