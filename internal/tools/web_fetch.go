package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/tim5wang/godex/internal/core/config"
	"golang.org/x/net/html"
)

const defaultWebFetchUserAgent = "GoDex/1.0 (+https://github.com/tim5wang/godex)"

var maxWebFetchBodyBytes int64 = 10 << 20

type webFetchCacheEntry struct {
	value   WebFetchResponse
	expires time.Time
}

type WebFetchResponse struct {
	URL                string          `json:"url"`
	Mode               string          `json:"mode"`
	ContentType        string          `json:"content_type,omitempty"`
	Content            string          `json:"content"`
	Truncated          bool            `json:"truncated"`
	FilePath           string          `json:"file_path,omitempty"`
	Title              string          `json:"title,omitempty"`
	Description        string          `json:"description,omitempty"`
	CanonicalURL       string          `json:"canonical_url,omitempty"`
	SiteName           string          `json:"site_name,omitempty"`
	PublishedAt        string          `json:"published_at,omitempty"`
	NeedsBrowser       bool            `json:"needs_browser,omitempty"`
	FallbackHint       string          `json:"fallback_hint,omitempty"`
	ExtractionWarnings []string        `json:"extraction_warnings,omitempty"`
	Chunks             []WebFetchChunk `json:"chunks,omitempty"`
}

type WebFetchChunk struct {
	Index   int    `json:"index"`
	Heading string `json:"heading,omitempty"`
	Content string `json:"content"`
	Score   int    `json:"score,omitempty"`
}

// WebFetchService performs safe URL fetch + extraction with live config.
type WebFetchService struct {
	mu         sync.RWMutex
	cfg        config.WebFetchConfig
	tempDir    string
	client     *http.Client
	cache      map[string]webFetchCacheEntry
	now        func() time.Time
	lightpanda *LightpandaBinary
}

func NewWebFetchService(cfg config.WebFetchConfig, tempDir string) *WebFetchService {
	service := &WebFetchService{
		tempDir: tempDir,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		cache: make(map[string]webFetchCacheEntry),
		now:   time.Now,
	}
	service.ApplyConfig(cfg, tempDir)
	return service
}

func (s *WebFetchService) ApplyConfig(cfg config.WebFetchConfig, tempDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = normalizeWebFetchConfig(cfg)
	s.tempDir = tempDir
	s.cache = make(map[string]webFetchCacheEntry)
}

func (s *WebFetchService) SetLightpandaFetcher(binary *LightpandaBinary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lightpanda = binary
	s.cache = make(map[string]webFetchCacheEntry)
}

func (s *WebFetchService) Fetch(ctx context.Context, rawURL, mode string, maxChars int, query ...string) (WebFetchResponse, error) {
	s.mu.RLock()
	cfg := s.cfg
	now := s.now
	client := s.client
	tempDir := s.tempDir
	lightpanda := s.lightpanda
	s.mu.RUnlock()

	if !cfg.Enabled {
		return WebFetchResponse{}, fmt.Errorf("web_fetch is disabled in tools.web_fetch.enabled")
	}
	parsed, err := validateRemoteURL(rawURL, cfg.AllowPrivateHosts)
	if err != nil {
		return WebFetchResponse{}, err
	}
	if err := enforceFetchPolicy(parsed.Hostname(), cfg); err != nil {
		return WebFetchResponse{}, err
	}

	mode = normalizeFetchMode(mode)
	if maxChars <= 0 {
		maxChars = cfg.MaxChars
	}
	queryText := ""
	if len(query) > 0 {
		queryText = strings.TrimSpace(query[0])
	}
	cacheKey := fmt.Sprintf("%s|%s|%d|%s", parsed.String(), mode, maxChars, queryText)
	if entry, ok := s.lookupCache(cacheKey); ok && now().Before(entry.expires) {
		return entry.value, nil
	}

	requestClient := &http.Client{
		Timeout: cfgTimeout(cfg.TimeoutSeconds),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if _, err := validateRemoteURL(req.URL.String(), cfg.AllowPrivateHosts); err != nil {
				return err
			}
			if err := enforceFetchPolicy(req.URL.Hostname(), cfg); err != nil {
				return err
			}
			return nil
		},
		Transport: client.Transport,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return WebFetchResponse{}, err
	}
	req.Header.Set("User-Agent", defaultWebFetchUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.8")
	resp, err := requestClient.Do(req)
	if err != nil {
		return WebFetchResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return WebFetchResponse{}, fmt.Errorf("web_fetch failed: %s", strings.TrimSpace(string(body)))
	}
	body, err := readLimitedWebFetchBody(resp.Body)
	if err != nil {
		return WebFetchResponse{}, err
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	extracted, err := extractFetchedContent(body, contentType, mode, resp.Request.URL.String())
	if err != nil {
		return WebFetchResponse{}, err
	}
	result := WebFetchResponse{
		URL:                resp.Request.URL.String(),
		Mode:               mode,
		ContentType:        contentType,
		Content:            extracted.Content,
		Title:              extracted.Metadata.Title,
		Description:        extracted.Metadata.Description,
		CanonicalURL:       extracted.Metadata.CanonicalURL,
		SiteName:           extracted.Metadata.SiteName,
		PublishedAt:        extracted.Metadata.PublishedAt,
		NeedsBrowser:       extracted.NeedsBrowser,
		ExtractionWarnings: append([]string{}, extracted.Warnings...),
	}

	// Lightpanda fallback: when NeedsBrowser=true and lightpanda binary is available,
	// retry the fetch with lightpanda CLI to get rendered content without LLM calls.
	if result.NeedsBrowser && lightpanda != nil {
		if lpContent, lpErr := lightpanda.FetchDump(ctx, result.URL, mode, WithFetchWaitUntil("networkidle"), WithFetchLogLevel("warn")); lpErr == nil && strings.TrimSpace(lpContent) != "" {
			result.Content = lpContent
			result.NeedsBrowser = false
			result.ExtractionWarnings = append(result.ExtractionWarnings, "re-fetched with lightpanda browser")
		}
	}
	// Anti-crawl fallback hint: when the fetch is degraded (needs browser or thin content)
	// on a known anti-crawl host, tell the caller the proven bypass route so it stops
	// repeating web_fetch on the same URL (see docs/tools_issues.md 2026-08-24 / 08-27).
	result.FallbackHint = fallbackHintForURL(result.URL, result.NeedsBrowser, body, len(strings.TrimSpace(result.Content)))
	fullContent := result.Content
	result.Chunks = selectFetchChunks(fullContent, queryText+" "+result.Title, false)
	if len(result.Content) > maxChars {
		result.Truncated = true
		filePath, err := spillFetchedContent(tempDir, result, fullContent)
		if err != nil {
			return WebFetchResponse{}, err
		}
		result.FilePath = filePath
		result.Content = result.Content[:maxChars]
		result.Chunks = selectFetchChunks(fullContent, queryText+" "+result.Title, true)
	}
	s.storeCache(cacheKey, webFetchCacheEntry{
		value:   result,
		expires: now().Add(time.Duration(60) * time.Second),
	})
	return result, nil
}

func readLimitedWebFetchBody(reader io.Reader) ([]byte, error) {
	limit := maxWebFetchBodyBytes
	if limit <= 0 {
		limit = 10 << 20
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("web_fetch response exceeds max size of %d bytes", limit)
	}
	return body, nil
}

type WebFetchTool struct {
	service *WebFetchService
}

type webFetchArgs struct {
	URL      string `json:"url"`
	Mode     string `json:"mode,omitempty"`
	MaxChars int    `json:"max_chars,omitempty"`
	Query    string `json:"query,omitempty"`
}

func NewWebFetchTool(service *WebFetchService) Tool {
	return NewTypedTool(NewToolSpec("web_fetch", "Fetch a web page or URL, extract readable content, metadata, browser-needed warnings, and relevant chunks; spill large responses to a temp file. On anti-crawl hosts (WeChat/GitHub/npm/Cloudflare) a degraded result carries a fallback_hint with a proven bypass route (curl mobile-UA / GitHub API / npm registry).", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "HTTP or HTTPS URL to fetch",
			},
			"mode": map[string]interface{}{
				"type":        "string",
				"description": "Extraction mode: markdown or text",
				"enum":        []string{"markdown", "text"},
			},
			"max_chars": map[string]interface{}{
				"type":        "integer",
				"description": "Optional maximum number of characters to return before spilling full content to a file",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Optional query used to select relevant chunks from long pages",
			},
		},
		"required": []string{"url"},
	}, nil), func(ctx context.Context, args webFetchArgs) (ToolResult, error) {
		if service == nil {
			return ToolResult{}, fmt.Errorf("web_fetch service is unavailable")
		}
		result, err := service.Fetch(ctx, args.URL, args.Mode, args.MaxChars, args.Query)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: result}, nil
	})
}

func normalizeWebFetchConfig(cfg config.WebFetchConfig) config.WebFetchConfig {
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = 60000
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 30
	}
	cfg.Policy = normalizeFetchPolicy(cfg.Policy)
	cfg.AllowedDomains = cleanStringList(cfg.AllowedDomains)
	cfg.BlockedDomains = cleanStringList(cfg.BlockedDomains)
	return cfg
}

func normalizeFetchMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "text") {
		return "text"
	}
	return "markdown"
}

func cfgTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}

func normalizeFetchPolicy(policy string) string {
	if strings.EqualFold(strings.TrimSpace(policy), "allowlist") {
		return "allowlist"
	}
	return "allow_all"
}

func enforceFetchPolicy(hostname string, cfg config.WebFetchConfig) error {
	if matchDomainPattern(hostname, cfg.BlockedDomains) {
		return fmt.Errorf("domain %q is blocked by policy", hostname)
	}
	if cfg.Policy == "allowlist" && !matchDomainPattern(hostname, cfg.AllowedDomains) {
		return fmt.Errorf("domain %q is not in the allowed domains list", hostname)
	}
	return nil
}

func cleanStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

type fetchedMetadata struct {
	Title        string
	Description  string
	CanonicalURL string
	SiteName     string
	PublishedAt  string
}

type fetchedExtraction struct {
	Content      string
	Metadata     fetchedMetadata
	NeedsBrowser bool
	Warnings     []string
}

func extractFetchedContent(body []byte, contentType, mode, finalURL string) (fetchedExtraction, error) {
	contentType = strings.ToLower(contentType)
	switch {
	case strings.Contains(contentType, "text/html"), strings.Contains(contentType, "application/xhtml+xml"):
		return extractHTMLContent(body, mode, finalURL)
	default:
		content := string(body)
		return fetchedExtraction{Content: content}, nil
	}
}

func extractHTMLContent(body []byte, mode, finalURL string) (fetchedExtraction, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return fetchedExtraction{}, err
	}
	meta := extractHTMLMetadata(doc, finalURL)
	root := readableRoot(doc)
	if root == nil {
		root = doc
	}

	var content string
	if mode == "text" {
		content = htmlNodeText(root)
	} else {
		markdown, err := htmltomarkdown.ConvertString(filteredInnerHTML(root))
		if err != nil {
			return fetchedExtraction{}, err
		}
		content = strings.TrimSpace(markdown)
	}
	warnings := extractionWarnings(body, content)
	return fetchedExtraction{
		Content:      content,
		Metadata:     meta,
		NeedsBrowser: len(warnings) > 0,
		Warnings:     warnings,
	}, nil
}

func extractHTMLMetadata(doc *html.Node, finalURL string) fetchedMetadata {
	meta := fetchedMetadata{}
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "title":
				if meta.Title == "" {
					meta.Title = strings.TrimSpace(htmlNodeText(n))
				}
			case "meta":
				name := strings.ToLower(nodeAttr(n, "name"))
				prop := strings.ToLower(nodeAttr(n, "property"))
				content := strings.TrimSpace(nodeAttr(n, "content"))
				switch {
				case content == "":
				case meta.Description == "" && (name == "description" || prop == "og:description"):
					meta.Description = content
				case meta.SiteName == "" && prop == "og:site_name":
					meta.SiteName = content
				case meta.PublishedAt == "" && (prop == "article:published_time" || name == "date" || name == "pubdate"):
					meta.PublishedAt = content
				case meta.Title == "" && prop == "og:title":
					meta.Title = content
				}
			case "link":
				rel := strings.ToLower(nodeAttr(n, "rel"))
				if meta.CanonicalURL == "" && strings.Contains(rel, "canonical") {
					meta.CanonicalURL = canonicalizeURLAgainst(finalURL, nodeAttr(n, "href"))
				}
			case "time":
				if meta.PublishedAt == "" {
					meta.PublishedAt = strings.TrimSpace(nodeAttr(n, "datetime"))
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	meta.Title = strings.TrimSpace(meta.Title)
	meta.Description = strings.TrimSpace(meta.Description)
	meta.SiteName = strings.TrimSpace(meta.SiteName)
	meta.PublishedAt = strings.TrimSpace(meta.PublishedAt)
	meta.CanonicalURL = canonicalizeURL(meta.CanonicalURL)
	return meta
}

func readableRoot(doc *html.Node) *html.Node {
	for _, match := range []func(*html.Node) bool{
		func(n *html.Node) bool { return n.Type == html.ElementNode && strings.EqualFold(n.Data, "article") },
		func(n *html.Node) bool { return n.Type == html.ElementNode && strings.EqualFold(n.Data, "main") },
		func(n *html.Node) bool {
			return n.Type == html.ElementNode && strings.EqualFold(nodeAttr(n, "role"), "main")
		},
		func(n *html.Node) bool { return n.Type == html.ElementNode && strings.EqualFold(n.Data, "body") },
	} {
		if found := findNode(doc, match); found != nil {
			return found
		}
	}
	return nil
}

func findNode(n *html.Node, match func(*html.Node) bool) *html.Node {
	if n == nil {
		return nil
	}
	if match(n) {
		return n
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := findNode(child, match); found != nil {
			return found
		}
	}
	return nil
}

func htmlNodeText(n *html.Node) string {
	var parts []string
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node == nil || skipReadableNode(node) {
			return
		}
		if node.Type == html.TextNode {
			if text := strings.TrimSpace(node.Data); text != "" {
				parts = append(parts, text)
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(n)
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func filteredInnerHTML(root *html.Node) string {
	var builder strings.Builder
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		renderFilteredHTML(&builder, child)
	}
	return builder.String()
}

func renderFilteredHTML(builder *strings.Builder, node *html.Node) {
	if node == nil || skipReadableNode(node) {
		return
	}
	if node.Type == html.TextNode {
		builder.WriteString(html.EscapeString(node.Data))
		return
	}
	if node.Type != html.ElementNode {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			renderFilteredHTML(builder, child)
		}
		return
	}
	var buf bytes.Buffer
	shallow := *node
	shallow.FirstChild = nil
	shallow.LastChild = nil
	_ = html.Render(&buf, &shallow)
	start := buf.String()
	if idx := strings.Index(start, "</"); idx > 0 {
		start = start[:idx]
	}
	builder.WriteString(start)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		renderFilteredHTML(builder, child)
	}
	builder.WriteString("</")
	builder.WriteString(node.Data)
	builder.WriteString(">")
}

func skipReadableNode(node *html.Node) bool {
	if node.Type != html.ElementNode {
		return false
	}
	switch strings.ToLower(node.Data) {
	case "script", "style", "noscript", "nav", "header", "footer", "aside", "form", "svg":
		return true
	}
	combined := strings.ToLower(nodeAttr(node, "class") + " " + nodeAttr(node, "id") + " " + nodeAttr(node, "role"))
	for _, marker := range []string{"cookie", "banner", "advert", "subscribe", "newsletter", "sidebar", "breadcrumb", "menu", "popup", "modal"} {
		if strings.Contains(combined, marker) {
			return true
		}
	}
	return false
}

func extractionWarnings(body []byte, content string) []string {
	lowerBody := strings.ToLower(string(body))
	contentLen := len(strings.TrimSpace(content))
	scriptLen := strings.Count(lowerBody, "<script")
	warnings := make([]string, 0, 3)
	if contentLen < 120 && (strings.Contains(lowerBody, "enable javascript") || strings.Contains(lowerBody, "please enable javascript") || strings.Contains(lowerBody, "<noscript")) {
		warnings = append(warnings, "page appears to require JavaScript")
	}
	if contentLen < 120 && (strings.Contains(lowerBody, `id="app"`) || strings.Contains(lowerBody, `id="root"`) || strings.Contains(lowerBody, `data-reactroot`)) {
		warnings = append(warnings, "page content appears to be rendered by a client-side app")
	}
	if contentLen < 300 && scriptLen >= 5 {
		warnings = append(warnings, "page is script-heavy and yielded little readable text")
	}
	return warnings
}

// fallbackHintForURL returns a proven bypass route (from docs/tools_issues.md) when a
// fetch is degraded on a known anti-crawl host. It returns an empty string when the
// fetch succeeded or the host is not a known anti-crawl case. This prevents callers
// from repeatedly retrying web_fetch on the same anti-crawl URL.
func fallbackHintForURL(rawURL string, needsBrowser bool, body []byte, contentLen int) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	// Only surface a fallback when extraction is actually degraded, so a normal
	// successful fetch (e.g. a renderable GitHub README) is not cluttered.
	degraded := needsBrowser || contentLen < 120
	if !degraded {
		return ""
	}
	switch {
	case strings.HasSuffix(host, "weixin.qq.com"):
		return `web_fetch could not get the WeChat article body (needs_browser/captcha). Fall back to bash+curl with a mobile UA to download the raw HTML, then extract the div#js_content section. See docs/tools_issues.md 2026-08-24.`
	case host == "github.com":
		return `web_fetch could not get the GitHub repo content. Prefer the GitHub API: GET api.github.com/repos/{owner}/{repo}/git/trees/{branch}?recursive=1 for the file tree; if the branch is unknown, first GET api.github.com/repos/{owner}/{repo} to read the default_branch, then fetch raw files from raw.githubusercontent.com/{owner}/{repo}/{branch}/{path}. See docs/tools_issues.md 2026-08-27.`
	case host == "raw.githubusercontent.com":
		return `web_fetch could not resolve the raw GitHub file. Use a confirmed branch name (query api.github.com/repos/{owner}/{repo} for default_branch if unsure) instead of guessing main/HEAD. See docs/tools_issues.md 2026-08-27.`
	case strings.HasSuffix(host, "npmjs.com") || host == "registry.npmjs.org":
		return `web_fetch could not get the npm page (Cloudflare anti-bot). Use the npm registry API: GET registry.npmjs.org/{pkg}/latest for package metadata (description/versions). See docs/tools_issues.md 2026-08-27.`
	case strings.Contains(strings.ToLower(string(body)), "just a moment") || strings.Contains(strings.ToLower(string(body)), "cf-chl") || strings.Contains(strings.ToLower(string(body)), "cloudflare"):
		return `web_fetch hit a Cloudflare challenge. Use the site's public API (e.g. api.github.com, registry.npmjs.org, or the target's JSON endpoint) or the browser tool instead of retrying web_fetch on the same URL. See docs/tools_issues.md 2026-08-27.`
	}
	return ""
}

func canonicalizeURLAgainst(baseURL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if !parsed.IsAbs() {
		base, err := url.Parse(baseURL)
		if err == nil {
			parsed = base.ResolveReference(parsed)
		}
	}
	return canonicalizeURL(parsed.String())
}

func nodeAttr(node *html.Node, name string) string {
	if node == nil {
		return ""
	}
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
	}
	return ""
}

func canonicalizeURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return strings.TrimSpace(raw)
	}
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" || lower == "mc_cid" || lower == "mc_eid" || lower == "igshid" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Host = strings.ToLower(parsed.Host)
	if (parsed.Scheme == "https" && strings.HasSuffix(parsed.Host, ":443")) || (parsed.Scheme == "http" && strings.HasSuffix(parsed.Host, ":80")) {
		parsed.Host = parsed.Hostname()
	}
	return parsed.String()
}

func selectFetchChunks(content, query string, force bool) []WebFetchChunk {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	parts := splitContentChunks(content)
	if len(parts) == 0 {
		return nil
	}
	keywords := queryKeywords(query)
	chunks := make([]WebFetchChunk, 0, len(parts))
	for i, part := range parts {
		score := chunkScore(part, keywords)
		chunks = append(chunks, WebFetchChunk{
			Index:   i,
			Heading: chunkHeading(part),
			Content: trimChunkContent(part, 1200),
			Score:   score,
		})
	}
	if len(keywords) > 0 {
		sort.SliceStable(chunks, func(i, j int) bool {
			if chunks[i].Score != chunks[j].Score {
				return chunks[i].Score > chunks[j].Score
			}
			return chunks[i].Index < chunks[j].Index
		})
	} else if !force && len(content) < 1200 {
		return nil
	}
	limit := 3
	if len(chunks) < limit {
		limit = len(chunks)
	}
	out := make([]WebFetchChunk, 0, limit)
	for _, chunk := range chunks {
		if len(keywords) > 0 && chunk.Score == 0 && len(out) > 0 {
			continue
		}
		out = append(out, chunk)
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 && force {
		out = append(out, chunks[:limit]...)
	}
	return out
}

func splitContentChunks(content string) []string {
	raw := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n\n")
	chunks := make([]string, 0, len(raw))
	var current strings.Builder
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if current.Len() > 0 && current.Len()+len(part) > 1200 {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(part)
	}
	if current.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(current.String()))
	}
	if len(chunks) == 0 && content != "" {
		chunks = append(chunks, content)
	}
	return chunks
}

func queryKeywords(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 0x4e00 && r <= 0x9fff)
	})
	seen := make(map[string]struct{}, len(fields))
	keywords := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if len([]rune(field)) < 3 && !containsCJK(field) {
			continue
		}
		switch field {
		case "the", "and", "for", "with", "from", "this", "that", "latest", "official":
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		keywords = append(keywords, field)
	}
	return keywords
}

func chunkScore(text string, keywords []string) int {
	lower := strings.ToLower(text)
	score := 0
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			score += 5
		}
	}
	if strings.HasPrefix(strings.TrimSpace(text), "#") {
		score += 2
	}
	if strings.Contains(text, "```") || strings.Contains(text, "|") {
		score++
	}
	return score
}

func chunkHeading(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "# "))
		if line != "" {
			return trimChunkContent(line, 120)
		}
	}
	return ""
}

func trimChunkContent(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return strings.TrimSpace(text[:limit]) + "..."
}

func containsCJK(text string) bool {
	for _, r := range text {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

func htmlToText(data []byte) string {
	tokenizer := html.NewTokenizer(strings.NewReader(string(data)))
	var parts []string
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
		case html.TextToken:
			text := strings.TrimSpace(string(tokenizer.Text()))
			if text != "" {
				parts = append(parts, text)
			}
		}
	}
}

func spillFetchedContent(tempDir string, result WebFetchResponse, content string) (string, error) {
	dir := filepath.Join(tempDir, "web_fetch")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	ext := ".txt"
	if result.Mode == "markdown" {
		ext = ".md"
	}
	sum := sha256.Sum256([]byte(content))
	name := "fetch-" + hex.EncodeToString(sum[:16]) + ext
	absolutePath := filepath.Join(dir, name)
	if _, err := os.Stat(absolutePath); err == nil {
		return filepath.ToSlash(absolutePath), nil
	}
	if err := os.WriteFile(absolutePath, []byte(content), 0644); err != nil {
		return "", err
	}
	return filepath.ToSlash(absolutePath), nil
}

func (s *WebFetchService) lookupCache(key string) (webFetchCacheEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.cache[key]
	return entry, ok
}

func (s *WebFetchService) storeCache(key string, entry webFetchCacheEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[key] = entry
}
