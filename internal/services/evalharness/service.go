package evalharness

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	evaldomain "github.com/tim5wang/godex/internal/domain/eval"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/platform/fsutil"
	"github.com/tim5wang/godex/internal/services/backend"
	"gopkg.in/yaml.v3"
)

const defaultCaseTimeout = 5 * time.Minute

//go:embed testdata
var embeddedTestdata embed.FS

// Backend is the runtime surface needed by the eval harness.
type Backend interface {
	OpenSession(context.Context, backend.SessionLocator) (*backend.OpenedSession, error)
	SetSessionModelProfile(context.Context, string, string) (backend.ModelsView, error)
	Submit(context.Context, string, message.Envelope) (*backend.SubmitResult, error)
	Snapshot(context.Context, string) (backend.Snapshot, error)
	Timeline(context.Context, string, int) ([]events.Event, error)
}

// Service executes eval suites and persists reports.
type Service struct {
	Backend  Backend
	LeadName string
	Now      func() time.Time
}

type caseOutput struct {
	Result   evaldomain.CaseResult
	Snapshot *backend.Snapshot
	Timeline []events.Event
}

// RunOptions controls one suite execution.
type RunOptions struct {
	SuitePath      string
	OutDir         string
	ModelProfileID string
}

// LoadSuite reads a godex.eval.yaml suite from disk.
func LoadSuite(path string) (evaldomain.Suite, error) {
	data, err := readSuiteFile(path)
	if err != nil {
		return evaldomain.Suite{}, err
	}
	var suite evaldomain.Suite
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&suite); err != nil {
		return evaldomain.Suite{}, err
	}
	if strings.TrimSpace(suite.Name) == "" {
		return evaldomain.Suite{}, fmt.Errorf("eval suite missing name")
	}
	if len(suite.Cases) == 0 {
		return evaldomain.Suite{}, fmt.Errorf("eval suite %q has no cases", suite.Name)
	}
	seen := map[string]struct{}{}
	for idx, item := range suite.Cases {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			return evaldomain.Suite{}, fmt.Errorf("eval case %d missing id", idx+1)
		}
		if _, ok := seen[id]; ok {
			return evaldomain.Suite{}, fmt.Errorf("duplicate eval case id %q", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(item.Prompt) == "" && strings.TrimSpace(item.ReplayFixture) == "" {
			return evaldomain.Suite{}, fmt.Errorf("eval case %q missing prompt or replay_fixture", id)
		}
	}
	return suite, nil
}

// RunSuite loads and executes one suite, writing report artifacts to disk.
func (s *Service) RunSuite(ctx context.Context, opts RunOptions) (evaldomain.Report, error) {
	if s == nil {
		return evaldomain.Report{}, fmt.Errorf("eval service is unavailable")
	}
	suite, err := LoadSuite(opts.SuitePath)
	if err != nil {
		return evaldomain.Report{}, err
	}
	if suiteRequiresBackend(suite) && s.Backend == nil {
		return evaldomain.Report{}, fmt.Errorf("eval backend is unavailable")
	}
	now := s.now()
	runID := fmt.Sprintf("%s-%s", safeName(suite.Name), now.Format("20060102-150405.000000000"))
	outDir := strings.TrimSpace(opts.OutDir)
	if outDir == "" {
		outDir = defaultRunsDir()
	}
	runDir := filepath.Join(outDir, runID)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return evaldomain.Report{}, err
	}
	report := evaldomain.Report{
		RunID:      runID,
		SuiteName:  suite.Name,
		StartedAt:  now,
		TotalCases: len(suite.Cases),
		Results:    make([]evaldomain.CaseResult, 0, len(suite.Cases)),
	}
	resultsPath := filepath.Join(runDir, "results.jsonl")
	resultsFile, err := os.OpenFile(resultsPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return evaldomain.Report{}, err
	}
	defer resultsFile.Close()
	writer := bufio.NewWriter(resultsFile)
	defer writer.Flush()
	suiteDir := filepath.Dir(opts.SuitePath)
	for _, item := range suite.Cases {
		output := s.runCase(ctx, suite.Name, suiteDir, item, opts.ModelProfileID)
		result := output.Result
		report.Results = append(report.Results, result)
		if result.Passed {
			report.PassedCases++
		} else {
			report.FailedCases++
		}
		_ = writeCaseArtifacts(runDir, output)
		data, _ := json.Marshal(result)
		_, _ = writer.Write(append(data, '\n'))
	}
	report.CompletedAt = s.now()
	report.Passed = report.FailedCases == 0
	if err := fsutil.WriteJSONAtomic(filepath.Join(runDir, "report.json"), report, 0644); err != nil {
		return evaldomain.Report{}, err
	}
	return report, nil
}

func suiteRequiresBackend(suite evaldomain.Suite) bool {
	for _, item := range suite.Cases {
		if strings.TrimSpace(item.ReplayFixture) == "" {
			return true
		}
	}
	return false
}

// ListReports returns report summaries under an eval runs directory.
func ListReports(dir string) ([]evaldomain.Report, error) {
	if strings.TrimSpace(dir) == "" {
		dir = defaultRunsDir()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	reports := make([]evaldomain.Report, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		report, err := ReadReport(filepath.Join(dir, entry.Name()))
		if err == nil {
			reports = append(reports, report)
		}
	}
	for i := 0; i < len(reports); i++ {
		for j := i + 1; j < len(reports); j++ {
			if reports[j].StartedAt.After(reports[i].StartedAt) {
				reports[i], reports[j] = reports[j], reports[i]
			}
		}
	}
	return reports, nil
}

func defaultRunsDir() string {
	if home := strings.TrimSpace(os.Getenv("GODEX_HOME")); home != "" {
		return filepath.Join(home, "evals", "runs")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".godex", "evals", "runs")
	}
	return filepath.Join("godex-evals", "runs")
}

// ReadReport reads either a run directory or report.json path.
func ReadReport(path string) (evaldomain.Report, error) {
	info, err := os.Stat(path)
	if err != nil {
		return evaldomain.Report{}, err
	}
	if info.IsDir() {
		path = filepath.Join(path, "report.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return evaldomain.Report{}, err
	}
	var report evaldomain.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return evaldomain.Report{}, err
	}
	return report, nil
}

func (s *Service) runCase(ctx context.Context, suiteName, suiteDir string, item evaldomain.Case, overrideProfileID string) caseOutput {
	if strings.TrimSpace(item.ReplayFixture) != "" {
		return s.runReplayCase(suiteDir, item)
	}
	started := s.now()
	result := evaldomain.CaseResult{ID: item.ID, Title: item.Title, StartedAt: started}
	output := caseOutput{Result: result}
	timeout := defaultCaseTimeout
	if item.TimeoutSeconds > 0 {
		timeout = time.Duration(item.TimeoutSeconds) * time.Second
	}
	caseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	locator := backend.SessionLocator{Channel: "eval", Key: safeName(suiteName) + "-" + safeName(item.ID)}
	opened, err := s.Backend.OpenSession(caseCtx, locator)
	if err != nil {
		result.CompletedAt = s.now()
		result.Error = err.Error()
		result.Failures = append(result.Failures, "open session: "+err.Error())
		output.Result = result
		return output
	}
	result.SessionID = opened.SessionID
	profileID := strings.TrimSpace(overrideProfileID)
	if profileID == "" {
		profileID = strings.TrimSpace(item.ModelProfileID)
	}
	if profileID != "" {
		result.ModelProfileID = profileID
		if _, err := s.Backend.SetSessionModelProfile(caseCtx, opened.SessionID, profileID); err != nil {
			result.CompletedAt = s.now()
			result.Error = err.Error()
			result.Failures = append(result.Failures, "set model profile: "+err.Error())
			output.Result = result
			return output
		}
	}
	lead := strings.TrimSpace(s.LeadName)
	if lead == "" {
		lead = "eval"
	}
	submit, err := s.Backend.Submit(caseCtx, opened.SessionID, message.NewCLIEnvelope(opened.SessionID, lead, item.Prompt, started))
	if submit != nil {
		result.TurnID = submit.TurnID
	}
	if err != nil {
		result.Error = err.Error()
		result.Failures = append(result.Failures, "submit: "+err.Error())
	}
	snapshot, snapshotErr := s.Backend.Snapshot(context.Background(), opened.SessionID)
	if snapshotErr == nil {
		output.Snapshot = &snapshot
		result.AssistantText = assistantText(snapshot.Messages)
	}
	timeline, timelineErr := s.Backend.Timeline(context.Background(), opened.SessionID, 500)
	if timelineErr == nil {
		output.Timeline = timeline
		result.Tools = extractTools(timeline)
	}
	result.Failures = append(result.Failures, evaluateExpectations(item.Expected, result.AssistantText, result.Tools)...)
	if snapshotErr != nil {
		result.Failures = append(result.Failures, "snapshot: "+snapshotErr.Error())
	}
	if timelineErr != nil {
		result.Failures = append(result.Failures, "timeline: "+timelineErr.Error())
	}
	result.CompletedAt = s.now()
	result.Passed = len(result.Failures) == 0
	output.Result = result
	return output
}

func (s *Service) runReplayCase(suiteDir string, item evaldomain.Case) caseOutput {
	started := s.now()
	result := evaldomain.CaseResult{ID: item.ID, Title: item.Title, StartedAt: started}
	output := caseOutput{Result: result}
	timeline, manifestSessionID, err := loadReplayFixtureTimeline(suiteDir, item.ReplayFixture)
	if err != nil {
		result.CompletedAt = s.now()
		result.Error = err.Error()
		result.Failures = append(result.Failures, "load replay fixture: "+err.Error())
		output.Result = result
		return output
	}
	result.SessionID = manifestSessionID
	result.TurnID = firstTimelineTurnID(timeline)
	result.AssistantText = assistantTextFromTimeline(timeline)
	result.Tools = extractTools(timeline)
	stability := analyzeTimelineStability(timeline)
	stability.Signals = appendForbiddenToolExchangeSignals(item.Expected, stability)
	result.InstabilitySignals = append([]string{}, stability.Signals...)
	result.Failures = append(result.Failures, evaluateExpectations(item.Expected, result.AssistantText, result.Tools)...)
	result.Failures = append(result.Failures, evaluateStabilityExpectations(item.Expected, stability)...)
	result.CompletedAt = s.now()
	result.Passed = len(result.Failures) == 0
	output.Result = result
	output.Timeline = timeline
	return output
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func assistantText(messages []protocol.Message) string {
	var builder strings.Builder
	for _, msg := range messages {
		if msg.Role != protocol.RoleAssistant {
			continue
		}
		text := strings.TrimSpace(protocol.MessageText(msg))
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(text)
	}
	return builder.String()
}

func extractTools(items []events.Event) []evaldomain.ToolCall {
	out := make([]evaldomain.ToolCall, 0)
	for _, event := range items {
		if event.Type != events.EventToolCallStarted && event.Type != events.EventToolCallFinished {
			continue
		}
		payload, ok := event.Payload.(events.ToolCallPayload)
		if !ok {
			data, _ := json.Marshal(event.Payload)
			_ = json.Unmarshal(data, &payload)
		}
		if strings.TrimSpace(payload.Name) == "" {
			continue
		}
		status := "started"
		if event.Type == events.EventToolCallFinished {
			status = "finished"
			if strings.TrimSpace(payload.Error) != "" {
				status = "failed"
			}
		}
		out = append(out, evaldomain.ToolCall{Name: payload.Name, Status: status, Error: strings.TrimSpace(payload.Error)})
	}
	return out
}

type replayManifest struct {
	SessionID string `json:"session_id"`
}

type timelineStability struct {
	MaxRepeatedAssistantMessages     int
	MaxRepeatedToolCalls             int
	EmptyToolExchangeRecommendations int
	ToolExchangeQueries              []string
	Signals                          []string
}

func loadReplayFixtureTimeline(suiteDir, fixture string) ([]events.Event, string, error) {
	fixture = strings.TrimSpace(fixture)
	if fixture == "" {
		return nil, "", fmt.Errorf("missing replay fixture")
	}
	root := fixture
	if !filepath.IsAbs(root) {
		root = filepath.Join(suiteDir, root)
	}
	info, err := os.Stat(root)
	if err != nil {
		timeline, sessionID, embeddedErr := loadEmbeddedReplayFixture(root)
		if embeddedErr == nil {
			return timeline, sessionID, nil
		}
		return nil, "", err
	}
	timelinePath := root
	if info.IsDir() {
		timelinePath = filepath.Join(root, "timeline.json")
	}
	data, err := os.ReadFile(timelinePath)
	if err != nil {
		return nil, "", err
	}
	var timeline []events.Event
	if err := json.Unmarshal(data, &timeline); err != nil {
		return nil, "", err
	}
	sessionID := "replay:" + safeName(filepath.Base(root))
	if info.IsDir() {
		var manifest replayManifest
		if manifestData, err := os.ReadFile(filepath.Join(root, "manifest.json")); err == nil {
			if err := json.Unmarshal(manifestData, &manifest); err == nil && strings.TrimSpace(manifest.SessionID) != "" {
				sessionID = strings.TrimSpace(manifest.SessionID)
			}
		}
	}
	return timeline, sessionID, nil
}

func readSuiteFile(filePath string) ([]byte, error) {
	data, err := os.ReadFile(filePath)
	if err == nil {
		return data, nil
	}
	if embeddedData, ok := readEmbeddedFile(filePath); ok {
		return embeddedData, nil
	}
	return nil, err
}

func readEmbeddedFile(filePath string) ([]byte, bool) {
	for _, candidate := range embeddedPathCandidates(filePath) {
		data, err := embeddedTestdata.ReadFile(candidate)
		if err == nil {
			return data, true
		}
	}
	return nil, false
}

func loadEmbeddedReplayFixture(root string) ([]events.Event, string, error) {
	for _, candidate := range embeddedPathCandidates(root) {
		info, err := fs.Stat(embeddedTestdata, candidate)
		if err != nil {
			continue
		}
		timelinePath := candidate
		if info.IsDir() {
			timelinePath = path.Join(candidate, "timeline.json")
		}
		data, err := embeddedTestdata.ReadFile(timelinePath)
		if err != nil {
			return nil, "", err
		}
		var timeline []events.Event
		if err := json.Unmarshal(data, &timeline); err != nil {
			return nil, "", err
		}
		sessionID := "replay:" + safeName(path.Base(candidate))
		if info.IsDir() {
			var manifest replayManifest
			if manifestData, err := embeddedTestdata.ReadFile(path.Join(candidate, "manifest.json")); err == nil {
				if err := json.Unmarshal(manifestData, &manifest); err == nil && strings.TrimSpace(manifest.SessionID) != "" {
					sessionID = strings.TrimSpace(manifest.SessionID)
				}
			}
		}
		return timeline, sessionID, nil
	}
	return nil, "", fmt.Errorf("embedded replay fixture not found: %s", root)
}

func embeddedPathCandidates(filePath string) []string {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(filePath)))
	clean = strings.TrimPrefix(clean, "./")
	out := make([]string, 0, 3)
	add := func(value string) {
		value = strings.TrimPrefix(strings.TrimSpace(value), "./")
		if value == "" || value == "." {
			return
		}
		for _, existing := range out {
			if existing == value {
				return
			}
		}
		out = append(out, value)
	}
	const packagePrefix = "internal/services/evalharness/"
	if idx := strings.Index(clean, packagePrefix); idx >= 0 {
		add(clean[idx+len(packagePrefix):])
	}
	if idx := strings.Index(clean, "testdata/"); idx >= 0 {
		add(clean[idx:])
	}
	add(clean)
	return out
}

func assistantTextFromTimeline(items []events.Event) string {
	var builder strings.Builder
	for _, event := range items {
		if event.Type != events.EventAssistantMessageComplete {
			continue
		}
		payload := eventPayloadMap(event)
		text := strings.TrimSpace(asString(payload["text"]))
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(text)
	}
	return builder.String()
}

func firstTimelineTurnID(items []events.Event) string {
	for _, event := range items {
		if strings.TrimSpace(event.TurnID) != "" {
			return strings.TrimSpace(event.TurnID)
		}
	}
	return ""
}

func analyzeTimelineStability(items []events.Event) timelineStability {
	var stability timelineStability
	assistantCounts := map[string]int{}
	toolCounts := map[string]int{}
	toolStarted := false
	for _, event := range items {
		payload := eventPayloadMap(event)
		switch event.Type {
		case events.EventAssistantMessageComplete:
			text := normalizeReplayText(asString(payload["text"]))
			if text == "" {
				continue
			}
			assistantCounts[text]++
			if assistantCounts[text] > stability.MaxRepeatedAssistantMessages {
				stability.MaxRepeatedAssistantMessages = assistantCounts[text]
			}
		case events.EventToolCallStarted:
			toolStarted = true
			name := strings.TrimSpace(asString(payload["name"]))
			if name == "" {
				continue
			}
			key := replayToolCallKey(name, payload["input"])
			toolCounts[key]++
			if toolCounts[key] > stability.MaxRepeatedToolCalls {
				stability.MaxRepeatedToolCalls = toolCounts[key]
			}
			if strings.EqualFold(name, "tool_exchange") {
				query := toolExchangeQuery(payload["input"])
				if query != "" {
					stability.ToolExchangeQueries = append(stability.ToolExchangeQueries, query)
				}
			}
		case events.EventToolCallFinished:
			name := strings.TrimSpace(asString(payload["name"]))
			if strings.EqualFold(name, "tool_exchange") {
				query := toolExchangeQuery(payload["input"])
				if query != "" && !containsString(stability.ToolExchangeQueries, query) {
					stability.ToolExchangeQueries = append(stability.ToolExchangeQueries, query)
				}
				if toolExchangeReturnedNoRecommendations(asString(payload["output"])) {
					stability.EmptyToolExchangeRecommendations++
				}
			}
			if !toolStarted {
				key := replayToolCallKey(name, payload["input"])
				toolCounts[key]++
				if toolCounts[key] > stability.MaxRepeatedToolCalls {
					stability.MaxRepeatedToolCalls = toolCounts[key]
				}
			}
		}
	}
	if stability.MaxRepeatedAssistantMessages > 1 {
		stability.Signals = append(stability.Signals, "repeated_assistant_message")
	}
	if stability.MaxRepeatedToolCalls > 1 {
		stability.Signals = append(stability.Signals, "repeated_tool_call")
	}
	if stability.EmptyToolExchangeRecommendations > 0 {
		stability.Signals = append(stability.Signals, "empty_tool_exchange_recommendation")
	}
	return stability
}

func evaluateExpectations(expected evaldomain.Expectation, text string, tools []evaldomain.ToolCall) []string {
	failures := make([]string, 0)
	lowerText := strings.ToLower(text)
	for _, want := range expected.RequiredSubstrings {
		if !strings.Contains(lowerText, strings.ToLower(strings.TrimSpace(want))) {
			failures = append(failures, "missing required substring: "+want)
		}
	}
	for _, forbidden := range expected.ForbiddenSubstrings {
		if strings.TrimSpace(forbidden) != "" && strings.Contains(lowerText, strings.ToLower(strings.TrimSpace(forbidden))) {
			failures = append(failures, "found forbidden substring: "+forbidden)
		}
	}
	toolCounts := map[string]int{}
	toolFailures := 0
	for _, tool := range tools {
		toolCounts[tool.Name]++
		if tool.Status == "failed" || strings.TrimSpace(tool.Error) != "" {
			toolFailures++
		}
	}
	for _, want := range expected.RequiredTools {
		if toolCounts[strings.TrimSpace(want)] == 0 {
			failures = append(failures, "missing required tool: "+want)
		}
	}
	for _, forbidden := range expected.ForbiddenTools {
		if toolCounts[strings.TrimSpace(forbidden)] > 0 {
			failures = append(failures, "used forbidden tool: "+forbidden)
		}
	}
	if expected.MaxToolFailures != nil && toolFailures > *expected.MaxToolFailures {
		failures = append(failures, fmt.Sprintf("tool failures %d exceed max %d", toolFailures, *expected.MaxToolFailures))
	}
	return failures
}

func evaluateStabilityExpectations(expected evaldomain.Expectation, stability timelineStability) []string {
	failures := make([]string, 0)
	if expected.MaxRepeatedAssistantMessages != nil && stability.MaxRepeatedAssistantMessages > *expected.MaxRepeatedAssistantMessages {
		failures = append(failures, fmt.Sprintf("repeated assistant messages %d exceed max %d", stability.MaxRepeatedAssistantMessages, *expected.MaxRepeatedAssistantMessages))
	}
	if expected.MaxRepeatedToolCalls != nil && stability.MaxRepeatedToolCalls > *expected.MaxRepeatedToolCalls {
		failures = append(failures, fmt.Sprintf("repeated tool calls %d exceed max %d", stability.MaxRepeatedToolCalls, *expected.MaxRepeatedToolCalls))
	}
	if expected.MaxEmptyToolExchangeRecommendations != nil && stability.EmptyToolExchangeRecommendations > *expected.MaxEmptyToolExchangeRecommendations {
		failures = append(failures, fmt.Sprintf("empty tool_exchange recommendations %d exceed max %d", stability.EmptyToolExchangeRecommendations, *expected.MaxEmptyToolExchangeRecommendations))
	}
	for _, forbidden := range expected.ForbiddenToolExchangeQueries {
		forbiddenHit := false
		for _, query := range stability.ToolExchangeQueries {
			if replayQueryMatches(query, forbidden) {
				failures = append(failures, "forbidden tool_exchange query: "+query)
				forbiddenHit = true
			}
		}
		if forbiddenHit {
			stability.Signals = appendUnique(stability.Signals, "forbidden_tool_exchange_query")
		}
	}
	for _, signal := range expected.ExpectedInstabilitySignals {
		if !containsString(stability.Signals, strings.TrimSpace(signal)) {
			failures = append(failures, "missing expected instability signal: "+signal)
		}
	}
	return failures
}

func appendForbiddenToolExchangeSignals(expected evaldomain.Expectation, stability timelineStability) []string {
	signals := append([]string{}, stability.Signals...)
	for _, forbidden := range expected.ForbiddenToolExchangeQueries {
		for _, query := range stability.ToolExchangeQueries {
			if replayQueryMatches(query, forbidden) {
				signals = appendUnique(signals, "forbidden_tool_exchange_query")
			}
		}
	}
	return signals
}

func eventPayloadMap(event events.Event) map[string]interface{} {
	if payload, ok := event.Payload.(map[string]interface{}); ok {
		return payload
	}
	data, _ := json.Marshal(event.Payload)
	var payload map[string]interface{}
	_ = json.Unmarshal(data, &payload)
	if payload == nil {
		return map[string]interface{}{}
	}
	return payload
}

func asString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func normalizeReplayText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func replayToolCallKey(name string, input interface{}) string {
	data, _ := json.Marshal(input)
	return strings.TrimSpace(name) + ":" + string(data)
}

func toolExchangeQuery(input interface{}) string {
	data, _ := json.Marshal(input)
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return ""
	}
	return strings.TrimSpace(asString(decoded["query"]))
}

func toolExchangeReturnedNoRecommendations(output string) bool {
	output = strings.TrimSpace(output)
	if output == "" {
		return false
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		return false
	}
	items, ok := decoded["recommended_bundles"].([]interface{})
	return ok && len(items) == 0
}

func replayQueryMatches(query, forbidden string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	forbidden = strings.ToLower(strings.TrimSpace(forbidden))
	return forbidden != "" && (query == forbidden || strings.Contains(query, forbidden))
}

func containsString(items []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, item := range items {
		if strings.TrimSpace(item) == want {
			return true
		}
	}
	return false
}

func appendUnique(items []string, value string) []string {
	if containsString(items, value) {
		return items
	}
	return append(items, value)
}

func writeCaseArtifacts(runDir string, output caseOutput) error {
	result := output.Result
	caseDir := filepath.Join(runDir, safeName(result.ID))
	if err := os.MkdirAll(caseDir, 0755); err != nil {
		return err
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(caseDir, "result.json"), result, 0644); err != nil {
		return err
	}
	if output.Snapshot != nil {
		if err := fsutil.WriteJSONAtomic(filepath.Join(caseDir, "snapshot.json"), output.Snapshot, 0644); err != nil {
			return err
		}
	}
	if output.Timeline != nil {
		if err := fsutil.WriteJSONAtomic(filepath.Join(caseDir, "timeline.json"), output.Timeline, 0644); err != nil {
			return err
		}
	}
	return nil
}

func safeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-", ".", "-")
	value = replacer.Replace(value)
	var builder strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			builder.WriteRune(r)
		}
	}
	if builder.Len() == 0 {
		return "eval"
	}
	return builder.String()
}
