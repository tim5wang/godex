package backend

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
)

// TaskboardDirectiveEvaluator evaluates cron watchdog directives against the
// taskboard status counts. It runs a reconcile pass first — the same semantics
// as the board's 对账 button — so running executions get observed/finalized and
// stalled marks are fresh, then evaluates the directive's wake conditions.
//
// This is the "cron 调用 godex 内联指令" bridge: a PJM writes a declarative
// watchdog_directive (e.g. {"wake_when":[{"metric":"error_count","op":">","value":0}]})
// instead of a python script that reads godex json files.
type TaskboardDirectiveEvaluator struct {
	executor *TaskboardExecutor
	ledger   *taskboard.Ledger
	now      func() time.Time
}

// NewTaskboardDirectiveEvaluator wires the evaluator against the executor and
// the shared ledger. executor may be nil; the directive then still evaluates
// pure ledger counts but without a reconcile refresh.
func NewTaskboardDirectiveEvaluator(executor *TaskboardExecutor, ledger *taskboard.Ledger) *TaskboardDirectiveEvaluator {
	return &TaskboardDirectiveEvaluator{executor: executor, ledger: ledger, now: time.Now}
}

// Evaluate implements automation.DirectiveEvaluator.
func (e *TaskboardDirectiveEvaluator) Evaluate(ctx context.Context, directive string) (automation.DirectiveDecision, error) {
	d, err := automation.ParseWatchdogDirective(directive)
	if err != nil {
		return automation.DirectiveDecision{}, err
	}
	projectID := strings.TrimSpace(d.ProjectID)

	sc := e.ledger.StatusCounts(projectID)
	// Reconcile first (the 对账 button semantics) so running executions are
	// observed / finalized and stall marks are fresh, then fold the per-project
	// stalled count back into the snapshot.
	if e.executor != nil {
		report, rerr := e.executor.Reconcile(ctx)
		if rerr != nil {
			return automation.DirectiveDecision{}, fmt.Errorf("taskboard reconcile before directive: %w", rerr)
		}
		sc = e.ledger.StatusCounts(projectID)
		sc.Stalled = e.stalledForProject(ctx, report, projectID)
		sc.UpdatedAt = e.now()
	}

	wake, reason := d.Evaluate(sc.CountMap())
	summary := formatStatusCounts(sc)
	return automation.DirectiveDecision{Wake: wake, Reason: reason, Summary: summary}, nil
}

// stalledForProject counts reconcile-marked stalled executions that belong to
// the target project (empty = all projects).
func (e *TaskboardDirectiveEvaluator) stalledForProject(ctx context.Context, report taskboard.ReconcileReport, projectID string) int {
	if projectID == "" {
		return report.Stalled
	}
	var count int
	for _, r := range report.Results {
		if !r.Stall {
			continue
		}
		if card, err := e.ledger.GetCard(r.CardID); err == nil && card.ProjectID == projectID {
			count++
		}
	}
	return count
}

// formatStatusCounts renders a compact, human-readable snapshot for the run log
// and for prompt injection so the woken agent (PJM) sees the state directly.
func formatStatusCounts(sc taskboard.StatusCounts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "total=%d", sc.Total)
	if sc.Error > 0 {
		fmt.Fprintf(&b, " error=%d", sc.Error)
	}
	if sc.Stalled > 0 {
		fmt.Fprintf(&b, " stalled=%d", sc.Stalled)
	}
	statuses := make([]string, 0, len(sc.Cards))
	for status := range sc.Cards {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	for _, status := range statuses {
		fmt.Fprintf(&b, " %s=%d", status, sc.Cards[status])
	}
	return b.String()
}
