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
// shape). DecideBatch sends multiple boolean questions against one shared state.
type JevCallerOptions struct {
	// BaseURL is the laya service root (e.g. http://127.0.0.1:8100). The
	// caller appends /predict.
	BaseURL string
	// Timeout bounds one call. Zero means 10s.
	Timeout time.Duration
}

// JevCaller calls a Jev-compatible Laya /predict endpoint.
type JevCaller struct {
	baseURL string
	timeout time.Duration
	client  *http.Client
}

// NewJevCaller builds a structured decision caller without a chat round-trip.
func NewJevCaller(opts JevCallerOptions) *JevCaller {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	return &JevCaller{
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
	State     map[string]any `json:"state"`
	Questions map[string]any `json:"questions"` // qid -> jevQuestion
}

// jevAnswer is one answer entry of the /predict response.
type jevAnswer struct {
	Type          string         `json:"type"`
	Choice        string         `json:"choice,omitempty"`
	Score         float64        `json:"score,omitempty"`
	Noul          *float64       `json:"noul,omitempty"`
	Confidence    float64        `json:"confidence,omitempty"`
	Probabilities map[string]any `json:"probabilities,omitempty"`
}

// jevResponse is the /predict response envelope.
type jevResponse struct {
	Model   string               `json:"model,omitempty"`
	Answers map[string]jevAnswer `json:"answers"`
	Usage   map[string]any       `json:"usage,omitempty"`
}

const (
	jevQuestionKey       = "q1"
	jevMaxBatchQuestions = 32
)

func (c *JevCaller) DecideBatch(ctx context.Context, reqs []Request) ([]Result, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	if c == nil || c.baseURL == "" {
		return nil, ErrNoCaller
	}
	if len(reqs) > jevMaxBatchQuestions {
		return nil, fmt.Errorf("jev: batch exceeds %d questions", jevMaxBatchQuestions)
	}
	state := strings.TrimSpace(reqs[0].State)
	questions := make(map[string]any, len(reqs))
	for i, req := range reqs {
		if normalizeType(req.DecisionType) != TypeBoolean {
			return nil, fmt.Errorf("jev: batch supports boolean decisions only")
		}
		if strings.TrimSpace(req.State) != state {
			return nil, fmt.Errorf("jev: batch decisions must share one state")
		}
		key := fmt.Sprintf("q%d", i+1)
		questions[key] = jevQuestion{
			Type:         "noul",
			Instructions: strings.TrimSpace(req.Question),
			Criteria: map[string]any{
				"false": "no, the statement does not hold",
				"true":  "yes, the statement holds",
			},
		}
	}
	if state == "" {
		return nil, fmt.Errorf("jev: batch state is required")
	}
	body := jevRequest{
		State:     map[string]any{"from": "godex-compaction", "subject": "tool history", "body": state},
		Questions: questions,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("jev: marshal batch request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/predict", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("jev: build batch request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := c.client.Do(httpReq)
	latency := time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("jev: call laya: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("jev: laya status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var parsed jevResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("jev: decode batch response: %w", err)
	}
	results := make([]Result, len(reqs))
	for i := range reqs {
		key := fmt.Sprintf("q%d", i+1)
		answer, ok := parsed.Answers[key]
		if !ok || answer.Noul == nil || (answer.Type != "" && answer.Type != "noul") || *answer.Noul < 0 || *answer.Noul > 1 {
			return nil, fmt.Errorf("jev: invalid batch answer %q", key)
		}
		results[i] = Result{
			Choice:     boolChoice(*answer.Noul >= 0.5),
			Score:      *answer.Noul,
			Confidence: answer.Confidence,
			Calibrated: answer.Confidence > 0,
			Model:      firstNonEmpty(parsed.Model, "laya"),
			Latency:    latency,
		}
	}
	return results, nil
}

func (c *JevCaller) Decide(ctx context.Context, req Request) (Result, error) {
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

	// State is shared context; each request's question remains its own
	// instruction. An empty State preserves the legacy question-as-body shape.
	stateBody := firstNonEmpty(req.State, req.Question)
	body := jevRequest{
		State: map[string]any{
			"from":    "godex-flow",
			"subject": "decision",
			"body":    stateBody,
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
			"jev":        answer,
			"latency_ms": latency.Milliseconds(),
			"usage":      parsed.Usage,
		},
	}
	switch decisionType {
	case TypeBoolean:
		if answer.Noul == nil || *answer.Noul < 0 || *answer.Noul > 1 {
			return Result{}, fmt.Errorf("jev: invalid noul score for %q", jevQuestionKey)
		}
		result.Choice = boolChoice(*answer.Noul >= 0.5)
		result.Score = *answer.Noul
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
