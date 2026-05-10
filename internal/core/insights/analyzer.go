package insights

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const candidatesFileName = "candidates.json"

type Input struct {
	CurrentMessages []Message
	ActiveSkills    []string
	ToolCatalog     ToolCatalog
	Todos           []WorkItem
	Tasks           []WorkItem
}

type Message struct {
	Text      string
	ToolNames []string
}

type ToolCatalog struct {
	ActiveBundles []string
}

type WorkItem struct {
	Status string
}

type transcriptMessage struct {
	Content []transcriptBlock `json:"content"`
}

type transcriptBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"`
}

type candidate struct {
	Fingerprint string `json:"fingerprint"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Content     string `json:"content"`
	Type        string `json:"memory_type"`
	Source      string `json:"source"`
}

type Analyzer struct {
	TranscriptsDir string
	TempDir        string
	MemoryDir      string
}

func NewAnalyzer(transcriptsDir, tempDir, memoryDir string) *Analyzer {
	return &Analyzer{
		TranscriptsDir: transcriptsDir,
		TempDir:        tempDir,
		MemoryDir:      memoryDir,
	}
}

func (a *Analyzer) Analyze(input Input) (*Report, error) {
	transcriptMessages, err := a.loadTranscriptMessages()
	if err != nil {
		return nil, err
	}
	candidates, err := a.loadCandidates()
	if err != nil {
		return nil, err
	}

	allMessages := append(cloneMessages(transcriptMessages), cloneMessages(input.CurrentMessages)...)
	textCorpus, toolNames := collectConversationSignals(allMessages)

	report := &Report{
		AgentMDAdditions:      agentSuggestionsFromCandidates(candidates),
		SkillCandidates:       skillCandidatesFromSignals(textCorpus, candidates, input.ActiveSkills),
		BundleRecommendations: bundleRecommendationsFromSignals(textCorpus, toolNames, input.ToolCatalog),
		Frictions:             frictionFindings(textCorpus, input.Todos, input.Tasks),
	}
	return report, nil
}

func (a *Analyzer) loadTranscriptMessages() ([]Message, error) {
	entries, err := os.ReadDir(a.TranscriptsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) > 10 {
		names = names[len(names)-10:]
	}

	messages := make([]Message, 0)
	for _, name := range names {
		path := filepath.Join(a.TranscriptsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var transcript []transcriptMessage
		if err := json.Unmarshal(data, &transcript); err != nil {
			return nil, err
		}
		messages = append(messages, transcriptToMessages(transcript)...)
	}
	return messages, nil
}

func (a *Analyzer) loadCandidates() ([]candidate, error) {
	paths := []string{}
	if strings.TrimSpace(a.MemoryDir) != "" {
		paths = append(paths, filepath.Join(a.MemoryDir, candidatesFileName))
	}
	if strings.TrimSpace(a.TempDir) != "" {
		paths = append(paths, filepath.Join(a.TempDir, "memory_candidates.json"))
	}
	for _, path := range paths {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, nil
		}
		var candidates []candidate
		if err := json.Unmarshal(data, &candidates); err != nil {
			return nil, err
		}
		return candidates, nil
	}
	return nil, nil
}

func collectConversationSignals(messages []Message) (string, []string) {
	texts := make([]string, 0, len(messages))
	toolNames := make([]string, 0)
	for _, msg := range messages {
		if text := strings.TrimSpace(msg.Text); text != "" {
			texts = append(texts, text)
		}
		toolNames = append(toolNames, msg.ToolNames...)
	}
	return strings.Join(texts, "\n"), toolNames
}

func agentSuggestionsFromCandidates(candidates []candidate) []string {
	items := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		switch candidate.Type {
		case "user":
			items = append(items, "Consider capturing this stable collaboration preference in `.godex/AGENT.local.md`: "+candidate.Summary)
		case "workflow":
			items = append(items, "Consider codifying this recurring workflow in `AGENT.md` or `.godex/rules/*.md`: "+candidate.Summary)
		case "project", "warning":
			items = append(items, "Consider promoting this durable project note into `AGENT.md`: "+candidate.Summary)
		}
	}
	return unique(items)
}

func skillCandidatesFromSignals(corpus string, candidates []candidate, activeSkills []string) []string {
	items := make([]string, 0, 3)
	if strings.Contains(corpus, "go test ./...") {
		items = append(items, "A `go-validation` skill could bundle the repeated Go test/build workflow and its fallback checks.")
	}
	reviewMentions := strings.Count(strings.ToLower(corpus), "review")
	if reviewMentions >= 2 {
		items = append(items, "A `review-assistant` skill could standardize code review prompts, evidence collection, and output format.")
	}
	for _, candidate := range candidates {
		if candidate.Type == "workflow" {
			items = append(items, "Promote workflow memory into a reusable skill: "+candidate.Title)
		}
	}
	for _, skillName := range activeSkills {
		if skillName == "" {
			continue
		}
		items = append(items, "Review whether the active skill `"+skillName+"` should grow structured frontmatter and sections for Skill v2.")
	}
	return unique(items)
}

func bundleRecommendationsFromSignals(corpus string, toolNames []string, catalog ToolCatalog) []string {
	active := make(map[string]bool, len(catalog.ActiveBundles))
	for _, name := range catalog.ActiveBundles {
		active[name] = true
	}
	usedTools := make(map[string]struct{}, len(toolNames))
	for _, name := range toolNames {
		usedTools[name] = struct{}{}
	}

	items := make([]string, 0, 4)
	if !active["background"] && (containsTool(usedTools, "background_run") || strings.Contains(corpus, `enable bundle "background"`)) {
		items = append(items, "Background bundle is used often enough to evaluate whether it should be easier to reach in this workflow.")
	}
	if !active["task_board"] && anyTool(usedTools, "task_create", "task_list", "task_update", "claim_task") {
		items = append(items, "Task board bundle shows recurring use; consider surfacing it earlier for planning-heavy sessions.")
	}
	if !active["team"] && anyTool(usedTools, "read_inbox", "send_message", "broadcast", "plan_approval") {
		items = append(items, "Team bundle usage suggests teammate workflows may deserve a more direct entry point.")
	}
	if !active["subagent"] && (containsTool(usedTools, "task") || strings.Contains(strings.ToLower(corpus), "subagent")) {
		items = append(items, "Subagent workflows appear repeatedly; consider keeping subagent access closer to the default path.")
	}
	return unique(items)
}

func frictionFindings(corpus string, todos []WorkItem, tasks []WorkItem) []string {
	lower := strings.ToLower(corpus)
	items := make([]string, 0, 5)
	switch {
	case strings.Contains(lower, "context deadline exceeded"):
		items = append(items, "Model/API timeouts are recurring and should be treated as a first-class runtime friction.")
	}
	if strings.Contains(lower, "command not allowed") {
		items = append(items, "Tool permission mismatches are showing up in conversation traces.")
	}
	if strings.Contains(lower, "no such file or directory") {
		items = append(items, "Path resolution and file existence checks remain a recurring source of errors.")
	}
	if strings.Contains(lower, "tool \"") && strings.Contains(lower, "is not active") {
		items = append(items, "Progressive tool loading is discoverable but still produces inactive-tool friction in practice.")
	}

	pendingTodos := 0
	for _, item := range todos {
		if isOpenStatus(item.Status) {
			pendingTodos++
		}
	}
	if pendingTodos >= 5 {
		items = append(items, "Large pending todo count suggests work is fragmenting faster than it is being closed out.")
	}

	pendingTasks := 0
	for _, item := range tasks {
		if isOpenStatus(item.Status) {
			pendingTasks++
		}
	}
	if pendingTasks >= 5 {
		items = append(items, "Persistent task board backlog is growing and may need tighter prioritization.")
	}
	return unique(items)
}

func transcriptToMessages(transcript []transcriptMessage) []Message {
	messages := make([]Message, 0, len(transcript))
	for _, msg := range transcript {
		item := Message{
			Text:      transcriptText(msg.Content),
			ToolNames: transcriptToolNames(msg.Content),
		}
		if item.Text == "" && len(item.ToolNames) == 0 {
			continue
		}
		messages = append(messages, item)
	}
	return messages
}

func transcriptText(blocks []transcriptBlock) string {
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			builder.WriteString(block.Text)
		}
	}
	return builder.String()
}

func transcriptToolNames(blocks []transcriptBlock) []string {
	names := make([]string, 0)
	for _, block := range blocks {
		if block.Type == "tool_use" && block.Name != "" {
			names = append(names, block.Name)
		}
	}
	return names
}

func cloneMessages(messages []Message) []Message {
	cloned := make([]Message, 0, len(messages))
	for _, msg := range messages {
		cloned = append(cloned, Message{
			Text:      msg.Text,
			ToolNames: append([]string{}, msg.ToolNames...),
		})
	}
	return cloned
}

func isOpenStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "pending", "in_progress":
		return true
	default:
		return false
	}
}

func containsTool(used map[string]struct{}, name string) bool {
	_, ok := used[name]
	return ok
}

func anyTool(used map[string]struct{}, names ...string) bool {
	for _, name := range names {
		if containsTool(used, name) {
			return true
		}
	}
	return false
}

func unique(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}
