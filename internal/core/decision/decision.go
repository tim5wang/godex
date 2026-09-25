// Package decision provides the low-cost structured decision model layer
// (Jev/Laya-class System-1 models) used by workflow decision nodes.
//
// A decision call is a small, non-conversational request: given a question
// and (optionally) a closed set of choices, the model returns a single
// structured verdict {choice, confidence}. It is intentionally distinct
// from the chat Caller used by subagent nodes: decision calls never enter
// the session transcript, never consume the context budget, and are metered
// independently (kind=decision).
package decision

import (
	"context"
	"errors"
	"time"
)

const (
	// TypeChoice asks the model to pick one of a closed set of choices.
	TypeChoice = "choice"
	// TypeBoolean asks for a true/false verdict. Choices are implicit.
	TypeBoolean = "boolean"
	// TypeScore asks for a numeric score in [0,1]. The choice field is left
	// empty; flows route on the returned score via output field predicates.
	TypeScore = "score"
)

var (
	// ErrNoCaller indicates no decision provider is configured/enabled.
	// The trailing hint keeps the "no decision caller configured" substring
	// (classifyDecisionFailure matches on it) while telling the operator how
	// to enable structured decisions.
	ErrNoCaller = errors.New("decision: no decision caller configured; enable agent.decision.enabled (+ agent.decision.provider) to use the structured decision model")
	// ErrInvalidChoice indicates the model returned a choice outside the
	// declared closed set, which must never auto-execute.
	ErrInvalidChoice = errors.New("decision: model returned choice outside declared set")
)

// Choice is one allowed verdict for a choice-type decision.
type Choice struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// Request is one structured decision call.
type Request struct {
	// Provider names the configured provider id (audit/metering label and,
	// later, per-node provider selection). F0 uses the default client.
	Provider string
	// Question is the decision prompt; it may carry the rendered task
	// context. It is data for the model, never executed.
	Question string
	// DecisionType is one of TypeChoice|TypeBoolean|TypeScore.
	DecisionType string
	// Choices is the closed candidate set for TypeChoice. Required there;
	// ignored for boolean/score.
	Choices []Choice
	// Timeout bounds the call. Zero defaults to 10s.
	Timeout time.Duration
	// MaxTokens bounds the response (decisions are tiny). Zero defaults to 64.
	MaxTokens int
}

// Result is the normalized decision output. It maps 1:1 to the standard
// decision node outputs documented in the Flow Spec v1.
type Result struct {
	Choice     string         `json:"choice"`
	Confidence float64        `json:"confidence"`
	Calibrated bool           `json:"calibrated"`
	Score      float64        `json:"score,omitempty"`
	Model      string         `json:"model,omitempty"`
	Latency    time.Duration  `json:"latency,omitempty"`
	Raw        map[string]any `json:"raw,omitempty"`
}

// Outputs renders the result as the node outputs map consumed by edge
// condition predicates (choice/confidence/output).
func (r Result) Outputs() map[string]any {
	out := map[string]any{
		"choice":     r.Choice,
		"confidence": r.Confidence,
		"calibrated": r.Calibrated,
	}
	if r.Score != 0 {
		out["score"] = r.Score
	}
	if r.Model != "" {
		out["model"] = r.Model
	}
	out["latency_ms"] = r.Latency.Milliseconds()
	return out
}

// Caller executes a single structured decision. Implementations must be
// safe for concurrent use.
type Caller interface {
	Decide(ctx context.Context, req Request) (Result, error)
}
