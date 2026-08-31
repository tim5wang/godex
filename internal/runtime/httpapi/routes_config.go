package httpapi

import (
	"net/http"

	"github.com/tim5wang/godex/internal/core/config"
)

func registerConfigRoutes(mux *http.ServeMux, manager *config.Manager, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /config/meta", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.Meta())
	})))
	mux.Handle("GET /config/schema", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.Schema())
	})))
	mux.Handle("GET /config", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.View())
	})))
	mux.Handle("PUT /config", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req updateConfigRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		view, err := manager.Update(r.Context(), config.UpdateRequest{
			Values:       req.Values,
			ClearSecrets: append([]string{}, req.ClearSecrets...),
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("POST /config/reload", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		view, err := manager.ReloadFromDisk(r.Context())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("POST /config/reveal", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req revealSecretRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		value, err := manager.Reveal(req.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": req.Path, "value": value})
	})))
	mux.Handle("GET /config/doctor", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.Doctor())
	})))
}
