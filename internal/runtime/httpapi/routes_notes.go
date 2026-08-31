package httpapi

import (
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/core/notes"
	"github.com/tim5wang/godex/internal/services/backend"
)

func registerNoteRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /notes", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListNotes(r.Context(), notes.SearchOptions{
			Query: strings.TrimSpace(r.URL.Query().Get("q")),
			Tag:   strings.TrimSpace(r.URL.Query().Get("tag")),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /notes/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.GetNote(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("GET /notes/{id}/related-memories", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.GetRelatedMemories(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("POST /notes", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req saveNoteRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		item, err := service.SaveNote(r.Context(), notes.SaveInput{
			ID:      req.ID,
			Title:   req.Title,
			Summary: req.Summary,
			Tags:    append([]string{}, req.Tags...),
			Content: req.Content,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("DELETE /notes/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.DeleteNote(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
}
