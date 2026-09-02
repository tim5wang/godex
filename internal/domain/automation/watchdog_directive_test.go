package automation

import (
	"context"
	"strings"
	"testing"
)

func TestParseWatchdogDirective(t *testing.T) {
	d, err := ParseWatchdogDirective(`{"query":"taskboard","wake_when":[{"metric":"error_count","op":">","value":0}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.Query != "taskboard" || len(d.WakeWhen) != 1 || d.WakeWhen[0].Metric != "error_count" {
		t.Fatalf("unexpected directive: %#v", d)
	}
}

func TestParseWatchdogDirectiveDefaultsQuery(t *testing.T) {
	d, err := ParseWatchdogDirective(`{"wake_when":[{"metric":"done_count","op":"==","value":5}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.Query != "taskboard" {
		t.Fatalf("expected default taskboard query, got %q", d.Query)
	}
}

func TestParseWatchdogDirectiveErrors(t *testing.T) {
	cases := []string{
		"",
		"not json",
		`{"query":"unknown"}`,
		`{"query":"taskboard","wake_when":[{"op":">","value":0}]}`,       // missing metric
		`{"query":"taskboard","wake_when":[{"metric":"x","op":"~~","value":0}]}`, // bad op
	}
	for _, raw := range cases {
		if _, err := ParseWatchdogDirective(raw); err == nil {
			t.Fatalf("expected error for %q", raw)
		}
	}
}

func TestWatchdogDirectiveEvaluate(t *testing.T) {
	counts := map[string]int{
		"error_count": 2,
		"done_count":  3,
		"total_count": 3,
	}
	tests := []struct {
		name string
		dir  WatchdogDirective
		want bool
	}{
		{
			name: "error gt 0",
			dir:  WatchdogDirective{Query: "taskboard", WakeWhen: []DirectiveCondition{{Metric: "error_count", Op: ">", Value: 0}}},
			want: true,
		},
		{
			name: "done eq total",
			dir:  WatchdogDirective{Query: "taskboard", WakeWhen: []DirectiveCondition{{Metric: "done_count", Op: "==", Value: 3}}},
			want: true,
		},
		{
			name: "stalled gt 0 (no match)",
			dir:  WatchdogDirective{Query: "taskboard", WakeWhen: []DirectiveCondition{{Metric: "stalled_count", Op: ">", Value: 0}}},
			want: false,
		},
		{
			name: "any condition OR semantics",
			dir: WatchdogDirective{Query: "taskboard", WakeWhen: []DirectiveCondition{
				{Metric: "stalled_count", Op: ">", Value: 0},
				{Metric: "error_count", Op: ">=", Value: 1},
			}},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := tt.dir.Evaluate(counts)
			if got != tt.want {
				t.Fatalf("Evaluate() = %v, want %v (reason %q)", got, tt.want, reason)
			}
		})
	}
}

func TestWatchdogDirectiveEvaluateAllEmpty(t *testing.T) {
	// Empty wake_when list never wakes (pure gate-off).
	d := WatchdogDirective{Query: "taskboard"}
	wake, reason := d.Evaluate(map[string]int{"error_count": 1})
	if wake || reason != "" {
		t.Fatalf("empty wake_when should never wake, got %v/%q", wake, reason)
	}
}

type stubDirectiveEvaluator struct {
	decision DirectiveDecision
	err      error
}

func (s stubDirectiveEvaluator) Evaluate(_ context.Context, _ string) (DirectiveDecision, error) {
	return s.decision, s.err
}

func TestDirectiveEvaluatorInterface(t *testing.T) {
	var ev DirectiveEvaluator = stubDirectiveEvaluator{decision: DirectiveDecision{Wake: true, Reason: "error_count > 0", Summary: "total=4 error=1"}}
	dec, err := ev.Evaluate(context.Background(), `{"query":"taskboard"}`)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !dec.Wake || !strings.Contains(dec.Summary, "total=4") {
		t.Fatalf("unexpected decision: %#v", dec)
	}
}
