package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/services/backend"
)

func registerMemoryRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /memory", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opts := memory.SearchOptions{
			Query:  strings.TrimSpace(r.URL.Query().Get("q")),
			Type:   memory.Type(strings.TrimSpace(r.URL.Query().Get("memory_type"))),
			Tag:    strings.TrimSpace(r.URL.Query().Get("tag")),
			Source: strings.TrimSpace(r.URL.Query().Get("source")),
			Status: memory.Status(strings.TrimSpace(r.URL.Query().Get("status"))),
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid memory limit"))
				return
			}
			opts.Limit = parsed
		}
		items, err := service.ListMemory(r.Context(), opts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /memory/candidates", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListMemoryCandidates(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /memory/audit", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid memory audit limit"))
				return
			}
			limit = parsed
		}
		items, err := service.ListMemoryAudit(r.Context(), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("POST /memory/digest", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.DigestMemory(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /memory/mine/project", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.MineProjectMemoryCandidates(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /memory/suppressions", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListMemorySuppressions(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})))
	mux.Handle("GET /memory/context", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		layers, err := service.PreviewMemoryContext(r.Context(), strings.TrimSpace(r.URL.Query().Get("q")))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, layers)
	})))
	mux.Handle("POST /memory/remember", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rememberMemoryRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := service.RememberMemory(r.Context(), memory.SaveInput{
			Title:   req.Title,
			Summary: req.Summary,
			Content: req.Content,
			Type:    memory.Type(req.MemoryType),
			Source:  req.Source,
			Tags:    append([]string{}, req.Tags...),
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	})))
	mux.Handle("POST /memory/update", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req updateMemoryRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := service.UpdateMemory(r.Context(), memory.UpdateInput{
			Match: memory.ForgetInput{
				Title: strings.TrimSpace(req.MatchTitle),
				File:  strings.TrimSpace(req.MatchFile),
			},
			Title:   req.Title,
			Summary: req.Summary,
			Content: req.Content,
			Type:    memory.Type(req.MemoryType),
			Source:  req.Source,
			Tags:    append([]string{}, req.Tags...),
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	})))
	mux.Handle("POST /memory/forget", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req forgetMemoryRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := service.ForgetMemory(r.Context(), memory.ForgetInput{
			Title: strings.TrimSpace(req.Title),
			File:  strings.TrimSpace(req.File),
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	})))
	mux.Handle("POST /memory/archive", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req forgetMemoryRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := service.ArchiveMemory(r.Context(), memory.ForgetInput{
			Title: strings.TrimSpace(req.Title),
			File:  strings.TrimSpace(req.File),
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	})))
	mux.Handle("POST /memory/restore", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req forgetMemoryRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := service.RestoreMemory(r.Context(), memory.ForgetInput{
			Title: strings.TrimSpace(req.Title),
			File:  strings.TrimSpace(req.File),
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	})))
	mux.Handle("POST /memory/milestones/archive", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries, err := service.ArchiveMilestones(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"archived": entries})
	})))
	mux.Handle("GET /memory/milestones", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries, err := service.ListMilestoneMemories(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, entries)
	})))
	mux.Handle("POST /memory/suppressions/remove", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req removeMemorySuppressionRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := service.RemoveMemorySuppression(r.Context(), req.Key); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"removed": true})
	})))
	mux.Handle("POST /memory/candidates/{fingerprint}/accept", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req acceptMemoryCandidateRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := service.AcceptMemoryCandidate(r.Context(), memory.AcceptCandidateInput{
			Fingerprint:   r.PathValue("fingerprint"),
			AlwaysInclude: req.AlwaysInclude,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	})))
	mux.Handle("POST /memory/candidates/{fingerprint}/dismiss", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		candidate, err := service.DismissMemoryCandidate(r.Context(), r.PathValue("fingerprint"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, candidate)
	})))
	mux.Handle("POST /memory/audit/{id}/restore", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req restoreMemoryAuditRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := service.RestoreMemoryAudit(r.Context(), r.PathValue("id"), req.Target)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	})))
}
