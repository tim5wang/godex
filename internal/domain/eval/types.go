package eval

import "time"

// Suite describes one repeatable agent behavior benchmark.
type Suite struct {
	Name  string `yaml:"name" json:"name"`
	Cases []Case `yaml:"cases" json:"cases"`
}

// Case is one benchmark prompt plus deterministic expectations.
type Case struct {
	ID             string      `yaml:"id" json:"id"`
	Title          string      `yaml:"title" json:"title,omitempty"`
	Prompt         string      `yaml:"prompt" json:"prompt"`
	ReplayFixture  string      `yaml:"replay_fixture" json:"replay_fixture,omitempty"`
	ModelProfileID string      `yaml:"model_profile_id" json:"model_profile_id,omitempty"`
	TimeoutSeconds int         `yaml:"timeout_seconds" json:"timeout_seconds,omitempty"`
	Expected       Expectation `yaml:"expected" json:"expected,omitempty"`
}

// Expectation defines the v1 deterministic scoring checks.
type Expectation struct {
	RequiredSubstrings                  []string `yaml:"required_substrings" json:"required_substrings,omitempty"`
	ForbiddenSubstrings                 []string `yaml:"forbidden_substrings" json:"forbidden_substrings,omitempty"`
	RequiredTools                       []string `yaml:"required_tools" json:"required_tools,omitempty"`
	ForbiddenTools                      []string `yaml:"forbidden_tools" json:"forbidden_tools,omitempty"`
	MaxToolFailures                     *int     `yaml:"max_tool_failures" json:"max_tool_failures,omitempty"`
	MaxRepeatedAssistantMessages        *int     `yaml:"max_repeated_assistant_messages" json:"max_repeated_assistant_messages,omitempty"`
	MaxRepeatedToolCalls                *int     `yaml:"max_repeated_tool_calls" json:"max_repeated_tool_calls,omitempty"`
	ForbiddenToolExchangeQueries        []string `yaml:"forbidden_tool_exchange_queries" json:"forbidden_tool_exchange_queries,omitempty"`
	MaxEmptyToolExchangeRecommendations *int     `yaml:"max_empty_tool_exchange_recommendations" json:"max_empty_tool_exchange_recommendations,omitempty"`
	ExpectedInstabilitySignals          []string `yaml:"expected_instability_signals" json:"expected_instability_signals,omitempty"`
}

// Report is the persisted summary for one suite run.
type Report struct {
	RunID       string       `json:"run_id"`
	SuiteName   string       `json:"suite_name"`
	StartedAt   time.Time    `json:"started_at"`
	CompletedAt time.Time    `json:"completed_at"`
	Passed      bool         `json:"passed"`
	TotalCases  int          `json:"total_cases"`
	PassedCases int          `json:"passed_cases"`
	FailedCases int          `json:"failed_cases"`
	Results     []CaseResult `json:"results"`
}

// CaseResult is the persisted score for one case.
type CaseResult struct {
	ID                 string     `json:"id"`
	Title              string     `json:"title,omitempty"`
	SessionID          string     `json:"session_id,omitempty"`
	TurnID             string     `json:"turn_id,omitempty"`
	ModelProfileID     string     `json:"model_profile_id,omitempty"`
	StartedAt          time.Time  `json:"started_at"`
	CompletedAt        time.Time  `json:"completed_at"`
	Passed             bool       `json:"passed"`
	Error              string     `json:"error,omitempty"`
	AssistantText      string     `json:"assistant_text,omitempty"`
	Tools              []ToolCall `json:"tools,omitempty"`
	InstabilitySignals []string   `json:"instability_signals,omitempty"`
	Failures           []string   `json:"failures,omitempty"`
}

// ToolCall is the compact tool lifecycle extracted from the session timeline.
type ToolCall struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}
