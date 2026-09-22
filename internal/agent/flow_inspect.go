package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Flow inspection (P3 Agent 闭环 §22.2 定期巡检)
//
// cron/手动触发：拉取已发布 flow 的运行记录，聚合失败率/卡点（waiting_human
// 超时、error 节点、迭代上限），生成巡检报告。报告不落盘，由调用方展示或
// 转成卡片（前端巡检报告卡片 / 后续自动化）。
// ---------------------------------------------------------------------------

// FlowInspectionSummary is the per-flow aggregation block of a report.
type FlowInspectionSummary struct {
	FlowID       string    `json:"flow_id"`
	PublishedVer string    `json:"published_version,omitempty"`
	Total        int       `json:"total"`
	Completed    int       `json:"completed"`
	Failed       int       `json:"failed"`
	Canceled     int       `json:"canceled"`
	Waiting      int       `json:"waiting"`
	Running      int       `json:"running"`
	FailureRate  float64   `json:"failure_rate"`
	LastRunAt    time.Time `json:"last_run_at,omitempty"`
	// Bottlenecks observed across the window.
	ErrorNodes        int `json:"error_nodes"`         // function_error / decision_failed / function_failed events
	HumanWaiting      int `json:"human_waiting"`       // runs currently stuck in waiting_human
	IterationCaps     int `json:"iteration_caps"`      // edge_iteration_cap events
	LatestErrorRuns   []string `json:"latest_error_runs,omitempty"` // run ids (recent first)
}

// FlowInspectionReport aggregates run health across published flows.
type FlowInspectionReport struct {
	GeneratedAt time.Time              `json:"generated_at"`
	WindowHours int                    `json:"window_hours"`
	Total       int                    `json:"total"`
	Failed      int                    `json:"failed"`
	Waiting     int                    `json:"waiting"`
	FailureRate float64                `json:"failure_rate"`
	Flows       []FlowInspectionSummary `json:"flows"`
}

// InspectFlows aggregates the run history of all published flows over the
// given window (hours). windowHours <= 0 defaults to 24.
func (a *Agent) InspectFlows(windowHours int) (*FlowInspectionReport, error) {
	if a == nil || a.flows == nil || a.workflows == nil {
		return nil, fmt.Errorf("flow runtime unavailable")
	}
	if windowHours <= 0 {
		windowHours = 24
	}
	window := time.Duration(windowHours) * time.Hour
	cutoff := time.Now().UTC().Add(-window)
	report := &FlowInspectionReport{
		GeneratedAt: time.Now().UTC(),
		WindowHours: windowHours,
		Flows:       []FlowInspectionSummary{},
	}
	ids, err := a.flows.listFlows()
	if err != nil {
		return nil, err
	}
	for _, flowID := range ids {
		cur, err := a.flows.loadCurrent(flowID)
		if err != nil || cur.Published == "" {
			continue // only published flows are inspected
		}
		sum, err := a.inspectOneFlow(flowID, cur.Published, cutoff)
		if err != nil {
			continue
		}
		report.Total += sum.Total
		report.Failed += sum.Failed
		report.Waiting += sum.Waiting
		report.Flows = append(report.Flows, *sum)
	}
	if report.Total > 0 {
		report.FailureRate = float64(report.Failed) / float64(report.Total)
	}
	sort.Slice(report.Flows, func(i, j int) bool {
		return report.Flows[i].FailureRate > report.Flows[j].FailureRate
	})
	return report, nil
}

// inspectOneFlow aggregates one flow's runs within the window.
func (a *Agent) inspectOneFlow(flowID, publishedVer string, cutoff time.Time) (*FlowInspectionSummary, error) {
	recs, err := a.flows.listRuns(flowID)
	if err != nil {
		return nil, err
	}
	sum := &FlowInspectionSummary{
		FlowID:       flowID,
		PublishedVer: publishedVer,
	}
	latestError := make([]string, 0, 3)
	for i := len(recs) - 1; i >= 0; i-- {
		rec := recs[i]
		if rec.StartedAt.Before(cutoff) {
			continue
		}
		sum.Total++
		switch rec.Status {
		case workflowStatusCompleted:
			sum.Completed++
		case workflowStatusError:
			sum.Failed++
			if len(latestError) < 3 {
				latestError = append(latestError, rec.RunID)
			}
		case workflowStatusCanceled:
			sum.Canceled++
		case workflowStatusWaitingHuman:
			sum.Waiting++
			sum.HumanWaiting++
		case workflowStatusRunning:
			sum.Running++
		}
		if rec.StartedAt.After(sum.LastRunAt) {
			sum.LastRunAt = rec.StartedAt
		}
		// Bottleneck events from the workflow event log.
		if rec.WorkflowID != "" {
			path := a.workflows.dir + "/" + rec.WorkflowID + "/" + workflowEventsFile
			for _, ev := range readWorkflowEvents(path) {
				switch ev["event"] {
				case "function_error", "decision_failed", "function_failed":
					sum.ErrorNodes++
				case "edge_iteration_cap":
					sum.IterationCaps++
				}
			}
		}
	}
	if sum.Total > 0 {
		sum.FailureRate = float64(sum.Failed) / float64(sum.Total)
	}
	sum.LatestErrorRuns = latestError
	return sum, nil
}

// InspectionCardMarkdown renders the report as a compact card-friendly
// markdown block (used by the UI inspection card / cron report).
func (r *FlowInspectionReport) InspectionCardMarkdown() string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Flow 巡检** · %s · 窗口 %dh\n\n", r.GeneratedAt.Format("2006-01-02 15:04"), r.WindowHours))
	b.WriteString(fmt.Sprintf("运行 %d · 失败 %d · 失败率 %.0f%% · 等待人工 %d\n\n", r.Total, r.Failed, r.FailureRate*100, r.Waiting))
	if len(r.Flows) == 0 {
		b.WriteString("（窗口内无已发布 flow 的运行记录）")
		return b.String()
	}
	b.WriteString("| Flow | 运行 | 失败率 | 卡点 |\n|---|---|---|---|\n")
	for _, f := range r.Flows {
		bottlenecks := fmt.Sprintf("error节点 %d", f.ErrorNodes)
		if f.HumanWaiting > 0 {
			bottlenecks += fmt.Sprintf(" · 人工卡 %d", f.HumanWaiting)
		}
		if f.IterationCaps > 0 {
			bottlenecks += fmt.Sprintf(" · 迭代上限 %d", f.IterationCaps)
		}
		b.WriteString(fmt.Sprintf("| %s | %d | %.0f%% | %s |\n", f.FlowID, f.Total, f.FailureRate*100, bottlenecks))
	}
	return b.String()
}
