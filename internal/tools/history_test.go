package tools

import (
	"context"
	"testing"

	"github.com/tim5wang/godex/internal/domain/automation"
)

type fakeHistoryRuntime struct {
	sessionID string
	request   HistorySearchRequest
	result    HistorySearchResult
}

func (f *fakeHistoryRuntime) SearchHistory(_ context.Context, sessionID string, _ automation.SessionContext, req HistorySearchRequest) (HistorySearchResult, error) {
	f.sessionID = sessionID
	f.request = req
	return f.result, nil
}

func TestHistorySearchToolUsesCurrentSessionContext(t *testing.T) {
	runtime := &fakeHistoryRuntime{
		result: HistorySearchResult{
			Scope:      HistorySearchScopeCurrentSession,
			MatchCount: 1,
		},
	}
	tool := NewHistorySearchTool(runtime)
	ctx := WithSessionID(context.Background(), "session-1")
	ctx = WithSessionContext(ctx, automation.SessionContext{SessionID: "session-1"})

	output, err := tool.Execute(ctx, map[string]interface{}{
		"query": "aurora",
	})
	if err != nil {
		t.Fatalf("execute history_search: %v", err)
	}
	if output == "" {
		t.Fatal("expected JSON output")
	}
	if runtime.sessionID != "session-1" {
		t.Fatalf("expected session context to flow into runtime, got %q", runtime.sessionID)
	}
	if runtime.request.Scope != "" {
		t.Fatalf("expected empty scope to preserve runtime defaulting, got %#v", runtime.request)
	}
}
