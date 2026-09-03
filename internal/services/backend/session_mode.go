package backend

import (
	"context"
	"strings"

	"github.com/tim5wang/godex/internal/domain/events"
)

// SessionMode returns the session creation mode currently pinned on the
// session ("" or "default" = standard mode, "minimal" = lean mode). It reads
// the locator metadata written at creation/load time.
func (s *Service) SessionMode(ctx context.Context, sessionID string) (string, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return "", err
	}
	session.mu.RLock()
	mode := strings.TrimSpace(session.locator.Metadata["mode"])
	session.mu.RUnlock()
	return mode, nil
}

// SetSessionMode switches an open session's creation mode at runtime: it
// updates the persisted locator metadata ("mode"), applies the mode's tool
// preset to the live agent, and persists the session so a reload restores the
// same mode. The session must already be open (OpenSession first).
func (s *Service) SetSessionMode(ctx context.Context, sessionID, mode string) error {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}
	mode = strings.TrimSpace(mode)
	if mode == "default" {
		mode = ""
	}
	session.mu.Lock()
	if session.locator.Metadata == nil {
		session.locator.Metadata = map[string]string{}
	}
	if mode == "" {
		delete(session.locator.Metadata, "mode")
	} else {
		session.locator.Metadata["mode"] = mode
	}
	agentRef := session.agent
	session.mu.Unlock()
	if agentRef != nil {
		agentRef.ApplySessionMode(mode)
	}
	now := s.now()
	if err := s.persistSession(session, now); err != nil {
		return err
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
	return nil
}
