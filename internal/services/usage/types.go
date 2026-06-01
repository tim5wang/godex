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
type CacheStats struct {
	Period         string  `json:"period"`
	Model          string  `json:"model"`
	TotalCalls     int     `json:"total_calls"`
	InputTokens    int64   `json:"input_tokens"`
	CacheReadTokens int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	// HitRate is the percentage of input tokens served from cache.
	// HitRate = cache_read_tokens / (input_tokens + cache_read_tokens) * 100
	HitRate float64 `json:"hit_rate"`
	TokensSaved int64 `json:"tokens_saved"`
}

// CacheStatsQuery groups the filtering parameters for GetCacheStats.
type CacheStatsQuery struct {
	RangeType string `json:"range_type"` // "day", "week", "month", "all"
	Model     string `json:"model"`      // filter by model name (optional)
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
}
