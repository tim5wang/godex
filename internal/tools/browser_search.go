package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/input"
	"github.com/tim5wang/godex/internal/core/config"
)

type BrowserSearchService struct {
	browser *BrowserService
	cfg     config.WebSearchBrowserConfig
}

type browserSearchEngineConfig struct {
	Engine                  string
	SearchURLTemplate       string
	BlockedHosts            []string
	ResultContainerSelector string
	ResultLinkSelector      string
	ResultSnippetSelector   string
	PreferredHosts          []string
	WaitNetworkIdleMS       int
	WaitAfterLoadMS         int
	MaxScrolls              int
	ResultTimeoutSeconds    int
}

func NewBrowserSearchProvider(browser *BrowserService, cfg config.WebSearchBrowserConfig) *BrowserSearchService {
	return &BrowserSearchService{
		browser: browser,
		cfg:     normalizeBrowserSearchConfig(cfg),
	}
}

func (s *BrowserSearchService) BrowserSearch(ctx context.Context, sessionID, query string, maxResults int) ([]SearchResult, error) {
	if s == nil || s.browser == nil {
		return nil, fmt.Errorf("browser search provider is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "web-search-browser"
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("browser search requires query")
	}
	if maxResults <= 0 {
		maxResults = defaultSearchResults
	}
	cfg := normalizeBrowserSearchConfig(s.cfg)
	if cfg.ResultTimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.ResultTimeoutSeconds)*time.Second)
		defer cancel()
	}
	var lastErr error
	for _, engine := range browserSearchEngineAttempts(cfg) {
		attemptCfg := browserSearchConfigForEngine(cfg, engine)
		results, err := s.browserSearchOnce(ctx, sessionID, query, maxResults, attemptCfg)
		if err != nil {
			lastErr = err
			continue
		}
		if len(results) > 0 {
			return results, nil
		}
		lastErr = fmt.Errorf("browser search engine %q returned no results", engine)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("browser search returned no results")
}

func (s *BrowserSearchService) browserSearchOnce(ctx context.Context, sessionID, query string, maxResults int, cfg browserSearchEngineConfig) ([]SearchResult, error) {
	searchURL, err := browserSearchURLForQuery(cfg, query)
	if err != nil {
		return nil, err
	}
	page, err := s.browser.Open(ctx, sessionID, searchURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.browser.Close(sessionID, page.PageID) }()

	if cfg.WaitNetworkIdleMS > 0 {
		if err := s.browser.WaitNetworkIdle(ctx, sessionID, page.PageID, cfg.WaitNetworkIdleMS); err != nil {
			_ = s.browser.Wait(ctx, sessionID, page.PageID, "", cfg.WaitAfterLoadMS)
		}
	}
	if cfg.WaitAfterLoadMS > 0 {
		_ = s.browser.Wait(ctx, sessionID, page.PageID, "", cfg.WaitAfterLoadMS)
	}
	if cfg.MaxScrolls > 0 {
		_ = s.scrollBrowserSearchPage(ctx, sessionID, page.PageID, cfg.MaxScrolls)
	}
	return s.extractBrowserSearchResults(ctx, sessionID, page.PageID, maxResults, cfg)
}

func (s *BrowserSearchService) scrollBrowserSearchPage(ctx context.Context, sessionID, pageID string, maxScrolls int) error {
	s.browser.mu.Lock()
	state, err := s.browser.pageStateLocked(sessionID, pageID)
	if err != nil {
		s.browser.mu.Unlock()
		return err
	}
	for i := 0; i < maxScrolls; i++ {
		if err := state.page.Keyboard.Press(input.PageDown); err != nil {
			s.browser.mu.Unlock()
			return err
		}
		s.browser.touchPageLocked(state)
		s.browser.mu.Unlock()
		if err := s.browser.Wait(ctx, sessionID, pageID, "", 400); err != nil {
			return err
		}
		s.browser.mu.Lock()
		state, err = s.browser.pageStateLocked(sessionID, pageID)
		if err != nil {
			s.browser.mu.Unlock()
			return err
		}
	}
	s.browser.mu.Unlock()
	return nil
}

func (s *BrowserSearchService) extractBrowserSearchResults(ctx context.Context, sessionID, pageID string, maxResults int, cfg browserSearchEngineConfig) ([]SearchResult, error) {
	s.browser.mu.Lock()
	defer s.browser.mu.Unlock()
	state, err := s.browser.pageStateLocked(sessionID, pageID)
	if err != nil {
		return nil, err
	}
	result, err := state.page.Eval(browserSearchResultsScript(maxResults, cfg))
	if err != nil {
		return nil, err
	}
	var payload struct {
		Results []SearchResult `json:"results"`
	}
	if err := decodeEvalJSONString(result, &payload); err != nil {
		return nil, err
	}
	s.browser.touchPageLocked(state)
	s.browser.refreshPageInfoLocked(state)
	return trimSearchResults(payload.Results, maxResults), nil
}

func normalizeBrowserSearchConfig(cfg config.WebSearchBrowserConfig) config.WebSearchBrowserConfig {
	cfg.Engine = strings.ToLower(strings.TrimSpace(cfg.Engine))
	switch cfg.Engine {
	case "", "duckduckgo":
		cfg.Engine = "duckduckgo"
		if len(cfg.EngineFallback) == 0 {
			cfg.EngineFallback = []string{"bing", "brave"}
		}
	case "bing":
		if len(cfg.EngineFallback) == 0 {
			cfg.EngineFallback = []string{"brave", "duckduckgo"}
		}
	case "brave":
		if len(cfg.EngineFallback) == 0 {
			cfg.EngineFallback = []string{"bing", "duckduckgo"}
		}
	case "custom":
	default:
		cfg.Engine = "duckduckgo"
	}
	if cfg.WaitNetworkIdleMS <= 0 {
		cfg.WaitNetworkIdleMS = 1500
	}
	if cfg.WaitAfterLoadMS <= 0 {
		cfg.WaitAfterLoadMS = 800
	}
	if cfg.MaxScrolls < 0 {
		cfg.MaxScrolls = 0
	}
	if cfg.ResultTimeoutSeconds <= 0 {
		cfg.ResultTimeoutSeconds = 20
	}
	cfg.PreferredHosts = cleanStringList(cfg.PreferredHosts)
	cfg.EngineFallback = cleanBrowserSearchEngines(cfg.EngineFallback)
	if cfg.Engines == nil {
		cfg.Engines = make(map[string]config.WebSearchBrowserEngineConfig)
	}
	for _, engine := range []string{"duckduckgo", "bing", "brave", "custom"} {
		engineCfg := cfg.Engines[engine]
		engineCfg.SearchURLTemplate = strings.TrimSpace(engineCfg.SearchURLTemplate)
		if engineCfg.SearchURLTemplate == "" {
			engineCfg.SearchURLTemplate = defaultBrowserSearchURLTemplate(engine)
		}
		engineCfg.BlockedHosts = cleanStringList(engineCfg.BlockedHosts)
		if len(engineCfg.BlockedHosts) == 0 {
			engineCfg.BlockedHosts = defaultBrowserSearchBlockedHosts(engine)
		}
		engineCfg.ResultContainerSelector = strings.TrimSpace(engineCfg.ResultContainerSelector)
		engineCfg.ResultLinkSelector = strings.TrimSpace(engineCfg.ResultLinkSelector)
		engineCfg.ResultSnippetSelector = strings.TrimSpace(engineCfg.ResultSnippetSelector)
		cfg.Engines[engine] = engineCfg
	}
	return cfg
}

func browserSearchConfigForEngine(cfg config.WebSearchBrowserConfig, engine string) browserSearchEngineConfig {
	cfg = normalizeBrowserSearchConfig(cfg)
	engine = strings.ToLower(strings.TrimSpace(engine))
	engineCfg := cfg.Engines[engine]
	return browserSearchEngineConfig{
		Engine:                  engine,
		SearchURLTemplate:       engineCfg.SearchURLTemplate,
		BlockedHosts:            append([]string{}, engineCfg.BlockedHosts...),
		ResultContainerSelector: engineCfg.ResultContainerSelector,
		ResultLinkSelector:      engineCfg.ResultLinkSelector,
		ResultSnippetSelector:   engineCfg.ResultSnippetSelector,
		PreferredHosts:          append([]string{}, cfg.PreferredHosts...),
		WaitNetworkIdleMS:       cfg.WaitNetworkIdleMS,
		WaitAfterLoadMS:         cfg.WaitAfterLoadMS,
		MaxScrolls:              cfg.MaxScrolls,
		ResultTimeoutSeconds:    cfg.ResultTimeoutSeconds,
	}
}

func defaultBrowserSearchURLTemplate(engine string) string {
	switch engine {
	case "duckduckgo":
		return "https://duckduckgo.com/?q={{query}}&ia=web"
	case "bing":
		return "https://www.bing.com/search?q={{query}}"
	case "brave":
		return "https://search.brave.com/search?q={{query}}&source=web"
	default:
		return ""
	}
}

func defaultBrowserSearchBlockedHosts(engine string) []string {
	switch engine {
	case "duckduckgo":
		return []string{"duckduckgo.com", "*.duckduckgo.com"}
	case "bing":
		return []string{"bing.com", "*.bing.com"}
	case "brave":
		return []string{"search.brave.com", "*.search.brave.com"}
	default:
		return nil
	}
}

func browserSearchEngineAttempts(cfg config.WebSearchBrowserConfig) []string {
	cfg = normalizeBrowserSearchConfig(cfg)
	candidates := append([]string{cfg.Engine}, cfg.EngineFallback...)
	if len(candidates) == 0 {
		candidates = []string{"duckduckgo"}
	}
	return cleanBrowserSearchEngines(candidates)
}

func cleanBrowserSearchEngines(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		switch value {
		case "duckduckgo", "bing", "brave", "custom":
		default:
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

func browserSearchURLForQuery(cfg browserSearchEngineConfig, query string) (string, error) {
	encoded := url.QueryEscape(strings.TrimSpace(query))
	searchURL := strings.ReplaceAll(cfg.SearchURLTemplate, "{{query}}", encoded)
	if strings.Contains(searchURL, "{{query_raw}}") {
		searchURL = strings.ReplaceAll(searchURL, "{{query_raw}}", strings.TrimSpace(query))
	}
	parsed, err := url.Parse(searchURL)
	if err != nil {
		return "", fmt.Errorf("invalid browser search URL template: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("browser search URL template must produce http or https URL")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("browser search URL template produced URL without hostname")
	}
	return parsed.String(), nil
}

func browserSearchResultsScript(maxResults int, cfg browserSearchEngineConfig) string {
	if maxResults <= 0 {
		maxResults = defaultSearchResults
	}
	settings := struct {
		BlockedHosts            []string `json:"blocked_hosts"`
		ResultContainerSelector string   `json:"result_container_selector"`
		ResultLinkSelector      string   `json:"result_link_selector"`
		ResultSnippetSelector   string   `json:"result_snippet_selector"`
	}{
		BlockedHosts:            cfg.BlockedHosts,
		ResultContainerSelector: cfg.ResultContainerSelector,
		ResultLinkSelector:      cfg.ResultLinkSelector,
		ResultSnippetSelector:   cfg.ResultSnippetSelector,
	}
	settingsJSON, _ := json.Marshal(settings)
	return fmt.Sprintf(`() => {
  const maxResults = %d;
  const settings = %s;
  const seen = new Set();
  const results = [];
  function hostBlocked(host) {
    host = (host || "").toLowerCase();
    for (const rawPattern of settings.blocked_hosts || []) {
      const pattern = String(rawPattern || "").trim().toLowerCase();
      if (!pattern) continue;
      if (pattern.startsWith("*.")) {
        const suffix = pattern.slice(1);
        const root = suffix.slice(1);
        if (host.endsWith(suffix) && host !== root) return true;
      } else if (host === pattern) {
        return true;
      }
    }
    return false;
  }
  function absoluteURL(raw) {
    try {
      return new URL(raw, location.href);
    } catch (_) {
      return null;
    }
  }
  function cleanHref(raw) {
    const url = absoluteURL(raw);
    if (!url) return "";
    if (url.protocol !== "http:" && url.protocol !== "https:") return "";
    const uddg = url.searchParams.get("uddg");
    if (uddg) {
      try {
        return decodeURIComponent(uddg);
      } catch (_) {
        return uddg;
      }
    }
    const host = url.hostname.toLowerCase();
    if (hostBlocked(host)) return "";
    return url.href;
  }
  function visible(el) {
    const rect = el.getBoundingClientRect();
    const style = window.getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
  }
  function closestResult(el) {
    return el.closest("article, li, [data-testid], .result, .web-result, .results_links, div");
  }
  function snippetFor(link, title) {
    const container = closestResult(link);
    if (!container) return "";
    const lines = (container.innerText || "")
      .split("\n")
      .map((line) => line.trim())
      .filter(Boolean)
      .filter((line) => line !== title);
    return lines.slice(0, 4).join(" ").slice(0, 400);
  }
  const links = [];
  if (settings.result_container_selector && settings.result_link_selector) {
    for (const container of Array.from(document.querySelectorAll(settings.result_container_selector))) {
      const link = container.querySelector(settings.result_link_selector);
      if (!link) continue;
      if (settings.result_snippet_selector) {
        const snippet = container.querySelector(settings.result_snippet_selector);
        if (snippet && !link.dataset.godexsnippet) {
          link.dataset.godexsnippet = (snippet.innerText || snippet.textContent || "").trim();
        }
      }
      links.push(link);
    }
  } else {
    links.push(...Array.from(document.querySelectorAll("a[href]")));
  }
  for (const link of links) {
    if (!visible(link)) continue;
    const href = cleanHref(link.getAttribute("href") || link.href || "");
    if (!href || seen.has(href)) continue;
    const title = (link.innerText || link.textContent || "").trim().replace(/\s+/g, " ");
    if (!title || title.length < 3) continue;
    seen.add(href);
    results.push({
      title,
      url: href,
      snippet: link.dataset.godexsnippet || snippetFor(link, title)
    });
    if (results.length >= maxResults) break;
  }
  return JSON.stringify({ results });
}`, maxResults, string(settingsJSON))
}
