package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/platform/browserutil"
	"github.com/tim5wang/godex/internal/platform/fsutil"
	"gopkg.in/yaml.v3"
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

// Doctor runs configuration diagnostics against the on-disk and effective config.
func (m *Manager) Doctor() DoctorReport {
	m.mu.RLock()
	configPath := m.configPath
	envPath := m.envPath
	homeDir := m.homeDir
	projectDir := m.projectDir
	homeConfigPath := m.homeConfigPath
	projectConfigPath := m.projectConfigPath
	homeEnvPath := m.homeEnvPath
	projectEnvPath := m.projectEnvPath
	current := m.current.Clone()
	origins := cloneOrigins(m.origins)
	lastApply := cloneApplyReport(m.lastApply)
	doctorAugmenter := m.doctorAugmenter
	m.mu.RUnlock()

	report := DoctorReport{
		GeneratedAt:       time.Now(),
		HomeDir:           homeDir,
		ProjectDir:        projectDir,
		HomeConfigFile:    homeConfigPath,
		ProjectConfigFile: projectConfigPath,
		HomeEnvFile:       homeEnvPath,
		ProjectEnvFile:    projectEnvPath,
		LastApply:         lastApply,
	}
	add := func(check DoctorCheck) {
		report.Checks = append(report.Checks, check)
		switch check.Severity {
		case "error":
			report.Errors++
		case "warning":
			report.Warnings++
		default:
			report.Infos++
		}
	}

	if _, err := parseConfigFile(configPath); err != nil {
		add(DoctorCheck{
			Severity:   "error",
			Code:       "config_parse",
			Path:       configPath,
			Message:    err.Error(),
			Suggestion: "Fix the YAML syntax or unknown keys in godex.yaml.",
		})
	}
	if projectConfigPath != "" && filepath.Clean(projectConfigPath) != filepath.Clean(configPath) {
		if _, err := os.Stat(projectConfigPath); err == nil {
			if _, err := parseConfigFile(projectConfigPath); err != nil {
				add(DoctorCheck{
					Severity:   "error",
					Code:       "project_config_parse",
					Path:       projectConfigPath,
					Message:    err.Error(),
					Suggestion: "Fix the YAML syntax or unknown keys in project godex.yaml.",
				})
			} else {
				add(DoctorCheck{
					Severity:   "warning",
					Code:       "project_config_shadows_home",
					Path:       projectConfigPath,
					Message:    "Project godex.yaml is overriding values from the global home config.",
					Suggestion: "Move stable global settings to the home config or remove project-only overrides you no longer need.",
				})
			}
		} else if err != nil && !os.IsNotExist(err) {
			add(DoctorCheck{
				Severity:   "warning",
				Code:       "project_config_unreadable",
				Path:       projectConfigPath,
				Message:    err.Error(),
				Suggestion: "Fix permissions or remove the unreadable project config path.",
			})
		}
	}

	if strings.TrimSpace(current.DefaultModelProfile().APIKey) == "" {
		add(DoctorCheck{
			Severity:   "error",
			Code:       "api_key_missing",
			Path:       "api.providers",
			Message:    "Default LLM provider credential is not configured.",
			Suggestion: "Set api.providers.<id>.api_key_env and export that environment variable, or set api.providers.<id>.api_key.",
		})
	}
	seenDefaultSkills := map[string]bool{}
	for _, rawName := range current.DefaultSkills {
		name := strings.TrimSpace(rawName)
		if name == "" || seenDefaultSkills[name] {
			continue
		}
		seenDefaultSkills[name] = true
		if _, err := skill.ResolvePath(current.SkillsDir, name); err != nil {
			check := DoctorCheck{
				Severity: "warning",
				Code:     "default_skill_missing",
				Path:     "team.default_skills",
				Message:  fmt.Sprintf("Default skill %q is configured but was not found in %s.", name, current.SkillsDir),
				Suggestion: "Install the skill into the skills directory or remove it from team.default_skills. " +
					"Missing default skills are skipped when sessions start.",
			}
			if !errors.Is(err, skill.ErrSkillNotFound) {
				check.Code = "default_skill_unreadable"
				check.Message = fmt.Sprintf("Default skill %q could not be checked: %v.", name, err)
				check.Suggestion = "Fix skills directory permissions or remove the entry from team.default_skills."
			}
			add(check)
		}
	}
	if likelyVisionModel(current.Model) {
		add(DoctorCheck{
			Severity:   "info",
			Code:       "image_understanding_enabled_candidate",
			Path:       "api.default_model",
			Message:    fmt.Sprintf("Model %q looks compatible with native image input.", current.Model),
			Suggestion: "Image attachments from Web and Feishu will attempt native vision input first and gracefully fall back if the endpoint rejects it.",
		})
	} else {
		add(DoctorCheck{
			Severity:   "info",
			Code:       "image_understanding_uncertain",
			Path:       "api.default_model",
			Message:    fmt.Sprintf("Model %q may not support native image input.", current.Model),
			Suggestion: "Use a vision-capable model such as kimi-k2.5 if you want direct image understanding from attachments.",
		})
	}
	cronJobsDir := filepath.Join(current.StateDir, "cron", "jobs")
	cronJobCount := 0
	if entries, err := os.ReadDir(cronJobsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			cronJobCount++
		}
	}
	if !current.Cron.Enabled && cronJobCount > 0 {
		add(DoctorCheck{
			Severity:   "warning",
			Code:       "cron_jobs_but_scheduler_disabled",
			Path:       "cron.enabled",
			Message:    fmt.Sprintf("Cron scheduler is disabled, but %d cron job(s) are stored locally.", cronJobCount),
			Suggestion: "Set cron.enabled to true or export GODEX_CRON_ENABLED=true before starting `godex serve`.",
		})
	}
	if current.Media.Moonshot.Enabled && strings.TrimSpace(current.Media.Moonshot.APIKey) == "" {
		add(DoctorCheck{
			Severity:   "error",
			Code:       "media_moonshot_api_key_missing",
			Path:       "media.moonshot.api_key",
			Message:    "Moonshot media preprocessing is enabled but api_key is empty.",
			Suggestion: "Set media.moonshot.api_key or GODEX_MEDIA_MOONSHOT_API_KEY.",
		})
	}
	if current.Media.Moonshot.Enabled && strings.TrimSpace(current.Media.Moonshot.BaseURL) == "" {
		add(DoctorCheck{
			Severity:   "error",
			Code:       "media_moonshot_base_url_missing",
			Path:       "media.moonshot.base_url",
			Message:    "Moonshot media preprocessing is enabled but base_url is empty.",
			Suggestion: "Set media.moonshot.base_url to a Moonshot OpenAI-compatible base such as https://api.moonshot.ai/v1.",
		})
	}
	if !current.Media.Moonshot.Enabled {
		add(DoctorCheck{
			Severity:   "info",
			Code:       "media_moonshot_disabled",
			Path:       "media.moonshot.enabled",
			Message:    "Moonshot media preprocessing is disabled; document OCR/file-extract will rely on local fallbacks only.",
			Suggestion: "Enable media.moonshot.enabled and provide a Moonshot API key if you want official file extraction and OCR.",
		})
	}
	if strings.TrimSpace(current.Media.Document.PDFToTextPath) != "" && !commandAvailable(current.Media.Document.PDFToTextPath) && !current.Media.Moonshot.Enabled {
		add(DoctorCheck{
			Severity:   "warning",
			Code:       "pdftotext_missing",
			Path:       "media.document.pdftotext_path",
			Message:    "pdftotext was not found; local PDF text extraction will be unavailable.",
			Suggestion: "Install poppler pdftotext or enable Moonshot media preprocessing.",
		})
	}
	if strings.EqualFold(strings.TrimSpace(current.Media.OCR.Mode), "tesseract") || (strings.EqualFold(strings.TrimSpace(current.Media.OCR.Mode), "auto") && !current.Media.Moonshot.Enabled) {
		if strings.TrimSpace(current.Media.OCR.TesseractPath) == "" || !commandAvailable(current.Media.OCR.TesseractPath) {
			add(DoctorCheck{
				Severity:   "warning",
				Code:       "tesseract_missing",
				Path:       "media.ocr.tesseract_path",
				Message:    "tesseract was not found; local OCR fallback will be unavailable.",
				Suggestion: "Install tesseract or enable Moonshot media preprocessing for OCR.",
			})
		}
	}
	if current.Media.Audio.Enabled {
		for _, item := range []struct {
			path    string
			binary  string
			code    string
			message string
		}{
			{path: "media.audio.ffmpeg_path", binary: current.Media.Audio.FFmpegPath, code: "ffmpeg_missing", message: "ffmpeg was not found; audio/video preprocessing cannot decode media."},
			{path: "media.audio.ffprobe_path", binary: current.Media.Audio.FFprobePath, code: "ffprobe_missing", message: "ffprobe was not found; media duration and stream inspection will be unavailable."},
			{path: "media.audio.whisper_cpp_path", binary: current.Media.Audio.WhisperCPPPath, code: "whisper_cpp_missing", message: "whisper.cpp CLI was not found; audio transcription is unavailable."},
		} {
			if strings.TrimSpace(item.binary) == "" || !commandAvailable(item.binary) {
				add(DoctorCheck{
					Severity:   "warning",
					Code:       item.code,
					Path:       item.path,
					Message:    item.message,
					Suggestion: "Install the required binary or disable audio/video preprocessing.",
				})
			}
		}
		if strings.TrimSpace(current.Media.Audio.WhisperModelPath) == "" {
			add(DoctorCheck{
				Severity:   "warning",
				Code:       "whisper_model_missing",
				Path:       "media.audio.whisper_model_path",
				Message:    "whisper.cpp model path is empty; audio transcription is unavailable.",
				Suggestion: "Point media.audio.whisper_model_path at a local whisper.cpp model file.",
			})
		} else if _, err := os.Stat(current.Media.Audio.WhisperModelPath); err != nil {
			add(DoctorCheck{
				Severity:   "warning",
				Code:       "whisper_model_not_found",
				Path:       "media.audio.whisper_model_path",
				Message:    fmt.Sprintf("whisper.cpp model file was not found at %s.", current.Media.Audio.WhisperModelPath),
				Suggestion: "Download a whisper.cpp model and update media.audio.whisper_model_path.",
			})
		}
	}
	if current.Media.Video.Enabled {
		add(DoctorCheck{
			Severity:   "info",
			Code:       "video_summary_local_pipeline",
			Path:       "media.video",
			Message:    "Video summaries will use transcript + keyframes because the main chat provider stays single-provider in v1.",
			Suggestion: "This is expected until Godex supports native video requests on the primary chat provider path.",
		})
	}
	if current.Feishu.Enabled && strings.TrimSpace(current.Feishu.AppID) == "" {
		add(DoctorCheck{
			Severity:   "error",
			Code:       "feishu_app_id_missing",
			Path:       "channels.feishu.app_id",
			Message:    "Feishu is enabled but app_id is empty.",
			Suggestion: "Set channels.feishu.app_id or FEISHU_APP_ID.",
		})
	}
	if current.Feishu.Enabled && strings.TrimSpace(current.Feishu.AppSecret) == "" {
		add(DoctorCheck{
			Severity:   "error",
			Code:       "feishu_secret_missing",
			Path:       "channels.feishu.app_secret",
			Message:    "Feishu is enabled but app_secret is empty.",
			Suggestion: "Set channels.feishu.app_secret or FEISHU_APP_SECRET.",
		})
	}
	weixinStateDir := filepath.Join(current.StateDir, "channels", "weixin", defaultStringValue(strings.TrimSpace(current.Weixin.AccountID), "default"))
	accountPath := filepath.Join(weixinStateDir, "account.json")
	accountData, accountReadErr := os.ReadFile(accountPath)
	if !current.Weixin.Enabled {
		if accountReadErr == nil && bytes.Contains(accountData, []byte("bot_token")) {
			add(DoctorCheck{
				Severity:   "info",
				Code:       "weixin_setup_but_disabled",
				Path:       "channels.weixin.enabled",
				Message:    "Weixin login state exists, but the channel is disabled in config.",
				Suggestion: "Set channels.weixin.enabled to true or export WEIXIN_ENABLED=true before starting `godex serve`.",
			})
		}
	}
	if current.Weixin.Enabled {
		if strings.TrimSpace(current.Weixin.BaseURL) == "" {
			add(DoctorCheck{
				Severity:   "error",
				Code:       "weixin_base_url_missing",
				Path:       "channels.weixin.base_url",
				Message:    "Weixin is enabled but base_url is empty.",
				Suggestion: "Set channels.weixin.base_url or WEIXIN_BASE_URL.",
			})
		} else if parsed, err := url.Parse(strings.TrimSpace(current.Weixin.BaseURL)); err != nil || parsed.Scheme == "" || parsed.Host == "" {
			add(DoctorCheck{
				Severity:   "error",
				Code:       "weixin_base_url_invalid",
				Path:       "channels.weixin.base_url",
				Message:    fmt.Sprintf("Weixin base_url %q is invalid.", current.Weixin.BaseURL),
				Suggestion: "Use a full HTTP(S) URL such as https://ilinkai.weixin.qq.com.",
			})
		}
		if len(current.Weixin.AllowFrom) == 0 {
			add(DoctorCheck{
				Severity:   "warning",
				Code:       "weixin_allow_from_empty",
				Path:       "channels.weixin.allow_from",
				Message:    "Weixin allow_from is empty, so any sender who can reach the bot may use it.",
				Suggestion: "Set channels.weixin.allow_from to one or more ilink user IDs for safer operation.",
			})
		}
		if err := ensureParentWritable(filepath.Join(weixinStateDir, "account.json")); err != nil {
			add(DoctorCheck{
				Severity:   "warning",
				Code:       "weixin_state_not_writable",
				Path:       "channels.weixin.account_id",
				Message:    fmt.Sprintf("Weixin state directory is not writable: %v", err),
				Suggestion: "Fix permissions for the Godex state directory before running `godex weixin setup`.",
			})
		}
		if accountReadErr != nil {
			if os.IsNotExist(accountReadErr) {
				add(DoctorCheck{
					Severity:   "warning",
					Code:       "weixin_not_setup",
					Path:       "channels.weixin",
					Message:    "Weixin is enabled, but no login state was found for the configured account.",
					Suggestion: "Run `godex weixin setup` to complete QR-code login for this account.",
				})
			} else {
				add(DoctorCheck{
					Severity:   "warning",
					Code:       "weixin_state_unreadable",
					Path:       "channels.weixin",
					Message:    fmt.Sprintf("Weixin account state could not be read: %v", accountReadErr),
					Suggestion: "Fix the state directory or run `godex weixin logout` and `godex weixin setup` again.",
				})
			}
		} else if !bytes.Contains(accountData, []byte("bot_token")) {
			add(DoctorCheck{
				Severity:   "warning",
				Code:       "weixin_state_incomplete",
				Path:       "channels.weixin",
				Message:    "Weixin account state exists but does not contain a bot token.",
				Suggestion: "Run `godex weixin setup` again to refresh the stored login state.",
			})
		}
	}
	if current.Tools.WebSearch.Enabled &&
		strings.TrimSpace(current.Tools.WebSearch.BraveAPIKey) == "" &&
		strings.TrimSpace(current.Tools.WebSearch.ExaAPIKey) == "" &&
		strings.TrimSpace(current.Tools.WebSearch.TavilyAPIKey) == "" &&
		!webSearchBrowserProviderEnabled(current) {
		add(DoctorCheck{
			Severity:   "warning",
			Code:       "web_search_fallback_only",
			Path:       "tools.web_search",
			Message:    "web_search is enabled, but only the fallback provider is configured.",
			Suggestion: "Add one of the provider API keys for higher quality current-information search.",
		})
	}
	if current.Tools.WebFetch.Enabled && strings.EqualFold(strings.TrimSpace(current.Tools.WebFetch.Policy), "allowlist") && len(current.Tools.WebFetch.AllowedDomains) == 0 {
		add(DoctorCheck{
			Severity:   "error",
			Code:       "web_fetch_empty_allowlist",
			Path:       "tools.web_fetch.allowed_domains",
			Message:    "web_fetch allowlist mode is enabled, but allowed_domains is empty.",
			Suggestion: "Add allowed domains or switch tools.web_fetch.policy back to allow_all.",
		})
	}
	domainPatternChecks := []struct {
		path     string
		patterns []string
	}{
		{path: "tools.web_fetch.allowed_domains", patterns: current.Tools.WebFetch.AllowedDomains},
		{path: "tools.web_fetch.blocked_domains", patterns: current.Tools.WebFetch.BlockedDomains},
		{path: "tools.web_search.browser.preferred_hosts", patterns: current.Tools.WebSearch.Browser.PreferredHosts},
	}
	for engine, engineCfg := range current.Tools.WebSearch.Browser.Engines {
		domainPatternChecks = append(domainPatternChecks, struct {
			path     string
			patterns []string
		}{
			path:     "tools.web_search.browser.engines." + engine + ".blocked_hosts",
			patterns: engineCfg.BlockedHosts,
		})
	}
	for _, item := range domainPatternChecks {
		for _, pattern := range item.patterns {
			if err := validateDomainPattern(pattern); err != nil {
				code := "web_fetch_bad_domain_pattern"
				if strings.HasPrefix(item.path, "tools.web_search.browser.") {
					code = "web_search_browser_bad_domain_pattern"
				}
				add(DoctorCheck{
					Severity:   "error",
					Code:       code,
					Path:       item.path,
					Message:    fmt.Sprintf("Invalid domain pattern %q: %v", pattern, err),
					Suggestion: "Use exact domains like example.com or wildcard prefixes like *.example.com.",
				})
			}
		}
	}
	if current.Tools.WebFetch.Enabled && current.Tools.WebFetch.AllowPrivateHosts {
		add(DoctorCheck{
			Severity:   "warning",
			Code:       "web_fetch_private_hosts_enabled",
			Path:       "tools.web_fetch.allow_private_hosts",
			Message:    "web_fetch private host access is enabled.",
			Suggestion: "Disable this unless you intentionally want the agent to reach localhost or private networks.",
		})
	}
	if current.Tools.Browser.Enabled && current.Tools.Browser.AllowPrivateHosts {
		add(DoctorCheck{
			Severity:   "warning",
			Code:       "browser_private_hosts_enabled",
			Path:       "tools.browser.allow_private_hosts",
			Message:    "browser private host access is enabled.",
			Suggestion: "Disable this unless browser automation must reach localhost or private networks.",
		})
	}
	if current.Tools.Browser.Enabled && strings.TrimSpace(current.Tools.Browser.BrowserPath) != "" && !commandAvailable(current.Tools.Browser.BrowserPath) {
		add(DoctorCheck{
			Severity:   "warning",
			Code:       "browser_path_missing",
			Path:       "tools.browser.browser_path",
			Message:    fmt.Sprintf("Configured browser path %q was not found.", current.Tools.Browser.BrowserPath),
			Suggestion: "Point browser_path at a valid local Chrome/Chromium binary or clear it and use cdp_url instead.",
		})
	}
	if current.Tools.Browser.Enabled && strings.TrimSpace(current.Tools.Browser.CDPURL) == "" && strings.TrimSpace(current.Tools.Browser.BrowserPath) == "" {
		if !probablyHasLocalBrowser() {
			add(DoctorCheck{
				Severity:   "warning",
				Code:       "browser_local_runtime_missing",
				Path:       "tools.browser",
				Message:    "browser is enabled, but no obvious local Chrome/Chromium runtime was found and both browser_path and cdp_url are empty.",
				Suggestion: "Install a local Chrome-compatible browser, configure tools.browser.browser_path, or configure tools.browser.cdp_url.",
			})
		}
	}
	if !current.Tools.Permissions.InteractiveApprovalEnabled {
		add(DoctorCheck{
			Severity:   "warning",
			Code:       "permissions_interactive_approval_disabled",
			Path:       "tools.permissions.interactive_approval_enabled",
			Message:    "interactive approval is disabled for remote tool calls.",
			Suggestion: "Enable interactive approvals unless every remote session source is fully trusted.",
		})
	}
	if current.Tools.Permissions.InteractiveApprovalEnabled && strings.EqualFold(strings.TrimSpace(current.Tools.Permissions.InteractiveApprovalMode), "yolo") {
		add(DoctorCheck{
			Severity:   "warning",
			Code:       "permissions_interactive_approval_yolo_mode",
			Path:       "tools.permissions.interactive_approval_mode",
			Message:    "interactive approval is in yolo mode and will auto-approve protected remote tool calls.",
			Suggestion: "Use manual mode unless remote sessions are fully trusted and you intentionally want hands-off execution.",
		})
	}
	for _, prefix := range current.Tools.Permissions.TrustedPathPrefixes {
		if prefix == "." || prefix == "/" {
			add(DoctorCheck{
				Severity:   "warning",
				Code:       "permissions_trusted_path_prefix_broad",
				Path:       "tools.permissions.trusted_path_prefixes",
				Message:    fmt.Sprintf("Trusted path prefix %q is very broad and may bypass approvals for most file mutations.", prefix),
				Suggestion: "Prefer a narrower subtree such as notes/, .godex/tmp/, or skills/sandbox/.",
			})
		}
	}

	if current.Logging.FilePath != "" {
		dir := filepath.Dir(current.Logging.FilePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			add(DoctorCheck{
				Severity:   "error",
				Code:       "log_dir_unwritable",
				Path:       "logging.file_path",
				Message:    fmt.Sprintf("Log directory %s cannot be created: %v", dir, err),
				Suggestion: "Choose a writable logging.file_path.",
			})
		} else if file, err := os.OpenFile(current.Logging.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err != nil {
			add(DoctorCheck{
				Severity:   "error",
				Code:       "log_file_unwritable",
				Path:       "logging.file_path",
				Message:    fmt.Sprintf("Log file %s cannot be opened: %v", current.Logging.FilePath, err),
				Suggestion: "Choose a writable logging.file_path.",
			})
		} else {
			_ = file.Close()
		}
	}

	for _, path := range []struct {
		field string
		dir   string
	}{
		{field: "paths.state_dir", dir: current.StateDir},
		{field: "paths.team_dir", dir: current.TeamDir},
		{field: "paths.tasks_dir", dir: current.TasksDir},
		{field: "paths.todos_dir", dir: current.TodosDir},
		{field: "paths.memory_dir", dir: current.MemoryDir},
		{field: "paths.rules_dir", dir: current.RulesDir},
		{field: "paths.skills_dir", dir: current.SkillsDir},
		{field: "paths.temp_dir", dir: current.TempDir},
		{field: "paths.transcripts_dir", dir: current.TranscriptsDir},
		{field: "paths.sessions_dir", dir: current.SessionsDir},
	} {
		if err := os.MkdirAll(path.dir, 0755); err != nil {
			add(DoctorCheck{
				Severity:   "error",
				Code:       "path_unwritable",
				Path:       path.field,
				Message:    fmt.Sprintf("Directory %s cannot be created: %v", path.dir, err),
				Suggestion: "Choose a writable path or fix filesystem permissions.",
			})
		}
	}

	for _, file := range []struct {
		path   string
		secret bool
	}{
		{path: configPath, secret: fileContainsSecret(current, configPath)},
		{path: projectConfigPath, secret: fileContainsSecret(current, projectConfigPath)},
		{path: envPath, secret: fileContainsSecret(current, envPath)},
		{path: projectEnvPath, secret: fileContainsSecret(current, projectEnvPath)},
	} {
		if strings.TrimSpace(file.path) == "" {
			continue
		}
		if !file.secret {
			continue
		}
		if info, err := os.Stat(file.path); err == nil {
			if info.Mode().Perm()&0077 != 0 {
				add(DoctorCheck{
					Severity:   "warning",
					Code:       "secret_file_permissions",
					Path:       file.path,
					Message:    fmt.Sprintf("Secret-bearing file %s is too permissive (%#o).", file.path, info.Mode().Perm()),
					Suggestion: "Run chmod 600 on the file.",
				})
			}
		}
	}

	for path, origin := range origins {
		if origin.OverriddenBy != "" {
			message := fmt.Sprintf("Value is overridden by %s.", origin.OverriddenBy)
			if origin.UsedEnv != "" {
				message = fmt.Sprintf("Value is overridden by %s (%s).", origin.OverriddenBy, origin.UsedEnv)
			}
			add(DoctorCheck{
				Severity:   "info",
				Code:       "field_shadowed",
				Path:       path,
				Message:    message,
				Suggestion: "Edit the higher-priority source if you want runtime changes to take effect.",
			})
		}
		if origin.UsedEnv != "" && origin.CanonicalEnv != "" && origin.UsedEnv != origin.CanonicalEnv {
			add(DoctorCheck{
				Severity:   "warning",
				Code:       "deprecated_env_alias",
				Path:       path,
				Message:    fmt.Sprintf("Using deprecated environment alias %s.", origin.UsedEnv),
				Suggestion: fmt.Sprintf("Switch to %s.", origin.CanonicalEnv),
			})
		}
	}

	if bytes := configDirSize(filepath.Join(current.StateDir, "subagents")); bytes > 0 {
		add(DoctorCheck{
			Severity:   "info",
			Code:       "subagent_workspace_storage",
			Path:       filepath.Join(current.StateDir, "subagents"),
			Message:    fmt.Sprintf("Subagent workspace storage is using %.1f MB.", float64(bytes)/(1024*1024)),
			Suggestion: "Run `godex gc subagents --dry-run` to inspect cleanable workspaces.",
		})
	}
	add(DoctorCheck{
		Severity: "info",
		Code:     "agent_profile_defaults",
		Path:     "agent.default_profiles",
		Message: fmt.Sprintf("Agent profile defaults: acp=%s, cli=%s, tui=%s, web=%s, weixin=%s, feishu=%s.",
			current.DefaultAgentProfileForChannel("acp"),
			current.DefaultAgentProfileForChannel("cli"),
			current.DefaultAgentProfileForChannel("tui"),
			current.DefaultAgentProfileForChannel("web"),
			current.DefaultAgentProfileForChannel("weixin"),
			current.DefaultAgentProfileForChannel("feishu")),
		Suggestion: "Use agent.profile or agent.default_profiles.* to tune default tool exposure by entrypoint.",
	})

	if len(report.Checks) == 0 {
		add(DoctorCheck{
			Severity: "info",
			Code:     "config_ok",
			Message:  "Configuration looks healthy.",
		})
	}
	sort.Slice(report.Checks, func(i, j int) bool {
		return report.Checks[i].Code < report.Checks[j].Code
	})
	if doctorAugmenter != nil {
		report = doctorAugmenter(report)
	}
	return report
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

func (m *Manager) reload(createIfMissing bool) error {
	if createIfMissing {
		if err := os.MkdirAll(filepath.Dir(m.homeConfigPath), 0755); err != nil {
			return err
		}
		if _, err := os.Stat(m.homeConfigPath); os.IsNotExist(err) {
			defaults := defaultConfigFile()
			if err := m.writeConfigFile(defaults); err != nil {
				return err
			}
		}
	}

	file, err := parseConfigFile(m.homeConfigPath)
	if err != nil {
		return err
	}
	effectiveFile, err := m.effectiveConfigFile(file)
	if err != nil {
		return err
	}
	cfg, origins, err := m.resolve(effectiveFile)
	if err != nil {
		return err
	}
	m.stored = file
	m.current = cfg
	m.origins = origins
	m.revision++
	if m.lastApply.StorageStatus == "" && m.lastApply.RuntimeStatus == "" {
		m.lastApply = ApplyReport{
			AppliedAt:     time.Now(),
			StorageStatus: StorageStatusSaved,
			RuntimeStatus: RuntimeStatusApplied,
			Message:       "Loaded configuration from disk.",
		}
	}
	return nil
}

func (m *Manager) resolve(file ConfigFile) (*Config, map[string]fieldOrigin, error) {
	homeDotenvMap, err := readDotEnvFile(m.homeEnvPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	projectDotenvMap, err := readDotEnvFile(m.projectEnvPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	dotenvMap := mergeEnvMaps(homeDotenvMap, projectDotenvMap)
	origins := make(map[string]fieldOrigin)
	current := resolveConfigFile(file, m.homeDir, m.projectDir, m.configPath, m.envPath, m.homeConfigPath, m.projectConfigPath, m.homeEnvPath, m.projectEnvPath)

	resolveString := func(path string, yamlValue string, canonicalEnv string, apply func(string)) {
		origin := fieldOrigin{
			Source:       SourceYAML,
			CanonicalEnv: canonicalEnv,
			YAMLValue:    yamlValue,
			Effective:    yamlValue,
		}
		value := yamlValue
		if dot := lookupEnvValue(dotenvMap, canonicalEnv); dot.Set {
			value = dot.Value
			origin.Source = SourceDotEnv
			origin.OverriddenBy = SourceDotEnv
			origin.DotEnvValue = dot.Value
			origin.UsedEnv = dot.Name
			origin.Effective = dot.Value
		}
		if env := lookupProcessValue(canonicalEnv); env.Set {
			value = env.Value
			origin.Source = SourceEnv
			origin.OverriddenBy = SourceEnv
			origin.EnvValue = env.Value
			origin.UsedEnv = env.Name
			origin.Effective = env.Value
		}
		origins[path] = origin
		apply(value)
	}

	resolveBool := func(path string, yamlValue bool, canonicalEnv string, apply func(bool)) {
		origin := fieldOrigin{
			Source:       SourceYAML,
			CanonicalEnv: canonicalEnv,
			YAMLValue:    yamlValue,
			Effective:    yamlValue,
		}
		value := yamlValue
		if dot, ok := lookupBool(dotenvMap, canonicalEnv); ok {
			value = dot
			origin.Source = SourceDotEnv
			origin.OverriddenBy = SourceDotEnv
			origin.DotEnvValue = dot
			origin.UsedEnv = canonicalEnv
			origin.Effective = dot
		}
		if env, ok := lookupProcessBool(canonicalEnv); ok {
			value = env
			origin.Source = SourceEnv
			origin.OverriddenBy = SourceEnv
			origin.EnvValue = env
			origin.UsedEnv = canonicalEnv
			origin.Effective = env
		}
		origins[path] = origin
		apply(value)
	}

	resolveInt := func(path string, yamlValue int, canonicalEnv string, apply func(int)) {
		origin := fieldOrigin{
			Source:       SourceYAML,
			CanonicalEnv: canonicalEnv,
			YAMLValue:    yamlValue,
			Effective:    yamlValue,
		}
		value := yamlValue
		if dot, ok, name := lookupInt(dotenvMap, canonicalEnv); ok {
			value = dot
			origin.Source = SourceDotEnv
			origin.OverriddenBy = SourceDotEnv
			origin.DotEnvValue = dot
			origin.UsedEnv = name
			origin.Effective = dot
		}
		if env, ok, name := lookupProcessInt(canonicalEnv); ok {
			value = env
			origin.Source = SourceEnv
			origin.OverriddenBy = SourceEnv
			origin.EnvValue = env
			origin.UsedEnv = name
			origin.Effective = env
		}
		origins[path] = origin
		apply(value)
	}

	resolveCSV := func(path string, yamlValue []string, canonicalEnv string, apply func([]string)) {
		origin := fieldOrigin{
			Source:       SourceYAML,
			CanonicalEnv: canonicalEnv,
			YAMLValue:    append([]string{}, yamlValue...),
			Effective:    append([]string{}, yamlValue...),
		}
		value := append([]string{}, yamlValue...)
		if dot, ok := lookupCSV(dotenvMap, canonicalEnv); ok {
			value = dot
			origin.Source = SourceDotEnv
			origin.OverriddenBy = SourceDotEnv
			origin.DotEnvValue = append([]string{}, dot...)
			origin.UsedEnv = canonicalEnv
			origin.Effective = append([]string{}, dot...)
		}
		if env, ok := lookupProcessCSV(canonicalEnv); ok {
			value = env
			origin.Source = SourceEnv
			origin.OverriddenBy = SourceEnv
			origin.EnvValue = append([]string{}, env...)
			origin.UsedEnv = canonicalEnv
			origin.Effective = append([]string{}, env...)
		}
		origins[path] = origin
		apply(value)
	}

	resolveString("api.default_model", file.API.DefaultModel, "GODEX_API_DEFAULT_MODEL", func(v string) { current.DefaultModelRef = v })
	resolveString("api.default_profile", file.API.DefaultProfile, "GODEX_API_DEFAULT_PROFILE", func(v string) { current.DefaultProfileID = v })
	resolveBool("api.auto_fallback_enabled", file.API.AutoFallbackEnabled, "GODEX_API_AUTO_FALLBACK_ENABLED", func(v bool) { current.AutoFallbackEnabled = v })
	resolveInt("api.timeout_seconds", file.API.TimeoutSeconds, "GODEX_API_TIMEOUT_SECONDS", func(v int) { current.APITimeoutSeconds = v })
	current.LLMProviders = llmProvidersFromConfigFile(file)
	applyProviderCredentialEnv(current.LLMProviders, dotenvMap)
	current.LLMStrategy = llmStrategyFromConfigFile(file)
	current.ModelProfiles = modelProfilesFromProviders(file, current.LLMProviders)
	current.DefaultModelRef = strings.TrimSpace(current.DefaultModelRef)
	current.DefaultProfileID = strings.TrimSpace(current.DefaultProfileID)
	if current.DefaultModelRef != "" {
		current.DefaultProfileID = current.DefaultModelRef
	}
	if current.DefaultProfileID == "" {
		current.DefaultProfileID = defaultProfileIDFromProfiles(current.ModelProfiles)
	}
	if _, ok := current.ModelProfiles[current.DefaultProfileID]; !ok {
		current.DefaultProfileID = defaultProfileIDFromProfiles(current.ModelProfiles)
	}
	current.DefaultModelRef = current.DefaultProfileID
	defaultProfile := current.ModelProfiles[current.DefaultProfileID]
	defaultProfile = normalizeModelProfile(current.DefaultProfileID, defaultProfile, current)
	apiDefaults := defaultConfigFile().API
	legacyIntOverride := func(path string, value, defaultValue int) bool {
		origin := origins[path]
		if value <= 0 {
			return false
		}
		return origin.Source == SourceEnv || origin.Source == SourceDotEnv || value != defaultValue
	}
	if isProviderModelProfileID(file, current.DefaultProfileID) {
		if legacyIntOverride("api.timeout_seconds", current.APITimeoutSeconds, apiDefaults.TimeoutSeconds) {
			defaultProfile.TimeoutSeconds = current.APITimeoutSeconds
		}
	}
	current.ModelProfiles[current.DefaultProfileID] = defaultProfile
	current.APIKey = defaultProfile.APIKey
	current.Model = defaultProfile.Model
	current.ReasoningEffort = defaultProfile.ReasoningEffort
	current.BaseURL = defaultProfile.BaseURL
	current.MaxTokens = defaultProfile.MaxTokens
	current.APITimeoutSeconds = defaultProfile.TimeoutSeconds
	origins["api.providers"] = fieldOrigin{Source: SourceYAML, YAMLValue: maskLLMProviders(file.API.Providers), Effective: maskLLMProviders(current.LLMProviders)}
	origins["api.model_strategy"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.API.ModelStrategy, Effective: current.LLMStrategy}
	origins["acp.agents"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.ACP.Agents, Effective: maskACPAgentSecrets(acpAgentsFromConfigFile(file))}
	resolveInt("agent.compress_threshold", file.Agent.CompressThreshold, "COMPRESS_THRESHOLD", func(v int) { current.CompressThreshold = v })
	resolveBool("agent.compaction.auto_enabled", file.Agent.Compaction.AutoEnabled, "GODEX_AGENT_COMPACTION_AUTO_ENABLED", func(v bool) {
		current.Compaction.AutoEnabled = v
	})
	resolveInt("agent.compaction.trigger_tokens", file.Agent.Compaction.TriggerTokens, "GODEX_AGENT_COMPACTION_TRIGGER_TOKENS", func(v int) {
		current.Compaction.TriggerTokens = positiveOrDefault(v, 60000)
	})
	resolveInt("agent.compaction.target_history_tokens", file.Agent.Compaction.TargetHistoryTokens, "GODEX_AGENT_COMPACTION_TARGET_HISTORY_TOKENS", func(v int) {
		current.Compaction.TargetHistoryTokens = positiveOrDefault(v, 12000)
	})
	resolveString("agent.compaction.mode", file.Agent.Compaction.Mode, "GODEX_AGENT_COMPACTION_MODE", func(v string) {
		current.Compaction.Mode = NormalizeCompactionMode(v)
	})
	resolveString("agent.compaction.model_profile_id", file.Agent.Compaction.ModelProfileID, "GODEX_AGENT_COMPACTION_MODEL_PROFILE_ID", func(v string) {
		current.Compaction.ModelProfileID = strings.TrimSpace(v)
	})
	resolveInt("agent.compaction.max_latency_ms", file.Agent.Compaction.MaxLatencyMS, "GODEX_AGENT_COMPACTION_MAX_LATENCY_MS", func(v int) {
		current.Compaction.MaxLatencyMS = positiveOrDefault(v, 3000)
	})
	resolveInt("agent.max_turns", file.Agent.MaxTurns, "GODEX_AGENT_MAX_TURNS", func(v int) { current.MaxTurns = v })
	resolveString("agent.profile", file.Agent.Profile, "GODEX_AGENT_PROFILE", func(v string) {
		current.AgentProfile = NormalizeAgentProfile(v)
	})
	resolveString("agent.default_profiles.acp", file.Agent.DefaultProfiles.ACP, "GODEX_AGENT_DEFAULT_PROFILE_ACP", func(v string) {
		current.AgentDefaultProfiles.ACP = NormalizeAgentProfile(v)
	})
	resolveString("agent.default_profiles.cli", file.Agent.DefaultProfiles.CLI, "GODEX_AGENT_DEFAULT_PROFILE_CLI", func(v string) {
		current.AgentDefaultProfiles.CLI = NormalizeAgentProfile(v)
	})
	resolveString("agent.default_profiles.tui", file.Agent.DefaultProfiles.TUI, "GODEX_AGENT_DEFAULT_PROFILE_TUI", func(v string) {
		current.AgentDefaultProfiles.TUI = NormalizeAgentProfile(v)
	})
	resolveString("agent.default_profiles.web", file.Agent.DefaultProfiles.Web, "GODEX_AGENT_DEFAULT_PROFILE_WEB", func(v string) {
		current.AgentDefaultProfiles.Web = NormalizeAgentProfile(v)
	})
	resolveString("agent.default_profiles.weixin", file.Agent.DefaultProfiles.Weixin, "GODEX_AGENT_DEFAULT_PROFILE_WEIXIN", func(v string) {
		current.AgentDefaultProfiles.Weixin = NormalizeAgentProfile(v)
	})
	resolveString("agent.default_profiles.feishu", file.Agent.DefaultProfiles.Feishu, "GODEX_AGENT_DEFAULT_PROFILE_FEISHU", func(v string) {
		current.AgentDefaultProfiles.Feishu = NormalizeAgentProfile(v)
	})
	resolveString("logging.level", file.Logging.Level, "LOG_LEVEL", func(v string) { current.Logging.Level = v })
	resolveString("logging.file_path", file.Logging.FilePath, "LOG_FILE", func(v string) { current.Logging.FilePath = resolveLogPath(m.homeDir, v) })
	resolveBool("logging.also_stderr", file.Logging.AlsoStderr, "LOG_MIRROR_TO_STDERR", func(v bool) { current.Logging.AlsoStderr = v })
	resolveString("web.token", file.Web.Token, "GODEX_WEB_TOKEN", func(v string) { current.WebToken = v })
	resolveBool("cron.enabled", file.Cron.Enabled, "GODEX_CRON_ENABLED", func(v bool) { current.Cron.Enabled = v })
	resolveInt("cron.tick_seconds", file.Cron.TickSeconds, "GODEX_CRON_TICK_SECONDS", func(v int) { current.Cron.TickSeconds = v })
	resolveString("cron.default_timezone", file.Cron.DefaultTimezone, "GODEX_CRON_DEFAULT_TIMEZONE", func(v string) { current.Cron.DefaultTimezone = v })
	resolveInt("cron.max_concurrent_runs", file.Cron.MaxConcurrentRuns, "GODEX_CRON_MAX_CONCURRENT_RUNS", func(v int) { current.Cron.MaxConcurrentRuns = v })
	resolveBool("heartbeat.enabled", file.Heartbeat.Enabled, "GODEX_HEARTBEAT_ENABLED", func(v bool) { current.Heartbeat.Enabled = v })
	resolveInt("heartbeat.tick_seconds", file.Heartbeat.TickSeconds, "GODEX_HEARTBEAT_TICK_SECONDS", func(v int) { current.Heartbeat.TickSeconds = v })
	resolveString("heartbeat.checklist_path", file.Heartbeat.ChecklistPath, "GODEX_HEARTBEAT_CHECKLIST_PATH", func(v string) { current.Heartbeat.ChecklistPath = resolvePath(m.workspace, v) })
	resolveString("heartbeat.ok_token", file.Heartbeat.OKToken, "GODEX_HEARTBEAT_OK_TOKEN", func(v string) { current.Heartbeat.OKToken = v })
	resolveInt("heartbeat.default_interval_seconds", file.Heartbeat.DefaultIntervalSeconds, "GODEX_HEARTBEAT_DEFAULT_INTERVAL_SECONDS", func(v int) { current.Heartbeat.DefaultIntervalSeconds = v })
	resolveString("heartbeat.default_timezone", file.Heartbeat.DefaultTimezone, "GODEX_HEARTBEAT_DEFAULT_TIMEZONE", func(v string) { current.Heartbeat.DefaultTimezone = v })
	resolveString("control.node_name", file.Control.NodeName, "GODEX_CONTROL_NODE_NAME", func(v string) { current.Control.NodeName = v })
	resolveString("control.center_url", file.Control.CenterURL, "GODEX_CONTROL_CENTER_URL", func(v string) { current.Control.CenterURL = v })
	resolveInt("control.heartbeat_seconds", file.Control.HeartbeatSeconds, "GODEX_CONTROL_HEARTBEAT_SECONDS", func(v int) {
		current.Control.HeartbeatSeconds = positiveOrDefault(v, 15)
	})
	resolveInt("control.offline_after_seconds", file.Control.OfflineAfterSeconds, "GODEX_CONTROL_OFFLINE_AFTER_SECONDS", func(v int) {
		current.Control.OfflineAfterSeconds = positiveOrDefault(v, 60)
	})
	origins["control.nodes"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Control.Nodes, Effective: controlNodesFromConfigFile(file)}
	resolveBool("runtime.recovery.auto_resume_interrupted_turns", file.Runtime.Recovery.AutoResumeInterruptedTurns, "GODEX_RUNTIME_RECOVERY_AUTO_RESUME_INTERRUPTED_TURNS", func(v bool) {
		current.Runtime.Recovery.AutoResumeInterruptedTurns = v
	})
	resolveBool("runtime.recovery.auto_repair_sessions", file.Runtime.Recovery.AutoRepairSessions, "GODEX_RUNTIME_RECOVERY_AUTO_REPAIR_SESSIONS", func(v bool) {
		current.Runtime.Recovery.AutoRepairSessions = v
	})
	resolveInt("storage.tmp_ttl_hours", file.Storage.TmpTTLHours, "GODEX_STORAGE_TMP_TTL_HOURS", func(v int) {
		current.Storage.TmpTTLHours = positiveOrDefault(v, 72)
	})
	resolveInt("storage.artifact_ttl_hours", file.Storage.ArtifactTTLHours, "GODEX_STORAGE_ARTIFACT_TTL_HOURS", func(v int) {
		current.Storage.ArtifactTTLHours = positiveOrDefault(v, 168)
	})
	resolveBool("storage.browser_cache_auto_clean", file.Storage.BrowserCacheAutoClean, "GODEX_STORAGE_BROWSER_CACHE_AUTO_CLEAN", func(v bool) {
		current.Storage.BrowserCacheAutoClean = v
	})
	resolveInt("storage.browser_cache_max_mb", file.Storage.BrowserCacheMaxMB, "GODEX_STORAGE_BROWSER_CACHE_MAX_MB", func(v int) {
		current.Storage.BrowserCacheMaxMB = positiveOrDefault(v, 256)
	})
	resolveInt("storage.session_checkpoint_keep_latest", file.Storage.SessionCheckpointKeepLatest, "GODEX_STORAGE_SESSION_CHECKPOINT_KEEP_LATEST", func(v int) {
		current.Storage.SessionCheckpointKeepLatest = positiveOrDefault(v, 20)
	})
	resolveInt("storage.session_checkpoint_ttl_hours", file.Storage.SessionCheckpointTTLHours, "GODEX_STORAGE_SESSION_CHECKPOINT_TTL_HOURS", func(v int) {
		current.Storage.SessionCheckpointTTLHours = positiveOrDefault(v, 168)
	})
	resolveBool("storage.session_checkpoint_auto_prune", file.Storage.SessionCheckpointAutoPrune, "GODEX_STORAGE_SESSION_CHECKPOINT_AUTO_PRUNE", func(v bool) {
		current.Storage.SessionCheckpointAutoPrune = v
	})
	resolveString("storage.session_backend", file.Storage.SessionBackend, "GODEX_STORAGE_SESSION_BACKEND", func(v string) {
		current.Storage.SessionBackend = normalizeSessionStorageBackend(v)
	})
	resolveString("storage.sqlite_path", file.Storage.SQLitePath, "GODEX_STORAGE_SQLITE_PATH", func(v string) {
		current.Storage.SQLitePath = strings.TrimSpace(v)
	})
	resolveString("security.profile", file.Security.Profile, "GODEX_SECURITY_PROFILE", func(v string) {
		current.Security.Profile = normalizeSecurityProfileName(v)
	})
	resolveString("team.lead_name", file.Team.LeadName, "LEAD_NAME", func(v string) { current.LeadName = v })
	resolveString("team.team_name", file.Team.TeamName, "TEAM_NAME", func(v string) { current.TeamName = v })
	resolveCSV("team.default_skills", file.Team.DefaultSkills, "DEFAULT_SKILLS", func(v []string) { current.DefaultSkills = append([]string{}, v...) })
	resolveInt("team.teammate_work_limit", file.Team.TeammateWorkLimit, "TEAMMATE_WORK_ITERATIONS", func(v int) { current.TeammateWorkLimit = v })
	resolveInt("team.teammate_poll_seconds", file.Team.TeammatePollSeconds, "TEAMMATE_IDLE_POLL_INTERVAL_SECONDS", func(v int) { current.TeammatePollEvery = time.Duration(v) * time.Second })
	resolveInt("team.teammate_idle_timeout_seconds", file.Team.TeammateIdleTimeoutSecs, "TEAMMATE_IDLE_TIMEOUT_SECONDS", func(v int) { current.TeammateIdleFor = time.Duration(v) * time.Second })
	resolveString("channels.feishu.app_id", file.Channels.Feishu.AppID, "FEISHU_APP_ID", func(v string) { current.Feishu.AppID = v })
	resolveString("channels.feishu.app_secret", file.Channels.Feishu.AppSecret, "FEISHU_APP_SECRET", func(v string) { current.Feishu.AppSecret = v })
	resolveBool("channels.feishu.enabled", file.Channels.Feishu.Enabled, "FEISHU_ENABLED", func(v bool) { current.Feishu.Enabled = v })
	resolveString("channels.feishu.domain", file.Channels.Feishu.Domain, "FEISHU_DOMAIN", func(v string) { current.Feishu.Domain = v })
	resolveBool("channels.weixin.enabled", file.Channels.Weixin.Enabled, "WEIXIN_ENABLED", func(v bool) { current.Weixin.Enabled = v })
	resolveString("channels.weixin.base_url", file.Channels.Weixin.BaseURL, "WEIXIN_BASE_URL", func(v string) { current.Weixin.BaseURL = v })
	resolveString("channels.weixin.cdn_base_url", file.Channels.Weixin.CDNBaseURL, "WEIXIN_CDN_BASE_URL", func(v string) { current.Weixin.CDNBaseURL = v })
	resolveString("channels.weixin.account_id", file.Channels.Weixin.AccountID, "WEIXIN_ACCOUNT_ID", func(v string) { current.Weixin.AccountID = v })
	resolveCSV("channels.weixin.allow_from", file.Channels.Weixin.AllowFrom, "WEIXIN_ALLOW_FROM", func(v []string) { current.Weixin.AllowFrom = append([]string{}, v...) })
	resolveString("channels.weixin.route_tag", file.Channels.Weixin.RouteTag, "WEIXIN_ROUTE_TAG", func(v string) { current.Weixin.RouteTag = v })
	resolveInt("channels.weixin.long_poll_timeout_ms", file.Channels.Weixin.LongPollTimeoutMs, "WEIXIN_LONG_POLL_TIMEOUT_MS", func(v int) { current.Weixin.LongPollTimeoutMs = v })
	resolveString("channels.weixin.proxy", file.Channels.Weixin.Proxy, "WEIXIN_PROXY", func(v string) { current.Weixin.Proxy = v })
	resolveBool("tools.web_search.enabled", file.Tools.WebSearch.Enabled, "GODEX_WEB_SEARCH_ENABLED", func(v bool) { current.Tools.WebSearch.Enabled = v })
	resolveCSV("tools.web_search.provider_order", file.Tools.WebSearch.ProviderOrder, "GODEX_WEB_SEARCH_PROVIDER_ORDER", func(v []string) {
		current.Tools.WebSearch.ProviderOrder = append([]string{}, v...)
	})
	resolveInt("tools.web_search.cache_ttl_seconds", file.Tools.WebSearch.CacheTTLSeconds, "GODEX_WEB_SEARCH_CACHE_TTL_SECONDS", func(v int) {
		current.Tools.WebSearch.CacheTTLSeconds = v
	})
	resolveString("tools.web_search.browser.engine", file.Tools.WebSearch.Browser.Engine, "GODEX_WEB_SEARCH_BROWSER_ENGINE", func(v string) {
		current.Tools.WebSearch.Browser.Engine = v
	})
	resolveCSV("tools.web_search.browser.engine_fallback", file.Tools.WebSearch.Browser.EngineFallback, "GODEX_WEB_SEARCH_BROWSER_ENGINE_FALLBACK", func(v []string) {
		current.Tools.WebSearch.Browser.EngineFallback = append([]string{}, v...)
	})
	for _, engine := range []string{"duckduckgo", "bing", "brave", "custom"} {
		engine := engine
		section := file.Tools.WebSearch.Browser.Engines[engine]
		envPrefix := "GODEX_WEB_SEARCH_BROWSER_" + strings.ToUpper(engine) + "_"
		pathPrefix := "tools.web_search.browser.engines." + engine + "."
		resolveString(pathPrefix+"search_url_template", section.SearchURLTemplate, envPrefix+"SEARCH_URL_TEMPLATE", func(v string) {
			updateBrowserEngineConfig(&current.Tools.WebSearch.Browser, engine, func(cfg *WebSearchBrowserEngineConfig) { cfg.SearchURLTemplate = v })
		})
		resolveCSV(pathPrefix+"blocked_hosts", section.BlockedHosts, envPrefix+"BLOCKED_HOSTS", func(v []string) {
			updateBrowserEngineConfig(&current.Tools.WebSearch.Browser, engine, func(cfg *WebSearchBrowserEngineConfig) { cfg.BlockedHosts = append([]string{}, v...) })
		})
		resolveString(pathPrefix+"result_container_selector", section.ResultContainerSelector, envPrefix+"RESULT_CONTAINER_SELECTOR", func(v string) {
			updateBrowserEngineConfig(&current.Tools.WebSearch.Browser, engine, func(cfg *WebSearchBrowserEngineConfig) { cfg.ResultContainerSelector = v })
		})
		resolveString(pathPrefix+"result_link_selector", section.ResultLinkSelector, envPrefix+"RESULT_LINK_SELECTOR", func(v string) {
			updateBrowserEngineConfig(&current.Tools.WebSearch.Browser, engine, func(cfg *WebSearchBrowserEngineConfig) { cfg.ResultLinkSelector = v })
		})
		resolveString(pathPrefix+"result_snippet_selector", section.ResultSnippetSelector, envPrefix+"RESULT_SNIPPET_SELECTOR", func(v string) {
			updateBrowserEngineConfig(&current.Tools.WebSearch.Browser, engine, func(cfg *WebSearchBrowserEngineConfig) { cfg.ResultSnippetSelector = v })
		})
	}
	resolveInt("tools.web_search.browser.wait_network_idle_ms", file.Tools.WebSearch.Browser.WaitNetworkIdleMS, "GODEX_WEB_SEARCH_BROWSER_WAIT_NETWORK_IDLE_MS", func(v int) {
		current.Tools.WebSearch.Browser.WaitNetworkIdleMS = v
	})
	resolveInt("tools.web_search.browser.wait_after_load_ms", file.Tools.WebSearch.Browser.WaitAfterLoadMS, "GODEX_WEB_SEARCH_BROWSER_WAIT_AFTER_LOAD_MS", func(v int) {
		current.Tools.WebSearch.Browser.WaitAfterLoadMS = v
	})
	resolveInt("tools.web_search.browser.max_scrolls", file.Tools.WebSearch.Browser.MaxScrolls, "GODEX_WEB_SEARCH_BROWSER_MAX_SCROLLS", func(v int) {
		current.Tools.WebSearch.Browser.MaxScrolls = v
	})
	resolveInt("tools.web_search.browser.result_timeout_seconds", file.Tools.WebSearch.Browser.ResultTimeoutSeconds, "GODEX_WEB_SEARCH_BROWSER_RESULT_TIMEOUT_SECONDS", func(v int) {
		current.Tools.WebSearch.Browser.ResultTimeoutSeconds = v
	})
	resolveCSV("tools.web_search.browser.preferred_hosts", file.Tools.WebSearch.Browser.PreferredHosts, "GODEX_WEB_SEARCH_BROWSER_PREFERRED_HOSTS", func(v []string) {
		current.Tools.WebSearch.Browser.PreferredHosts = append([]string{}, v...)
	})
	resolveString("tools.web_search.brave.api_key", file.Tools.WebSearch.Brave.APIKey, "GODEX_WEB_SEARCH_BRAVE_API_KEY", func(v string) {
		current.Tools.WebSearch.BraveAPIKey = v
	})
	resolveString("tools.web_search.exa.api_key", file.Tools.WebSearch.Exa.APIKey, "GODEX_WEB_SEARCH_EXA_API_KEY", func(v string) {
		current.Tools.WebSearch.ExaAPIKey = v
	})
	resolveString("tools.web_search.tavily.api_key", file.Tools.WebSearch.Tavily.APIKey, "GODEX_WEB_SEARCH_TAVILY_API_KEY", func(v string) {
		current.Tools.WebSearch.TavilyAPIKey = v
	})
	resolveBool("tools.web_fetch.enabled", file.Tools.WebFetch.Enabled, "GODEX_WEB_FETCH_ENABLED", func(v bool) { current.Tools.WebFetch.Enabled = v })
	resolveInt("tools.web_fetch.max_chars", file.Tools.WebFetch.MaxChars, "GODEX_WEB_FETCH_MAX_CHARS", func(v int) {
		current.Tools.WebFetch.MaxChars = v
	})
	resolveInt("tools.web_fetch.timeout_seconds", file.Tools.WebFetch.TimeoutSeconds, "GODEX_WEB_FETCH_TIMEOUT_SECONDS", func(v int) {
		current.Tools.WebFetch.TimeoutSeconds = v
	})
	resolveString("tools.web_fetch.policy", file.Tools.WebFetch.Policy, "GODEX_WEB_FETCH_POLICY", func(v string) {
		current.Tools.WebFetch.Policy = v
	})
	resolveCSV("tools.web_fetch.allowed_domains", file.Tools.WebFetch.AllowedDomains, "GODEX_WEB_FETCH_ALLOWED_DOMAINS", func(v []string) {
		current.Tools.WebFetch.AllowedDomains = append([]string{}, v...)
	})
	resolveCSV("tools.web_fetch.blocked_domains", file.Tools.WebFetch.BlockedDomains, "GODEX_WEB_FETCH_BLOCKED_DOMAINS", func(v []string) {
		current.Tools.WebFetch.BlockedDomains = append([]string{}, v...)
	})
	resolveBool("tools.web_fetch.allow_private_hosts", file.Tools.WebFetch.AllowPrivateHosts, "GODEX_WEB_FETCH_ALLOW_PRIVATE_HOSTS", func(v bool) {
		current.Tools.WebFetch.AllowPrivateHosts = v
	})
	resolveInt("tools.glob.default_max_results", file.Tools.Glob.DefaultMaxResults, "GODEX_GLOB_DEFAULT_MAX_RESULTS", func(v int) {
		current.Tools.Glob.DefaultMaxResults = v
	})
	resolveInt("tools.subagent.max_batch_size", file.Tools.Subagent.MaxBatchSize, "GODEX_SUBAGENT_MAX_BATCH_SIZE", func(v int) {
		current.Tools.Subagent.MaxBatchSize = v
	})
	resolveInt("tools.subagent.max_concurrent_jobs", file.Tools.Subagent.MaxConcurrentJobs, "GODEX_SUBAGENT_MAX_CONCURRENT_JOBS", func(v int) {
		current.Tools.Subagent.MaxConcurrentJobs = v
	})
	resolveInt("tools.subagent.default_max_turns", file.Tools.Subagent.DefaultMaxTurns, "GODEX_SUBAGENT_DEFAULT_MAX_TURNS", func(v int) {
		current.Tools.Subagent.DefaultMaxTurns = v
	})
	resolveInt("tools.subagent.max_job_timeout_ms", file.Tools.Subagent.MaxJobTimeoutMs, "GODEX_SUBAGENT_MAX_JOB_TIMEOUT_MS", func(v int) {
		current.Tools.Subagent.MaxJobTimeoutMs = v
	})
	resolveString("tools.subagent.readonly_isolation", file.Tools.Subagent.ReadOnlyIsolation, "GODEX_SUBAGENT_READONLY_ISOLATION", func(v string) {
		current.Tools.Subagent.ReadOnlyIsolation = normalizeSubagentReadOnlyIsolation(v)
	})
	resolveString("tools.subagent.git_dirty_isolation", file.Tools.Subagent.GitDirtyIsolation, "GODEX_SUBAGENT_GIT_DIRTY_ISOLATION", func(v string) {
		current.Tools.Subagent.GitDirtyIsolation = normalizeSubagentGitDirtyIsolation(v)
	})
	resolveString("tools.subagent.non_git_write_isolation", file.Tools.Subagent.NonGitWriteIsolation, "GODEX_SUBAGENT_NON_GIT_WRITE_ISOLATION", func(v string) {
		current.Tools.Subagent.NonGitWriteIsolation = normalizeSubagentNonGitWriteIsolation(v)
	})
	resolveInt("tools.subagent.workspace_ttl_hours", file.Tools.Subagent.WorkspaceTTLHours, "GODEX_SUBAGENT_WORKSPACE_TTL_HOURS", func(v int) {
		current.Tools.Subagent.WorkspaceTTLHours = v
	})
	resolveString("tools.execution.mode", file.Tools.Execution.Mode, "GODEX_TOOLS_EXECUTION_MODE", func(v string) {
		current.Tools.Execution.Mode = v
	})
	resolveString("tools.execution.docker_image", file.Tools.Execution.DockerImage, "GODEX_TOOLS_EXECUTION_DOCKER_IMAGE", func(v string) {
		current.Tools.Execution.DockerImage = v
	})
	resolveString("tools.execution.docker_network", file.Tools.Execution.DockerNetwork, "GODEX_TOOLS_EXECUTION_DOCKER_NETWORK", func(v string) {
		current.Tools.Execution.DockerNetwork = v
	})
	resolveString("tools.execution.ssh_target", file.Tools.Execution.SSHTarget, "GODEX_TOOLS_EXECUTION_SSH_TARGET", func(v string) {
		current.Tools.Execution.SSHTarget = v
	})
	resolveString("tools.execution.ssh_workspace", file.Tools.Execution.SSHWorkspace, "GODEX_TOOLS_EXECUTION_SSH_WORKSPACE", func(v string) {
		current.Tools.Execution.SSHWorkspace = v
	})
	resolveCSV("tools.execution.ssh_options", file.Tools.Execution.SSHOptions, "GODEX_TOOLS_EXECUTION_SSH_OPTIONS", func(v []string) {
		current.Tools.Execution.SSHOptions = append([]string{}, v...)
	})
	resolveCSV("tools.execution.shell_allow_patterns", file.Tools.Execution.ShellAllowPatterns, "GODEX_TOOLS_EXECUTION_SHELL_ALLOW_PATTERNS", func(v []string) {
		current.Tools.Execution.ShellAllowPatterns = append([]string{}, v...)
	})
	resolveCSV("tools.execution.shell_deny_patterns", file.Tools.Execution.ShellDenyPatterns, "GODEX_TOOLS_EXECUTION_SHELL_DENY_PATTERNS", func(v []string) {
		current.Tools.Execution.ShellDenyPatterns = append([]string{}, v...)
	})
	resolveBool("tools.browser.enabled", file.Tools.Browser.Enabled, "GODEX_BROWSER_ENABLED", func(v bool) {
		current.Tools.Browser.Enabled = v
	})
	resolveBool("tools.browser.headless", file.Tools.Browser.Headless, "GODEX_BROWSER_HEADLESS", func(v bool) {
		current.Tools.Browser.Headless = v
	})
	resolveString("tools.browser.browser_path", file.Tools.Browser.BrowserPath, "GODEX_BROWSER_PATH", func(v string) {
		current.Tools.Browser.BrowserPath = v
	})
	resolveString("tools.browser.cdp_url", file.Tools.Browser.CDPURL, "GODEX_BROWSER_CDP_URL", func(v string) {
		current.Tools.Browser.CDPURL = v
	})
	resolveInt("tools.browser.action_timeout_seconds", file.Tools.Browser.ActionTimeoutSeconds, "GODEX_BROWSER_ACTION_TIMEOUT_SECONDS", func(v int) {
		current.Tools.Browser.ActionTimeoutSeconds = v
	})
	resolveInt("tools.browser.idle_timeout_seconds", file.Tools.Browser.IdleTimeoutSeconds, "GODEX_BROWSER_IDLE_TIMEOUT_SECONDS", func(v int) {
		current.Tools.Browser.IdleTimeoutSeconds = v
	})
	resolveInt("tools.browser.max_pages_per_session", file.Tools.Browser.MaxPagesPerSession, "GODEX_BROWSER_MAX_PAGES_PER_SESSION", func(v int) {
		current.Tools.Browser.MaxPagesPerSession = v
	})
	resolveBool("tools.browser.allow_private_hosts", file.Tools.Browser.AllowPrivateHosts, "GODEX_BROWSER_ALLOW_PRIVATE_HOSTS", func(v bool) {
		current.Tools.Browser.AllowPrivateHosts = v
	})
	resolveBool("tools.history_search.enabled", file.Tools.History.Enabled, "GODEX_TOOLS_HISTORY_SEARCH_ENABLED", func(v bool) {
		current.Tools.History.Enabled = v
	})
	resolveBool("tools.history_search.auto.enabled", file.Tools.History.Auto.Enabled, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_ENABLED", func(v bool) {
		current.Tools.History.Auto.Enabled = v
	})
	resolveInt("tools.history_search.auto.max_per_turn", file.Tools.History.Auto.MaxPerTurn, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_MAX_PER_TURN", func(v int) {
		current.Tools.History.Auto.MaxPerTurn = v
	})
	resolveString("tools.history_search.auto.default_scope", file.Tools.History.Auto.DefaultScope, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_DEFAULT_SCOPE", func(v string) {
		current.Tools.History.Auto.DefaultScope = v
	})
	resolveBool("tools.history_search.auto.allow_archive_on_clear", file.Tools.History.Auto.AllowArchiveOnClear, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_ALLOW_ARCHIVE_ON_CLEAR", func(v bool) {
		current.Tools.History.Auto.AllowArchiveOnClear = v
	})
	resolveBool("tools.history_search.auto.allow_archive_on_compact", file.Tools.History.Auto.AllowArchiveOnCompact, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_ALLOW_ARCHIVE_ON_COMPACT", func(v bool) {
		current.Tools.History.Auto.AllowArchiveOnCompact = v
	})
	resolveBool("tools.history_search.auto.allow_all_archives_automatic", file.Tools.History.Auto.AllowAllArchivesAutomatic, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_ALLOW_ALL_ARCHIVES_AUTOMATIC", func(v bool) {
		current.Tools.History.Auto.AllowAllArchivesAutomatic = v
	})
	resolveInt("tools.history_search.auto.min_score", file.Tools.History.Auto.MinScore, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_MIN_SCORE", func(v int) {
		current.Tools.History.Auto.MinScore = v
	})
	resolveCSV("tools.history_search.cues.explicit", file.Tools.History.Cues.Explicit, "GODEX_TOOLS_HISTORY_SEARCH_CUES_EXPLICIT", func(v []string) {
		current.Tools.History.Cues.Explicit = append([]string{}, v...)
	})
	resolveCSV("tools.history_search.cues.implicit", file.Tools.History.Cues.Implicit, "GODEX_TOOLS_HISTORY_SEARCH_CUES_IMPLICIT", func(v []string) {
		current.Tools.History.Cues.Implicit = append([]string{}, v...)
	})
	resolveCSV("tools.history_search.blocks.session_sources", file.Tools.History.Blocks.SessionSources, "GODEX_TOOLS_HISTORY_SEARCH_BLOCKS_SESSION_SOURCES", func(v []string) {
		current.Tools.History.Blocks.SessionSources = append([]string{}, v...)
	})
	resolveBool("tools.permissions.block_automation_mutations", file.Tools.Permissions.BlockAutomationMutations, "GODEX_TOOLS_PERMISSIONS_BLOCK_AUTOMATION_MUTATIONS", func(v bool) {
		current.Tools.Permissions.BlockAutomationMutations = v
	})
	resolveBool("tools.permissions.interactive_approval_enabled", file.Tools.Permissions.InteractiveApprovalEnabled, "GODEX_TOOLS_PERMISSIONS_INTERACTIVE_APPROVAL_ENABLED", func(v bool) {
		current.Tools.Permissions.InteractiveApprovalEnabled = v
	})
	resolveString("tools.permissions.interactive_approval_mode", file.Tools.Permissions.InteractiveApprovalMode, "GODEX_TOOLS_PERMISSIONS_INTERACTIVE_APPROVAL_MODE", func(v string) {
		current.Tools.Permissions.InteractiveApprovalMode = v
	})
	resolveInt("tools.permissions.pending_ttl_seconds", file.Tools.Permissions.PendingTTLSeconds, "GODEX_TOOLS_PERMISSIONS_PENDING_TTL_SECONDS", func(v int) {
		current.Tools.Permissions.PendingTTLSeconds = v
	})
	resolveCSV("tools.permissions.interactive_approval_sources", file.Tools.Permissions.InteractiveApprovalSources, "GODEX_TOOLS_PERMISSIONS_INTERACTIVE_APPROVAL_SOURCES", func(v []string) {
		current.Tools.Permissions.InteractiveApprovalSources = append([]string{}, v...)
	})
	resolveCSV("tools.permissions.interactive_approval_tools", file.Tools.Permissions.InteractiveApprovalTools, "GODEX_TOOLS_PERMISSIONS_INTERACTIVE_APPROVAL_TOOLS", func(v []string) {
		current.Tools.Permissions.InteractiveApprovalTools = append([]string{}, v...)
	})
	resolveCSV("tools.permissions.trusted_path_prefixes", file.Tools.Permissions.TrustedPathPrefixes, "GODEX_TOOLS_PERMISSIONS_TRUSTED_PATH_PREFIXES", func(v []string) {
		current.Tools.Permissions.TrustedPathPrefixes = append([]string{}, v...)
	})
	resolveCSV("tools.permissions.trusted_command_prefixes", file.Tools.Permissions.TrustedCommandPrefixes, "GODEX_TOOLS_PERMISSIONS_TRUSTED_COMMAND_PREFIXES", func(v []string) {
		current.Tools.Permissions.TrustedCommandPrefixes = append([]string{}, v...)
	})
	resolveBool("media.moonshot.enabled", file.Media.Moonshot.Enabled, "GODEX_MEDIA_MOONSHOT_ENABLED", func(v bool) {
		current.Media.Moonshot.Enabled = v
	})
	resolveString("media.moonshot.base_url", file.Media.Moonshot.BaseURL, "GODEX_MEDIA_MOONSHOT_BASE_URL", func(v string) {
		current.Media.Moonshot.BaseURL = v
	})
	resolveString("media.moonshot.api_key", file.Media.Moonshot.APIKey, "GODEX_MEDIA_MOONSHOT_API_KEY", func(v string) {
		current.Media.Moonshot.APIKey = v
	})
	resolveInt("media.document.max_chars", file.Media.Document.MaxChars, "GODEX_MEDIA_DOCUMENT_MAX_CHARS", func(v int) {
		current.Media.Document.MaxChars = v
	})
	resolveString("media.document.pdftotext_path", file.Media.Document.PDFToTextPath, "GODEX_MEDIA_DOCUMENT_PDFTOTEXT_PATH", func(v string) {
		current.Media.Document.PDFToTextPath = v
	})
	resolveString("media.ocr.mode", file.Media.OCR.Mode, "GODEX_MEDIA_OCR_MODE", func(v string) {
		current.Media.OCR.Mode = v
	})
	resolveString("media.ocr.tesseract_path", file.Media.OCR.TesseractPath, "GODEX_MEDIA_OCR_TESSERACT_PATH", func(v string) {
		current.Media.OCR.TesseractPath = v
	})
	resolveInt("media.ocr.max_chars", file.Media.OCR.MaxChars, "GODEX_MEDIA_OCR_MAX_CHARS", func(v int) {
		current.Media.OCR.MaxChars = v
	})
	resolveBool("media.audio.enabled", file.Media.Audio.Enabled, "GODEX_MEDIA_AUDIO_ENABLED", func(v bool) {
		current.Media.Audio.Enabled = v
	})
	resolveString("media.audio.ffmpeg_path", file.Media.Audio.FFmpegPath, "GODEX_MEDIA_AUDIO_FFMPEG_PATH", func(v string) {
		current.Media.Audio.FFmpegPath = v
	})
	resolveString("media.audio.ffprobe_path", file.Media.Audio.FFprobePath, "GODEX_MEDIA_AUDIO_FFPROBE_PATH", func(v string) {
		current.Media.Audio.FFprobePath = v
	})
	resolveString("media.audio.whisper_cpp_path", file.Media.Audio.WhisperCPPPath, "GODEX_MEDIA_AUDIO_WHISPER_CPP_PATH", func(v string) {
		current.Media.Audio.WhisperCPPPath = v
	})
	resolveString("media.audio.whisper_model_path", file.Media.Audio.WhisperModelPath, "GODEX_MEDIA_AUDIO_WHISPER_MODEL_PATH", func(v string) {
		current.Media.Audio.WhisperModelPath = resolvePath(m.workspace, v)
	})
	resolveInt("media.audio.max_chars", file.Media.Audio.MaxChars, "GODEX_MEDIA_AUDIO_MAX_CHARS", func(v int) {
		current.Media.Audio.MaxChars = v
	})
	resolveBool("media.video.enabled", file.Media.Video.Enabled, "GODEX_MEDIA_VIDEO_ENABLED", func(v bool) {
		current.Media.Video.Enabled = v
	})
	resolveInt("media.video.keyframe_interval_seconds", file.Media.Video.KeyframeIntervalSeconds, "GODEX_MEDIA_VIDEO_KEYFRAME_INTERVAL_SECONDS", func(v int) {
		current.Media.Video.KeyframeIntervalSeconds = v
	})
	resolveInt("media.video.max_frames", file.Media.Video.MaxFrames, "GODEX_MEDIA_VIDEO_MAX_FRAMES", func(v int) {
		current.Media.Video.MaxFrames = v
	})

	origins["paths.state_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.StateDir, Effective: file.Paths.StateDir}
	origins["paths.team_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.TeamDir, Effective: file.Paths.TeamDir}
	origins["paths.tasks_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.TasksDir, Effective: file.Paths.TasksDir}
	origins["paths.todos_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.TodosDir, Effective: file.Paths.TodosDir}
	origins["paths.memory_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.MemoryDir, Effective: file.Paths.MemoryDir}
	origins["paths.rules_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.RulesDir, Effective: file.Paths.RulesDir}
	origins["paths.skills_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.SkillsDir, Effective: file.Paths.SkillsDir}
	origins["paths.mcp_config_path"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.MCPConfigPath, Effective: file.Paths.MCPConfigPath}
	origins["paths.temp_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.TempDir, Effective: file.Paths.TempDir}
	origins["paths.transcripts_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.TranscriptsDir, Effective: file.Paths.TranscriptsDir}
	origins["paths.sessions_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.SessionsDir, Effective: file.Paths.SessionsDir}

	current.Logging.HasSensitive = strings.TrimSpace(current.Logging.FilePath) != "" ||
		strings.TrimSpace(current.WebToken) != "" ||
		strings.TrimSpace(current.Tools.WebSearch.BraveAPIKey) != "" ||
		strings.TrimSpace(current.Tools.WebSearch.ExaAPIKey) != "" ||
		strings.TrimSpace(current.Tools.WebSearch.TavilyAPIKey) != "" ||
		strings.TrimSpace(current.Media.Moonshot.APIKey) != ""
	return current, origins, nil
}

func (m *Manager) viewLocked() View {
	stored := storedValues(m.stored)
	effective := effectiveValues(m.current)
	fields := make(map[string]FieldState, len(m.origins))
	for _, section := range m.schema {
		for _, field := range section.Fields {
			origin := m.origins[field.Path]
			state := FieldState{
				Source:       origin.Source,
				OverriddenBy: origin.OverriddenBy,
				Secret:       field.Secret,
				Masked:       field.Secret,
				LiveApply:    field.LiveApply,
				Env:          field.Env,
				Configured:   fieldConfigured(field.Path, m.stored, m.current, origin),
			}
			if origin.UsedEnv != "" && origin.CanonicalEnv != "" && origin.UsedEnv != origin.CanonicalEnv {
				state.DeprecatedEnv = origin.UsedEnv
			}
			fields[field.Path] = state
			if field.Secret && field.Type != "json" {
				stored[field.Path] = ""
				if state.Configured {
					effective[field.Path] = "********"
				} else {
					effective[field.Path] = ""
				}
			}
		}
	}
	return View{
		FilePath:          m.configPath,
		EnvFile:           m.envPath,
		HomeDir:           m.homeDir,
		ProjectDir:        m.projectDir,
		HomeConfigFile:    m.homeConfigPath,
		ProjectConfigFile: m.projectConfigPath,
		HomeEnvFile:       m.homeEnvPath,
		ProjectEnvFile:    m.projectEnvPath,
		Revision:          m.revision,
		StoredValues:      stored,
		EffectiveValues:   effective,
		Fields:            fields,
		LastApply:         cloneApplyReport(m.lastApply),
	}
}

func resolveConfigFile(file ConfigFile, homeDir, projectDir, configFile, envFile, homeConfigFile, projectConfigFile, homeEnvFile, projectEnvFile string) *Config {
	if projectDir == "" {
		projectDir = defaultWorkspaceDir()
	}
	if homeDir == "" {
		homeDir = defaultHomeDir()
	}
	workspace := projectDir
	modelProfiles := modelProfilesFromConfigFile(file)
	defaultProfileID := strings.TrimSpace(file.API.DefaultModel)
	if defaultProfileID == "" {
		defaultProfileID = strings.TrimSpace(file.API.DefaultProfile)
	}
	if defaultProfileID == "" {
		defaultProfileID = defaultProfileIDFromProfiles(modelProfiles)
	}
	if _, ok := modelProfiles[defaultProfileID]; !ok {
		defaultProfileID = defaultProfileIDFromProfiles(modelProfiles)
	}
	defaultProfile := modelProfiles[defaultProfileID]
	cfg := &Config{
		APIKey:              defaultProfile.APIKey,
		Model:               defaultProfile.Model,
		BaseURL:             defaultProfile.BaseURL,
		DefaultProfileID:    defaultProfileID,
		DefaultModelRef:     defaultProfileID,
		AutoFallbackEnabled: file.API.AutoFallbackEnabled,
		ModelProfiles:       modelProfiles,
		LLMProviders:        llmProvidersFromConfigFile(file),
		LLMStrategy:         llmStrategyFromConfigFile(file),
		APITimeoutSeconds:   defaultProfile.TimeoutSeconds,
		WebToken:            file.Web.Token,
		Cron: CronConfig{
			Enabled:           file.Cron.Enabled,
			TickSeconds:       file.Cron.TickSeconds,
			DefaultTimezone:   file.Cron.DefaultTimezone,
			MaxConcurrentRuns: file.Cron.MaxConcurrentRuns,
		},
		Heartbeat: HeartbeatConfig{
			Enabled:                file.Heartbeat.Enabled,
			TickSeconds:            file.Heartbeat.TickSeconds,
			ChecklistPath:          resolvePath(workspace, file.Heartbeat.ChecklistPath),
			OKToken:                file.Heartbeat.OKToken,
			DefaultIntervalSeconds: file.Heartbeat.DefaultIntervalSeconds,
			DefaultTimezone:        file.Heartbeat.DefaultTimezone,
		},
		Control: ControlConfig{
			NodeName:            file.Control.NodeName,
			CenterURL:           file.Control.CenterURL,
			HeartbeatSeconds:    file.Control.HeartbeatSeconds,
			OfflineAfterSeconds: file.Control.OfflineAfterSeconds,
			Nodes:               controlNodesFromConfigFile(file),
		},
		Runtime: RuntimeConfig{
			Recovery: RuntimeRecoveryConfig{
				AutoResumeInterruptedTurns: file.Runtime.Recovery.AutoResumeInterruptedTurns,
				AutoRepairSessions:         file.Runtime.Recovery.AutoRepairSessions,
			},
		},
		Storage: StorageConfig{
			TmpTTLHours:                 file.Storage.TmpTTLHours,
			ArtifactTTLHours:            file.Storage.ArtifactTTLHours,
			BrowserCacheAutoClean:       file.Storage.BrowserCacheAutoClean,
			BrowserCacheMaxMB:           file.Storage.BrowserCacheMaxMB,
			SessionCheckpointKeepLatest: file.Storage.SessionCheckpointKeepLatest,
			SessionCheckpointTTLHours:   file.Storage.SessionCheckpointTTLHours,
			SessionCheckpointAutoPrune:  file.Storage.SessionCheckpointAutoPrune,
			SessionBackend:              normalizeSessionStorageBackend(file.Storage.SessionBackend),
			SQLitePath:                  strings.TrimSpace(file.Storage.SQLitePath),
		},
		Security: SecurityConfig{
			Profile: normalizeSecurityProfileName(file.Security.Profile),
		},
		MaxTokens:         defaultProfile.MaxTokens,
		HomeDir:           homeDir,
		WorkspaceDir:      workspace,
		ProjectDir:        projectDir,
		ConfigFile:        configFile,
		HomeConfigFile:    homeConfigFile,
		ProjectConfigFile: projectConfigFile,
		StateDir:          resolvePath(homeDir, file.Paths.StateDir),
		TeamDir:           resolvePath(homeDir, file.Paths.TeamDir),
		TasksDir:          resolvePath(homeDir, file.Paths.TasksDir),
		MemoryDir:         resolvePath(homeDir, file.Paths.MemoryDir),
		RulesDir:          resolvePath(homeDir, file.Paths.RulesDir),
		SkillsDir:         resolvePath(homeDir, file.Paths.SkillsDir),
		PackagesDir:       resolvePath(homeDir, "packages"),
		TodosDir:          resolvePath(homeDir, file.Paths.TodosDir),
		MCPConfigPath:     resolvePath(homeDir, file.Paths.MCPConfigPath),
		TempDir:           resolvePath(homeDir, file.Paths.TempDir),
		TranscriptsDir:    resolvePath(homeDir, file.Paths.TranscriptsDir),
		SessionsDir:       resolvePath(homeDir, file.Paths.SessionsDir),
		CompressThreshold: file.Agent.CompressThreshold,
		Compaction: AgentCompactionConfig{
			AutoEnabled:         file.Agent.Compaction.AutoEnabled,
			TriggerTokens:       positiveOrDefault(file.Agent.Compaction.TriggerTokens, 60000),
			TargetHistoryTokens: positiveOrDefault(file.Agent.Compaction.TargetHistoryTokens, 12000),
			Mode:                NormalizeCompactionMode(file.Agent.Compaction.Mode),
			ModelProfileID:      strings.TrimSpace(file.Agent.Compaction.ModelProfileID),
			MaxLatencyMS:        positiveOrDefault(file.Agent.Compaction.MaxLatencyMS, 3000),
		},
		MaxTurns:     file.Agent.MaxTurns,
		AgentProfile: NormalizeAgentProfile(file.Agent.Profile),
		AgentDefaultProfiles: AgentDefaultProfilesConfig{
			ACP:    NormalizeAgentProfile(file.Agent.DefaultProfiles.ACP),
			CLI:    NormalizeAgentProfile(file.Agent.DefaultProfiles.CLI),
			TUI:    NormalizeAgentProfile(file.Agent.DefaultProfiles.TUI),
			Web:    NormalizeAgentProfile(file.Agent.DefaultProfiles.Web),
			Weixin: NormalizeAgentProfile(file.Agent.DefaultProfiles.Weixin),
			Feishu: NormalizeAgentProfile(file.Agent.DefaultProfiles.Feishu),
		},
		LeadName:          file.Team.LeadName,
		TeamName:          file.Team.TeamName,
		DefaultSkills:     append([]string{}, file.Team.DefaultSkills...),
		TeammateWorkLimit: file.Team.TeammateWorkLimit,
		TeammatePollEvery: time.Duration(file.Team.TeammatePollSeconds) * time.Second,
		TeammateIdleFor:   time.Duration(file.Team.TeammateIdleTimeoutSecs) * time.Second,
		EnvFile:           envFile,
		HomeEnvFile:       homeEnvFile,
		ProjectEnvFile:    projectEnvFile,
		Logging: LoggingConfig{
			Level:      file.Logging.Level,
			FilePath:   resolveLogPath(homeDir, file.Logging.FilePath),
			AlsoStderr: file.Logging.AlsoStderr,
		},
		Tools: ToolsConfig{
			WebSearch: WebSearchConfig{
				Enabled:         file.Tools.WebSearch.Enabled,
				ProviderOrder:   append([]string{}, file.Tools.WebSearch.ProviderOrder...),
				CacheTTLSeconds: file.Tools.WebSearch.CacheTTLSeconds,
				Browser: WebSearchBrowserConfig{
					Engine:               file.Tools.WebSearch.Browser.Engine,
					EngineFallback:       append([]string{}, file.Tools.WebSearch.Browser.EngineFallback...),
					Engines:              webSearchBrowserEnginesFromFile(file.Tools.WebSearch.Browser.Engines),
					WaitNetworkIdleMS:    file.Tools.WebSearch.Browser.WaitNetworkIdleMS,
					WaitAfterLoadMS:      file.Tools.WebSearch.Browser.WaitAfterLoadMS,
					MaxScrolls:           file.Tools.WebSearch.Browser.MaxScrolls,
					ResultTimeoutSeconds: file.Tools.WebSearch.Browser.ResultTimeoutSeconds,
					PreferredHosts:       append([]string{}, file.Tools.WebSearch.Browser.PreferredHosts...),
				},
				BraveAPIKey:  file.Tools.WebSearch.Brave.APIKey,
				ExaAPIKey:    file.Tools.WebSearch.Exa.APIKey,
				TavilyAPIKey: file.Tools.WebSearch.Tavily.APIKey,
			},
			WebFetch: WebFetchConfig{
				Enabled:           file.Tools.WebFetch.Enabled,
				MaxChars:          file.Tools.WebFetch.MaxChars,
				TimeoutSeconds:    file.Tools.WebFetch.TimeoutSeconds,
				Policy:            file.Tools.WebFetch.Policy,
				AllowedDomains:    append([]string{}, file.Tools.WebFetch.AllowedDomains...),
				BlockedDomains:    append([]string{}, file.Tools.WebFetch.BlockedDomains...),
				AllowPrivateHosts: file.Tools.WebFetch.AllowPrivateHosts,
			},
			Glob: GlobConfig{
				DefaultMaxResults: file.Tools.Glob.DefaultMaxResults,
			},
			Subagent: SubagentConfig{
				MaxBatchSize:         file.Tools.Subagent.MaxBatchSize,
				MaxConcurrentJobs:    file.Tools.Subagent.MaxConcurrentJobs,
				DefaultMaxTurns:      file.Tools.Subagent.DefaultMaxTurns,
				MaxJobTimeoutMs:      file.Tools.Subagent.MaxJobTimeoutMs,
				ReadOnlyIsolation:    normalizeSubagentReadOnlyIsolation(file.Tools.Subagent.ReadOnlyIsolation),
				GitDirtyIsolation:    normalizeSubagentGitDirtyIsolation(file.Tools.Subagent.GitDirtyIsolation),
				NonGitWriteIsolation: normalizeSubagentNonGitWriteIsolation(file.Tools.Subagent.NonGitWriteIsolation),
				WorkspaceTTLHours:    file.Tools.Subagent.WorkspaceTTLHours,
			},
			Execution: ToolExecutionConfig{
				Mode:               file.Tools.Execution.Mode,
				DockerImage:        file.Tools.Execution.DockerImage,
				DockerNetwork:      file.Tools.Execution.DockerNetwork,
				SSHTarget:          file.Tools.Execution.SSHTarget,
				SSHWorkspace:       file.Tools.Execution.SSHWorkspace,
				SSHOptions:         append([]string{}, file.Tools.Execution.SSHOptions...),
				ShellAllowPatterns: append([]string{}, file.Tools.Execution.ShellAllowPatterns...),
				ShellDenyPatterns:  append([]string{}, file.Tools.Execution.ShellDenyPatterns...),
			},
			Browser: BrowserConfig{
				Enabled:              file.Tools.Browser.Enabled,
				Headless:             file.Tools.Browser.Headless,
				BrowserPath:          file.Tools.Browser.BrowserPath,
				CDPURL:               file.Tools.Browser.CDPURL,
				ActionTimeoutSeconds: file.Tools.Browser.ActionTimeoutSeconds,
				IdleTimeoutSeconds:   file.Tools.Browser.IdleTimeoutSeconds,
				MaxPagesPerSession:   file.Tools.Browser.MaxPagesPerSession,
				AllowPrivateHosts:    file.Tools.Browser.AllowPrivateHosts,
			},
			History: HistorySearchConfig{
				Enabled: file.Tools.History.Enabled,
				Auto: HistorySearchAutoConfig{
					Enabled:                   file.Tools.History.Auto.Enabled,
					MaxPerTurn:                file.Tools.History.Auto.MaxPerTurn,
					DefaultScope:              file.Tools.History.Auto.DefaultScope,
					AllowArchiveOnClear:       file.Tools.History.Auto.AllowArchiveOnClear,
					AllowArchiveOnCompact:     file.Tools.History.Auto.AllowArchiveOnCompact,
					AllowAllArchivesAutomatic: file.Tools.History.Auto.AllowAllArchivesAutomatic,
					MinScore:                  file.Tools.History.Auto.MinScore,
				},
				Cues: HistorySearchCueConfig{
					Explicit: append([]string{}, file.Tools.History.Cues.Explicit...),
					Implicit: append([]string{}, file.Tools.History.Cues.Implicit...),
				},
				Blocks: HistorySearchBlockConfig{
					SessionSources: append([]string{}, file.Tools.History.Blocks.SessionSources...),
				},
			},
			Permissions: PermissionConfig{
				BlockAutomationMutations:   file.Tools.Permissions.BlockAutomationMutations,
				InteractiveApprovalEnabled: file.Tools.Permissions.InteractiveApprovalEnabled,
				InteractiveApprovalMode:    file.Tools.Permissions.InteractiveApprovalMode,
				InteractiveApprovalSources: append([]string{}, file.Tools.Permissions.InteractiveApprovalSources...),
				InteractiveApprovalTools:   append([]string{}, file.Tools.Permissions.InteractiveApprovalTools...),
				PendingTTLSeconds:          file.Tools.Permissions.PendingTTLSeconds,
				TrustedPathPrefixes:        append([]string{}, file.Tools.Permissions.TrustedPathPrefixes...),
				TrustedCommandPrefixes:     append([]string{}, file.Tools.Permissions.TrustedCommandPrefixes...),
			},
		},
		Media: MediaConfig{
			Moonshot: MoonshotMediaConfig{
				Enabled: file.Media.Moonshot.Enabled,
				BaseURL: file.Media.Moonshot.BaseURL,
				APIKey:  file.Media.Moonshot.APIKey,
			},
			Document: DocumentMediaConfig{
				MaxChars:      file.Media.Document.MaxChars,
				PDFToTextPath: file.Media.Document.PDFToTextPath,
			},
			OCR: OCRMediaConfig{
				Mode:          file.Media.OCR.Mode,
				TesseractPath: file.Media.OCR.TesseractPath,
				MaxChars:      file.Media.OCR.MaxChars,
			},
			Audio: AudioMediaConfig{
				Enabled:          file.Media.Audio.Enabled,
				FFmpegPath:       file.Media.Audio.FFmpegPath,
				FFprobePath:      file.Media.Audio.FFprobePath,
				WhisperCPPPath:   file.Media.Audio.WhisperCPPPath,
				WhisperModelPath: resolvePath(workspace, file.Media.Audio.WhisperModelPath),
				MaxChars:         file.Media.Audio.MaxChars,
			},
			Video: VideoMediaConfig{
				Enabled:                 file.Media.Video.Enabled,
				KeyframeIntervalSeconds: file.Media.Video.KeyframeIntervalSeconds,
				MaxFrames:               file.Media.Video.MaxFrames,
			},
		},
		ACP: ACPConfig{
			Agents: acpAgentsFromConfigFile(file),
		},
		Feishu: FeishuConfig{
			Enabled:   file.Channels.Feishu.Enabled,
			AppID:     file.Channels.Feishu.AppID,
			AppSecret: file.Channels.Feishu.AppSecret,
			Domain:    file.Channels.Feishu.Domain,
		},
		Weixin: WeixinConfig{
			Enabled:           file.Channels.Weixin.Enabled,
			BaseURL:           file.Channels.Weixin.BaseURL,
			CDNBaseURL:        file.Channels.Weixin.CDNBaseURL,
			AccountID:         file.Channels.Weixin.AccountID,
			AllowFrom:         append([]string{}, file.Channels.Weixin.AllowFrom...),
			RouteTag:          file.Channels.Weixin.RouteTag,
			LongPollTimeoutMs: file.Channels.Weixin.LongPollTimeoutMs,
			Proxy:             file.Channels.Weixin.Proxy,
		},
	}
	return cfg
}

func modelProfilesFromConfigFile(file ConfigFile) map[string]ModelProfileConfig {
	return modelProfilesFromProviders(file, llmProvidersFromConfigFile(file))
}

func modelProfilesFromProviders(file ConfigFile, providers map[string]llm.ProviderConfig) map[string]ModelProfileConfig {
	registry := llm.NewRegistry(providers, file.API.ModelStrategy)
	candidates := registry.AllCandidates()
	profiles := make(map[string]ModelProfileConfig, len(candidates))
	for _, candidate := range candidates {
		profiles[candidate.ProfileID] = modelProfileFromLLMCandidate(candidate)
	}
	return profiles
}

func llmProvidersFromConfigFile(file ConfigFile) map[string]llm.ProviderConfig {
	registry := llm.NewRegistry(file.API.Providers, file.API.ModelStrategy)
	providers := normalizeOpenAICodexProviders(registry.Providers())
	defaults := defaultConfigFile().API
	defaultRefText := strings.TrimSpace(file.API.DefaultModel)
	if defaultRefText == "" {
		defaultRefText = strings.TrimSpace(file.API.DefaultProfile)
	}
	ref, ok := llm.ParseProfileID(defaultRefText)
	if !ok {
		return providers
	}
	provider, ok := providers[ref.Provider]
	if !ok {
		return providers
	}
	model, ok := provider.Models[ref.Model]
	if !ok {
		return providers
	}
	if file.API.TimeoutSeconds > 0 && file.API.TimeoutSeconds != defaults.TimeoutSeconds {
		provider.TimeoutSeconds = file.API.TimeoutSeconds
	}
	provider.Models[ref.Model] = model
	provider = normalizeOpenAICodexProviderModels(provider)
	providers[ref.Provider] = provider
	return providers
}

func normalizeOpenAICodexProviders(providers map[string]llm.ProviderConfig) map[string]llm.ProviderConfig {
	for id, provider := range providers {
		providers[id] = normalizeOpenAICodexProviderModels(provider)
	}
	return providers
}

func applyProviderCredentialEnv(providers map[string]llm.ProviderConfig, dotenvMap map[string]string) {
	for id, provider := range providers {
		envName := strings.TrimSpace(provider.APIKeyEnv)
		if envName == "" {
			switch provider.Type {
			case ProviderOpenAICodex:
				envName = "GODEX_OPENAI_CODEX_OAUTH_TOKEN"
			case ProviderOpenAICompatible:
				envName = "OPENAI_API_KEY"
			}
		}
		if envName != "" {
			if dot := lookupEnvValue(dotenvMap, envName); dot.Set {
				provider.APIKey = dot.Value
			}
			if env := lookupProcessValue(envName); env.Set {
				provider.APIKey = env.Value
			}
		}
		if strings.TrimSpace(provider.CredentialKind) == "" {
			if provider.Type == ProviderOpenAICodex {
				provider.CredentialKind = "oauth-token"
			} else if strings.TrimSpace(envName) != "" || strings.TrimSpace(provider.APIKey) != "" {
				provider.CredentialKind = "api-key"
			}
		}
		providers[id] = provider
	}
}

func llmStrategyFromConfigFile(file ConfigFile) llm.StrategyConfig {
	strategy := llm.NormalizeStrategy(file.API.ModelStrategy)
	if len(strategy.Candidates) == 0 {
		if ref, ok := llm.ParseProfileID(file.API.DefaultModel); ok {
			strategy.Candidates = append(strategy.Candidates, ref)
		}
	}
	if len(strategy.Candidates) == 0 {
		if ref, ok := llm.ParseProfileID(file.API.DefaultProfile); ok {
			strategy.Candidates = append(strategy.Candidates, ref)
		}
	}
	return strategy
}

func isProviderModelProfileID(file ConfigFile, profileID string) bool {
	ref, ok := llm.ParseProfileID(profileID)
	if !ok {
		return false
	}
	provider, ok := file.API.Providers[ref.Provider]
	if !ok {
		return false
	}
	_, ok = provider.Models[ref.Model]
	return ok
}

func normalizeSecurityProfileName(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "trusted-local", "guarded-local", "sandboxed", "strict", "host-privileged", "dev/repair":
		return strings.ToLower(strings.TrimSpace(profile))
	case "dev-repair", "dev_repair", "repair":
		return "dev/repair"
	case "trusted":
		return "trusted-local"
	case "guarded", "":
		return "guarded-local"
	default:
		return "guarded-local"
	}
}

func normalizeSessionStorageBackend(backend string) string {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "sqlite":
		return "sqlite"
	default:
		return "json"
	}
}

func normalizeSubagentReadOnlyIsolation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "snapshot":
		return "snapshot"
	case "shared_readonly", "":
		return "shared_readonly"
	default:
		return "shared_readonly"
	}
}

func normalizeSubagentGitDirtyIsolation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "snapshot":
		return "snapshot"
	case "dirty_overlay", "":
		return "dirty_overlay"
	default:
		return "dirty_overlay"
	}
}

func normalizeSubagentNonGitWriteIsolation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "shared_with_approval", "deny":
		return strings.ToLower(strings.TrimSpace(value))
	case "copy_snapshot", "":
		return "copy_snapshot"
	default:
		return "copy_snapshot"
	}
}

func positiveOrDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func controlNodesFromConfigFile(file ConfigFile) []ControlNodeConfig {
	nodes := make([]ControlNodeConfig, 0, len(file.Control.Nodes))
	for _, item := range file.Control.Nodes {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		nodes = append(nodes, ControlNodeConfig{
			ID:           id,
			Name:         strings.TrimSpace(item.Name),
			Endpoint:     strings.TrimSpace(item.Endpoint),
			WorkspaceDir: strings.TrimSpace(item.WorkspaceDir),
			GodexHome:    strings.TrimSpace(item.GodexHome),
			Version:      strings.TrimSpace(item.Version),
			Capabilities: uniqueNonEmptyStrings(item.Capabilities),
		})
	}
	return nodes
}

func acpAgentsFromConfigFile(file ConfigFile) map[string]ACPAgentConfig {
	agents := make(map[string]ACPAgentConfig, len(file.ACP.Agents))
	for id, item := range file.ACP.Agents {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		env := make(map[string]string, len(item.Env))
		for key, value := range item.Env {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			env[key] = value
		}
		agents[id] = ACPAgentConfig{
			ID:             id,
			Command:        strings.TrimSpace(item.Command),
			Args:           append([]string{}, item.Args...),
			Env:            env,
			TimeoutSeconds: item.TimeoutSeconds,
			Description:    strings.TrimSpace(item.Description),
		}
	}
	return agents
}

func uniqueNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func defaultProfileIDFromProfiles(profiles map[string]ModelProfileConfig) string {
	if _, ok := profiles[defaultProfileID]; ok {
		return defaultProfileID
	}
	keys := make([]string, 0, len(profiles))
	for key := range profiles {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return defaultProfileID
	}
	return keys[0]
}

func maskProfileSecrets(profiles map[string]ModelProfileConfig) map[string]ModelProfileConfig {
	out := make(map[string]ModelProfileConfig, len(profiles))
	for id, profile := range profiles {
		if strings.TrimSpace(profile.APIKey) != "" {
			profile.APIKey = "********"
		}
		out[id] = profile
	}
	return out
}

func maskACPAgentSecrets(agents map[string]ACPAgentConfig) map[string]ACPAgentConfig {
	out := make(map[string]ACPAgentConfig, len(agents))
	for id, agent := range agents {
		if len(agent.Args) > 0 {
			agent.Args = append([]string{}, agent.Args...)
		}
		if len(agent.Env) > 0 {
			env := make(map[string]string, len(agent.Env))
			for key, value := range agent.Env {
				if looksSecretKey(key) && strings.TrimSpace(value) != "" {
					value = "********"
				}
				env[key] = value
			}
			agent.Env = env
		}
		out[id] = agent
	}
	return out
}

func parseConfigFile(path string) (ConfigFile, error) {
	defaults := defaultConfigFile()
	data, err := os.ReadFile(path)
	if err != nil {
		return ConfigFile{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&defaults); err != nil {
		return ConfigFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return defaults, nil
}

func (m *Manager) effectiveConfigFile(homeFile ConfigFile) (ConfigFile, error) {
	if strings.TrimSpace(m.projectConfigPath) == "" || filepath.Clean(m.projectConfigPath) == filepath.Clean(m.homeConfigPath) {
		return homeFile, nil
	}
	if _, err := os.Stat(m.projectConfigPath); err != nil {
		if os.IsNotExist(err) {
			return homeFile, nil
		}
		return ConfigFile{}, err
	}
	return mergeConfigFileLayer(homeFile, m.projectConfigPath)
}

func mergeConfigFileLayer(base ConfigFile, layerPath string) (ConfigFile, error) {
	baseMap, err := configFileToMap(base)
	if err != nil {
		return ConfigFile{}, err
	}
	data, err := os.ReadFile(layerPath)
	if err != nil {
		return ConfigFile{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return base, nil
	}
	validator := ConfigFile{}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&validator); err != nil {
		return ConfigFile{}, fmt.Errorf("parse %s: %w", layerPath, err)
	}
	var layer map[string]any
	if err := yaml.Unmarshal(data, &layer); err != nil {
		return ConfigFile{}, fmt.Errorf("parse %s: %w", layerPath, err)
	}
	deepMergeYAML(baseMap, layer)
	return configFileFromMap(layerPath, baseMap)
}

func configFileToMap(file ConfigFile) (map[string]any, error) {
	data, err := yaml.Marshal(file)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func configFileFromMap(path string, values map[string]any) (ConfigFile, error) {
	data, err := yaml.Marshal(values)
	if err != nil {
		return ConfigFile{}, err
	}
	var file ConfigFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return ConfigFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return file, nil
}

func deepMergeYAML(dst, src map[string]any) {
	for key, srcValue := range src {
		srcMap, srcOK := srcValue.(map[string]any)
		dstMap, dstOK := dst[key].(map[string]any)
		if srcOK && dstOK {
			deepMergeYAML(dstMap, srcMap)
			continue
		}
		dst[key] = srcValue
	}
}

func applyStoredValues(file *ConfigFile, req UpdateRequest) error {
	if file == nil {
		return fmt.Errorf("missing config file state")
	}
	canonicalAPITouched := false
	clearSecrets := make(map[string]struct{}, len(req.ClearSecrets))
	for _, path := range req.ClearSecrets {
		clearSecrets[strings.TrimSpace(path)] = struct{}{}
	}

	for _, section := range baseSchema() {
		for _, field := range section.Fields {
			value, ok := req.Values[field.Path]
			if field.Secret {
				if _, clear := clearSecrets[field.Path]; clear {
					value = ""
					ok = true
				}
				if !ok {
					continue
				}
				if field.Type == "json" {
					if value == nil {
						continue
					}
					if text, isString := value.(string); isString && strings.TrimSpace(text) == "" {
						continue
					}
				} else {
					if asString := strings.TrimSpace(asString(value)); asString == "" {
						continue
					} else {
						value = asString
					}
				}
			}
			if !ok {
				continue
			}
			if isCanonicalAPIPath(field.Path) {
				canonicalAPITouched = true
			}
			if err := setStoredValue(file, field.Path, field.Type, value); err != nil {
				return err
			}
		}
	}
	if canonicalAPITouched {
		clearLegacyAPIFields(file)
	}
	for path := range clearSecrets {
		switch path {
		case "api.providers":
			for id, provider := range file.API.Providers {
				provider.APIKey = ""
				file.API.Providers[id] = provider
			}
		case "web.token":
			file.Web.Token = ""
		case "tools.web_search.brave.api_key":
			file.Tools.WebSearch.Brave.APIKey = ""
		case "tools.web_search.exa.api_key":
			file.Tools.WebSearch.Exa.APIKey = ""
		case "tools.web_search.tavily.api_key":
			file.Tools.WebSearch.Tavily.APIKey = ""
		case "media.moonshot.api_key":
			file.Media.Moonshot.APIKey = ""
		case "channels.feishu.app_id":
			file.Channels.Feishu.AppID = ""
		case "channels.feishu.app_secret":
			file.Channels.Feishu.AppSecret = ""
		}
	}
	return nil
}

func isCanonicalAPIPath(path string) bool {
	switch path {
	case "api.default_model", "api.providers", "api.model_strategy":
		return true
	default:
		return false
	}
}

func clearLegacyAPIFields(file *ConfigFile) {
	file.API.DefaultProfile = ""
	file.API.AutoFallbackEnabled = true
	file.API.TimeoutSeconds = 0
}

func setStoredValue(file *ConfigFile, path, kind string, value any) error {
	if strings.HasPrefix(path, "tools.web_search.browser.engines.") {
		setBrowserEngineFileValue(&file.Tools.WebSearch.Browser, path, value)
		return nil
	}
	switch path {
	case "api.default_model":
		file.API.DefaultModel = asString(value)
	case "api.default_profile":
		file.API.DefaultProfile = asString(value)
	case "api.auto_fallback_enabled":
		file.API.AutoFallbackEnabled = asBool(value)
	case "api.providers":
		providers, err := asLLMProviders(value)
		if err != nil {
			return err
		}
		for id, provider := range providers {
			if strings.TrimSpace(provider.APIKey) == "********" {
				provider.APIKey = file.API.Providers[id].APIKey
				providers[id] = provider
			}
		}
		file.API.Providers = providers
	case "api.model_strategy":
		strategy, err := asLLMStrategy(value)
		if err != nil {
			return err
		}
		file.API.ModelStrategy = strategy
	case "acp.agents":
		agents, err := asACPAgents(value)
		if err != nil {
			return err
		}
		for id, agent := range agents {
			if len(agent.Env) == 0 {
				continue
			}
			for key, envValue := range agent.Env {
				if strings.TrimSpace(envValue) == "********" {
					if existing, ok := file.ACP.Agents[id]; ok && existing.Env != nil {
						agent.Env[key] = existing.Env[key]
					}
				}
			}
			agents[id] = agent
		}
		file.ACP.Agents = agents
	case "api.timeout_seconds":
		file.API.TimeoutSeconds = asInt(value)
	case "agent.compress_threshold":
		file.Agent.CompressThreshold = asInt(value)
	case "agent.compaction.auto_enabled":
		file.Agent.Compaction.AutoEnabled = asBool(value)
	case "agent.compaction.trigger_tokens":
		file.Agent.Compaction.TriggerTokens = asInt(value)
	case "agent.compaction.target_history_tokens":
		file.Agent.Compaction.TargetHistoryTokens = asInt(value)
	case "agent.compaction.mode":
		file.Agent.Compaction.Mode = NormalizeCompactionMode(asString(value))
	case "agent.compaction.model_profile_id":
		file.Agent.Compaction.ModelProfileID = strings.TrimSpace(asString(value))
	case "agent.compaction.max_latency_ms":
		file.Agent.Compaction.MaxLatencyMS = asInt(value)
	case "agent.max_turns":
		file.Agent.MaxTurns = asInt(value)
	case "agent.profile":
		file.Agent.Profile = NormalizeAgentProfile(asString(value))
	case "agent.default_profiles.acp":
		file.Agent.DefaultProfiles.ACP = NormalizeAgentProfile(asString(value))
	case "agent.default_profiles.cli":
		file.Agent.DefaultProfiles.CLI = NormalizeAgentProfile(asString(value))
	case "agent.default_profiles.tui":
		file.Agent.DefaultProfiles.TUI = NormalizeAgentProfile(asString(value))
	case "agent.default_profiles.web":
		file.Agent.DefaultProfiles.Web = NormalizeAgentProfile(asString(value))
	case "agent.default_profiles.weixin":
		file.Agent.DefaultProfiles.Weixin = NormalizeAgentProfile(asString(value))
	case "agent.default_profiles.feishu":
		file.Agent.DefaultProfiles.Feishu = NormalizeAgentProfile(asString(value))
	case "logging.level":
		file.Logging.Level = asString(value)
	case "logging.file_path":
		file.Logging.FilePath = asString(value)
	case "logging.also_stderr":
		file.Logging.AlsoStderr = asBool(value)
	case "web.token":
		file.Web.Token = asString(value)
	case "cron.enabled":
		file.Cron.Enabled = asBool(value)
	case "cron.tick_seconds":
		file.Cron.TickSeconds = asInt(value)
	case "cron.default_timezone":
		file.Cron.DefaultTimezone = asString(value)
	case "cron.max_concurrent_runs":
		file.Cron.MaxConcurrentRuns = asInt(value)
	case "heartbeat.enabled":
		file.Heartbeat.Enabled = asBool(value)
	case "heartbeat.tick_seconds":
		file.Heartbeat.TickSeconds = asInt(value)
	case "heartbeat.checklist_path":
		file.Heartbeat.ChecklistPath = asString(value)
	case "heartbeat.ok_token":
		file.Heartbeat.OKToken = asString(value)
	case "heartbeat.default_interval_seconds":
		file.Heartbeat.DefaultIntervalSeconds = asInt(value)
	case "heartbeat.default_timezone":
		file.Heartbeat.DefaultTimezone = asString(value)
	case "control.node_name":
		file.Control.NodeName = asString(value)
	case "control.center_url":
		file.Control.CenterURL = asString(value)
	case "control.heartbeat_seconds":
		file.Control.HeartbeatSeconds = asInt(value)
	case "control.offline_after_seconds":
		file.Control.OfflineAfterSeconds = asInt(value)
	case "control.nodes":
		file.Control.Nodes = asControlNodeSections(value)
	case "runtime.recovery.auto_resume_interrupted_turns":
		file.Runtime.Recovery.AutoResumeInterruptedTurns = asBool(value)
	case "runtime.recovery.auto_repair_sessions":
		file.Runtime.Recovery.AutoRepairSessions = asBool(value)
	case "security.profile":
		file.Security.Profile = normalizeSecurityProfileName(asString(value))
	case "team.lead_name":
		file.Team.LeadName = asString(value)
	case "team.team_name":
		file.Team.TeamName = asString(value)
	case "team.default_skills":
		file.Team.DefaultSkills = asStringList(value)
	case "team.teammate_work_limit":
		file.Team.TeammateWorkLimit = asInt(value)
	case "team.teammate_poll_seconds":
		file.Team.TeammatePollSeconds = asInt(value)
	case "team.teammate_idle_timeout_seconds":
		file.Team.TeammateIdleTimeoutSecs = asInt(value)
	case "paths.state_dir":
		file.Paths.StateDir = asString(value)
	case "paths.team_dir":
		file.Paths.TeamDir = asString(value)
	case "paths.tasks_dir":
		file.Paths.TasksDir = asString(value)
	case "paths.todos_dir":
		file.Paths.TodosDir = asString(value)
	case "paths.memory_dir":
		file.Paths.MemoryDir = asString(value)
	case "paths.rules_dir":
		file.Paths.RulesDir = asString(value)
	case "paths.skills_dir":
		file.Paths.SkillsDir = asString(value)
	case "paths.mcp_config_path":
		file.Paths.MCPConfigPath = asString(value)
	case "paths.temp_dir":
		file.Paths.TempDir = asString(value)
	case "paths.transcripts_dir":
		file.Paths.TranscriptsDir = asString(value)
	case "paths.sessions_dir":
		file.Paths.SessionsDir = asString(value)
	case "storage.tmp_ttl_hours":
		file.Storage.TmpTTLHours = asInt(value)
	case "storage.artifact_ttl_hours":
		file.Storage.ArtifactTTLHours = asInt(value)
	case "storage.browser_cache_auto_clean":
		file.Storage.BrowserCacheAutoClean = asBool(value)
	case "storage.browser_cache_max_mb":
		file.Storage.BrowserCacheMaxMB = asInt(value)
	case "storage.session_checkpoint_keep_latest":
		file.Storage.SessionCheckpointKeepLatest = asInt(value)
	case "storage.session_checkpoint_ttl_hours":
		file.Storage.SessionCheckpointTTLHours = asInt(value)
	case "storage.session_checkpoint_auto_prune":
		file.Storage.SessionCheckpointAutoPrune = asBool(value)
	case "storage.session_backend":
		file.Storage.SessionBackend = normalizeSessionStorageBackend(asString(value))
	case "storage.sqlite_path":
		file.Storage.SQLitePath = asString(value)
	case "tools.web_search.enabled":
		file.Tools.WebSearch.Enabled = asBool(value)
	case "tools.web_search.provider_order":
		file.Tools.WebSearch.ProviderOrder = asStringList(value)
	case "tools.web_search.cache_ttl_seconds":
		file.Tools.WebSearch.CacheTTLSeconds = asInt(value)
	case "tools.web_search.browser.engine":
		file.Tools.WebSearch.Browser.Engine = asString(value)
	case "tools.web_search.browser.engine_fallback":
		file.Tools.WebSearch.Browser.EngineFallback = asStringList(value)
	case "tools.web_search.browser.wait_network_idle_ms":
		file.Tools.WebSearch.Browser.WaitNetworkIdleMS = asInt(value)
	case "tools.web_search.browser.wait_after_load_ms":
		file.Tools.WebSearch.Browser.WaitAfterLoadMS = asInt(value)
	case "tools.web_search.browser.max_scrolls":
		file.Tools.WebSearch.Browser.MaxScrolls = asInt(value)
	case "tools.web_search.browser.result_timeout_seconds":
		file.Tools.WebSearch.Browser.ResultTimeoutSeconds = asInt(value)
	case "tools.web_search.browser.preferred_hosts":
		file.Tools.WebSearch.Browser.PreferredHosts = asStringList(value)
	case "tools.web_search.brave.api_key":
		file.Tools.WebSearch.Brave.APIKey = asString(value)
	case "tools.web_search.exa.api_key":
		file.Tools.WebSearch.Exa.APIKey = asString(value)
	case "tools.web_search.tavily.api_key":
		file.Tools.WebSearch.Tavily.APIKey = asString(value)
	case "tools.web_fetch.enabled":
		file.Tools.WebFetch.Enabled = asBool(value)
	case "tools.web_fetch.max_chars":
		file.Tools.WebFetch.MaxChars = asInt(value)
	case "tools.web_fetch.timeout_seconds":
		file.Tools.WebFetch.TimeoutSeconds = asInt(value)
	case "tools.web_fetch.policy":
		file.Tools.WebFetch.Policy = asString(value)
	case "tools.web_fetch.allowed_domains":
		file.Tools.WebFetch.AllowedDomains = asStringList(value)
	case "tools.web_fetch.blocked_domains":
		file.Tools.WebFetch.BlockedDomains = asStringList(value)
	case "tools.web_fetch.allow_private_hosts":
		file.Tools.WebFetch.AllowPrivateHosts = asBool(value)
	case "tools.glob.default_max_results":
		file.Tools.Glob.DefaultMaxResults = asInt(value)
	case "tools.subagent.max_batch_size":
		file.Tools.Subagent.MaxBatchSize = asInt(value)
	case "tools.subagent.max_concurrent_jobs":
		file.Tools.Subagent.MaxConcurrentJobs = asInt(value)
	case "tools.subagent.default_max_turns":
		file.Tools.Subagent.DefaultMaxTurns = asInt(value)
	case "tools.subagent.max_job_timeout_ms":
		file.Tools.Subagent.MaxJobTimeoutMs = asInt(value)
	case "tools.subagent.readonly_isolation":
		file.Tools.Subagent.ReadOnlyIsolation = normalizeSubagentReadOnlyIsolation(asString(value))
	case "tools.subagent.git_dirty_isolation":
		file.Tools.Subagent.GitDirtyIsolation = normalizeSubagentGitDirtyIsolation(asString(value))
	case "tools.subagent.non_git_write_isolation":
		file.Tools.Subagent.NonGitWriteIsolation = normalizeSubagentNonGitWriteIsolation(asString(value))
	case "tools.subagent.workspace_ttl_hours":
		file.Tools.Subagent.WorkspaceTTLHours = asInt(value)
	case "tools.execution.mode":
		file.Tools.Execution.Mode = asString(value)
	case "tools.execution.docker_image":
		file.Tools.Execution.DockerImage = asString(value)
	case "tools.execution.docker_network":
		file.Tools.Execution.DockerNetwork = asString(value)
	case "tools.execution.ssh_target":
		file.Tools.Execution.SSHTarget = asString(value)
	case "tools.execution.ssh_workspace":
		file.Tools.Execution.SSHWorkspace = asString(value)
	case "tools.execution.ssh_options":
		file.Tools.Execution.SSHOptions = asStringList(value)
	case "tools.execution.shell_allow_patterns":
		file.Tools.Execution.ShellAllowPatterns = asStringList(value)
	case "tools.execution.shell_deny_patterns":
		file.Tools.Execution.ShellDenyPatterns = asStringList(value)
	case "tools.browser.enabled":
		file.Tools.Browser.Enabled = asBool(value)
	case "tools.browser.headless":
		file.Tools.Browser.Headless = asBool(value)
	case "tools.browser.browser_path":
		file.Tools.Browser.BrowserPath = asString(value)
	case "tools.browser.cdp_url":
		file.Tools.Browser.CDPURL = asString(value)
	case "tools.browser.action_timeout_seconds":
		file.Tools.Browser.ActionTimeoutSeconds = asInt(value)
	case "tools.browser.idle_timeout_seconds":
		file.Tools.Browser.IdleTimeoutSeconds = asInt(value)
	case "tools.browser.max_pages_per_session":
		file.Tools.Browser.MaxPagesPerSession = asInt(value)
	case "tools.browser.allow_private_hosts":
		file.Tools.Browser.AllowPrivateHosts = asBool(value)
	case "tools.history_search.enabled":
		file.Tools.History.Enabled = asBool(value)
	case "tools.history_search.auto.enabled":
		file.Tools.History.Auto.Enabled = asBool(value)
	case "tools.history_search.auto.max_per_turn":
		file.Tools.History.Auto.MaxPerTurn = asInt(value)
	case "tools.history_search.auto.default_scope":
		file.Tools.History.Auto.DefaultScope = asString(value)
	case "tools.history_search.auto.allow_archive_on_clear":
		file.Tools.History.Auto.AllowArchiveOnClear = asBool(value)
	case "tools.history_search.auto.allow_archive_on_compact":
		file.Tools.History.Auto.AllowArchiveOnCompact = asBool(value)
	case "tools.history_search.auto.allow_all_archives_automatic":
		file.Tools.History.Auto.AllowAllArchivesAutomatic = asBool(value)
	case "tools.history_search.auto.min_score":
		file.Tools.History.Auto.MinScore = asInt(value)
	case "tools.history_search.cues.explicit":
		file.Tools.History.Cues.Explicit = asStringList(value)
	case "tools.history_search.cues.implicit":
		file.Tools.History.Cues.Implicit = asStringList(value)
	case "tools.history_search.blocks.session_sources":
		file.Tools.History.Blocks.SessionSources = asStringList(value)
	case "tools.permissions.block_automation_mutations":
		file.Tools.Permissions.BlockAutomationMutations = asBool(value)
	case "tools.permissions.interactive_approval_enabled":
		file.Tools.Permissions.InteractiveApprovalEnabled = asBool(value)
	case "tools.permissions.interactive_approval_mode":
		file.Tools.Permissions.InteractiveApprovalMode = asString(value)
	case "tools.permissions.pending_ttl_seconds":
		file.Tools.Permissions.PendingTTLSeconds = asInt(value)
	case "tools.permissions.interactive_approval_sources":
		file.Tools.Permissions.InteractiveApprovalSources = asStringList(value)
	case "tools.permissions.interactive_approval_tools":
		file.Tools.Permissions.InteractiveApprovalTools = asStringList(value)
	case "tools.permissions.trusted_path_prefixes":
		file.Tools.Permissions.TrustedPathPrefixes = asStringList(value)
	case "tools.permissions.trusted_command_prefixes":
		file.Tools.Permissions.TrustedCommandPrefixes = asStringList(value)
	case "media.moonshot.enabled":
		file.Media.Moonshot.Enabled = asBool(value)
	case "media.moonshot.base_url":
		file.Media.Moonshot.BaseURL = asString(value)
	case "media.moonshot.api_key":
		file.Media.Moonshot.APIKey = asString(value)
	case "media.document.max_chars":
		file.Media.Document.MaxChars = asInt(value)
	case "media.document.pdftotext_path":
		file.Media.Document.PDFToTextPath = asString(value)
	case "media.ocr.mode":
		file.Media.OCR.Mode = asString(value)
	case "media.ocr.tesseract_path":
		file.Media.OCR.TesseractPath = asString(value)
	case "media.ocr.max_chars":
		file.Media.OCR.MaxChars = asInt(value)
	case "media.audio.enabled":
		file.Media.Audio.Enabled = asBool(value)
	case "media.audio.ffmpeg_path":
		file.Media.Audio.FFmpegPath = asString(value)
	case "media.audio.ffprobe_path":
		file.Media.Audio.FFprobePath = asString(value)
	case "media.audio.whisper_cpp_path":
		file.Media.Audio.WhisperCPPPath = asString(value)
	case "media.audio.whisper_model_path":
		file.Media.Audio.WhisperModelPath = asString(value)
	case "media.audio.max_chars":
		file.Media.Audio.MaxChars = asInt(value)
	case "media.video.enabled":
		file.Media.Video.Enabled = asBool(value)
	case "media.video.keyframe_interval_seconds":
		file.Media.Video.KeyframeIntervalSeconds = asInt(value)
	case "media.video.max_frames":
		file.Media.Video.MaxFrames = asInt(value)
	case "channels.feishu.enabled":
		file.Channels.Feishu.Enabled = asBool(value)
	case "channels.feishu.app_id":
		file.Channels.Feishu.AppID = asString(value)
	case "channels.feishu.app_secret":
		file.Channels.Feishu.AppSecret = asString(value)
	case "channels.feishu.domain":
		file.Channels.Feishu.Domain = asString(value)
	case "channels.weixin.enabled":
		file.Channels.Weixin.Enabled = asBool(value)
	case "channels.weixin.base_url":
		file.Channels.Weixin.BaseURL = asString(value)
	case "channels.weixin.cdn_base_url":
		file.Channels.Weixin.CDNBaseURL = asString(value)
	case "channels.weixin.account_id":
		file.Channels.Weixin.AccountID = asString(value)
	case "channels.weixin.allow_from":
		file.Channels.Weixin.AllowFrom = asStringList(value)
	case "channels.weixin.route_tag":
		file.Channels.Weixin.RouteTag = asString(value)
	case "channels.weixin.long_poll_timeout_ms":
		file.Channels.Weixin.LongPollTimeoutMs = asInt(value)
	case "channels.weixin.proxy":
		file.Channels.Weixin.Proxy = asString(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func asLLMProviders(value any) (map[string]llm.ProviderConfig, error) {
	if value == nil {
		return map[string]llm.ProviderConfig{}, nil
	}
	var data []byte
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return map[string]llm.ProviderConfig{}, nil
		}
		data = []byte(trimmed)
	default:
		var err error
		data, err = yaml.Marshal(typed)
		if err != nil {
			return nil, fmt.Errorf("invalid api.providers: %w", err)
		}
	}
	var providers map[string]llm.ProviderConfig
	if err := yaml.Unmarshal(data, &providers); err != nil {
		return nil, fmt.Errorf("invalid api.providers: %w", err)
	}
	return llm.NewRegistry(providers, llm.StrategyConfig{}).Providers(), nil
}

func asLLMStrategy(value any) (llm.StrategyConfig, error) {
	if value == nil {
		return llm.StrategyConfig{}, nil
	}
	var data []byte
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return llm.StrategyConfig{}, nil
		}
		data = []byte(trimmed)
	default:
		var err error
		data, err = yaml.Marshal(typed)
		if err != nil {
			return llm.StrategyConfig{}, fmt.Errorf("invalid api.model_strategy: %w", err)
		}
	}
	var strategy llm.StrategyConfig
	if err := yaml.Unmarshal(data, &strategy); err != nil {
		return llm.StrategyConfig{}, fmt.Errorf("invalid api.model_strategy: %w", err)
	}
	return llm.NormalizeStrategy(strategy), nil
}

func asACPAgents(value any) (map[string]ACPAgentSection, error) {
	if value == nil {
		return map[string]ACPAgentSection{}, nil
	}
	var data []byte
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return map[string]ACPAgentSection{}, nil
		}
		data = []byte(trimmed)
	default:
		var err error
		data, err = yaml.Marshal(typed)
		if err != nil {
			return nil, fmt.Errorf("invalid acp.agents: %w", err)
		}
	}
	var agents map[string]ACPAgentSection
	if err := yaml.Unmarshal(data, &agents); err != nil {
		return nil, fmt.Errorf("invalid acp.agents: %w", err)
	}
	cleaned := make(map[string]ACPAgentSection, len(agents))
	for id, agent := range agents {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		agent.Command = strings.TrimSpace(agent.Command)
		if agent.Env == nil {
			agent.Env = map[string]string{}
		}
		if agent.TimeoutSeconds <= 0 {
			agent.TimeoutSeconds = 600
		}
		cleaned[id] = agent
	}
	return cleaned, nil
}

func asControlNodeSections(value any) []ControlNodeSection {
	if value == nil {
		return nil
	}
	var data []byte
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		data = []byte(trimmed)
	default:
		var err error
		data, err = yaml.Marshal(typed)
		if err != nil {
			return nil
		}
	}
	var nodes []ControlNodeSection
	if err := yaml.Unmarshal(data, &nodes); err != nil {
		return nil
	}
	out := make([]ControlNodeSection, 0, len(nodes))
	for _, node := range nodes {
		node.ID = strings.TrimSpace(node.ID)
		if node.ID == "" {
			continue
		}
		node.Name = strings.TrimSpace(node.Name)
		node.Endpoint = strings.TrimSpace(node.Endpoint)
		node.WorkspaceDir = strings.TrimSpace(node.WorkspaceDir)
		node.GodexHome = strings.TrimSpace(node.GodexHome)
		node.Version = strings.TrimSpace(node.Version)
		node.Capabilities = uniqueNonEmptyStrings(node.Capabilities)
		out = append(out, node)
	}
	return out
}

func maskLLMProviders(providers map[string]llm.ProviderConfig) map[string]llm.ProviderConfig {
	out := make(map[string]llm.ProviderConfig, len(providers))
	for id, provider := range providers {
		if strings.TrimSpace(provider.APIKey) != "" {
			provider.APIKey = "********"
		}
		if len(provider.Models) > 0 {
			models := make(map[string]llm.ModelConfig, len(provider.Models))
			for modelID, model := range provider.Models {
				model.Tags = append([]string{}, model.Tags...)
				models[modelID] = model
			}
			provider.Models = models
		}
		out[id] = provider
	}
	return out
}

func maskACPAgentSections(agents map[string]ACPAgentSection) map[string]ACPAgentSection {
	out := make(map[string]ACPAgentSection, len(agents))
	for id, agent := range agents {
		if len(agent.Args) > 0 {
			agent.Args = append([]string{}, agent.Args...)
		}
		if len(agent.Env) > 0 {
			env := make(map[string]string, len(agent.Env))
			for key, value := range agent.Env {
				if looksSecretKey(key) && strings.TrimSpace(value) != "" {
					value = "********"
				}
				env[key] = value
			}
			agent.Env = env
		}
		out[id] = agent
	}
	return out
}

func looksSecretKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(key, "key") || strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password")
}

func storedValues(file ConfigFile) map[string]any {
	values := map[string]any{
		"api.default_model":                                      file.API.DefaultModel,
		"api.default_profile":                                    file.API.DefaultProfile,
		"api.auto_fallback_enabled":                              file.API.AutoFallbackEnabled,
		"api.providers":                                          maskLLMProviders(llmProvidersFromConfigFile(file)),
		"api.model_strategy":                                     llmStrategyFromConfigFile(file),
		"acp.agents":                                             maskACPAgentSections(file.ACP.Agents),
		"api.timeout_seconds":                                    file.API.TimeoutSeconds,
		"agent.compress_threshold":                               file.Agent.CompressThreshold,
		"agent.compaction.auto_enabled":                          file.Agent.Compaction.AutoEnabled,
		"agent.compaction.trigger_tokens":                        file.Agent.Compaction.TriggerTokens,
		"agent.compaction.target_history_tokens":                 file.Agent.Compaction.TargetHistoryTokens,
		"agent.compaction.mode":                                  NormalizeCompactionMode(file.Agent.Compaction.Mode),
		"agent.compaction.model_profile_id":                      strings.TrimSpace(file.Agent.Compaction.ModelProfileID),
		"agent.compaction.max_latency_ms":                        file.Agent.Compaction.MaxLatencyMS,
		"agent.max_turns":                                        file.Agent.MaxTurns,
		"agent.profile":                                          NormalizeAgentProfile(file.Agent.Profile),
		"agent.default_profiles.acp":                             NormalizeAgentProfile(file.Agent.DefaultProfiles.ACP),
		"agent.default_profiles.cli":                             NormalizeAgentProfile(file.Agent.DefaultProfiles.CLI),
		"agent.default_profiles.tui":                             NormalizeAgentProfile(file.Agent.DefaultProfiles.TUI),
		"agent.default_profiles.web":                             NormalizeAgentProfile(file.Agent.DefaultProfiles.Web),
		"agent.default_profiles.weixin":                          NormalizeAgentProfile(file.Agent.DefaultProfiles.Weixin),
		"agent.default_profiles.feishu":                          NormalizeAgentProfile(file.Agent.DefaultProfiles.Feishu),
		"logging.level":                                          file.Logging.Level,
		"logging.file_path":                                      file.Logging.FilePath,
		"logging.also_stderr":                                    file.Logging.AlsoStderr,
		"web.token":                                              "",
		"cron.enabled":                                           file.Cron.Enabled,
		"cron.tick_seconds":                                      file.Cron.TickSeconds,
		"cron.default_timezone":                                  file.Cron.DefaultTimezone,
		"cron.max_concurrent_runs":                               file.Cron.MaxConcurrentRuns,
		"heartbeat.enabled":                                      file.Heartbeat.Enabled,
		"heartbeat.tick_seconds":                                 file.Heartbeat.TickSeconds,
		"heartbeat.checklist_path":                               file.Heartbeat.ChecklistPath,
		"heartbeat.ok_token":                                     file.Heartbeat.OKToken,
		"heartbeat.default_interval_seconds":                     file.Heartbeat.DefaultIntervalSeconds,
		"heartbeat.default_timezone":                             file.Heartbeat.DefaultTimezone,
		"control.node_name":                                      file.Control.NodeName,
		"control.center_url":                                     file.Control.CenterURL,
		"control.heartbeat_seconds":                              file.Control.HeartbeatSeconds,
		"control.offline_after_seconds":                          file.Control.OfflineAfterSeconds,
		"control.nodes":                                          append([]ControlNodeSection{}, file.Control.Nodes...),
		"runtime.recovery.auto_resume_interrupted_turns":         file.Runtime.Recovery.AutoResumeInterruptedTurns,
		"runtime.recovery.auto_repair_sessions":                  file.Runtime.Recovery.AutoRepairSessions,
		"security.profile":                                       normalizeSecurityProfileName(file.Security.Profile),
		"team.lead_name":                                         file.Team.LeadName,
		"team.team_name":                                         file.Team.TeamName,
		"team.default_skills":                                    append([]string{}, file.Team.DefaultSkills...),
		"team.teammate_work_limit":                               file.Team.TeammateWorkLimit,
		"team.teammate_poll_seconds":                             file.Team.TeammatePollSeconds,
		"team.teammate_idle_timeout_seconds":                     file.Team.TeammateIdleTimeoutSecs,
		"paths.state_dir":                                        file.Paths.StateDir,
		"paths.team_dir":                                         file.Paths.TeamDir,
		"paths.tasks_dir":                                        file.Paths.TasksDir,
		"paths.todos_dir":                                        file.Paths.TodosDir,
		"paths.memory_dir":                                       file.Paths.MemoryDir,
		"paths.rules_dir":                                        file.Paths.RulesDir,
		"paths.skills_dir":                                       file.Paths.SkillsDir,
		"paths.mcp_config_path":                                  file.Paths.MCPConfigPath,
		"paths.temp_dir":                                         file.Paths.TempDir,
		"paths.transcripts_dir":                                  file.Paths.TranscriptsDir,
		"paths.sessions_dir":                                     file.Paths.SessionsDir,
		"storage.tmp_ttl_hours":                                  file.Storage.TmpTTLHours,
		"storage.artifact_ttl_hours":                             file.Storage.ArtifactTTLHours,
		"storage.browser_cache_auto_clean":                       file.Storage.BrowserCacheAutoClean,
		"storage.browser_cache_max_mb":                           file.Storage.BrowserCacheMaxMB,
		"storage.session_checkpoint_keep_latest":                 file.Storage.SessionCheckpointKeepLatest,
		"storage.session_checkpoint_ttl_hours":                   file.Storage.SessionCheckpointTTLHours,
		"storage.session_checkpoint_auto_prune":                  file.Storage.SessionCheckpointAutoPrune,
		"storage.session_backend":                                normalizeSessionStorageBackend(file.Storage.SessionBackend),
		"storage.sqlite_path":                                    file.Storage.SQLitePath,
		"tools.web_search.enabled":                               file.Tools.WebSearch.Enabled,
		"tools.web_search.provider_order":                        append([]string{}, file.Tools.WebSearch.ProviderOrder...),
		"tools.web_search.cache_ttl_seconds":                     file.Tools.WebSearch.CacheTTLSeconds,
		"tools.web_search.browser.engine":                        file.Tools.WebSearch.Browser.Engine,
		"tools.web_search.browser.engine_fallback":               append([]string{}, file.Tools.WebSearch.Browser.EngineFallback...),
		"tools.web_search.browser.wait_network_idle_ms":          file.Tools.WebSearch.Browser.WaitNetworkIdleMS,
		"tools.web_search.browser.wait_after_load_ms":            file.Tools.WebSearch.Browser.WaitAfterLoadMS,
		"tools.web_search.browser.max_scrolls":                   file.Tools.WebSearch.Browser.MaxScrolls,
		"tools.web_search.browser.result_timeout_seconds":        file.Tools.WebSearch.Browser.ResultTimeoutSeconds,
		"tools.web_search.browser.preferred_hosts":               append([]string{}, file.Tools.WebSearch.Browser.PreferredHosts...),
		"tools.web_search.brave.api_key":                         "",
		"tools.web_search.exa.api_key":                           "",
		"tools.web_search.tavily.api_key":                        "",
		"tools.web_fetch.enabled":                                file.Tools.WebFetch.Enabled,
		"tools.web_fetch.max_chars":                              file.Tools.WebFetch.MaxChars,
		"tools.web_fetch.timeout_seconds":                        file.Tools.WebFetch.TimeoutSeconds,
		"tools.web_fetch.policy":                                 file.Tools.WebFetch.Policy,
		"tools.web_fetch.allowed_domains":                        append([]string{}, file.Tools.WebFetch.AllowedDomains...),
		"tools.web_fetch.blocked_domains":                        append([]string{}, file.Tools.WebFetch.BlockedDomains...),
		"tools.web_fetch.allow_private_hosts":                    file.Tools.WebFetch.AllowPrivateHosts,
		"tools.glob.default_max_results":                         file.Tools.Glob.DefaultMaxResults,
		"tools.subagent.max_batch_size":                          file.Tools.Subagent.MaxBatchSize,
		"tools.subagent.max_concurrent_jobs":                     file.Tools.Subagent.MaxConcurrentJobs,
		"tools.subagent.default_max_turns":                       file.Tools.Subagent.DefaultMaxTurns,
		"tools.subagent.max_job_timeout_ms":                      file.Tools.Subagent.MaxJobTimeoutMs,
		"tools.subagent.readonly_isolation":                      normalizeSubagentReadOnlyIsolation(file.Tools.Subagent.ReadOnlyIsolation),
		"tools.subagent.git_dirty_isolation":                     normalizeSubagentGitDirtyIsolation(file.Tools.Subagent.GitDirtyIsolation),
		"tools.subagent.non_git_write_isolation":                 normalizeSubagentNonGitWriteIsolation(file.Tools.Subagent.NonGitWriteIsolation),
		"tools.subagent.workspace_ttl_hours":                     file.Tools.Subagent.WorkspaceTTLHours,
		"tools.execution.mode":                                   file.Tools.Execution.Mode,
		"tools.execution.docker_image":                           file.Tools.Execution.DockerImage,
		"tools.execution.docker_network":                         file.Tools.Execution.DockerNetwork,
		"tools.execution.ssh_target":                             file.Tools.Execution.SSHTarget,
		"tools.execution.ssh_workspace":                          file.Tools.Execution.SSHWorkspace,
		"tools.execution.ssh_options":                            append([]string{}, file.Tools.Execution.SSHOptions...),
		"tools.execution.shell_allow_patterns":                   append([]string{}, file.Tools.Execution.ShellAllowPatterns...),
		"tools.execution.shell_deny_patterns":                    append([]string{}, file.Tools.Execution.ShellDenyPatterns...),
		"tools.browser.enabled":                                  file.Tools.Browser.Enabled,
		"tools.browser.headless":                                 file.Tools.Browser.Headless,
		"tools.browser.browser_path":                             file.Tools.Browser.BrowserPath,
		"tools.browser.cdp_url":                                  file.Tools.Browser.CDPURL,
		"tools.browser.action_timeout_seconds":                   file.Tools.Browser.ActionTimeoutSeconds,
		"tools.browser.idle_timeout_seconds":                     file.Tools.Browser.IdleTimeoutSeconds,
		"tools.browser.max_pages_per_session":                    file.Tools.Browser.MaxPagesPerSession,
		"tools.browser.allow_private_hosts":                      file.Tools.Browser.AllowPrivateHosts,
		"tools.history_search.enabled":                           file.Tools.History.Enabled,
		"tools.history_search.auto.enabled":                      file.Tools.History.Auto.Enabled,
		"tools.history_search.auto.max_per_turn":                 file.Tools.History.Auto.MaxPerTurn,
		"tools.history_search.auto.default_scope":                file.Tools.History.Auto.DefaultScope,
		"tools.history_search.auto.allow_archive_on_clear":       file.Tools.History.Auto.AllowArchiveOnClear,
		"tools.history_search.auto.allow_archive_on_compact":     file.Tools.History.Auto.AllowArchiveOnCompact,
		"tools.history_search.auto.allow_all_archives_automatic": file.Tools.History.Auto.AllowAllArchivesAutomatic,
		"tools.history_search.auto.min_score":                    file.Tools.History.Auto.MinScore,
		"tools.history_search.cues.explicit":                     append([]string{}, file.Tools.History.Cues.Explicit...),
		"tools.history_search.cues.implicit":                     append([]string{}, file.Tools.History.Cues.Implicit...),
		"tools.history_search.blocks.session_sources":            append([]string{}, file.Tools.History.Blocks.SessionSources...),
		"tools.permissions.block_automation_mutations":           file.Tools.Permissions.BlockAutomationMutations,
		"tools.permissions.interactive_approval_enabled":         file.Tools.Permissions.InteractiveApprovalEnabled,
		"tools.permissions.interactive_approval_mode":            file.Tools.Permissions.InteractiveApprovalMode,
		"tools.permissions.pending_ttl_seconds":                  file.Tools.Permissions.PendingTTLSeconds,
		"tools.permissions.interactive_approval_sources":         append([]string{}, file.Tools.Permissions.InteractiveApprovalSources...),
		"tools.permissions.interactive_approval_tools":           append([]string{}, file.Tools.Permissions.InteractiveApprovalTools...),
		"tools.permissions.trusted_path_prefixes":                append([]string{}, file.Tools.Permissions.TrustedPathPrefixes...),
		"tools.permissions.trusted_command_prefixes":             append([]string{}, file.Tools.Permissions.TrustedCommandPrefixes...),
		"media.moonshot.enabled":                                 file.Media.Moonshot.Enabled,
		"media.moonshot.base_url":                                file.Media.Moonshot.BaseURL,
		"media.moonshot.api_key":                                 "",
		"media.document.max_chars":                               file.Media.Document.MaxChars,
		"media.document.pdftotext_path":                          file.Media.Document.PDFToTextPath,
		"media.ocr.mode":                                         file.Media.OCR.Mode,
		"media.ocr.tesseract_path":                               file.Media.OCR.TesseractPath,
		"media.ocr.max_chars":                                    file.Media.OCR.MaxChars,
		"media.audio.enabled":                                    file.Media.Audio.Enabled,
		"media.audio.ffmpeg_path":                                file.Media.Audio.FFmpegPath,
		"media.audio.ffprobe_path":                               file.Media.Audio.FFprobePath,
		"media.audio.whisper_cpp_path":                           file.Media.Audio.WhisperCPPPath,
		"media.audio.whisper_model_path":                         file.Media.Audio.WhisperModelPath,
		"media.audio.max_chars":                                  file.Media.Audio.MaxChars,
		"media.video.enabled":                                    file.Media.Video.Enabled,
		"media.video.keyframe_interval_seconds":                  file.Media.Video.KeyframeIntervalSeconds,
		"media.video.max_frames":                                 file.Media.Video.MaxFrames,
		"channels.feishu.enabled":                                file.Channels.Feishu.Enabled,
		"channels.feishu.app_id":                                 "",
		"channels.feishu.app_secret":                             "",
		"channels.feishu.domain":                                 file.Channels.Feishu.Domain,
		"channels.weixin.enabled":                                file.Channels.Weixin.Enabled,
		"channels.weixin.base_url":                               file.Channels.Weixin.BaseURL,
		"channels.weixin.cdn_base_url":                           file.Channels.Weixin.CDNBaseURL,
		"channels.weixin.account_id":                             file.Channels.Weixin.AccountID,
		"channels.weixin.allow_from":                             append([]string{}, file.Channels.Weixin.AllowFrom...),
		"channels.weixin.route_tag":                              file.Channels.Weixin.RouteTag,
		"channels.weixin.long_poll_timeout_ms":                   file.Channels.Weixin.LongPollTimeoutMs,
		"channels.weixin.proxy":                                  file.Channels.Weixin.Proxy,
	}
	addBrowserEngineFileValues(values, file.Tools.WebSearch.Browser.Engines)
	return values
}

func effectiveValues(cfg *Config) map[string]any {
	if cfg == nil {
		return map[string]any{}
	}
	values := map[string]any{
		"api.default_model":                                      cfg.DefaultModelRef,
		"api.default_profile":                                    cfg.DefaultProfileID,
		"api.auto_fallback_enabled":                              cfg.AutoFallbackEnabled,
		"api.providers":                                          maskLLMProviders(cfg.LLMProviders),
		"api.model_strategy":                                     cfg.LLMStrategy,
		"acp.agents":                                             maskACPAgentSecrets(cfg.ACP.Agents),
		"api.timeout_seconds":                                    cfg.APITimeoutSeconds,
		"agent.compress_threshold":                               cfg.CompressThreshold,
		"agent.compaction.auto_enabled":                          cfg.Compaction.AutoEnabled,
		"agent.compaction.trigger_tokens":                        cfg.Compaction.TriggerTokens,
		"agent.compaction.target_history_tokens":                 cfg.Compaction.TargetHistoryTokens,
		"agent.compaction.mode":                                  cfg.Compaction.Mode,
		"agent.compaction.model_profile_id":                      cfg.Compaction.ModelProfileID,
		"agent.compaction.max_latency_ms":                        cfg.Compaction.MaxLatencyMS,
		"agent.max_turns":                                        cfg.MaxTurns,
		"agent.profile":                                          cfg.AgentProfile,
		"agent.default_profiles.acp":                             cfg.AgentDefaultProfiles.ACP,
		"agent.default_profiles.cli":                             cfg.AgentDefaultProfiles.CLI,
		"agent.default_profiles.tui":                             cfg.AgentDefaultProfiles.TUI,
		"agent.default_profiles.web":                             cfg.AgentDefaultProfiles.Web,
		"agent.default_profiles.weixin":                          cfg.AgentDefaultProfiles.Weixin,
		"agent.default_profiles.feishu":                          cfg.AgentDefaultProfiles.Feishu,
		"logging.level":                                          cfg.Logging.Level,
		"logging.file_path":                                      cfg.Logging.FilePath,
		"logging.also_stderr":                                    cfg.Logging.AlsoStderr,
		"web.token":                                              cfg.WebToken,
		"cron.enabled":                                           cfg.Cron.Enabled,
		"cron.tick_seconds":                                      cfg.Cron.TickSeconds,
		"cron.default_timezone":                                  cfg.Cron.DefaultTimezone,
		"cron.max_concurrent_runs":                               cfg.Cron.MaxConcurrentRuns,
		"heartbeat.enabled":                                      cfg.Heartbeat.Enabled,
		"heartbeat.tick_seconds":                                 cfg.Heartbeat.TickSeconds,
		"heartbeat.checklist_path":                               cfg.Heartbeat.ChecklistPath,
		"heartbeat.ok_token":                                     cfg.Heartbeat.OKToken,
		"heartbeat.default_interval_seconds":                     cfg.Heartbeat.DefaultIntervalSeconds,
		"heartbeat.default_timezone":                             cfg.Heartbeat.DefaultTimezone,
		"control.node_name":                                      cfg.Control.NodeName,
		"control.center_url":                                     cfg.Control.CenterURL,
		"control.heartbeat_seconds":                              cfg.Control.HeartbeatSeconds,
		"control.offline_after_seconds":                          cfg.Control.OfflineAfterSeconds,
		"control.nodes":                                          append([]ControlNodeConfig{}, cfg.Control.Nodes...),
		"runtime.recovery.auto_resume_interrupted_turns":         cfg.Runtime.Recovery.AutoResumeInterruptedTurns,
		"runtime.recovery.auto_repair_sessions":                  cfg.Runtime.Recovery.AutoRepairSessions,
		"security.profile":                                       cfg.Security.Profile,
		"team.lead_name":                                         cfg.LeadName,
		"team.team_name":                                         cfg.TeamName,
		"team.default_skills":                                    append([]string{}, cfg.DefaultSkills...),
		"team.teammate_work_limit":                               cfg.TeammateWorkLimit,
		"team.teammate_poll_seconds":                             int(cfg.TeammatePollEvery / time.Second),
		"team.teammate_idle_timeout_seconds":                     int(cfg.TeammateIdleFor / time.Second),
		"paths.state_dir":                                        cfg.StateDir,
		"paths.team_dir":                                         cfg.TeamDir,
		"paths.tasks_dir":                                        cfg.TasksDir,
		"paths.todos_dir":                                        cfg.TodosDir,
		"paths.memory_dir":                                       cfg.MemoryDir,
		"paths.rules_dir":                                        cfg.RulesDir,
		"paths.skills_dir":                                       cfg.SkillsDir,
		"paths.mcp_config_path":                                  cfg.MCPConfigPath,
		"paths.temp_dir":                                         cfg.TempDir,
		"paths.transcripts_dir":                                  cfg.TranscriptsDir,
		"paths.sessions_dir":                                     cfg.SessionsDir,
		"storage.tmp_ttl_hours":                                  cfg.Storage.TmpTTLHours,
		"storage.artifact_ttl_hours":                             cfg.Storage.ArtifactTTLHours,
		"storage.browser_cache_auto_clean":                       cfg.Storage.BrowserCacheAutoClean,
		"storage.browser_cache_max_mb":                           cfg.Storage.BrowserCacheMaxMB,
		"storage.session_checkpoint_keep_latest":                 cfg.Storage.SessionCheckpointKeepLatest,
		"storage.session_checkpoint_ttl_hours":                   cfg.Storage.SessionCheckpointTTLHours,
		"storage.session_checkpoint_auto_prune":                  cfg.Storage.SessionCheckpointAutoPrune,
		"storage.session_backend":                                normalizeSessionStorageBackend(cfg.Storage.SessionBackend),
		"storage.sqlite_path":                                    cfg.Storage.SQLitePath,
		"tools.web_search.enabled":                               cfg.Tools.WebSearch.Enabled,
		"tools.web_search.provider_order":                        append([]string{}, cfg.Tools.WebSearch.ProviderOrder...),
		"tools.web_search.cache_ttl_seconds":                     cfg.Tools.WebSearch.CacheTTLSeconds,
		"tools.web_search.browser.engine":                        cfg.Tools.WebSearch.Browser.Engine,
		"tools.web_search.browser.engine_fallback":               append([]string{}, cfg.Tools.WebSearch.Browser.EngineFallback...),
		"tools.web_search.browser.wait_network_idle_ms":          cfg.Tools.WebSearch.Browser.WaitNetworkIdleMS,
		"tools.web_search.browser.wait_after_load_ms":            cfg.Tools.WebSearch.Browser.WaitAfterLoadMS,
		"tools.web_search.browser.max_scrolls":                   cfg.Tools.WebSearch.Browser.MaxScrolls,
		"tools.web_search.browser.result_timeout_seconds":        cfg.Tools.WebSearch.Browser.ResultTimeoutSeconds,
		"tools.web_search.browser.preferred_hosts":               append([]string{}, cfg.Tools.WebSearch.Browser.PreferredHosts...),
		"tools.web_search.brave.api_key":                         cfg.Tools.WebSearch.BraveAPIKey,
		"tools.web_search.exa.api_key":                           cfg.Tools.WebSearch.ExaAPIKey,
		"tools.web_search.tavily.api_key":                        cfg.Tools.WebSearch.TavilyAPIKey,
		"tools.web_fetch.enabled":                                cfg.Tools.WebFetch.Enabled,
		"tools.web_fetch.max_chars":                              cfg.Tools.WebFetch.MaxChars,
		"tools.web_fetch.timeout_seconds":                        cfg.Tools.WebFetch.TimeoutSeconds,
		"tools.web_fetch.policy":                                 cfg.Tools.WebFetch.Policy,
		"tools.web_fetch.allowed_domains":                        append([]string{}, cfg.Tools.WebFetch.AllowedDomains...),
		"tools.web_fetch.blocked_domains":                        append([]string{}, cfg.Tools.WebFetch.BlockedDomains...),
		"tools.web_fetch.allow_private_hosts":                    cfg.Tools.WebFetch.AllowPrivateHosts,
		"tools.glob.default_max_results":                         cfg.Tools.Glob.DefaultMaxResults,
		"tools.subagent.max_batch_size":                          cfg.Tools.Subagent.MaxBatchSize,
		"tools.subagent.max_concurrent_jobs":                     cfg.Tools.Subagent.MaxConcurrentJobs,
		"tools.subagent.default_max_turns":                       cfg.Tools.Subagent.DefaultMaxTurns,
		"tools.subagent.max_job_timeout_ms":                      cfg.Tools.Subagent.MaxJobTimeoutMs,
		"tools.subagent.readonly_isolation":                      cfg.Tools.Subagent.ReadOnlyIsolation,
		"tools.subagent.git_dirty_isolation":                     cfg.Tools.Subagent.GitDirtyIsolation,
		"tools.subagent.non_git_write_isolation":                 cfg.Tools.Subagent.NonGitWriteIsolation,
		"tools.subagent.workspace_ttl_hours":                     cfg.Tools.Subagent.WorkspaceTTLHours,
		"tools.execution.mode":                                   cfg.Tools.Execution.Mode,
		"tools.execution.docker_image":                           cfg.Tools.Execution.DockerImage,
		"tools.execution.docker_network":                         cfg.Tools.Execution.DockerNetwork,
		"tools.execution.ssh_target":                             cfg.Tools.Execution.SSHTarget,
		"tools.execution.ssh_workspace":                          cfg.Tools.Execution.SSHWorkspace,
		"tools.execution.ssh_options":                            append([]string{}, cfg.Tools.Execution.SSHOptions...),
		"tools.execution.shell_allow_patterns":                   append([]string{}, cfg.Tools.Execution.ShellAllowPatterns...),
		"tools.execution.shell_deny_patterns":                    append([]string{}, cfg.Tools.Execution.ShellDenyPatterns...),
		"tools.browser.enabled":                                  cfg.Tools.Browser.Enabled,
		"tools.browser.headless":                                 cfg.Tools.Browser.Headless,
		"tools.browser.browser_path":                             cfg.Tools.Browser.BrowserPath,
		"tools.browser.cdp_url":                                  cfg.Tools.Browser.CDPURL,
		"tools.browser.action_timeout_seconds":                   cfg.Tools.Browser.ActionTimeoutSeconds,
		"tools.browser.idle_timeout_seconds":                     cfg.Tools.Browser.IdleTimeoutSeconds,
		"tools.browser.max_pages_per_session":                    cfg.Tools.Browser.MaxPagesPerSession,
		"tools.browser.allow_private_hosts":                      cfg.Tools.Browser.AllowPrivateHosts,
		"tools.history_search.enabled":                           cfg.Tools.History.Enabled,
		"tools.history_search.auto.enabled":                      cfg.Tools.History.Auto.Enabled,
		"tools.history_search.auto.max_per_turn":                 cfg.Tools.History.Auto.MaxPerTurn,
		"tools.history_search.auto.default_scope":                cfg.Tools.History.Auto.DefaultScope,
		"tools.history_search.auto.allow_archive_on_clear":       cfg.Tools.History.Auto.AllowArchiveOnClear,
		"tools.history_search.auto.allow_archive_on_compact":     cfg.Tools.History.Auto.AllowArchiveOnCompact,
		"tools.history_search.auto.allow_all_archives_automatic": cfg.Tools.History.Auto.AllowAllArchivesAutomatic,
		"tools.history_search.auto.min_score":                    cfg.Tools.History.Auto.MinScore,
		"tools.history_search.cues.explicit":                     append([]string{}, cfg.Tools.History.Cues.Explicit...),
		"tools.history_search.cues.implicit":                     append([]string{}, cfg.Tools.History.Cues.Implicit...),
		"tools.history_search.blocks.session_sources":            append([]string{}, cfg.Tools.History.Blocks.SessionSources...),
		"tools.permissions.block_automation_mutations":           cfg.Tools.Permissions.BlockAutomationMutations,
		"tools.permissions.interactive_approval_enabled":         cfg.Tools.Permissions.InteractiveApprovalEnabled,
		"tools.permissions.interactive_approval_mode":            cfg.Tools.Permissions.InteractiveApprovalMode,
		"tools.permissions.pending_ttl_seconds":                  cfg.Tools.Permissions.PendingTTLSeconds,
		"tools.permissions.interactive_approval_sources":         append([]string{}, cfg.Tools.Permissions.InteractiveApprovalSources...),
		"tools.permissions.interactive_approval_tools":           append([]string{}, cfg.Tools.Permissions.InteractiveApprovalTools...),
		"tools.permissions.trusted_path_prefixes":                append([]string{}, cfg.Tools.Permissions.TrustedPathPrefixes...),
		"tools.permissions.trusted_command_prefixes":             append([]string{}, cfg.Tools.Permissions.TrustedCommandPrefixes...),
		"media.moonshot.enabled":                                 cfg.Media.Moonshot.Enabled,
		"media.moonshot.base_url":                                cfg.Media.Moonshot.BaseURL,
		"media.moonshot.api_key":                                 cfg.Media.Moonshot.APIKey,
		"media.document.max_chars":                               cfg.Media.Document.MaxChars,
		"media.document.pdftotext_path":                          cfg.Media.Document.PDFToTextPath,
		"media.ocr.mode":                                         cfg.Media.OCR.Mode,
		"media.ocr.tesseract_path":                               cfg.Media.OCR.TesseractPath,
		"media.ocr.max_chars":                                    cfg.Media.OCR.MaxChars,
		"media.audio.enabled":                                    cfg.Media.Audio.Enabled,
		"media.audio.ffmpeg_path":                                cfg.Media.Audio.FFmpegPath,
		"media.audio.ffprobe_path":                               cfg.Media.Audio.FFprobePath,
		"media.audio.whisper_cpp_path":                           cfg.Media.Audio.WhisperCPPPath,
		"media.audio.whisper_model_path":                         cfg.Media.Audio.WhisperModelPath,
		"media.audio.max_chars":                                  cfg.Media.Audio.MaxChars,
		"media.video.enabled":                                    cfg.Media.Video.Enabled,
		"media.video.keyframe_interval_seconds":                  cfg.Media.Video.KeyframeIntervalSeconds,
		"media.video.max_frames":                                 cfg.Media.Video.MaxFrames,
		"channels.feishu.enabled":                                cfg.Feishu.Enabled,
		"channels.feishu.app_id":                                 cfg.Feishu.AppID,
		"channels.feishu.app_secret":                             cfg.Feishu.AppSecret,
		"channels.feishu.domain":                                 cfg.Feishu.Domain,
		"channels.weixin.enabled":                                cfg.Weixin.Enabled,
		"channels.weixin.base_url":                               cfg.Weixin.BaseURL,
		"channels.weixin.cdn_base_url":                           cfg.Weixin.CDNBaseURL,
		"channels.weixin.account_id":                             cfg.Weixin.AccountID,
		"channels.weixin.allow_from":                             append([]string{}, cfg.Weixin.AllowFrom...),
		"channels.weixin.route_tag":                              cfg.Weixin.RouteTag,
		"channels.weixin.long_poll_timeout_ms":                   cfg.Weixin.LongPollTimeoutMs,
		"channels.weixin.proxy":                                  cfg.Weixin.Proxy,
	}
	addBrowserEngineEffectiveValues(values, cfg.Tools.WebSearch.Browser.Engines)
	return values
}

func addBrowserEngineFileValues(values map[string]any, engines map[string]WebSearchBrowserEngineSection) {
	for _, engine := range []string{"duckduckgo", "bing", "brave", "custom"} {
		cfg := engines[engine]
		prefix := "tools.web_search.browser.engines." + engine + "."
		values[prefix+"search_url_template"] = cfg.SearchURLTemplate
		values[prefix+"blocked_hosts"] = append([]string{}, cfg.BlockedHosts...)
		values[prefix+"result_container_selector"] = cfg.ResultContainerSelector
		values[prefix+"result_link_selector"] = cfg.ResultLinkSelector
		values[prefix+"result_snippet_selector"] = cfg.ResultSnippetSelector
	}
}

func addBrowserEngineEffectiveValues(values map[string]any, engines map[string]WebSearchBrowserEngineConfig) {
	for _, engine := range []string{"duckduckgo", "bing", "brave", "custom"} {
		cfg := engines[engine]
		prefix := "tools.web_search.browser.engines." + engine + "."
		values[prefix+"search_url_template"] = cfg.SearchURLTemplate
		values[prefix+"blocked_hosts"] = append([]string{}, cfg.BlockedHosts...)
		values[prefix+"result_container_selector"] = cfg.ResultContainerSelector
		values[prefix+"result_link_selector"] = cfg.ResultLinkSelector
		values[prefix+"result_snippet_selector"] = cfg.ResultSnippetSelector
	}
}

func fieldConfigured(path string, stored ConfigFile, current *Config, origin fieldOrigin) bool {
	switch path {
	case "api.providers":
		return len(stored.API.Providers) > 0 || len(current.LLMProviders) > 0
	case "web.token":
		return strings.TrimSpace(current.WebToken) != "" || strings.TrimSpace(stored.Web.Token) != ""
	case "tools.web_search.brave.api_key":
		return strings.TrimSpace(current.Tools.WebSearch.BraveAPIKey) != "" || strings.TrimSpace(stored.Tools.WebSearch.Brave.APIKey) != ""
	case "tools.web_search.exa.api_key":
		return strings.TrimSpace(current.Tools.WebSearch.ExaAPIKey) != "" || strings.TrimSpace(stored.Tools.WebSearch.Exa.APIKey) != ""
	case "tools.web_search.tavily.api_key":
		return strings.TrimSpace(current.Tools.WebSearch.TavilyAPIKey) != "" || strings.TrimSpace(stored.Tools.WebSearch.Tavily.APIKey) != ""
	case "media.moonshot.api_key":
		return strings.TrimSpace(current.Media.Moonshot.APIKey) != "" || strings.TrimSpace(stored.Media.Moonshot.APIKey) != ""
	case "channels.feishu.app_id":
		return strings.TrimSpace(current.Feishu.AppID) != "" || strings.TrimSpace(stored.Channels.Feishu.AppID) != ""
	case "channels.feishu.app_secret":
		return strings.TrimSpace(current.Feishu.AppSecret) != "" || strings.TrimSpace(stored.Channels.Feishu.AppSecret) != ""
	default:
		return origin.Effective != nil
	}
}

func resolvePath(workspace, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(workspace, value))
}

func resolveLogPath(homeDir, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return filepath.Join(homeDir, "log", "godex.log")
	}
	value = expandHomePath(value)
	if filepath.IsAbs(value) {
		clean := filepath.Clean(value)
		if clean == filepath.Join(homeDir, "godex.log") {
			return filepath.Join(homeDir, "log", "godex.log")
		}
		return clean
	}
	clean := filepath.Clean(value)
	if clean == "godex.log" {
		return filepath.Join(homeDir, "log", "godex.log")
	}
	return filepath.Clean(filepath.Join(homeDir, clean))
}

func expandHomePath(value string) string {
	if value == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home
		}
		return value
	}
	if strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	return value
}

func readDotEnvFile(path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return map[string]string{}, nil
	}
	values, err := godotenv.Read(path)
	if err != nil {
		return map[string]string{}, err
	}
	return values, nil
}

func mergeEnvMaps(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, layer := range layers {
		for key, value := range layer {
			out[key] = value
		}
	}
	return out
}

func updateEnvVar(content, key, value string) string {
	prefix := key + "="
	if strings.TrimSpace(content) == "" {
		return prefix + value + "\n"
	}
	lines := strings.Split(content, "\n")
	found := false
	for idx, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[idx] = prefix + value
			found = true
		}
	}
	if !found {
		if strings.TrimSpace(content) != "" && !strings.HasSuffix(content, "\n") {
			lines = append(lines, "")
		}
		lines = append(lines, prefix+value)
	}
	return normalizeEnvContent(strings.Join(lines, "\n"))
}

func removeEnvVar(content, key string) string {
	prefix := key + "="
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			continue
		}
		out = append(out, line)
	}
	return normalizeEnvContent(strings.Join(out, "\n"))
}

func normalizeEnvContent(content string) string {
	content = strings.TrimRight(content, "\n")
	if strings.TrimSpace(content) == "" {
		return ""
	}
	return content + "\n"
}

func lookupEnvValue(values map[string]string, name string) envValue {
	if value, ok := values[name]; ok {
		return envValue{Value: value, Name: name, Set: true}
	}
	return envValue{}
}

func lookupProcessValue(name string) envValue {
	if value, ok := os.LookupEnv(name); ok {
		return envValue{Value: value, Name: name, Set: true}
	}
	return envValue{}
}

func lookupBool(values map[string]string, name string) (bool, bool) {
	value, ok := values[name]
	if !ok {
		return false, false
	}
	parsed, ok := parseBool(value)
	return parsed, ok
}

func lookupProcessBool(name string) (bool, bool) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return false, false
	}
	parsed, ok := parseBool(value)
	return parsed, ok
}

func lookupInt(values map[string]string, name string) (int, bool, string) {
	match := lookupEnvValue(values, name)
	if !match.Set {
		return 0, false, ""
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(match.Value))
	if err != nil {
		return 0, false, ""
	}
	return parsed, true, match.Name
}

func lookupProcessInt(name string) (int, bool, string) {
	match := lookupProcessValue(name)
	if !match.Set {
		return 0, false, ""
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(match.Value))
	if err != nil {
		return 0, false, ""
	}
	return parsed, true, match.Name
}

func lookupCSV(values map[string]string, name string) ([]string, bool) {
	value, ok := values[name]
	if !ok {
		return nil, false
	}
	return parseCSV(value), true
}

func lookupProcessCSV(name string) ([]string, bool) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil, false
	}
	return parseCSV(value), true
}

func parseBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func parseCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func validateDomainPattern(value string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fmt.Errorf("empty pattern")
	}
	if strings.HasPrefix(value, "*.") {
		value = strings.TrimPrefix(value, "*.")
	}
	if strings.Contains(value, "/") || strings.Contains(value, ":") || strings.Contains(value, " ") {
		return fmt.Errorf("must be a bare hostname pattern")
	}
	if !strings.Contains(value, ".") {
		return fmt.Errorf("must include a dot-separated domain")
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" {
			return fmt.Errorf("contains an empty label")
		}
	}
	return nil
}

func defaultStringValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func ensureParentWritable(path string) error {
	dir := filepath.Dir(strings.TrimSpace(path))
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".godex-write-test-*")
	if err != nil {
		return err
	}
	name := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(name)
		return closeErr
	}
	return os.Remove(name)
}

func commandAvailable(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if filepath.IsAbs(name) {
		_, err := os.Stat(name)
		return err == nil
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func probablyHasLocalBrowser() bool {
	return browserutil.HasLocalBrowser()
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func asInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

func asBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := parseBool(typed)
		return parsed
	default:
		return false
	}
}

func asStringList(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string{}, typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text := strings.TrimSpace(asString(item))
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		return parseCSV(typed)
	default:
		return nil
	}
}

func cloneSectionSchema(section SectionSchema) SectionSchema {
	cloned := SectionSchema{
		ID:          section.ID,
		Label:       section.Label,
		Description: section.Description,
		Fields:      make([]FieldSchema, 0, len(section.Fields)),
	}
	for _, field := range section.Fields {
		fieldCopy := field
		if len(field.Options) > 0 {
			fieldCopy.Options = append([]string{}, field.Options...)
		}
		cloned.Fields = append(cloned.Fields, fieldCopy)
	}
	return cloned
}

func cloneApplyReport(report ApplyReport) ApplyReport {
	cloned := report
	if len(report.Warnings) > 0 {
		cloned.Warnings = append([]string{}, report.Warnings...)
	}
	if len(report.Errors) > 0 {
		cloned.Errors = append([]string{}, report.Errors...)
	}
	return cloned
}

func runtimeApplySucceeded(status RuntimeStatus) bool {
	return status == RuntimeStatusApplied || status == RuntimeStatusAppliedWithWarning
}

func normalizeApplyReport(report ApplyReport, defaults ApplyReport) ApplyReport {
	normalized := report
	if normalized.AppliedAt.IsZero() {
		normalized.AppliedAt = defaults.AppliedAt
	}
	if normalized.StorageStatus == "" {
		normalized.StorageStatus = defaults.StorageStatus
	}
	if normalized.RuntimeStatus == "" {
		normalized.RuntimeStatus = defaults.RuntimeStatus
	}
	if normalized.Message == "" {
		normalized.Message = defaultApplyMessage(normalized.StorageStatus, normalized.RuntimeStatus, defaults.Message)
	}
	return normalized
}

func defaultApplyMessage(storage StorageStatus, runtime RuntimeStatus, fallback string) string {
	switch {
	case storage == StorageStatusSaveFailed:
		if fallback != "" {
			return fallback
		}
		return "Failed to save configuration to godex.yaml."
	case runtime == RuntimeStatusFailed:
		return "Configuration saved, but runtime apply failed."
	case runtime == RuntimeStatusAppliedWithWarning:
		return "Configuration applied with warnings."
	case runtime == RuntimeStatusApplied:
		if fallback != "" {
			return fallback
		}
		return "Configuration applied successfully."
	case runtime == RuntimeStatusSkipped:
		if fallback != "" {
			return fallback
		}
		return "Configuration saved without applying runtime changes."
	default:
		if fallback != "" {
			return fallback
		}
		return "Configuration update completed."
	}
}

func defaultApplyStorage(status StorageStatus) StorageStatus {
	if status == "" {
		return StorageStatusSaveFailed
	}
	return status
}

func defaultApplyRuntime(status RuntimeStatus) RuntimeStatus {
	if status == "" {
		return RuntimeStatusSkipped
	}
	return status
}

func cloneOrigins(input map[string]fieldOrigin) map[string]fieldOrigin {
	out := make(map[string]fieldOrigin, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func webSearchBrowserProviderEnabled(cfg *Config) bool {
	if cfg == nil || !cfg.Tools.Browser.Enabled {
		return false
	}
	for _, item := range cfg.Tools.WebSearch.ProviderOrder {
		if strings.EqualFold(strings.TrimSpace(item), "browser") {
			return true
		}
	}
	return false
}

func webSearchBrowserEnginesFromFile(input map[string]WebSearchBrowserEngineSection) map[string]WebSearchBrowserEngineConfig {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]WebSearchBrowserEngineConfig, len(input))
	for key, value := range input {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		out[key] = WebSearchBrowserEngineConfig{
			SearchURLTemplate:       value.SearchURLTemplate,
			BlockedHosts:            append([]string{}, value.BlockedHosts...),
			ResultContainerSelector: value.ResultContainerSelector,
			ResultLinkSelector:      value.ResultLinkSelector,
			ResultSnippetSelector:   value.ResultSnippetSelector,
		}
	}
	return out
}

func updateBrowserEngineConfig(browser *WebSearchBrowserConfig, engine string, update func(*WebSearchBrowserEngineConfig)) {
	if browser.Engines == nil {
		browser.Engines = make(map[string]WebSearchBrowserEngineConfig)
	}
	engine = strings.ToLower(strings.TrimSpace(engine))
	value := browser.Engines[engine]
	update(&value)
	browser.Engines[engine] = value
}

func setBrowserEngineFileValue(browser *WebSearchBrowserSection, path string, value any) {
	const prefix = "tools.web_search.browser.engines."
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.SplitN(rest, ".", 2)
	if len(parts) != 2 {
		return
	}
	engine := strings.ToLower(strings.TrimSpace(parts[0]))
	if engine == "" {
		return
	}
	if browser.Engines == nil {
		browser.Engines = make(map[string]WebSearchBrowserEngineSection)
	}
	cfg := browser.Engines[engine]
	switch parts[1] {
	case "search_url_template":
		cfg.SearchURLTemplate = asString(value)
	case "blocked_hosts":
		cfg.BlockedHosts = asStringList(value)
	case "result_container_selector":
		cfg.ResultContainerSelector = asString(value)
	case "result_link_selector":
		cfg.ResultLinkSelector = asString(value)
	case "result_snippet_selector":
		cfg.ResultSnippetSelector = asString(value)
	default:
		return
	}
	browser.Engines[engine] = cfg
}

func fileContainsSecret(cfg *Config, path string) bool {
	if cfg == nil {
		return false
	}
	switch filepath.Clean(path) {
	case filepath.Clean(cfg.ConfigFile), filepath.Clean(cfg.HomeConfigFile), filepath.Clean(cfg.ProjectConfigFile):
		return strings.TrimSpace(cfg.APIKey) != "" ||
			strings.TrimSpace(cfg.WebToken) != "" ||
			strings.TrimSpace(cfg.Tools.WebSearch.BraveAPIKey) != "" ||
			strings.TrimSpace(cfg.Tools.WebSearch.ExaAPIKey) != "" ||
			strings.TrimSpace(cfg.Tools.WebSearch.TavilyAPIKey) != "" ||
			strings.TrimSpace(cfg.Media.Moonshot.APIKey) != "" ||
			strings.TrimSpace(cfg.Feishu.AppID) != "" ||
			strings.TrimSpace(cfg.Feishu.AppSecret) != ""
	case filepath.Clean(cfg.EnvFile), filepath.Clean(cfg.HomeEnvFile), filepath.Clean(cfg.ProjectEnvFile):
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		text := string(data)
		for _, key := range []string{
			"ANTHROPIC_API_KEY",
			"OPENAI_API_KEY",
			"GODEX_OPENAI_CODEX_OAUTH_TOKEN",
			"GODEX_WEB_TOKEN",
			"GODEX_WEB_SEARCH_BRAVE_API_KEY",
			"GODEX_WEB_SEARCH_EXA_API_KEY",
			"GODEX_WEB_SEARCH_TAVILY_API_KEY",
			"GODEX_MEDIA_MOONSHOT_API_KEY",
			"FEISHU_APP_ID",
			"FEISHU_APP_SECRET",
		} {
			if strings.Contains(text, key+"=") {
				return true
			}
		}
	}
	return false
}

func configDirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (m *Manager) writeConfigFile(file ConfigFile) error {
	rendered, err := renderConfigTemplate(file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.configPath), 0755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(m.configPath, rendered, 0600)
}
