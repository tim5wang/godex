package config

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tim5wang/godex/internal/platform/fsutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NewManager creates a config manager rooted at the project workspace and a
// global Godex home. The home config is the stable write target; project
// godex.yaml/.env files remain readable compatibility layers.
func NewManager(opts Options) (*Manager, error) {
	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		projectDir = strings.TrimSpace(opts.WorkspaceDir)
	}
	if projectDir == "" {
		projectDir = strings.TrimSpace(os.Getenv("GODEX_PROJECT_DIR"))
	}
	if projectDir == "" {
		projectDir = defaultWorkspaceDir()
	}
	var err error
	projectDir, err = filepath.Abs(projectDir)
	if err != nil {
		return nil, err
	}

	homeDir := strings.TrimSpace(opts.HomeDir)
	if homeDir == "" {
		homeDir = strings.TrimSpace(os.Getenv("GODEX_HOME"))
	}
	if homeDir == "" {
		homeDir = defaultHomeDir()
	}
	homeDir, err = filepath.Abs(homeDir)
	if err != nil {
		return nil, err
	}

	homeConfigPath := defaultConfigPath(homeDir)
	projectConfigPath := strings.TrimSpace(opts.ConfigPath)
	if projectConfigPath == "" {
		projectConfigPath = strings.TrimSpace(os.Getenv("GODEX_CONFIG"))
	}
	if projectConfigPath == "" {
		projectConfigPath = defaultConfigPath(projectDir)
	}
	if !filepath.IsAbs(projectConfigPath) {
		projectConfigPath = filepath.Join(projectDir, projectConfigPath)
	}

	homeEnvPath := defaultEnvPath(homeDir)
	projectEnvPath := strings.TrimSpace(opts.EnvFile)
	if projectEnvPath == "" {
		projectEnvPath = defaultEnvPath(projectDir)
	}
	if !filepath.IsAbs(projectEnvPath) {
		projectEnvPath = filepath.Join(projectDir, projectEnvPath)
	}

	manager := &Manager{
		workspace:         projectDir,
		homeDir:           homeDir,
		projectDir:        projectDir,
		configPath:        homeConfigPath,
		envPath:           homeEnvPath,
		homeConfigPath:    homeConfigPath,
		projectConfigPath: filepath.Clean(projectConfigPath),
		homeEnvPath:       homeEnvPath,
		projectEnvPath:    filepath.Clean(projectEnvPath),
		schema:            baseSchema(),
	}
	if err := manager.reload(true); err != nil {
		return nil, err
	}
	return manager, nil
}

// SetApplier installs the live-apply callback used after saving YAML.
func (m *Manager) SetApplier(applier ApplyFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applier = applier
}

// SetDoctorAugmenter installs an optional report augmenter used to append
// runtime diagnostics, such as channel status, onto the base config doctor output.
func (m *Manager) SetDoctorAugmenter(augmenter func(DoctorReport) DoctorReport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.doctorAugmenter = augmenter
}

// RegisterSectionSchema appends one dynamic config section, such as channel settings.
func (m *Manager) RegisterSectionSchema(section SectionSchema) {
	if section.ID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for idx := range m.schema {
		if m.schema[idx].ID == section.ID {
			m.schema[idx] = cloneSectionSchema(section)
			return
		}
	}
	m.schema = append(m.schema, cloneSectionSchema(section))
}

// Current returns the current effective config snapshot.
func (m *Manager) Current() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current.Clone()
}

// Meta returns config storage metadata.
func (m *Manager) Meta() Meta {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Meta{
		FilePath:          m.configPath,
		EnvFile:           m.envPath,
		HomeDir:           m.homeDir,
		ProjectDir:        m.projectDir,
		HomeConfigFile:    m.homeConfigPath,
		ProjectConfigFile: m.projectConfigPath,
		HomeEnvFile:       m.homeEnvPath,
		ProjectEnvFile:    m.projectEnvPath,
		Revision:          m.revision,
		LastApply:         cloneApplyReport(m.lastApply),
	}
}

// Schema returns a copy of the editor schema.
func (m *Manager) Schema() []SectionSchema {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SectionSchema, 0, len(m.schema))
	for _, section := range m.schema {
		out = append(out, cloneSectionSchema(section))
	}
	return out
}

// View returns the masked config editor payload.
func (m *Manager) View() View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.viewLocked()
}

// Reveal returns the effective secret value for one field.
func (m *Manager) Reveal(path string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch strings.TrimSpace(path) {
	case "api.providers":
		data, err := json.MarshalIndent(m.stored.API.Providers, "", "  ")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	case "web.token":
		return m.current.WebToken, nil
	case "channels.feishu.app_id":
		return m.current.Feishu.AppID, nil
	case "channels.feishu.app_secret":
		return m.current.Feishu.AppSecret, nil
	case "tools.web_search.brave.api_key":
		return m.current.Tools.WebSearch.BraveAPIKey, nil
	case "tools.web_search.exa.api_key":
		return m.current.Tools.WebSearch.ExaAPIKey, nil
	case "tools.web_search.tavily.api_key":
		return m.current.Tools.WebSearch.TavilyAPIKey, nil
	case "tools.web_search.serpapi.api_key":
		return m.current.Tools.WebSearch.SerpAPIKey, nil
	case "media.moonshot.api_key":
		return m.current.Media.Moonshot.APIKey, nil
	default:
		return "", fmt.Errorf("secret field not found: %s", path)
	}
}

// WriteHomeEnvVar writes or replaces one secret in the global home .env file.
func (m *Manager) WriteHomeEnvVar(key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("missing env var name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing := ""
	if data, err := os.ReadFile(m.homeEnvPath); err == nil {
		existing = string(data)
	} else if !os.IsNotExist(err) {
		return err
	}
	next := updateEnvVar(existing, key, value)
	if err := os.MkdirAll(filepath.Dir(m.homeEnvPath), 0700); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(m.homeEnvPath, []byte(next), 0600); err != nil {
		return err
	}
	_ = os.Setenv(key, value)
	return nil
}

// RemoveHomeEnvVar removes one secret from the global home .env file.
func (m *Manager) RemoveHomeEnvVar(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("missing env var name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing := ""
	if data, err := os.ReadFile(m.homeEnvPath); err == nil {
		existing = string(data)
	} else if os.IsNotExist(err) {
		_ = os.Unsetenv(key)
		return nil
	} else {
		return err
	}
	next := removeEnvVar(existing, key)
	if err := os.MkdirAll(filepath.Dir(m.homeEnvPath), 0700); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(m.homeEnvPath, []byte(next), 0600); err != nil {
		return err
	}
	_ = os.Unsetenv(key)
	return nil
}

// Update saves the new YAML view and triggers the configured apply callback.
func (m *Manager) Update(ctx context.Context, req UpdateRequest) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	nextStored := m.stored
	if err := applyStoredValues(&nextStored, req); err != nil {
		return View{}, err
	}

	if err := m.writeConfigFile(nextStored); err != nil {
		m.lastApply = ApplyReport{
			AppliedAt:     now,
			StorageStatus: StorageStatusSaveFailed,
			RuntimeStatus: RuntimeStatusSkipped,
			Message:       "Failed to save configuration to godex.yaml.",
			Errors:        []string{err.Error()},
		}
		return View{}, err
	}

	oldCfg := m.current.Clone()
	effectiveFile, err := m.effectiveConfigFile(nextStored)
	if err != nil {
		m.stored = nextStored
		m.revision++
		m.lastApply = ApplyReport{
			AppliedAt:     now,
			StorageStatus: StorageStatusSaved,
			RuntimeStatus: RuntimeStatusFailed,
			Message:       "Configuration saved, but the runtime could not merge home and project config.",
			Errors:        []string{err.Error()},
		}
		return m.viewLocked(), nil
	}
	nextCfg, nextOrigins, err := m.resolve(effectiveFile)
	if err != nil {
		m.stored = nextStored
		m.revision++
		m.lastApply = ApplyReport{
			AppliedAt:     now,
			StorageStatus: StorageStatusSaved,
			RuntimeStatus: RuntimeStatusFailed,
			Message:       "Configuration saved, but the runtime could not resolve the updated config.",
			Errors:        []string{err.Error()},
		}
		return m.viewLocked(), nil
	}

	report := ApplyReport{
		AppliedAt:     now,
		StorageStatus: StorageStatusSaved,
		RuntimeStatus: RuntimeStatusApplied,
		Message:       "Configuration saved and applied successfully.",
	}
	if m.applier != nil {
		report = normalizeApplyReport(m.applier(ctx, oldCfg, nextCfg.Clone()), ApplyReport{
			AppliedAt:     now,
			StorageStatus: StorageStatusSaved,
			RuntimeStatus: RuntimeStatusApplied,
			Message:       "Configuration applied successfully.",
		})
	} else {
		report = normalizeApplyReport(report, report)
	}

	m.stored = nextStored
	if runtimeApplySucceeded(report.RuntimeStatus) {
		m.current = nextCfg
		m.origins = nextOrigins
	}
	m.revision++
	m.lastApply = report

	return m.viewLocked(), nil
}

// ReloadFromDisk reparses the home/project config layers and applies the
// resulting effective config to the live runtime.
func (m *Manager) ReloadFromDisk(ctx context.Context) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	file, err := parseConfigFile(m.homeConfigPath)
	if err != nil {
		m.lastApply = ApplyReport{
			AppliedAt:     now,
			StorageStatus: StorageStatusSaveFailed,
			RuntimeStatus: RuntimeStatusSkipped,
			Message:       "Failed to reload configuration from disk.",
			Errors:        []string{err.Error()},
		}
		return m.viewLocked(), err
	}
	effectiveFile, err := m.effectiveConfigFile(file)
	if err != nil {
		m.stored = file
		m.revision++
		m.lastApply = ApplyReport{
			AppliedAt:     now,
			StorageStatus: StorageStatusSaved,
			RuntimeStatus: RuntimeStatusFailed,
			Message:       "Configuration reloaded from disk, but the runtime could not merge home and project config.",
			Errors:        []string{err.Error()},
		}
		return m.viewLocked(), nil
	}
	nextCfg, nextOrigins, err := m.resolve(effectiveFile)
	if err != nil {
		m.stored = file
		m.revision++
		m.lastApply = ApplyReport{
			AppliedAt:     now,
			StorageStatus: StorageStatusSaved,
			RuntimeStatus: RuntimeStatusFailed,
			Message:       "Configuration reloaded from disk, but the runtime could not resolve it.",
			Errors:        []string{err.Error()},
		}
		return m.viewLocked(), nil
	}

	oldCfg := m.current.Clone()
	report := ApplyReport{
		AppliedAt:     now,
		StorageStatus: StorageStatusSaved,
		RuntimeStatus: RuntimeStatusApplied,
		Message:       "Configuration reloaded from disk and applied successfully.",
	}
	if m.applier != nil {
		report = normalizeApplyReport(m.applier(ctx, oldCfg, nextCfg.Clone()), ApplyReport{
			AppliedAt:     now,
			StorageStatus: StorageStatusSaved,
			RuntimeStatus: RuntimeStatusApplied,
			Message:       "Configuration reloaded from disk and applied successfully.",
		})
	} else {
		report = normalizeApplyReport(report, report)
	}

	m.stored = file
	if runtimeApplySucceeded(report.RuntimeStatus) {
		m.current = nextCfg
		m.origins = nextOrigins
	}
	m.revision++
	m.lastApply = report

	return m.viewLocked(), nil
}
