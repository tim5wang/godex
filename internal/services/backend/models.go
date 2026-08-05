package backend

import (
	"context"
	"fmt"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/security"
	"sort"
	"strings"
)

func (s *Service) Models(ctx context.Context, sessionID string) (ModelsView, error) {
	_ = ctx
	sessionProfileID := ""
	reasoningEffort := ""
	if strings.TrimSpace(sessionID) != "" {
		session, err := s.requireSession(sessionID)
		if err != nil {
			return ModelsView{}, err
		}
		session.mu.RLock()
		sessionProfileID = strings.TrimSpace(session.modelProfileID)
		reasoningEffort = normalizeSessionReasoningEffort(session.reasoningEffort)
		session.mu.RUnlock()
	}
	return s.modelsView(sessionProfileID, reasoningEffort), nil
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
	return s.modelsView(profile.ID, reasoningEffort), nil
}

func (s *Service) modelsView(sessionProfileID, reasoningEffort string) ModelsView {
	cfg := s.cfg
	if cfg == nil {
		return ModelsView{}
	}
	defaultID := strings.TrimSpace(cfg.DefaultProfileID)
	reasoningEffort = normalizeSessionReasoningEffort(reasoningEffort)
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

// SecuritySummary returns a lightweight Capability/Identity/Knowledge risk view.
