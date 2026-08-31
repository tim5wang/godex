package httpapi

import (
	"fmt"
	"net/http"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
	coreproviders "github.com/tim5wang/godex/internal/core/providers"
)

func registerProviderRoutes(mux *http.ServeMux, manager *config.Manager, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /providers", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, coreproviders.List(manager.Current()))
	})))
	mux.Handle("POST /providers/{id}/test", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := coreproviders.Test(r.Context(), manager.Current(), r.PathValue("id"))
		status := http.StatusOK
		if !result.OK {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, result)
	})))
	mux.Handle("POST /providers/{id}/models", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := coreproviders.DiscoverModels(r.Context(), manager.Current(), r.PathValue("id"))
		status := http.StatusOK
		if !result.OK {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, result)
	})))

	mux.Handle("GET /providers/import/codex", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imported, err := config.ImportCodexProviders("", "")
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, imported)
	})))
	mux.Handle("POST /providers/import/codex", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imported, err := config.ImportCodexProviders("", "")
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cfg := manager.Current()
		merged := make(map[string]llm.ProviderConfig, len(cfg.LLMProviders)+len(imported))
		for id, p := range cfg.LLMProviders {
			merged[id] = p
		}
		added := 0
		for _, p := range imported {
			targetID := "codex-" + p.ProviderID
			if _, exists := merged[targetID]; exists {
				continue
			}
			merged[targetID] = p.ProviderConfig
			added++
		}
		if added == 0 {
			writeError(w, http.StatusConflict, fmt.Errorf("all providers already exist"))
			return
		}
		if err := manager.UpdateProviders(merged); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"imported":  added,
			"providers": coreproviders.List(manager.Current()),
		})
	})))
}
