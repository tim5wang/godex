package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

const modelProfileConfigID = "model_profile"

// ACP session modes map onto GoDex's session creation modes (see
// internal/agent/session_mode.go). "default" is the standard full-tool mode;
// "minimal" is the lean mode for quick file/shell work.
const (
	acpSessionModeDefault = "default"
	acpSessionModeMinimal = "minimal"
)

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

// sessionModeFeatureProvider is the optional runtime session-mode surface.
// Agents that implement it let an ACP client switch the session's creation
// mode ("default"/"minimal") via session/set_mode.
type sessionModeFeatureProvider interface {
	SessionMode(context.Context, string) (string, error)
	SetSessionMode(context.Context, string, string) error
}

// sessionListFeatureProvider is the optional persisted-session list surface.
// When implemented, session/list merges the backend's persisted ACP sessions
// with the in-memory ones so a restarted agent still lists its sessions.
type sessionListFeatureProvider interface {
	ListSessions(context.Context) ([]acp.SessionInfo, error)
}

type sessionMCPBridgeCloseProvider interface {
	CloseACPMCPBridge(context.Context, string)
}

// BackendFeatures adapts *backend.Service into ACP session features. Session
// mode methods are optional on the wrapped backend: backends that implement
// runtime mode switching (backend.Service does) are used; others degrade to
// no-ops with the default mode.
type BackendFeatures struct {
	Backend interface {
		OpenSession(context.Context, backend.SessionLocator) (*backend.OpenedSession, error)
		Models(context.Context, string) (backend.ModelsView, error)
		SetSessionModelProfile(context.Context, string, string) (backend.ModelsView, error)
	}
}

// backendSessionMode exposes the optional runtime mode surface of a backend.
type backendSessionMode interface {
	SessionMode(context.Context, string) (string, error)
	SetSessionMode(context.Context, string, string) error
}

// backendSessionLister exposes the optional persisted-session listing surface
// of a backend. When present, session/list can include sessions persisted by
// a previous agent process.
type backendSessionLister interface {
	ListSessions(context.Context, backend.SessionListFilter) ([]backend.ListedSession, error)
}

type backendMCPBridgeCloser interface {
	CloseACPMCPBridge(context.Context, string)
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

func (f BackendFeatures) CloseACPMCPBridge(ctx context.Context, sessionID string) {
	if closer, ok := f.Backend.(backendMCPBridgeCloser); ok && closer != nil {
		closer.CloseACPMCPBridge(ctx, sessionID)
	}
}

func (f BackendFeatures) AvailableCommands(context.Context, string) []commands.CommandMetadata {
	return commands.AvailableMetadata()
}

// SessionMode reads the session's current creation mode from the backend when
// it supports runtime mode switching; otherwise the default (empty) mode.
func (f BackendFeatures) SessionMode(ctx context.Context, sessionID string) (string, error) {
	if modeBackend, ok := f.Backend.(backendSessionMode); ok && modeBackend != nil {
		return modeBackend.SessionMode(ctx, sessionID)
	}
	return "", nil
}

// SetSessionMode applies a session creation mode at runtime when the backend
// supports it; backends without runtime mode switching accept the call as a
// no-op (the mode is still recorded on the ACP session state).
func (f BackendFeatures) SetSessionMode(ctx context.Context, sessionID, mode string) error {
	if modeBackend, ok := f.Backend.(backendSessionMode); ok && modeBackend != nil {
		return modeBackend.SetSessionMode(ctx, sessionID, mode)
	}
	return nil
}

// ListSessions returns the backend's persisted ACP-channel sessions when the
// backend supports listing; backends without it contribute nothing. The
// session id is the locator key that EnsureSession binds (channel=acp), and
// the cwd is taken from the session's project_dir metadata.
func (f BackendFeatures) ListSessions(ctx context.Context) ([]acp.SessionInfo, error) {
	lister, ok := f.Backend.(backendSessionLister)
	if !ok || lister == nil {
		return nil, nil
	}
	items, err := lister.ListSessions(ctx, backend.SessionListFilter{Channel: "acp"})
	if err != nil {
		return nil, err
	}
	out := make([]acp.SessionInfo, 0, len(items))
	for _, item := range items {
		sid := strings.TrimSpace(item.Locator.Key)
		if sid == "" {
			continue
		}
		info := acp.SessionInfo{
			SessionId: acp.SessionId(sid),
		}
		if dir := strings.TrimSpace(item.Locator.Metadata["project_dir"]); dir != "" {
			info.Cwd = dir
		}
		if title := strings.TrimSpace(item.Title); title != "" {
			info.Title = &title
		}
		if !item.UpdatedAt.IsZero() {
			ts := item.UpdatedAt.UTC().Format(time.RFC3339)
			info.UpdatedAt = &ts
		}
		out = append(out, info)
	}
	return out, nil
}

// acpSessionModes returns the modes godex advertises to ACP clients: the
// standard full-tool mode and the lean minimal mode.
func acpSessionModes() []acp.SessionMode {
	defaultDesc := "Standard GoDex mode with the full active tool set."
	minimalDesc := "Lean mode for quick file/shell work (core tools only)."
	return []acp.SessionMode{
		{Id: acp.SessionModeId(acpSessionModeDefault), Name: "Default", Description: &defaultDesc},
		{Id: acp.SessionModeId(acpSessionModeMinimal), Name: "Minimal", Description: &minimalDesc},
	}
}

// acpSessionModeState builds the ACP mode state for a session. current may be
// empty (default) and is normalized to the default mode id.
func acpSessionModeState(current string) *acp.SessionModeState {
	current = strings.TrimSpace(current)
	if current == "" {
		current = acpSessionModeDefault
	}
	return &acp.SessionModeState{
		AvailableModes: acpSessionModes(),
		CurrentModeId:  acp.SessionModeId(current),
	}
}

// validateAcpSessionMode reports whether the mode id is one godex supports.
func validateAcpSessionMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case acpSessionModeDefault, acpSessionModeMinimal:
		return true
	default:
		return false
	}
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
