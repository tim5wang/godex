package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

func registerServiceCatalogRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /models", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		view, err := service.Models(r.Context(), strings.TrimSpace(r.URL.Query().Get("session_id")))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("GET /security/summary", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		summary, err := service.SecuritySummary(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, summary)
	})))
	mux.Handle("GET /security/audit", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid audit limit"))
				return
			}
			limit = parsed
		}
		items, err := service.SecurityAudit(r.Context(), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
}

func registerPackageManagementRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /packages", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListPackages(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /packages/quality", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		report, err := service.PackageQuality(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, report)
	})))
	mux.Handle("POST /packages/install", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req installPackageRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		item, err := service.InstallPackage(r.Context(), strings.TrimSpace(req.Source))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /packages/remove", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req removePackageRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		item, err := service.RemovePackage(r.Context(), strings.TrimSpace(req.Name))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /packages/{name}/reinstall", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.ReinstallPackage(r.Context(), strings.TrimSpace(r.PathValue("name")))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /packages/{name}/smoke/{smoke}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req packageSmokeRunRequest
		if r.Body != nil && r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		run, err := service.RunPackageSmoke(
			r.Context(),
			strings.TrimSpace(r.PathValue("name")),
			strings.TrimSpace(r.PathValue("smoke")),
			strings.TrimSpace(req.SessionID),
		)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})))
}

func registerPromptAndCommandRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /prompts", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		includeContent := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_content")), "true")
		items, err := service.ListPrompts(r.Context(), includeContent)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /commands", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Expose the built-in slash-command metadata so the web composer
		// can offer the same "/" completion palette as the TUI and ACP
		// clients without hardcoding its own copy of the command list.
		writeJSON(w, http.StatusOK, commands.AvailableMetadata())
	})))
}

func registerPackageCatalogRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /packages/commands", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		includeContent := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_content")), "true")
		items, err := service.ListPackageCommands(r.Context(), includeContent)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /packages/roles", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		includeContent := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_content")), "true")
		items, err := service.ListPackageRoles(r.Context(), includeContent)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
}
