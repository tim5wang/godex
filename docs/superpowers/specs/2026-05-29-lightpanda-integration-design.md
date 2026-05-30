# SDD: Lightpanda Browser Integration for Web Search

## 1. Overview

Integrate Lightpanda headless browser as a lightweight search provider for GoDex's `web_search` and `web_fetch` tools. Lightpanda (Zig, ~123MB memory) replaces Chromium (~2GB) for search-only workloads, providing ~9x speed improvement.

## 2. Goals

1. **New `lightpanda` search provider** — CLI-based (`lightpanda fetch --dump markdown`), no rod/Chromium dependency
2. **Provider fallback** — `lightpanda` fails → seamless degradation to brave/exa/tavily/duckduckgo (zero extra LLM calls)
3. **`web_fetch` enhancement** — When HTTP fetch detects JS-heavy page (`NeedsBrowser=true`), auto-retry with Lightpanda
4. **Binary management** — Auto-download lightpanda binary to `$GODEX_HOME/bin/`, or use configured path
5. **Backward compatible** — No behavior change unless user explicitly adds `"lightpanda"` to `provider_order`

## 3. Non-Goals

- Replacing rod/Chromium for interactive browser tool (click, fill, screenshot)
- Lightpanda CDP mode (too unstable for Beta)
- Lightpanda MCP integration (future direction)

## 4. Architecture

```
web_search request
    │
    ▼
WebSearchService.Search()
    │  provider_order: ["lightpanda", "brave", "exa", "tavily", "duckduckgo"]
    │
    ├─ "lightpanda": LightpandaSearchProvider.BrowserSearch()
    │     exec: lightpanda fetch --dump markdown "https://duckduckgo.com/?q=..."
    │     Go: parse markdown → []SearchResult
    │
    ├─ "brave": braveSearch() [existing]
    ├─ "exa": exaSearch() [existing]
    ├─ "tavily": tavilySearch() [existing]
    ├─ "browser": BrowserSearchService [existing, rod/Chromium]
    └─ "duckduckgo": duckDuckGoSearch() [existing, HTML parse]

web_fetch request
    │
    ▼
WebFetchService.Fetch()
    ├─ HTTP fetch (existing)
    │   └─ NeedsBrowser=true && lightpanda available?
    │       └─ lightpanda fetch --dump markdown (auto-retry)
    └─ mode="lightpanda"? → force lightpanda path
```

## 5. Config Changes

### 5.1 YAML (`godex.yaml`)

```yaml
tools:
  web_search:
    provider_order: ["lightpanda", "brave", "tavily", "duckduckgo"]
  lightpanda:
    enabled: true
    binary_path: ""          # empty = auto-find/download
    auto_download: true
    search_engine: duckduckgo  # duckduckgo | bing | brave
    search_template: ""      # custom URL template, overrides search_engine
    wait_network_ms: 1500
    obey_robots: false
    log_level: warn
```

### 5.2 Config Types (Go)

```go
// types.go
type LightpandaSection struct {
    Enabled        bool   `yaml:"enabled"`
    BinaryPath     string `yaml:"binary_path"`
    AutoDownload   bool   `yaml:"auto_download"`
    SearchEngine   string `yaml:"search_engine"`
    SearchTemplate string `yaml:"search_template"`
    WaitNetworkMS  int    `yaml:"wait_network_ms"`
    ObeyRobots     bool   `yaml:"obey_robots"`
    LogLevel       string `yaml:"log_level"`
}

// config.go
type LightpandaConfig struct {
    Enabled        bool
    BinaryPath     string
    AutoDownload   bool
    SearchEngine   string
    SearchTemplate string
    WaitNetworkMS  int
    ObeyRobots     bool
    LogLevel       string
}
```

## 6. File Plan

| File | Action | Purpose |
|------|--------|---------|
| `internal/tools/lightpanda.go` | NEW | Binary manager: resolve, download, FetchDump CLI |
| `internal/tools/lightpanda_test.go` | NEW | Unit tests for binary manager |
| `internal/tools/lightpanda_search.go` | NEW | Search provider: build URL, parse markdown, BrowserSearch() |
| `internal/tools/lightpanda_search_test.go` | NEW | Unit tests for search parsing |
| `internal/tools/web_search.go` | MODIFY | Add "lightpanda" to providerOrder, searchWithProvider, service fields |
| `internal/tools/web_search_test.go` | MODIFY | Add lightpanda fallback tests |
| `internal/tools/web_fetch.go` | MODIFY | Add lightpanda fallback for NeedsBrowser pages |
| `internal/tools/web_fetch_test.go` | MODIFY | Add fallback tests |
| `internal/core/config/types.go` | MODIFY | Add LightpandaSection |
| `internal/core/config/config.go` | MODIFY | Add LightpandaConfig + resolution |
| `internal/core/config/defaults.go` | MODIFY | Add lightpanda defaults |
| `internal/core/config/schema.go` | MODIFY | Add lightpanda schema fields |
| `internal/agent/tool_registration.go` | MODIFY | Initialize + inject lightpanda |

## 7. TDD Implementation Order

### Phase 1: Binary Manager (`lightpanda.go`)
1. `TestResolvePath_ExplicitConfig` → implement ResolvePath
2. `TestResolvePath_InPATH` → implement PATH lookup
3. `TestResolvePath_NotFound` → return error
4. `TestFetchDump_BuildsCorrectArgs` → implement FetchDump (mock exec)
5. `TestFetchDump_Timeout` → ctx cancellation

### Phase 2: Search Provider (`lightpanda_search.go`)
1. `TestParseSearchResults_DDGMarkdown` → implement markdown parser
2. `TestParseSearchResults_LooseURLs` → implement fallback parser
3. `TestParseSearchResults_Empty` → return nil
4. `TestFilterSearchEngineSelfLinks` → implement host filter
5. `TestBuildSearchURL` → implement URL builder
6. `TestBrowserSearchIntegration` → end-to-end with mock binary

### Phase 3: WebSearch Integration (`web_search.go`)
1. `TestProviderOrderIncludesLightpanda` → modify providerOrder
2. `TestSearchWithProvider_Lightpanda` → add case
3. `TestSearchFallbackFromLightpanda` → verify degradation

### Phase 4: WebFetch Enhancement (`web_fetch.go`)
1. `TestFetchLightpandaFallback` → implement fallback
2. `TestFetchLightpandaUnavailable` → no-op when nil

### Phase 5: Config + Wiring
1. Config types + defaults + schema
2. Tool registration wiring

## 8. Acceptance Criteria

1. `provider_order: ["lightpanda", "brave", "tavily", "duckduckgo"]` — lightpanda tried first
2. lightpanda CLI fails → automatic fallback to next provider, no error to LLM
3. lightpanda not installed → provider returns error, fallback works
4. `web_fetch` with `NeedsBrowser=true` auto-retries with lightpanda when available
5. All existing tests pass unchanged
6. No behavior change when lightpanda not in provider_order
