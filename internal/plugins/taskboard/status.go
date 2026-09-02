package taskboard

import "time"

// StatusCounts is a read-only snapshot of the board grouped by card status and
// execution status. It powers both the `taskboard status` action / HTTP
// endpoint and the cron watchdog-directive evaluator, so a PJM can write a
// declarative wake rule ("wake me when error_count > 0") instead of a python
// script that reads godex json files directly.
type StatusCounts struct {
	// Total is the number of non-deleted cards (optionally project-scoped).
	Total int `json:"total"`
	// Cards maps card status -> count (backlog/todo/in_progress/in_review/done).
	Cards map[string]int `json:"cards"`
	// Executions maps execution status -> count (running/completed/failed/cancelled).
	Executions map[string]int `json:"executions"`
	// Error is the number of executions in a failing state: terminal failed, or
	// running with a recorded error observation.
	Error int `json:"error"`
	// Stalled is the number of running executions marked stalled by the last
	// reconcile pass (idle past the threshold). Filled by the backend evaluator
	// which runs a reconcile first; zero when no reconcile ran.
	Stalled int `json:"stalled,omitempty"`
	// UpdatedAt is when this snapshot was computed.
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// StatusCounts computes the read-only counts for a project (empty = all).
// It is pure ledger math — no reconcile, no session observation — so the
// status action stays side-effect free. The backend watchdog evaluator layers
// a reconcile pass on top for stalled/error freshness.
func (l *Ledger) StatusCounts(projectID string) StatusCounts {
	cards := l.ListCards(CardFilter{ProjectID: projectID})
	sc := StatusCounts{
		Cards:      map[string]int{},
		Executions: map[string]int{},
		UpdatedAt:  time.Now(),
	}
	for _, c := range cards {
		sc.Total++
		sc.Cards[c.Status]++
		for _, ex := range c.Executions {
			sc.Executions[ex.Status]++
			if ex.Status == ExecutionFailed || (ex.Status == ExecutionRunning && ex.ErrorType != "") {
				sc.Error++
			}
		}
	}
	return sc
}

// CountMap returns the flat metric map used by watchdog directive conditions
// (e.g. total_count, done_count, error_count, running_count, ...).
func (sc StatusCounts) CountMap() map[string]int {
	m := map[string]int{
		"total_count": sc.Total,
		"error_count": sc.Error,
		"stalled_count": sc.Stalled,
	}
	for status, count := range sc.Cards {
		m[status+"_count"] = count
	}
	for status, count := range sc.Executions {
		m[status+"_count"] = count
	}
	return m
}
