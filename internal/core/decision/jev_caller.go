package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// JevCallerOptions configures the native Jev/Laya decision caller. It talks
// to a laya server.py /predict endpoint (Jev-compatible request/response
// shape):
//
//	POST {BaseURL}/predict
//	{"state": {...}, "questions": {qid: {"type": "choice"|"score"|"noul",
//	 "instructions": ..., "criteria": ...}}}
//	→ {"model": "rl-agent", "answers": {qid: {"type": ..., "choice": ...,
//	   "confidence": ..., "probabilities": ...}}, "usage": {...}}
//
// A single decision.Request maps to one question under the fixed key "q1".
type JevCallerOptions struct {
	// BaseURL is the laya service root (e.g. http://127.0.0.1:8100). The
	// caller appends /predict.
	BaseURL string
	// Timeout bounds one call. Zero means 10s.
	Timeout time.Duration
}

type jevCaller struct {
	baseURL string
	timeout time.Duration
	client  *http.Client
}

// NewJevCaller builds a Caller that drives a native Jev/Laya structured
// decision endpoint (no chat round-trip, no JSON-repair gamble).
func NewJevCaller(opts JevCallerOptions) Caller {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	return &jevCaller{
		baseURL: strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/"),
		timeout: opts.Timeout,
		client:  &http.Client{Timeout: opts.Timeout},
	}
}

// jevQuestion mirrors the Jev question shape (rl_agent_api._to_internal).
type jevQuestion struct {
	Type         string         `json:"type"` // choice | score | noul
	Instructions string         `json:"instructions"`
	Criteria     map[string]any `json:"criteria,omitempty"`
}

// jevRequest is the /predict body.
type jevRequest struct {
	State     map[string]any   `json:"state"`
	Questions map[string]any   `json:"questions"` // qid -> jevQuestion
}

// jevAnswer is one answer entry of the /predict response.
type jevAnswer struct {
	Type         string         `json:"type"`
	Choice       string         `json:"choice,omitempty"`
	Score        float64        `json:"score,omitempty"`
	Noul         float64        `json:"noul,omitempty"`
	Confidence   float64        `json:"confidence,omitempty"`
	Probabilities map[string]any `json:"probabilities,omitempty"`
}

// jevResponse is the /predict response envelope.
type jevResponse struct {
	Model   string              `json:"model,omitempty"`
	Answers map[string]jevAnswer `json:"answers"`
	Usage   map[string]any      `json:"usage,omitempty"`
}

const jevQuestionKey = "q1"

func (c *jevCaller) Decide(ctx context.Context, req Request) (Result, error) {
	if c == nil || c.baseURL == "" {
		return Result{}, ErrNoCaller
	}
	decisionType := normalizeType(req.DecisionType)
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = c.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	q := jevQuestion{Instructions: strings.TrimSpace(req.Question)}
	crit := map[string]any{}
	switch decisionType {
	case TypeBoolean:
		// noul: statement hold? p[1] >= 0.5 → true.
		q.Type = "noul"
		crit["false"] = "no, the statement does not hold"
		crit["true"] = "yes, the statement holds"
	case TypeScore:
		q.Type = "score"
		for i, ch := range req.Choices {
			label := ch.ID
			if strings.TrimSpace(ch.Label) != "" {
				label = ch.Label
			}
			crit[fmt.Sprintf("%d", i)] = label
		}
	default:
		q.Type = "choice"
		for _, ch := range req.Choices {
			if strings.TrimSpace(ch.Label) != "" {
				crit[ch.ID] = ch.Label
			} else {
				crit[ch.ID] = nil
			}
		}
	}
	q.Criteria = crit

	// The Jev state carries the question text as body; from/subject stay
	// generic so the model judges the instruction against the content.
	body := jevRequest{
		State: map[string]any{
			"from":    "godex-flow",
			"subject": "decision",
			"body":    strings.TrimSpace(req.Question),
		},
		Questions: map[string]any{jevQuestionKey: q},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Result{}, fmt.Errorf("jev: marshal request: %w", err)
	}

	start := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/predict", bytes.NewReader(raw))
	if err != nil {
		return Result{}, fmt.Errorf("jev: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(httpReq)
	latency := time.Since(start)
	if err != nil {
		return Result{}, fmt.Errorf("jev: call laya: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return Result{}, fmt.Errorf("jev: laya status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var parsed jevResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&parsed); err != nil {
		return Result{}, fmt.Errorf("jev: decode response: %w", err)
	}
	answer, ok := parsed.Answers[jevQuestionKey]
	if !ok {
		return Result{}, fmt.Errorf("jev: response missing answer %q", jevQuestionKey)
	}

	result := Result{
		Confidence: answer.Confidence,
		Calibrated: answer.Confidence > 0,
		Model:      firstNonEmpty(parsed.Model, "laya"),
		Latency:    latency,
		Raw: map[string]any{
			"jev":           answer,
			"latency_ms":    latency.Milliseconds(),
			"usage":         parsed.Usage,
		},
	}
	switch decisionType {
	case TypeBoolean:
		result.Choice = boolChoice(answer.Noul >= 0.5)
		result.Score = answer.Noul
	case TypeScore:
		result.Score = answer.Score
		result.Choice = ""
	default:
		result.Choice = answer.Choice
	}
	if err := validateResult(decisionType, req.Choices, result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
