package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// WatchdogDirective is a declarative pre-run gate for a cron job. It replaces
// the "PJM 写 python 直读 godex json" pattern with a native, inspectable rule:
// the directive describes which metric to query (e.g. taskboard status counts)
// and the wake conditions that should fire the agent (e.g. PJM).
//
// A cron job may set watchdog_directive instead of (or in addition to) a shell
// watchdog_script. When the job fires, the cron runtime asks the injected
// DirectiveEvaluator to evaluate the directive: if it says "wake", the agent
// runs; if it says "sleep", the tick is suppressed at zero token cost.
type WatchdogDirective struct {
	// Query selects the metric source. Currently only "taskboard" is wired.
	Query string `json:"query"`
	// ProjectID optionally narrows the taskboard query to one project.
	ProjectID string `json:"project_id,omitempty"`
	// WakeWhen lists the conditions that wake the agent. Conditions are OR'd:
	// any met condition wakes; empty list never wakes (pure gate-off).
	WakeWhen []DirectiveCondition `json:"wake_when,omitempty"`
}

// DirectiveCondition is one wake predicate over the queried counts.
type DirectiveCondition struct {
	// Metric is the count key, e.g. "error_count", "stalled_count",
	// "done_count", "total_count", "running_count".
	Metric string `json:"metric"`
	// Op is one of >, >=, <, <=, ==, !=
	Op string `json:"op"`
	// Value is the numeric threshold to compare against.
	Value float64 `json:"value"`
}

// DirectiveDecision is the result of evaluating one directive.
type DirectiveDecision struct {
	// Wake is true when the agent should run (conditions met).
	Wake bool `json:"wake"`
	// Reason is a human-readable explanation (which condition matched).
	Reason string `json:"reason,omitempty"`
	// Summary is a compact snapshot of the queried counts for the run log /
	// prompt injection so the woken agent (PJM) sees the state directly.
	Summary string `json:"summary,omitempty"`
}

// ParseWatchdogDirective parses a JSON-encoded watchdog directive.
func ParseWatchdogDirective(raw string) (WatchdogDirective, error) {
	var d WatchdogDirective
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return d, fmt.Errorf("watchdog directive is empty")
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return d, fmt.Errorf("invalid watchdog directive: %w", err)
	}
	if strings.TrimSpace(d.Query) == "" {
		d.Query = "taskboard"
	}
	if err := d.Validate(); err != nil {
		return d, err
	}
	return d, nil
}

// Validate checks the directive structure.
func (d WatchdogDirective) Validate() error {
	switch d.Query {
	case "taskboard":
	default:
		return fmt.Errorf("unsupported watchdog directive query %q", d.Query)
	}
	for i, c := range d.WakeWhen {
		if strings.TrimSpace(c.Metric) == "" {
			return fmt.Errorf("watchdog condition %d: metric is required", i)
		}
		switch c.Op {
		case ">", ">=", "<", "<=", "==", "!=":
		default:
			return fmt.Errorf("watchdog condition %d: unsupported op %q", i, c.Op)
		}
	}
	return nil
}

// Evaluate checks the conditions against a count map. It returns whether to
// wake, plus a reason describing which condition(s) matched.
func (d WatchdogDirective) Evaluate(counts map[string]int) (bool, string) {
	matched := make([]string, 0, len(d.WakeWhen))
	for _, c := range d.WakeWhen {
		v, ok := counts[c.Metric]
		if !ok {
			continue
		}
		if compareCount(v, c.Op, c.Value) {
			matched = append(matched, fmt.Sprintf("%s %s %g", c.Metric, c.Op, c.Value))
		}
	}
	if len(matched) == 0 {
		return false, ""
	}
	return true, strings.Join(matched, ", ")
}

func compareCount(left int, op string, right float64) bool {
	switch op {
	case ">":
		return float64(left) > right
	case ">=":
		return float64(left) >= right
	case "<":
		return float64(left) < right
	case "<=":
		return float64(left) <= right
	case "==":
		return float64(left) == right
	case "!=":
		return float64(left) != right
	}
	return false
}

// DirectiveEvaluator evaluates a watchdog directive for the cron runtime. The
// concrete implementation (e.g. taskboard-backed) is injected at assembly time
// so the cron runtime stays agnostic about the metric source.
type DirectiveEvaluator interface {
	Evaluate(ctx context.Context, directive string) (DirectiveDecision, error)
}
