package server

import (
	"context"
	"fmt"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

const modelProfileConfigID = "model_profile"

// SessionFeatureProvider supplies ACP session metadata that lives in the
// unified GoDex backend.
type SessionFeatureProvider interface {
	EnsureSession(context.Context, string) (string, error)
	Models(context.Context, string) (backend.ModelsView, error)
	SetSessionModelProfile(context.Context, string, string) (backend.ModelsView, error)
}

type commandFeatureProvider interface {
	AvailableCommands(context.Context, string) []commands.CommandMetadata
}

// BackendFeatures adapts *backend.Service into ACP session features.
type BackendFeatures struct {
	Backend interface {
		OpenSession(context.Context, backend.SessionLocator) (*backend.OpenedSession, error)
		Models(context.Context, string) (backend.ModelsView, error)
		SetSessionModelProfile(context.Context, string, string) (backend.ModelsView, error)
	}
}

func (f BackendFeatures) EnsureSession(ctx context.Context, acpSessionID string) (string, error) {
	if f.Backend == nil {
		return "", nil
	}
	opened, err := f.Backend.OpenSession(ctx, backend.SessionLocator{Channel: "acp", Key: strings.TrimSpace(acpSessionID)})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(opened.SessionID), nil
}

func (f BackendFeatures) Models(ctx context.Context, sessionID string) (backend.ModelsView, error) {
	if f.Backend == nil {
		return backend.ModelsView{}, nil
	}
	return f.Backend.Models(ctx, sessionID)
}

func (f BackendFeatures) SetSessionModelProfile(ctx context.Context, sessionID, profileID string) (backend.ModelsView, error) {
	if f.Backend == nil {
		return backend.ModelsView{}, nil
	}
	return f.Backend.SetSessionModelProfile(ctx, sessionID, profileID)
}

func (f BackendFeatures) AvailableCommands(context.Context, string) []commands.CommandMetadata {
	return commands.AvailableMetadata()
}

func acpCommands(items []commands.CommandMetadata) []acp.AvailableCommand {
	if len(items) == 0 {
		items = commands.AvailableMetadata()
	}
	out := make([]acp.AvailableCommand, 0, len(items))
	for _, item := range items {
		name := strings.TrimPrefix(strings.TrimSpace(item.Name), "/")
		if name == "" {
			continue
		}
		hint := strings.TrimSpace(item.InputHint)
		if hint == "" {
			hint = "arguments"
		}
		out = append(out, acp.AvailableCommand{
			Name:        name,
			Description: strings.TrimSpace(item.Description),
			Input:       &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: hint}},
		})
	}
	return out
}

func acpModelConfigOptions(view backend.ModelsView) []acp.SessionConfigOption {
	current := strings.TrimSpace(view.SessionProfileID)
	if current == "" {
		current = strings.TrimSpace(view.DefaultProfileID)
	}
	options := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(view.Profiles))
	for _, profile := range view.Profiles {
		id := strings.TrimSpace(profile.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(profile.Name)
		if name == "" {
			name = strings.TrimSpace(profile.Provider + "/" + profile.Model)
		}
		if name == "/" {
			name = id
		}
		desc := strings.TrimSpace(profile.Provider)
		if strings.TrimSpace(profile.Model) != "" {
			if desc != "" {
				desc += " / "
			}
			desc += strings.TrimSpace(profile.Model)
		}
		option := acp.SessionConfigSelectOption{Name: name, Value: acp.SessionConfigValueId(id)}
		if desc != "" {
			option.Description = &desc
		}
		options = append(options, option)
		if profile.Selected {
			current = id
		}
	}
	if current == "" && len(options) > 0 {
		current = string(options[0].Value)
	}
	if len(options) == 0 && current == "" {
		return nil
	}
	category := acp.SessionConfigOptionCategoryModel
	desc := "Choose the GoDex model profile for this ACP session."
	option := acp.NewSessionConfigOptionSelect(acp.SessionConfigValueId(current), acp.SessionConfigSelectOptions{Ungrouped: &options})
	option.Select.Id = acp.SessionConfigId(modelProfileConfigID)
	option.Select.Name = "Model"
	option.Select.Category = &category
	option.Select.Description = &desc
	return []acp.SessionConfigOption{option}
}

func modelProfileIDFromConfigRequest(req acp.SetSessionConfigOptionRequest) (string, string, bool) {
	if req.ValueId == nil {
		return "", "", false
	}
	if strings.TrimSpace(string(req.ValueId.ConfigId)) != modelProfileConfigID {
		return "", "", false
	}
	return strings.TrimSpace(string(req.ValueId.SessionId)), strings.TrimSpace(string(req.ValueId.Value)), true
}

func validateModelProfile(view backend.ModelsView, profileID string) error {
	profileID = strings.TrimSpace(profileID)
	for _, profile := range view.Profiles {
		if strings.TrimSpace(profile.ID) == profileID {
			return nil
		}
	}
	return fmt.Errorf("model profile not found: %s", profileID)
}
