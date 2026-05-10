package tools

import (
	"context"
	"fmt"
	"strings"
)

type historySearchArgs struct {
	Query string `json:"query"`
	Scope string `json:"scope,omitempty"`
	Limit int    `json:"limit,omitempty"`
	Role  string `json:"role,omitempty"`
}

func NewHistorySearchTool(runtime HistorySearchRuntime) Tool {
	return NewTypedTool(NewToolSpec("history_search", "Search prior visible conversation history snippets from the current session, compacted transcripts, or all archives when explicitly requested.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]string{"type": "string"},
			"scope": map[string]interface{}{
				"type": "string",
				"enum": []string{
					HistorySearchScopeCurrentSession,
					HistorySearchScopeSessionArchive,
					HistorySearchScopeAllArchives,
				},
			},
			"limit": map[string]string{"type": "integer"},
			"role": map[string]interface{}{
				"type": "string",
				"enum": []string{"user", "assistant", "any"},
			},
		},
		"required": []string{"query"},
	}, nil), func(ctx context.Context, args historySearchArgs) (ToolResult, error) {
		if runtime == nil {
			return ToolResult{}, fmt.Errorf("history search runtime unavailable")
		}
		sessionID := strings.TrimSpace(SessionIDFromContext(ctx))
		runtimeCtx := SessionContextFromContext(ctx)
		if sessionID == "" {
			sessionID = strings.TrimSpace(runtimeCtx.SessionID)
		}
		result, err := runtime.SearchHistory(ctx, sessionID, runtimeCtx, HistorySearchRequest{
			Query: strings.TrimSpace(args.Query),
			Scope: strings.TrimSpace(args.Scope),
			Limit: args.Limit,
			Role:  strings.TrimSpace(args.Role),
		})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: result}, nil
	})
}
