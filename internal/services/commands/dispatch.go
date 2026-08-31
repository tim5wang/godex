package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/agent"
)

// Execute validates and dispatches one normalized command.
func (s *Service) Execute(ctx context.Context, a *agent.Agent, cmd Command) (Result, error) {
	if a == nil {
		return Result{}, fmt.Errorf("missing agent")
	}
	if cmd.Name == "" {
		return Result{}, fmt.Errorf("%w: empty command", ErrUnknownCommand)
	}
	switch cmd.Name {
	case "bash", "sh":
		return s.executeLocalBash(ctx, cmd)
	case "compact":
		return s.executeCompact(a, cmd)
	case "tasks":
		if err := commandTakesNoArgs(cmd); err != nil {
			return Result{}, err
		}
		return Result{Name: cmd.Name, Output: fmt.Sprint(a.TaskMgr().List())}, nil
	case "team":
		if err := commandTakesNoArgs(cmd); err != nil {
			return Result{}, err
		}
		return Result{Name: cmd.Name, Output: renderTeam(a.TeamMgr().List())}, nil
	case "inbox":
		if err := commandTakesNoArgs(cmd); err != nil {
			return Result{}, err
		}
		return Result{Name: cmd.Name, Output: fmt.Sprint(a.MsgBus().ReadInbox(s.cfg.LeadName))}, nil
	case "todos":
		return s.executeTodos(a, cmd)
	case "insights":
		if err := commandTakesNoArgs(cmd); err != nil {
			return Result{}, err
		}
		return s.executeInsights(a)
	case "doctor":
		if err := commandTakesNoArgs(cmd); err != nil {
			return Result{}, err
		}
		return s.executeDoctor()
	case "channels":
		if err := commandTakesNoArgs(cmd); err != nil {
			return Result{}, err
		}
		return s.executeChannels()
	default:
		return s.executeExtended(ctx, a, cmd)
	}
}

func (s *Service) executeExtended(ctx context.Context, a *agent.Agent, cmd Command) (Result, error) {
	switch cmd.Name {
	case "skills":
		return s.executeSkills(a, cmd)
	case "packages":
		return s.executePackages(cmd)
	case "memory":
		return s.executeMemory(a, cmd)
	case "note":
		return s.executeNote(ctx, cmd)
	case "memory-digest":
		if err := commandTakesNoArgs(cmd); err != nil {
			return Result{}, err
		}
		return s.executeMemoryDigest(a)
	case "memory-log":
		return s.executeMemoryLog(a, cmd)
	case "memory-restore":
		return s.executeMemoryRestore(a, cmd)
	case "model":
		return s.executeModel(a, ctx, cmd)
	case "clear":
		return s.executeClear(a, ctx, cmd)
	case "approve":
		return s.executeApprove(a, ctx, cmd)
	case "deny":
		return s.executeDeny(a, ctx, cmd)
	case "session":
		return s.executeSession(a, ctx, cmd)
	case "new":
		return s.executeNewSession(a, ctx, cmd)
	case "resume":
		return s.executeResumeSession(a, ctx, cmd)
	case "history":
		return s.executeHistory(a, ctx, cmd)
	case "cron":
		return s.executeCron(ctx, cmd)
	case "heartbeat":
		return s.executeHeartbeat(ctx, cmd)
	case "help":
		if err := commandTakesNoArgs(cmd); err != nil {
			return Result{}, err
		}
		return Result{Name: cmd.Name, Output: s.HelpText()}, nil
	default:
		if result, ok, err := s.executePackageCommand(cmd); ok || err != nil {
			return result, err
		}
		return Result{}, fmt.Errorf("%w: /%s", ErrUnknownCommand, cmd.Name)
	}
}

func (s *Service) executeCompact(a *agent.Agent, cmd Command) (Result, error) {
	mode := a.DefaultCompactionMode()
	for _, arg := range cmd.Args {
		switch strings.TrimSpace(arg) {
		case "--model", "--deep":
			mode = "model"
		case "--hybrid":
			mode = "hybrid"
		default:
			return Result{}, fmt.Errorf("usage: /compact [--model|--deep|--hybrid]")
		}
	}
	output, err := a.CompactConversationWithMode(mode)
	return Result{Name: cmd.Name, Output: output, RefreshSnapshot: true}, err
}

func commandTakesNoArgs(cmd Command) error {
	if len(cmd.Args) > 0 {
		return fmt.Errorf("command /%s does not accept arguments", cmd.Name)
	}
	return nil
}
