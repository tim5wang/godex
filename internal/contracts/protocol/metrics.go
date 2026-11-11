package protocol

// TurnMetrics measures one runner invocation. ModelMS includes failed model
// attempts; ToolMS is batch wall time, not the sum of overlapping reads.
type TurnMetrics struct {
	DurationMS int64 `json:"duration_ms"`
	// FirstTextMS excludes thinking and failed attempts. It measures committed
	// assistant text, not a semantic judgment of answer quality.
	FirstTextMS *int64 `json:"first_text_ms,omitempty"`
	// ModelCalls counts runner-level attempts. SDK-internal transport retries
	// remain part of ModelMS rather than being reported as new tool rounds.
	ModelCalls   int   `json:"model_calls"`
	ModelRetries int   `json:"model_retries"`
	ModelErrors  int   `json:"model_errors"`
	ModelMS      int64 `json:"model_ms"`
	ToolCalls    int   `json:"tool_calls"`
	ToolMS       int64 `json:"tool_ms"`
	RecoveryMS   int64 `json:"recovery_ms"`
	OtherMS      int64 `json:"other_ms"`
	OutputTokens int   `json:"output_tokens"`
}
