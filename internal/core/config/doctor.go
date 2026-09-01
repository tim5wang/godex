package config

import "strings"

// Doctor runs configuration diagnostics against the on-disk and effective config.
func (m *Manager) Doctor() DoctorReport {
	run := newDoctorRun(m.doctorSnapshot())
	run.checkConfigFiles()
	run.checkModelAndSkills()
	run.checkAutomationAndMedia()
	run.checkChannels()
	run.checkWebTools()
	run.checkPermissionsAndFilesystem()
	run.checkOriginsAndStorage()
	return run.finish()
}

func likelyVisionModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	for _, token := range []string{"vision", "kimi-k2.5", "gpt-4.1", "gpt-4o", "claude-3", "gemini"} {
		if strings.Contains(model, token) {
			return true
		}
	}
	return false
}
