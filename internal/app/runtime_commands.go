package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/sessionadmin"
	"github.com/tim5wang/godex/internal/tools"
)

type modelCommandRuntime interface {
	Current() *config.Config
	Update(context.Context, config.UpdateRequest) (config.View, error)
}

type modelSessionRuntime interface {
	Models(context.Context, string) (backend.ModelsView, error)
	SetSessionModelProfile(context.Context, string, string) (backend.ModelsView, error)
}

type sessionCommandRuntime interface {
	ListSessions(context.Context, backend.SessionListFilter) ([]backend.ListedSession, error)
	OpenSession(context.Context, backend.SessionLocator) (*backend.OpenedSession, error)
}

// NewModelCommandHandler adapts live config management into the slash-command surface.
func NewModelCommandHandler(runtime modelCommandRuntime) func(context.Context, commands.Command) (commands.Result, error) {
	return NewModelCommandHandlerWithSessions(runtime, nil)
}

// NewModelCommandHandlerWithSessions adapts model configuration and
// session-scoped model profile switching into the slash-command surface.
func NewModelCommandHandlerWithSessions(runtime modelCommandRuntime, sessions modelSessionRuntime) func(context.Context, commands.Command) (commands.Result, error) {
	return func(ctx context.Context, cmd commands.Command) (commands.Result, error) {
		if runtime == nil {
			return commands.Result{Name: "model", Output: "Model runtime is unavailable in this process."}, nil
		}
		if len(cmd.Args) == 0 {
			return commands.Result{Name: "model", Output: renderModelProfilesForCommand(ctx, runtime.Current(), sessions)}, nil
		}
		action := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
		switch action {
		case "get", "list":
			if len(cmd.Args) != 1 {
				return commands.Result{}, fmt.Errorf("usage: /model %s", action)
			}
			return commands.Result{Name: "model", Output: renderModelProfilesForCommand(ctx, runtime.Current(), sessions)}, nil
		case "use", "session":
			if len(cmd.Args) != 2 {
				return commands.Result{}, fmt.Errorf("usage: /model use <profile-id>")
			}
			if sessions == nil {
				return commands.Result{Name: "model", Output: "Session model runtime is unavailable in this process."}, nil
			}
			currentSession, ok := commands.CurrentSessionContext(ctx)
			if !ok || strings.TrimSpace(currentSession.SessionID) == "" {
				return commands.Result{Name: "model", Output: "Current session is unavailable; use Web/TUI session commands or set the default model instead."}, nil
			}
			profileID := strings.TrimSpace(cmd.Args[1])
			if profileID == "" {
				return commands.Result{}, fmt.Errorf("usage: /model use <profile-id>")
			}
			view, err := sessions.SetSessionModelProfile(ctx, currentSession.SessionID, profileID)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "model", Output: "Updated current session model profile to " + profileID + ".\n" + renderModelProfilesView(runtime.Current(), view)}, nil
		case "set", "default":
			if len(cmd.Args) != 2 {
				return commands.Result{}, fmt.Errorf("usage: /model default <profile-or-model>")
			}
			next := strings.TrimSpace(cmd.Args[1])
			if next == "" {
				return commands.Result{}, fmt.Errorf("usage: /model default <profile-or-model>")
			}
			values := map[string]interface{}{"api.default_model": next}
			view, err := runtime.Update(ctx, config.UpdateRequest{
				Values: values,
			})
			if err != nil {
				return commands.Result{}, err
			}
			if effectiveProfile, _ := view.EffectiveValues["api.default_model"].(string); strings.TrimSpace(effectiveProfile) == next {
				return commands.Result{Name: "model", Output: "Updated default model profile to " + effectiveProfile + "."}, nil
			}
			effective, _ := view.EffectiveValues["api.default_model"].(string)
			if strings.TrimSpace(effective) == "" {
				effective = next
			}
			return commands.Result{Name: "model", Output: "Updated default model to " + effective + "."}, nil
		default:
			return commands.Result{}, fmt.Errorf("unknown /model action %q", action)
		}
	}
}

func renderModelProfilesForCommand(ctx context.Context, cfg *config.Config, sessions modelSessionRuntime) string {
	if sessions != nil {
		if current, ok := commands.CurrentSessionContext(ctx); ok && strings.TrimSpace(current.SessionID) != "" {
			view, err := sessions.Models(ctx, current.SessionID)
			if err == nil {
				return renderModelProfilesView(cfg, view)
			}
		}
	}
	return renderModelProfilesConfig(cfg)
}

func renderModelProfilesConfig(cfg *config.Config) string {
	if cfg == nil {
		return "Current model: (unset)"
	}
	lines := []string{"Current model: " + strings.TrimSpace(cfg.Model)}
	if strings.TrimSpace(cfg.DefaultProfileID) != "" {
		lines = append(lines, "Default profile: "+strings.TrimSpace(cfg.DefaultProfileID))
	}
	if len(cfg.ModelProfiles) == 0 {
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "Available profiles:")
	keys := make([]string, 0, len(cfg.ModelProfiles))
	for key := range cfg.ModelProfiles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		profile, ok := cfg.ModelProfileByID(key)
		if !ok {
			continue
		}
		marker := " "
		if profile.ID == cfg.DefaultProfileID {
			marker = "*"
		}
		lines = append(lines, fmt.Sprintf("%s %s (%s): %s", marker, profile.ID, profile.Provider, profile.Model))
	}
	return strings.Join(lines, "\n")
}

func renderModelProfilesView(cfg *config.Config, view backend.ModelsView) string {
	currentModel := ""
	if cfg != nil {
		currentModel = strings.TrimSpace(cfg.Model)
	}
	if currentModel == "" {
		currentModel = "(unset)"
	}
	lines := []string{"Current model: " + currentModel}
	if strings.TrimSpace(view.DefaultProfileID) != "" {
		lines = append(lines, "Default profile: "+strings.TrimSpace(view.DefaultProfileID))
	}
	if strings.TrimSpace(view.SessionProfileID) != "" {
		lines = append(lines, "Session profile: "+strings.TrimSpace(view.SessionProfileID))
	}
	if len(view.Profiles) == 0 {
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "Available model profiles:")
	profiles := append([]backend.ModelProfile{}, view.Profiles...)
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Default != profiles[j].Default {
			return profiles[i].Default
		}
		if profiles[i].Selected != profiles[j].Selected {
			return profiles[i].Selected
		}
		return profiles[i].ID < profiles[j].ID
	})
	for _, profile := range profiles {
		defaultMark := " "
		if profile.Default {
			defaultMark = "*"
		}
		sessionMark := " "
		if profile.Selected {
			sessionMark = ">"
		}
		label := strings.TrimSpace(profile.Name)
		if label == "" {
			label = profile.ID
		}
		lines = append(lines, fmt.Sprintf("%s%s %s (%s): %s", defaultMark, sessionMark, profile.ID, profile.Provider, profile.Model))
		if label != profile.ID {
			lines[len(lines)-1] += " - " + label
		}
	}
	lines = append(lines, "Legend: * default, > current session")
	return strings.Join(lines, "\n")
}

// NewClearCommandHandler adapts current-session clearing into the slash-command surface.
func NewClearCommandHandler(admin *sessionadmin.Service) func(context.Context, *agent.Agent, commands.Command) (commands.Result, error) {
	return func(ctx context.Context, a *agent.Agent, cmd commands.Command) (commands.Result, error) {
		if len(cmd.Args) != 0 {
			return commands.Result{}, fmt.Errorf("usage: /clear")
		}
		runtime, sessionID, runtimeCtx, ok, err := currentAdminRuntime(ctx, a, admin)
		if err != nil {
			return commands.Result{}, err
		}
		if !ok {
			return commands.Result{Name: "clear", Output: "Current session context is unavailable in this process."}, nil
		}
		result, err := runtime.ClearMessages(ctx, sessionID, runtimeCtx)
		if err != nil {
			return commands.Result{}, err
		}
		return commands.Result{Name: "clear", Output: renderSessionActionResult(result), RefreshSnapshot: err == nil}, nil
	}
}

// NewApproveCommandHandler adapts permission approval into the slash-command surface.
func NewApproveCommandHandler(admin *sessionadmin.Service) func(context.Context, *agent.Agent, commands.Command) (commands.Result, error) {
	return func(ctx context.Context, a *agent.Agent, cmd commands.Command) (commands.Result, error) {
		runtime, sessionID, runtimeCtx, ok, err := currentAdminRuntime(ctx, a, admin)
		if err != nil {
			return commands.Result{}, err
		}
		if !ok {
			return commands.Result{Name: "approve", Output: "Current session context is unavailable in this process."}, nil
		}
		items, err := runtime.ListPendingPermissions(ctx, sessionID, runtimeCtx)
		if err != nil {
			return commands.Result{}, err
		}
		if len(cmd.Args) == 1 && strings.EqualFold(strings.TrimSpace(cmd.Args[0]), "list") {
			return commands.Result{Name: "approve", Output: renderApproveListOrEmpty(items)}, nil
		}
		requestID, scope, listOnly, err := resolveApproveCommandArgs(cmd.Args, items)
		if err != nil {
			return commands.Result{}, err
		}
		if listOnly {
			return commands.Result{Name: "approve", Output: renderApproveListOrEmpty(items)}, nil
		}
		resolution, err := runtime.ApprovePermission(ctx, sessionID, runtimeCtx, requestID, scope)
		if err != nil {
			return commands.Result{}, err
		}
		return commands.Result{Name: "approve", Output: renderPermissionResolution("approved", resolution), RefreshSnapshot: true}, nil
	}
}

func resolveApproveCommandArgs(args []string, items []tools.PendingPermission) (requestID string, scope tools.PermissionGrantScope, listOnly bool, err error) {
	scope = tools.PermissionGrantOnce
	if len(args) > 2 {
		return "", "", false, fmt.Errorf("usage: /approve [list|request-id] [once|session]")
	}
	if len(args) == 0 {
		switch len(items) {
		case 0:
			return "", "", true, nil
		case 1:
			return strings.TrimSpace(items[0].ID), scope, false, nil
		default:
			return "", "", true, nil
		}
	}
	first := strings.TrimSpace(args[0])
	switch strings.ToLower(first) {
	case "", "once":
		if len(items) == 1 {
			return strings.TrimSpace(items[0].ID), tools.PermissionGrantOnce, false, nil
		}
		return "", "", true, nil
	case string(tools.PermissionGrantSession):
		if len(args) != 1 {
			return "", "", false, fmt.Errorf("usage: /approve [list|request-id] [once|session]")
		}
		if len(items) == 1 {
			return strings.TrimSpace(items[0].ID), tools.PermissionGrantSession, false, nil
		}
		return "", "", true, nil
	case "list":
		return "", "", true, nil
	}
	requestID = first
	if requestID == "" {
		return "", "", false, fmt.Errorf("usage: /approve [list|request-id] [once|session]")
	}
	if len(args) == 2 {
		switch strings.ToLower(strings.TrimSpace(args[1])) {
		case "", string(tools.PermissionGrantOnce):
			scope = tools.PermissionGrantOnce
		case string(tools.PermissionGrantSession):
			scope = tools.PermissionGrantSession
		default:
			return "", "", false, fmt.Errorf("usage: /approve [list|request-id] [once|session]")
		}
	}
	return requestID, scope, false, nil
}

func renderApproveListOrEmpty(items []tools.PendingPermission) string {
	if len(items) == 0 {
		return "No pending permission requests for this session."
	}
	return renderPendingPermissions(items)
}

// NewDenyCommandHandler adapts permission denial into the slash-command surface.
func NewDenyCommandHandler(admin *sessionadmin.Service) func(context.Context, *agent.Agent, commands.Command) (commands.Result, error) {
	return func(ctx context.Context, a *agent.Agent, cmd commands.Command) (commands.Result, error) {
		runtime, sessionID, runtimeCtx, ok, err := currentAdminRuntime(ctx, a, admin)
		if err != nil {
			return commands.Result{}, err
		}
		if !ok {
			return commands.Result{Name: "deny", Output: "Current session context is unavailable in this process."}, nil
		}
		if len(cmd.Args) == 0 {
			return commands.Result{}, fmt.Errorf("usage: /deny <request-id> [reason...]")
		}
		resolution, err := runtime.DenyPermission(ctx, sessionID, runtimeCtx, strings.TrimSpace(cmd.Args[0]), strings.TrimSpace(strings.Join(cmd.Args[1:], " ")))
		if err != nil {
			return commands.Result{}, err
		}
		return commands.Result{Name: "deny", Output: renderPermissionResolution("denied", resolution), RefreshSnapshot: true}, nil
	}
}

// NewSessionCommandHandler adapts session browsing and current-session management into slash commands.
func NewSessionCommandHandler(runtime sessionCommandRuntime, admin *sessionadmin.Service) func(context.Context, *agent.Agent, commands.Command) (commands.Result, error) {
	return func(ctx context.Context, a *agent.Agent, cmd commands.Command) (commands.Result, error) {
		if runtime == nil {
			return commands.Result{Name: "session", Output: "Session runtime is unavailable in this process."}, nil
		}
		if len(cmd.Args) == 0 {
			cmd.Args = []string{"current"}
		}
		action := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
		args := cmd.Args[1:]
		switch action {
		case "current":
			if len(args) != 0 {
				return commands.Result{}, fmt.Errorf("usage: /session current")
			}
			bound, sessionID, runtimeCtx, ok, err := currentAdminRuntime(ctx, a, admin)
			if err != nil {
				return commands.Result{}, err
			}
			if !ok {
				return commands.Result{Name: "session", Output: "Current session context is unavailable in this process."}, nil
			}
			summary, err := bound.CurrentSession(ctx, sessionID, runtimeCtx)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "session", Output: renderCurrentSession(summary)}, nil
		case "context":
			if len(args) != 0 {
				return commands.Result{}, fmt.Errorf("usage: /session context")
			}
			bound, sessionID, runtimeCtx, ok, err := currentAdminRuntime(ctx, a, admin)
			if err != nil {
				return commands.Result{}, err
			}
			if !ok {
				return commands.Result{Name: "session", Output: "Current session context is unavailable in this process."}, nil
			}
			summary, err := bound.ContextSummary(ctx, sessionID, runtimeCtx)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "session", Output: renderContextInspection(summary)}, nil
		case "tokens":
			bound, sessionID, runtimeCtx, ok, err := currentAdminRuntime(ctx, a, admin)
			if err != nil {
				return commands.Result{}, err
			}
			if !ok {
				return commands.Result{Name: "session", Output: "Current session context is unavailable in this process."}, nil
			}
			reveal := len(args) == 1 && strings.EqualFold(strings.TrimSpace(args[0]), "reveal")
			if len(args) > 1 || (len(args) == 1 && !reveal) {
				return commands.Result{}, fmt.Errorf("usage: /session tokens [reveal]")
			}
			summary, err := bound.TokenSummary(ctx, sessionID, runtimeCtx, reveal)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "session", Output: renderTokenSummary(summary)}, nil
		case "auth":
			if len(args) != 1 {
				return commands.Result{}, fmt.Errorf("usage: /session auth <status|login|logout>")
			}
			bound, sessionID, runtimeCtx, ok, err := currentAdminRuntime(ctx, a, admin)
			if err != nil {
				return commands.Result{}, err
			}
			if !ok {
				return commands.Result{Name: "session", Output: "Current session context is unavailable in this process."}, nil
			}
			status, err := bound.ChannelAuth(ctx, sessionID, runtimeCtx, strings.TrimSpace(args[0]))
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "session", Output: renderChannelAuth(status)}, nil
		case "list":
			if len(args) > 1 {
				return commands.Result{}, fmt.Errorf("usage: /session list [channel]")
			}
			filter := backend.SessionListFilter{}
			if len(args) == 1 {
				filter.Channel = strings.TrimSpace(args[0])
			}
			sessions, err := runtime.ListSessions(ctx, filter)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "session", Output: renderSessionList(sessions)}, nil
		case "new":
			if len(args) > 1 {
				return commands.Result{}, fmt.Errorf("usage: /session new [key|channel:key]")
			}
			locator := backend.SessionLocator{Channel: "local", Key: "session-" + time.Now().Format("20060102-150405")}
			if len(args) == 1 {
				spec := strings.TrimSpace(args[0])
				if spec == "" {
					return commands.Result{}, fmt.Errorf("usage: /session new [key|channel:key]")
				}
				if strings.Contains(spec, ":") {
					parts := strings.SplitN(spec, ":", 2)
					locator.Channel = strings.TrimSpace(parts[0])
					locator.Key = strings.TrimSpace(parts[1])
				} else {
					locator.Key = spec
				}
				if strings.TrimSpace(locator.Channel) == "" || strings.TrimSpace(locator.Key) == "" {
					return commands.Result{}, fmt.Errorf("usage: /session new [key|channel:key]")
				}
			}
			opened, err := runtime.OpenSession(ctx, locator)
			if err != nil {
				return commands.Result{}, err
			}
			return commands.Result{Name: "session", Output: renderSessionCreated(*opened)}, nil
		default:
			return commands.Result{}, fmt.Errorf("unknown /session action %q", action)
		}
	}
}

func currentAdminRuntime(ctx context.Context, a *agent.Agent, admin *sessionadmin.Service) (tools.SessionAdminRuntime, string, automation.SessionContext, bool, error) {
	if admin == nil {
		return nil, "", automation.SessionContext{}, false, fmt.Errorf("session admin runtime unavailable")
	}
	current, ok := commands.CurrentSessionContext(ctx)
	if !ok || strings.TrimSpace(current.SessionID) == "" {
		return nil, "", automation.SessionContext{}, false, nil
	}
	runtimeCtx := automation.SessionContext{
		SessionID:      current.SessionID,
		LocatorChannel: current.Channel,
		LocatorKey:     current.Key,
		LocatorUserID:  current.UserID,
		Metadata:       cloneMetadata(current.Metadata),
	}
	if a == nil {
		return admin.Bind(nil), current.SessionID, runtimeCtx, true, nil
	}
	return admin.Bind(a), current.SessionID, runtimeCtx, true, nil
}

func renderCurrentSession(summary tools.SessionSummary) string {
	lines := []string{
		"Current session:",
		"ID: " + summary.SessionID,
		"Locator: " + renderLocator(backend.SessionLocator{Channel: summary.Channel, Key: summary.Key}),
		fmt.Sprintf("Messages: %d", summary.MessageCount),
		fmt.Sprintf("Active skills: %d", summary.ActiveSkillCount),
		fmt.Sprintf("Pending approvals: %d", summary.PendingPermissionCount),
		fmt.Sprintf("Running: %t", summary.Running),
	}
	if summary.UserID != "" {
		lines = append(lines, "User: "+summary.UserID)
	}
	if !summary.UpdatedAt.IsZero() {
		lines = append(lines, "Updated: "+renderSessionTime(summary.UpdatedAt))
	}
	return strings.Join(lines, "\n")
}

func renderContextInspection(summary tools.ContextInspection) string {
	lines := []string{
		"Current session context:",
		fmt.Sprintf("Messages: %d", summary.MessageCount),
		fmt.Sprintf("Estimated tokens: %d", summary.TokenEstimate),
		fmt.Sprintf("Compress threshold: %d", summary.CompressThreshold),
		fmt.Sprintf("Suggest /compact: %t", summary.SuggestCompact),
		fmt.Sprintf("Active skills: %d", summary.ActiveSkillCount),
		fmt.Sprintf("Pending approvals: %d", summary.PendingPermissionCount),
	}
	return strings.Join(lines, "\n")
}

func renderTokenSummary(summary tools.SessionTokenView) string {
	lines := []string{"Session tokens:"}
	if summary.Channel != "" {
		lines = append(lines, "Channel: "+summary.Channel)
	}
	lines = append(lines, fmt.Sprintf("Supported: %t", summary.Supported))
	if summary.AccountID != "" {
		lines = append(lines, "Account: "+summary.AccountID)
	}
	if summary.UserID != "" {
		lines = append(lines, "User: "+summary.UserID)
	}
	if summary.TokenCount > 0 {
		lines = append(lines, fmt.Sprintf("Token count: %d", summary.TokenCount))
	}
	if !summary.UpdatedAt.IsZero() {
		lines = append(lines, "Updated: "+renderSessionTime(summary.UpdatedAt))
	}
	if summary.TokenMasked != "" {
		lines = append(lines, "Masked token: "+summary.TokenMasked)
	}
	if summary.Token != "" {
		lines = append(lines, "Token: "+summary.Token)
	}
	if summary.Message != "" {
		lines = append(lines, summary.Message)
	}
	return strings.Join(lines, "\n")
}

func renderChannelAuth(status tools.SessionChannelAuth) string {
	lines := []string{"Channel auth:"}
	if status.Channel != "" {
		lines = append(lines, "Channel: "+status.Channel)
	}
	lines = append(lines, fmt.Sprintf("Supported: %t", status.Supported))
	if status.AccountID != "" {
		lines = append(lines, "Account: "+status.AccountID)
	}
	if status.State != "" {
		lines = append(lines, "State: "+status.State)
	}
	if status.Supported {
		lines = append(lines, fmt.Sprintf("Enabled: %t", status.Enabled))
		lines = append(lines, fmt.Sprintf("Configured: %t", status.Configured))
	}
	if status.LoginState != "" {
		lines = append(lines, "Login state: "+status.LoginState)
	}
	if status.QRCode != "" {
		lines = append(lines, "QR token: "+status.QRCode)
	}
	if status.QRCodeImgURL != "" {
		lines = append(lines, "QR image URL: "+status.QRCodeImgURL)
	}
	if status.LoginMessage != "" {
		lines = append(lines, status.LoginMessage)
	} else if status.Message != "" {
		lines = append(lines, status.Message)
	}
	return strings.Join(lines, "\n")
}

func renderPendingPermissions(items []tools.PendingPermission) string {
	if len(items) == 0 {
		return "No pending permission requests."
	}
	lines := []string{"Pending permission requests:"}
	for _, item := range items {
		line := fmt.Sprintf("- %s tool=%s", item.ID, item.Request.ToolName)
		if item.Request.Action != "" {
			line += " action=" + item.Request.Action
		}
		if item.Reason != "" {
			line += " reason=" + item.Reason
		}
		if item.Request.Command != "" {
			line += " command=" + item.Request.Command
		}
		if len(item.Request.Paths) > 0 {
			line += " paths=" + strings.Join(item.Request.Paths, ",")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderPermissionResolution(status string, resolution tools.PermissionResolution) string {
	lines := []string{
		fmt.Sprintf("Permission %s: %s", status, resolution.RequestID),
		"Tool: " + resolution.Request.ToolName,
	}
	if resolution.Request.Action != "" {
		lines = append(lines, "Action: "+resolution.Request.Action)
	}
	if resolution.Scope != "" {
		lines = append(lines, "Scope: "+string(resolution.Scope))
	}
	if resolution.Reason != "" {
		lines = append(lines, "Reason: "+resolution.Reason)
	}
	if resolution.Resumed {
		if resolution.ResumeStatus != "" {
			lines = append(lines, "Resume: "+resolution.ResumeStatus)
		}
		if resolution.ResumePendingRequestID != "" {
			lines = append(lines, fmt.Sprintf("Next approval: /approve %s", resolution.ResumePendingRequestID))
		}
		if resolution.ResumeError != "" {
			lines = append(lines, "Resume error: "+resolution.ResumeError)
		}
		if strings.TrimSpace(resolution.ResumeOutput) != "" {
			lines = append(lines, "", resolution.ResumeOutput)
		}
	}
	return strings.Join(lines, "\n")
}

func renderSessionActionResult(result tools.SessionActionResult) string {
	lines := []string{fmt.Sprintf("%s: %s", result.Action, result.Status)}
	if result.ClearedMessages > 0 {
		lines = append(lines, fmt.Sprintf("Cleared messages: %d", result.ClearedMessages))
	}
	if result.Message != "" {
		lines = append(lines, result.Message)
	}
	return strings.Join(lines, "\n")
}

func renderSessionList(items []backend.ListedSession) string {
	if len(items) == 0 {
		return "No persisted sessions found."
	}
	lines := []string{"Sessions:"}
	for _, item := range items {
		line := fmt.Sprintf("- %s  %s  updated=%s", item.SessionID, renderLocator(item.Locator), renderSessionTime(item.UpdatedAt))
		if strings.TrimSpace(item.Title) != "" {
			line += "  title=" + item.Title
		}
		if item.Running {
			line += "  [running]"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderSessionCreated(session backend.OpenedSession) string {
	return strings.Join([]string{
		"Opened session:",
		"ID: " + session.SessionID,
		"Locator: " + renderLocator(session.Locator),
		"Use it with: godex command --session " + renderLocator(session.Locator) + " /help",
	}, "\n")
}

func renderLocator(locator backend.SessionLocator) string {
	if strings.TrimSpace(locator.Channel) == "" {
		return locator.Key
	}
	if strings.TrimSpace(locator.Key) == "" {
		return locator.Channel
	}
	return locator.Channel + ":" + locator.Key
}

func renderSessionTime(ts time.Time) string {
	if ts.IsZero() {
		return "(unknown)"
	}
	return ts.UTC().Format("2006-01-02 15:04Z")
}

func cloneMetadata(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
