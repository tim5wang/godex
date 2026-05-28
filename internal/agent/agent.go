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
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/core/teammate"
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
	sandbox       *sandbox.Sandbox
	workerRuntime workerruntime.Runtime

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
	sessionStartTime     time.Time
	mu                   sync.Mutex
}

type dependencies struct {
	taskMgr      *task.Manager
	msgBus       *message.Bus
	client       conversation.Caller
	skillLoader  *skill.Loader
	instrLoader  *instructions.Loader
	memoryMgr    *memory.Manager
	memoryExt    *memory.Extractor
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
	sandbox      *sandbox.Sandbox
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
