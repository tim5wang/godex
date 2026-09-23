package decision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestJevCallerChoice verifies a native Jev/Laya /predict endpoint drives a
// choice decision: request shape is Jev-compatible (state + questions.q1),
// and the response's choice/confidence map onto the normalized Result.
func TestJevCallerChoice(t *testing.T) {
	var got jevRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/predict" {
			t.Errorf("expected /predict, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"rl-agent","answers":{"q1":{"type":"choice","choice":"auto","confidence":0.91,"probabilities":{"auto":0.91,"manual":0.09}}},"usage":{"input_tokens":10,"output_tokens":0}}`))
	}))
	defer srv.Close()

	caller := NewJevCaller(JevCallerOptions{BaseURL: srv.URL, Timeout: 5 * time.Second})
	if caller == nil {
		t.Fatal("expected non-nil jev caller")
	}
	res, err := caller.Decide(context.Background(), Request{
		Provider:     "laya",
		Question:     "should we auto-process this ticket?",
		DecisionType: TypeChoice,
		Choices:      []Choice{{ID: "auto"}, {ID: "manual"}},
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if res.Choice != "auto" {
		t.Fatalf("expected choice=auto, got %q", res.Choice)
	}
	if res.Confidence < 0.9 {
		t.Fatalf("expected confidence ~0.91, got %v", res.Confidence)
	}
	if res.Model != "rl-agent" {
		t.Fatalf("expected model=rl-agent, got %q", res.Model)
	}
	// Request shape: one question under q1 with the closed choice set.
	if len(got.Questions) != 1 {
		t.Fatalf("expected 1 question, got %+v", got.Questions)
	}
	q, ok := got.Questions["q1"].(map[string]any)
	if !ok {
		t.Fatalf("expected q1 map, got %+v", got.Questions)
	}
	if q["type"] != "choice" {
		t.Fatalf("expected type=choice, got %v", q["type"])
	}
	crit, _ := q["criteria"].(map[string]any)
	if _, ok := crit["auto"]; !ok {
		t.Fatalf("expected auto in criteria, got %+v", crit)
	}
	if _, ok := crit["manual"]; !ok {
		t.Fatalf("expected manual in criteria, got %+v", crit)
	}
}

// TestJevCallerBoolean verifies noul answers map to a boolean verdict.
func TestJevCallerBoolean(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"rl-agent","answers":{"q1":{"type":"noul","noul":0.97,"confidence":0.8}}}`))
	}))
	defer srv.Close()

	caller := NewJevCaller(JevCallerOptions{BaseURL: srv.URL, Timeout: 5 * time.Second})
	res, err := caller.Decide(context.Background(), Request{
		Question:     "is this order eligible for auto-refund?",
		DecisionType: TypeBoolean,
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if res.Choice != "true" {
		t.Fatalf("expected choice=true for noul>=0.5, got %q", res.Choice)
	}
}

// TestJevCallerScore verifies score answers map onto Result.Score.
func TestJevCallerScore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"rl-agent","answers":{"q1":{"type":"score","score":0.42,"confidence":0.7}}}`))
	}))
	defer srv.Close()

	caller := NewJevCaller(JevCallerOptions{BaseURL: srv.URL, Timeout: 5 * time.Second})
	res, err := caller.Decide(context.Background(), Request{
		Question:     "rate the urgency",
		DecisionType: TypeScore,
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if res.Score < 0.4 {
		t.Fatalf("expected score ~0.42, got %v", res.Score)
	}
}

// TestJevCallerBadStatus verifies non-200 responses surface as errors.
func TestJevCallerBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "inference error: oom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	caller := NewJevCaller(JevCallerOptions{BaseURL: srv.URL, Timeout: 5 * time.Second})
	if _, err := caller.Decide(context.Background(), Request{
		Question:     "x",
		DecisionType: TypeChoice,
		Choices:      []Choice{{ID: "a"}},
	}); err == nil {
		t.Fatal("expected error for 500 response")
	}
}
