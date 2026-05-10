package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/tools"
)

func renderPrefixedBlock(text, firstPrefix, nextPrefix string, width int, style lipgloss.Style) []string {
	raw := wrapWithIndent(text, firstPrefix, nextPrefix, width)
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		lines = append(lines, style.Render(line))
	}
	return lines
}

func renderLabeledBlock(label, body string, width int, labelStyle, bodyStyle lipgloss.Style) []string {
	lines := []string{labelStyle.Render(ellipsize(label, width))}
	if strings.TrimSpace(body) == "" {
		return lines
	}
	for _, line := range wrapWithIndent(body, "  ", "  ", width) {
		lines = append(lines, bodyStyle.Render(line))
	}
	return lines
}

func wrapWithIndent(text, firstPrefix, nextPrefix string, width int) []string {
	width = maxInt(4, width)
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	paragraphs := strings.Split(normalized, "\n")
	if len(paragraphs) == 0 {
		return []string{firstPrefix}
	}

	lines := make([]string, 0, len(paragraphs))
	for paragraphIndex, paragraph := range paragraphs {
		prefix := firstPrefix
		if paragraphIndex > 0 {
			prefix = nextPrefix
		}
		if paragraph == "" {
			lines = append(lines, prefix)
			continue
		}

		available := maxInt(1, width-runewidth.StringWidth(prefix))
		wrapped := strings.Split(runewidth.Wrap(paragraph, available), "\n")
		for i, line := range wrapped {
			currentPrefix := prefix
			if i > 0 {
				currentPrefix = nextPrefix
			}
			lines = append(lines, currentPrefix+line)
		}
	}
	if len(lines) == 0 {
		return []string{firstPrefix}
	}
	return lines
}

func toolDetailText(item feedItem) string {
	sections := make([]string, 0, 3)
	if len(item.Input) > 0 {
		sections = append(sections, "Input\n"+formatToolInput(item.Input, true))
	}
	if output := strings.TrimSpace(item.Output); output != "" {
		sections = append(sections, "Output\n"+output)
	}
	if errText := strings.TrimSpace(item.Error); errText != "" {
		sections = append(sections, "Error\n"+errText)
	}
	if len(sections) == 0 && item.Status == "running" {
		sections = append(sections, "Working...")
	}
	return strings.Join(sections, "\n\n")
}

func permissionDetailText(item feedItem) string {
	if item.Permission == nil {
		return "Waiting for approval."
	}
	pending := item.Permission
	request := pending.Request
	sections := make([]string, 0, 8)
	sections = append(sections, "Request\n"+strings.TrimSpace(pending.ID))
	if sessionID := strings.TrimSpace(item.SessionID); sessionID != "" {
		sections = append(sections, "Session\n"+sessionID)
	}
	if toolName := strings.TrimSpace(request.ToolName); toolName != "" {
		sections = append(sections, "Tool\n"+toolName)
	}
	if reason := strings.TrimSpace(pending.Reason); reason != "" {
		sections = append(sections, "Reason\n"+reason)
	}
	if request.Source != "" || request.Sender != "" {
		source := request.Source
		if request.Sender != "" {
			if source != "" {
				source += " · "
			}
			source += request.Sender
		}
		sections = append(sections, "Source\n"+source)
	}
	if command := strings.TrimSpace(request.Command); command != "" {
		sections = append(sections, "Command\n"+command)
	}
	if len(request.Paths) > 0 {
		sections = append(sections, "Paths\n"+strings.Join(request.Paths, "\n"))
	}
	if request.Action != "" || request.Mutation {
		action := request.Action
		if action == "" {
			action = "tool call"
		}
		if request.Mutation {
			action += " · mutation"
		}
		sections = append(sections, "Action\n"+action)
	}
	if len(request.Input) > 0 {
		sections = append(sections, "Input\n"+formatPermissionInput(request.Input, true))
	}
	sections = append(sections, "Shortcuts\na allow once\ns allow session\nx deny")
	return strings.Join(sections, "\n\n")
}

func permissionHeaderSummary(item feedItem) string {
	if item.Permission == nil {
		return strings.TrimSpace(item.Summary)
	}
	pending := item.Permission
	request := pending.Request
	parts := make([]string, 0, 8)
	if id := strings.TrimSpace(pending.ID); id != "" {
		parts = append(parts, "req "+id)
	}
	if sessionID := strings.TrimSpace(item.SessionID); sessionID != "" {
		parts = append(parts, "sess "+sessionID)
	}
	if action := strings.TrimSpace(request.Action); action != "" {
		parts = append(parts, action)
	}
	if command := strings.TrimSpace(request.Command); command != "" {
		parts = append(parts, "cmd "+ellipsize(command, 36))
	} else if len(request.Paths) > 0 {
		parts = append(parts, "path: "+strings.TrimSpace(request.Paths[0]))
	} else if input := permissionInputPreview(request.Input); input != "" {
		parts = append(parts, input)
	} else if summary := strings.TrimSpace(item.Summary); summary != "" {
		parts = append(parts, summary)
	}
	parts = append(parts, "a once", "s session", "x deny")
	return strings.Join(parts, " · ")
}

func persistentOverlayItems(items []feedItem, dropCommands bool) []feedItem {
	filtered := make([]feedItem, 0, len(items))
	for _, item := range items {
		switch item.Kind {
		case feedCommand, feedWarning, feedError:
			if dropCommands && item.Kind == feedCommand {
				continue
			}
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func toolSnapshotID(msgIndex, blockIndex int, block protocol.Block) string {
	if block.ID != "" {
		return "tool:" + block.ID
	}
	return fmt.Sprintf("tool:%d:%d:%s", msgIndex, blockIndex, block.Name)
}

func toolRuntimeKey(turnID string, payload events.ToolCallPayload) string {
	if payload.ID != "" {
		return "tool:" + payload.ID
	}
	return fmt.Sprintf("tool:%s:%s:%s", turnID, payload.Name, formatToolInput(payload.Input, false))
}

func cloneToolInput(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	return protocol.ToolUseBlock("", "", input).Input
}

func formatToolInput(input map[string]interface{}, multiline bool) string {
	if len(input) == 0 {
		return ""
	}
	var (
		data []byte
		err  error
	)
	if multiline {
		data, err = json.MarshalIndent(input, "", "  ")
	} else {
		data, err = json.Marshal(input)
	}
	if err != nil {
		return fmt.Sprint(input)
	}
	return string(data)
}

func formatPermissionInput(input map[string]interface{}, multiline bool) string {
	return formatAnyInput(redactSensitiveValue(input), multiline)
}

func formatAnyInput(input interface{}, multiline bool) string {
	var (
		data []byte
		err  error
	)
	if multiline {
		data, err = json.MarshalIndent(input, "", "  ")
	} else {
		data, err = json.Marshal(input)
	}
	if err != nil {
		return fmt.Sprint(input)
	}
	return string(data)
}

func redactSensitiveValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			if sensitiveKey(key) {
				out[key] = "[redacted]"
				continue
			}
			out[key] = redactSensitiveValue(child)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, child := range typed {
			out[i] = redactSensitiveValue(child)
		}
		return out
	case []string:
		out := make([]interface{}, len(typed))
		for i, child := range typed {
			out[i] = child
		}
		return out
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{"secret", "token", "key", "password"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func permissionInputPreview(input map[string]interface{}) string {
	if len(input) == 0 {
		return ""
	}
	for _, key := range []string{"command", "path", "url", "pattern", "content"} {
		if value, ok := input[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(redactSensitiveValue(map[string]interface{}{key: value}).(map[string]interface{})[key]))
			if text != "" {
				return key + ": " + text
			}
		}
	}
	return "input: " + formatPermissionInput(input, false)
}

func summarizeTool(input map[string]interface{}, output, err string, running bool) string {
	if errText := firstSummaryLine(err); errText != "" {
		return errText
	}
	if outputText := firstSummaryLine(output); outputText != "" {
		return outputText
	}
	if inputText := strings.TrimSpace(formatToolInput(input, false)); inputText != "" {
		return "input: " + inputText
	}
	if running {
		return "working..."
	}
	return "completed"
}

func summarizePendingPermission(pending tools.PendingPermission) string {
	if reason := firstSummaryLine(pending.Reason); reason != "" {
		return reason
	}
	if command := strings.TrimSpace(pending.Request.Command); command != "" {
		return "command: " + command
	}
	if len(pending.Request.Paths) > 0 {
		return "path: " + strings.TrimSpace(pending.Request.Paths[0])
	}
	if action := strings.TrimSpace(pending.Request.Action); action != "" {
		return action
	}
	return "approval required"
}

func formatPermissionResolution(resolution tools.PermissionResolution) (string, string) {
	switch resolution.Decision {
	case tools.PermissionAllow:
		scope := string(resolution.Scope)
		if strings.TrimSpace(scope) == "" {
			scope = "once"
		}
		return "Permission approved", fmt.Sprintf("%s · %s", resolution.Request.ToolName, scope)
	case tools.PermissionDeny:
		body := resolution.Request.ToolName
		if reason := strings.TrimSpace(resolution.Reason); reason != "" {
			if body != "" {
				body += "\n"
			}
			body += reason
		}
		return "Permission denied", strings.TrimSpace(body)
	default:
		return "Permission updated", resolution.Request.ToolName
	}
}

func firstSummaryLine(text string) string {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func renderRule(width int) string {
	if width <= 0 {
		return ""
	}
	return ruleStyle.Render(strings.Repeat("─", width))
}

func shortenPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if path == home {
			return "~"
		}
		prefix := home + string(os.PathSeparator)
		if strings.HasPrefix(path, prefix) {
			return "~" + string(os.PathSeparator) + strings.TrimPrefix(path, prefix)
		}
	}
	return path
}

func ellipsize(text string, width int) string {
	if width <= 0 {
		return ""
	}
	return runewidth.Truncate(text, width, "…")
}

func spanVisible(id string, spans []itemSpan, visibleStart, visibleEnd int) bool {
	for _, span := range spans {
		if span.ID != id {
			continue
		}
		return span.End >= visibleStart && span.Start < visibleEnd
	}
	return false
}

func formatClock(value time.Time) string {
	if value.IsZero() {
		return "n/a"
	}
	return value.Format("15:04:05")
}

func formatElapsed(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	seconds := int(value / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds = seconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes = minutes % 60
	return fmt.Sprintf("%dh%02dm", hours, minutes)
}

var heartbeatFrames = []string{"·", "◐", "◓", "◑", "◒"}

func tickHeartbeat() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return heartbeatTickMsg{}
	})
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func maxInt(values ...int) int {
	best := values[0]
	for _, value := range values[1:] {
		if value > best {
			best = value
		}
	}
	return best
}
