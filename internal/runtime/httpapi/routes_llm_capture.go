package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/tim5wang/godex/internal/core/llmcapture"
)

func registerLlmCaptureRoutes(mux *http.ServeMux, capture *llmcapture.Capture, protected func(http.Handler) http.Handler) {
	if capture == nil {
		return
	}
	// GET /llm-capture/status — current capture state + dump location.
	mux.Handle("GET /llm-capture/status", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled":   capture.Enabled(),
			"dump_path": capture.DumpPath(),
		})
	})))

	// POST /llm-capture/enable — turn capture on (starts appending to jsonl).
	mux.Handle("POST /llm-capture/enable", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := capture.SetEnabled(true); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": true})
	})))

	// POST /llm-capture/disable — turn capture off (stops appending).
	mux.Handle("POST /llm-capture/disable", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := capture.SetEnabled(false); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": false})
	})))

	// GET /llm-capture/records?limit=N — recent records, newest first.
	mux.Handle("GET /llm-capture/records", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				limit = n
			}
		}
		writeJSON(w, http.StatusOK, capture.List(limit))
	})))

	// GET /llm-capture/records/{id} — one full record (request + response).
	mux.Handle("GET /llm-capture/records/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := capture.Get(r.PathValue("id"))
		if rec == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("llm capture record not found"))
			return
		}
		writeJSON(w, http.StatusOK, rec)
	})))

	// POST /llm-capture/clear — wipe the in-memory ring (disk history stays).
	mux.Handle("POST /llm-capture/clear", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.Clear()
		writeJSON(w, http.StatusOK, map[string]interface{}{"cleared": true})
	})))
}
