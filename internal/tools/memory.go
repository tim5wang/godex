package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	Action       string   `json:"action"`
	Title        string   `json:"title,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	Content      string   `json:"content,omitempty"`
	MemoryType   string   `json:"memory_type,omitempty"`
	Source       string   `json:"source,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	File         string   `json:"file,omitempty"`
	IDOrTitle    string   `json:"id_or_title,omitempty"`
	Query        string   `json:"query,omitempty"`
	Tag          string   `json:"tag,omitempty"`
	Limit        int      `json:"limit,omitempty"`
	Fingerprint  string   `json:"fingerprint,omitempty"`
	ExpiresAt    *string  `json:"expires_at,omitempty"`
	SupersededBy *string  `json:"superseded_by,omitempty"`
	RecallPolicy *string  `json:"recall_policy,omitempty"`
	Status       string   `json:"status,omitempty"`
}

func NewMemoryTool(writer MemoryWriter) Tool {
	return NewTypedTool(NewToolSpec("memory", "Manage durable cross-session memory. Use action to select operation:", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":        map[string]interface{}{"type": "string", "enum": []string{"list", "get", "search", "candidates", "accept", "dismiss", "remember", "update", "forget"}},
			"title":         map[string]string{"type": "string"},
			"summary":       map[string]string{"type": "string"},
			"content":       map[string]string{"type": "string"},
			"memory_type":   map[string]interface{}{"type": "string", "enum": []string{"user", "workflow", "project", "warning", "work_method", "work_fact"}},
			"source":        map[string]string{"type": "string"},
			"tags":          map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
			"file":          map[string]string{"type": "string"},
			"id_or_title":   map[string]string{"type": "string"},
			"query":         map[string]string{"type": "string"},
			"tag":           map[string]string{"type": "string"},
			"limit":         map[string]string{"type": "integer"},
			"fingerprint":   map[string]string{"type": "string"},
			"expires_at":    map[string]string{"type": "string", "description": "RFC3339 expiry; update with an empty string to clear it."},
			"status":        map[string]interface{}{"type": "string", "enum": []string{"active", "archived", "all"}, "description": "For search: include archived historical records explicitly."},
			"recall_policy": map[string]interface{}{"type": "string", "enum": []string{"auto", "manual"}, "description": "manual keeps snapshots available to explicit search without automatic injection."},
			"superseded_by": map[string]string{"type": "string", "description": "For update: ID or title of the current memory replacing this entry; empty clears the relationship."},
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
				Status: memory.Status(args.Status),
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

		case "update":
			updater, ok := writer.(interface {
				Update(memory.UpdateInput) (*memory.Entry, error)
			})
			if !ok {
				return ToolResult{}, fmt.Errorf("memory update is unavailable")
			}
			match := strings.TrimSpace(args.IDOrTitle)
			if match == "" {
				match = args.Title
			}
			if match == "" {
				return ToolResult{}, fmt.Errorf("missing id_or_title for update action")
			}
			old, err := writer.Get(match)
			if err != nil {
				return ToolResult{}, err
			}
			expires, err := parseMemoryExpiry(args.ExpiresAt)
			if err != nil {
				return ToolResult{}, err
			}
			input := memory.UpdateInput{Match: memory.ForgetInput{File: old.File}, Title: old.Title, Summary: old.Summary, Content: old.Content, Type: old.Type, Source: old.Source, Tags: old.Tags, ExpiresAt: expires, SupersededBy: args.SupersededBy, RecallPolicy: args.RecallPolicy}
			if args.Title != "" {
				input.Title = args.Title
			}
			if args.Summary != "" {
				input.Summary = args.Summary
			}
			if args.Content != "" {
				input.Content = args.Content
			}
			if args.MemoryType != "" {
				input.Type = memory.Type(args.MemoryType)
			}
			if args.Source != "" {
				input.Source = args.Source
			}
			if args.Tags != nil {
				input.Tags = args.Tags
			}
			entry, err := updater.Update(input)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: entry}, nil

		case "remember":
			if args.SupersededBy != nil {
				return ToolResult{}, fmt.Errorf("use update to mark an existing memory superseded")
			}
			expires, err := parseMemoryExpiry(args.ExpiresAt)
			if err != nil {
				return ToolResult{}, err
			}
			missing := make([]string, 0, 4)
			if args.Title == "" {
				missing = append(missing, "title")
			}
			if args.Summary == "" {
				missing = append(missing, "summary")
			}
			if args.Content == "" {
				missing = append(missing, "content")
			}
			if args.MemoryType == "" {
				missing = append(missing, "memory_type")
			}
			if len(missing) > 0 {
				return ToolResult{}, fmt.Errorf("missing required argument(s) for remember action: %s", strings.Join(missing, ", "))
			}
			input := memory.SaveInput{
				Title:   args.Title,
				Summary: args.Summary,
				Content: args.Content,
				Type:    memory.Type(args.MemoryType),
				Source:  args.Source,
				Tags:    args.Tags,
			}
			if expires != nil {
				input.ExpiresAt = *expires
			}
			if args.RecallPolicy != nil {
				input.RecallPolicy = *args.RecallPolicy
			}
			entry, err := writer.Remember(input)
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
			return ToolResult{}, fmt.Errorf("unknown action: %s. Valid actions: list, get, search, candidates, accept, dismiss, remember, update, forget", args.Action)
		}
	})
}

func parseMemoryExpiry(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	if strings.TrimSpace(*value) == "" {
		return new(time.Time), nil
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*value))
	if err != nil {
		return nil, fmt.Errorf("invalid expires_at: use RFC3339")
	}
	return &ts, nil
}
