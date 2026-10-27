package httpapi

import (
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/tools"
)

func registerSkillRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	// Center-local workspace gate (design decision ⑨). Everything under
	// /sessions operates on THIS instance's own workspace: the operator's local
	// conversations and the agent runs inside them. A tenant principal holds
	// relay_only, so it is refused here — consistently, rather than the previous
	// split where the list and single-session reads were merely `protected` (any
	// authenticated caller) while only create was gated, letting a tenant read and
	// even DELETE sessions it does not own. Node actors stay exempt inside
	// requireCenterExecution, so driving a remote node from the center still works.
	gated := requireCenterExecution(protected)
	mux.Handle("GET /skills/catalog", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListGlobalSkillCatalog(r.Context())
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/skills/catalog", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListSessionSkills(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/skills/sources", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("GET /sessions/{id}/skills/active", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ActiveSessionSkills(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/skills/{name}", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.GetSessionSkill(r.Context(), r.PathValue("id"), r.PathValue("name"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /sessions/{id}/skills/install", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("POST /sessions/{id}/skills/normalize", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("DELETE /sessions/{id}/skills/{name}", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.RemoveSessionSkill(r.Context(), r.PathValue("id"), r.PathValue("name"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/skills/load", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("POST /sessions/{id}/skills/expand", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("POST /sessions/{id}/skills/unload", gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
