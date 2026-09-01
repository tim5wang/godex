package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/tools"
)

func (s *Service) executeModel(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"list"}
	}
	action := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
	switch action {
	case "help", "-h", "--help":
		if len(cmd.Args) != 1 {
			return Result{}, fmt.Errorf("usage: /model help")
		}
		return Result{Name: "model", Output: modelHelpText()}, nil
	case "get":
		if len(cmd.Args) != 1 {
			return Result{}, fmt.Errorf("usage: /model get")
		}
		return Result{Name: "model", Output: renderModelState(a.CurrentModel())}, nil
	case "list", "use", "session", "set", "default":
		if action == "list" && len(cmd.Args) != 1 {
			return Result{}, fmt.Errorf("usage: /model list")
		}
		if (action == "use" || action == "session") && len(cmd.Args) != 2 {
			return Result{}, fmt.Errorf("usage: /model use <profile-id>")
		}
		if (action == "set" || action == "default") && len(cmd.Args) != 2 {
			return Result{}, fmt.Errorf("usage: /model default <profile-or-model>")
		}
		s.mu.RLock()
		handler := s.model
		s.mu.RUnlock()
		if handler == nil {
			return Result{Name: "model", Output: "Model runtime is unavailable in this process."}, nil
		}
		return handler(ctx, cmd)
	default:
		if len(cmd.Args) == 1 {
			s.mu.RLock()
			handler := s.model
			s.mu.RUnlock()
			if handler == nil {
				return Result{Name: "model", Output: "Model runtime is unavailable in this process."}, nil
			}
			return handler(ctx, Command{Name: cmd.Name, Args: []string{"use", cmd.Args[0]}, Raw: cmd.Raw})
		}
		return Result{}, fmt.Errorf("unknown /model action %q", cmd.Args[0])
	}
}

func modelHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  /model list",
		"  /model use <profile-id>",
		"  /model <profile-id>",
		"  /model default <profile-or-model>",
		"  /model get",
		"",
		"Use `/model use` to switch only the current conversation.",
		"Use `/model default` to update the workspace default for new sessions.",
	}, "\n")
}

func (s *Service) executeClear(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.clear
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "clear", Output: "Clear runtime is unavailable in this process."}, nil
	}
	return handler(ctx, a, cmd)
}

func (s *Service) executeApprove(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.approve
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "approve", Output: "Permission approval runtime is unavailable in this process."}, nil
	}
	return handler(ctx, a, cmd)
}

func (s *Service) executeDeny(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.deny
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "deny", Output: "Permission approval runtime is unavailable in this process."}, nil
	}
	return handler(ctx, a, cmd)
}

func (s *Service) executeSession(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.session
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "session", Output: "Session runtime is unavailable in this process."}, nil
	}
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"current"}
	}
	return handler(ctx, a, cmd)
}

func (s *Service) executeNewSession(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.newSession
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "new", Output: "New session runtime is unavailable in this process."}, nil
	}
	if len(cmd.Args) > 0 {
		return Result{}, fmt.Errorf("command /%s does not accept arguments", cmd.Name)
	}
	return handler(ctx, a, cmd)
}

func (s *Service) executeResumeSession(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.resumeSession
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "resume", Output: "Resume session runtime is unavailable in this process."}, nil
	}
	return handler(ctx, a, cmd)
}

func (s *Service) executeHistory(a *agent.Agent, ctx context.Context, cmd Command) (Result, error) {
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"show"}
	}
	action := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
	switch action {
	case "show":
		if len(cmd.Args) != 1 {
			return Result{}, fmt.Errorf("usage: /history show")
		}
		return Result{Name: "history", Output: renderHistory(a.GetMessages(), 0)}, nil
	case "tail":
		limit := 10
		if len(cmd.Args) > 2 {
			return Result{}, fmt.Errorf("usage: /history tail [count]")
		}
		if len(cmd.Args) == 2 {
			parsed, err := strconv.Atoi(strings.TrimSpace(cmd.Args[1]))
			if err != nil || parsed <= 0 {
				return Result{}, fmt.Errorf("usage: /history tail [count]")
			}
			limit = parsed
		}
		return Result{Name: "history", Output: renderHistory(a.GetMessages(), limit)}, nil
	case "search":
		req, err := parseHistorySearchArgs(cmd.Args[1:])
		if err != nil {
			return Result{}, err
		}
		runtime := a.HistorySearchRuntime()
		if runtime == nil {
			return Result{Name: "history", Output: "History search is unavailable in this process."}, nil
		}
		sessionID, runtimeCtx := historySearchSessionContext(ctx)
		result, err := runtime.SearchHistory(ctx, sessionID, runtimeCtx, req)
		if err != nil {
			return Result{}, err
		}
		return Result{Name: "history", Output: renderHistorySearch(result)}, nil
	default:
		return Result{}, fmt.Errorf("unknown /history action %q", cmd.Args[0])
	}
}

func (s *Service) executeCron(ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.cron
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "cron", Output: "Cron runtime is unavailable in this process."}, nil
	}
	return handler(ctx, cmd)
}

func (s *Service) executeHeartbeat(ctx context.Context, cmd Command) (Result, error) {
	s.mu.RLock()
	handler := s.heartbeat
	s.mu.RUnlock()
	if handler == nil {
		return Result{Name: "heartbeat", Output: "Heartbeat runtime is unavailable in this process."}, nil
	}
	return handler(ctx, cmd)
}

func (s *Service) executeTodos(a *agent.Agent, cmd Command) (Result, error) {
	if len(cmd.Args) == 0 {
		cmd.Args = []string{"list"}
	}
	switch strings.ToLower(strings.TrimSpace(cmd.Args[0])) {
	case "list":
		return Result{Name: cmd.Name, Output: a.TodoMgr().Render()}, nil
	case "clear":
		// Reset persists the empty list to disk and clears
		// the in-memory state.  If persistence fails we
		// surface the error instead of reporting success —
		// otherwise the next session's todos would silently
		// reappear from the stale on-disk file, which is
		// exactly the cross-session pollution bug we are
		// guarding against.
		if err := a.TodoMgr().Reset(); err != nil {
			return Result{Name: cmd.Name, Output: "Failed to clear todos: " + err.Error()}, err
		}
		return Result{Name: cmd.Name, Output: "Cleared todo list.", RefreshSnapshot: true}, nil
	default:
		return Result{}, fmt.Errorf("unknown /todos subcommand %q (usage: /todos list|clear)", cmd.Args[0])
	}
}

func (s *Service) executeInsights(a *agent.Agent) (Result, error) {
	s.mu.RLock()
	analyze := s.analyze
	cfg := s.cfg
	s.mu.RUnlock()

	report, err := analyze(buildInsightsInput(collectInsightsSnapshot(a)))
	if err != nil {
		return Result{}, err
	}

	markdown := report.Markdown()
	reportPath := filepath.Join(cfg.TempDir, "insights-latest.md")
	if err := os.WriteFile(reportPath, []byte(markdown), 0644); err != nil {
		return Result{
			Name:   "insights",
			Output: markdown,
		}, fmt.Errorf("write insights report: %w", err)
	}

	output := markdown + "\nSaved insights report to " + reportPath
	return Result{
		Name:         "insights",
		Output:       output,
		ArtifactPath: reportPath,
	}, nil
}

func (s *Service) executeDoctor() (Result, error) {
	s.mu.RLock()
	doctor := s.doctor
	s.mu.RUnlock()
	if doctor == nil {
		return Result{Name: "doctor", Output: "Configuration doctor is unavailable in this runtime."}, nil
	}
	report := doctor()
	return Result{Name: "doctor", Output: report.Text()}, nil
}

func renderModelState(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "(unset)"
	}
	return "Current model: " + model
}

func renderHistory(messages []protocol.Message, tail int) string {
	if len(messages) == 0 {
		return "No conversation history yet."
	}
	start := 0
	if tail > 0 && len(messages) > tail {
		start = len(messages) - tail
	}
	lines := []string{"Conversation history:"}
	for idx := start; idx < len(messages); idx++ {
		msg := messages[idx]
		label := fmt.Sprintf("%d. %s", idx+1, msg.Role)
		if msg.Metadata != nil && msg.Metadata.Kind != "" {
			label += " [" + string(msg.Metadata.Kind) + "]"
		}
		lines = append(lines, label)
		text := strings.TrimSpace(protocol.MessageText(msg))
		if text != "" {
			lines = append(lines, "   "+text)
		}
		if msg.Metadata != nil && len(msg.Metadata.Attachments) > 0 {
			names := make([]string, 0, len(msg.Metadata.Attachments))
			for _, attachment := range msg.Metadata.Attachments {
				name := strings.TrimSpace(attachment.Name)
				if name == "" {
					name = attachment.ID
				}
				names = append(names, name)
			}
			lines = append(lines, "   attachments: "+strings.Join(names, ", "))
		}
		if text == "" && (msg.Metadata == nil || len(msg.Metadata.Attachments) == 0) {
			lines = append(lines, "   (no text)")
		}
	}
	return strings.Join(lines, "\n")
}

func parseHistorySearchArgs(args []string) (tools.HistorySearchRequest, error) {
	usageErr := fmt.Errorf("usage: /history search <query> [scope=current_session|session_archive|all_archives] [limit=N] [role=user|assistant|any]")
	if len(args) == 0 {
		return tools.HistorySearchRequest{}, usageErr
	}

	req := tools.HistorySearchRequest{}
	queryParts := make([]string, 0, len(args))
	for _, raw := range args {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			queryParts = append(queryParts, raw)
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if value == "" {
			return tools.HistorySearchRequest{}, usageErr
		}
		switch key {
		case "scope":
			switch value {
			case tools.HistorySearchScopeCurrentSession, tools.HistorySearchScopeSessionArchive, tools.HistorySearchScopeAllArchives:
				req.Scope = value
			default:
				return tools.HistorySearchRequest{}, usageErr
			}
		case "limit":
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return tools.HistorySearchRequest{}, usageErr
			}
			req.Limit = parsed
		case "role":
			switch value {
			case "user", "assistant", "any":
				req.Role = value
			default:
				return tools.HistorySearchRequest{}, usageErr
			}
		default:
			return tools.HistorySearchRequest{}, usageErr
		}
	}

	req.Query = strings.TrimSpace(strings.Join(queryParts, " "))
	if req.Query == "" {
		return tools.HistorySearchRequest{}, usageErr
	}
	return req, nil
}

func historySearchSessionContext(ctx context.Context) (string, automation.SessionContext) {
	current, ok := CurrentSessionContext(ctx)
	if !ok {
		return "", automation.SessionContext{}
	}
	return strings.TrimSpace(current.SessionID), automation.SessionContext{
		SessionID:      current.SessionID,
		LocatorChannel: current.Channel,
		LocatorKey:     current.Key,
		LocatorUserID:  current.UserID,
		Metadata:       cloneStringMap(current.Metadata),
	}
}

func renderHistorySearch(result tools.HistorySearchResult) string {
	lines := []string{
		"History search:",
		"Scope: " + strings.TrimSpace(result.Scope),
		fmt.Sprintf("Matches: %d", result.MatchCount),
	}
	if len(result.Snippets) == 0 {
		lines = append(lines, "No matching history snippets found.")
		return strings.Join(lines, "\n")
	}
	for idx, item := range result.Snippets {
		header := fmt.Sprintf("%d. %s", idx+1, item.Role)
		if !item.Timestamp.IsZero() {
			header += " @ " + item.Timestamp.UTC().Format("2006-01-02 15:04Z")
		}
		lines = append(lines, header)
		meta := make([]string, 0, 3)
		if item.SourceKind != "" {
			meta = append(meta, "source="+item.SourceKind)
		}
		if item.SessionID != "" {
			meta = append(meta, "session="+item.SessionID)
		}
		if item.SessionTitle != "" {
			meta = append(meta, "title="+item.SessionTitle)
		}
		if len(meta) > 0 {
			lines = append(lines, "   "+strings.Join(meta, " | "))
		}
		lines = append(lines, "   "+item.TextExcerpt)
	}
	return strings.Join(lines, "\n")
}
