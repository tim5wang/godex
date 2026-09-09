package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/selfdocs"
)

type selfdocsArgs struct {
	Action string `json:"action"` // list | get | search
	ID     string `json:"id,omitempty"`
	Query  string `json:"query,omitempty"`
}

// NewSelfdocsTool creates a tool that lets godex look up its own features
// (self-knowledge extracted from `// godex-feature:` comments at build time).
func NewSelfdocsTool() Tool {
	return NewTypedTool(NewToolSpec("godex_docs", "Look up GoDex's own features (self-knowledge compiled from source comments). action=list: list all feature ids with one-line summaries. action=get: show the full entry for one feature id (description, entry points, docs, source location). action=search: find features whose id/description/entry/docs contain the query. Use this before claiming a capability is unavailable or when asked about godex functionality.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{"type": "string", "enum": []string{"list", "get", "search"}},
			"id":     map[string]string{"type": "string"},
			"query":  map[string]string{"type": "string"},
		},
		"required": []string{"action"},
	}, nil), func(ctx context.Context, args selfdocsArgs) (ToolResult, error) {
		_ = ctx
		switch args.Action {
		case "list":
			return ToolResult{Structured: selfdocsListResult()}, nil
		case "get":
			if strings.TrimSpace(args.ID) == "" {
				return ToolResult{}, fmt.Errorf("get requires id")
			}
			f, ok := selfdocs.Get(strings.TrimSpace(args.ID))
			if !ok {
				return ToolResult{}, fmt.Errorf("unknown feature id %q (list to see available ids)", args.ID)
			}
			return ToolResult{Structured: toSelfdocsFeatureView(f)}, nil
		case "search":
			if strings.TrimSpace(args.Query) == "" {
				return ToolResult{}, fmt.Errorf("search requires query")
			}
			results := selfdocs.Search(strings.TrimSpace(args.Query))
			return ToolResult{Structured: selfdocsListResultWith(results)}, nil
		default:
			return ToolResult{}, fmt.Errorf("unknown action %q (list|get|search)", args.Action)
		}
	})
}

type selfdocsEntryView struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

type selfdocsFeatureView struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	EntryPoints []string `json:"entry_points,omitempty"`
	Docs        []string `json:"docs,omitempty"`
	Location    string   `json:"location"`
}

func selfdocsListResult() map[string]interface{} {
	items := make([]selfdocsEntryView, 0, len(selfdocs.All()))
	for _, f := range selfdocs.All() {
		items = append(items, selfdocsEntryView{ID: f.ID, Summary: firstLine(f.Description)})
	}
	return map[string]interface{}{
		"count":    len(items),
		"features": items,
	}
}

func selfdocsListResultWith(features []selfdocs.Feature) map[string]interface{} {
	items := make([]selfdocsEntryView, 0, len(features))
	for _, f := range features {
		items = append(items, selfdocsEntryView{ID: f.ID, Summary: firstLine(f.Description)})
	}
	return map[string]interface{}{
		"count":    len(items),
		"features": items,
	}
}

func toSelfdocsFeatureView(f selfdocs.Feature) selfdocsFeatureView {
	return selfdocsFeatureView{
		ID:          f.ID,
		Description: f.Description,
		EntryPoints: f.EntryPoints,
		Docs:        f.Docs,
		Location:    f.Location,
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
