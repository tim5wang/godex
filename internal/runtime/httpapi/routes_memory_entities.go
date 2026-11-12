package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/tim5wang/godex/internal/core/workcontext"
)

func registerMemoryEntityRoutes(handle func(string, func(*http.Request, *workcontext.Store, workcontext.Scope) (any, error))) {
	handle("GET /v1/work-contexts/{id}/memory/entities", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		cursor, err := optionalEntityInt(r, "cursor", 0)
		if err != nil {
			return nil, err
		}
		limit, err := optionalEntityInt(r, "limit", 30)
		if err != nil {
			return nil, err
		}
		return s.ListMemoryEntities(r.Context(), scope, r.PathValue("id"), r.URL.Query().Get("q"), cursor, limit)
	})
	handle("POST /v1/work-contexts/{id}/memory/entities", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.EntityInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, fmt.Errorf("%w: %v", workcontext.ErrInvalid, err)
		}
		return s.SaveMemoryEntity(r.Context(), scope, r.PathValue("id"), input)
	})
	handle("GET /v1/work-contexts/{id}/memory/entity-relations", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.ListEntityRelations(r.Context(), scope, r.PathValue("id"))
	})
	handle("POST /v1/work-contexts/{id}/memory/entity-relations", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.EntityRelationInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, fmt.Errorf("%w: %v", workcontext.ErrInvalid, err)
		}
		return s.SaveEntityRelation(r.Context(), scope, r.PathValue("id"), input)
	})
	handle("GET /v1/work-contexts/{id}/memory/entity-audits", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.EntityAuditHistory(r.Context(), scope, r.PathValue("id"))
	})
}
func optionalEntityInt(r *http.Request, key string, fallback int) (int, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: invalid %s", workcontext.ErrInvalid, key)
	}
	return n, nil
}
