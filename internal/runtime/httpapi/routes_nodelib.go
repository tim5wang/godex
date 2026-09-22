package httpapi

import (
	"net/http"

	"github.com/tim5wang/godex/internal/core/nodelib"
	"github.com/tim5wang/godex/internal/services/backend"
)

// registerNodeLibraryRoutes registers the flow node library management API
// (P3): CRUD over reusable function-node definitions (js/wasm handlers) that
// can be dropped into any flow canvas.
func registerNodeLibraryRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /v1/node-library", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListNodeLibrary()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /v1/node-library/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.GetNodeLibraryEntry(r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /v1/node-library", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e nodelib.Entry
		if err := decodeJSON(r, &e); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := service.SaveNodeLibraryEntry(e); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		item, err := service.GetNodeLibraryEntry(e.ID)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusCreated, item)
	})))
	mux.Handle("PUT /v1/node-library/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e nodelib.Entry
		if err := decodeJSON(r, &e); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		e.ID = r.PathValue("id")
		if err := service.SaveNodeLibraryEntry(e); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		item, err := service.GetNodeLibraryEntry(e.ID)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("DELETE /v1/node-library/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := service.DeleteNodeLibraryEntry(r.PathValue("id")); err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": r.PathValue("id")})
	})))
}
