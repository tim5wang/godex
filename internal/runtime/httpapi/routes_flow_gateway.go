package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

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
// idempotency-key dedup (flow mode), unified error envelope. Rate limiting
// and input-schema validation land with P2.3 (variable schema).
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
	// run instead of starting a second one (P2.1 flow mode).
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

// gatewayIdemStore is a small in-process dedup map (flow mode). Keyed by
// route + biz key id + idempotency key -> run id. Entries expire to bound
// memory; a restart simply loses dedup memory (runs are durable).
type gatewayIdemStore struct {
	mu    sync.Mutex
	items map[string]gatewayIdemEntry
}

type gatewayIdemEntry struct {
	RunID string
	Until time.Time
}

func newGatewayIdemStore() *gatewayIdemStore {
	return &gatewayIdemStore{items: make(map[string]gatewayIdemEntry)}
}

func (s *gatewayIdemStore) lookup(key string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[key]
	if !ok || time.Now().After(e.Until) {
		if ok {
			delete(s.items, key)
		}
		return "", false
	}
	return e.RunID, true
}

func (s *gatewayIdemStore) put(key, runID string, ttl time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key] = gatewayIdemEntry{RunID: runID, Until: time.Now().Add(ttl)}
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
		handleFlowGatewayFlow(w, r, service, idem, route, flowID)
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
func handleFlowGatewayFlow(w http.ResponseWriter, r *http.Request, service *backend.Service, idem *gatewayIdemStore, route, flowID string) {
	var req gatewayRequest
	if err := decodeJSONAllowEmpty(r, &req); err != nil {
		writeGatewayError(w, http.StatusBadRequest, "invalid_request", err, route, "", "")
		return
	}
	ctx := r.Context()

	idemKey := strings.TrimSpace(req.IdempotencyKey)
	if idemKey != "" {
		if existing, ok := idem.lookup(route + "|" + flowID + "|" + idemKey); ok {
			if run, err := service.RefreshFlowRun(flowID, existing); err == nil {
				writeJSON(w, http.StatusOK, run)
				return
			}
			// Stale/expired run record: fall through and start a new one.
		}
	}

	run, err := service.CreateFlowRun(ctx, flowID, req.Version, req.Inputs)
	if err != nil {
		writeGatewayError(w, statusForFlowError(err), "flow_create_failed", err, route, "", "")
		return
	}
	started, err := service.StartFlowRun(ctx, flowID, run.RunID)
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
	if idemKey != "" {
		idem.put(route+"|"+flowID+"|"+idemKey, run.RunID, 10*time.Minute)
	}
	writeJSON(w, http.StatusAccepted, started)
}
