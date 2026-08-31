package httpapi

import (
	"fmt"
	"net/http"

	"github.com/tim5wang/godex/internal/domain/automation"
)

func registerAutomationRoutes(
	mux *http.ServeMux,
	cronRuntime cronAutomationProvider,
	heartbeatRuntime heartbeatAutomationProvider,
	protected func(http.Handler) http.Handler,
) {
	mux.Handle("GET /automation/cron/jobs", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		jobs, err := cronRuntime.ListJobs()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, jobs)
	})))
	mux.Handle("POST /automation/cron/jobs", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		var input automation.CronCreateInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		input.CreatedBy = "web"
		input.CreatedFromSession = "web-ui"
		job, err := cronRuntime.CreateJob(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})))
	mux.Handle("GET /automation/cron/jobs/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		job, err := cronRuntime.GetJob(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})))
	mux.Handle("PATCH /automation/cron/jobs/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		var input automation.CronUpdateInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		input.ID = r.PathValue("id")
		job, err := cronRuntime.UpdateJob(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})))
	mux.Handle("DELETE /automation/cron/jobs/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		if err := cronRuntime.DeleteJob(r.PathValue("id")); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("POST /automation/cron/jobs/{id}/run", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		run, err := cronRuntime.RunNow(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})))
	mux.Handle("GET /automation/cron/jobs/{id}/runs", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cronRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("cron automation unavailable"))
			return
		}
		runs, err := cronRuntime.ListRunLogs(r.PathValue("id"), 20)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, runs)
	})))
	mux.Handle("GET /automation/heartbeat", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if heartbeatRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("heartbeat automation unavailable"))
			return
		}
		rule, err := heartbeatRuntime.GetRule()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, rule)
	})))
	mux.Handle("PUT /automation/heartbeat", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if heartbeatRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("heartbeat automation unavailable"))
			return
		}
		var input automation.HeartbeatSetInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		input.CreatedBy = "web"
		input.CreatedFromSession = "web-ui"
		rule, err := heartbeatRuntime.SetRule(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, rule)
	})))
	mux.Handle("POST /automation/heartbeat/test", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if heartbeatRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("heartbeat automation unavailable"))
			return
		}
		run, err := heartbeatRuntime.TestNow(r.Context())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})))
	mux.Handle("GET /automation/heartbeat/logs", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if heartbeatRuntime == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("heartbeat automation unavailable"))
			return
		}
		runs, err := heartbeatRuntime.ListRunLogs(20)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, runs)
	})))
}
