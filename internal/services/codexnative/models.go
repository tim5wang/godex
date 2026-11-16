package codexnative

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Models uses the thread's provider rather than the daemon's default provider.
// Catalog discovery never resumes a stored thread or changes its settings.
func (s *Service) Models(ctx context.Context, id string) ([]Model, error) {
	c, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	thread, err := s.read(ctx, c, id)
	if err != nil {
		return nil, err
	}
	return s.models(ctx, c, thread.ModelProvider)
}

func (s *Service) models(ctx context.Context, c *client, provider string) ([]Model, error) {
	models := make([]Model, 0)
	seen := map[string]bool{}
	cursor := ""
	for {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if provider != "" {
			params["modelProvider"] = provider
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Data       []Model `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if _, err := c.call(ctx, "model/list", params, &page); err != nil {
			return nil, err
		}
		for _, model := range page.Data {
			if !model.Hidden {
				models = append(models, model)
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return models, nil
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return nil, fmt.Errorf("Codex model/list returned a repeated cursor")
		}
		seen[cursor] = true
	}
}

func (s *Service) UpdateModelSettings(ctx context.Context, id string, settings ModelSettings) (Thread, error) {
	if settings.Model == "" || strings.TrimSpace(settings.Model) != settings.Model || len(settings.Model) > 256 || len(settings.Effort) > 128 {
		return Thread{}, fmt.Errorf("%w: a catalog model and supported reasoning effort are required", ErrInvalid)
	}
	c, err := s.connect(ctx)
	if err != nil {
		return Thread{}, err
	}
	thread, err := s.read(ctx, c, id)
	if err != nil {
		return Thread{}, err
	}
	// Never implicitly resume for a settings change; the UI must first obtain
	// explicit collaboration consent and connect to the existing native thread.
	var status struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(thread.Status, &status)
	if (status.Type != "idle" && status.Type != "active") || thread.CanAcceptDirectInput == nil || !*thread.CanAcceptDirectInput {
		return Thread{}, fmt.Errorf("%w: connect to a writable native thread first", ErrConflict)
	}
	models, err := s.models(ctx, c, thread.ModelProvider)
	if err != nil {
		return Thread{}, err
	}
	valid := false
	for _, model := range models {
		if model.Model != settings.Model {
			continue
		}
		valid = len(model.SupportedReasoningEfforts) == 0 && settings.Effort == ""
		for _, effort := range model.SupportedReasoningEfforts {
			if effort.ReasoningEffort == settings.Effort {
				valid = true
			}
		}
	}
	if !valid {
		return Thread{}, fmt.Errorf("%w: model or reasoning effort is not advertised by this thread's provider", ErrInvalid)
	}
	params := map[string]any{"threadId": id, "model": settings.Model}
	if settings.Effort != "" {
		params["effort"] = settings.Effort
	}
	// This native RPC applies to subsequent turns, not the running turn. Do not
	// fall back to resume/turn-start or write global config on older runtimes.
	if _, err = c.call(ctx, "thread/settings/update", params, nil); err != nil {
		return Thread{}, err
	}
	return s.read(ctx, c, id)
}
