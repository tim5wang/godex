package tui

import "strings"

func (m *model) recordInputHistory(input string) {
	input = strings.TrimRight(input, "\n")
	if strings.TrimSpace(input) == "" {
		return
	}
	if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != input {
		m.inputHistory = append(m.inputHistory, input)
	}
	m.inputHistoryIndex = -1
	m.inputDraft = ""
}

func (m *model) handleInputHistory(key string) bool {
	if key != "up" && key != "down" && key != "ctrl+p" && key != "ctrl+n" {
		return false
	}
	if strings.Contains(m.composer.Value(), "\n") {
		return false
	}
	if len(m.inputHistory) == 0 {
		return false
	}
	if (key == "up" || key == "down") && m.inputHistoryIndex == -1 && strings.TrimSpace(m.composer.Value()) == "" {
		return false
	}

	switch key {
	case "up", "ctrl+p":
		if m.inputHistoryIndex == -1 {
			m.inputDraft = m.composer.Value()
			m.inputHistoryIndex = len(m.inputHistory) - 1
		} else if m.inputHistoryIndex > 0 {
			m.inputHistoryIndex--
		}
		m.composer.SetValue(m.inputHistory[m.inputHistoryIndex])
		return true
	case "down", "ctrl+n":
		if m.inputHistoryIndex == -1 {
			return false
		}
		if m.inputHistoryIndex < len(m.inputHistory)-1 {
			m.inputHistoryIndex++
			m.composer.SetValue(m.inputHistory[m.inputHistoryIndex])
			return true
		}
		m.inputHistoryIndex = -1
		m.composer.SetValue(m.inputDraft)
		m.inputDraft = ""
		return true
	default:
		return false
	}
}

func (m *model) resetInputHistoryNavigation() {
	m.inputHistoryIndex = -1
	m.inputDraft = ""
}
