package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/flow"
	"github.com/tim5wang/godex/internal/services/backend"
)

// flowService is the subset of the backend Service used by flow routes.
type flowService interface {
	ListFlows() ([]agent.FlowSummaryView, error)
	ListFlowVersions(flowID string) ([]agent.FlowVersionView, error)
	GetFlowVersion(flowID, version string) (agent.FlowVersionView, error)
	CreateFlow(args agent.FlowCreateArgs) (agent.FlowVersionView, error)
	ValidateFlow(def *flow.Definition) (string, error)
	PublishFlow(flowID, version string) (agent.FlowVersionView, error)
	CreateFlowRun(ctx context.Context, flowID, version string, inputs map[string]any) (agent.FlowRunView, error)
	StartFlowRun(ctx context.Context, flowID, runID string) (agent.FlowRunView, error)
	WaitFlowRun(ctx context.Context, flowID, runID string, timeoutMS int) (agent.FlowRunView, error)
	RefreshFlowRun(flowID, runID string) (agent.FlowRunView, error)
	CancelFlowRun(ctx context.Context, flowID, runID string) (agent.FlowRunView, error)
	ListFlowRuns(flowID string) ([]agent.FlowRunView, error)
	ListHumanTasks(queue, status string) ([]agent.HumanTaskView, error)
	ReplyFlowRunHuman(ctx context.Context, flowID, runID, nodeID string, value any) (agent.FlowRunView, error)
	FlowRunEvents(flowID, runID string) ([]map[string]any, error)
}

// registerFlowRoutes registers the Flow Spec v1 management API (design doc
// §7). Management routes are web-token protected (like agent templates); run
// execution is also exposed for the FlowGram runtime view.
func registerFlowRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	if service == nil {
		return
	}
	// GET /v1/flows — list flows with status lanes.
	mux.Handle("GET /v1/flows", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListFlows()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	// POST /v1/flows — create/update a draft version.
	mux.Handle("POST /v1/flows", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.FlowCreateArgs
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		view, err := service.CreateFlow(req)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, http.StatusCreated, view)
	})))
	// GET /v1/flows/{id} — flow summary + all versions.
	mux.Handle("GET /v1/flows/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flowID := r.PathValue("id")
		versions, err := service.ListFlowVersions(flowID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, versions)
	})))
	// POST /v1/flows/{id}/versions/{ver}/validate — compile + validate a
	// supplied definition (body) without saving.
	mux.Handle("POST /v1/flows/{id}/versions/{ver}/validate", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var def flow.Definition
		if err := decodeJSON(r, &def); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		digest, err := service.ValidateFlow(&def)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"digest": digest})
	})))
	// POST /v1/flows/{id}/versions/{ver}/publish — promote draft/gray.
	mux.Handle("POST /v1/flows/{id}/versions/{ver}/publish", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		view, err := service.PublishFlow(r.PathValue("id"), r.PathValue("ver"))
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	// POST /v1/flows/{id}/runs — create + start a run.
	mux.Handle("POST /v1/flows/{id}/runs", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Version string         `json:"version,omitempty"`
			Inputs  map[string]any `json:"inputs,omitempty"`
			WaitMS  int            `json:"wait_ms,omitempty"`
		}
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ctx := r.Context()
		run, err := service.CreateFlowRun(ctx, r.PathValue("id"), req.Version, req.Inputs)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
		started, err := service.StartFlowRun(ctx, r.PathValue("id"), run.RunID)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
		if req.WaitMS > 0 {
			started, err = service.WaitFlowRun(ctx, r.PathValue("id"), run.RunID, req.WaitMS)
			if err != nil {
				writeError(w, http.StatusUnprocessableEntity, err)
				return
			}
		}
		writeJSON(w, http.StatusAccepted, started)
	})))
	// GET /v1/flows/{id}/runs — list runs (newest first).
	mux.Handle("GET /v1/flows/{id}/runs", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runs, err := service.ListFlowRuns(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, runs)
	})))
	// GET /v1/flow-runs/{runID} — one run (resolved via flow query param).
	mux.Handle("GET /v1/flow-runs/{runID}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flowID := r.URL.Query().Get("flow_id")
		if flowID == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("missing flow_id query param"))
			return
		}
		run, err := service.RefreshFlowRun(flowID, r.PathValue("runID"))
		if err != nil {
			writeError(w, statusForFlowError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})))
	// POST /v1/flow-runs/{runID}/cancel — cancel the run.
	mux.Handle("POST /v1/flow-runs/{runID}/cancel", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flowID := r.URL.Query().Get("flow_id")
		if flowID == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("missing flow_id query param"))
			return
		}
		run, err := service.CancelFlowRun(r.Context(), flowID, r.PathValue("runID"))
		if err != nil {
			writeError(w, statusForFlowError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})))
	// POST /v1/flow-runs/{runID}/human/{nodeID}/reply — submit a human task
	// value, complete the blocked node and continue the run (P1.1).
	mux.Handle("POST /v1/flow-runs/{runID}/human/{nodeID}/reply", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Value any `json:"value"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		flowID := r.URL.Query().Get("flow_id")
		run, err := service.ReplyFlowRunHuman(r.Context(), flowID, r.PathValue("runID"), r.PathValue("nodeID"), req.Value)
		if err != nil {
			writeError(w, statusForFlowError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})))
	// GET /v1/human-tasks — human task queue (optional ?queue=&status=).
	mux.Handle("GET /v1/human-tasks", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queue := r.URL.Query().Get("queue")
		status := r.URL.Query().Get("status")
		tasks, err := service.ListHumanTasks(queue, status)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, tasks)
	})))
	// GET /v1/flow-runs/{runID}/events — SSE stream of the run's workflow
	// events (created/start/handoff/...). Polls the append-only events log and
	// pushes new events incrementally until the run reaches a terminal state
	// or the client disconnects (P1.2).
	mux.Handle("GET /v1/flow-runs/{runID}/events", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, http.ErrNotSupported)
			return
		}
		flowID := r.URL.Query().Get("flow_id")
		runID := r.PathValue("runID")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		emit := func(events []map[string]any) {
			for _, ev := range events {
				data, _ := json.Marshal(ev)
				_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			}
			flusher.Flush()
		}

		ctx := r.Context()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		sent := 0
		for {
			events, err := service.FlowRunEvents(flowID, runID)
			if err != nil {
				_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
				flusher.Flush()
				return
			}
			if len(events) > sent {
				emit(events[sent:])
				sent = len(events)
			}
			// Terminal state: push a done event and close the stream.
			if run, rerr := service.RefreshFlowRun(flowID, runID); rerr == nil {
				switch run.Status {
				case "completed", "error", "canceled":
					_, _ = fmt.Fprintf(w, "event: done\ndata: %s\n\n", run.Status)
					flusher.Flush()
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})))
}

func statusForFlowError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if jsonErr, ok := err.(*json.SyntaxError); ok {
		_ = jsonErr
		return http.StatusBadRequest
	}
	return http.StatusUnprocessableEntity
}
