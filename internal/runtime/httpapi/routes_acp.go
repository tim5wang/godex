package httpapi

import (
	"net/http"

	"github.com/tim5wang/godex/internal/services/backend"
)

// registerACPAgentRoutes exposes ACP agent runtime capabilities to the
// settings page: currently the model-options discovery that backs the agent
// editor's model dropdown.
func registerACPAgentRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /acp/agents/{id}/models", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent, err := service.GetACPAgent(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		models, err := service.DiscoverACPAgentModels(r.Context(), agent)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
	})))
}
