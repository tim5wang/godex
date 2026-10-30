package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
	"github.com/tim5wang/godex/internal/services/backend"
)

// registerWorkContextRoutes keeps this first personal desktop on the same
// instance/workspace authorization surface as sessions and the taskboard.
// It does not accidentally give relay-only tenant accounts center execution.
func registerWorkContextRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	gated := requireCenterExecution(protected)
	handle := func(pattern string, fn func(*http.Request, *workcontext.Store, workcontext.Scope) (any, error)) {
		mux.Handle(pattern, gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok || !principal.Valid() {
				writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated principal"))
				return
			}
			scope := workcontext.Scope{TenantID: principal.TenantID, OwnerID: principal.UserID}
			store, err := service.WorkContexts()
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, err)
				return
			}
			// Bound JSON at the route boundary, including large source excerpts.
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
			}
			result, err := fn(r, store, scope)
			if err != nil {
				status := http.StatusInternalServerError
				switch {
				case errors.Is(err, workcontext.ErrNotFound), errors.Is(err, taskboard.ErrCardNotFound):
					status = http.StatusNotFound
				case errors.Is(err, workcontext.ErrConflict), errors.Is(err, workcontext.ErrArchived), errors.Is(err, taskboard.ErrVersionConflict):
					status = http.StatusConflict
				case errors.Is(err, workcontext.ErrInvalid):
					status = http.StatusBadRequest
				}
				writeError(w, status, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		})))
	}
	id := func(r *http.Request) string { return strings.TrimSpace(r.PathValue("id")) }
	invalidJSON := func(err error) error { return fmt.Errorf("%w: %v", workcontext.ErrInvalid, err) }
	handle("GET /v1/work-contexts", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.List(r.Context(), scope)
	})
	handle("POST /v1/work-contexts", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.ContextInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.Create(r.Context(), scope, input)
	})
	handle("GET /v1/work-context-resources", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.ListResources(r.Context(), scope)
	})
	handle("GET /v1/work-contexts/{id}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return service.WorkContextDetail(r.Context(), scope, id(r))
	})
	handle("PATCH /v1/work-contexts/{id}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.ContextPatch
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.Patch(r.Context(), scope, id(r), input)
	})
	handle("GET /v1/work-contexts/{id}/checkpoints", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.Checkpoints(r.Context(), scope, id(r))
	})
	handle("POST /v1/work-contexts/{id}/checkpoints", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input struct {
			IfVersion  int                    `json:"if_version"`
			Checkpoint workcontext.Checkpoint `json:"checkpoint"`
		}
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.SaveCheckpoint(r.Context(), scope, id(r), input.IfVersion, input.Checkpoint)
	})
	handle("GET /v1/work-contexts/{id}/brief", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		detail, err := service.WorkContextDetail(r.Context(), scope, id(r))
		if err != nil {
			return nil, err
		}
		return map[string]any{"context": detail.Context, "events": detail.Events, "facts": detail.Facts, "delegations": detail.Delegations, "production_verified": false}, nil
	})
	handle("POST /v1/work-contexts/{id}/resources", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.ResourceInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.AddResource(r.Context(), scope, id(r), input)
	})
	handle("DELETE /v1/work-contexts/{id}/resources/{resourceID}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		err := s.RemoveResource(r.Context(), scope, id(r), r.PathValue("resourceID"))
		return map[string]bool{"ok": err == nil}, err
	})
	handle("POST /v1/work-contexts/{id}/relations", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.RelationInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.AddRelation(r.Context(), scope, id(r), input)
	})
	handle("DELETE /v1/work-contexts/{id}/relations/{relationID}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		err := s.RemoveRelation(r.Context(), scope, id(r), r.PathValue("relationID"))
		return map[string]bool{"ok": err == nil}, err
	})
	handle("POST /v1/work-contexts/{id}/facts", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.FactInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.CreateFact(r.Context(), scope, id(r), input)
	})
	handle("PATCH /v1/work-contexts/{id}/facts/{factID}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.FactReview
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.ReviewFact(r.Context(), scope, id(r), r.PathValue("factID"), input)
	})
	handle("GET /v1/work-contexts/{id}/recall", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.Recall(r.Context(), scope, id(r), r.URL.Query().Get("scope"), r.URL.Query().Get("q"))
	})
	handle("POST /v1/work-contexts/{id}/facts/{factID}/memory-candidate", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return service.ProposeWorkMemory(r.Context(), scope, id(r), r.PathValue("factID"))
	})
	handle("POST /v1/work-contexts/{id}/sessions", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input backend.WorkSessionInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return service.LinkWorkSession(r.Context(), scope, id(r), input)
	})
	handle("DELETE /v1/work-contexts/{id}/sessions/{sessionID}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		err := s.UnlinkSession(r.Context(), scope, id(r), r.PathValue("sessionID"))
		return map[string]bool{"ok": err == nil}, err
	})
	handle("POST /v1/work-contexts/{id}/delegations", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input backend.WorkDelegationInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return service.DelegateWork(r.Context(), scope, id(r), input)
	})
	handle("POST /v1/work-contexts/{id}/delegations/{delegationID}/capture", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return service.CaptureWorkResult(r.Context(), scope, id(r), r.PathValue("delegationID"))
	})
	handle("POST /v1/work-contexts/{id}/organize", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return service.OrganizeWorkContext(r.Context(), scope, id(r))
	})
	handle("GET /v1/work-contexts/{id}/events", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		detail, err := s.Get(r.Context(), scope, id(r))
		if err != nil {
			return nil, err
		}
		return detail.Events, nil
	})
}
