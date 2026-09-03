package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/tools"
)

// GetACPAgent returns one configured ACP agent by id.
func (s *Service) GetACPAgent(id string) (config.ACPAgentConfig, error) {
	if s.cfg == nil || s.cfg.ACP.Agents == nil {
		return config.ACPAgentConfig{}, fmt.Errorf("unknown ACP agent %q", id)
	}
	agent, ok := s.cfg.ACP.Agents[id]
	if !ok {
		return config.ACPAgentConfig{}, fmt.Errorf("unknown ACP agent %q", id)
	}
	return agent, nil
}

// DiscoverACPAgentModels connects to a configured ACP agent and returns its
// advertised model options (from the agent's session configOptions). It is
// used by the settings ACP agent editor's model dropdown.
func (s *Service) DiscoverACPAgentModels(ctx context.Context, agent config.ACPAgentConfig) ([]tools.ACPModelOption, error) {
	workspace := strings.TrimSpace(s.cfg.WorkspaceDir)
	if workspace == "" {
		workspace = "."
	}
	return tools.DiscoverACPAgentModelOptions(ctx, agent, workspace)
}
