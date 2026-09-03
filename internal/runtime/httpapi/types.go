package httpapi

import (
	"context"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/message"
	rtchannels "github.com/tim5wang/godex/internal/runtime/channels"
	"github.com/tim5wang/godex/internal/runtime/channels/weixin"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
	"github.com/tim5wang/godex/internal/version"
)

type openSessionRequest struct {
	Locator backend.SessionLocator `json:"locator"`
	// WorkspaceDir optionally pins this session's tool execution to an
	// explicit working directory.  It is folded into the locator's
	// project_dir metadata before the backend validates and hashes it.
	WorkspaceDir string `json:"workspace_dir,omitempty"`
}

type submitMessageRequest struct {
	Envelope  message.Envelope `json:"envelope"`
	Text      string           `json:"text,omitempty"`
	Sender    string           `json:"sender,omitempty"`
	QueueMode string           `json:"queue_mode,omitempty"`
}

type openAIChatCompletionRequest struct {
	Model     string              `json:"model,omitempty"`
	Messages  []openAIChatMessage `json:"messages"`
	Stream    bool                `json:"stream,omitempty"`
	MaxTokens int                 `json:"max_tokens,omitempty"`
	// Tools is the OpenAI Chat Completions `tools` array. The
	// upstream LLM client (internal/core/conversation/openai_client.go)
	// converts each entry to the wire shape {type:"function",
	// function:{name, description, parameters, strict}} and the
	// model uses them to decide when to emit a tool_calls delta.
	//
	// This field was previously absent from the struct, so the
	// OpenAI usage-gateway path silently dropped every tool the
	// caller advertised — the model would never see the bash /
	// read / write_file schemas and would resort to generating
	// text-based "fake" tool calls like `<bash>...` that the
	// OpenAI SDK does not parse as a tool invocation. The fix
	// restores the field and routes it through to the LLM client
	// via usageGatewayProtocolRequest.
	Tools    []openAIToolWire       `json:"tools,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// openAIToolWire is the OpenAI Chat Completions `tools[]` entry.
// The shape mirrors what pi (and other OpenAI SDKs) send on the
// wire so the JSON decoder can capture the caller's tool catalog
// verbatim.
type openAIToolWire struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	// Strict is the OpenAI "strict mode" flag the client adds to
	// function definitions. We pass it through to the upstream
	// without interpretation.
	Strict *bool `json:"strict,omitempty"`
}

// Anthropic Messages API types
//
// Pi (and other Anthropic SDKs) sends a superset of fields. The
// gateway forwards every field the upstream provider understands
// (model, max_tokens, temperature, top_p, top_k, stop_sequences,
// metadata, tool_choice, service_tier, thinking, system) and
// silently drops fields the upstream rejects (mcp_servers,
// context_management — those are passed through as JSON in
// ExtraFields so future upstreams can pick them up without us
// having to widen this struct every time Anthropic adds one).
type anthropicMessageRequest struct {
	Model        string                   `json:"model"`
	Messages     []anthropicMessage       `json:"messages"`
	System       interface{}              `json:"system,omitempty"`
	Tools        []anthropicTool          `json:"tools,omitempty"`
	MaxTokens    int                      `json:"max_tokens"`
	Stream       bool                     `json:"stream,omitempty"`
	Thinking     *anthropicThinkingConfig `json:"thinking,omitempty"`
	ExtraHeaders map[string]string        `json:"extra_headers,omitempty"`
	// Optional request knobs forwarded verbatim. Pi sets most of
	// these when the user picks a reasoning level / max_tokens
	// override. We forward the fields the upstream understands
	// (temperature, top_p, top_k, stop_sequences, metadata,
	// service_tier, tool_choice) and ignore the rest.
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	TopK          *int                 `json:"top_k,omitempty"`
	StopSequences []string             `json:"stop_sequences,omitempty"`
	Metadata      *anthropicMetadata   `json:"metadata,omitempty"`
	ServiceTier   string               `json:"service_tier,omitempty"`
	ToolChoice    *anthropicToolChoice `json:"tool_choice,omitempty"`
	// ExtraFields is a passthrough for any other top-level field
	// the Anthropic SDK might add (mcp_servers, context_management,
	// container, etc.). The conversion logic merges this map into
	// the wire payload so the upstream sees them verbatim, but the
	// gateway never crashes on unknown keys.
	ExtraFields map[string]interface{} `json:"-"`
}

// anthropicMetadata mirrors Anthropic's `metadata.user_id` shape.
// Pi's Anthropic SDK fills it when session affinity is enabled.
type anthropicMetadata struct {
	UserID string `json:"user_id,omitempty"`
}

// anthropicToolChoice mirrors Anthropic's `tool_choice` field,
// which can be a string ("auto" | "any" | "none") or an object
// ("{type: \"tool\", name: \"...\"}"). We capture both shapes.
type anthropicToolChoice struct {
	Type string `json:"type,omitempty"`
	Name string `json:"name,omitempty"`
}

type anthropicThinkingConfig struct {
	Type         string `json:"type,omitempty"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	// Text block
	Text string `json:"text,omitempty"`
	// Image block
	Source *anthropicImageSource `json:"source,omitempty"`
	// Tool use block
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
	// Tool result block. The Anthropic spec lets `content` be either a
	// string (simple text output) or an array of content blocks (text
	// + image, etc.). We capture it as interface{} and collapse to a
	// string at conversion time so the upstream provider (which only
	// accepts a string) sees the tool output verbatim.
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   interface{} `json:"content,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`
	// Thinking block fields. Anthropic's extended thinking returns
	// blocks of type "thinking" carrying the model's chain-of-thought
	// text plus an opaque `signature` the client must echo back on
	// the next turn. We forward both fields so multi-turn Pi sessions
	// keep their thinking context intact.
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// Cache control breakpoint. Pi attaches this to the last user
	// message and the system prompt to mark them as ephemeral-cache
	// breakpoints. We propagate it to the upstream so Anthropic-style
	// cache routing (when the upstream is `anthropic_native`) honours
	// it; compatible providers that don't understand the field see
	// it dropped at the wire-shaping layer.
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type,omitempty"`
	TTL  string `json:"ttl,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type anthropicResponse struct {
	ID           string                   `json:"id"`
	Type         string                   `json:"type"`
	Role         string                   `json:"role"`
	Content      []anthropicResponseBlock `json:"content"`
	Model        string                   `json:"model"`
	StopReason   string                   `json:"stop_reason,omitempty"`
	StopSequence *string                  `json:"stop_sequence,omitempty"`
	Usage        *anthropicUsage          `json:"usage,omitempty"`
}

type anthropicResponseBlock struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
	// Thinking block fields. Anthropic's extended thinking returns
	// blocks of type "thinking" carrying the model's chain-of-thought
	// text plus an opaque `signature` the client must echo back on
	// the next turn. We surface both fields in non-streaming responses
	// so Pi can keep its multi-turn reasoning context intact.
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// Data is the opaque payload of a `redacted_thinking` block.
	// Anthropic uses a separate shape (singular `data` field) for
	// the safety-filtered case; the client must echo it back on
	// the next turn the same way it echoes a `signature`. The
	// gateway forwards the bytes verbatim so the client can keep
	// the redacted context.
	Data string `json:"data,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type openAIChatMessage struct {
	Role string `json:"role"`
	// Content is the assistant's text delta for this chunk. We use
	// interface{} + omitempty so the JSON encoder can emit either a
	// string (text deltas) or omit the field entirely (role
	// announcement, tool_calls deltas). The OpenAI wire format
	// expects `content` to be absent on the role announcement chunk
	// and on tool_calls chunks; emitting `content: null` instead
	// confuses some SDKs (notably the strict-mode parsers in the
	// Codex CLI), which then leave the assistant's content block
	// empty even when subsequent text deltas arrive.
	Content interface{} `json:"content,omitempty"`
	// ToolCalls carries an OpenAI streaming tool_calls delta emitted
	// by streamUsageGatewayChatCompletions when the provider forwards
	// a tool_call_delta frame. The OpenAI SDK uses the per-chunk
	// `index` to dedupe tool calls across the stream, so we forward
	// the upstream's index verbatim rather than hardcoding 0 —
	// otherwise multiple tool calls in the same turn would all share
	// index 0 and overwrite each other in the SDK's accumulator.
	ToolCalls []openAIToolCallWire `json:"tool_calls,omitempty"`
	// ToolCallID is the OpenAI `role: "tool"` message's
	// `tool_call_id` field. It binds the tool result back to
	// the originating assistant tool_call so the model can
	// associate the two. Without it the upstream cannot match
	// the result to the call and silently drops the message
	// (this was the root cause of the "Pi agent hangs after
	// the first tool call" regression on the OpenAI →
	// anthropic_compatible cross-protocol path).
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name is the function name on a `role: "tool"` message.
	// The OpenAI wire format includes it for traceability;
	// the upstream protocol representation does not need it
	// (the tool_use_id is sufficient), so we capture it for
	// completeness even though we don't currently surface it
	// on the wire to the upstream.
	Name string `json:"name,omitempty"`
}

type openAIToolCallWire struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openAIChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []openAIChatChoice     `json:"choices"`
	Usage   map[string]interface{} `json:"usage,omitempty"`
}

type openAIChatChoice struct {
	Index        int                `json:"index"`
	Message      *openAIChatMessage `json:"message,omitempty"`
	Delta        *openAIChatMessage `json:"delta,omitempty"`
	FinishReason string             `json:"finish_reason,omitempty"`
}

type setSessionModelRequest struct {
	ProfileID       string `json:"profile_id"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type setSessionACPAgentModelRequest struct {
	Model string `json:"model"`
}

type forkSessionRequest struct {
	TurnID       string `json:"turn_id,omitempty"`
	MessageIndex *int   `json:"message_index,omitempty"`
	Title        string `json:"title,omitempty"`
}

type renameSessionRequest struct {
	Title string `json:"title"`
}

type installPackageRequest struct {
	Source string `json:"source"`
}

type removePackageRequest struct {
	Name string `json:"name"`
}

type packageSmokeRunRequest struct {
	SessionID string `json:"session_id,omitempty"`
}

type attachmentListResponse struct {
	Attachments []message.AttachmentRef `json:"attachments"`
}

type commandRequest struct {
	Command  string            `json:"command,omitempty"`
	Name     string            `json:"name,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type updateProjectLedgerRequest struct {
	Goal         string   `json:"goal,omitempty"`
	CurrentPhase string   `json:"current_phase,omitempty"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	Validation   []string `json:"validation,omitempty"`
	Decisions    []string `json:"decisions,omitempty"`
	Risks        []string `json:"risks,omitempty"`
	Blockers     []string `json:"blockers,omitempty"`
	NextSteps    []string `json:"next_steps,omitempty"`
}

type metaResponse struct {
	LeadName      string       `json:"lead_name"`
	Model         string       `json:"model"`
	WorkspaceDir  string       `json:"workspace_dir"`
	AuthRequired  bool         `json:"auth_required"`
	Version       version.Info `json:"version"`
	ExecutionMode string       `json:"execution_mode,omitempty"`
	SSHTarget     string       `json:"ssh_target,omitempty"`
	SSHWorkspace  string       `json:"ssh_workspace,omitempty"`
	SSHOptions    []string     `json:"ssh_options,omitempty"`
	DockerImage   string       `json:"docker_image,omitempty"`
	DockerNetwork string       `json:"docker_network,omitempty"`
	// VoiceEnabled 指示实时语音对话是否启用（media.audio.voice_enabled）。
	VoiceEnabled bool `json:"voice_enabled"`
}

type updateConfigRequest struct {
	Values       map[string]interface{} `json:"values,omitempty"`
	ClearSecrets []string               `json:"clear_secrets,omitempty"`
}

type revealSecretRequest struct {
	Path string `json:"path"`
}

type accountRequest struct {
	AccountID string `json:"account_id,omitempty"`
}

type skillLoadRequest struct {
	Name string `json:"name"`
}

type skillExpandRequest struct {
	Name     string   `json:"name"`
	Sections []string `json:"sections"`
}

type skillInstallRequest struct {
	Source string `json:"source"`
	Name   string `json:"name,omitempty"`
}

type permissionApproveRequest struct {
	Scope string `json:"scope,omitempty"`
}

type permissionDenyRequest struct {
	Reason string `json:"reason,omitempty"`
}

type longTaskNodeRequest struct {
	NodeID    string `json:"node_id,omitempty"`
	CancelAll bool   `json:"cancel_all,omitempty"`
}

type longTaskLookupRequest struct {
	Commit string `json:"commit,omitempty"`
}

type longTaskRollbackRequest struct {
	NodeID string `json:"node_id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type longTaskGCRequest struct {
	OlderThanSeconds int  `json:"older_than_seconds,omitempty"`
	Apply            bool `json:"apply,omitempty"`
}

type acceptMemoryCandidateRequest struct {
	AlwaysInclude bool `json:"always_include,omitempty"`
}

type rememberMemoryRequest struct {
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Content    string   `json:"content"`
	MemoryType string   `json:"memory_type"`
	Source     string   `json:"source,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

type updateMemoryRequest struct {
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Content    string   `json:"content"`
	MemoryType string   `json:"memory_type"`
	Source     string   `json:"source,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	MatchTitle string   `json:"match_title,omitempty"`
	MatchFile  string   `json:"match_file,omitempty"`
}

type forgetMemoryRequest struct {
	Title string `json:"title,omitempty"`
	File  string `json:"file,omitempty"`
}

type restoreMemoryAuditRequest struct {
	Target string `json:"target,omitempty"`
}

type removeMemorySuppressionRequest struct {
	Key string `json:"key"`
}

type saveNoteRequest struct {
	ID      string   `json:"id,omitempty"`
	Title   string   `json:"title"`
	Summary string   `json:"summary,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Content string   `json:"content"`
}

type statusProvider interface {
	StatusReport() rtchannels.StatusReport
}

type weixinAuthProvider interface {
	Status(context.Context, string) (weixin.WebAuthStatus, error)
	Start(context.Context, string) (weixin.WebAuthStatus, error)
	Logout(context.Context, string) (weixin.WebAuthStatus, error)
}

type cronAutomationProvider interface {
	ListJobs() ([]automation.CronJob, error)
	GetJob(string) (automation.CronJob, error)
	CreateJob(automation.CronCreateInput) (automation.CronJob, error)
	UpdateJob(automation.CronUpdateInput) (automation.CronJob, error)
	DeleteJob(string) error
	ToggleJob(string, bool) (automation.CronJob, error)
	RunNow(context.Context, string) (automation.CronRunLog, error)
	ListRunLogs(string, int) ([]automation.CronRunLog, error)
}

type heartbeatAutomationProvider interface {
	GetRule() (automation.HeartbeatRule, error)
	SetRule(automation.HeartbeatSetInput) (automation.HeartbeatRule, error)
	Toggle(bool) (automation.HeartbeatRule, error)
	TestNow(context.Context) (automation.HeartbeatRunLog, error)
	ListRunLogs(int) ([]automation.HeartbeatRunLog, error)
}

type serviceRuntimeProvider interface {
	Status(context.Context) (any, error)
	Restart(context.Context) error
}

type controlNodeRegistry interface {
	Register(context.Context, noderegistry.NodeInput) (noderegistry.NodeView, error)
	Heartbeat(context.Context, string, noderegistry.NodeInput) (noderegistry.NodeView, error)
	List(context.Context) ([]noderegistry.NodeView, error)
	Get(context.Context, string) (noderegistry.NodeView, error)
	SetCredentialHash(context.Context, string, string) error
	Delete(context.Context, string) (noderegistry.NodeView, error)
}

// nodeDisconnector is an optional capability on the control registry: when the
// registry is wired to the relay hub (center server), deleting a node also
// forcibly drops its live relay connection. Handlers detect it via type
// assertion so the handler signature stays stable.
type nodeDisconnector interface {
	DisconnectNode(nodeID string)
}

// nodeOverviewProvider supplies the aggregated observation view for one node.
// The relay.EventStore satisfies it; handlers detect it via type assertion on
// the control registry object so the handler signature stays stable.
type nodeOverviewProvider interface {
	Overview(string) (relay.NodeOverview, bool)
}
