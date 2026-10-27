package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/tools"
)

func registerWorkflowRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	// Center-local workspace gate (design decision ⑨). Everything under
	// /sessions operates on THIS instance's own workspace: the operator's local
	// conversations and the agent runs inside them. A tenant principal holds
	// relay_only, so it is refused here — consistently, rather than the previous
	// split where the list and single-session reads were merely `protected` (any
	// authenticated caller) while only create was gated, letting a tenant read and
	// even DELETE sessions it does not own. Node actors stay exempt inside
	// requireCenterExecution, so driving a remote node from the center still works.
	gated := requireCenterExecution(protected)
	mux.Handle("GET /sessions/{id}/subagents", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListSubagents(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/subagents/{jobID}", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.GetSubagent(r.Context(), r.PathValue("id"), r.PathValue("jobID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("GET /sessions/{id}/subagents/{jobID}/review", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		review, err := service.ReviewSubagent(r.Context(), r.PathValue("id"), r.PathValue("jobID"))
		if err != nil {
			writeError(w, statusForSubagentActionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, review)
	})))
	mux.Handle("POST /sessions/{id}/subagents/{jobID}/cancel", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.CancelSubagent(r.Context(), r.PathValue("id"), r.PathValue("jobID"))
		if err != nil {
			writeError(w, statusForSubagentActionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /sessions/{id}/subagents/{jobID}/resume", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.ResumeSubagent(r.Context(), r.PathValue("id"), r.PathValue("jobID"))
		if err != nil {
			writeError(w, statusForSubagentActionError(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, item)
	})))
	mux.Handle("POST /sessions/{id}/subagents/{jobID}/merge", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.MergeSubagent(r.Context(), r.PathValue("id"), r.PathValue("jobID"))
		if err != nil {
			writeError(w, statusForSubagentActionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("GET /sessions/{id}/longtasks", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListLongTasks(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("POST /sessions/{id}/longtasks", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.LongTaskArgs
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		view, err := service.CreateLongTask(r.Context(), r.PathValue("id"), req)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusCreated, view)
	})))
	mux.Handle("GET /sessions/{id}/longtasks/{workflowID}", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		view, err := service.GetLongTask(r.Context(), r.PathValue("id"), r.PathValue("workflowID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("POST /sessions/{id}/longtasks/{workflowID}/run", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.LongTaskArgs
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		view, err := service.RunLongTask(r.Context(), r.PathValue("id"), r.PathValue("workflowID"), req)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("POST /sessions/{id}/longtasks/{workflowID}/cancel", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req longTaskNodeRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var (
			view agent.LongTaskView
			err  error
		)
		if req.CancelAll {
			view, err = service.CancelLongTaskAll(r.Context(), r.PathValue("id"), r.PathValue("workflowID"))
		} else {
			view, err = service.CancelLongTask(r.Context(), r.PathValue("id"), r.PathValue("workflowID"), req.NodeID)
		}
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("POST /sessions/{id}/longtasks/{workflowID}/finalize", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req longTaskNodeRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		view, err := service.FinalizeLongTaskStory(r.Context(), r.PathValue("id"), r.PathValue("workflowID"), req.NodeID)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	// T12: commit-hash reverse lookup.
	mux.Handle("POST /sessions/{id}/longtasks/{workflowID}/lookup", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req longTaskLookupRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Commit) == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("missing commit"))
			return
		}
		out, err := service.LookupLongTask(r.Context(), r.PathValue("id"), req.Commit, r.PathValue("workflowID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})))
	// T12: rollback. Empty reason is allowed; >1024 bytes is rejected.
	mux.Handle("POST /sessions/{id}/longtasks/{workflowID}/rollback", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req longTaskRollbackRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if len(req.Reason) > 1024 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("rollback reason exceeds 1024 bytes (got %d)", len(req.Reason)))
			return
		}
		result, err := service.RollbackLongTaskStory(r.Context(), r.PathValue("id"), r.PathValue("workflowID"), req.NodeID, req.Reason)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	// T12: explicit lazy GC. Default dry-run; older_than_seconds=0
	// means permanent retention (T12 default).
	mux.Handle("POST /sessions/{id}/longtasks/{workflowID}/gc", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req longTaskGCRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.GCLongTaskArtifacts(r.Context(), r.PathValue("id"), r.PathValue("workflowID"), req.OlderThanSeconds, req.Apply)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("GET /sessions/{id}/permissions", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.PendingPermissions(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("POST /sessions/{id}/permissions/{requestID}/approve", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req permissionApproveRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		scope := tools.PermissionGrantScope(strings.TrimSpace(req.Scope))
		resolution, err := service.ApprovePermission(r.Context(), r.PathValue("id"), r.PathValue("requestID"), scope)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, resolution)
	})))
	mux.Handle("POST /sessions/{id}/permissions/{requestID}/deny", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req permissionDenyRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resolution, err := service.DenyPermission(r.Context(), r.PathValue("id"), r.PathValue("requestID"), req.Reason)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, resolution)
	})))
	// Agent templates (talent market): CRUD over builtin/user/package-derived
	// presets selected at session creation time.
}
