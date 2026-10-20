package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/mcp"
)

type cachedMCPStatusStub struct {
	regMCPInterface
	t *testing.T
}

func (s cachedMCPStatusStub) Statuses(context.Context) ([]mcp.ServerStatus, error) {
	s.t.Fatal("status polling must not perform synchronous discovery")
	return nil, nil
}

func (s cachedMCPStatusStub) CachedStatuses() ([]mcp.ServerStatus, error) {
	return []mcp.ServerStatus{{Name: "slow", State: "discovering"}}, nil
}

func TestMCPStatusRouteUsesCachedDiscovery(t *testing.T) {
	mux := http.NewServeMux()
	registerMCPRoutes(mux, func(h http.Handler) http.Handler { return h }, cachedMCPStatusStub{t: t})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/mcp/status", nil))
	var result struct {
		Statuses []mcp.ServerStatus `json:"statuses"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || len(result.Statuses) != 1 || result.Statuses[0].State != "discovering" {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
}
