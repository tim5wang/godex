package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/core/mcp"
)

// mcpServerInput is the write payload for creating/updating an MCP server. It
// mirrors mcp.ServerConfig but keeps the field set explicit so callers cannot
// accidentally write an internal field.
type mcpServerInput struct {
	Name            string            `json:"name"`
	Type            string            `json:"type"`
	Root            string            `json:"root,omitempty"`
	Command         string            `json:"command,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	URL             string            `json:"url,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	SessionRequired bool              `json:"session_required,omitempty"`
}

// regMCPInterface is the subset of the MCP lifecycle manager the router needs.
// It is satisfied by *mcp.Manager; declaring it as an interface keeps the
// routes testable without a live config file.
type regMCPInterface interface {
	ListServers() ([]mcp.ServerConfig, error)
	GetServer(name string) (mcp.ServerConfig, error)
	UpsertServer(server mcp.ServerConfig) error
	DeleteServer(name string) error
	TestConnection(ctx context.Context, name string) (*mcp.ServerStatus, error)
	Statuses(ctx context.Context) ([]mcp.ServerStatus, error)
}

// registerMCPRoutes registers the MCP registry management API, used by the
// Settings MCP tab (list/create/update/delete servers, test connections, live
// status) and as the data source for the BusinessAgent MCP-server picker.
func registerMCPRoutes(mux *http.ServeMux, protected func(http.Handler) http.Handler, mgr regMCPInterface) {
	if mgr == nil {
		return
	}

	mux.Handle("GET /v1/mcp/servers", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		servers, err := mgr.ListServers()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"servers": servers})
	})))

	mux.Handle("POST /v1/mcp/servers", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input mcpServerInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		server := input.toServerConfig()
		if err := mgr.UpsertServer(server); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, server)
	})))

	mux.Handle("GET /v1/mcp/servers/{name}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server, err := mgr.GetServer(r.PathValue("name"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, server)
	})))

	mux.Handle("PUT /v1/mcp/servers/{name}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input mcpServerInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		exists, err := mgr.GetServer(r.PathValue("name"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		// Preserve the path name as the authoritative identity; the request
		// body may carry a different display name but we key by path.
		server := input.toServerConfig()
		if strings.TrimSpace(server.Name) == "" {
			server.Name = exists.Name
		}
		server.Name = r.PathValue("name")
		if err := mgr.UpsertServer(server); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, server)
	})))

	mux.Handle("DELETE /v1/mcp/servers/{name}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := mgr.DeleteServer(r.PathValue("name")); err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))

	mux.Handle("POST /v1/mcp/servers/{name}/test", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, err := mgr.TestConnection(r.Context(), r.PathValue("name"))
		if err != nil {
			writeJSON(w, http.StatusOK, status)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})))

	mux.Handle("GET /v1/mcp/status", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		statuses, err := mgr.Statuses(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"statuses": statuses})
	})))
}

func (in mcpServerInput) toServerConfig() mcp.ServerConfig {
	return mcp.ServerConfig{
		Name:            strings.TrimSpace(in.Name),
		Type:            strings.TrimSpace(in.Type),
		Root:            strings.TrimSpace(in.Root),
		Command:         strings.TrimSpace(in.Command),
		Args:            in.Args,
		Env:             in.Env,
		URL:             strings.TrimSpace(in.URL),
		Headers:         in.Headers,
		SessionRequired: in.SessionRequired,
	}
}
