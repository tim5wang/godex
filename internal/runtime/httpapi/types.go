package httpapi

import (
	"context"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/message"
	rtchannels "github.com/tim5wang/godex/internal/runtime/channels"
	"github.com/tim5wang/godex/internal/runtime/channels/weixin"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/version"
)

type openSessionRequest struct {
	Locator backend.SessionLocator `json:"locator"`
}

type submitMessageRequest struct {
	Envelope  message.Envelope `json:"envelope"`
	Text      string           `json:"text,omitempty"`
	Sender    string           `json:"sender,omitempty"`
	QueueMode string           `json:"queue_mode,omitempty"`
}

type openAIChatCompletionRequest struct {
	Model     string                 `json:"model,omitempty"`
	Messages  []openAIChatMessage    `json:"messages"`
	Stream    bool                   `json:"stream,omitempty"`
	MaxTokens int                    `json:"max_tokens,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// Anthropic Messages API types
type anthropicMessageRequest struct {
	Model            string                   `json:"model"`
	Messages         []anthropicMessage       `json:"messages"`
	System           interface{}              `json:"system,omitempty"`
	Tools           []anthropicTool          `json:"tools,omitempty"`
	MaxTokens       int                      `json:"max_tokens"`
	Stream          bool                     `json:"stream,omitempty"`
	Thinking        *anthropicThinkingConfig  `json:"thinking,omitempty"`
	ExtraHeaders    map[string]string        `json:"extra_headers,omitempty"`
}

type anthropicThinkingConfig struct {
	Type      string `json:"type,omitempty"`
	BudgetTokens int `json:"budget_tokens,omitempty"`
}

type anthropicMessage struct {
	Role    string                    `json:"role"`
	Content []anthropicContentBlock   `json:"content"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	// Text block
	Text string `json:"text,omitempty"`
	// Image block
	Source *anthropicImageSource `json:"source,omitempty"`
	// Tool use block
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
	// Tool result block
	ToolUseID string `json:"tool_use_id,omitempty"`
}

type anthropicImageSource struct {
	Type       string `json:"type"`
	MediaType  string `json:"media_type,omitempty"`
	Data       string `json:"data,omitempty"`
}

type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type anthropicResponse struct {
	ID        string                  `json:"id"`
	Type      string                  `json:"type"`
	Role      string                  `json:"role"`
	Content   []anthropicResponseBlock `json:"content"`
	Model     string                  `json:"model"`
	StopReason string                 `json:"stop_reason,omitempty"`
	StopSequence *string              `json:"stop_sequence,omitempty"`
	Usage     *anthropicUsage         `json:"usage,omitempty"`
}

type anthropicResponseBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens            int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens    int `json:"cache_read_input_tokens,omitempty"`
}

type openAIChatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
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

type forkSessionRequest struct {
	TurnID       string `json:"turn_id,omitempty"`
	MessageIndex *int   `json:"message_index,omitempty"`
	Title        string `json:"title,omitempty"`
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
	LeadName     string       `json:"lead_name"`
	Model        string       `json:"model"`
	WorkspaceDir string       `json:"workspace_dir"`
	AuthRequired bool         `json:"auth_required"`
	Version      version.Info `json:"version"`
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
	NodeID string `json:"node_id,omitempty"`
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
}
