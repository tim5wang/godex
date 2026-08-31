package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/tim5wang/godex/internal/services/backend"
)

func registerSessionRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("POST /sessions", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openSessionRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		// Fold the optional workspace_dir into the locator's project_dir
		// metadata so the backend sees a single source of truth.  An
		// explicit locator metadata entry wins when both are set.
		if dir := strings.TrimSpace(req.WorkspaceDir); dir != "" {
			if req.Locator.Metadata == nil {
				req.Locator.Metadata = map[string]string{}
			}
			if strings.TrimSpace(req.Locator.Metadata["project_dir"]) == "" {
				req.Locator.Metadata["project_dir"] = dir
			}
		}
		opened, err := service.OpenSession(r.Context(), req.Locator)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, opened)
	})))
	mux.Handle("GET /sessions", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := service.ListSessions(r.Context(), backend.SessionListFilter{
			Channel: strings.TrimSpace(r.URL.Query().Get("channel")),
		})
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})))
	mux.Handle("DELETE /sessions/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := service.DeleteSession(r.Context(), r.PathValue("id")); err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("PATCH /sessions/{id}/title", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req renameSessionRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		item, err := service.RenameSession(r.Context(), r.PathValue("id"), req.Title)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /sessions/{id}/fork", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req forkSessionRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		opened, err := service.ForkSession(r.Context(), r.PathValue("id"), backend.ForkRequest{
			TurnID:       strings.TrimSpace(req.TurnID),
			MessageIndex: req.MessageIndex,
			Title:        strings.TrimSpace(req.Title),
		})
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, opened)
	})))
	mux.Handle("POST /sessions/{id}/model", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req setSessionModelRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		view, err := service.SetSessionModelProfileWithReasoning(r.Context(), r.PathValue("id"), strings.TrimSpace(req.ProfileID), strings.TrimSpace(req.ReasoningEffort))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})))
	mux.Handle("GET /sessions/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := service.Snapshot(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	})))
	mux.Handle("GET /sessions/{id}/context-inspector", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inspector, err := service.ContextInspector(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, inspector)
	})))
	mux.Handle("GET /sessions/{id}/transcript/{ref}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		messages, err := service.ReadTranscript(r.PathValue("id"), r.PathValue("ref"))
		if err != nil {
			if errors.Is(err, backend.ErrSessionNotFound) || errors.Is(err, backend.ErrTranscriptNotFound) {
				writeError(w, http.StatusNotFound, err)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ref":      r.PathValue("ref"),
			"messages": messages,
		})
	})))
	mux.Handle("GET /sessions/{id}/ledger", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ledger, err := service.ProjectLedger(r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, ledger)
	})))
	mux.Handle("POST /sessions/{id}/ledger", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req updateProjectLedgerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ledger, err := service.UpdateProjectLedger(r.PathValue("id"), backend.ProjectLedgerPatch{
			Goal:         req.Goal,
			CurrentPhase: req.CurrentPhase,
			ChangedFiles: req.ChangedFiles,
			Validation:   req.Validation,
			Decisions:    req.Decisions,
			Risks:        req.Risks,
			Blockers:     req.Blockers,
			NextSteps:    req.NextSteps,
		})
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, ledger)
	})))
	mux.Handle("GET /sessions/{id}/timeline", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid timeline limit"))
				return
			}
			limit = parsed
		}
		items, err := service.Timeline(r.Context(), r.PathValue("id"), limit)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /sessions/{id}/timeline/page", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		limit := 50
		if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid timeline limit"))
				return
			}
			limit = parsed
		}
		if raw := strings.TrimSpace(query.Get("cursor")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid timeline cursor"))
				return
			}
		}
		var types []string
		for _, typ := range strings.Split(query.Get("type"), ",") {
			if typ = strings.TrimSpace(typ); typ != "" {
				types = append(types, typ)
			}
		}
		page, err := service.TimelinePage(r.Context(), r.PathValue("id"), backend.TimelinePageRequest{
			Limit:  limit,
			Cursor: strings.TrimSpace(query.Get("cursor")),
			Types:  types,
			Query:  strings.TrimSpace(query.Get("q")),
			JobID:  strings.TrimSpace(query.Get("job_id")),
			TurnID: strings.TrimSpace(query.Get("turn_id")),
		})
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})))
	mux.Handle("GET /sessions/{id}/compactions", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.Compactions(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
}
