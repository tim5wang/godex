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
