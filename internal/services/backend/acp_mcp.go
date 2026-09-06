package backend

import (
	"context"
	"fmt"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/tim5wang/godex/internal/core/mcp"
	"github.com/tim5wang/godex/internal/domain/events"
)

// BridgeACPMCPServers registers ACP client-proposed stdio MCP servers only in
// this process and exposes their tools to the target session. Failures are
// reported as warning events and do not abort the ACP prompt.
func (s *Service) BridgeACPMCPServers(ctx context.Context, sessionID string, servers []acp.McpServer) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return
	}
	mgr := s.MCPManager()
	if mgr == nil {
		s.emitACPMCPWarning(session, "ACP MCP bridge unavailable: MCP manager is not configured")
		return
	}
	for index, proposed := range servers {
		if proposed.Stdio == nil {
			s.emitACPMCPWarning(session, "ACP MCP bridge ignored non-stdio server")
			continue
		}
		stdio := proposed.Stdio
		name := strings.TrimSpace(stdio.Name)
		if name == "" {
			name = fmt.Sprintf("server-%d", index+1)
		}
		serverName := fmt.Sprintf("acp-mcp:%s:%s", sessionID, name)
		env := make(map[string]string, len(stdio.Env))
		for _, item := range stdio.Env {
			env[item.Name] = item.Value
		}
		cfg := mcp.ServerConfig{
			Name:    serverName,
			Type:    mcp.ServerTypeStdio,
			Command: strings.TrimSpace(stdio.Command),
			Args:    append([]string(nil), stdio.Args...),
			Env:     env,
		}
		owner := serverName
		if err := mgr.UpsertTransientServer(cfg); err != nil {
			s.emitACPMCPWarning(session, fmt.Sprintf("ACP MCP bridge failed for %s: %v", name, err))
			continue
		}
		if err := session.agent.RegisterTransientMCPServerTools(ctx, owner, serverName); err != nil {
			mgr.DeleteTransientServer(serverName)
			s.emitACPMCPWarning(session, fmt.Sprintf("ACP MCP bridge failed for %s: %v", name, err))
		}
	}
}

// CloseACPMCPBridge releases the transient MCP processes and tools owned by
// one ACP client session without deleting the durable GoDex conversation.
func (s *Service) CloseACPMCPBridge(_ context.Context, sessionID string) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return
	}
	mgr := s.MCPManager()
	if mgr == nil {
		return
	}
	for _, owner := range mgr.DeleteTransientServers("acp-mcp:" + sessionID + ":") {
		session.agent.UnregisterTransientMCPServerTools(owner)
	}
}

func (s *Service) emitACPMCPWarning(session *sessionState, message string) {
	if session == nil || session.events == nil {
		return
	}
	session.events.Emit(events.Event{
		SessionID: session.id,
		Type:      events.EventWarningRaised,
		Timestamp: time.Now(),
		Payload:   map[string]any{"message": message, "source": "acp_mcp_bridge"},
	})
}
