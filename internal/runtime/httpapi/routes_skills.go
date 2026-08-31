package httpapi

import (
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/tools"
)

func registerSkillRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /skills/catalog", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListGlobalSkillCatalog(r.Context())
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/skills/catalog", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListSessionSkills(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/skills/sources", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		mode := strings.TrimSpace(r.URL.Query().Get("mode"))
		var (
			items []tools.SkillSourceEntry
			err   error
		)
		if query != "" {
			items, err = service.SearchSessionSkillSources(r.Context(), r.PathValue("id"), query)
		} else if mode == "trending" {
			items, err = service.ListTrendingSessionSkillSources(r.Context(), r.PathValue("id"))
		} else {
			items, err = service.ListSessionSkillSources(r.Context(), r.PathValue("id"))
		}
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/skills/active", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ActiveSessionSkills(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/skills/{name}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.GetSessionSkill(r.Context(), r.PathValue("id"), r.PathValue("name"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /sessions/{id}/skills/install", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req skillInstallRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.InstallSessionSkill(r.Context(), r.PathValue("id"), strings.TrimSpace(req.Source), strings.TrimSpace(req.Name))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/skills/normalize", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req skillLoadRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.NormalizeSessionSkill(r.Context(), r.PathValue("id"), strings.TrimSpace(req.Name))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("DELETE /sessions/{id}/skills/{name}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.RemoveSessionSkill(r.Context(), r.PathValue("id"), r.PathValue("name"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/skills/load", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req skillLoadRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.ActivateSessionSkill(r.Context(), r.PathValue("id"), strings.TrimSpace(req.Name))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/skills/expand", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req skillExpandRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.ExpandSessionSkill(r.Context(), r.PathValue("id"), strings.TrimSpace(req.Name), append([]string{}, req.Sections...))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/skills/unload", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req skillLoadRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.UnloadSessionSkill(r.Context(), r.PathValue("id"), strings.TrimSpace(req.Name))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
}
