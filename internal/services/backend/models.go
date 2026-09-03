package backend

import (
	"context"
	"fmt"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/security"
	"sort"
	"strings"
)

func (s *Service) Models(ctx context.Context, sessionID string) (ModelsView, error) {
	_ = ctx
	sessionProfileID := ""
	reasoningEffort := ""
	acpModel := ""
	if strings.TrimSpace(sessionID) != "" {
		session, err := s.requireSession(sessionID)
		if err != nil {
			return ModelsView{}, err
		}
		session.mu.RLock()
		sessionProfileID = strings.TrimSpace(session.modelProfileID)
		reasoningEffort = normalizeSessionReasoningEffort(session.reasoningEffort)
		acpModel = strings.TrimSpace(session.acpModel)
		session.mu.RUnlock()
	}
	return s.modelsView(sessionProfileID, reasoningEffort, acpModel), nil
}

// SetSessionACPAgentModel persists the ACP model override for a session. The
// value is a raw ACP model id (e.g. `["ais","llm-gateway--deepseek-v4-flash"]`)
// that the ACP harness forwards via session config "model". Empty clears it.
func (s *Service) SetSessionACPAgentModel(ctx context.Context, sessionID, model string) (ModelsView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return ModelsView{}, err
	}
	model = strings.TrimSpace(model)
	session.mu.Lock()
	session.acpModel = model
	sessionProfileID := strings.TrimSpace(session.modelProfileID)
	reasoningEffort := normalizeSessionReasoningEffort(session.reasoningEffort)
	session.mu.Unlock()
	now := s.now()
	if err := s.persistSession(session, now); err != nil {
		return ModelsView{}, err
	}
	session.events.Emit(events.Event{
		SessionID: session.id,
		Type:      events.EventSnapshotReady,
		Timestamp: now,
		Payload: events.SnapshotPayload{
			UpdatedAt: now,
			Running:   false,
		},
	})
	_ = s.writeSessionTimeline(session)
	return s.modelsView(sessionProfileID, reasoningEffort, model), nil
}

// SetSessionModelProfile persists and applies a session-specific model profile.
func (s *Service) SetSessionModelProfile(ctx context.Context, sessionID, profileID string) (ModelsView, error) {
	return s.SetSessionModelProfileWithReasoning(ctx, sessionID, profileID, "")
}

// SetSessionModelProfileWithReasoning persists and applies a session-specific model profile plus optional reasoning effort override.
func (s *Service) SetSessionModelProfileWithReasoning(ctx context.Context, sessionID, profileID, reasoningEffort string) (ModelsView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return ModelsView{}, err
	}
	profile, ok := s.cfg.ModelProfileByID(profileID)
	if !ok {
		return ModelsView{}, fmt.Errorf("model profile not found: %s", profileID)
	}
	reasoningEffort = normalizeSessionReasoningEffort(reasoningEffort)
	appliedProfile := profile
	if reasoningEffort != "" {
		appliedProfile.ReasoningEffort = reasoningEffort
	}
	session.mu.Lock()
	session.modelProfileID = profile.ID
	session.reasoningEffort = reasoningEffort
	sessionProfileID := strings.TrimSpace(session.modelProfileID)
	acpModel := strings.TrimSpace(session.acpModel)
	session.mu.Unlock()
	session.agent.ApplyModelProfile(appliedProfile)
	now := s.now()
	if err := s.persistSession(session, now); err != nil {
		return ModelsView{}, err
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:        now,
		Category:  "capability",
		Action:    "set_session_model",
		Severity:  "info",
		SessionID: session.id,
		Summary:   "Session model profile changed to " + profile.ID,
		Metadata: map[string]string{
			"profile_id":       profile.ID,
			"provider":         profile.Provider,
			"model":            profile.Model,
			"reasoning_effort": reasoningEffort,
		},
	})
	session.events.Emit(events.Event{
		SessionID: session.id,
		Type:      events.EventSnapshotReady,
		Timestamp: now,
		Payload: events.SnapshotPayload{
			UpdatedAt: now,
			Running:   false,
		},
	})
	_ = s.writeSessionTimeline(session)
	return s.modelsView(sessionProfileID, reasoningEffort, acpModel), nil
}

func (s *Service) modelsView(sessionProfileID, reasoningEffort, acpModel string) ModelsView {
	cfg := s.cfg
	if cfg == nil {
		return ModelsView{}
	}
	defaultID := strings.TrimSpace(cfg.DefaultProfileID)
	reasoningEffort = normalizeSessionReasoningEffort(reasoningEffort)
	acpModel = strings.TrimSpace(acpModel)
	profiles := make([]ModelProfile, 0, len(cfg.ModelProfiles))
	for id := range cfg.ModelProfiles {
		profile, ok := cfg.ModelProfileByID(id)
		if !ok {
			continue
		}
		profiles = append(profiles, ModelProfile{
			ID:                profile.ID,
			Name:              profile.Name,
			Provider:          profile.Provider,
			ProviderName:      providerDisplayName(cfg, profile.ID, profile.Provider),
			Model:             profile.Model,
			BaseURL:           profile.BaseURL,
			MaxTokens:         profile.MaxTokens,
			TimeoutSeconds:    profile.TimeoutSeconds,
			SupportsStreaming: profile.SupportsStreaming,
			SupportsVision:    profile.SupportsVision,
			ReasoningEffort:   profile.ReasoningEffort,
			Default:           profile.ID == defaultID,
			Selected:          strings.TrimSpace(sessionProfileID) != "" && profile.ID == strings.TrimSpace(sessionProfileID),
		})
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Default != profiles[j].Default {
			return profiles[i].Default
		}
		return profiles[i].ID < profiles[j].ID
	})
	return ModelsView{
		DefaultProfileID: defaultID,
		SessionProfileID: strings.TrimSpace(sessionProfileID),
		ReasoningEffort:  reasoningEffort,
		AcpModel:         acpModel,
		Profiles:         profiles,
	}
}

func normalizeSessionReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

// providerDisplayName resolves the human-readable provider label for a model
// profile so UI model pickers can group profiles by provider. The profile ID
// is "<provider>.<model>"; we resolve the provider part against the configured
// LLM providers (which carry a display Name) and fall back to the provider ID
// and then the protocol type.
func providerDisplayName(cfg *config.Config, profileID, providerType string) string {
	if ref, ok := llm.ParseProfileID(profileID); ok && cfg != nil {
		if provider, found := cfg.LLMProviders[ref.Provider]; found {
			if name := strings.TrimSpace(provider.Name); name != "" {
				return name
			}
			return strings.TrimSpace(ref.Provider)
		}
		return strings.TrimSpace(ref.Provider)
	}
	if strings.TrimSpace(providerType) != "" {
		return strings.TrimSpace(providerType)
	}
	return strings.TrimSpace(profileID)
}

// SecuritySummary returns a lightweight Capability/Identity/Knowledge risk view.
