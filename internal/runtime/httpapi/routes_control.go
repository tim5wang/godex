package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
)

func registerRuntimeServiceRoutes(mux *http.ServeMux, serviceRuntime serviceRuntimeProvider, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /runtime/service", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serviceRuntime == nil {
			writeJSON(w, http.StatusOK, map[string]any{"managed": false, "detail": "service runtime control is unavailable"})
			return
		}
		status, err := serviceRuntime.Status(r.Context())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})))
	mux.Handle("POST /runtime/service/restart", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serviceRuntime == nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("service runtime control is unavailable"))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "message": "Service restart requested."})
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = serviceRuntime.Restart(context.Background())
		}()
	})))
}

func registerControlNodeRoutes(mux *http.ServeMux, controlRegistry controlNodeRegistry, overviewProvider nodeOverviewProvider, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /control/nodes", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeJSON(w, http.StatusOK, []noderegistry.NodeView{})
			return
		}
		nodes, err := controlRegistry.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, nodes)
	})))
	mux.Handle("GET /control/nodes/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("control node registry is unavailable"))
			return
		}
		node, err := controlRegistry.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, node)
	})))
	mux.Handle("DELETE /control/nodes/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("control node registry is unavailable"))
			return
		}
		id := r.PathValue("id")
		node, err := controlRegistry.Delete(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		// Drop the node's live relay connection if the registry is wired to
		// the relay hub (center server); otherwise the node would keep its
		// tunnel open after deletion.
		if disconnector, ok := controlRegistry.(nodeDisconnector); ok {
			disconnector.DisconnectNode(id)
		}
		writeJSON(w, http.StatusOK, node)
	})))
	mux.Handle("POST /control/nodes/register", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("control node registry is unavailable"))
			return
		}
		var input noderegistry.NodeInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		node, err := controlRegistry.Register(r.Context(), input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, node)
	})))
	mux.Handle("POST /control/nodes/{id}/heartbeat", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("control node registry is unavailable"))
			return
		}
		var input noderegistry.NodeInput
		if err := decodeJSONAllowEmpty(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		node, err := controlRegistry.Heartbeat(r.Context(), r.PathValue("id"), input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, node)
	})))
	mux.Handle("POST /control/nodes/{id}/credential", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("control node registry is unavailable"))
			return
		}
		id := r.PathValue("id")
		if _, err := controlRegistry.Get(r.Context(), id); err != nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("node not found: %s", id))
			return
		}
		credential, err := relay.GenerateCredential()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if err := controlRegistry.SetCredentialHash(r.Context(), id, relay.HashCredential(credential)); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"node_id":    id,
			"credential": credential,
		})
	})))
	mux.Handle("GET /control/nodes/{id}/overview", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("control node registry is unavailable"))
			return
		}
		id := r.PathValue("id")
		node, err := controlRegistry.Get(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		var overview relay.NodeOverview
		if overviewProvider != nil {
			if ov, ok := overviewProvider.Overview(id); ok {
				overview = ov
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"node":     node,
			"overview": overview,
		})
	})))
}
