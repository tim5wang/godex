package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/tools"
)

// Backend is the unified godex backend surface used by the ACP server.
// It is satisfied by *backend.Service.
type Backend interface {
	OpenSession(context.Context, backend.SessionLocator) (*backend.OpenedSession, error)
	SubmitAsync(context.Context, string, message.Envelope, ...backend.SubmitOptions) (*backend.SubmitResult, error)
	AttachSink(string, events.Sink) (func(), error)
	ExecuteCommand(context.Context, string, commands.Command) (commands.Result, error)
	PendingPermissions(context.Context, string) ([]tools.PendingPermission, error)
	ApprovePermission(context.Context, string, string, tools.PermissionGrantScope) (tools.PermissionResolution, error)
	DenyPermission(context.Context, string, string, string) (tools.PermissionResolution, error)
}

// BackendPromptOptions tunes how ACP turns are mapped into GoDex sessions.
type BackendPromptOptions struct {
	AgentProfile string
}

// BackendPromptHandler creates a PromptHandler that delegates to the godex
// unified backend. Each ACP prompt becomes an OpenSession → AttachSink →
// SubmitAsync → collect events cycle, giving the ACP agent full access to
// tools, memory, skills, conversation history, and all other godex capabilities.
func BackendPromptHandler(bk Backend) PromptHandler {
	return BackendPromptHandlerWithOptions(bk, BackendPromptOptions{})
}

// BackendPromptHandlerWithOptions creates a backend prompt handler with
// optional per-process ACP overrides.
func BackendPromptHandlerWithOptions(bk Backend, opts BackendPromptOptions) PromptHandler {
	return func(ctx context.Context, turn PromptTurn) (PromptResult, error) {
		// Map ACP session to a godex session via the unified backend.
		locator := backend.SessionLocator{
			Channel: "acp",
			Key:     turn.SessionID,
		}
		if profile := strings.TrimSpace(opts.AgentProfile); profile != "" {
			locator.Metadata = map[string]string{"agent_profile": config.NormalizeAgentProfile(profile)}
		}
		opened, err := bk.OpenSession(ctx, locator)
		if err != nil {
			return PromptResult{}, fmt.Errorf("acp open session: %w", err)
		}
		sessionID := opened.SessionID

		if cmd, ok := commands.Parse(turn.Prompt); ok {
			result, err := bk.ExecuteCommand(ctx, sessionID, cmd)
			if err != nil {
				return PromptResult{}, fmt.Errorf("acp command: %w", err)
			}
			output := strings.TrimSpace(result.Output)
			if output == "" {
				output = fmt.Sprintf("Command /%s completed.", cmd.Name)
			}
			return PromptResult{
				FinalText:  output,
				StopReason: acp.StopReasonEndTurn,
			}, nil
		}

		// Subscribe to runtime events so we can stream text deltas back
		// to the ACP client and collect the final reply.
		eventCh := make(chan events.Event, 256)
		unsubscribe, err := bk.AttachSink(sessionID, events.SinkFunc(func(event events.Event) {
			select {
			case <-ctx.Done():
			case eventCh <- event:
			default:
			}
		}))
		if err != nil {
			return PromptResult{}, fmt.Errorf("acp attach sink: %w", err)
		}
		defer unsubscribe()

		// Submit the user prompt through the unified backend.
		envelope := message.NewTextEnvelope(message.SourceACP, sessionID, "acp_user", turn.Prompt, time.Now())
		if profile := strings.TrimSpace(opts.AgentProfile); profile != "" {
			envelope.Metadata = map[string]string{"agent_profile": config.NormalizeAgentProfile(profile)}
		}
		result, err := bk.SubmitAsync(ctx, sessionID, envelope)
		if err != nil {
			return PromptResult{}, fmt.Errorf("acp submit: %w", err)
		}
		turnID := result.TurnID
		watchedTurnIDs := map[string]struct{}{}
		if strings.TrimSpace(turnID) != "" {
			watchedTurnIDs[strings.TrimSpace(turnID)] = struct{}{}
		}
		resumeFallbackText := map[string]string{}
		resolvedApprovalIDs := map[string]struct{}{}
		if result.PendingApproval || strings.TrimSpace(result.PendingRequestID) != "" || strings.EqualFold(result.Status, "pending_approval") {
			items, pendingErr := bk.PendingPermissions(ctx, sessionID)
			if pendingErr != nil {
				logger.Warnf("ACP pending permissions lookup: %v", pendingErr)
			}
			approval, ok, err := resolveNativeApproval(ctx, turn.PermissionRequester, bk, sessionID, result.PendingRequestID, items)
			if err != nil {
				return PromptResult{}, err
			}
			if ok {
				if approval.RequestID != "" {
					resolvedApprovalIDs[approval.RequestID] = struct{}{}
				}
				if approval.ContinueTurnID != "" {
					watchedTurnIDs[approval.ContinueTurnID] = struct{}{}
					if approval.FallbackText != "" {
						resumeFallbackText[approval.ContinueTurnID] = approval.FallbackText
					}
				} else {
					return approval.Result, nil
				}
			} else {
				return PromptResult{
					FinalText:  renderPendingApproval(result.PendingRequestID, items),
					StopReason: acp.StopReasonEndTurn,
				}, nil
			}
		}

		// Collect events from the backend, streaming text deltas to the
		// ACP peer via SessionUpdater, and accumulating the final text.
		var collected strings.Builder
		streamed := false
		deltaSinceComplete := false
		var lastTodoPlan []acp.PlanEntry
		for {
			select {
			case <-ctx.Done():
				finalText := strings.TrimSpace(collected.String())
				return PromptResult{
					FinalText:  finalText,
					StopReason: acp.StopReasonCancelled,
				}, nil

			case event := <-eventCh:
				if len(watchedTurnIDs) > 0 {
					if _, ok := watchedTurnIDs[strings.TrimSpace(event.TurnID)]; !ok {
						continue
					}
				} else if event.TurnID != turnID {
					continue
				}
				switch event.Type {
				case events.EventAssistantTextDelta:
					payload, ok := event.Payload.(events.TextPayload)
					if !ok || payload.Text == "" {
						continue
					}
					collected.WriteString(payload.Text)
					deltaSinceComplete = true
					if turn.Updater != nil {
						if err := turn.Updater.Update(ctx, acp.UpdateAgentMessageText(payload.Text)); err != nil {
							logger.Warnf("ACP session update: %v", err)
						} else {
							streamed = true
						}
					}

				case events.EventAssistantMessageComplete:
					payload, ok := event.Payload.(events.TextPayload)
					if ok && !deltaSinceComplete && payload.Text != "" {
						collected.WriteString(payload.Text)
					}
					deltaSinceComplete = false

				case events.EventToolCallStarted:
					payload, ok := event.Payload.(events.ToolCallPayload)
					if !ok || payload.Name == "" {
						continue
					}
					if turn.Updater != nil {
						callID := strings.TrimSpace(payload.ID)
						if callID == "" {
							callID = fmt.Sprintf("tc_%s", payload.Name)
						}
						_ = turn.Updater.Update(ctx, acp.StartToolCall(
							acp.ToolCallId(callID),
							toolCallTitle(payload.Name, payload.Input),
							acp.WithStartStatus(acp.ToolCallStatusInProgress),
							acp.WithStartKind(toolKind(payload.Name)),
							acp.WithStartRawInput(payload.Input),
						))
					}

				case events.EventTodoListUpdated:
					payload, ok := event.Payload.(events.TodoListPayload)
					if !ok {
						continue
					}
					if turn.Updater != nil {
						callID := strings.TrimSpace(payload.SourceToolCallID)
						if callID == "" {
							callID = "tc_todo_write"
						}
						_ = turn.Updater.Update(ctx, acp.UpdateToolCall(
							acp.ToolCallId(callID),
							acp.WithUpdateTitle(todoToolTitle(payload)),
							acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
							acp.WithUpdateRawOutput(todoRawOutput(payload)),
							acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(payload.RenderPlain()))}),
						))
						lastTodoPlan = todoPlanEntries(payload)
						_ = turn.Updater.Update(ctx, acp.UpdatePlan(lastTodoPlan...))
					}

				case events.EventToolCallFinished:
					payload, ok := event.Payload.(events.ToolCallPayload)
					if !ok || payload.Name == "" {
						continue
					}
					if payload.Name == "todo_write" && strings.TrimSpace(payload.Error) == "" {
						continue
					}
					if turn.Updater != nil {
						callID := strings.TrimSpace(payload.ID)
						if callID == "" {
							callID = fmt.Sprintf("tc_%s", payload.Name)
						}
						output := strings.TrimSpace(payload.Output)
						if len(output) > 500 {
							output = output[:500] + "…"
						}
						_ = turn.Updater.Update(ctx, acp.UpdateToolCall(
							acp.ToolCallId(callID),
							acp.WithUpdateTitle(toolCallTitle(payload.Name, payload.Input)),
							acp.WithUpdateRawOutput(output),
							acp.WithUpdateRawInput(payload.Input),
							acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
						))
					}

				case events.EventWarningRaised:
					payload, ok := event.Payload.(events.NoticePayload)
					if !ok || payload.Message == "" {
						continue
					}
					if turn.Updater != nil {
						warnText := fmt.Sprintf("[warning] %s", payload.Message)
						_ = turn.Updater.Update(ctx, acp.UpdateAgentMessageText(warnText))
					}

				case events.EventErrorRaised:
					payload, _ := event.Payload.(events.NoticePayload)
					finalText := strings.TrimSpace(collected.String())
					if finalText != "" {
						// We already streamed some text; return what we have.
						return PromptResult{
							FinalText:  finalText,
							StopReason: acp.StopReasonEndTurn,
							Streamed:   streamed,
						}, nil
					}
					errMsg := strings.TrimSpace(payload.Message)
					if errMsg == "" {
						errMsg = "agent error"
					}
					return PromptResult{}, errors.New(errMsg)

				case events.EventTurnCompleted:
					payload, _ := event.Payload.(events.TurnPayload)
					status := strings.TrimSpace(payload.Status)
					if strings.EqualFold(status, "pending_approval") {
						if requestID := strings.TrimSpace(result.PendingRequestID); requestID != "" {
							if _, seen := resolvedApprovalIDs[requestID]; seen {
								continue
							}
						}
						items, pendingErr := bk.PendingPermissions(ctx, sessionID)
						if pendingErr != nil {
							logger.Warnf("ACP pending permissions lookup: %v", pendingErr)
						}
						approval, ok, err := resolveNativeApproval(ctx, turn.PermissionRequester, bk, sessionID, result.PendingRequestID, items)
						if err != nil {
							return PromptResult{}, err
						}
						if ok {
							if approval.RequestID != "" {
								resolvedApprovalIDs[approval.RequestID] = struct{}{}
							}
							if approval.ContinueTurnID != "" {
								watchedTurnIDs[approval.ContinueTurnID] = struct{}{}
								if approval.FallbackText != "" {
									resumeFallbackText[approval.ContinueTurnID] = approval.FallbackText
								}
								continue
							}
							return approval.Result, nil
						}
						return PromptResult{
							FinalText:  renderPendingApproval(result.PendingRequestID, items),
							StopReason: acp.StopReasonEndTurn,
						}, nil
					}
					finalText := strings.TrimSpace(collected.String())
					if finalText == "" {
						finalText = strings.TrimSpace(resumeFallbackText[strings.TrimSpace(event.TurnID)])
					}
					if strings.EqualFold(status, "canceled") || strings.EqualFold(status, "cancelled") {
						return PromptResult{
							FinalText:  finalText,
							StopReason: acp.StopReasonCancelled,
							Streamed:   streamed,
						}, nil
					}
					if strings.EqualFold(status, "error") && finalText == "" {
						return PromptResult{}, errors.New("agent turn failed")
					}
					if strings.EqualFold(status, "completed") && turn.Updater != nil && lastTodoPlan != nil {
						_ = turn.Updater.Update(ctx, acp.UpdatePlan(completePlanEntries(lastTodoPlan)...))
					}
					return PromptResult{
						FinalText:  finalText,
						StopReason: acp.StopReasonEndTurn,
						Streamed:   streamed,
					}, nil
				}
			}
		}
	}
}

func renderPendingApproval(requestID string, items []tools.PendingPermission) string {
	requestID = strings.TrimSpace(requestID)
	item, ok := findPendingPermission(requestID, items)
	if ok && requestID == "" {
		requestID = strings.TrimSpace(item.ID)
	}
	if requestID == "" {
		requestID = "pending"
	}

	var b strings.Builder
	b.WriteString("Pending approval required.\n")
	b.WriteString("Request: ")
	b.WriteString(requestID)
	b.WriteString("\n")
	if ok {
		req := item.Request
		if intent := strings.TrimSpace(tools.PermissionIntentSummary(item)); intent != "" {
			b.WriteString("Intent: ")
			b.WriteString(intent)
			b.WriteString("\n")
		}
		if risk := strings.TrimSpace(tools.PermissionRiskSummary(req)); risk != "" {
			b.WriteString("Risk: ")
			b.WriteString(risk)
			b.WriteString("\n")
		}
		if expiry := strings.TrimSpace(tools.PermissionExpirySummary(item, time.Now())); expiry != "" {
			b.WriteString("Expiry: ")
			b.WriteString(expiry)
			b.WriteString("\n")
		}
		if toolName := strings.TrimSpace(req.ToolName); toolName != "" {
			b.WriteString("Tool: ")
			b.WriteString(toolName)
			b.WriteString("\n")
		}
		if action := strings.TrimSpace(req.Action); action != "" {
			b.WriteString("Action: ")
			b.WriteString(action)
			b.WriteString("\n")
		}
		if command := strings.TrimSpace(req.Command); command != "" {
			b.WriteString("Command: ")
			b.WriteString(command)
			b.WriteString("\n")
		}
		if len(req.Paths) > 0 {
			b.WriteString("Paths: ")
			b.WriteString(strings.Join(req.Paths, ", "))
			b.WriteString("\n")
		}
		if reason := strings.TrimSpace(item.Reason); reason != "" {
			b.WriteString("Reason: ")
			b.WriteString(reason)
			b.WriteString("\n")
		}
	}
	b.WriteString("\nApprove once: /approve")
	b.WriteString("\nApprove pattern: /approve pattern")
	b.WriteString("\nApprove 10 minutes: /approve timebox:10m")
	b.WriteString("\nApprove 5 uses: /approve count:5")
	b.WriteString("\nApprove for session: /approve session")
	b.WriteString("\nInspect all pending: /approve list")
	b.WriteString("\nDeny: /deny ")
	b.WriteString(requestID)
	return b.String()
}

type nativeApprovalSelection struct {
	RequestID string
	OptionID  string
}

type nativeApprovalResult struct {
	RequestID      string
	Result         PromptResult
	ContinueTurnID string
	FallbackText   string
}

func resolveNativeApproval(ctx context.Context, requester PermissionRequester, bk Backend, sessionID, requestID string, items []tools.PendingPermission) (nativeApprovalResult, bool, error) {
	selection, ok := requestNativeApproval(ctx, requester, requestID, items)
	if !ok {
		return nativeApprovalResult{}, false, nil
	}
	switch selection.OptionID {
	case "canceled":
		return nativeApprovalResult{
			RequestID: selection.RequestID,
			Result:    PromptResult{FinalText: "Permission request canceled.", StopReason: acp.StopReasonCancelled},
		}, true, nil
	case "allow_once":
		resolution, err := bk.ApprovePermission(ctx, sessionID, selection.RequestID, tools.PermissionGrantOnce)
		if err != nil {
			return nativeApprovalResult{}, false, err
		}
		return approvalResultFromResolution(resolution), true, nil
	case "allow_session":
		resolution, err := bk.ApprovePermission(ctx, sessionID, selection.RequestID, tools.PermissionGrantSession)
		if err != nil {
			return nativeApprovalResult{}, false, err
		}
		return approvalResultFromResolution(resolution), true, nil
	case "deny":
		resolution, err := bk.DenyPermission(ctx, sessionID, selection.RequestID, "Denied from ACP client")
		if err != nil {
			return nativeApprovalResult{}, false, err
		}
		return nativeApprovalResult{
			RequestID: strings.TrimSpace(resolution.RequestID),
			Result:    PromptResult{FinalText: renderPermissionResolution(resolution), StopReason: acp.StopReasonEndTurn},
		}, true, nil
	default:
		return nativeApprovalResult{}, false, nil
	}
}

func approvalResultFromResolution(resolution tools.PermissionResolution) nativeApprovalResult {
	if turnID := strings.TrimSpace(resolution.ResumeTurnID); turnID != "" {
		return nativeApprovalResult{
			RequestID:      strings.TrimSpace(resolution.RequestID),
			ContinueTurnID: turnID,
			FallbackText:   renderPermissionResolution(resolution),
		}
	}
	return nativeApprovalResult{
		RequestID: strings.TrimSpace(resolution.RequestID),
		Result:    PromptResult{FinalText: renderPermissionResolution(resolution), StopReason: acp.StopReasonEndTurn},
	}
}

func requestNativeApproval(ctx context.Context, requester PermissionRequester, requestID string, items []tools.PendingPermission) (nativeApprovalSelection, bool) {
	if requester == nil {
		return nativeApprovalSelection{}, false
	}
	requestID = strings.TrimSpace(requestID)
	item, ok := findPendingPermission(requestID, items)
	if !ok {
		return nativeApprovalSelection{}, false
	}
	if requestID == "" {
		requestID = strings.TrimSpace(item.ID)
	}
	if requestID == "" {
		return nativeApprovalSelection{}, false
	}
	resp, err := requester.RequestPermission(ctx, acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(requestID),
			Title:      stringPtr(approvalTitle(item)),
			RawInput:   approvalRawInput(item),
			Status:     toolCallStatusPtr(acp.ToolCallStatusPending),
		},
		Options: []acp.PermissionOption{
			{OptionId: acp.PermissionOptionId("allow_once"), Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
			{OptionId: acp.PermissionOptionId("allow_session"), Name: "Allow for session", Kind: acp.PermissionOptionKindAllowAlways},
			{OptionId: acp.PermissionOptionId("deny"), Name: "Deny", Kind: acp.PermissionOptionKindRejectOnce},
		},
	})
	if err != nil {
		logger.Warnf("ACP native approval failed: %v", err)
		return nativeApprovalSelection{}, false
	}
	if resp.Outcome.Cancelled != nil {
		return nativeApprovalSelection{RequestID: requestID, OptionID: "canceled"}, true
	}
	if resp.Outcome.Selected == nil {
		return nativeApprovalSelection{}, false
	}
	optionID := strings.TrimSpace(string(resp.Outcome.Selected.OptionId))
	if optionID == "" {
		return nativeApprovalSelection{}, false
	}
	return nativeApprovalSelection{RequestID: requestID, OptionID: optionID}, true
}

func renderPermissionResolution(resolution tools.PermissionResolution) string {
	if output := strings.TrimSpace(resolution.ResumeOutput); output != "" {
		return output
	}
	if errText := strings.TrimSpace(resolution.ResumeError); errText != "" {
		return "Permission " + strings.TrimSpace(resolution.RequestID) + " resolved, but resume failed: " + errText
	}
	requestID := strings.TrimSpace(resolution.RequestID)
	if requestID == "" {
		requestID = "request"
	}
	switch resolution.Decision {
	case tools.PermissionAllow:
		return "Approved permission " + requestID + "."
	case tools.PermissionDeny:
		return "Denied permission " + requestID + "."
	default:
		return "Resolved permission " + requestID + "."
	}
}

func approvalTitle(item tools.PendingPermission) string {
	req := item.Request
	parts := []string{"Approval"}
	if intent := strings.TrimSpace(tools.PermissionIntentSummary(item)); intent != "" {
		parts = append(parts, truncateString(intent, 120))
	}
	if risk := strings.TrimSpace(tools.PermissionRiskSummary(req)); risk != "" {
		parts = append(parts, truncateString(risk, 80))
	}
	if expiry := strings.TrimSpace(tools.PermissionExpirySummary(item, time.Now())); expiry != "" {
		parts = append(parts, expiry)
	}
	if toolName := strings.TrimSpace(req.ToolName); toolName != "" {
		parts = append(parts, toolName)
	}
	if action := strings.TrimSpace(req.Action); action != "" {
		parts = append(parts, action)
	}
	if command := strings.TrimSpace(req.Command); command != "" {
		parts = append(parts, truncateString(command, 80))
	} else if len(req.Paths) > 0 {
		parts = append(parts, truncateString(strings.Join(req.Paths, ", "), 80))
	}
	if reason := strings.TrimSpace(item.Reason); reason != "" {
		parts = append(parts, truncateString(reason, 80))
	}
	return strings.Join(parts, " · ")
}

func approvalRawInput(item tools.PendingPermission) map[string]any {
	req := item.Request
	raw := map[string]any{
		"request_id": item.ID,
		"tool":       req.ToolName,
		"action":     req.Action,
		"reason":     item.Reason,
		"source":     req.Source,
		"sender":     req.Sender,
	}
	if command := strings.TrimSpace(req.Command); command != "" {
		raw["command"] = command
	}
	if len(req.Paths) > 0 {
		raw["paths"] = req.Paths
	}
	return raw
}

func todoPlanEntries(payload events.TodoListPayload) []acp.PlanEntry {
	entries := make([]acp.PlanEntry, 0, len(payload.Items))
	for _, item := range payload.Items {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		entries = append(entries, acp.PlanEntry{
			Content:  content,
			Priority: acp.PlanEntryPriorityMedium,
			Status:   todoPlanStatus(item.Status),
		})
	}
	return entries
}

func todoPlanStatus(status string) acp.PlanEntryStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return acp.PlanEntryStatusCompleted
	case "in_progress":
		return acp.PlanEntryStatusInProgress
	default:
		return acp.PlanEntryStatusPending
	}
}

func completePlanEntries(entries []acp.PlanEntry) []acp.PlanEntry {
	if entries == nil {
		return nil
	}
	completed := make([]acp.PlanEntry, len(entries))
	copy(completed, entries)
	for idx := range completed {
		completed[idx].Status = acp.PlanEntryStatusCompleted
	}
	return completed
}

func stringPtr(value string) *string {
	return &value
}

func toolCallStatusPtr(value acp.ToolCallStatus) *acp.ToolCallStatus {
	return &value
}

func findPendingPermission(requestID string, items []tools.PendingPermission) (tools.PendingPermission, bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID != "" {
		for _, item := range items {
			if strings.TrimSpace(item.ID) == requestID {
				return item, true
			}
		}
	}
	if len(items) > 0 {
		return items[0], true
	}
	return tools.PendingPermission{}, false
}

func toolCallTitle(name string, input map[string]interface{}) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	switch name {
	case "bash", "background_run":
		if command := stringFromAny(input["command"]); command != "" {
			return name + ": " + truncateString(command, 120)
		}
	case "read_file", "write_file", "edit_file", "attach_file":
		if path := stringFromAny(input["path"]); path != "" {
			return name + ": " + truncateString(path, 120)
		}
	case "web_search":
		if query := stringFromAny(input["query"]); query != "" {
			return name + ": " + truncateString(query, 120)
		}
	case "web_fetch", "browser":
		if url := stringFromAny(input["url"]); url != "" {
			return name + ": " + truncateString(url, 120)
		}
	}
	return name
}

func toolKind(name string) acp.ToolKind {
	switch strings.TrimSpace(name) {
	case "read_file", "attach_file":
		return acp.ToolKindRead
	case "write_file", "edit_file":
		return acp.ToolKindEdit
	case "glob", "web_search":
		return acp.ToolKindSearch
	case "web_fetch", "browser":
		return acp.ToolKindFetch
	case "bash", "background_run":
		return acp.ToolKindExecute
	default:
		return acp.ToolKindOther
	}
}

func todoToolTitle(payload events.TodoListPayload) string {
	return "Todo list updated · " + payload.Summary()
}

func todoRawOutput(payload events.TodoListPayload) map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(payload.Items))
	for _, item := range payload.Items {
		items = append(items, map[string]interface{}{
			"id":          item.ID,
			"content":     item.Content,
			"status":      item.Status,
			"active_form": item.ActiveForm,
		})
	}
	return map[string]interface{}{
		"total":       payload.Total,
		"completed":   payload.Completed,
		"in_progress": payload.InProgress,
		"pending":     payload.Pending,
		"items":       items,
	}
}

func stringFromAny(value interface{}) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func truncateString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit-1] + "…"
}
