package httpapi

import (
	"net/http"

	"github.com/tim5wang/godex/internal/core/templates"
	"github.com/tim5wang/godex/internal/services/backend"
)

func registerAgentTemplateRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /agent-templates", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListAgentTemplates()
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /agent-templates/options", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, service.AgentTemplateFormOptions())
	})))
	mux.Handle("GET /agent-templates/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.GetAgentTemplate(r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /agent-templates", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var tpl templates.AgentTemplate
		if err := decodeJSON(r, &tpl); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := service.SaveAgentTemplate(tpl); err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		item, err := service.GetAgentTemplate(tpl.ID)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusCreated, item)
	})))
	mux.Handle("PUT /agent-templates/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var tpl templates.AgentTemplate
		if err := decodeJSON(r, &tpl); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		tpl.ID = r.PathValue("id")
		if err := service.SaveAgentTemplate(tpl); err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		item, err := service.GetAgentTemplate(tpl.ID)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("DELETE /agent-templates/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := service.DeleteAgentTemplate(r.PathValue("id")); err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": r.PathValue("id")})
	})))
	mux.Handle("POST /agent-templates/{id}/validate", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, warnings, err := service.ValidateAgentTemplate(r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"template": item, "warnings": warnings})
	})))
	// Global skill catalog (independent of any session), used by the new-session
	// flow to pick which installed skills a fresh session should start with.
}
