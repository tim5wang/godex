package usage

import "time"

// KeyPrefix is the prefix for all generated proxy API keys.
const KeyPrefix = "gdx_"

// ProxyAPIKey represents a usage gateway proxy API key.
type ProxyAPIKey struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	KeyHash          string    `json:"key_hash,omitempty"`
	KeyPrefix        string    `json:"key_prefix"`
	Enabled          bool      `json:"enabled"`
	BudgetCredits    float64   `json:"budget_credits"`
	WarningThreshold float64   `json:"warning_threshold"`
	AllowedModels    []string  `json:"allowed_models"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ProxyModel maps a public model name to a target provider profile and model.
type ProxyModel struct {
	ID              string    `json:"id"`
	PublicModel     string    `json:"public_model"`
	TargetProfileID string    `json:"target_profile_id"`
	TargetModel     string    `json:"target_model"`
	CreditWeight    float64   `json:"credit_weight"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// UsageCall records a single gateway chat completion call.
type UsageCall struct {
	ID               string    `json:"id"`
	Timestamp        time.Time `json:"timestamp"`
	APIKeyID         string    `json:"api_key_id"`
	PublicModel      string    `json:"public_model"`
	TargetProfileID  string    `json:"target_profile_id"`
	TargetModel      string    `json:"target_model"`
	InputTokens      int       `json:"input_tokens"`
	OutputTokens     int       `json:"output_tokens"`
	CacheReadTokens  int       `json:"cache_read_tokens"`
	CacheWriteTokens int       `json:"cache_write_tokens"`
	BillableTokens   int       `json:"billable_tokens"`
	CreditWeight     float64   `json:"credit_weight"`
	Credits          float64   `json:"credits"`
	Estimated        bool      `json:"estimated"`
	Status           string    `json:"status"`
	Error            string    `json:"error,omitempty"`
	LatencyMs        int64     `json:"latency_ms"`
	SourceChannel    string    `json:"source_channel,omitempty"`
	SessionID        string    `json:"session_id,omitempty"`
	TurnID           string    `json:"turn_id,omitempty"`
	JobID            string    `json:"job_id,omitempty"`
	ErrorCode        string    `json:"error_code,omitempty"`
}

// UsageSummary aggregates usage data by time period and optionally by API key.
type UsageSummary struct {
	Period           string  `json:"period"`
	APIKeyID         string  `json:"api_key_id"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	BillableTokens   int     `json:"billable_tokens"`
	Credits          float64 `json:"credits"`
	CallCount        int     `json:"call_count"`
	ErrorCount       int     `json:"error_count"`
}

// KeyCreateRequest is the input for creating a new proxy API key.
type KeyCreateRequest struct {
	Name             string   `json:"name"`
	BudgetCredits    float64  `json:"budget_credits"`
	WarningThreshold float64  `json:"warning_threshold"`
	AllowedModels    []string `json:"allowed_models"`
}

// KeyCreateResponse is returned when a proxy API key is created.
type KeyCreateResponse struct {
	Key    ProxyAPIKey `json:"key"`
	Secret string      `json:"secret"`
}

// KeyUpdateRequest contains the fields that may be updated on a proxy API key.
type KeyUpdateRequest struct {
	Name             *string   `json:"name,omitempty"`
	Enabled          *bool     `json:"enabled,omitempty"`
	BudgetCredits    *float64  `json:"budget_credits,omitempty"`
	WarningThreshold *float64  `json:"warning_threshold,omitempty"`
	AllowedModels    *[]string `json:"allowed_models,omitempty"`
}

// ModelCreateRequest is the input for creating a new model mapping.
type ModelCreateRequest struct {
	PublicModel     string  `json:"public_model"`
	TargetProfileID string  `json:"target_profile_id"`
	TargetModel     string  `json:"target_model"`
	CreditWeight    float64 `json:"credit_weight"`
}

// ModelUpdateRequest contains the fields that may be updated on a model mapping.
type ModelUpdateRequest struct {
	PublicModel     *string  `json:"public_model,omitempty"`
	TargetProfileID *string  `json:"target_profile_id,omitempty"`
	TargetModel     *string  `json:"target_model,omitempty"`
	CreditWeight    *float64 `json:"credit_weight,omitempty"`
	Enabled         *bool    `json:"enabled,omitempty"`
}

// CacheStats aggregates cache performance by model and time period.
//
// The previous version only surfaced a single `HitRate` field that
// was actually the token-level cache efficiency (cache_read / (input
// + cache_read) * 100), not a true call-level hit rate. This
// revision separates the two so the dashboard can show the
// percentage of requests that benefited from caching as well as the
// percentage of input context that came from cache. Without this
// split the operator can't tell whether a low HitRate means "few
// requests qualify for caching" (low call-level hit) or "caching
// saves only a small slice of context" (low token-level
// efficiency), and the two failure modes need different fixes.
type CacheStats struct {
	Period           string  `json:"period"`
	Model            string  `json:"model"`

	// Call counts. A call is considered a "cache hit" when it
	// surfaces at least one cached token; otherwise it's a miss.
	// Partial hits (e.g. the system prompt was cached but the
	// user's new message wasn't) still count as hits because
	// any cache reuse is meaningful.
	TotalCalls     int `json:"total_calls"`
	CacheHitCalls  int `json:"cache_hit_calls"`
	CacheMissCalls int `json:"cache_miss_calls"`

	// Token-level aggregates.
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`

	// HitRate is the call-level cache hit rate:
	//   cache_hit_calls / total_calls * 100
	// This is the metric operators usually want when they ask
	// "what fraction of requests benefit from the cache?". It is
	// rounded to two decimal places so the JSON stays compact.
	HitRate float64 `json:"hit_rate"`

	// CacheEfficiency is the token-level cache efficiency:
	//   cache_read_tokens / (input_tokens + cache_read_tokens) * 100
	// It measures "what fraction of the input context came from
	// cache" — the metric that drives the cost-saving story
	// because cached tokens are billed at a steep discount by
	// every provider that supports prompt caching.
	CacheEfficiency float64 `json:"cache_efficiency"`

	// TokensSaved is the wall-clock-equivalent of cache_read
	// tokens. We expose it as a separate field so the dashboard
	// can render "X tokens saved" without recomputing the
	// calculation in the frontend.
	TokensSaved int64 `json:"tokens_saved"`

	// EstimatedSavingsCredits is the credit reduction the cache
	// delivered, computed against the gateway's own 4x cache-read
	// discount (cache_read_tokens count as 0.25x billable in
	// RecordCall). The field lets the operator answer "how much
	// credit did caching save me this period?" without
	// reconstructing the math from raw counts.
	EstimatedSavingsCredits float64 `json:"estimated_savings_credits"`
}

// CacheStatsQuery groups the filtering parameters for GetCacheStats.
type CacheStatsQuery struct {
	// RangeType is "day", "week", "month", or "all". "all"
	// returns the lifetime aggregate with no time filter; the
	// Period field on each CacheStats row is then the empty
	// string (the SQL groups by no period column).
	RangeType string `json:"range_type"`
	// Model filters by the upstream target model name. Empty
	// means no filter.
	Model string `json:"model"`
	// APIKeyID filters by the proxy key that originated the
	// call. The web-token admin path can leave this empty to
	// see the global view; a proxy key calling its own
	// /usage/cache-stats endpoint must have the gateway fill
	// this in from the authenticated key to avoid leaking
	// cross-tenant cache stats.
	APIKeyID string `json:"api_key_id"`
}

// SessionUsageSummary aggregates usage by session.
type SessionUsageSummary struct {
	SessionID       string           `json:"session_id"`
	CallCount       int              `json:"call_count"`
	InputTokens     int              `json:"input_tokens"`
	OutputTokens    int              `json:"output_tokens"`
	CacheReadTokens int              `json:"cache_read_tokens"`
	CacheWriteTokens int             `json:"cache_write_tokens"`
	BillableTokens  int              `json:"billable_tokens"`
	Credits         float64          `json:"credits"`
	FirstCall       string           `json:"first_call"`
	LastCall        string           `json:"last_call"`
	ModelUsage      []ModelTokenUsage `json:"model_usage"`
}

// ModelTokenUsage records token usage per model within a session.
type ModelTokenUsage struct {
	Model        string `json:"model"`
	CallCount    int    `json:"call_count"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

// TimeSeriesQuery groups the filtering parameters for GetTimeSeries.
type TimeSeriesQuery struct {
	Granularity string `json:"granularity"` // "hour" or "day"
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
	APIKeyID    string `json:"api_key_id"`
	SessionID   string `json:"session_id"`
	Model       string `json:"model"`
}

// TimeSeriesPoint is a single bucket of time-series usage data.
type TimeSeriesPoint struct {
	Bucket          string  `json:"bucket"`
	CallCount       int     `json:"call_count"`
	ErrorCount      int     `json:"error_count"`
	InputTokens     int     `json:"input_tokens"`
	OutputTokens    int     `json:"output_tokens"`
	CacheReadTokens int     `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	BillableTokens  int     `json:"billable_tokens"`
	Credits         float64 `json:"credits"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
}

// Store is the persistence interface for usage gateway data.
type Store interface {
	ListKeys() ([]ProxyAPIKey, error)
	GetKey(id string) (*ProxyAPIKey, error)
	GetKeyByHash(hash string) (*ProxyAPIKey, error)
	CreateKey(key *ProxyAPIKey) error
	UpdateKey(key *ProxyAPIKey) error

	ListModels() ([]ProxyModel, error)
	GetModel(id string) (*ProxyModel, error)
	GetModelByPublicName(name string) (*ProxyModel, error)
	CreateModel(model *ProxyModel) error
	UpdateModel(model *ProxyModel) error

	RecordCall(call *UsageCall) error
	GetCalls(date string, apiKeyID string) ([]UsageCall, error)
	GetSummary(rangeType string, apiKeyID string) ([]UsageSummary, error)
	GetCacheStats(query CacheStatsQuery) ([]CacheStats, error)

	GetTimeSeries(query TimeSeriesQuery) ([]TimeSeriesPoint, error)
	GetSessionUsage(sessionID string) (*SessionUsageSummary, error)
	ListSessions(apiKeyID string, limit, offset int) ([]SessionUsageSummary, error)
}
