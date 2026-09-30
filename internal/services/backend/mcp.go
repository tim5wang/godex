package backend

import (
	"context"

	"github.com/tim5wang/godex/internal/core/mcp"
)

func (s *Service) ListSessionMCPServers(ctx context.Context, sessionID string) ([]mcp.ServerConfig, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.ListMCPServers()
}
