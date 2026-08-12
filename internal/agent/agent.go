package agent

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/background"
	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/instructions"
	"github.com/tim5wang/godex/internal/core/mcp"
	"github.com/tim5wang/godex/internal/core/media"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/notes"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/core/security"
	"github.com/tim5wang/godex/internal/core/teammate"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/sandbox"
	"github.com/tim5wang/godex/internal/services/historysearch"
	"github.com/tim5wang/godex/internal/services/sessionadmin"
	"github.com/tim5wang/godex/internal/tools"
	"github.com/tim5wang/godex/internal/workerruntime"
)

// Agent is the main agent orchestrator.
type Agent struct {
	cfg           *config.Config
	toolHandler   *tools.ToolHandler
	todoMgr       *todo.Manager
	skillLoader   *skill.Loader
	instrLoader   *instructions.Loader
	memoryMgr     *memory.Manager
	memoryExt     *memory.Extractor
	memoryStrategy memory.Strategy
	notesMgr      *notes.Manager
	mcpMgr        *mcp.Manager
	compressor    *compress.Compressor
	summarizer    compress.SessionSummarizer
	taskMgr       *task.Manager
	bgMgr         *background.Manager
	webSearch     *tools.WebSearchService
	webFetch      *tools.WebFetchService
	browser       *tools.BrowserService
	permissions   *tools.PermissionManager
	historySearch tools.HistorySearchRuntime
	sessionAdmin  tools.SessionAdminRuntime
	cron          tools.CronManager
	heartbeat     tools.HeartbeatManager
	media         *media.Processor
	msgBus        *message.Bus
	teamMgr       *teammate.Manager
	subagentJobs  *subagentJobStore
	workflows     *workflowStore
	client        conversation.Caller
	sandbox       sandbox.Sandbox
	workerRuntime workerruntime.Runtime
	// screener classifies untrusted content before it reaches the model
	// (roadmap 6.1 content-level security screener).
	screener    security.Screener
	screenAudit screenAuditFn
	roleBundles   *roleBundleRegistry
	// emitSink is the event sink of the currently running turn, set by
	// RunWithOptions. Manual compaction (compress tool) emits snapshot_ready
	// through it so compaction history records manual compactions too; nil
	// falls back to NopSink (e.g. /compact outside a turn).
	emitSink events.Sink
	// workspaceOverride is set when this session was opened against an
	// explicit working directory different from the service-level
	// cfg.WorkspaceDir. ApplyConfig must re-apply the override on top of
	// any refreshed global config so a global config reload never moves
	// the session's tool execution back to the service directory.
	workspaceOverride string

	prompts              conversation.PromptLayers
	messages             []protocol.Message
	pendingResume        *PendingResumeState
	idleRequested        bool
	activeSkills         map[string]*activeSkillState
	transcriptRefs       []string
	historyVersion       int64
	lastCompactedVersion int64
	compactionMu         sync.Mutex
	compactionCandidate  *compactionCandidate
	compactionRunning    bool
	now                  func() time.Time
	mu                   sync.Mutex
	// cacheStatsMu guards cacheStats, the in-memory per-session aggregation
	// of provider-reported prompt cache usage. It is fed by a conversation
	// usage hook and surfaced through InspectContext so the UI can show the
	// real cache hit rate instead of only the static prefix estimate.
	cacheStatsMu sync.Mutex
	cacheStats   sessionCacheStats
	unsubUsage   func()
	// currentLongTaskArgs is a transient pointer set by runLongTaskSync
	// for the duration of a single run so helpers like
	// appendLongTaskReflux can see args such as NoReflux without
	// having to thread the value through every call site.
	currentLongTaskArgs *longTaskArgs

	// harnessOnce guards lazy initialization of harnessRouterVal.
	harnessOnce sync.Once
	harnessRouterVal Harness
	// extraHarnesses holds engines registered via RegisterHarness beyond
	// the built-in godex engine (roadmap 6.4 multi-engine switching).
	extraHarnesses map[string]Harness
}

type dependencies struct {
	taskMgr      *task.Manager
	msgBus       *message.Bus
	client       conversation.Caller
	skillLoader  *skill.Loader
	instrLoader  *instructions.Loader
	memoryMgr    *memory.Manager
	memoryExt    *memory.Extractor
	memoryStrategy memory.Strategy
	notesMgr     *notes.Manager
	mcpMgr       *mcp.Manager
	compressor   *compress.Compressor
	summarizer   compress.SessionSummarizer
	bgMgr        *background.Manager
	webSearch    *tools.WebSearchService
	webFetch     *tools.WebFetchService
	browser      *tools.BrowserService
	permissions  *tools.PermissionManager
	history      *historysearch.Service
	sessionAdmin *sessionadmin.Service
	cron         tools.CronManager
	heartbeat    tools.HeartbeatManager
	media        *media.Processor
	teamMgr      *teammate.Manager
	subagentJobs *subagentJobStore
	workflows    *workflowStore
	todoMgr      *todo.Manager
	sandbox      sandbox.Sandbox
}

// New creates a new agent.
func New(cfg *config.Config) *Agent {
	return newAgentWithDependencies(cfg, buildDependencies(cfg))
}

// Run runs the agent loop.
func (a *Agent) Run(ctx context.Context) error {
	return a.RunWithOptions(ctx, RunOptions{})
}

func extractTranscriptRefs(messages []protocol.Message) []string {
	if len(messages) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(messages))
	refs := make([]string, 0, len(messages))
	for _, msg := range messages {
		if msg.Metadata == nil {
			continue
		}
		ref := strings.TrimSpace(msg.Metadata.Transcript)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

func mergeTranscriptRefs(existing, incoming []string) []string {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	merged := make([]string, 0, len(existing)+len(incoming))
	for _, ref := range existing {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		merged = append(merged, ref)
	}
	for _, ref := range incoming {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		merged = append(merged, ref)
	}
	return merged
}

// GenerateTitle sends the first user message to the LLM for a concise session
// title. Returns the generated title or an error. Intended for async use.
func (a *Agent) GenerateTitle(ctx context.Context, firstMessage string) (string, error) {
	if a.client == nil {
		return "", nil
	}
	req := protocol.Request{
		System: "You are a title generator. Generate a concise session title (max 6 words) for a conversation that starts with the following user message. Output ONLY the title, no explanation, no quotes.",
		Messages: []protocol.APIMessage{
			{
				Role:    protocol.RoleUser,
				Content: []protocol.Block{{Type: protocol.BlockText, Text: firstMessage}},
			},
		},
		MaxTokens: 30,
	}
	resp, err := a.client.Call(ctx, req)
	if err != nil || resp == nil {
		return "", err
	}
	text := strings.TrimSpace(protocol.MessageText(protocol.MessageFromResponse(*resp)))
	text = strings.Trim(text, "\"'`「」『』")
	text = strings.Join(strings.Fields(text), " ")
	if text == "" || len(text) > 120 {
		return "", nil
	}
	return text, nil
}
