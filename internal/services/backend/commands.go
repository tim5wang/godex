package backend

import (
	"context"
	"fmt"
	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/commands"
	"strings"
)

func (s *Service) ExecuteCommand(ctx context.Context, sessionID string, cmd commands.Command) (commands.Result, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return commands.Result{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return commands.Result{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	now := s.now()
	turnID := session.nextTurnID(now)
	ctx = withSessionLock(ctx, sessionID)
	ctx = commands.WithSessionContext(ctx, commands.SessionContext{
		SessionID: sessionID,
		Channel:   session.locator.Channel,
		Key:       session.locator.Key,
		UserID:    session.locator.UserID,
		Metadata:  mergeCommandContextMetadata(session.locator.Metadata, cmd.Metadata),
	})
	if cmd.Name == "ledger" {
		if len(cmd.Args) > 0 {
			return commands.Result{}, fmt.Errorf("command /ledger does not accept arguments")
		}
		ledger, err := s.ProjectLedger(sessionID)
		if err != nil {
			return commands.Result{}, err
		}
		output := ledger.Compact
		if strings.TrimSpace(output) == "" {
			output = "Project ledger is empty."
		}
		return commands.Result{Name: "ledger", Output: output}, nil
	}
	result, execErr := s.commands.Execute(ctx, session.agent, cmd)
	if result.Name == "" {
		result.Name = cmd.Name
	}
	if execErr == nil && strings.EqualFold(cmd.Name, "clear") {
		session.clearQueue()
		if err := s.writeSessionQueue(session); err != nil {
			execErr = err
		}
	}
	if execErr == nil && result.Dispatch != nil && result.Dispatch.Mode == "subagent_job" {
		jobID, err := s.dispatchPackageSubagent(ctx, session, turnID, result.Dispatch)
		if err != nil {
			execErr = err
			result.DispatchStatus = "failed"
			result.DispatchError = err.Error()
			result.Diagnostics = append(result.Diagnostics, err.Error())
		} else {
			result.DispatchedJobID = jobID
			result.DispatchStatus = "dispatched"
			result.RefreshSnapshot = true
			if strings.TrimSpace(result.Output) != "" {
				result.Output += "\n"
			}
			result.Output += "Started durable subagent job " + jobID + "."
		}
	}
	commandAttachments, artifactWarnings := s.materializeArtifactPaths(sessionID, []string{strings.TrimSpace(result.ArtifactPath)})
	if len(commandAttachments) > 0 {
		session.agent.AppendAssistantDelivery("", "", commandAttachments)
	}
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	release()
	released = true

	if persistErr != nil {
		if execErr == nil {
			execErr = persistErr
		} else {
			session.events.Emit(events.Event{
				SessionID: sessionID,
				TurnID:    turnID,
				Type:      events.EventWarningRaised,
				Timestamp: updatedAt,
				Payload: events.NoticePayload{
					Message: fmt.Sprintf("failed to persist session state: %v", persistErr),
				},
			})
		}
	}
	if execErr == nil && result.Dispatch != nil && result.Dispatch.Mode == "agent_turn" {
		dispatchedTurnID, err := s.dispatchPackageAgentTurn(context.Background(), sessionID, result.Dispatch)
		if err != nil {
			execErr = err
			result.DispatchStatus = "failed"
			result.DispatchError = err.Error()
			result.Diagnostics = append(result.Diagnostics, err.Error())
		} else {
			result.DispatchedTurnID = dispatchedTurnID
			result.DispatchStatus = "dispatched"
			result.RefreshSnapshot = true
			if strings.TrimSpace(result.Output) != "" {
				result.Output += "\n"
			}
			result.Output += "Queued agent turn " + dispatchedTurnID + "."
		}
	}
	if execErr != nil {
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventErrorRaised,
			Timestamp: updatedAt,
			Payload:   events.NoticePayload{Message: execErr.Error()},
		})
	}
	for _, warning := range artifactWarnings {
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: updatedAt,
			Payload:   events.NoticePayload{Message: warning},
		})
	}
	if result.RefreshSnapshot {
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventSnapshotReady,
			Timestamp: updatedAt,
			Payload: events.SnapshotPayload{
				UpdatedAt: updatedAt,
				Running:   false,
			},
		})
	}
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventCommandCompleted,
		Timestamp: updatedAt,
		Payload: events.CommandPayload{
			Name:               result.Name,
			Output:             result.Output,
			ArtifactPath:       result.ArtifactPath,
			RefreshSnapshot:    result.RefreshSnapshot,
			DispatchMode:       commandDispatchMode(result.Dispatch),
			DispatchInvocation: commandDispatchInvocation(result.Dispatch),
			DispatchStatus:     result.DispatchStatus,
			DispatchError:      result.DispatchError,
			DispatchedTurnID:   result.DispatchedTurnID,
			DispatchedJobID:    result.DispatchedJobID,
			Error:              errorString(execErr),
		},
	})
	_ = s.writeSessionTimeline(session)

	return result, execErr
}

func commandDispatchMode(dispatch *commands.PackageCommandDispatch) string {
	if dispatch == nil {
		return ""
	}
	return strings.TrimSpace(dispatch.Mode)
}

func commandDispatchInvocation(dispatch *commands.PackageCommandDispatch) string {
	if dispatch == nil {
		return ""
	}
	return strings.TrimSpace(dispatch.Invocation)
}

func (s *Service) dispatchPackageSubagent(ctx context.Context, session *sessionState, turnID string, dispatch *commands.PackageCommandDispatch) (string, error) {
	if session == nil || dispatch == nil {
		return "", fmt.Errorf("missing package command dispatch")
	}
	prompt := strings.TrimSpace(dispatch.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("package command dispatch missing prompt")
	}
	agentType := strings.TrimSpace(dispatch.AgentType)
	if agentType == "" && len(dispatch.Roles) > 0 {
		agentType = strings.TrimSpace(dispatch.Roles[0])
	}
	if agentType == "" {
		agentType = "Explore"
	}
	dispatchCtx := agent.WithSubagentEvents(ctx, session.id, turnID, session.events)
	job, err := session.agent.StartDurableSubagentWithContext(dispatchCtx, prompt, agentType, dispatch.WriteScope)
	if err != nil {
		return "", err
	}
	return job.IDString(), nil
}

func (s *Service) dispatchPackageAgentTurn(ctx context.Context, sessionID string, dispatch *commands.PackageCommandDispatch) (string, error) {
	if dispatch == nil {
		return "", fmt.Errorf("missing package command dispatch")
	}
	prompt := strings.TrimSpace(dispatch.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("package command dispatch missing prompt")
	}
	metadata := map[string]string{
		"package": dispatch.PackageName,
		"command": dispatch.CommandName,
		"mode":    dispatch.Mode,
	}
	if dispatch.Namespace != "" {
		metadata["namespace"] = dispatch.Namespace
	}
	if dispatch.Invocation != "" {
		metadata["invocation"] = dispatch.Invocation
	}
	envelope := message.NewRuntimeEnvelope(message.SourceCommand, sessionID, "package-command", prompt, s.now(), metadata)
	result, err := s.SubmitAsync(ctx, sessionID, envelope, SubmitOptions{QueueMode: QueueModeFollowUp})
	if err != nil {
		return "", err
	}
	return result.TurnID, nil
}

func (s *Service) wireSlashCommandHandlers() {
	if s.commands == nil {
		return
	}
	s.commands.SetAgentTemplate(func(ctx context.Context, a *agent.Agent, cmd commands.Command) (commands.Result, error) {
		args := append([]string(nil), cmd.Args...)
		if len(args) == 0 || (len(args) == 1 && strings.EqualFold(strings.TrimSpace(args[0]), "list")) {
			items, err := s.ListAgentTemplates()
			if err != nil {
				return commands.Result{}, err
			}
			currentID := strings.TrimSpace(a.TemplateID())
			if currentID == "" {
				currentID = "default"
			}
			lines := []string{"Agent templates:"}
			for _, item := range items {
				marker := " "
				if item.ID == currentID {
					marker = "*"
				}
				line := fmt.Sprintf("%s %s", marker, item.ID)
				if name := strings.TrimSpace(item.Name); name != "" && name != item.ID {
					line += " — " + name
				}
				lines = append(lines, line)
			}
			lines = append(lines, "", "Switch with: /agent <template-id>")
			return commands.Result{Name: "agent", Output: strings.Join(lines, "\n")}, nil
		}

		templateID := ""
		switch {
		case len(args) == 1:
			templateID = strings.TrimSpace(args[0])
		case len(args) == 2 && strings.EqualFold(strings.TrimSpace(args[0]), "use"):
			templateID = strings.TrimSpace(args[1])
		default:
			return commands.Result{}, fmt.Errorf("usage: /agent [list|use <template-id>|<template-id>]")
		}
		if templateID == "" {
			return commands.Result{}, fmt.Errorf("usage: /agent [list|use <template-id>|<template-id>]")
		}
		sessionCtx, ok := commands.CurrentSessionContext(ctx)
		if !ok || strings.TrimSpace(sessionCtx.SessionID) == "" {
			return commands.Result{}, fmt.Errorf("current session context is unavailable")
		}
		tmpl, warnings, err := s.ValidateAgentTemplate(templateID)
		if err != nil {
			return commands.Result{}, err
		}
		oldEngine := a.TemplateEngine()
		if err := s.ApplyTemplateToSession(sessionCtx.SessionID, tmpl.ID); err != nil {
			return commands.Result{}, err
		}
		lines := []string{fmt.Sprintf("Agent template switched to %s (%s).", tmpl.Name, tmpl.ID)}
		if newEngine := a.TemplateEngine(); newEngine != oldEngine {
			lines = append(lines, fmt.Sprintf("Harness changed from %s to %s; the new harness starts its own external context on the next turn.", oldEngine, newEngine))
		}
		for _, warning := range warnings {
			lines = append(lines, "Warning: "+warning)
		}
		return commands.Result{Name: "agent", Output: strings.Join(lines, "\n"), RefreshSnapshot: true}, nil
	})
	s.commands.SetNewSession(func(ctx context.Context, a *agent.Agent, cmd commands.Command) (commands.Result, error) {
		locator, err := s.CreateNewSession(ctx)
		if err != nil {
			return commands.Result{}, err
		}

		projectDir := ""
		if s.cfg != nil {
			projectDir = strings.TrimSpace(s.cfg.WorkspaceDir)
			if projectDir == "" {
				projectDir = strings.TrimSpace(s.cfg.ProjectDir)
			}
		}

		output := fmt.Sprintf("✓ New session created.\n\nSession: %s:%s", locator.Channel, locator.Key)
		if projectDir != "" {
			output += fmt.Sprintf("\nWorkspace: %s", projectDir)
		}
		output += "\n\nSwitched to the new session. Next time you run godex in this directory, it will open this session."

		return commands.Result{
			Name:   "new",
			Output: output,
		}, nil
	})

	s.commands.SetResumeSession(func(ctx context.Context, a *agent.Agent, cmd commands.Command) (commands.Result, error) {
		allSessions, err := s.ListSessions(ctx, SessionListFilter{})
		if err != nil {
			return commands.Result{}, err
		}

		// If args are provided, filter by session ID or name/key
		query := strings.TrimSpace(strings.Join(cmd.Args, " "))
		if query != "" {
			var matched []ListedSession
			queryLower := strings.ToLower(query)
			for _, session := range allSessions {
				if strings.HasPrefix(strings.ToLower(session.SessionID), queryLower) ||
					strings.EqualFold(session.Locator.Key, query) ||
					strings.Contains(strings.ToLower(session.Title), queryLower) {
					matched = append(matched, session)
				}
			}
			if len(matched) == 0 {
				return commands.Result{Name: "resume", Output: fmt.Sprintf("No session found matching %q.", query)}, nil
			}
			var lines []string
			lines = append(lines, fmt.Sprintf("Sessions matching %q:", query))
			for _, session := range matched {
				lines = append(lines, formatSessionLine(session))
			}
			lines = append(lines, "", "To resume a session, restart godex with: godex tui --session <channel:key>")
			return commands.Result{
				Name:   "resume",
				Output: strings.Join(lines, "\n"),
			}, nil
		}

		currentProjectDir := ""
		if s.cfg != nil {
			currentProjectDir = strings.TrimSpace(s.cfg.WorkspaceDir)
			if currentProjectDir == "" {
				currentProjectDir = strings.TrimSpace(s.cfg.ProjectDir)
			}
		}
		currentProjectDir = cleanProjectDir(currentProjectDir)

		var current, others []ListedSession
		for _, session := range allSessions {
			sessionProjectDir := ""
			if session.Locator.Metadata != nil {
				sessionProjectDir = cleanProjectDir(session.Locator.Metadata[sessionProjectDirMetadataKey])
			}
			if currentProjectDir != "" && sessionProjectDir == currentProjectDir {
				current = append(current, session)
			} else {
				others = append(others, session)
			}
		}

		var lines []string
		if len(current) > 0 {
			lines = append(lines, fmt.Sprintf("Sessions for %s:", currentProjectDir))
			for _, session := range current {
				lines = append(lines, formatSessionLine(session))
			}
		}
		if len(others) > 0 {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, "Other sessions:")
			for _, session := range others {
				lines = append(lines, formatSessionLine(session))
			}
		}
		if len(lines) == 0 {
			return commands.Result{Name: "resume", Output: "No saved sessions found."}, nil
		}
		lines = append(lines, "", "To resume a session, restart godex with: godex tui --session <channel:key>")

		return commands.Result{
			Name:   "resume",
			Output: strings.Join(lines, "\n"),
		}, nil
	})
}

// formatSessionLine renders one listed session as: name · date · ID · working-dir.
func formatSessionLine(session ListedSession) string {
	name := strings.TrimSpace(session.Title)
	if name == "" || name == "-" {
		name = fmt.Sprintf("%s:%s", session.Locator.Channel, session.Locator.Key)
	}
	line := fmt.Sprintf("- %s", name)

	if !session.LastActivityAt.IsZero() {
		line += fmt.Sprintf(" · %s", session.LastActivityAt.Format("2006-01-02 15:04"))
	}

	line += fmt.Sprintf(" · %s:%s", session.Locator.Channel, session.Locator.Key)

	if session.Locator.Metadata != nil {
		if projectDir := strings.TrimSpace(session.Locator.Metadata[sessionProjectDirMetadataKey]); projectDir != "" {
			line += fmt.Sprintf(" · %s", truncatePathTail(projectDir, 40))
		}
	}

	if session.Running {
		line += " [running]"
	}
	return line
}

// truncatePathTail keeps only the last maxLen characters of a path, adding
// "..." prefix when truncation occurs. Useful for dense directory display.
func truncatePathTail(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	return "..." + path[len(path)-maxLen:]
}

// ListSessions returns persisted sessions ordered by most recent update first.
