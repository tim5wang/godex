// DeepSeek Search/Fetch 端点客户端：web_search provider 链与 web_fetch 降级兜底共用。
// 入口：POST /v1/tools/search、POST /v1/tools/fetch（Bearer 鉴权，裸 JSON）
// 文档：https://api-docs.deepseek.com/zh-cn/guides/search、docs/tools_issues.md
// 特性归属见 internal/tools/web_search.go 的 godex-feature: web 标记。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeepSeek exposes Search/Fetch as raw JSON endpoints outside the LLM
// namespace, authenticated with the same account API key.
const (
	deepseekSearchEndpoint = "https://api.deepseek.com/v1/tools/search"
	deepseekFetchEndpoint  = "https://api.deepseek.com/v1/tools/fetch"
)

// DeepSeekToolsClient calls DeepSeek's raw Search/Fetch endpoints. Both are
// billed per successful request regardless of result count, so callers must
// avoid variant fan-out and needless fetches.
type DeepSeekToolsClient struct {
	apiKey string
	client *http.Client
	// search/fetch are endpoint overrides pointed at httptest servers by tests.
	search string
	fetch  string
	// allowPrivateURLs lets tests exercise the remote-fetch path against a
	// loopback server. Production leaves it false so private/local URLs are
	// never handed to a third-party endpoint.
	allowPrivateURLs bool
}

func NewDeepSeekToolsClient(apiKey string) *DeepSeekToolsClient {
	return &DeepSeekToolsClient{
		apiKey: strings.TrimSpace(apiKey),
		client: &http.Client{Timeout: 30 * time.Second},
		search: deepseekSearchEndpoint,
		fetch:  deepseekFetchEndpoint,
	}
}

func (c *DeepSeekToolsClient) configured() bool {
	return c != nil && strings.TrimSpace(c.apiKey) != ""
}

// remoteFetchAllowed keeps private/local destinations off the third-party
// endpoint even when local fetching of private hosts is enabled: sending an
// internal URL to DeepSeek would leak network topology and reachability.
func (c *DeepSeekToolsClient) remoteFetchAllowed(rawURL string) bool {
	if c == nil {
		return false
	}
	if c.allowPrivateURLs {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	return disallowPrivateHost(parsed.Hostname()) == nil
}

type deepseekSearchItem struct {
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Date        string  `json:"date"`
	Authority   int     `json:"authority"`
}

// searchResults runs one Search request. DeepSeek applies its own operator
// syntax (site:, -site:, quotes) and caps count at 50.
func (c *DeepSeekToolsClient) searchResults(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	body, err := json.Marshal(map[string]interface{}{
		"query": query,
		"count": maxResults,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.search, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", searchUserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := deepseekStatusError("search", resp); err != nil {
		return nil, err
	}
	var parsed struct {
		Results []deepseekSearchItem `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(parsed.Results))
	for _, item := range parsed.Results {
		result := SearchResult{
			Title:   strings.TrimSpace(item.Title),
			URL:     strings.TrimSpace(item.URL),
			Snippet: strings.TrimSpace(item.Snippet),
		}
		// summary (600-800 chars) is already model-ready and occupies the same
		// slot as a locally fetched preview, so no extra fetch is needed.
		result.FetchedPreview = strings.TrimSpace(item.Summary)
		result.PublishedAt = strings.TrimSpace(item.Date)
		result.providerScore = item.RerankScore
		// authority 0-4 with 3+ meaning highly authoritative (-1 = unknown).
		if item.Authority >= 3 {
			result.providerScore += 0.6
		}
		results = append(results, result)
	}
	return trimSearchResults(results, maxResults), nil
}

// fetchPage returns the page Markdown body and title.
func (c *DeepSeekToolsClient) fetchPage(ctx context.Context, rawURL string) (string, string, error) {
	body, err := json.Marshal(map[string]string{"url": rawURL})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.fetch, strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", defaultWebFetchUserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if err := deepseekStatusError("fetch", resp); err != nil {
		return "", "", err
	}
	var parsed struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", "", err
	}
	content := strings.TrimSpace(parsed.Content)
	if content == "" {
		return "", "", fmt.Errorf("deepseek fetch returned no content for %s", rawURL)
	}
	return content, strings.TrimSpace(parsed.Title), nil
}

// deepseekStatusError surfaces balance/QPS failures as actionable messages
// instead of letting them read as a generic provider outage.
func deepseekStatusError(op string, resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusPaymentRequired:
		return fmt.Errorf("deepseek %s failed (HTTP 402): account balance insufficient; top up the DeepSeek account and retry", op)
	case http.StatusTooManyRequests:
		return fmt.Errorf("deepseek %s failed (HTTP 429): account-level QPS limit exceeded, wait and retry", op)
	}
	return fmt.Errorf("deepseek %s failed (%s): %s", op, resp.Status, strings.TrimSpace(string(data)))
}
