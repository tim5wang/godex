package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/modelcontext"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/platform/stringutil"
	"github.com/tim5wang/godex/internal/tools"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/domain/message"
)

type fakeCaller struct {
	resp protocol.Response
	err  error
}

func (f fakeCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	_ = req
	if f.err != nil {
		return nil, f.err
	}
	resp := f.resp
	return &resp, nil
}

type failingSessionSummarizer struct{}

func (failingSessionSummarizer) SummarizeSession(context.Context, compress.SessionSummaryRequest) (compress.SessionSummaryResult, error) {
	return compress.SessionSummaryResult{}, errors.New("model summarizer should not be used")
}

type recordingSessionSummarizer struct {
	calls int
}

func (r *recordingSessionSummarizer) SummarizeSession(_ context.Context, req compress.SessionSummaryRequest) (compress.SessionSummaryResult, error) {
	r.calls++
	return compress.SessionSummaryResult{
		Messages: []protocol.Message{
			protocol.NewSummaryMessage("model compact summary", ""),
		},
	}, nil
}

func TestCompactConversationDefaultsToFastSummarizer(t *testing.T) {
	a := newTestAgent(t, 100000)
	a.AddMessage(strings.Repeat("slow model compact should be avoided ", 40))
	a.summarizer = failingSessionSummarizer{}

	output, err := a.CompactConversation()
	if err != nil {
		t.Fatalf("default compact should use fast summarizer: %v", err)
	}
	if !strings.Contains(output, "## Session Compaction Summary") {
		t.Fatalf("expected fast compact summary, got %q", output)
	}
}

func TestCompactConversationModelModeUsesConfiguredSummarizer(t *testing.T) {
	a := newTestAgent(t, 100000)
	a.AddMessage("use deep model compact")
	recorder := &recordingSessionSummarizer{}
	a.summarizer = recorder

	output, err := a.CompactConversationWithMode("model")
	if err != nil {
		t.Fatalf("model compact: %v", err)
	}
	if recorder.calls != 1 {
		t.Fatalf("expected model summarizer call, got %d", recorder.calls)
	}
	if output != "model compact summary" {
		t.Fatalf("expected model summary output, got %q", output)
	}
}

func TestCompactConversationModelModeUsesSessionLLMWhenDefaultIsRuleBased(t *testing.T) {
	a := newTestAgent(t, 100000)
	a.AddMessage("use session model compact")
	// Simulate a web UI session: startup wiring left a rule-based default
	// summarizer (cfg.APIKey empty), but the session has an active client/model.
	a.summarizer = compress.NewRuleBasedSessionSummarizer(a.compressor)
	a.client = fakeCaller{resp: protocol.Response{Content: []protocol.Block{
		protocol.TextBlock("model summary from session llm"),
	}}}

	output, err := a.CompactConversationWithMode("model")
	if err != nil {
		t.Fatalf("model compact with session llm: %v", err)
	}
	if !strings.Contains(output, "model summary from session llm") {
		t.Fatalf("expected session LLM summary output, got %q", output)
	}
}

func TestApplyModelProfileRebuildsSummarizerForSessionModel(t *testing.T) {
	a := newTestAgent(t, 100000)
	profile := config.ModelProfileConfig{
		ID:        "kimi",
		Model:     "kimi-k3",
		BaseURL:   "http://127.0.0.1:8765",
		APIKey:    "test-key",
		MaxTokens: 4096,
	}
	a.ApplyModelProfile(profile)

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.summarizer.(*compress.LLMSessionSummarizer); !ok {
		t.Fatalf("expected LLM summarizer after ApplyModelProfile, got %T", a.summarizer)
	}
}

func TestBuildContextIncludesStructuredRuntimeMessages(t *testing.T) {
	a := newTestAgent(t, 100000)
	a.RegisterTools()
	a.AddMessage("hello")
	a.appendMessage(protocol.NewMessage(protocol.RoleAssistant,
		protocol.TextBlock("working"),
		protocol.ToolUseBlock("tool-1", "read_file", map[string]interface{}{"path": "README.md"}),
	))
	a.appendMessage(protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-1", "done")))

	a.mu.Lock()
	a.prompts.Skills = append(a.prompts.Skills, "Follow the loaded skill.")
	a.mu.Unlock()

	if err := a.msgBus.Send(message.Message{
		Type:    message.MsgTypeMessage,
		From:    "teammate",
		To:      "lead",
		Content: "need review",
	}); err != nil {
		t.Fatalf("send inbox message: %v", err)
	}

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	if !strings.Contains(build.System, "Follow the loaded skill.") {
		t.Fatalf("expected skill prompt in system prompt, got %q", build.System)
	}
	if len(build.Messages) < 5 {
		t.Fatalf("expected persistent history plus runtime prompt state, got %d messages", len(build.Messages))
	}
	var inboxMsg *protocol.Message
	for i := range build.Messages {
		if build.Messages[i].Metadata != nil && build.Messages[i].Metadata.Kind == protocol.KindInbox {
			inboxMsg = &build.Messages[i]
			break
		}
	}
	if inboxMsg == nil {
		t.Fatalf("expected inbox runtime message in messages list, got %v", len(build.Messages))
	}
	apiMessages := protocol.ToAPIMessages(build.Messages)
	var toolResultAPI *protocol.APIMessage
	for i := range apiMessages {
		if len(apiMessages[i].Content) > 0 && apiMessages[i].Content[0].ToolUseID == "tool-1" {
			toolResultAPI = &apiMessages[i]
			break
		}
	}
	if toolResultAPI == nil {
		t.Fatalf("expected tool result block for tool-1, got none")
	}
	if got := protocol.MessageText(*inboxMsg); !strings.Contains(got, "Inbox updates") {
		t.Fatalf("expected inbox summary text, got %q", got)
	}
	if got := a.msgBus.PeekInbox("lead"); len(got) != 1 {
		t.Fatalf("expected inbox preview to remain before ack, got %d messages", len(got))
	}
	build.AckRuntime()
	if got := a.msgBus.PeekInbox("lead"); len(got) != 0 {
		t.Fatalf("expected inbox to be acked after explicit ack, got %d messages", len(got))
	}
}

func TestImplicitBundlesDoesNotTreatDescribingAsBing(t *testing.T) {
	bundles := implicitBundlesForQuery("Create a markdown document describing GoDex Local Task Center TUI MVP value.")
	if len(bundles) != 0 {
		t.Fatalf("expected no implicit bundles for describing, got %+v", bundles)
	}

	bundles = implicitBundlesForQuery("Search Bing for current GoDex release notes.")
	if !containsString(bundles, bundleWeb) {
		t.Fatalf("expected explicit Bing search to require web bundle, got %+v", bundles)
	}
}

func TestBuildContextConservativeAutoCompactTrigger(t *testing.T) {
	t.Run("history over threshold compacts", func(t *testing.T) {
		a := newTestAgent(t, 80)
		a.AddMessage(strings.Repeat("history ", 80))

		build, err := a.buildContext(context.Background())
		if err != nil {
			t.Fatalf("build context: %v", err)
		}
		if !build.Compacted {
			t.Fatalf("expected history over threshold to compact, breakdown=%+v reasons=%v", build.TokenBreakdown, build.CompressionReasons)
		}
	})

	t.Run("total over threshold with tiny history does not compact", func(t *testing.T) {
		a := newTestAgent(t, 10)

		build, err := a.buildContext(context.Background())
		if err != nil {
			t.Fatalf("build context: %v", err)
		}
		if build.Compacted {
			t.Fatalf("did not expect tiny history to compact, breakdown=%+v reasons=%v", build.TokenBreakdown, build.CompressionReasons)
		}
		if build.TokenBreakdown.Total <= a.cfg.CompressThreshold {
			t.Fatalf("test setup expected total over threshold, breakdown=%+v", build.TokenBreakdown)
		}
	})

	t.Run("total over threshold with compactable history compacts", func(t *testing.T) {
		a := newTestAgent(t, 1000)
		a.RegisterTools()
		a.AddMessage(strings.Repeat("compactable ", 260))

		build, err := a.buildContext(context.Background())
		if err != nil {
			t.Fatalf("build context: %v", err)
		}
		if !build.Compacted {
			t.Fatalf("expected total pressure with compactable history to compact, breakdown=%+v reasons=%v", build.TokenBreakdown, build.CompressionReasons)
		}
	})
}

func TestBuildContextUsesCompactionPolicyTrigger(t *testing.T) {
	a := newTestAgent(t, 100000)
	a.cfg.Compaction.TriggerTokens = 80
	a.cfg.Compaction.TargetHistoryTokens = 20
	a.AddMessage(strings.Repeat("policy trigger ", 90))

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if !build.Compacted {
		t.Fatalf("expected compaction policy trigger to compact before legacy threshold, breakdown=%+v", build.TokenBreakdown)
	}
	if build.CompactionMode != "fast" {
		t.Fatalf("expected fast compaction mode, got %q", build.CompactionMode)
	}
	if build.PreCompactionTotal == 0 || build.PostCompactionTotal == 0 {
		t.Fatalf("expected compaction totals, got before=%d after=%d", build.PreCompactionTotal, build.PostCompactionTotal)
	}
	if len(build.LargestContextSources) == 0 {
		t.Fatalf("expected largest context source diagnostics")
	}
}

func TestBuildContextUsesMatchingBackgroundCompactionCandidate(t *testing.T) {
	a := newTestAgent(t, 80)
	a.AddMessage(strings.Repeat("background candidate ", 90))
	history, version := a.messageState()
	system, err := a.buildRuntimeSystemPrompt()
	if err != nil {
		t.Fatalf("system prompt: %v", err)
	}
	estimate := estimateContextBudget(system, history, nil, nil, nil, 0, nil, a.compactionTriggerTokens())
	result, err := a.runCompaction(context.Background(), "fast", compress.SessionSummaryRequest{
		System:         system,
		History:        history,
		TokenBreakdown: tokenBreakdownMap(estimate.Breakdown),
	})
	if err != nil {
		t.Fatalf("prepare candidate: %v", err)
	}
	a.storeCompactionCandidate(compactionCandidate{HistoryVersion: version, Result: result})

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if !build.Compacted {
		t.Fatalf("expected matching background candidate to be applied")
	}
	if build.CompactionMode != "fast" {
		t.Fatalf("expected fast candidate diagnostics, got %q", build.CompactionMode)
	}
	if a.takeCompactionCandidate(version) != nil {
		t.Fatalf("expected candidate to be consumed")
	}
}

func TestBuildContextDedupesRepeatedLargeToolResultSummaries(t *testing.T) {
	a := newTestAgent(t, 4096)
	summary := modelcontext.SummaryJSON(modelcontext.LargeToolResultSummary{
		ToolName:     "bash",
		ToolUseID:    "tool-large",
		Bytes:        100000,
		SHA256:       "abc123",
		ArtifactPath: ".godex/.tool-results/session/tool-large.json",
		Preview:      strings.Repeat("preview ", 200),
	})
	a.appendMessage(protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-large-1", summary)))
	a.appendMessage(protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("tool-large-2", summary)))

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(build.Messages) < 4 {
		t.Fatalf("expected four messages, got %d", len(build.Messages))
	}
	first := build.Messages[2].Content[0].Content
	second := build.Messages[3].Content[0].Content
	if !strings.Contains(first, "tool_result_truncated") {
		t.Fatalf("expected first summary to remain full reference, got %q", first)
	}
	if !strings.Contains(second, "tool_result_duplicate") {
		t.Fatalf("expected repeated summary to become duplicate reference, got %q", second)
	}
	if strings.Contains(second, strings.Repeat("preview ", 20)) {
		t.Fatalf("expected duplicate reference to omit repeated preview, got %q", second)
	}
}

func TestActiveSkillsPromptAppliesContextBudget(t *testing.T) {
	longCore := strings.Repeat("This skill section has many details about workflows and examples. ", 2000)
	prompt := buildActiveSkillsPrompt([]activeSkillState{{
		catalog: skill.CatalogEntry{
			ID:          "large-skill",
			Name:        "Large Skill",
			Description: "A deliberately large skill.",
		},
		core: longCore,
		expanded: map[string]string{
			"details": strings.Repeat("Extra section detail. ", 1000),
		},
		expandedOrder: []string{"details"},
	}})

	if !strings.Contains(prompt, "# Active Skills") || !strings.Contains(prompt, "Context Budget Notes") {
		t.Fatalf("expected active skill prompt with budget notes, got %q", prompt)
	}
	if strings.Contains(prompt, strings.Repeat("This skill section has many details", 200)) {
		t.Fatalf("expected large skill section to be truncated")
	}
	if !strings.Contains(prompt, "[skill section truncated]") {
		t.Fatalf("expected truncation marker, got %q", prompt)
	}
}

func TestInspectContextReportsTokenBreakdownAndToolResultReferences(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	if _, err := a.memoryMgr.Remember(memory.SaveInput{
		Title:   "Runtime context",
		Summary: "Runtime context includes memory previews.",
		Content: "Use memory previews when debugging context compression.",
		Type:    memory.TypeProject,
		Source:  "test",
	}); err != nil {
		t.Fatalf("remember memory: %v", err)
	}
	if err := a.msgBus.Send(message.Message{
		Type:    message.MsgTypeMessage,
		From:    "teammate",
		To:      "lead",
		Content: "runtime note",
	}); err != nil {
		t.Fatalf("send inbox message: %v", err)
	}

	a.appendMessage(protocol.Message{
		Role: protocol.RoleUser,
		Content: []protocol.Block{
			protocol.TextBlock("please debug context compression with memory"),
		},
		Metadata: &protocol.Metadata{
			Attachments: []protocol.Attachment{{
				ID:        "att-1",
				Name:      "report.txt",
				MIMEType:  "text/plain",
				Path:      "/tmp/report.txt",
				SizeBytes: 1024,
			}},
		},
	})
	a.appendMessage(protocol.NewMessage(protocol.RoleAssistant,
		protocol.ToolUseBlock("tool-large", "read_file", map[string]interface{}{"path": "large.log"}),
	))
	a.appendMessage(protocol.NewMessage(protocol.RoleUser,
		protocol.ToolResultBlock("tool-large", modelcontext.SummaryJSON(modelcontext.LargeToolResultSummary{
			ToolName:     "read_file",
			ToolUseID:    "tool-large",
			Bytes:        65536,
			SHA256:       strings.Repeat("a", 64),
			ArtifactPath: ".godex/.tool-results/session/tool-large.json",
			Preview:      "head\n...\ntail",
		})),
	))

	inspection, err := a.InspectContext(context.Background(), "session-context")
	if err != nil {
		t.Fatalf("inspect context: %v", err)
	}
	if inspection.TokenEstimate != inspection.TokenBreakdown.Total || inspection.TotalTokenEstimate != inspection.TokenBreakdown.Total {
		t.Fatalf("expected total estimate fields to match breakdown: %+v", inspection)
	}
	if inspection.TokenBreakdown.System == 0 || inspection.TokenBreakdown.History == 0 || inspection.TokenBreakdown.Memory == 0 || inspection.TokenBreakdown.Runtime == 0 || inspection.TokenBreakdown.ToolSchemas == 0 {
		t.Fatalf("expected non-zero primary breakdown fields, got %+v", inspection.TokenBreakdown)
	}
	if inspection.TokenBreakdown.ToolResults == 0 || inspection.TokenBreakdown.Attachments == 0 {
		t.Fatalf("expected tool result and attachment pressure, got %+v", inspection.TokenBreakdown)
	}
	if inspection.LargeToolResultReferenceCount != 1 || len(inspection.ToolResultReferences) != 1 {
		t.Fatalf("expected one large tool result reference, got %+v", inspection)
	}
	if got := inspection.ToolResultReferences[0].ArtifactPath; !strings.Contains(got, "tool-large.json") {
		t.Fatalf("expected artifact reference, got %+v", inspection.ToolResultReferences[0])
	}
	if inspection.PrefixCache.SystemHash == "" || inspection.PrefixCache.ToolSchemasHash == "" || inspection.PrefixCache.StablePrefixHash == "" {
		t.Fatalf("expected prefix cache hashes, got %+v", inspection.PrefixCache)
	}
	if inspection.PrefixCache.StableSystemTokens == 0 {
		t.Fatalf("expected stable system token estimate, got %+v", inspection.PrefixCache)
	}
	if inspection.PrefixCache.StableToolSchemaTokens == 0 {
		t.Fatalf("expected stable tool schema token estimate, got %+v", inspection.PrefixCache)
	}
	if inspection.PrefixCache.StableMemoryIndexTokens == 0 {
		t.Fatalf("expected stable memory index token estimate, got %+v", inspection.PrefixCache)
	}
	if inspection.PrefixCache.DynamicRuntimeTokens == 0 {
		t.Fatalf("expected dynamic runtime token estimate, got %+v", inspection.PrefixCache)
	}
	for _, want := range []string{"environment", "tool_availability"} {
		if inspection.PrefixCache.DynamicSectionTokens[want] == 0 {
			t.Fatalf("expected quasi-stable section token estimate for %q, got %+v", want, inspection.PrefixCache.DynamicSectionTokens)
		}
	}
}

func TestBuildContextDoesNotExposeIdleToolForPrimaryAgent(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	for _, schema := range build.ToolSchemas {
		if schema.Name == "idle" {
			t.Fatalf("did not expect primary agent to expose idle tool, got %+v", build.ToolSchemas)
		}
	}
}

func TestBuildContextIncludesEnvironmentPrompt(t *testing.T) {
	a := newTestAgent(t, 4096)

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	for _, notWant := range []string{
		"# Environment",
		"Local date: 2026-04-17",
		"Timezone: Asia/Shanghai",
	} {
		if strings.Contains(build.System, notWant) {
			t.Fatalf("did not expect dynamic environment prompt %q in system prompt, got %q", notWant, build.System)
		}
	}
	foundEnvironment := false
	foundDate := false
	for _, msg := range build.Messages {
		if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindBackground {
			continue
		}
		text := protocol.MessageText(msg)
		if strings.Contains(text, "Local date: 2026-04-17") {
			// The volatile date/weekday line lives in its own tail message,
			// never inside the stable # Environment section.
			foundDate = true
			if strings.Contains(text, "# Environment") {
				t.Fatalf("expected date to stay out of the stable environment section, got %q", text)
			}
		}
		if !strings.Contains(text, "# Runtime Prompt State") || !strings.Contains(text, "# Environment") {
			continue
		}
		foundEnvironment = true
		for _, want := range []string{
			"# Environment",
			"This is optional runtime context.",
			"Skills directory: " + a.cfg.SkillsDir,
			"Temporary files directory: " + a.cfg.TempDir,
			"Timezone: Asia/Shanghai",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("expected environment runtime message to contain %q, got %q", want, text)
			}
		}
		if strings.Contains(text, "Local date") || strings.Contains(text, "Weekday") {
			t.Fatalf("expected date/weekday OUT of the stable environment section, got %q", text)
		}
	}
	if !foundEnvironment {
		t.Fatalf("expected environment prompt in runtime messages, got %+v", build.Messages)
	}
	if !foundDate {
		t.Fatalf("expected volatile date message in the tail, got %+v", build.Messages)
	}
}

func TestBuildContextOrdersQuasiStableBeforeHistoryAndVolatileAfter(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	if _, err := a.memoryMgr.Remember(memory.SaveInput{
		Title:   "Runtime context",
		Summary: "Memory previews for ordering test.",
		Content: "remembered for cache ordering test",
		Type:    memory.TypeProject,
		Source:  "test",
	}); err != nil {
		t.Fatalf("remember memory: %v", err)
	}
	if err := a.msgBus.Send(message.Message{
		Type:    message.MsgTypeMessage,
		From:    "teammate",
		To:      "lead",
		Content: "volatile inbox note",
	}); err != nil {
		t.Fatalf("send inbox message: %v", err)
	}
	a.AddMessage("first persistent user message")

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	historyIdx := -1
	promptStateIdx := -1
	memoryIndexIdx := -1
	inboxIdx := -1
	for i := range build.Messages {
		msg := build.Messages[i]
		text := protocol.MessageText(msg)
		switch {
		case (msg.Metadata == nil || !msg.Metadata.Ephemeral) && strings.Contains(text, "first persistent user message"):
			historyIdx = i
		case msg.Metadata != nil && msg.Metadata.Kind == protocol.KindBackground && strings.Contains(text, "# Runtime Prompt State"):
			promptStateIdx = i
		case msg.Metadata != nil && msg.Metadata.Kind == protocol.KindMemory && strings.Contains(text, "# Memory"):
			memoryIndexIdx = i
		case msg.Metadata != nil && msg.Metadata.Kind == protocol.KindInbox:
			inboxIdx = i
		}
	}
	if historyIdx < 0 {
		t.Fatalf("expected persistent history message, got %+v", build.Messages)
	}
	if promptStateIdx < 0 {
		t.Fatalf("expected quasi-stable runtime prompt state message, got %+v", build.Messages)
	}
	if memoryIndexIdx < 0 {
		t.Fatalf("expected quasi-stable memory index message, got %+v", build.Messages)
	}
	if inboxIdx < 0 {
		t.Fatalf("expected volatile inbox message, got %+v", build.Messages)
	}
	if promptStateIdx > historyIdx {
		t.Fatalf("expected quasi-stable prompt state (%d) before history (%d)", promptStateIdx, historyIdx)
	}
	if memoryIndexIdx > historyIdx {
		t.Fatalf("expected quasi-stable memory index (%d) before history (%d)", memoryIndexIdx, historyIdx)
	}
	if inboxIdx < historyIdx {
		t.Fatalf("expected volatile inbox message (%d) after history (%d)", inboxIdx, historyIdx)
	}
}

func TestBuildContextKeepsDynamicPromptStateOutOfStableSystem(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	if _, err := a.memoryMgr.Remember(memory.SaveInput{
		Title:   "Runtime context",
		Summary: "Memory index should be dynamic prompt state.",
		Content: "Keep memory index out of the stable system prompt.",
		Type:    memory.TypeProject,
		Source:  "test",
	}); err != nil {
		t.Fatalf("remember memory: %v", err)
	}

	a.AddMessage("please inspect the runtime context")
	first, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	a.now = func() time.Time {
		return time.Date(2026, time.April, 18, 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	}
	second, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context after date change: %v", err)
	}
	if first.System != second.System {
		t.Fatalf("expected stable system prompt across date changes\nfirst: %q\nsecond: %q", first.System, second.System)
	}

	for _, notWant := range []string{
		"# Memory",
		"Memory directory: " + a.cfg.MemoryDir,
		"# Environment",
		"# Tool Availability",
		"Active bundles:",
	} {
		if strings.Contains(first.System, notWant) {
			t.Fatalf("did not expect dynamic prompt state %q in system prompt, got %q", notWant, first.System)
		}
	}

	firstRuntime := runtimePromptStateText(first.Messages)
	secondRuntime := runtimePromptStateText(second.Messages)
	if firstRuntime != secondRuntime {
		t.Fatalf("expected runtime prompt state stable across date change (date moved to tail)\nfirst: %q\nsecond: %q", firstRuntime, secondRuntime)
	}
	firstDate := volatileBackgroundText(first.Messages, "Local date: 2026-04-17")
	secondDate := volatileBackgroundText(second.Messages, "Local date: 2026-04-18")
	if !firstDate || !secondDate {
		t.Fatalf("expected volatile date message to track the date, first=%v second=%v", firstDate, secondDate)
	}

	for _, msg := range a.GetMessages() {
		if msg.Metadata != nil && msg.Metadata.Ephemeral {
			t.Fatalf("did not expect dynamic prompt state to persist in history, got %+v", a.GetMessages())
		}
	}
}

func TestBuildContextIncludesCapabilityCheckPrompt(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	for _, want := range []string{
		"You are a helpful AI agent working inside this workspace.",
		"# Capability Check",
		"Check what is already configured in this workspace before calling a capability unavailable.",
		"Start with relevant skills and active tools, then use tool_exchange if another bundle would help.",
		"Keep the active tool workspace small: when calling tool_exchange, disable bundles that are clearly irrelevant to the current conversation",
		"When a tool generates a local file such as a screenshot or export, treat it as a generated artifact.",
		"When the user wants a local file sent or attached without reading its contents, prefer attach_file instead of read_file.",
		"Do not use read_file for binary or large artifacts such as PDFs, images, media, or archives",
		"Skip canned self-introductions and stay focused on the request.",
	} {
		if !strings.Contains(build.System, want) {
			t.Fatalf("expected system prompt to contain %q, got %q", want, build.System)
		}
	}
}

func TestBuildContextExposesOnlyActiveToolSchemas(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	names := make(map[string]struct{}, len(build.ToolSchemas))
	for _, schema := range build.ToolSchemas {
		names[schema.Name] = struct{}{}
	}

	for _, expected := range []string{
		"bash",
		"glob",
		"read_file",
		"write_file",
		"edit_file",
		"attach_file",
		"grep",
		"find",
		"ls",
		"todo_write",
		"todo_list",
		"skill",
		"tool_exchange",
	} {
		if _, ok := names[expected]; !ok {
			t.Fatalf("expected active tool schema %q, got %+v", expected, build.ToolSchemas)
		}
	}

	for _, unexpected := range []string{
		"task",
		"subagent",
		"read_inbox",
		"send_message",
	} {
		if _, ok := names[unexpected]; ok {
			t.Fatalf("did not expect inactive tool schema %q, got %+v", unexpected, build.ToolSchemas)
		}
	}

	// Pin the exact default active-tool set so future additions surface as test
	// updates rather than silently passing through the >=10 lower bound.
	gotNames := make([]string, 0, len(names))
	for n := range names {
		gotNames = append(gotNames, n)
	}
	slices.Sort(gotNames)
	wantNames := []string{
		"attach_file", "bash", "compress", "edit_file", "find", "glob", "grep",
		"history_search", "ls", "lsp", "memory", "read_file", "skill",
		"todo_list", "todo_write", "tool_exchange", "web_fetch", "web_search", "write_file",
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("active tool schema set changed.\nwant: %v\ngot:  %v", wantNames, gotNames)
	}
}

func TestBuildContextCodingProfileUsesLeanToolSurface(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	ctx := tools.WithSessionContext(context.Background(), automation.SessionContext{AgentProfile: config.AgentProfileCoding})

	build, err := a.buildContext(ctx)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	names := make(map[string]struct{}, len(build.ToolSchemas))
	for _, schema := range build.ToolSchemas {
		names[schema.Name] = struct{}{}
	}
	for _, want := range []string{
		"bash",
		"glob",
		"read_file",
		"write_file",
		"edit_file",
		"attach_file",
		"todo_write",
		"todo_list",
		"tool_exchange",
		"memory",
		"skill",
	} {
		if _, ok := names[want]; !ok {
			t.Fatalf("expected coding profile tool %q, got %+v", want, build.ToolSchemas)
		}
	}
	for _, blocked := range []string{} {
		if _, ok := names[blocked]; ok {
			t.Fatalf("did not expect coding profile to expose %q by default, got %+v", blocked, build.ToolSchemas)
		}
	}
	if strings.Contains(build.System, "# Skill Availability") {
		t.Fatalf("did not expect coding profile to inject skill catalog prompt, got %q", build.System)
	}
	if !strings.Contains(build.System, "Effective profile: coding") {
		t.Fatalf("expected coding profile prompt, got %q", build.System)
	}
	for _, want := range []string{
		"Keep user-visible replies compact like a coding agent",
		"Do not narrate routine steps",
		"Response style: concise by default",
	} {
		if !strings.Contains(build.System, want) {
			t.Fatalf("expected coding profile prompt to contain %q, got %q", want, build.System)
		}
	}
	runtimeState := runtimePromptStateText(build.Messages)
	for _, want := range []string{"Active tools:", "bash", "read_file", "write_file", "grep"} {
		if !strings.Contains(runtimeState, want) {
			t.Fatalf("expected runtime tool availability to contain %q, got %q", want, runtimeState)
		}
	}
}

func TestBuildContextCodingProfileIncludesRepoMap(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	if err := os.MkdirAll(filepath.Join(a.cfg.WorkspaceDir, "internal", "demo"), 0755); err != nil {
		t.Fatalf("mkdir demo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(a.cfg.WorkspaceDir, "internal", "demo", "worker.go"), []byte(`package demo

type Worker struct{}

func RunTask() {}
`), 0644); err != nil {
		t.Fatalf("write demo file: %v", err)
	}
	ctx := tools.WithSessionContext(context.Background(), automation.SessionContext{AgentProfile: config.AgentProfileCoding})

	build, err := a.buildContext(ctx)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	runtimeState := runtimePromptStateText(build.Messages)
	for _, want := range []string{"# Repo Map", "internal/demo/worker.go", "type Worker", "func RunTask"} {
		if !strings.Contains(runtimeState, want) {
			t.Fatalf("expected repo map runtime state to contain %q, got %q", want, runtimeState)
		}
	}
}

func TestBuildContextCodingProfileCanExposeSkillsWhenRequested(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.AddMessage("请加载适合代码评审的 skill")
	ctx := tools.WithSessionContext(context.Background(), automation.SessionContext{AgentProfile: config.AgentProfileCoding})

	build, err := a.buildContext(ctx)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	for _, want := range []string{"skill"} {
		if _, ok := schemaByName(build.ToolSchemas, want); !ok {
			t.Fatalf("expected %s for explicit skill request in coding profile, got %+v", want, build.ToolSchemas)
		}
	}
}

func TestBuildContextPreloadsWebForWeatherQueries(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.AddMessage("今天深圳天气怎么样？")

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	if _, ok := schemaByName(build.ToolSchemas, "web_search"); !ok {
		t.Fatalf("expected web_search schema for weather query, got %+v", build.ToolSchemas)
	}
	if _, ok := schemaByName(build.ToolSchemas, "web_fetch"); !ok {
		t.Fatalf("expected web_fetch schema for weather query, got %+v", build.ToolSchemas)
	}
	if strings.Contains(build.System, "web (current information lookup and page fetching)") {
		t.Fatalf("did not expect active web bundle state in system prompt, got %q", build.System)
	}
	if got := runtimePromptStateText(build.Messages); !strings.Contains(got, "web (current information lookup and page fetching)") {
		t.Fatalf("expected runtime prompt state to show active web bundle, got %q", got)
	}
}

func TestBuildContextPreloadsWebForExplicitWebSearchQueries(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.AddMessage("网络搜索一下 GoDex agent runtime 的资料")

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	if _, ok := schemaByName(build.ToolSchemas, "web_search"); !ok {
		t.Fatalf("expected web_search schema for explicit web search query, got %+v", build.ToolSchemas)
	}
	if _, ok := schemaByName(build.ToolSchemas, "web_fetch"); !ok {
		t.Fatalf("expected web_fetch schema for explicit web search query, got %+v", build.ToolSchemas)
	}
}

func TestBuildContextPreloadsWebForAllQueries(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.AddMessage("帮我重构这个函数")

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	if _, ok := schemaByName(build.ToolSchemas, "web_search"); !ok {
		t.Fatalf("expected web_search schema for all queries, got %+v", build.ToolSchemas)
	}
	if _, ok := schemaByName(build.ToolSchemas, "web_fetch"); !ok {
		t.Fatalf("expected web_fetch schema for all queries, got %+v", build.ToolSchemas)
	}
}

func TestBuildContextExposesHistorySearchForExplicitRecall(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.AddMessage("刚才我们定过哪个方案？")

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if build.HistoryRecall == nil || !build.HistoryRecall.AllowTool || !build.HistoryRecall.ExplicitRequest {
		t.Fatalf("expected explicit history recall decision, got %+v", build.HistoryRecall)
	}
	if _, ok := schemaByName(build.ToolSchemas, "history_search"); !ok {
		t.Fatalf("expected history_search to be exposed, got %+v", build.ToolSchemas)
	}
}

func TestBuildContextHidesHistorySearchForOrdinaryQuery(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.AddMessage("请修一下这个函数")

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if _, ok := schemaByName(build.ToolSchemas, "history_search"); !ok {
		t.Fatalf("expected history_search to be always available, got %+v", build.ToolSchemas)
	}
}

func TestBuildContextAutoExposesHistorySearchAfterClearOrCompact(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.RestoreStateForSession("session-1", SessionState{
		Messages:       []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "还记得这个 PDF 放在哪吗？")},
		TranscriptRefs: []string{"transcript_20260424_120000.json"},
	})

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if build.HistoryRecall == nil || !build.HistoryRecall.AllowTool || !build.HistoryRecall.Automatic {
		t.Fatalf("expected weak automatic history recall, got %+v", build.HistoryRecall)
	}
	if build.HistoryRecall.RecommendedScope != tools.HistorySearchScopeSessionArchive {
		t.Fatalf("expected session_archive recommendation, got %+v", build.HistoryRecall)
	}
	if _, ok := schemaByName(build.ToolSchemas, "history_search"); !ok {
		t.Fatalf("expected history_search to be exposed, got %+v", build.ToolSchemas)
	}
}

func TestBuildContextDoesNotAutoExposeAllArchives(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cfg.Tools.History.Auto.DefaultScope = tools.HistorySearchScopeAllArchives
	a.RegisterTools()
	a.RestoreStateForSession("session-1", SessionState{
		Messages:       []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "还记得 rollback checklist 吗？")},
		TranscriptRefs: []string{"transcript_20260424_120000.json"},
	})

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if build.HistoryRecall == nil {
		t.Fatal("expected history recall decision")
	}
	if build.HistoryRecall.RecommendedScope == tools.HistorySearchScopeAllArchives {
		t.Fatalf("did not expect automatic all_archives recommendation, got %+v", build.HistoryRecall)
	}
}

func TestBuildContextLimitsAutomaticHistoryRecallToOncePerTurn(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.RestoreStateForSession("session-1", SessionState{
		Messages:       []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "还记得上线窗口吗？")},
		TranscriptRefs: []string{"transcript_20260424_120000.json"},
	})
	ctx := withHistoryRecallTurnState(context.Background())
	if state := historyRecallTurnStateFromContext(ctx); state != nil {
		state.setAutomaticExposure(true)
		state.consumeAutomaticExposure()
	}

	build, err := a.buildContext(ctx)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if _, ok := schemaByName(build.ToolSchemas, "history_search"); !ok {
		t.Fatalf("expected history_search to be always available, got %+v", build.ToolSchemas)
	}
}

func TestBuildContextBlocksAutomaticHistoryRecallForSessionSource(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.RestoreStateForSession("session-1", SessionState{
		Messages:       []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "还记得巡检规则吗？")},
		TranscriptRefs: []string{"transcript_20260424_120000.json"},
	})
	ctx := tools.WithSessionContext(context.Background(), automation.SessionContext{Source: "cron"})

	build, err := a.buildContext(ctx)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if _, ok := schemaByName(build.ToolSchemas, "history_search"); !ok {
		t.Fatalf("expected history_search to be always available, got %+v", build.ToolSchemas)
	}
}

func TestBuildContextIncludesProjectLedger(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	ctx := tools.WithSessionContext(context.Background(), automation.SessionContext{
		SessionID:              "session-ledger",
		ProjectLedger:          "Goal: ship the long task\nCurrent phase: validation",
		ProjectLedgerUpdatedAt: time.Now(),
	})

	build, err := a.buildContext(ctx)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if strings.Contains(build.System, "Long-task project ledger") || strings.Contains(build.System, "Goal: ship the long task") {
		t.Fatalf("did not expect volatile project ledger in system prompt, got %q", build.System)
	}
	foundLedger := false
	for _, msg := range build.Messages {
		if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindBackground {
			continue
		}
		text := protocol.MessageText(msg)
		if strings.Contains(text, "Long-task project ledger") && strings.Contains(text, "Goal: ship the long task") {
			foundLedger = true
			break
		}
	}
	if !foundLedger {
		t.Fatalf("expected project ledger as ephemeral runtime message, got %+v", build.Messages)
	}
}

func TestBuildContextIncludesToolAvailabilityPrompt(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	for _, want := range []string{
		"# Tool Availability",
		"Only active tools are callable right now.",
		"Use tool_exchange with a short query to discover or change bundle state when needed.",
		"Do not use bash/curl/python/node as a substitute for web_search or web_fetch when the web bundle is active.",
		"Keep the active tool workspace tidy: use disable_bundles for active bundles that this conversation no longer needs.",
		"- Active bundles: core_code (workspace shell commands and code file access), lsp (LSP code intelligence (definitions, references, hover, diagnostics, completions)), planning (lightweight todo planning and progress tracking), web (current information lookup and page fetching)",
		"- Available bundles: background (long-running command execution and status checks), desktop (local desktop screenshots, clipboard, keyboard, mouse, and window inspection), external_agents (external ACP agent delegation over stdio), mcp (configured MCP resource servers), packages (declaration-only package and prompt ecosystem), subagent (isolated delegated exploration or implementation work), task_board (persistent task board operations), team (teammate inbox, messaging, and approval workflows)",
	} {
		if strings.Contains(build.System, want) {
			t.Fatalf("did not expect tool availability prompt %q in system prompt, got %q", want, build.System)
		}
		runtimeState := runtimePromptStateText(build.Messages)
		if !strings.Contains(runtimeState, want) {
			t.Fatalf("expected runtime prompt state to contain %q, got %q", want, runtimeState)
		}
	}
}

func runtimePromptStateText(messages []protocol.Message) string {
	var parts []string
	for _, msg := range messages {
		if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindBackground {
			continue
		}
		text := protocol.MessageText(msg)
		if strings.Contains(text, "# Runtime Prompt State") {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// volatileBackgroundText reports whether any background runtime message contains
// the given substring. Unlike runtimePromptStateText, it scans every background
// message, including the volatile date/weekday tail message.
func volatileBackgroundText(messages []protocol.Message, want string) bool {
	for _, msg := range messages {
		if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindBackground {
			continue
		}
		if strings.Contains(protocol.MessageText(msg), want) {
			return true
		}
	}
	return false
}

func TestBuildContextIncludesSkillCatalogPrompt(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	skillPath := filepath.Join(a.cfg.SkillsDir, "review-helper", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
description: Review code changes with a structured checklist
when_to_use:
  - when the user asks for review
recommended_bundles:
  - background
sections:
  - core
  - workflow
---
## Core
Focus on regressions and missing tests.

## Workflow
Read the diff first.`), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	runtimeState := runtimePromptStateText(build.Messages)
	for _, want := range []string{
		"# Skill Availability",
		"Installed and discoverable skills. Use skill with action=list/sources/install/load/expand/unload. If the user says find-skills or asks to find a skill, use skill with action=sources and query; do not use tool_exchange for skill search.",
		"- review-helper: Review code changes with a structured checklist",
		"Recommended bundles: background.",
		"Sections: core, workflow.",
	} {
		if !strings.Contains(runtimeState, want) {
			t.Fatalf("expected skill catalog runtime prompt to contain %q, got %q", want, runtimeState)
		}
	}
}

func TestSkillCatalogPromptCompactsNestedSkillSuites(t *testing.T) {
	items := []skill.CatalogEntry{
		{ID: "solo", Name: "solo", Description: "Standalone skill"},
		{ID: "gstack", Name: "gstack", Description: "Gstack suite root skill"},
	}
	for _, name := range []string{
		"plan-ceo-review",
		"plan-eng-review",
		"plan-design-review",
		"qa",
		"review",
		"ship",
		"debug",
	} {
		items = append(items, skill.CatalogEntry{
			ID:          "gstack/" + name,
			Name:        name,
			Description: "gstack " + name,
			Compatibility: skill.Compatibility{
				Status: skill.CompatibilityNativeSupported,
			},
		})
	}

	prompt := buildSkillCatalogPrompt(items)
	for _, want := range []string{
		"- solo: Standalone skill",
		"- gstack: Gstack suite root skill 7 nested skills available:",
		"gstack/plan-ceo-review",
		"gstack/plan-eng-review",
		"gstack/ship",
		`Use skill with action=list and suite=`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected compact catalog prompt to contain %q, got %q", want, prompt)
		}
	}
	for _, omitted := range []string{"gstack plan-ceo-review", "gstack plan-eng-review", "gstack ship"} {
		if strings.Contains(prompt, omitted) {
			t.Fatalf("expected suite prompt to omit child descriptions such as %q, got %q", omitted, prompt)
		}
	}
	if strings.Contains(prompt, "- gstack suite:") {
		t.Fatalf("expected root skill line to carry nested skill summary instead of separate suite line, got %q", prompt)
	}
}

func TestSkillCatalogExposesSuiteMetadata(t *testing.T) {
	a := newTestAgent(t, 4096)
	rootPath := filepath.Join(a.cfg.SkillsDir, "gstack", "SKILL.md")
	childPath := filepath.Join(a.cfg.SkillsDir, "gstack", "plan-eng-review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(rootPath), 0755); err != nil {
		t.Fatalf("mkdir root skill: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(childPath), 0755); err != nil {
		t.Fatalf("mkdir child skill: %v", err)
	}
	if err := os.WriteFile(rootPath, []byte(`---
description: Gstack suite root skill
---
## Core
Use gstack for specialist workflows.`), 0644); err != nil {
		t.Fatalf("write root skill: %v", err)
	}
	if err := os.WriteFile(childPath, []byte(`---
description: Engineering manager architecture review
---
## Core
Review architecture.`), 0644); err != nil {
		t.Fatalf("write child skill: %v", err)
	}

	items, err := a.ListSkills()
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	var root, child skill.CatalogEntry
	for _, item := range items {
		switch item.ID {
		case "gstack":
			root = item
		case "gstack/plan-eng-review":
			child = item
		}
	}
	if root.SkillKind != "suite_root" || root.ChildSkillCount != 1 || len(root.ChildSkillIDs) != 1 || root.ChildSkillIDs[0] != "gstack/plan-eng-review" {
		t.Fatalf("expected suite root metadata, got %+v", root)
	}
	if !strings.Contains(root.ChildSkillHint, `suite="gstack"`) {
		t.Fatalf("expected child skill hint to mention suite lookup, got %q", root.ChildSkillHint)
	}
	if child.SkillKind != "child_skill" || child.SuiteID != "gstack" {
		t.Fatalf("expected child skill metadata, got %+v", child)
	}

	detail, err := a.GetSkill("gstack")
	if err != nil {
		t.Fatalf("get root skill: %v", err)
	}
	if detail.SkillKind != "suite_root" || detail.ChildSkillCount != 1 {
		t.Fatalf("expected get skill to preserve suite metadata, got %+v", detail)
	}

	activated, err := a.ActivateSkill("gstack")
	if err != nil {
		t.Fatalf("activate root skill: %v", err)
	}
	if activated.SkillKind != "suite_root" || activated.ChildSkillCount != 1 || len(activated.ChildSkillIDs) != 1 {
		t.Fatalf("expected activation to expose loaded suite root metadata, got %+v", activated)
	}
}

func TestBuildContextExposesSkillManagementSchemasOnlyWhenNeeded(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	skillPath := filepath.Join(a.cfg.SkillsDir, "example", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
description: Use the example skill.
sections:
  - core
  - workflow
---
## Core
Core.

## Workflow
Workflow.`), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	before, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context before load: %v", err)
	}
	if _, ok := schemaByName(before.ToolSchemas, "skill"); !ok {
		t.Fatalf("expected skill to be always available, got %+v", before.ToolSchemas)
	}

	if _, err := a.handleTool(context.Background(), "skill", map[string]interface{}{"action": "load", "name": "example"}); err != nil {
		t.Fatalf("load skill: %v", err)
	}

	after, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context after load: %v", err)
	}
	names := make(map[string]struct{}, len(after.ToolSchemas))
	for _, schema := range after.ToolSchemas {
		names[schema.Name] = struct{}{}
	}
	for _, want := range []string{"skill"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("expected %s after skill activation, got %+v", want, after.ToolSchemas)
		}
	}
}

func TestBuildContextExposesMemoryCandidateActionsOnlyWhenNeeded(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	before, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context before candidates: %v", err)
	}
	if _, ok := schemaByName(before.ToolSchemas, "memory"); !ok {
		t.Fatalf("expected memory to be always available, got %+v", before.ToolSchemas)
	}

	a.AddMessage("以后请用中文回复。")
	a.client = fakeCaller{resp: protocol.Response{
		Content: []protocol.Block{protocol.TextBlock("好的，我之后会使用中文回复。")},
	}}
	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("run agent to capture candidate: %v", err)
	}

	after, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context after candidates: %v", err)
	}
	names := make(map[string]struct{}, len(after.ToolSchemas))
	for _, schema := range after.ToolSchemas {
		names[schema.Name] = struct{}{}
	}
	for _, want := range []string{"memory"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("expected %s after candidate capture, got %+v", want, after.ToolSchemas)
		}
	}
}

func TestBuildContextExposesSkillMarketSchemasWhenUserRequestsSkillInstall(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.AddMessage("帮我安装一个适合代码评审的 skill。")

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	names := make(map[string]struct{}, len(build.ToolSchemas))
	for _, schema := range build.ToolSchemas {
		names[schema.Name] = struct{}{}
	}
	for _, want := range []string{"skill"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("expected %s for skill-install request, got %+v", want, build.ToolSchemas)
		}
	}
}

func TestBuildContextExposesMemoryAdminSchemasWhenUserMentionsMemory(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.AddMessage("记住这个偏好，并列出当前 memory。")

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	names := make(map[string]struct{}, len(build.ToolSchemas))
	for _, schema := range build.ToolSchemas {
		names[schema.Name] = struct{}{}
	}
	for _, want := range []string{"memory"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("expected %s for memory request, got %+v", want, build.ToolSchemas)
		}
	}
}

func TestBuildContextExposesCompressWhenRequested(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.AddMessage("请先压缩上下文，再继续回答。")

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	for _, schema := range build.ToolSchemas {
		if schema.Name == "compress" {
			return
		}
	}
	t.Fatalf("expected compress schema when user asks to compress context, got %+v", build.ToolSchemas)
}

func TestBuildContextShowsThirdPartySkillCompatibility(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	skillPath := filepath.Join(a.cfg.SkillsDir, "round-table", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`/round-table <topic>

Use bash tools and smoke_test.sh before continuing.
Preferred search path: mcp__MiniMax__web_search.
Coordinate with rt-tech and rt-risk subagent roles.`), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	runtimeState := runtimePromptStateText(build.Messages)
	for _, want := range []string{
		"- round-table: Skill: round-table",
		"Compatibility: degraded_supported.",
		"Recommended bundles: core_code, background, subagent, mcp.",
		"Missing capabilities: mcp.",
	} {
		if !strings.Contains(runtimeState, want) {
			t.Fatalf("expected third-party skill runtime prompt to contain %q, got %q", want, runtimeState)
		}
	}
}

func TestListSkillsResolvesMCPCompatibilityWhenServersConfigured(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	if err := os.MkdirAll(filepath.Dir(a.cfg.MCPConfigPath), 0755); err != nil {
		t.Fatalf("mkdir mcp config dir: %v", err)
	}
	if err := os.WriteFile(a.cfg.MCPConfigPath, []byte(`{"servers":[{"name":"docs","type":"filesystem","root":"docs"}]}`), 0644); err != nil {
		t.Fatalf("write mcp config: %v", err)
	}

	skillPath := filepath.Join(a.cfg.SkillsDir, "round-table", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`/round-table <topic>

Preferred search path: mcp__MiniMax__web_search.`), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	items, err := a.ListSkills()
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 skill, got %+v", items)
	}
	if items[0].Compatibility.Status != skill.CompatibilityNativeSupported {
		t.Fatalf("expected mcp and slash-command runtime adapters to resolve compatibility, got %+v", items[0].Compatibility)
	}
	if stringutil.Contains(items[0].Compatibility.MissingCapabilities, "mcp") {
		t.Fatalf("did not expect mcp to remain missing, got %+v", items[0].Compatibility)
	}
	if stringutil.Contains(items[0].Compatibility.MissingCapabilities, "slash_command_runtime") {
		t.Fatalf("did not expect slash-command runtime to remain missing, got %+v", items[0].Compatibility)
	}
}

func TestListSkillsAdaptsForkHooksAndAllowedTools(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	skillPath := filepath.Join(a.cfg.SkillsDir, "forked-review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
description: Forked review workflow
---
allowed-tools: bash, background
context: fork
hooks: on_complete
`), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	items, err := a.ListSkills()
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 skill, got %+v", items)
	}

	if items[0].Compatibility.Status != skill.CompatibilityNativeSupported {
		t.Fatalf("expected runtime adapters to resolve fork+hooks skill, got %+v", items[0].Compatibility)
	}
	for _, want := range []string{"core_code", "background", "subagent"} {
		if !stringutil.Contains(items[0].RecommendedBundles, want) {
			t.Fatalf("expected recommended bundle %q, got %+v", want, items[0].RecommendedBundles)
		}
	}
}

func TestBuildContextDoesNotMisclassifyPlainJSONText(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.appendMessage(protocol.NewTextMessage(protocol.RoleUser, `[{"type":"tool_result","tool_use_id":"tool-1","content":"done"}]`))

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	if len(build.Messages) < 1 {
		t.Fatalf("expected at least one message, got %d", len(build.Messages))
	}
	if len(build.Messages[0].Content) != 1 || build.Messages[0].Content[0].Type != protocol.BlockText {
		t.Fatalf("expected plain JSON string to remain text, got %+v", build.Messages[0].Content)
	}
}

func TestBuildContextCompactsPersistentHistoryButKeepsRuntimeMessages(t *testing.T) {
	a := newTestAgent(t, 8)
	a.AddMessage(strings.Repeat("user message ", 20))
	a.appendMessage(protocol.NewTextMessage(protocol.RoleAssistant, strings.Repeat("assistant message ", 20)))

	if err := a.msgBus.Send(message.Message{
		Type:    message.MsgTypeMessage,
		From:    "teammate",
		To:      "lead",
		Content: "fresh inbox update",
	}); err != nil {
		t.Fatalf("send inbox message: %v", err)
	}

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	if !build.Compacted {
		t.Fatal("expected context to be compacted")
	}

	foundSummary := false
	foundInbox := false
	for _, msg := range build.Messages {
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindSummary {
			foundSummary = true
		}
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindInbox {
			foundInbox = true
		}
	}
	if !foundSummary {
		t.Fatal("expected compacted context to include summary message")
	}
	if !foundInbox {
		t.Fatal("expected compacted context to keep inbox runtime message")
	}

	stored := a.GetMessages()
	if len(stored) == 0 || stored[0].Metadata == nil || stored[0].Metadata.Kind != protocol.KindSummary {
		t.Fatalf("expected persistent history to be compacted in memory, got %+v", stored)
	}
	for _, msg := range stored {
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindInbox {
			t.Fatal("did not expect runtime inbox message to be persisted")
		}
	}

	entries, err := os.ReadDir(a.cfg.TranscriptsDir)
	if err != nil {
		t.Fatalf("read transcript dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 transcript after first compaction, got %d", len(entries))
	}

	secondBuild, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context second time: %v", err)
	}
	if secondBuild.Compacted {
		t.Fatal("expected unchanged compacted history not to compact again")
	}
	entries, err = os.ReadDir(a.cfg.TranscriptsDir)
	if err != nil {
		t.Fatalf("read transcript dir again: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected transcript count to remain 1, got %d", len(entries))
	}
}

func TestBuildContextIncludesInstructionPrompt(t *testing.T) {
	a := newTestAgent(t, 4096)

	projectInstructions := filepath.Join(a.cfg.WorkspaceDir, "AGENT.md")
	if err := os.WriteFile(projectInstructions, []byte("Follow project instruction."), 0644); err != nil {
		t.Fatalf("write project instructions: %v", err)
	}
	rulePath := filepath.Join(a.cfg.RulesDir, "testing.md")
	if err := os.WriteFile(rulePath, []byte("Always run tests after runtime changes."), 0644); err != nil {
		t.Fatalf("write rule: %v", err)
	}
	localInstructions := filepath.Join(a.cfg.StateDir, "AGENT.local.md")
	if err := os.WriteFile(localInstructions, []byte("Keep local debug notes private."), 0644); err != nil {
		t.Fatalf("write local instructions: %v", err)
	}

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	for _, want := range []string{
		"# Instructions",
		"## AGENT.md",
		"Follow project instruction.",
		"## .godex/rules/testing.md",
		"Always run tests after runtime changes.",
		"## .godex/AGENT.local.md",
		"Keep local debug notes private.",
	} {
		if !strings.Contains(build.System, want) {
			t.Fatalf("expected system prompt to contain %q, got %q", want, build.System)
		}
	}
}

func TestBuildContextInjectsRelevantMemoryForCurrentQuery(t *testing.T) {
	a := newTestAgent(t, 4096)

	if _, err := a.memoryMgr.Remember(memory.SaveInput{
		Title:   "Testing Workflow",
		Summary: "Run go test ./... and go test -race ./... after runtime changes.",
		Content: "After changing core agent runtime code, run go test ./... and go test -race ./... before wrapping up.",
		Type:    memory.TypeWorkflow,
	}); err != nil {
		t.Fatalf("remember memory: %v", err)
	}

	a.AddMessage("Please update the runtime and run the tests afterwards.")
	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	foundMemory := false
	for _, msg := range build.Messages {
		if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindMemory {
			continue
		}
		text := protocol.MessageText(msg)
		if !strings.Contains(text, "Memory context:") {
			continue
		}
		foundMemory = true
		for _, want := range []string{
			"Memory context:",
			"Relevant recall for the current request:",
			"Testing Workflow [workflow]",
			"Run go test ./... and go test -race ./... after runtime changes.",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("expected memory runtime message to contain %q, got %q", want, text)
			}
		}
	}
	if !foundMemory {
		t.Fatal("expected relevant memory message to be injected")
	}

	stored := a.GetMessages()
	for _, msg := range stored {
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindMemory {
			t.Fatalf("did not expect relevant memory to persist in stored history, got %+v", stored)
		}
	}
}

func TestBuildContextInjectsStableCoreMemoryWithoutQueryMatch(t *testing.T) {
	a := newTestAgent(t, 4096)

	if _, err := a.memoryMgr.Remember(memory.SaveInput{
		Title:   "Project Identity",
		Summary: "GoDex is a shared backend workspace for Web, TUI, and IM.",
		Content: "Treat GoDex as a shared backend workspace coordinating Web, TUI, and IM channels.",
		Type:    memory.TypeIdentity,
	}); err != nil {
		t.Fatalf("remember identity memory: %v", err)
	}
	if _, err := a.memoryMgr.Remember(memory.SaveInput{
		Title:   "Chinese Preference",
		Summary: "Reply in concise Chinese.",
		Content: "以后请用中文回复，并保持简洁。",
		Type:    memory.TypeUser,
	}); err != nil {
		t.Fatalf("remember user memory: %v", err)
	}

	a.AddMessage("What should we work on next?")
	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	foundMemory := false
	for _, msg := range build.Messages {
		if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindMemory {
			continue
		}
		text := protocol.MessageText(msg)
		if !strings.Contains(text, "Memory context:") {
			continue
		}
		foundMemory = true
		for _, want := range []string{
			"Memory context:",
			"L0 identity:",
			"Project Identity [identity]",
			"Core project memory:",
			"Chinese Preference [user]",
			"Reply in concise Chinese.",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("expected memory runtime message to contain %q, got %q", want, text)
			}
		}
		if strings.Contains(text, "Relevant recall for the current request:") {
			t.Fatalf("did not expect relevant recall section for unmatched query, got %q", text)
		}
		if strings.Contains(text, "Treat GoDex as a shared backend workspace coordinating Web, TUI, and IM channels.") {
			t.Fatalf("did not expect identity memory full content in context, got %q", text)
		}
	}
	if !foundMemory {
		t.Fatal("expected stable core memory message to be injected")
	}
}

func TestBuildContextTruncatesRelevantMemoryContent(t *testing.T) {
	a := newTestAgent(t, 4096)
	longContent := strings.Repeat("repeat detail ", 160) + "UNIQUE_TAIL_SHOULD_NOT_APPEAR"
	if _, err := a.memoryMgr.Remember(memory.SaveInput{
		Title:   "Testing Workflow",
		Summary: "Runtime context includes memory previews.",
		Content: longContent,
		Type:    memory.TypeWorkflow,
	}); err != nil {
		t.Fatalf("remember memory: %v", err)
	}

	a.AddMessage("Please debug runtime context memory previews.")
	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	for _, msg := range build.Messages {
		if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindMemory {
			continue
		}
		text := protocol.MessageText(msg)
		if !strings.Contains(text, "Relevant recall") {
			continue
		}
		if !strings.Contains(text, "Testing Workflow") || !strings.Contains(text, "repeat detail") {
			t.Fatalf("expected relevant memory preview, got %q", text)
		}
		if strings.Contains(text, "UNIQUE_TAIL_SHOULD_NOT_APPEAR") {
			t.Fatalf("expected long relevant memory content to be truncated, got %q", text)
		}
		return
	}
	t.Fatal("expected memory context message")
}

func TestRunDoesNotAckRuntimeInputsOnCallError(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.AddMessage("hello")
	a.client = fakeCaller{err: errors.New("boom")}

	if err := a.msgBus.Send(message.Message{
		Type:    message.MsgTypeMessage,
		From:    "teammate",
		To:      "lead",
		Content: "need review",
	}); err != nil {
		t.Fatalf("send inbox message: %v", err)
	}

	task, err := a.bgMgr.Start("bg-fail", exec.Command("sh", "-c", "printf done"), 0)
	if err != nil {
		t.Fatalf("start background task: %v", err)
	}
	<-task.Done

	err = a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected boom error, got %v", err)
	}
	if got := a.msgBus.PeekInbox("lead"); len(got) != 1 {
		t.Fatalf("expected inbox message to remain after failed call, got %d", len(got))
	}
	if got := a.bgMgr.PeekNotifications(); len(got) != 1 {
		t.Fatalf("expected background notification to remain after failed call, got %d", len(got))
	}
}

func TestRunAcksRuntimeInputsAfterSuccessfulTurn(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.AddMessage("hello")
	a.client = fakeCaller{resp: protocol.Response{Content: []protocol.Block{protocol.TextBlock("done")}}}

	if err := a.msgBus.Send(message.Message{
		Type:    message.MsgTypeMessage,
		From:    "teammate",
		To:      "lead",
		Content: "need review",
	}); err != nil {
		t.Fatalf("send inbox message: %v", err)
	}

	task, err := a.bgMgr.Start("bg-success", exec.Command("sh", "-c", "printf done"), 0)
	if err != nil {
		t.Fatalf("start background task: %v", err)
	}
	<-task.Done

	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("run agent: %v", err)
	}
	if got := a.msgBus.PeekInbox("lead"); len(got) != 0 {
		t.Fatalf("expected inbox to be acked after success, got %d", len(got))
	}
	if got := a.bgMgr.PeekNotifications(); len(got) != 0 {
		t.Fatalf("expected background notifications to be acked after success, got %d", len(got))
	}
}

func TestRunCapturesMemoryCandidatesAfterSuccessfulTurn(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.AddMessage("以后请用中文回复。")
	a.client = fakeCaller{resp: protocol.Response{
		Content: []protocol.Block{protocol.TextBlock("好的，我之后会使用中文回复。")},
	}}

	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("run agent: %v", err)
	}

	candidates, err := memory.LoadCandidates(filepath.Join(a.cfg.MemoryDir, memory.CandidatesFileName))
	if err != nil {
		t.Fatalf("load captured memory candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 captured memory candidate, got %+v", candidates)
	}
	if candidates[0].Title != "User Preference: Reply in Chinese" {
		t.Fatalf("unexpected candidate %+v", candidates[0])
	}
}

func TestLoadSkillToolActivatesSkillForFutureTurnsAndDeduplicates(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	skillPath := filepath.Join(a.cfg.SkillsDir, "example", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
description: Use the example skill.
recommended_bundles:
  - background
sections:
  - core
  - workflow
---
## Core
Use the example skill core.

## Workflow
Run the example workflow.`), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	result, err := a.handleTool(context.Background(), "skill", map[string]interface{}{"action": "load", "name": "example"})
	if err != nil {
		t.Fatalf("load skill tool: %v", err)
	}
	for _, want := range []string{`"status":"activated"`, `"loaded_sections":["core"]`, `"recommended_bundles":["background"]`} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected load result to contain %q, got %q", want, result)
		}
	}

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	runtimeState := runtimePromptStateText(build.Messages)
	if !strings.Contains(runtimeState, "# Active Skills") {
		t.Fatalf("expected active skills runtime prompt, got %q", runtimeState)
	}
	if !strings.Contains(runtimeState, "Use the example skill core.") {
		t.Fatalf("expected runtime prompt to include core skill content, got %q", runtimeState)
	}
	if strings.Contains(runtimeState, "Run the example workflow.") {
		t.Fatalf("did not expect workflow section before expand, got %q", runtimeState)
	}

	result, err = a.handleTool(context.Background(), "skill", map[string]interface{}{"action": "load", "name": "example"})
	if err != nil {
		t.Fatalf("reload skill tool: %v", err)
	}
	if !strings.Contains(result, `"status":"already_active"`) {
		t.Fatalf("expected activated status, got %q", result)
	}

	build, err = a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context again: %v", err)
	}
	runtimeState = runtimePromptStateText(build.Messages)
	if got := strings.Count(runtimeState, "Use the example skill core."); got != 1 {
		t.Fatalf("expected skill core to be injected once, got %d copies in %q", got, runtimeState)
	}
}

func TestExpandSkillToolAddsRequestedSectionForFutureTurns(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	skillPath := filepath.Join(a.cfg.SkillsDir, "example", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
description: Use the example skill.
sections:
  - core
  - workflow
  - references
---
## Core
Use the example skill core.

## Workflow
Run the example workflow.

## References
Review the reference checklist.`), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	if _, err := a.handleTool(context.Background(), "skill", map[string]interface{}{"action": "load", "name": "example"}); err != nil {
		t.Fatalf("load skill tool: %v", err)
	}

	result, err := a.handleTool(context.Background(), "skill", map[string]interface{}{
		"action":   "expand",
		"name":     "example",
		"sections": []interface{}{"workflow"},
	})
	if err != nil {
		t.Fatalf("expand skill tool: %v", err)
	}
	for _, want := range []string{`"status":"expanded"`, `"expanded_sections":["workflow"]`} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected expand result to contain %q, got %q", want, result)
		}
	}

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	runtimeState := runtimePromptStateText(build.Messages)
	if !strings.Contains(runtimeState, "Run the example workflow.") {
		t.Fatalf("expected expanded workflow section in runtime prompt, got %q", runtimeState)
	}
	if strings.Contains(runtimeState, "Review the reference checklist.") {
		t.Fatalf("did not expect unexpanded references section in runtime prompt, got %q", runtimeState)
	}
}

func TestRememberMemoryToolPersistsEntryAndUpdatesIndex(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	result, err := a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "remember",
		"title":       "Testing Workflow",
		"summary":     "Run go test ./... after runtime changes.",
		"content":     "Run go test ./... and go test -race ./... when runtime code changes.",
		"memory_type": "workflow",
	})
	if err != nil {
		t.Fatalf("remember memory tool: %v", err)
	}
	if !strings.Contains(result, `"status":"saved"`) {
		t.Fatalf("expected saved status, got %q", result)
	}

	indexData, err := os.ReadFile(filepath.Join(a.cfg.MemoryDir, memory.EntrypointName))
	if err != nil {
		t.Fatalf("read memory index: %v", err)
	}
	if got := string(indexData); !strings.Contains(got, "[Testing Workflow](testing_workflow.md) - workflow - Run go test ./... after runtime changes.") {
		t.Fatalf("expected index to contain saved entry, got %q", got)
	}

	fileData, err := os.ReadFile(filepath.Join(a.cfg.MemoryDir, "testing_workflow.md"))
	if err != nil {
		t.Fatalf("read memory file: %v", err)
	}
	if got := string(fileData); !strings.Contains(got, "Run go test ./... and go test -race ./... when runtime code changes.") {
		t.Fatalf("expected memory file to contain saved content, got %q", got)
	}
}

func TestForgetMemoryToolDeletesEntryAndRewritesIndex(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	if _, err := a.memoryMgr.Remember(memory.SaveInput{
		Title:   "Outdated Workflow",
		Summary: "Old memory to remove.",
		Content: "This workflow is stale.",
		Type:    memory.TypeWorkflow,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	result, err := a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "forget",
		"title": "Outdated Workflow",
	})
	if err != nil {
		t.Fatalf("forget memory tool: %v", err)
	}
	if !strings.Contains(result, `"status":"forgotten"`) {
		t.Fatalf("expected forgotten status, got %q", result)
	}

	indexData, err := os.ReadFile(filepath.Join(a.cfg.MemoryDir, memory.EntrypointName))
	if err != nil {
		t.Fatalf("read memory index: %v", err)
	}
	if strings.Contains(string(indexData), "Outdated Workflow") {
		t.Fatalf("expected index to remove forgotten memory, got %q", string(indexData))
	}
	if _, err := os.Stat(filepath.Join(a.cfg.MemoryDir, "outdated_workflow.md")); !os.IsNotExist(err) {
		t.Fatalf("expected memory file to be removed, got %v", err)
	}
}

func TestMemoryBrowseAndCandidateTools(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	if _, err := a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "remember",
		"title":       "Delivery Rule",
		"summary":     "Prefer explicit delivery confirmations.",
		"content":     "When automation delivers to a channel, make the result visible to the user.",
		"memory_type": "project",
		"source":      "manual",
		"tags":        []interface{}{"delivery", "automation"},
	}); err != nil {
		t.Fatalf("seed remember memory: %v", err)
	}

	listResult, err := a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "list"})
	if err != nil {
		t.Fatalf("list memory tool: %v", err)
	}
	for _, want := range []string{`"title":"Delivery Rule"`, `"tags":["automation","delivery"]`, `"source":"manual"`} {
		if !strings.Contains(listResult, want) {
			t.Fatalf("expected list_memory result to contain %q, got %q", want, listResult)
		}
	}

	searchResult, err := a.handleTool(context.Background(), "memory", map[string]interface{}{
		"action": "search",
		"tag":    "automation",
	})
	if err != nil {
		t.Fatalf("search memory tool: %v", err)
	}
	if !strings.Contains(searchResult, `"title":"Delivery Rule"`) {
		t.Fatalf("expected search_memory result to contain saved memory, got %q", searchResult)
	}

	getResult, err := a.handleTool(context.Background(), "memory", map[string]interface{}{
		"action":      "get",
		"id_or_title": "Delivery Rule",
	})
	if err != nil {
		t.Fatalf("get memory tool: %v", err)
	}
	if !strings.Contains(getResult, `"content":"When automation delivers to a channel, make the result visible to the user."`) {
		t.Fatalf("expected get_memory result to contain content, got %q", getResult)
	}

	a.AddMessage("以后请用中文回复。")
	a.client = fakeCaller{resp: protocol.Response{
		Content: []protocol.Block{protocol.TextBlock("好的，我之后会使用中文回复。")},
	}}
	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("run agent to capture candidate: %v", err)
	}

	candidatesResult, err := a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "candidates"})
	if err != nil {
		t.Fatalf("list memory candidates tool: %v", err)
	}
	if !strings.Contains(candidatesResult, `"title":"User Preference: Reply in Chinese"`) {
		t.Fatalf("expected candidate to be listed, got %q", candidatesResult)
	}

	candidates, err := a.memoryMgr.ListCandidates()
	if err != nil {
		t.Fatalf("list candidates directly: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %+v", candidates)
	}

	acceptResult, err := a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "accept",
		"fingerprint": candidates[0].Fingerprint,
	})
	if err != nil {
		t.Fatalf("accept memory candidate tool: %v", err)
	}
	if !strings.Contains(acceptResult, `"status":"accepted"`) {
		t.Fatalf("expected accepted status, got %q", acceptResult)
	}

	a.AddMessage("修一下 runtime。")
	a.client = fakeCaller{resp: protocol.Response{
		Content: []protocol.Block{protocol.TextBlock("Run go test ./... after Go changes.")},
	}}
	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("run agent to capture second candidate: %v", err)
	}

	candidates, err = a.memoryMgr.ListCandidates()
	if err != nil {
		t.Fatalf("list candidates after second capture: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate after accept+second capture, got %+v", candidates)
	}

	dismissResult, err := a.handleTool(context.Background(), "memory", map[string]interface{}{"action": "dismiss",
		"fingerprint": candidates[0].Fingerprint,
	})
	if err != nil {
		t.Fatalf("dismiss memory candidate tool: %v", err)
	}
	if !strings.Contains(dismissResult, `"status":"dismissed"`) {
		t.Fatalf("expected dismissed status, got %q", dismissResult)
	}
}

func TestInactiveToolIsRejectedUntilBundleEnabled(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	_, err := a.handleTool(context.Background(), "background", map[string]interface{}{
		"command": `sh -c 'printf ok'`,
	})
	if err == nil || !strings.Contains(err.Error(), `enable bundle "background" with tool_exchange`) {
		t.Fatalf("expected inactive tool guidance, got %v", err)
	}

	if _, err := a.handleTool(context.Background(), "tool_exchange", map[string]interface{}{
		"enable_bundles": []interface{}{"background"},
	}); err != nil {
		t.Fatalf("enable background bundle: %v", err)
	}

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context after enabling bundle: %v", err)
	}
	names := make(map[string]struct{}, len(build.ToolSchemas))
	for _, schema := range build.ToolSchemas {
		names[schema.Name] = struct{}{}
	}
	if _, ok := names["background"]; !ok {
		t.Fatalf("expected background_run schema after enabling bundle, got %+v", build.ToolSchemas)
	}
	if _, ok := names["background"]; !ok {
		t.Fatalf("expected check_background schema after enabling bundle, got %+v", build.ToolSchemas)
	}

	if _, err := a.handleTool(context.Background(), "background", map[string]interface{}{
		"action":  "run",
		"command": `sh -c 'printf ok'`,
	}); err != nil {
		t.Fatalf("expected background to work after enabling bundle, got %v", err)
	}
}

func newTestAgent(t *testing.T, compressThreshold int) *Agent {
	t.Helper()

	workspace := t.TempDir()
	cfg := &config.Config{
		Model:             "test-model",
		BaseURL:           "http://127.0.0.1",
		MaxTokens:         1024,
		WorkspaceDir:      workspace,
		StateDir:          filepath.Join(workspace, ".godex"),
		TeamDir:           filepath.Join(workspace, ".godex", ".team"),
		TasksDir:          filepath.Join(workspace, ".godex", ".tasks"),
		TodosDir:          filepath.Join(workspace, ".godex", ".todos"),
		MemoryDir:         filepath.Join(workspace, ".godex", "memory"),
		RulesDir:          filepath.Join(workspace, ".godex", "rules"),
		SkillsDir:         filepath.Join(workspace, ".godex", "skills"),
		MCPConfigPath:     filepath.Join(workspace, ".godex", "mcp.json"),
		TempDir:           filepath.Join(workspace, ".godex", ".tmp"),
		TranscriptsDir:    filepath.Join(workspace, ".godex", ".transcripts"),
		CompressThreshold: compressThreshold,
		Compaction: config.AgentCompactionConfig{
			AutoEnabled:         true,
			TriggerTokens:       compressThreshold,
			TargetHistoryTokens: 12000,
			Mode:                "fast",
			MaxLatencyMS:        3000,
		},
		LeadName: "lead",
		TeamName: "default",
		Tools: config.ToolsConfig{
			WebSearch: config.WebSearchConfig{
				Enabled:         true,
				ProviderOrder:   []string{"brave", "exa", "tavily", "duckduckgo"},
				CacheTTLSeconds: 300,
			},
			WebFetch: config.WebFetchConfig{
				Enabled:        true,
				MaxChars:       60000,
				TimeoutSeconds: 30,
				Policy:         "allow_all",
			},
			Glob: config.GlobConfig{
				DefaultMaxResults: 200,
			},
			Browser: config.BrowserConfig{
				Enabled:              false,
				Headless:             true,
				ActionTimeoutSeconds: 30,
				IdleTimeoutSeconds:   600,
				MaxPagesPerSession:   3,
			},
			Permissions: config.PermissionConfig{
				BlockAutomationMutations:   true,
				InteractiveApprovalEnabled: true,
				InteractiveApprovalSources: []string{"web", "gateway", "feishu", "weixin"},
				InteractiveApprovalTools: []string{
					"bash",
					"background",
					"write_file",
					"edit_file",
					"skill",
					"tool_exchange",
					"cron",
					"heartbeat",
					"browser",
				},
			},
		},
	}

	for _, dir := range []string{
		filepath.Join(cfg.TeamDir, "inbox"),
		cfg.TasksDir,
		cfg.TodosDir,
		cfg.MemoryDir,
		cfg.RulesDir,
		cfg.SkillsDir,
		cfg.TempDir,
		cfg.TranscriptsDir,
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	a := New(cfg)
	a.now = func() time.Time {
		return time.Date(2026, time.April, 17, 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			a.compactionMu.Lock()
			running := a.compactionRunning
			a.compactionMu.Unlock()
			if !running {
				return
			}
			if time.Now().After(deadline) {
				t.Error("timed out waiting for background compaction")
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	return a
}

func TestSubagentSchemaUsesJSONSchemaEnumArray(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	for _, schema := range build.ToolSchemas {
		if schema.Name != "subagent" {
			continue
		}
		properties, _ := schema.InputSchema["properties"].(map[string]interface{})
		agentType, _ := properties["agent_type"].(map[string]interface{})
		if _, ok := agentType["enum"]; ok {
			t.Fatalf("did not expect fixed agent_type enum after named role support, got %#v", agentType)
		}
		description, _ := agentType["description"].(string)
		if !strings.Contains(description, "named role") {
			t.Fatalf("expected agent_type description to mention named roles, got %#v", agentType)
		}
		if _, ok := properties["required_bundles"]; !ok {
			t.Fatalf("expected task schema to expose required_bundles, got %#v", properties)
		}
		if _, ok := properties["required_tools"]; !ok {
			t.Fatalf("expected task schema to expose required_tools, got %#v", properties)
		}
		return
	}

	t.Fatal("expected task subagent schema to be present")
}

func TestBuildContextIncludesTodoStatusWhenTodoListNotEmpty(t *testing.T) {
	a := newTestAgent(t, 100000)
	a.RegisterTools()
	if _, err := a.todoMgr.Add("Ship C fix", "Shipping C fix"); err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	if _, err := a.todoMgr.Add("Verify tests", "Verifying tests"); err != nil {
		t.Fatalf("seed todo: %v", err)
	}

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	// System prompt must not be polluted (lock long-cache anchor).
	for _, leak := range []string{"Ship C fix", "Verifying tests", "Current todos:"} {
		if strings.Contains(build.System, leak) {
			t.Fatalf("todo status must not leak into system prompt, found %q in system", leak)
		}
	}

	found := false
	for _, msg := range build.Messages {
		if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindBackground {
			continue
		}
		text := protocol.MessageText(msg)
		// Manager.Render() outputs Item.Content (not ActiveForm) and appends
		// the (X/N completed) footer, so assert against Content.
		if strings.Contains(text, "Ship C fix") && strings.Contains(text, "Verify tests") && strings.Contains(text, "Current todos:") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected todo status rendered in a KindBackground ephemeral message, got %+v", build.Messages)
	}
}

func TestBuildContextSkipsTodoStatusWhenTodoListEmpty(t *testing.T) {
	a := newTestAgent(t, 100000)
	a.RegisterTools()

	build, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	for _, msg := range build.Messages {
		if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindBackground {
			continue
		}
		if strings.Contains(protocol.MessageText(msg), "Current todos:") {
			t.Fatalf("todo status must be skipped when list is empty, got %q", protocol.MessageText(msg))
		}
	}
	if strings.Contains(build.System, "Current todos:") {
		t.Fatalf("system prompt must not carry an empty todo section, got %q", build.System)
	}
}

func TestBuildContextKeepsSystemPromptUnchangedWhenTodoStatusAdded(t *testing.T) {
	a := newTestAgent(t, 100000)
	a.RegisterTools()

	empty, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context (empty): %v", err)
	}

	if _, err := a.todoMgr.Add("Pin system prompt", "Pinning system prompt"); err != nil {
		t.Fatalf("seed todo: %v", err)
	}

	withTodo, err := a.buildContext(context.Background())
	if err != nil {
		t.Fatalf("build context (with todo): %v", err)
	}

	if empty.System != withTodo.System {
		t.Fatalf("system prompt must be byte-identical before and after adding todos; diff: %q vs %q", empty.System, withTodo.System)
	}
}

func schemaByName(items []protocol.ToolSchema, name string) (protocol.ToolSchema, bool) {
	for _, item := range items {
		if item.Name == name {
			return item, true
		}
	}
	return protocol.ToolSchema{}, false
}
