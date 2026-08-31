package history

import (
	"context"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
)

type SearchRequest struct {
	Query string `json:"query"`
	Scope string `json:"scope,omitempty"`
	Limit int    `json:"limit,omitempty"`
	Role  string `json:"role,omitempty"`
}

type SearchSnippet struct {
	SourceKind   string    `json:"source_kind"`
	SessionID    string    `json:"session_id,omitempty"`
	SessionTitle string    `json:"session_title,omitempty"`
	Timestamp    time.Time `json:"timestamp,omitempty"`
	Role         string    `json:"role"`
	TextExcerpt  string    `json:"text_excerpt"`
	MatchTerms   []string  `json:"match_terms,omitempty"`
	Score        int       `json:"score"`
}

type SearchResult struct {
	Scope      string          `json:"scope"`
	MatchCount int             `json:"match_count"`
	Snippets   []SearchSnippet `json:"snippets,omitempty"`
}

// Current exposes current in-memory session history to the search runtime.
type Current interface {
	GetMessages() []protocol.Message
	TranscriptRefs() []string
}

// Runtime performs scoped history lookups for one session.
type Runtime interface {
	SearchHistory(context.Context, string, automation.SessionContext, SearchRequest) (SearchResult, error)
}
