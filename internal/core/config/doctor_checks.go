package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/platform/fsutil"
)

type doctorSnapshot struct {
	configPath        string
	envPath           string
	homeDir           string
	projectDir        string
	homeConfigPath    string
	projectConfigPath string
	homeEnvPath       string
	projectEnvPath    string
	current           *Config
	origins           map[string]fieldOrigin
	lastApply         ApplyReport
	augmenter         func(DoctorReport) DoctorReport
}

type doctorRun struct {
	doctorSnapshot
	report DoctorReport
}

func (m *Manager) doctorSnapshot() doctorSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return doctorSnapshot{
		configPath:        m.configPath,
		envPath:           m.envPath,
		homeDir:           m.homeDir,
		projectDir:        m.projectDir,
		homeConfigPath:    m.homeConfigPath,
		projectConfigPath: m.projectConfigPath,
		homeEnvPath:       m.homeEnvPath,
		projectEnvPath:    m.projectEnvPath,
		current:           m.current.Clone(),
		origins:           cloneOrigins(m.origins),
		lastApply:         cloneApplyReport(m.lastApply),
		augmenter:         m.doctorAugmenter,
	}
}

func newDoctorRun(snapshot doctorSnapshot) *doctorRun {
	return &doctorRun{
		doctorSnapshot: snapshot,
		report: DoctorReport{
			GeneratedAt:       time.Now(),
			HomeDir:           snapshot.homeDir,
			ProjectDir:        snapshot.projectDir,
			HomeConfigFile:    snapshot.homeConfigPath,
			ProjectConfigFile: snapshot.projectConfigPath,
			HomeEnvFile:       snapshot.homeEnvPath,
			ProjectEnvFile:    snapshot.projectEnvPath,
			LastApply:         snapshot.lastApply,
		},
	}
}

func (d *doctorRun) add(check DoctorCheck) {
	d.report.Checks = append(d.report.Checks, check)
	switch check.Severity {
	case "error":
		d.report.Errors++
	case "warning":
		d.report.Warnings++
	default:
		d.report.Infos++
	}
}

func (d *doctorRun) finish() DoctorReport {
	if len(d.report.Checks) == 0 {
		d.add(DoctorCheck{Severity: "info", Code: "config_ok", Message: "Configuration looks healthy."})
	}
	sort.Slice(d.report.Checks, func(i, j int) bool {
		return d.report.Checks[i].Code < d.report.Checks[j].Code
	})
	if d.augmenter != nil {
		d.report = d.augmenter(d.report)
	}
	return d.report
}

func (d *doctorRun) checkConfigFiles() {
	if _, err := parseConfigFile(d.configPath); err != nil {
		d.add(DoctorCheck{Severity: "error", Code: "config_parse", Path: d.configPath, Message: err.Error(), Suggestion: "Fix the YAML syntax or unknown keys in godex.yaml."})
	}
	if d.projectConfigPath == "" || filepath.Clean(d.projectConfigPath) == filepath.Clean(d.configPath) {
		return
	}
	if _, err := os.Stat(d.projectConfigPath); err == nil {
		if _, err := parseConfigFile(d.projectConfigPath); err != nil {
			d.add(DoctorCheck{Severity: "error", Code: "project_config_parse", Path: d.projectConfigPath, Message: err.Error(), Suggestion: "Fix the YAML syntax or unknown keys in project godex.yaml."})
			return
		}
		d.add(DoctorCheck{Severity: "warning", Code: "project_config_shadows_home", Path: d.projectConfigPath, Message: "Project godex.yaml is overriding values from the global home config.", Suggestion: "Move stable global settings to the home config or remove project-only overrides you no longer need."})
	} else if !os.IsNotExist(err) {
		d.add(DoctorCheck{Severity: "warning", Code: "project_config_unreadable", Path: d.projectConfigPath, Message: err.Error(), Suggestion: "Fix permissions or remove the unreadable project config path."})
	}
}

func (d *doctorRun) checkModelAndSkills() {
	current := d.current
	if strings.TrimSpace(current.DefaultModelProfile().APIKey) == "" {
		d.add(DoctorCheck{Severity: "error", Code: "api_key_missing", Path: "api.providers", Message: "Default LLM provider credential is not configured.", Suggestion: "Set api.providers.<id>.api_key_env and export that environment variable, or set api.providers.<id>.api_key."})
	}
	seen := map[string]bool{}
	for _, rawName := range current.DefaultSkills {
		name := strings.TrimSpace(rawName)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if _, err := skill.ResolvePath(current.SkillsDir, name); err != nil {
			check := DoctorCheck{Severity: "warning", Code: "default_skill_missing", Path: "team.default_skills", Message: fmt.Sprintf("Default skill %q is configured but was not found in %s.", name, current.SkillsDir), Suggestion: "Install the skill into the skills directory or remove it from team.default_skills. Missing default skills are skipped when sessions start."}
			if !errors.Is(err, skill.ErrSkillNotFound) {
				check.Code = "default_skill_unreadable"
				check.Message = fmt.Sprintf("Default skill %q could not be checked: %v.", name, err)
				check.Suggestion = "Fix skills directory permissions or remove the entry from team.default_skills."
			}
			d.add(check)
		}
	}
	if likelyVisionModel(current.Model) {
		d.add(DoctorCheck{Severity: "info", Code: "image_understanding_enabled_candidate", Path: "api.default_model", Message: fmt.Sprintf("Model %q looks compatible with native image input.", current.Model), Suggestion: "Image attachments from Web and Feishu will attempt native vision input first and gracefully fall back if the endpoint rejects it."})
		return
	}
	d.add(DoctorCheck{Severity: "info", Code: "image_understanding_uncertain", Path: "api.default_model", Message: fmt.Sprintf("Model %q may not support native image input.", current.Model), Suggestion: "Use a vision-capable model such as kimi-k2.5 if you want direct image understanding from attachments."})
}

func (d *doctorRun) checkAutomationAndMedia() {
	current := d.current
	cronJobsDir := filepath.Join(current.StateDir, "cron", "jobs")
	cronJobCount := 0
	if entries, err := os.ReadDir(cronJobsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
				cronJobCount++
			}
		}
	}
	if !current.Cron.Enabled && cronJobCount > 0 {
		d.add(DoctorCheck{Severity: "warning", Code: "cron_jobs_but_scheduler_disabled", Path: "cron.enabled", Message: fmt.Sprintf("Cron scheduler is disabled, but %d cron job(s) are stored locally.", cronJobCount), Suggestion: "Set cron.enabled to true or export GODEX_CRON_ENABLED=true before starting `godex serve`."})
	}
	d.checkMoonshotMedia()
	d.checkLocalMediaTools()
	if current.Media.Video.Enabled {
		d.add(DoctorCheck{Severity: "info", Code: "video_summary_local_pipeline", Path: "media.video", Message: "Video summaries will use transcript + keyframes because the main chat provider stays single-provider in v1.", Suggestion: "This is expected until Godex supports native video requests on the primary chat provider path."})
	}
}

func (d *doctorRun) checkMoonshotMedia() {
	moonshot := d.current.Media.Moonshot
	if moonshot.Enabled && strings.TrimSpace(moonshot.APIKey) == "" {
		d.add(DoctorCheck{Severity: "error", Code: "media_moonshot_api_key_missing", Path: "media.moonshot.api_key", Message: "Moonshot media preprocessing is enabled but api_key is empty.", Suggestion: "Set media.moonshot.api_key or GODEX_MEDIA_MOONSHOT_API_KEY."})
	}
	if moonshot.Enabled && strings.TrimSpace(moonshot.BaseURL) == "" {
		d.add(DoctorCheck{Severity: "error", Code: "media_moonshot_base_url_missing", Path: "media.moonshot.base_url", Message: "Moonshot media preprocessing is enabled but base_url is empty.", Suggestion: "Set media.moonshot.base_url to a Moonshot OpenAI-compatible base such as https://api.moonshot.ai/v1."})
	}
	if !moonshot.Enabled {
		d.add(DoctorCheck{Severity: "info", Code: "media_moonshot_disabled", Path: "media.moonshot.enabled", Message: "Moonshot media preprocessing is disabled; document OCR/file-extract will rely on local fallbacks only.", Suggestion: "Enable media.moonshot.enabled and provide a Moonshot API key if you want official file extraction and OCR."})
	}
}

func (d *doctorRun) checkLocalMediaTools() {
	media := d.current.Media
	if strings.TrimSpace(media.Document.PDFToTextPath) != "" && !commandAvailable(media.Document.PDFToTextPath) && !media.Moonshot.Enabled {
		d.add(DoctorCheck{Severity: "warning", Code: "pdftotext_missing", Path: "media.document.pdftotext_path", Message: "pdftotext was not found; local PDF text extraction will be unavailable.", Suggestion: "Install poppler pdftotext or enable Moonshot media preprocessing."})
	}
	ocrMode := strings.TrimSpace(media.OCR.Mode)
	if strings.EqualFold(ocrMode, "tesseract") || (strings.EqualFold(ocrMode, "auto") && !media.Moonshot.Enabled) {
		if strings.TrimSpace(media.OCR.TesseractPath) == "" || !commandAvailable(media.OCR.TesseractPath) {
			d.add(DoctorCheck{Severity: "warning", Code: "tesseract_missing", Path: "media.ocr.tesseract_path", Message: "tesseract was not found; local OCR fallback will be unavailable.", Suggestion: "Install tesseract or enable Moonshot media preprocessing for OCR."})
		}
	}
	if !media.Audio.Enabled {
		return
	}
	for _, item := range []struct{ path, binary, code, message string }{
		{path: "media.audio.ffmpeg_path", binary: media.Audio.FFmpegPath, code: "ffmpeg_missing", message: "ffmpeg was not found; audio/video preprocessing cannot decode media."},
		{path: "media.audio.ffprobe_path", binary: media.Audio.FFprobePath, code: "ffprobe_missing", message: "ffprobe was not found; media duration and stream inspection will be unavailable."},
		{path: "media.audio.whisper_cpp_path", binary: media.Audio.WhisperCPPPath, code: "whisper_cpp_missing", message: "whisper.cpp CLI was not found; audio transcription is unavailable."},
	} {
		if strings.TrimSpace(item.binary) == "" || !commandAvailable(item.binary) {
			d.add(DoctorCheck{Severity: "warning", Code: item.code, Path: item.path, Message: item.message, Suggestion: "Install the required binary or disable audio/video preprocessing."})
		}
	}
	if strings.TrimSpace(media.Audio.WhisperModelPath) == "" {
		d.add(DoctorCheck{Severity: "warning", Code: "whisper_model_missing", Path: "media.audio.whisper_model_path", Message: "whisper.cpp model path is empty; audio transcription is unavailable.", Suggestion: "Point media.audio.whisper_model_path at a local whisper.cpp model file."})
	} else if _, err := os.Stat(media.Audio.WhisperModelPath); err != nil {
		d.add(DoctorCheck{Severity: "warning", Code: "whisper_model_not_found", Path: "media.audio.whisper_model_path", Message: fmt.Sprintf("whisper.cpp model file was not found at %s.", media.Audio.WhisperModelPath), Suggestion: "Download a whisper.cpp model and update media.audio.whisper_model_path."})
	}
}

func (d *doctorRun) checkChannels() {
	current := d.current
	if current.Feishu.Enabled && strings.TrimSpace(current.Feishu.AppID) == "" {
		d.add(DoctorCheck{Severity: "error", Code: "feishu_app_id_missing", Path: "channels.feishu.app_id", Message: "Feishu is enabled but app_id is empty.", Suggestion: "Set channels.feishu.app_id or FEISHU_APP_ID."})
	}
	if current.Feishu.Enabled && strings.TrimSpace(current.Feishu.AppSecret) == "" {
		d.add(DoctorCheck{Severity: "error", Code: "feishu_secret_missing", Path: "channels.feishu.app_secret", Message: "Feishu is enabled but app_secret is empty.", Suggestion: "Set channels.feishu.app_secret or FEISHU_APP_SECRET."})
	}
	d.checkWeixin()
}

func (d *doctorRun) checkWeixin() {
	current := d.current
	stateDir := filepath.Join(current.StateDir, "channels", "weixin", defaultStringValue(strings.TrimSpace(current.Weixin.AccountID), "default"))
	accountPath := filepath.Join(stateDir, "account.json")
	accountData, accountReadErr := os.ReadFile(accountPath)
	if !current.Weixin.Enabled {
		if accountReadErr == nil && bytes.Contains(accountData, []byte("bot_token")) {
			d.add(DoctorCheck{Severity: "info", Code: "weixin_setup_but_disabled", Path: "channels.weixin.enabled", Message: "Weixin login state exists, but the channel is disabled in config.", Suggestion: "Set channels.weixin.enabled to true or export WEIXIN_ENABLED=true before starting `godex serve`."})
		}
		return
	}
	if strings.TrimSpace(current.Weixin.BaseURL) == "" {
		d.add(DoctorCheck{Severity: "error", Code: "weixin_base_url_missing", Path: "channels.weixin.base_url", Message: "Weixin is enabled but base_url is empty.", Suggestion: "Set channels.weixin.base_url or WEIXIN_BASE_URL."})
	} else if parsed, err := url.Parse(strings.TrimSpace(current.Weixin.BaseURL)); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		d.add(DoctorCheck{Severity: "error", Code: "weixin_base_url_invalid", Path: "channels.weixin.base_url", Message: fmt.Sprintf("Weixin base_url %q is invalid.", current.Weixin.BaseURL), Suggestion: "Use a full HTTP(S) URL such as https://ilinkai.weixin.qq.com."})
	}
	if len(current.Weixin.AllowFrom) == 0 {
		d.add(DoctorCheck{Severity: "warning", Code: "weixin_allow_from_empty", Path: "channels.weixin.allow_from", Message: "Weixin allow_from is empty, so any sender who can reach the bot may use it.", Suggestion: "Set channels.weixin.allow_from to one or more ilink user IDs for safer operation."})
	}
	if err := ensureParentWritable(accountPath); err != nil {
		d.add(DoctorCheck{Severity: "warning", Code: "weixin_state_not_writable", Path: "channels.weixin.account_id", Message: fmt.Sprintf("Weixin state directory is not writable: %v", err), Suggestion: "Fix permissions for the Godex state directory before running `godex weixin setup`."})
	}
	if accountReadErr == nil {
		if !bytes.Contains(accountData, []byte("bot_token")) {
			d.add(DoctorCheck{Severity: "warning", Code: "weixin_state_incomplete", Path: "channels.weixin", Message: "Weixin account state exists but does not contain a bot token.", Suggestion: "Run `godex weixin setup` again to refresh the stored login state."})
		}
		return
	}
	if os.IsNotExist(accountReadErr) {
		d.add(DoctorCheck{Severity: "warning", Code: "weixin_not_setup", Path: "channels.weixin", Message: "Weixin is enabled, but no login state was found for the configured account.", Suggestion: "Run `godex weixin setup` to complete QR-code login for this account."})
		return
	}
	d.add(DoctorCheck{Severity: "warning", Code: "weixin_state_unreadable", Path: "channels.weixin", Message: fmt.Sprintf("Weixin account state could not be read: %v", accountReadErr), Suggestion: "Fix the state directory or run `godex weixin logout` and `godex weixin setup` again."})
}

func (d *doctorRun) checkWebTools() {
	current := d.current
	if current.Tools.WebSearch.Enabled && strings.TrimSpace(current.Tools.WebSearch.BraveAPIKey) == "" && strings.TrimSpace(current.Tools.WebSearch.ExaAPIKey) == "" && strings.TrimSpace(current.Tools.WebSearch.TavilyAPIKey) == "" && strings.TrimSpace(current.Tools.WebSearch.SerpAPIKey) == "" && !webSearchBrowserProviderEnabled(current) {
		d.add(DoctorCheck{Severity: "warning", Code: "web_search_fallback_only", Path: "tools.web_search", Message: "web_search is enabled, but only the fallback provider is configured.", Suggestion: "Add one of the provider API keys for higher quality current-information search."})
	}
	if current.Tools.WebFetch.Enabled && strings.EqualFold(strings.TrimSpace(current.Tools.WebFetch.Policy), "allowlist") && len(current.Tools.WebFetch.AllowedDomains) == 0 {
		d.add(DoctorCheck{Severity: "error", Code: "web_fetch_empty_allowlist", Path: "tools.web_fetch.allowed_domains", Message: "web_fetch allowlist mode is enabled, but allowed_domains is empty.", Suggestion: "Add allowed domains or switch tools.web_fetch.policy back to allow_all."})
	}
	d.checkDomainPatterns()
	if current.Tools.WebFetch.Enabled && current.Tools.WebFetch.AllowPrivateHosts {
		d.add(DoctorCheck{Severity: "warning", Code: "web_fetch_private_hosts_enabled", Path: "tools.web_fetch.allow_private_hosts", Message: "web_fetch private host access is enabled.", Suggestion: "Disable this unless you intentionally want the agent to reach localhost or private networks."})
	}
	if current.Tools.Browser.Enabled && current.Tools.Browser.AllowPrivateHosts {
		d.add(DoctorCheck{Severity: "warning", Code: "browser_private_hosts_enabled", Path: "tools.browser.allow_private_hosts", Message: "browser private host access is enabled.", Suggestion: "Disable this unless browser automation must reach localhost or private networks."})
	}
	if current.Tools.Browser.Enabled && strings.TrimSpace(current.Tools.Browser.BrowserPath) != "" && !commandAvailable(current.Tools.Browser.BrowserPath) {
		d.add(DoctorCheck{Severity: "warning", Code: "browser_path_missing", Path: "tools.browser.browser_path", Message: fmt.Sprintf("Configured browser path %q was not found.", current.Tools.Browser.BrowserPath), Suggestion: "Point browser_path at a valid local Chrome/Chromium binary or clear it and use cdp_url instead."})
	}
	if current.Tools.Browser.Enabled && strings.TrimSpace(current.Tools.Browser.CDPURL) == "" && strings.TrimSpace(current.Tools.Browser.BrowserPath) == "" && !probablyHasLocalBrowser() {
		d.add(DoctorCheck{Severity: "warning", Code: "browser_local_runtime_missing", Path: "tools.browser", Message: "browser is enabled, but no obvious local Chrome/Chromium runtime was found and both browser_path and cdp_url are empty.", Suggestion: "Install a local Chrome-compatible browser, configure tools.browser.browser_path, or configure tools.browser.cdp_url."})
	}
}

func (d *doctorRun) checkDomainPatterns() {
	type patternSet struct {
		path     string
		patterns []string
	}
	current := d.current
	checks := []patternSet{
		{path: "tools.web_fetch.allowed_domains", patterns: current.Tools.WebFetch.AllowedDomains},
		{path: "tools.web_fetch.blocked_domains", patterns: current.Tools.WebFetch.BlockedDomains},
		{path: "tools.web_search.browser.preferred_hosts", patterns: current.Tools.WebSearch.Browser.PreferredHosts},
	}
	for engine, engineCfg := range current.Tools.WebSearch.Browser.Engines {
		checks = append(checks, patternSet{path: "tools.web_search.browser.engines." + engine + ".blocked_hosts", patterns: engineCfg.BlockedHosts})
	}
	for _, item := range checks {
		for _, pattern := range item.patterns {
			if err := validateDomainPattern(pattern); err != nil {
				code := "web_fetch_bad_domain_pattern"
				if strings.HasPrefix(item.path, "tools.web_search.browser.") {
					code = "web_search_browser_bad_domain_pattern"
				}
				d.add(DoctorCheck{Severity: "error", Code: code, Path: item.path, Message: fmt.Sprintf("Invalid domain pattern %q: %v", pattern, err), Suggestion: "Use exact domains like example.com or wildcard prefixes like *.example.com."})
			}
		}
	}
}

func (d *doctorRun) checkPermissionsAndFilesystem() {
	current := d.current
	permissions := current.Tools.Permissions
	if !permissions.InteractiveApprovalEnabled {
		d.add(DoctorCheck{Severity: "warning", Code: "permissions_interactive_approval_disabled", Path: "tools.permissions.interactive_approval_enabled", Message: "interactive approval is disabled for remote tool calls.", Suggestion: "Enable interactive approvals unless every remote session source is fully trusted."})
	}
	if permissions.InteractiveApprovalEnabled && strings.EqualFold(strings.TrimSpace(permissions.InteractiveApprovalMode), "yolo") {
		d.add(DoctorCheck{Severity: "warning", Code: "permissions_interactive_approval_yolo_mode", Path: "tools.permissions.interactive_approval_mode", Message: "interactive approval is in yolo mode and will auto-approve protected remote tool calls.", Suggestion: "Use manual mode unless remote sessions are fully trusted and you intentionally want hands-off execution."})
	}
	for _, prefix := range permissions.TrustedPathPrefixes {
		if prefix == "." || prefix == "/" {
			d.add(DoctorCheck{Severity: "warning", Code: "permissions_trusted_path_prefix_broad", Path: "tools.permissions.trusted_path_prefixes", Message: fmt.Sprintf("Trusted path prefix %q is very broad and may bypass approvals for most file mutations.", prefix), Suggestion: "Prefer a narrower subtree such as notes/, .godex/tmp/, or skills/sandbox/."})
		}
	}
	d.checkWritablePaths()
	d.checkSecretFilePermissions()
}

func (d *doctorRun) checkWritablePaths() {
	current := d.current
	if current.Logging.FilePath != "" {
		dir := filepath.Dir(current.Logging.FilePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			d.add(DoctorCheck{Severity: "error", Code: "log_dir_unwritable", Path: "logging.file_path", Message: fmt.Sprintf("Log directory %s cannot be created: %v", dir, err), Suggestion: "Choose a writable logging.file_path."})
		} else if file, err := os.OpenFile(current.Logging.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err != nil {
			d.add(DoctorCheck{Severity: "error", Code: "log_file_unwritable", Path: "logging.file_path", Message: fmt.Sprintf("Log file %s cannot be opened: %v", current.Logging.FilePath, err), Suggestion: "Choose a writable logging.file_path."})
		} else {
			_ = file.Close()
		}
	}
	for _, path := range []struct{ field, dir string }{
		{field: "paths.state_dir", dir: current.StateDir}, {field: "paths.team_dir", dir: current.TeamDir},
		{field: "paths.tasks_dir", dir: current.TasksDir}, {field: "paths.todos_dir", dir: current.TodosDir},
		{field: "paths.memory_dir", dir: current.MemoryDir}, {field: "paths.rules_dir", dir: current.RulesDir},
		{field: "paths.skills_dir", dir: current.SkillsDir}, {field: "paths.temp_dir", dir: current.TempDir},
		{field: "paths.transcripts_dir", dir: current.TranscriptsDir}, {field: "paths.sessions_dir", dir: current.SessionsDir},
	} {
		if err := os.MkdirAll(path.dir, 0755); err != nil {
			d.add(DoctorCheck{Severity: "error", Code: "path_unwritable", Path: path.field, Message: fmt.Sprintf("Directory %s cannot be created: %v", path.dir, err), Suggestion: "Choose a writable path or fix filesystem permissions."})
		}
	}
}

func (d *doctorRun) checkSecretFilePermissions() {
	for _, file := range []struct {
		path   string
		secret bool
	}{
		{path: d.configPath, secret: fileContainsSecret(d.current, d.configPath)},
		{path: d.projectConfigPath, secret: fileContainsSecret(d.current, d.projectConfigPath)},
		{path: d.envPath, secret: fileContainsSecret(d.current, d.envPath)},
		{path: d.projectEnvPath, secret: fileContainsSecret(d.current, d.projectEnvPath)},
	} {
		if strings.TrimSpace(file.path) == "" || !file.secret {
			continue
		}
		if info, err := os.Stat(file.path); err == nil && info.Mode().Perm()&0077 != 0 {
			d.add(DoctorCheck{Severity: "warning", Code: "secret_file_permissions", Path: file.path, Message: fmt.Sprintf("Secret-bearing file %s is too permissive (%#o).", file.path, info.Mode().Perm()), Suggestion: "Run chmod 600 on the file."})
		}
	}
}

func (d *doctorRun) checkOriginsAndStorage() {
	for path, origin := range d.origins {
		if origin.OverriddenBy != "" {
			message := fmt.Sprintf("Value is overridden by %s.", origin.OverriddenBy)
			if origin.UsedEnv != "" {
				message = fmt.Sprintf("Value is overridden by %s (%s).", origin.OverriddenBy, origin.UsedEnv)
			}
			d.add(DoctorCheck{Severity: "info", Code: "field_shadowed", Path: path, Message: message, Suggestion: "Edit the higher-priority source if you want runtime changes to take effect."})
		}
		if origin.UsedEnv != "" && origin.CanonicalEnv != "" && origin.UsedEnv != origin.CanonicalEnv {
			d.add(DoctorCheck{Severity: "warning", Code: "deprecated_env_alias", Path: path, Message: fmt.Sprintf("Using deprecated environment alias %s.", origin.UsedEnv), Suggestion: fmt.Sprintf("Switch to %s.", origin.CanonicalEnv)})
		}
	}
	if size := fsutil.DirSizeBestEffort(filepath.Join(d.current.StateDir, "subagents")); size > 0 {
		d.add(DoctorCheck{Severity: "info", Code: "subagent_workspace_storage", Path: filepath.Join(d.current.StateDir, "subagents"), Message: fmt.Sprintf("Subagent workspace storage is using %.1f MB.", float64(size)/(1024*1024)), Suggestion: "Run `godex gc subagents --dry-run` to inspect cleanable workspaces."})
	}
	d.add(DoctorCheck{
		Severity: "info", Code: "agent_profile_defaults", Path: "agent.default_profiles",
		Message: fmt.Sprintf("Agent profile defaults: acp=%s, cli=%s, tui=%s, web=%s, weixin=%s, feishu=%s.",
			d.current.DefaultAgentProfileForChannel("acp"), d.current.DefaultAgentProfileForChannel("cli"),
			d.current.DefaultAgentProfileForChannel("tui"), d.current.DefaultAgentProfileForChannel("web"),
			d.current.DefaultAgentProfileForChannel("weixin"), d.current.DefaultAgentProfileForChannel("feishu")),
		Suggestion: "Use agent.profile or agent.default_profiles.* to tune default tool exposure by entrypoint.",
	})
}
