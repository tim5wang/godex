package skill

import (
	"bytes"
	"encoding/json"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
	"github.com/tim5wang/godex/internal/platform/stringutil"
	"golang.org/x/net/html"
)

// SourceEntry describes one curated skill source available for installation.
type SourceEntry struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Summary          string         `json:"summary"`
	Source           string         `json:"source"`
	SkillName        string         `json:"skill_name,omitempty"`
	Tags             []string       `json:"tags,omitempty"`
	Categories       []string       `json:"categories,omitempty"`
	Version          string         `json:"version,omitempty"`
	Trust            string         `json:"trust,omitempty"`
	Origin           string         `json:"origin,omitempty"`
	Installs         int            `json:"installs,omitempty"`
	Warnings         []string       `json:"warnings,omitempty"`
	InstallSupported bool           `json:"install_supported"`
	InstallSource    string         `json:"install_source,omitempty"`
	InstallName      string         `json:"install_name,omitempty"`
	InstallReason    string         `json:"install_reason,omitempty"`
	Installed        bool           `json:"installed"`
	InstalledPath    string         `json:"installed_path,omitempty"`
	InstallMemory    *InstallMemory `json:"install_memory,omitempty"`
}

type sourceConfigFile struct {
	Indexes []string      `json:"indexes,omitempty"`
	Sources []SourceEntry `json:"sources,omitempty"`
}

type sourceIndexDocument struct {
	Version int           `json:"version,omitempty"`
	Sources []SourceEntry `json:"sources,omitempty"`
}

type skillsHubSearchResponse struct {
	Skills []skillsHubSearchSkill `json:"skills"`
}

type skillsHubSearchSkill struct {
	ID       string `json:"id"`
	SkillID  string `json:"skillId"`
	Name     string `json:"name"`
	Installs int    `json:"installs"`
	Source   string `json:"source"`
}

type skillsHubLeaderboardEntry struct {
	ID       string
	Name     string
	SkillID  string
	Source   string
	Installs int
}

type skillsHubCache struct {
	Queries map[string]skillsHubCacheEntry `json:"queries,omitempty"`
}

type skillsHubCacheEntry struct {
	UpdatedAt string        `json:"updated_at,omitempty"`
	Items     []SourceEntry `json:"items,omitempty"`
}

type sourceCatalogMode string

const (
	sourceCatalogModeDefault  sourceCatalogMode = ""
	sourceCatalogModeTrending sourceCatalogMode = "trending"
)

// SourceCatalog returns curated skill sources with installed-state decoration.
func SourceCatalog(workspaceDir, skillsDir string) ([]SourceEntry, error) {
	return sourceCatalog(workspaceDir, skillsDir, "", sourceCatalogModeDefault)
}

// SearchSourceCatalog returns curated sources plus skills.sh search results for the given query.
func SearchSourceCatalog(workspaceDir, skillsDir, query string) ([]SourceEntry, error) {
	return sourceCatalog(workspaceDir, skillsDir, query, sourceCatalogModeDefault)
}

// TrendingSourceCatalog returns popular skills.sh entries ranked by install heat.
func TrendingSourceCatalog(workspaceDir, skillsDir string) ([]SourceEntry, error) {
	items, warnings := searchSkillsHubFeed(workspaceDir, skillsDir, sourceCatalogModeTrending, 24)
	if len(items) == 0 && len(warnings) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(stringutil.Unique(warnings), "; "))
	}
	return decorateSourceEntries(items, skillsDir, workspaceDir, warnings), nil
}

func sourceCatalog(workspaceDir, skillsDir, query string, mode sourceCatalogMode) ([]SourceEntry, error) {
	configFile, err := loadSourceConfig(workspaceDir)
	if err != nil {
		return nil, err
	}

	items := append([]SourceEntry{}, defaultSourceCatalog()...)

	remote, remoteWarnings := loadRemoteSources(configFile.Indexes)
	items = mergeSourceEntries(items, remote)
	items = mergeSourceEntries(items, configFile.Sources)
	skillsHubItems, skillsHubWarnings := searchSkillsHub(workspaceDir, skillsDir, query, 12)
	items = mergeSourceEntries(items, skillsHubItems)
	remoteWarnings = stringutil.Unique(remoteWarnings)
	skillsHubWarnings = stringutil.Unique(skillsHubWarnings)
	searchWarnings := stringutil.Unique(append([]string{}, remoteWarnings...))
	searchWarnings = stringutil.Unique(append(searchWarnings, skillsHubWarnings...))
	items = decorateSourceEntries(items, skillsDir, workspaceDir, searchWarnings)

	sort.Slice(items, func(i, j int) bool {
		if items[i].Installed != items[j].Installed {
			return items[i].Installed
		}
		if mode == sourceCatalogModeTrending && items[i].Installs != items[j].Installs {
			return items[i].Installs > items[j].Installs
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

func decorateSourceEntries(items []SourceEntry, skillsDir, workspaceDir string, warnings []string) []SourceEntry {
	items = append([]SourceEntry{}, items...)
	warnings = stringutil.Unique(append([]string{}, warnings...))
	for i := range items {
		items[i].Warnings = append(append([]string{}, items[i].Warnings...), warnings...)
		items[i].Installed = false
		items[i].InstalledPath = ""
		items[i].InstallMemory = nil
		if strings.TrimSpace(items[i].SkillName) == "" && strings.TrimSpace(items[i].InstallName) != "" {
			items[i].SkillName = strings.TrimSpace(items[i].InstallName)
		}
		name := strings.TrimSpace(items[i].InstallName)
		if name == "" {
			name = strings.TrimSpace(items[i].SkillName)
		}
		if name == "" {
			name = sanitizeSkillDirName(items[i].Name)
		}
		if items[i].SkillName == "" {
			items[i].SkillName = name
		}
		if name == "" {
			items[i].InstallSupported = false
			items[i].InstallReason = "No installable skill name could be derived from this source."
			continue
		}
		pathValue, err := ResolvePath(skillsDir, name)
		if err == nil {
			memory := readInstallMemoryForSkillPath(pathValue)
			if sourceEntryMatchesInstalledMemory(items[i], memory) {
				items[i].Installed = true
				items[i].InstalledPath = pathValue
				items[i].InstallMemory = memory
			}
		}
		if items[i].ID == "" {
			items[i].ID = name
		}
		items[i].InstallSource, items[i].InstallName, items[i].InstallReason, items[i].InstallSupported = installPreviewForSource(items[i], workspaceDir)
		if items[i].SkillName == "" {
			items[i].SkillName = items[i].InstallName
		}
		items[i].Warnings = stringutil.Unique(items[i].Warnings)
	}
	return items
}

func searchSkillsHub(workspaceDir, skillsDir, query string, limit int) ([]SourceEntry, []string) {
	query = strings.TrimSpace(query)
	if len(query) < 2 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("SKILLS_API_URL")), "/")
	if baseURL == "" {
		baseURL = "https://skills.sh"
	}

	searchURL := fmt.Sprintf("%s/api/search?q=%s&limit=%d", baseURL, url.QueryEscape(query), limit)
	req, err := http.NewRequest(http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, []string{fmt.Sprintf("skills.sh search request failed: %v", err)}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return cachedSkillsHubResults(workspaceDir, skillsDir, query, fmt.Sprintf("skills.sh search failed: %v", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cachedSkillsHubResults(workspaceDir, skillsDir, query, fmt.Sprintf("skills.sh search failed: unexpected status %s", resp.Status))
	}

	var payload skillsHubSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return cachedSkillsHubResults(workspaceDir, skillsDir, query, fmt.Sprintf("skills.sh search decode failed: %v", err))
	}

	items := make([]SourceEntry, 0, len(payload.Skills))
	for _, item := range payload.Skills {
		source := strings.TrimSpace(item.Source)
		skillName := strings.TrimSpace(item.SkillID)
		displayName := strings.TrimSpace(item.Name)
		if skillName == "" {
			skillName = sanitizeSkillDirName(displayName)
		}
		entry := normalizeSourceEntry(SourceEntry{
			ID:         "skillsh:" + strings.TrimSpace(item.ID),
			Name:       displayName,
			Summary:    skillsHubSummary(item.Installs),
			Source:     source,
			SkillName:  skillName,
			Trust:      "community",
			Origin:     "skillsh",
			Installs:   item.Installs,
			Categories: []string{"skills.sh"},
		}, "skillsh")
		entry.InstallSource, entry.InstallName, entry.InstallReason, entry.InstallSupported = installPreviewForSource(entry, "")
		if !entry.InstallSupported {
			entry.Warnings = append(entry.Warnings, fmt.Sprintf("This skills.sh entry cannot be installed by GoDex yet. %s", entry.InstallReason))
		}
		items = append(items, entry)
	}
	saveSkillsHubResults(workspaceDir, skillsDir, query, items)
	return items, nil
}

func searchSkillsHubFeed(workspaceDir, skillsDir string, mode sourceCatalogMode, limit int) ([]SourceEntry, []string) {
	if limit <= 0 {
		limit = 12
	}
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("SKILLS_API_URL")), "/")
	if baseURL == "" {
		baseURL = "https://skills.sh"
	}
	path := "/"
	label := "popular"
	if mode == sourceCatalogModeTrending {
		path = "/trending"
		label = "trending"
	}
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		return nil, []string{fmt.Sprintf("skills.sh %s feed request failed: %v", label, err)}
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return cachedSkillsHubResults(workspaceDir, skillsDir, skillsHubCacheKeyForFeed(mode), fmt.Sprintf("skills.sh %s feed failed: %v", label, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cachedSkillsHubResults(workspaceDir, skillsDir, skillsHubCacheKeyForFeed(mode), fmt.Sprintf("skills.sh %s feed failed: unexpected status %s", label, resp.Status))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return cachedSkillsHubResults(workspaceDir, skillsDir, skillsHubCacheKeyForFeed(mode), fmt.Sprintf("skills.sh %s feed read failed: %v", label, err))
	}
	rows, err := parseSkillsHubLeaderboard(body)
	if err != nil {
		return cachedSkillsHubResults(workspaceDir, skillsDir, skillsHubCacheKeyForFeed(mode), fmt.Sprintf("skills.sh %s feed parse failed: %v", label, err))
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Installs == rows[j].Installs {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Installs > rows[j].Installs
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	items := make([]SourceEntry, 0, len(rows))
	for _, row := range rows {
		entry := normalizeSourceEntry(SourceEntry{
			ID:         "skillsh:" + row.ID,
			Name:       row.Name,
			Summary:    skillsHubSummary(row.Installs),
			Source:     row.Source,
			SkillName:  row.SkillID,
			Trust:      "community",
			Origin:     "skillsh",
			Installs:   row.Installs,
			Categories: []string{"skills.sh", label},
		}, "skillsh")
		entry.InstallSource, entry.InstallName, entry.InstallReason, entry.InstallSupported = installPreviewForSource(entry, "")
		if !entry.InstallSupported {
			entry.Warnings = append(entry.Warnings, fmt.Sprintf("This skills.sh entry cannot be installed by GoDex yet. %s", entry.InstallReason))
		}
		items = append(items, entry)
	}
	saveSkillsHubResults(workspaceDir, skillsDir, skillsHubCacheKeyForFeed(mode), items)
	return items, nil
}

func skillsHubSummary(installs int) string {
	if installs <= 0 {
		return "Discovered via skills.sh."
	}
	switch {
	case installs >= 1_000_000:
		return fmt.Sprintf("Discovered via skills.sh. %.1fM installs.", float64(installs)/1_000_000)
	case installs >= 1_000:
		return fmt.Sprintf("Discovered via skills.sh. %.1fK installs.", float64(installs)/1_000)
	default:
		return fmt.Sprintf("Discovered via skills.sh. %d installs.", installs)
	}
}

func defaultSourceCatalog() []SourceEntry {
	return []SourceEntry{
		{
			ID:         "code-guide",
			Name:       "code-guide",
			Summary:    "Behavioral guardrails for safer coding, review, and refactor work.",
			Source:     "https://github.com/tim5wang/godex",
			SkillName:  "code-guide",
			Tags:       []string{"native", "review", "coding"},
			Categories: []string{"development"},
			Trust:      "official",
			Origin:     "curated",
		},
		{
			ID:         "playwright-cli",
			Name:       "playwright-cli",
			Summary:    "Automate browser screenshots and page interaction flows with Playwright CLI.",
			Source:     "https://github.com/tim5wang/godex",
			SkillName:  "playwright-cli",
			Tags:       []string{"browser", "automation", "screenshots"},
			Categories: []string{"browser", "automation"},
			Trust:      "official",
			Origin:     "curated",
		},
		{
			ID:         "browser-assist",
			Name:       "browser-assist",
			Summary:    "Use GoDex's built-in browser handoff/resume flow when login, captcha, or user confirmation blocks automation.",
			Source:     "https://github.com/tim5wang/godex",
			SkillName:  "browser-assist",
			Tags:       []string{"browser", "handoff", "automation"},
			Categories: []string{"browser", "automation"},
			Trust:      "official",
			Origin:     "curated",
		},
	}
}

func loadSourceConfig(workspaceDir string) (sourceConfigFile, error) {
	path := filepath.Join(workspaceDir, ".godex", "skill-sources.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sourceConfigFile{}, nil
		}
		return sourceConfigFile{}, fmt.Errorf("read skill sources: %w", err)
	}

	var file sourceConfigFile
	if err := json.Unmarshal(data, &file); err == nil && (len(file.Indexes) > 0 || len(file.Sources) > 0) {
		for i := range file.Sources {
			file.Sources[i] = normalizeSourceEntry(file.Sources[i], "local")
		}
		return file, nil
	}

	var legacy []SourceEntry
	if err := json.Unmarshal(data, &legacy); err != nil {
		return sourceConfigFile{}, fmt.Errorf("decode skill sources %s: %w", path, err)
	}
	for i := range legacy {
		legacy[i] = normalizeSourceEntry(legacy[i], "local")
	}
	return sourceConfigFile{Sources: legacy}, nil
}

func loadRemoteSources(indexURLs []string) ([]SourceEntry, []string) {
	if len(indexURLs) == 0 {
		return nil, nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	items := make([]SourceEntry, 0, len(indexURLs)*4)
	warnings := make([]string, 0, len(indexURLs))
	for _, indexURL := range indexURLs {
		indexURL = strings.TrimSpace(indexURL)
		if indexURL == "" {
			continue
		}
		remote, err := fetchRemoteSourceIndex(client, indexURL)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		items = mergeSourceEntries(items, remote)
	}
	return items, warnings
}

func fetchRemoteSourceIndex(client *http.Client, indexURL string) ([]SourceEntry, error) {
	resp, err := client.Get(indexURL)
	if err != nil {
		return nil, fmt.Errorf("fetch skill source index %s: %w", indexURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch skill source index %s: unexpected status %s", indexURL, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read skill source index %s: %w", indexURL, err)
	}
	var doc sourceIndexDocument
	if err := json.Unmarshal(data, &doc); err == nil && len(doc.Sources) > 0 {
		for i := range doc.Sources {
			doc.Sources[i] = normalizeSourceEntry(doc.Sources[i], "remote")
		}
		return doc.Sources, nil
	}

	var raw []SourceEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode skill source index %s: %w", indexURL, err)
	}
	for i := range raw {
		raw[i] = normalizeSourceEntry(raw[i], "remote")
	}
	return raw, nil
}

func mergeSourceEntries(base []SourceEntry, extra []SourceEntry) []SourceEntry {
	if len(extra) == 0 {
		return base
	}
	merged := append([]SourceEntry{}, base...)
	byKey := make(map[string]int, len(merged))
	for i, item := range merged {
		byKey[sourceEntryKey(item)] = i
	}
	for _, item := range extra {
		key := sourceEntryKey(item)
		if idx, ok := byKey[key]; ok {
			merged[idx] = item
			continue
		}
		byKey[key] = len(merged)
		merged = append(merged, item)
	}
	return merged
}

func sourceEntryKey(item SourceEntry) string {
	if id := strings.TrimSpace(item.ID); id != "" {
		return "id:" + id
	}
	if name := strings.TrimSpace(item.SkillName); name != "" {
		return "skill:" + strings.ToLower(name)
	}
	return "name:" + strings.ToLower(strings.TrimSpace(item.Name))
}

func normalizeSourceEntry(item SourceEntry, origin string) SourceEntry {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Summary = strings.TrimSpace(item.Summary)
	item.Source = strings.TrimSpace(item.Source)
	item.SkillName = strings.TrimSpace(item.SkillName)
	item.Version = strings.TrimSpace(item.Version)
	item.Trust = normalizeSourceTrust(item.Trust, origin)
	item.Origin = strings.TrimSpace(item.Origin)
	item.Warnings = stringutil.Unique(item.Warnings)
	if item.Origin == "" {
		item.Origin = origin
	}
	item.InstallSource, item.InstallName, item.InstallReason, item.InstallSupported = installPreviewForSource(item, "")
	item.Installed = false
	item.InstalledPath = ""
	item.InstallMemory = nil
	if item.ID == "" {
		if item.SkillName != "" {
			item.ID = item.SkillName
		} else if item.Name != "" {
			item.ID = sanitizeSkillDirName(item.Name)
		}
	}
	tags := make([]string, 0, len(item.Tags))
	seen := make(map[string]struct{}, len(item.Tags))
	for _, tag := range item.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tags = append(tags, tag)
	}
	item.Tags = tags
	item.Categories = normalizeSourceLabels(item.Categories)
	return item
}

func skillsHubCacheKeyForFeed(mode sourceCatalogMode) string {
	if mode == "" {
		mode = sourceCatalogModeDefault
	}
	return "feed:" + string(mode)
}

func parseSkillsHubLeaderboard(body []byte) ([]skillsHubLeaderboardEntry, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	results := make([]skillsHubLeaderboardEntry, 0, 24)
	seen := map[string]struct{}{}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil {
			return
		}
		if node.Type == html.ElementNode && node.Data == "a" {
			if entry, ok := parseSkillsHubAnchor(node); ok {
				if _, exists := seen[entry.ID]; !exists {
					seen[entry.ID] = struct{}{}
					results = append(results, entry)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if len(results) == 0 {
		return nil, fmt.Errorf("no leaderboard entries found")
	}
	return results, nil
}

func parseSkillsHubAnchor(node *html.Node) (skillsHubLeaderboardEntry, bool) {
	href := strings.TrimSpace(nodeAttr(node, "href"))
	if href == "" || !strings.HasPrefix(href, "/") || strings.Contains(href, "?") {
		return skillsHubLeaderboardEntry{}, false
	}
	parts := strings.Split(strings.Trim(href, "/"), "/")
	if len(parts) != 3 {
		return skillsHubLeaderboardEntry{}, false
	}
	name := strings.TrimSpace(firstTagText(node, "h3"))
	source := strings.TrimSpace(firstTagText(node, "p"))
	if name == "" || source == "" {
		return skillsHubLeaderboardEntry{}, false
	}
	installs := 0
	for _, text := range allTagTexts(node, "span") {
		if value, ok := parseSkillsHubInstalls(text); ok {
			installs = value
		}
	}
	if installs <= 0 {
		return skillsHubLeaderboardEntry{}, false
	}
	return skillsHubLeaderboardEntry{
		ID:       strings.Trim(href, "/"),
		Name:     name,
		SkillID:  parts[2],
		Source:   source,
		Installs: installs,
	}, true
}

func nodeAttr(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func firstTagText(node *html.Node, tag string) string {
	for _, value := range allTagTexts(node, tag) {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func allTagTexts(node *html.Node, tag string) []string {
	values := []string{}
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current == nil {
			return
		}
		if current.Type == html.ElementNode && current.Data == tag {
			text := strings.TrimSpace(collapseHTMLText(current))
			if text != "" {
				values = append(values, text)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walk(child)
	}
	return values
}

func collapseHTMLText(node *html.Node) string {
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current == nil {
			return
		}
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			builder.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return stdhtml.UnescapeString(strings.Join(strings.Fields(builder.String()), " "))
}

func parseSkillsHubInstalls(text string) (int, bool) {
	text = strings.TrimSpace(strings.ToUpper(strings.ReplaceAll(text, ",", "")))
	if text == "" {
		return 0, false
	}
	multiplier := 1.0
	switch {
	case strings.HasSuffix(text, "M"):
		multiplier = 1_000_000
		text = strings.TrimSuffix(text, "M")
	case strings.HasSuffix(text, "K"):
		multiplier = 1_000
		text = strings.TrimSuffix(text, "K")
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return int(value * multiplier), true
}

func sourceEntryInstallSupported(item SourceEntry) bool {
	_, _, _, supported := installPreviewForSource(item, "")
	return supported
}

func installPreviewForSource(item SourceEntry, workspaceDir string) (source string, name string, reason string, supported bool) {
	source = strings.TrimSpace(item.Source)
	name = strings.TrimSpace(item.SkillName)
	if name == "" {
		name = sanitizeSkillDirName(item.Name)
	}
	if name == "" {
		return source, "", "No installable skill name could be derived from this source.", false
	}
	if workspaceDir != "" && source != "" && !filepath.IsAbs(source) {
		candidate := filepath.Join(workspaceDir, source)
		if _, err := os.Stat(candidate); err == nil {
			source = candidate
		}
	}
	source, name = normalizeInstallRequest(source, name)
	if strings.TrimSpace(name) == "" {
		return source, name, "No installable skill name could be derived from this source.", false
	}
	if !supportsInstallSource(source) {
		return source, name, "GoDex currently supports local skill paths and git repository sources.", false
	}
	return source, name, "", true
}

func cachedSkillsHubResults(workspaceDir, skillsDir, query, warning string) ([]SourceEntry, []string) {
	items, updatedAt, ok := loadSkillsHubResults(workspaceDir, skillsDir, query)
	if !ok {
		return nil, []string{warning}
	}
	return items, []string{fmt.Sprintf("%s; using cached skills.sh results from %s", warning, updatedAt)}
}

func skillsHubCachePath(workspaceDir, skillsDir string) string {
	if dir := strings.TrimSpace(skillsDir); dir != "" {
		return filepath.Join(filepath.Dir(filepath.Clean(dir)), "skills-hub-cache.json")
	}
	return legacySkillsHubCachePath(workspaceDir)
}

func legacySkillsHubCachePath(workspaceDir string) string {
	return filepath.Join(workspaceDir, ".godex", "skills-hub-cache.json")
}

func loadSkillsHubResults(workspaceDir, skillsDir, query string) ([]SourceEntry, string, bool) {
	items, updatedAt, ok := loadSkillsHubResultsFromPath(skillsHubCachePath(workspaceDir, skillsDir), query)
	if ok {
		return items, updatedAt, true
	}
	if strings.TrimSpace(skillsDir) != "" {
		return loadSkillsHubResultsFromPath(legacySkillsHubCachePath(workspaceDir), query)
	}
	return nil, "", false
}

func loadSkillsHubResultsFromPath(path, query string) ([]SourceEntry, string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", false
	}
	var cache skillsHubCache
	if err := json.Unmarshal(data, &cache); err != nil || cache.Queries == nil {
		return nil, "", false
	}
	entry, ok := cache.Queries[strings.ToLower(strings.TrimSpace(query))]
	if !ok || len(entry.Items) == 0 {
		return nil, "", false
	}
	items := append([]SourceEntry{}, entry.Items...)
	for i := range items {
		items[i] = normalizeSourceEntry(items[i], "skillsh")
	}
	return items, entry.UpdatedAt, true
}

func saveSkillsHubResults(workspaceDir, skillsDir, query string, items []SourceEntry) {
	cache := skillsHubCache{Queries: map[string]skillsHubCacheEntry{}}
	cachePath := skillsHubCachePath(workspaceDir, skillsDir)
	if data, err := os.ReadFile(cachePath); err == nil {
		_ = json.Unmarshal(data, &cache)
	}
	if cache.Queries == nil {
		cache.Queries = make(map[string]skillsHubCacheEntry)
	}
	cache.Queries[strings.ToLower(strings.TrimSpace(query))] = skillsHubCacheEntry{
		UpdatedAt: time.Now().Format(time.RFC3339),
		Items:     append([]SourceEntry{}, items...),
	}
	_ = fsutil.WriteJSONAtomic(cachePath, cache, 0644)
}

func sourceEntryMatchesInstalledMemory(item SourceEntry, memory *InstallMemory) bool {
	if memory == nil {
		return true
	}
	entrySource := normalizeComparableSource(item.Source)
	memorySource := normalizeComparableSource(memory.Source)
	switch {
	case entrySource != "" && memorySource != "":
		return entrySource == memorySource
	case strings.TrimSpace(memory.SourceEntryID) != "" && strings.TrimSpace(item.ID) != "":
		return strings.EqualFold(strings.TrimSpace(memory.SourceEntryID), strings.TrimSpace(item.ID))
	default:
		return true
	}
}

func normalizeSourceLabels(items []string) []string {
	labels := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		labels = append(labels, item)
	}
	return labels
}

func normalizeSourceTrust(value, origin string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "official", "verified", "community", "local":
		return value
	}
	switch strings.ToLower(strings.TrimSpace(origin)) {
	case "curated":
		return "official"
	case "local":
		return "local"
	case "remote":
		return "community"
	default:
		return "community"
	}
}
