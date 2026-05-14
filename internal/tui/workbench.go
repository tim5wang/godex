package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/tim5wang/godex/internal/agent"
)

type workbenchTab int

const (
	workbenchTabTask workbenchTab = iota
	workbenchTabWorkers
	workbenchTabGraph
	workbenchTabDiff
	workbenchTabLogs
)

type workbenchSummary struct {
	Plan   []string
	Active []string
	Review []string
}

type outcomeStatus string

const (
	outcomeStatusIdle           outcomeStatus = "idle"
	outcomeStatusRunning        outcomeStatus = "running"
	outcomeStatusBlocked        outcomeStatus = "blocked"
	outcomeStatusReadyForReview outcomeStatus = "ready_for_review"
	outcomeStatusMerged         outcomeStatus = "merged"
	outcomeStatusFailed         outcomeStatus = "failed"
)

type outcomeSignal struct {
	Kind   string
	ID     string
	Status string
	Detail string
}

type workbenchOutcome struct {
	Status         outcomeStatus
	Title          string
	Detail         string
	LongTaskID     string
	LongTaskStatus string
	WorkerID       string
	WorkerStatus   string
	MergeStatus    string
	Recovered      bool
	HasLongTask    bool
	HasWorker      bool
	LongTask       agent.LongTaskView
	Worker         agent.DurableSubagentJobView
	Signals        []outcomeSignal
}

func (m *model) buildWorkbenchSummary() workbenchSummary {
	summary := workbenchSummary{
		Plan:   m.workbenchPlanLines(),
		Active: m.workbenchActiveLines(),
		Review: m.workbenchReviewLines(),
	}
	return summary
}

func (m *model) workbenchPlanLines() []string {
	outcomes := m.buildWorkbenchOutcomes()
	lines := make([]string, 0, len(outcomes)+len(m.snapshot.Tasks)+len(m.snapshot.Todos)+len(m.snapshot.QueuedTurns)+1)
	for _, outcome := range outcomes {
		if outcome.Status == outcomeStatusIdle {
			continue
		}
		lines = append(lines, formatOutcomePlanLine(outcome))
	}
	for _, item := range m.snapshot.Tasks {
		if item == nil {
			continue
		}
		title := strings.TrimSpace(item.Subject)
		if title == "" {
			title = fmt.Sprintf("task-%d", item.ID)
		}
		if title != "" {
			lines = append(lines, "task · "+title)
		}
	}
	for _, item := range m.snapshot.Todos {
		title := strings.TrimSpace(item.Content)
		if title != "" {
			lines = append(lines, "todo · "+title)
		}
	}
	for _, item := range m.snapshot.QueuedTurns {
		title := firstNonBlank(item.Summary, item.ID)
		if title != "" {
			lines = append(lines, fmt.Sprintf("%s · %s", item.ID, title))
		}
	}
	if len(lines) == 0 {
		return []string{"No active plan. Start with a request or run a longtask."}
	}
	return lines
}

func (m *model) workbenchActiveLines() []string {
	outcomes := m.buildWorkbenchOutcomes()
	lines := make([]string, 0, len(outcomes)+4)
	if m.snapshot.Running {
		turn := strings.TrimSpace(m.snapshot.ActiveTurnID)
		if turn == "" {
			turn = "active turn"
		}
		phase := strings.TrimSpace(m.snapshot.ActivePhase)
		if phase == "" {
			phase = "running"
		}
		lines = append(lines, fmt.Sprintf("%s · %s", turn, phase))
	}
	for _, outcome := range outcomes {
		switch outcome.Status {
		case outcomeStatusRunning, outcomeStatusBlocked:
			lines = append(lines, formatOutcomeActiveLine(outcome))
		default:
			continue
		}
	}
	if len(lines) == 0 {
		return []string{"Idle"}
	}
	return lines
}

func (m *model) workbenchReviewLines() []string {
	outcomes := m.buildWorkbenchOutcomes()
	lines := make([]string, 0, len(m.snapshot.PendingPermissions)+len(outcomes)+1)
	for _, pending := range m.snapshot.PendingPermissions {
		title := strings.TrimSpace(pending.ID)
		if title == "" {
			title = "permission"
		}
		action := strings.TrimSpace(pending.Request.Action)
		if action == "" {
			action = "approval"
		}
		tool := strings.TrimSpace(pending.Request.ToolName)
		if tool != "" {
			action = tool + " " + action
		}
		lines = append(lines, fmt.Sprintf("%s · pending %s", title, action))
	}
	for _, outcome := range outcomes {
		switch outcome.Status {
		case outcomeStatusReadyForReview, outcomeStatusMerged, outcomeStatusBlocked, outcomeStatusFailed:
			if outcome.HasWorker || outcome.Status == outcomeStatusFailed {
				lines = append(lines, formatOutcomeReviewLine(outcome))
			}
		}
	}
	if len(lines) == 0 {
		return []string{"Nothing waiting for review"}
	}
	return lines
}

func (m *model) workbenchWorkerLines() []string {
	outcomes := m.buildWorkbenchOutcomes()
	lines := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		if !outcome.HasWorker {
			continue
		}
		lines = append(lines, formatOutcomeWorkerLine(outcome))
	}
	if len(lines) == 0 {
		return []string{"No durable workers in this session"}
	}
	return lines
}

func (m *model) workbenchGraphLines() []string {
	outcomes := m.buildWorkbenchOutcomes()
	lines := []string{
		"session · " + m.sessionID,
		"active branch · main",
	}
	for _, outcome := range outcomes {
		if outcome.HasLongTask && outcome.HasWorker {
			lines = append(lines, fmt.Sprintf("longtask %s %s -> worker %s %s", outcome.LongTaskID, firstNonBlank(outcome.LongTaskStatus, "unknown"), outcome.WorkerID, firstNonBlank(outcome.MergeStatus, outcome.WorkerStatus, string(outcome.Status))))
			continue
		}
		if !outcome.HasWorker {
			continue
		}
		job := outcome.Worker
		if job.SourceBranchID == "" && job.SourceNodeID == "" && job.WorkerBranchID == "" {
			continue
		}
		title := firstNonBlank(job.DisplayTitle, job.JobID)
		lines = append(lines, fmt.Sprintf("%s · source %s@%s · worker %s", title, firstNonBlank(job.SourceBranchID, "main"), firstNonBlank(job.SourceNodeID, "head"), firstNonBlank(job.WorkerBranchID, "n/a")))
	}
	return lines
}

func (m *model) buildWorkbenchOutcomes() []workbenchOutcome {
	outcomes := make([]workbenchOutcome, 0, len(m.longTasks)+len(m.subagents))
	matchedWorkers := make(map[int]struct{})
	for _, longTask := range m.longTasks {
		outcome := outcomeFromLongTask(longTask)
		if workerIndex, ok := m.matchWorkerForLongTask(longTask, matchedWorkers); ok {
			worker := m.subagents[workerIndex]
			matchedWorkers[workerIndex] = struct{}{}
			outcome = mergeLongTaskWorkerOutcome(outcome, worker)
		}
		outcomes = append(outcomes, outcome)
	}
	for i, worker := range m.subagents {
		if _, ok := matchedWorkers[i]; ok {
			continue
		}
		outcomes = append(outcomes, outcomeFromWorker(worker))
	}
	return outcomes
}

func (m *model) matchWorkerForLongTask(longTask agent.LongTaskView, used map[int]struct{}) (int, bool) {
	for i, worker := range m.subagents {
		if _, ok := used[i]; ok {
			continue
		}
		if longTaskMatchesWorkerByJobID(longTask, worker.JobID) {
			return i, true
		}
	}
	for i, worker := range m.subagents {
		if _, ok := used[i]; ok {
			continue
		}
		if longTaskMatchesWorkerByPath(longTask, worker) {
			return i, true
		}
	}
	return 0, false
}

func outcomeFromLongTask(longTask agent.LongTaskView) workbenchOutcome {
	id := firstNonBlank(longTask.LongTaskID, longTask.WorkflowID)
	status := classifyLongTaskStatus(longTask)
	title := firstNonBlank(longTask.Project, firstLongTaskStoryTitle(longTask), longTask.Description, longTask.WorkflowID, longTask.LongTaskID, "longtask")
	return workbenchOutcome{
		Status:         status,
		Title:          title,
		Detail:         formatLongTaskDetail(longTask),
		LongTaskID:     id,
		LongTaskStatus: strings.TrimSpace(longTask.Status),
		HasLongTask:    true,
		LongTask:       longTask,
		Signals: []outcomeSignal{{
			Kind:   "longtask",
			ID:     id,
			Status: firstNonBlank(longTask.Status, string(status)),
			Detail: formatLongTaskDetail(longTask),
		}},
	}
}

func outcomeFromWorker(worker agent.DurableSubagentJobView) workbenchOutcome {
	status := classifyWorkerOutcomeStatus(worker)
	title := firstNonBlank(worker.DisplayTitle, worker.Objective, worker.JobID, "worker")
	return workbenchOutcome{
		Status:       status,
		Title:        title,
		Detail:       firstNonBlank(worker.LastMessage, worker.Result),
		WorkerID:     worker.JobID,
		WorkerStatus: strings.TrimSpace(worker.Status),
		MergeStatus:  strings.TrimSpace(worker.MergeStatus),
		HasWorker:    true,
		Worker:       worker,
		Signals: []outcomeSignal{{
			Kind:   "worker",
			ID:     worker.JobID,
			Status: firstNonBlank(worker.MergeStatus, worker.Status, string(status)),
			Detail: firstNonBlank(worker.LastMessage, worker.Result),
		}},
	}
}

func mergeLongTaskWorkerOutcome(longOutcome workbenchOutcome, worker agent.DurableSubagentJobView) workbenchOutcome {
	workerOutcome := outcomeFromWorker(worker)
	longOutcome.HasWorker = true
	longOutcome.Worker = worker
	longOutcome.WorkerID = worker.JobID
	longOutcome.WorkerStatus = workerOutcome.WorkerStatus
	longOutcome.MergeStatus = workerOutcome.MergeStatus
	longOutcome.Signals = append(longOutcome.Signals, workerOutcome.Signals...)
	if workerOutcome.Title != "" && (longOutcome.Title == "" || longOutcome.Title == "longtask") {
		longOutcome.Title = workerOutcome.Title
	}
	longFailed := longOutcome.Status == outcomeStatusFailed || strings.EqualFold(longOutcome.LongTaskStatus, "error") || strings.EqualFold(longOutcome.LongTaskStatus, "failed")
	if longFailed && workerOutcome.Status != outcomeStatusFailed {
		longOutcome.Recovered = true
	}
	switch workerOutcome.Status {
	case outcomeStatusMerged, outcomeStatusReadyForReview, outcomeStatusBlocked, outcomeStatusRunning:
		longOutcome.Status = workerOutcome.Status
	case outcomeStatusFailed:
		if longOutcome.Status != outcomeStatusFailed {
			longOutcome.Status = workerOutcome.Status
		}
	}
	longOutcome.Detail = firstNonBlank(workerOutcome.Detail, longOutcome.Detail)
	return longOutcome
}

func formatSubagentExecutionLine(job agent.DurableSubagentJobView) string {
	title := firstNonBlank(job.DisplayTitle, job.Objective, job.JobID)
	parts := []string{job.JobID}
	if title != "" && title != job.JobID {
		parts = append(parts, title)
	}
	if job.LastPhase != "" {
		parts = append(parts, job.LastPhase)
	} else if job.Status != "" {
		parts = append(parts, job.Status)
	}
	if job.WorkerID != "" {
		parts = append(parts, job.WorkerID)
	}
	if job.SandboxID != "" {
		parts = append(parts, job.SandboxID)
	}
	if job.WorkerBranchID != "" {
		parts = append(parts, job.WorkerBranchID)
	}
	if job.SourceBranchID != "" {
		parts = append(parts, "source "+job.SourceBranchID)
	}
	return strings.Join(nonEmptyStrings(parts), " · ")
}

func formatOutcomePlanLine(outcome workbenchOutcome) string {
	parts := []string{outcomeStatusLabel(outcome.Status), outcome.Title}
	if outcome.HasLongTask && outcome.HasWorker && outcome.Recovered {
		parts = append(parts, "recovered from failed longtask "+outcome.LongTaskID)
	} else if outcome.HasLongTask {
		parts = append(parts, outcome.LongTaskID)
		parts = append(parts, formatLongTaskDetail(outcome.LongTask))
	}
	if outcome.HasWorker && outcome.WorkerID != "" {
		parts = append(parts, outcome.WorkerID)
	}
	return strings.Join(nonEmptyStrings(parts), " · ")
}

func formatOutcomeActiveLine(outcome workbenchOutcome) string {
	parts := []string{outcomeStatusLabel(outcome.Status)}
	if outcome.HasWorker {
		parts = append(parts, formatSubagentExecutionLine(outcome.Worker))
	} else {
		parts = append(parts, firstNonBlank(outcome.LongTaskID, outcome.Title))
	}
	if outcome.Status == outcomeStatusBlocked && outcome.Detail != "" {
		parts = append(parts, outcome.Detail)
	}
	return strings.Join(nonEmptyStrings(parts), " · ")
}

func formatOutcomeReviewLine(outcome workbenchOutcome) string {
	parts := []string{outcomeStatusLabel(outcome.Status)}
	if outcome.HasWorker {
		parts = append(parts, outcome.WorkerID, firstNonBlank(outcome.Title, outcome.Worker.DisplayTitle, outcome.Worker.Objective))
		if outcome.MergeStatus != "" {
			parts = append(parts, outcome.MergeStatus)
		}
		if outcome.Recovered && outcome.LongTaskID != "" {
			parts = append(parts, "recovered longtask "+outcome.LongTaskID)
		}
		return strings.Join(nonEmptyStrings(parts), " · ")
	}
	if outcome.HasLongTask {
		parts = append(parts, outcome.LongTaskID, outcome.Title, formatLongTaskDetail(outcome.LongTask))
	}
	return strings.Join(nonEmptyStrings(parts), " · ")
}

func formatOutcomeWorkerLine(outcome workbenchOutcome) string {
	line := formatSubagentExecutionLine(outcome.Worker)
	if outcome.Recovered && outcome.LongTaskID != "" {
		line += " · recovered longtask " + outcome.LongTaskID
	} else if outcome.HasLongTask && outcome.LongTaskID != "" {
		line += " · longtask " + outcome.LongTaskID
	}
	return line
}

func classifyLongTaskStatus(longTask agent.LongTaskView) outcomeStatus {
	status := strings.ToLower(strings.TrimSpace(longTask.Status))
	if longTask.Running > 0 || status == "running" {
		return outcomeStatusRunning
	}
	if longTask.Failed > 0 || status == "failed" || status == "error" {
		return outcomeStatusFailed
	}
	if longTask.Pending > 0 || status == "pending" {
		return outcomeStatusRunning
	}
	if status == "completed" || longTask.Completed > 0 && longTask.Total > 0 && longTask.Completed == longTask.Total {
		return outcomeStatusMerged
	}
	if status == "" {
		return outcomeStatusIdle
	}
	return outcomeStatusRunning
}

func classifyWorkerOutcomeStatus(worker agent.DurableSubagentJobView) outcomeStatus {
	status := strings.ToLower(strings.TrimSpace(worker.Status))
	merge := strings.ToLower(strings.TrimSpace(worker.MergeStatus))
	switch merge {
	case "merged", "no_changes":
		return outcomeStatusMerged
	}
	switch status {
	case "pending_approval":
		return outcomeStatusBlocked
	case "running", "pending", "resuming":
		return outcomeStatusRunning
	case "completed":
		return outcomeStatusReadyForReview
	case "failed", "cancelled", "canceled", "error", "timeout", "interrupted":
		return outcomeStatusFailed
	default:
		if merge != "" && merge != "merged" && merge != "no_changes" {
			return outcomeStatusReadyForReview
		}
		return outcomeStatusIdle
	}
}

func outcomeStatusLabel(status outcomeStatus) string {
	switch status {
	case outcomeStatusRunning:
		return "Running"
	case outcomeStatusBlocked:
		return "Blocked"
	case outcomeStatusReadyForReview:
		return "Ready for review"
	case outcomeStatusMerged:
		return "Merged"
	case outcomeStatusFailed:
		return "Failed"
	default:
		return "Idle"
	}
}

func longTaskMatchesWorkerByJobID(longTask agent.LongTaskView, workerID string) bool {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return false
	}
	for _, story := range longTask.Stories {
		if strings.TrimSpace(story.JobID) == workerID {
			return true
		}
	}
	return false
}

func longTaskMatchesWorkerByPath(longTask agent.LongTaskView, worker agent.DurableSubagentJobView) bool {
	longPaths := longTaskPathTokens(longTask)
	workerPaths := workerPathTokens(worker)
	return pathTokenSetsOverlap(longPaths, workerPaths)
}

func longTaskPathTokens(longTask agent.LongTaskView) map[string]struct{} {
	values := []string{
		longTask.Project,
		longTask.Description,
		longTask.WorkflowID,
		longTask.LongTaskID,
	}
	for _, story := range longTask.Stories {
		values = append(values, story.Title, story.Description, story.Error, story.ResultPreview)
		values = append(values, story.AcceptanceCriteria...)
	}
	return extractPathTokens(values...)
}

func workerPathTokens(worker agent.DurableSubagentJobView) map[string]struct{} {
	values := []string{
		worker.DisplayTitle,
		worker.Objective,
		worker.Prompt,
		worker.Result,
		worker.Error,
		worker.LastMessage,
	}
	values = append(values, worker.WriteScope...)
	return extractPathTokens(values...)
}

func extractPathTokens(values ...string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, value := range values {
		for _, field := range strings.FieldsFunc(value, func(r rune) bool {
			return unicode.IsSpace(r) || strings.ContainsRune("`'\"“”‘’()[]{}<>，,;:", r)
		}) {
			token := normalizePathToken(field)
			if token == "" {
				continue
			}
			out[token] = struct{}{}
		}
	}
	return out
}

func normalizePathToken(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, ".!?")
	value = strings.TrimPrefix(value, "file://")
	if value == "" || strings.Contains(value, "://") || !strings.Contains(value, "/") {
		return ""
	}
	value = filepath.Clean(value)
	value = strings.TrimPrefix(value, "./")
	value = strings.Trim(value, "/")
	if value == "." || value == "" || strings.Count(value, "/") == 0 {
		return ""
	}
	return value
}

func pathTokenSetsOverlap(left, right map[string]struct{}) bool {
	for l := range left {
		for r := range right {
			if pathsShareSpecificPrefix(l, r) {
				return true
			}
		}
	}
	return false
}

func pathsShareSpecificPrefix(left, right string) bool {
	if left == right {
		return true
	}
	shorter, longer := left, right
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	return strings.HasPrefix(longer, shorter+"/")
}

func formatLongTaskDetail(longTask agent.LongTaskView) string {
	status := strings.TrimSpace(longTask.Status)
	if status == "" {
		status = string(classifyLongTaskStatus(longTask))
	}
	return fmt.Sprintf("%s %d/%d · pending %d · failed %d", status, longTask.Running, longTask.Total, longTask.Pending, longTask.Failed)
}

func firstLongTaskStoryTitle(longTask agent.LongTaskView) string {
	for _, story := range longTask.Stories {
		if title := strings.TrimSpace(story.Title); title != "" {
			return title
		}
	}
	return ""
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func nonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}
