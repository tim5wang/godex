package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/core/memory"
)

// MemoryWriter persists and browses durable cross-session memory.
type MemoryWriter interface {
	Remember(input memory.SaveInput) (*memory.Entry, error)
	Forget(input memory.ForgetInput) (*memory.Entry, error)
	List() ([]memory.Entry, error)
	Get(idOrTitle string) (*memory.StoredMemory, error)
	Search(opts memory.SearchOptions) ([]memory.StoredMemory, error)
	ListCandidates() ([]memory.Candidate, error)
	AcceptCandidate(fingerprint string) (*memory.Entry, error)
	DismissCandidate(fingerprint string) (*memory.Candidate, error)
}

type rememberMemoryArgs struct {
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Content    string   `json:"content"`
	MemoryType string   `json:"memory_type"`
	Source     string   `json:"source,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

type forgetMemoryArgs struct {
	Title string `json:"title,omitempty"`
	File  string `json:"file,omitempty"`
}

type listMemoryArgs struct{}

type getMemoryArgs struct {
	IDOrTitle string `json:"id_or_title"`
}

type searchMemoryArgs struct {
	Query      string `json:"query,omitempty"`
	MemoryType string `json:"memory_type,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Source     string `json:"source,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type fingerprintArgs struct {
	Fingerprint string `json:"fingerprint"`
}

// NewRememberMemoryTool creates a new remember memory tool.
func NewRememberMemoryTool(writer MemoryWriter) Tool {
	return NewTypedTool(NewToolSpec("remember_memory", "Save durable project memory for future sessions", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"title":       map[string]string{"type": "string"},
			"summary":     map[string]string{"type": "string"},
			"content":     map[string]string{"type": "string"},
			"memory_type": map[string]interface{}{"type": "string", "enum": []string{"user", "workflow", "project", "warning"}},
			"source":      map[string]string{"type": "string"},
			"tags": map[string]interface{}{
				"type": "array",
				"items": map[string]string{
					"type": "string",
				},
			},
		},
		"required": []string{"title", "summary", "content", "memory_type"},
	}, nil), func(ctx context.Context, args rememberMemoryArgs) (ToolResult, error) {
		_ = ctx
		if args.Title == "" {
			return ToolResult{}, fmt.Errorf("missing title argument")
		}
		if args.Summary == "" {
			return ToolResult{}, fmt.Errorf("missing summary argument")
		}
		if args.Content == "" {
			return ToolResult{}, fmt.Errorf("missing content argument")
		}
		if args.MemoryType == "" {
			return ToolResult{}, fmt.Errorf("missing memory_type argument")
		}
		entry, err := writer.Remember(memory.SaveInput{
			Title:   args.Title,
			Summary: args.Summary,
			Content: args.Content,
			Type:    memory.Type(args.MemoryType),
			Source:  args.Source,
			Tags:    args.Tags,
		})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: map[string]interface{}{
			"id":          entry.ID,
			"title":       entry.Title,
			"file":        entry.File,
			"summary":     entry.Summary,
			"memory_type": entry.Type,
			"source":      entry.Source,
			"tags":        entry.Tags,
			"status":      "saved",
		}}, nil
	})
}

// NewForgetMemoryTool creates a new forget memory tool.
func NewForgetMemoryTool(writer MemoryWriter) Tool {
	return NewTypedTool(NewToolSpec("forget_memory", "Delete stale durable project memory", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"title": map[string]string{"type": "string"},
			"file":  map[string]string{"type": "string"},
		},
	}, nil), func(ctx context.Context, args forgetMemoryArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Title) == "" && strings.TrimSpace(args.File) == "" {
			return ToolResult{}, fmt.Errorf("missing title or file argument")
		}
		entry, err := writer.Forget(memory.ForgetInput{Title: args.Title, File: args.File})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: map[string]interface{}{
			"id":          entry.ID,
			"title":       entry.Title,
			"file":        entry.File,
			"summary":     entry.Summary,
			"memory_type": entry.Type,
			"status":      "forgotten",
		}}, nil
	})
}

// NewListMemoryTool creates a new list memory tool.
func NewListMemoryTool(writer MemoryWriter) Tool {
	return NewTypedTool(NewToolSpec("list_memory", "List durable project memories with metadata", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args listMemoryArgs) (ToolResult, error) {
		_ = ctx
		items, err := writer.List()
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: map[string]interface{}{"memories": items}}, nil
	})
}

// NewGetMemoryTool creates a new get memory tool.
func NewGetMemoryTool(writer MemoryWriter) Tool {
	return NewTypedTool(NewToolSpec("get_memory", "Get one durable memory by id or title", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id_or_title": map[string]string{"type": "string"},
		},
		"required": []string{"id_or_title"},
	}, nil), func(ctx context.Context, args getMemoryArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.IDOrTitle) == "" {
			return ToolResult{}, fmt.Errorf("missing id_or_title argument")
		}
		record, err := writer.Get(args.IDOrTitle)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: record}, nil
	})
}

// NewSearchMemoryTool creates a new search memory tool.
func NewSearchMemoryTool(writer MemoryWriter) Tool {
	return NewTypedTool(NewToolSpec("search_memory", "Search durable memories by keyword, type, tag, or source", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query":       map[string]string{"type": "string"},
			"memory_type": map[string]interface{}{"type": "string", "enum": []string{"user", "workflow", "project", "warning"}},
			"tag":         map[string]string{"type": "string"},
			"source":      map[string]string{"type": "string"},
			"limit":       map[string]string{"type": "integer"},
		},
	}, nil), func(ctx context.Context, args searchMemoryArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Query) == "" && strings.TrimSpace(args.MemoryType) == "" && strings.TrimSpace(args.Tag) == "" && strings.TrimSpace(args.Source) == "" {
			return ToolResult{}, fmt.Errorf("missing query or filter arguments")
		}
		items, err := writer.Search(memory.SearchOptions{
			Query:  args.Query,
			Type:   memory.Type(args.MemoryType),
			Tag:    args.Tag,
			Source: args.Source,
			Limit:  args.Limit,
		})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: map[string]interface{}{"memories": items}}, nil
	})
}

// NewListMemoryCandidatesTool creates a new list memory candidates tool.
func NewListMemoryCandidatesTool(writer MemoryWriter) Tool {
	return NewTypedTool(NewToolSpec("list_memory_candidates", "List pending durable-memory candidates", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args listMemoryArgs) (ToolResult, error) {
		_ = ctx
		items, err := writer.ListCandidates()
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: map[string]interface{}{"candidates": items}}, nil
	})
}

// NewAcceptMemoryCandidateTool creates a new accept memory candidate tool.
func NewAcceptMemoryCandidateTool(writer MemoryWriter) Tool {
	return NewTypedTool(NewToolSpec("accept_memory_candidate", "Accept one pending memory candidate into durable memory", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"fingerprint": map[string]string{"type": "string"},
		},
		"required": []string{"fingerprint"},
	}, nil), func(ctx context.Context, args fingerprintArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Fingerprint) == "" {
			return ToolResult{}, fmt.Errorf("missing fingerprint argument")
		}
		entry, err := writer.AcceptCandidate(args.Fingerprint)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: map[string]interface{}{
			"id":          entry.ID,
			"title":       entry.Title,
			"file":        entry.File,
			"summary":     entry.Summary,
			"memory_type": entry.Type,
			"status":      "accepted",
		}}, nil
	})
}

// NewMemoryTool creates a unified memory management tool replacing the 8 individual memory CRUD tools.
type memoryToolArgs struct {
	Action      string   `json:"action"`
	Title       string   `json:"title,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Content     string   `json:"content,omitempty"`
	MemoryType  string   `json:"memory_type,omitempty"`
	Source      string   `json:"source,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	File        string   `json:"file,omitempty"`
	IDOrTitle   string   `json:"id_or_title,omitempty"`
	Query       string   `json:"query,omitempty"`
	Tag         string   `json:"tag,omitempty"`
	Limit       int      `json:"limit,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
}

func NewMemoryTool(writer MemoryWriter) Tool {
	return NewTypedTool(NewToolSpec("memory", "Manage durable cross-session memory. Use action to select operation:", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":      map[string]interface{}{"type": "string", "enum": []string{"list", "get", "search", "candidates", "accept", "dismiss", "remember", "forget"}},
			"title":       map[string]string{"type": "string"},
			"summary":     map[string]string{"type": "string"},
			"content":     map[string]string{"type": "string"},
			"memory_type": map[string]interface{}{"type": "string", "enum": []string{"user", "workflow", "project", "warning"}},
			"source":      map[string]string{"type": "string"},
			"tags":        map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
			"file":        map[string]string{"type": "string"},
			"id_or_title": map[string]string{"type": "string"},
			"query":       map[string]string{"type": "string"},
			"tag":         map[string]string{"type": "string"},
			"limit":       map[string]string{"type": "integer"},
			"fingerprint": map[string]string{"type": "string"},
		},
		"required": []string{"action"},
	}, nil), func(ctx context.Context, args memoryToolArgs) (ToolResult, error) {
		_ = ctx
		switch args.Action {
		case "list":
			items, err := writer.List()
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"memories": items}}, nil

		case "get":
			if strings.TrimSpace(args.IDOrTitle) == "" {
				return ToolResult{}, fmt.Errorf("missing id_or_title for get action")
			}
			record, err := writer.Get(args.IDOrTitle)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: record}, nil

		case "search":
			items, err := writer.Search(memory.SearchOptions{
				Query:  args.Query,
				Type:   memory.Type(args.MemoryType),
				Tag:    args.Tag,
				Source: args.Source,
				Limit:  args.Limit,
			})
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"memories": items}}, nil

		case "candidates":
			items, err := writer.ListCandidates()
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{"candidates": items}}, nil

		case "accept":
			if strings.TrimSpace(args.Fingerprint) == "" {
				return ToolResult{}, fmt.Errorf("missing fingerprint for accept action")
			}
			entry, err := writer.AcceptCandidate(args.Fingerprint)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{
				"id":          entry.ID,
				"title":       entry.Title,
				"file":        entry.File,
				"summary":     entry.Summary,
				"memory_type": entry.Type,
				"status":      "accepted",
			}}, nil

		case "dismiss":
			if strings.TrimSpace(args.Fingerprint) == "" {
				return ToolResult{}, fmt.Errorf("missing fingerprint for dismiss action")
			}
			candidate, err := writer.DismissCandidate(args.Fingerprint)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{
				"fingerprint": candidate.Fingerprint,
				"title":       candidate.Title,
				"summary":     candidate.Summary,
				"memory_type": candidate.Type,
				"status":      "dismissed",
			}}, nil

		case "remember":
			if args.Title == "" {
				return ToolResult{}, fmt.Errorf("missing title for remember action")
			}
			if args.Summary == "" {
				return ToolResult{}, fmt.Errorf("missing summary for remember action")
			}
			if args.Content == "" {
				return ToolResult{}, fmt.Errorf("missing content for remember action")
			}
			if args.MemoryType == "" {
				return ToolResult{}, fmt.Errorf("missing memory_type for remember action")
			}
			entry, err := writer.Remember(memory.SaveInput{
				Title:   args.Title,
				Summary: args.Summary,
				Content: args.Content,
				Type:    memory.Type(args.MemoryType),
				Source:  args.Source,
				Tags:    args.Tags,
			})
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{
				"id":          entry.ID,
				"title":       entry.Title,
				"file":        entry.File,
				"summary":     entry.Summary,
				"memory_type": entry.Type,
				"source":      entry.Source,
				"tags":        entry.Tags,
				"status":      "saved",
			}}, nil

		case "forget":
			if strings.TrimSpace(args.Title) == "" && strings.TrimSpace(args.File) == "" {
				return ToolResult{}, fmt.Errorf("missing title or file for forget action")
			}
			entry, err := writer.Forget(memory.ForgetInput{Title: args.Title, File: args.File})
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{
				"id":          entry.ID,
				"title":       entry.Title,
				"file":        entry.File,
				"summary":     entry.Summary,
				"memory_type": entry.Type,
				"status":      "forgotten",
			}}, nil

		default:
			return ToolResult{}, fmt.Errorf("unknown action: %s. Valid actions: list, get, search, candidates, accept, dismiss, remember, forget", args.Action)
		}
	})
}

// NewDismissMemoryCandidateTool creates a new dismiss memory candidate tool.
func NewDismissMemoryCandidateTool(writer MemoryWriter) Tool {
	return NewTypedTool(NewToolSpec("dismiss_memory_candidate", "Dismiss one pending memory candidate", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"fingerprint": map[string]string{"type": "string"},
		},
		"required": []string{"fingerprint"},
	}, nil), func(ctx context.Context, args fingerprintArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Fingerprint) == "" {
			return ToolResult{}, fmt.Errorf("missing fingerprint argument")
		}
		candidate, err := writer.DismissCandidate(args.Fingerprint)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: map[string]interface{}{
			"fingerprint": candidate.Fingerprint,
			"title":       candidate.Title,
			"summary":     candidate.Summary,
			"memory_type": candidate.Type,
			"status":      "dismissed",
		}}, nil
	})
}
