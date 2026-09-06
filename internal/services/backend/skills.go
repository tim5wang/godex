package backend

import (
	"context"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/tools"
)

func (s *Service) ListSessionSkills(ctx context.Context, sessionID string) ([]skill.CatalogEntry, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.ListSkills()
}

// ListGlobalSkillCatalog returns installed/discoverable skill metadata
// independent of any session. Skills install globally, so the same catalog
// backs every session; this is used by session-creation UI to let the user
// pick which installed skills a new session should start with.
func (s *Service) ListGlobalSkillCatalog(ctx context.Context) ([]skill.CatalogEntry, error) {
	_ = ctx
	if s.cfg == nil {
		return nil, nil
	}
	loader := skill.NewLoader(s.cfg.SkillsDir)
	items, err := loader.Catalog(s.cfg.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// ListSessionSkillSources returns curated install sources for the session workspace.
func (s *Service) ListSessionSkillSources(ctx context.Context, sessionID string) ([]tools.SkillSourceEntry, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.ListSkillSources()
}

// SearchSessionSkillSources returns curated install sources plus search-backed marketplace results.
func (s *Service) SearchSessionSkillSources(ctx context.Context, sessionID, query string) ([]tools.SkillSourceEntry, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.SearchSkillSources(query)
}

// ListTrendingSessionSkillSources returns popular skills.sh entries for the session workspace.
func (s *Service) ListTrendingSessionSkillSources(ctx context.Context, sessionID string) ([]tools.SkillSourceEntry, error) {
	_ = ctx
	if _, err := s.requireSession(sessionID); err != nil {
		return nil, err
	}
	items, err := skill.TrendingSourceCatalog(s.cfg.WorkspaceDir, s.cfg.SkillsDir)
	if err != nil {
		return nil, err
	}
	result := make([]tools.SkillSourceEntry, 0, len(items))
	for _, item := range items {
		result = append(result, tools.SkillSourceEntry{
			ID:               item.ID,
			Name:             item.Name,
			Summary:          item.Summary,
			Source:           item.Source,
			SkillName:        item.SkillName,
			Tags:             append([]string{}, item.Tags...),
			Categories:       append([]string{}, item.Categories...),
			Version:          item.Version,
			Trust:            item.Trust,
			Origin:           item.Origin,
			Installs:         item.Installs,
			Warnings:         append([]string{}, item.Warnings...),
			InstallSupported: item.InstallSupported,
			InstallSource:    item.InstallSource,
			InstallName:      item.InstallName,
			InstallReason:    item.InstallReason,
			Installed:        item.Installed,
			InstalledPath:    item.InstalledPath,
			InstallMemory:    cloneInstallMemory(item.InstallMemory),
		})
	}
	return result, nil
}

// GetSessionSkill returns one discoverable skill's lightweight metadata.
func (s *Service) GetSessionSkill(ctx context.Context, sessionID, name string) (skill.CatalogEntry, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return skill.CatalogEntry{}, err
	}
	return session.agent.GetSkill(name)
}

// ActiveSessionSkills returns the currently active skills for a session.
func (s *Service) ActiveSessionSkills(ctx context.Context, sessionID string) ([]tools.SkillActivation, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.ActiveSkills()
}

// InstallSessionSkill installs a new skill source into the session workspace skills directory.
func (s *Service) InstallSessionSkill(ctx context.Context, sessionID, source, name string) (tools.SkillInstallResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.SkillInstallResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return tools.SkillInstallResult{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	result, err := session.agent.InstallSkillContext(ctx, source, name)
	updatedAt := s.now()
	release()
	released = true
	if err == nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			Type:      events.EventSkillStateChanged,
			Timestamp: updatedAt,
			Payload: events.SkillPayload{
				Action:             "installed",
				ID:                 result.ID,
				Name:               result.Name,
				Source:             result.Source,
				Sections:           append([]string{}, result.Sections...),
				RecommendedBundles: append([]string{}, result.RecommendedBundles...),
			},
		})
		s.emitSkillRefresh(session, updatedAt)
	}
	return result, err
}

// NormalizeSessionSkill explicitly runs LLM-backed normalization for one installed skill.
func (s *Service) NormalizeSessionSkill(ctx context.Context, sessionID, name string) (skill.CatalogEntry, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return skill.CatalogEntry{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return skill.CatalogEntry{}, err
	}
	defer release()

	item, err := session.agent.NormalizeSkill(ctx, name)
	if err != nil {
		return skill.CatalogEntry{}, err
	}
	now := s.now()
	session.events.Emit(events.Event{
		SessionID: session.id,
		Type:      events.EventSkillStateChanged,
		Timestamp: now,
		Payload: events.SkillPayload{
			Action:             "normalized",
			ID:                 item.ID,
			Name:               item.Name,
			Sections:           append([]string{}, item.Sections...),
			RecommendedBundles: append([]string{}, item.RecommendedBundles...),
		},
	})
	s.emitSkillRefresh(session, now)
	return item, nil
}

// RemoveSessionSkill deletes an installed skill source and persists the updated active stack.
func (s *Service) RemoveSessionSkill(ctx context.Context, sessionID, name string) (tools.SkillRemoveResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.SkillRemoveResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return tools.SkillRemoveResult{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	result, err := session.agent.RemoveSkill(name)
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	release()
	released = true
	if persistErr != nil && err == nil {
		err = persistErr
	}
	if err == nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			Type:      events.EventSkillStateChanged,
			Timestamp: updatedAt,
			Payload: events.SkillPayload{
				Action: "removed",
				ID:     result.ID,
				Name:   result.Name,
			},
		})
		s.emitSkillRefresh(session, updatedAt)
	}
	return result, err
}

// ActivateSessionSkill loads a skill core into the session and persists the updated state.
func (s *Service) ActivateSessionSkill(ctx context.Context, sessionID, name string) (tools.SkillActivation, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.SkillActivation{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return tools.SkillActivation{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	result, err := session.agent.ActivateSkill(name)
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	release()
	released = true
	if persistErr != nil && err == nil {
		err = persistErr
	}
	if err == nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			Type:      events.EventSkillStateChanged,
			Timestamp: updatedAt,
			Payload: events.SkillPayload{
				Action:             "activated",
				ID:                 result.ID,
				Name:               result.Name,
				Sections:           append([]string{}, result.LoadedSections...),
				RecommendedBundles: append([]string{}, result.RecommendedBundles...),
			},
		})
		s.emitSkillRefresh(session, updatedAt)
	}
	return result, err
}

// ExpandSessionSkill loads additional skill sections into the session and persists the updated state.
func (s *Service) ExpandSessionSkill(ctx context.Context, sessionID, name string, sections []string) (tools.SkillExpansion, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.SkillExpansion{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return tools.SkillExpansion{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	result, err := session.agent.ExpandSkill(name, sections)
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	release()
	released = true
	if persistErr != nil && err == nil {
		err = persistErr
	}
	if err == nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			Type:      events.EventSkillStateChanged,
			Timestamp: updatedAt,
			Payload: events.SkillPayload{
				Action:             "expanded",
				ID:                 result.ID,
				Name:               result.Name,
				Sections:           append([]string{}, result.ExpandedSections...),
				RecommendedBundles: append([]string{}, result.RecommendedBundles...),
			},
		})
		s.emitSkillRefresh(session, updatedAt)
	}
	return result, err
}

// UnloadSessionSkill removes an active skill from the session and persists the updated state.
func (s *Service) UnloadSessionSkill(ctx context.Context, sessionID, name string) (tools.SkillActivation, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.SkillActivation{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return tools.SkillActivation{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	result, err := session.agent.UnloadSkill(name)
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	release()
	released = true
	if persistErr != nil && err == nil {
		err = persistErr
	}
	if err == nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			Type:      events.EventSkillStateChanged,
			Timestamp: updatedAt,
			Payload: events.SkillPayload{
				Action:             "unloaded",
				ID:                 result.ID,
				Name:               result.Name,
				Sections:           append([]string{}, result.LoadedSections...),
				RecommendedBundles: append([]string{}, result.RecommendedBundles...),
			},
		})
		s.emitSkillRefresh(session, updatedAt)
	}
	return result, err
}

// wireSlashCommandHandlers installs session management slash-command handlers
// so the commands service can delegate /new and /resume to the backend.
