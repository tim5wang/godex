package tools

import (
	"context"
	"fmt"
)

// uiCardField is one form field of a ui_card form.
type uiCardField struct {
	Name        string         `json:"name"`
	Label       string         `json:"label"`
	Type        string         `json:"type"`
	Required    bool           `json:"required,omitempty"`
	Placeholder string         `json:"placeholder,omitempty"`
	Options     []uiCardOption `json:"options,omitempty"`
}

type uiCardOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type uiCardAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type uiCardArgs struct {
	Kind    string         `json:"kind"`
	Title   string         `json:"title,omitempty"`
	Content string         `json:"content,omitempty"`
	Fields  []uiCardField  `json:"fields,omitempty"`
	Actions []uiCardAction `json:"actions,omitempty"`
}

// NewUICardTool creates the ui_card tool: it emits a structured interactive
// card (form / button group / markdown card) that the Web UI renders as
// interactive elements (see Workflows Launch view). The tool output JSON is
// what the frontend parses; TUI/CLI see the raw JSON.
func NewUICardTool() Tool {
	return NewTypedTool(NewToolSpec("ui_card", "Emit a structured interactive card (form, button group, or markdown card) so the user can fill in structured input or click one-click choices instead of typing free text. Use kind=\"form\" with fields for gathering structured input, kind=\"button_group\" with actions for choices, kind=\"card\" for a styled markdown block. The Web UI renders these as interactive elements; other clients show the JSON.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"kind": map[string]interface{}{"type": "string", "enum": []string{"form", "button_group", "card"}},
			"title": map[string]string{
				"type": "string",
			},
			"content": map[string]string{
				"type": "string",
			},
			"fields": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]string{"type": "string"},
						"label": map[string]string{
							"type": "string",
						},
						"type": map[string]interface{}{"type": "string", "enum": []string{"text", "textarea", "select", "number"}},
						"required": map[string]string{
							"type": "boolean",
						},
						"placeholder": map[string]string{
							"type": "string",
						},
						"options": map[string]interface{}{
							"type": "array",
							"items": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"label": map[string]string{"type": "string"},
									"value": map[string]string{"type": "string"},
								},
							},
						},
					},
				},
			},
			"actions": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id": map[string]string{"type": "string"},
						"label": map[string]string{
							"type": "string",
						},
						"kind": map[string]interface{}{"type": "string", "enum": []string{"message", "command", "approve", "url"}},
						"value": map[string]string{
							"type": "string",
						},
					},
				},
			},
		},
		"required": []string{"kind"},
	}, nil), func(ctx context.Context, args uiCardArgs) (ToolResult, error) {
		if args.Kind != "form" && args.Kind != "button_group" && args.Kind != "card" {
			return ToolResult{}, fmt.Errorf("kind must be one of form|button_group|card")
		}
		// The tool is a pure emitter: validate the shape and echo it back as
		// structured output so the frontend can render the card.
		return ToolResult{Structured: args}, nil
	})
}
