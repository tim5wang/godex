package tui

import (
	"fmt"
	"strings"
)

func (m *model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading TUI..."
	}
	// Recalculate layout to track dynamic composer height.
	m.resize()

	parts := []string{
		m.renderHeader(),
		m.viewport.View(),
		m.renderComposer(),
	}
	return strings.Join(parts, "\n")
}

func (m *model) resize() {
	// Dynamic composer height: 1–10 lines based on content, scroll when >10.
	composerRows := clampInt(m.composerLineCount(), 1, 10)
	m.showRules = m.height >= 12
	m.composer.SetHeight(composerRows)
	m.composer.SetWidth(maxInt(12, m.width-1))

	composerBlockHeight := composerRows + 2
	if m.showRules {
		composerBlockHeight += 2
	}
	m.feedHeight = maxInt(1, m.height-3-composerBlockHeight)

	m.viewport.Width = maxInt(10, m.width)
	m.viewport.Height = m.feedHeight
}

func (m *model) renderHeader() string {
	modelLine := fmt.Sprintf("%s · %s:%s", m.activeModelLabel(), m.locator.Channel, m.locator.Key)
	lines := []string{
		titleStyle.Render(ellipsize(botName, m.width)),
		headerMetaStyle.Render(ellipsize(modelLine, m.width)),
		headerMetaStyle.Render(ellipsize(shortenPath(m.cfg.WorkspaceDir), m.width)),
	}
	return strings.Join(lines, "\n")
}

func (m *model) activeModelLabel() string {
	if m == nil || m.cfg == nil {
		return "model: unknown"
	}
	profileID := strings.TrimSpace(m.snapshot.ModelProfileID)
	scope := "default"
	if profileID == "" {
		profileID = strings.TrimSpace(m.cfg.DefaultProfileID)
	} else {
		scope = "session"
	}

	profile, ok := m.cfg.ModelProfileByID(profileID)
	modelName := strings.TrimSpace(m.cfg.Model)
	displayName := ""
	if ok {
		modelName = strings.TrimSpace(profile.Model)
		displayName = strings.TrimSpace(profile.Name)
		if displayName == profile.ID {
			displayName = ""
		}
	}
	if modelName == "" {
		modelName = "model: unknown"
	}
	if displayName == "" || displayName == modelName {
		return fmt.Sprintf("%s · %s", modelName, scope)
	}
	return fmt.Sprintf("%s · %s · %s", displayName, modelName, scope)
}

func (m *model) renderComposer() string {
	lines := make([]string, 0, 5)
	lines = append(lines, m.renderHeartbeatLine())
	if m.showRules {
		lines = append(lines, renderRule(m.width))
	}
	lines = append(lines, m.composer.View())
	if m.showRules {
		lines = append(lines, renderRule(m.width))
	}
	lines = append(lines, mutedLineStyle.Render(ellipsize(m.status, m.width)))
	return strings.Join(lines, "\n")
}

func (m *model) composerLineCount() int {
	val := m.composer.Value()
	if val == "" {
		return 1
	}
	return strings.Count(val, "\n") + 1
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (m *model) renderHeartbeatLine() string {
	return m.statusStyle().Render(ellipsize(m.renderRuntimeStatus(), m.width))
}
