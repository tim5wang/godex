package config

import (
	"fmt"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/platform/browserutil"
	"github.com/tim5wang/godex/internal/platform/fsutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

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

// UpdateProviders atomically replaces the full provider set in the stored
// config, writes it to disk, and re-resolves the live runtime config so
// that callers see the updated provider list immediately.
func (m *Manager) UpdateProviders(providers map[string]llm.ProviderConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cloned := make(map[string]llm.ProviderConfig, len(providers))
	for id, provider := range providers {
		cloned[id] = llm.NormalizeProvider(id, provider)
	}
	m.stored.API.Providers = cloned
	m.revision++
	if err := m.writeConfigFile(m.stored); err != nil {
		return err
	}
	// Re-resolve the live config so callers see the new providers immediately.
	effective, err := m.effectiveConfigFile(m.stored)
	if err != nil {
		return err
	}
	nextCfg, nextOrigins, err := m.resolve(effective)
	if err != nil {
		return err
	}
	m.current = nextCfg
	m.origins = nextOrigins
	return nil
}

// ConfigFilePath returns the path of the configuration file managed by this config manager.
func (m *Manager) ConfigFilePath() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.configPath
}

// EnvFilePath returns the path of the .env file managed by this config manager.
func (m *Manager) EnvFilePath() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.envPath
}
