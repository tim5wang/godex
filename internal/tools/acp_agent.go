package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	platformtooling "github.com/tim5wang/godex/internal/platform/tooling"
)

type acpAgentArgs struct {
	Action         string `json:"action,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Prompt         string `json:"prompt,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// NewACPAgentTool creates a small ACP stdio client for configured external agents.
func NewACPAgentTool(agents map[string]config.ACPAgentConfig, workspace string) Tool {
	runtime := cloneACPAgents(agents)
	pool := newACPToolSessionPool()
	return NewTypedTool(NewToolSpec("acp_agent", "Call an external Agent Client Protocol agent over stdio. Use action=list to inspect configured agents, or action=run with agent and prompt.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{"list", "run"},
			},
			"agent": map[string]string{
				"type":        "string",
				"description": "Configured ACP agent id",
			},
			"prompt": map[string]string{
				"type":        "string",
				"description": "Prompt to send with session/prompt",
			},
			"timeout_seconds": map[string]string{
				"type":        "integer",
				"description": "Optional idle-timeout override for this call (seconds without any agent output)",
			},
		},
	}, nil), func(ctx context.Context, args acpAgentArgs) (ToolResult, error) {
		action := strings.TrimSpace(args.Action)
		if action == "" {
			action = "run"
		}
		switch action {
		case "list":
			return ToolResult{Structured: formatACPAgents(runtime)}, nil
		case "run":
		default:
			return ToolResult{}, fmt.Errorf("unsupported acp_agent action %q", action)
		}
		id := strings.TrimSpace(args.Agent)
		if id == "" {
			return ToolResult{}, fmt.Errorf("missing agent argument")
		}
		agent, ok := runtime[id]
		if !ok {
			return ToolResult{}, fmt.Errorf("unknown ACP agent %q", id)
		}
		prompt := strings.TrimSpace(args.Prompt)
		if prompt == "" {
			return ToolResult{}, fmt.Errorf("missing prompt argument")
		}
		var result acpRunResult
		var err error
		if agent.ReuseToolSessions {
			result, err = pool.run(ctx, agent, workspace, prompt, args.TimeoutSeconds)
		} else {
			result, err = runACPAgent(ctx, agent, workspace, prompt, args.TimeoutSeconds, nil, "")
		}
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{
			Text:       result.Text,
			Structured: result,
			Metadata: map[string]interface{}{
				"agent":       id,
				"stop_reason": result.StopReason,
			},
		}, nil
	})
}

type acpToolSessionPool struct {
	mu      sync.Mutex
	entries map[string]*acpToolSessionEntry
}

type acpToolSessionEntry struct {
	mu        sync.Mutex
	sess      *ACPSession
	sessionID string
	timer     *time.Timer
}

func newACPToolSessionPool() *acpToolSessionPool {
	return &acpToolSessionPool{entries: map[string]*acpToolSessionEntry{}}
}

func (p *acpToolSessionPool) run(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int) (acpRunResult, error) {
	key := agent.ID + "\x00" + workspace
	p.mu.Lock()
	entry := p.entries[key]
	if entry == nil {
		entry = &acpToolSessionEntry{}
		p.entries[key] = entry
	}
	p.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.timer != nil {
		entry.timer.Stop()
		entry.timer = nil
	}
	if entry.sess != nil {
		select {
		case <-entry.sess.Done():
			_ = entry.sess.Close()
			entry.sess = nil
		default:
		}
	}
	if entry.sess == nil {
		sess, err := OpenACPSession(ctx, agent, workspace, agent.Model, agent.ReasoningEffort, timeoutSeconds, entry.sessionID)
		if err != nil {
			return acpRunResult{}, err
		}
		entry.sess = sess
		entry.sessionID = sess.SessionID()
	}
	entry.sess.SetPermissionHandler(func(_ context.Context, req ACPPermissionRequest) (ACPPermissionResponse, error) {
		return DenyACPPermissionRequest(req)
	})
	result, err := entry.sess.Prompt(ctx, prompt, nil)
	if result.SessionID != "" {
		entry.sessionID = result.SessionID
	}
	if err != nil {
		select {
		case <-entry.sess.Done():
			_ = entry.sess.Close()
			entry.sess = nil
		default:
		}
	}
	idleSeconds := timeoutSeconds
	if idleSeconds <= 0 {
		idleSeconds = agent.TimeoutSeconds
	}
	if idleSeconds <= 0 {
		idleSeconds = 600
	}
	entry.timer = time.AfterFunc(time.Duration(idleSeconds)*time.Second, func() {
		entry.mu.Lock()
		if entry.sess != nil {
			_ = entry.sess.Close()
			entry.sess = nil
		}
		entry.timer = nil
		entry.mu.Unlock()
	})
	return result, err
}

type acpRunResult struct {
	Agent      string   `json:"agent"`
	SessionID  string   `json:"session_id,omitempty"`
	StopReason string   `json:"stop_reason,omitempty"`
	Text       string   `json:"text,omitempty"`
	Updates    []string `json:"updates,omitempty"`
	// Usage is the per-turn token usage the agent reported in the
	// session/prompt result (optional; only agents that implement the ACP
	// usage reporting emit it).
	Usage *ACPTurnUsage `json:"usage,omitempty"`
	// SessionUsage is the latest context-window watermark (usage_update)
	// observed during this run (nil when the agent never sent one).
	SessionUsage *ACPSessionUsage `json:"session_usage,omitempty"`
}

// ACPRunResult is the exported result of one external ACP agent run, reused by
// the ACP harness (阶段 C: Pi/其他 ACP agent 的 Harness adapter).
type ACPRunResult = acpRunResult

// ACPPermissionRequest / ACPPermissionResponse alias the ACP SDK permission
// types so callers can plug decision policies without importing the SDK.
type ACPPermissionRequest = acp.RequestPermissionRequest
type ACPPermissionResponse = acp.RequestPermissionResponse

// ACPPermissionHandler decides how to answer one session/request_permission
// request from the external agent (M4 权限桥). It returns the chosen outcome
// or an error — an error is answered with a JSON-RPC error and usually aborts
// the agent's pending tool call.
type ACPPermissionHandler func(ctx context.Context, req ACPPermissionRequest) (ACPPermissionResponse, error)

// DenyACPPermissionRequest builds a deny decision for one permission request:
// it selects the agent's "reject once" option when present, falling back to
// "reject always". It errors when the agent offered no rejection option (the
// request is then answered with a JSON-RPC error, which is still a denial).
func DenyACPPermissionRequest(req ACPPermissionRequest) (ACPPermissionResponse, error) {
	var rejectAlways *acp.PermissionOption
	for i := range req.Options {
		opt := &req.Options[i]
		switch opt.Kind {
		case acp.PermissionOptionKindRejectOnce:
			return ACPPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(opt.OptionId)}, nil
		case acp.PermissionOptionKindRejectAlways:
			rejectAlways = opt
		}
	}
	if rejectAlways != nil {
		return ACPPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(rejectAlways.OptionId)}, nil
	}
	return ACPPermissionResponse{}, fmt.Errorf("godex ACP client: agent offered no rejection option for session/request_permission")
}

// SelectACPPermissionOption answers a permission request by selecting the
// named option (e.g. "allow_session" chosen by an approval UI / policy
// rules). It errors when the agent did not offer that option.
func SelectACPPermissionOption(req ACPPermissionRequest, optionID string) (ACPPermissionResponse, error) {
	for i := range req.Options {
		if string(req.Options[i].OptionId) == optionID {
			return ACPPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(req.Options[i].OptionId)}, nil
		}
	}
	return ACPPermissionResponse{}, fmt.Errorf("godex ACP client: option %q not offered for session/request_permission", optionID)
}

// ACPPermissionRequestToGodex maps an external agent permission request onto
// the host permission vocabulary without losing the original raw input.
func ACPPermissionRequestToGodex(agentID, sessionID, turnID string, req ACPPermissionRequest) PermissionRequest {
	title := strings.TrimSpace(string(req.ToolCall.ToolCallId))
	if req.ToolCall.Title != nil && strings.TrimSpace(*req.ToolCall.Title) != "" {
		title = strings.TrimSpace(*req.ToolCall.Title)
	}
	input := rawToMap(req.ToolCall.RawInput)
	paths := make([]string, 0, len(req.ToolCall.Locations))
	for _, location := range req.ToolCall.Locations {
		if path := strings.TrimSpace(location.Path); path != "" {
			paths = append(paths, path)
		}
	}
	action := "other"
	if req.ToolCall.Kind != nil && strings.TrimSpace(string(*req.ToolCall.Kind)) != "" {
		action = string(*req.ToolCall.Kind)
	}
	command := ""
	if value, ok := input["command"].(string); ok {
		command = strings.TrimSpace(value)
	}
	mutation := action != string(acp.ToolKindRead) && action != string(acp.ToolKindSearch) && action != string(acp.ToolKindThink)
	return PermissionRequest{
		SessionID: sessionID,
		TurnID:    turnID,
		Source:    "acp",
		Sender:    "acp:" + strings.TrimSpace(agentID),
		ToolName:  "acp:" + strings.TrimSpace(agentID) + ":" + title,
		Action:    action,
		Paths:     paths,
		Command:   command,
		Mutation:  mutation,
		Input:     input,
	}
}

// ACPPermissionResponseForDecision selects the closest option offered by the
// external agent for one host-side permission decision.
func ACPPermissionResponseForDecision(req ACPPermissionRequest, decision PermissionDecision, scope PermissionGrantScope) (ACPPermissionResponse, error) {
	wanted := acp.PermissionOptionKindRejectOnce
	if decision == PermissionAllow {
		wanted = acp.PermissionOptionKindAllowOnce
		if scope != "" && scope != PermissionGrantOnce {
			wanted = acp.PermissionOptionKindAllowAlways
		}
	} else if scope != "" && scope != PermissionGrantOnce {
		wanted = acp.PermissionOptionKindRejectAlways
	}
	for _, option := range req.Options {
		if option.Kind == wanted {
			return ACPPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(option.OptionId)}, nil
		}
	}
	if decision == PermissionAllow && wanted == acp.PermissionOptionKindAllowAlways {
		for _, option := range req.Options {
			if option.Kind == acp.PermissionOptionKindAllowOnce {
				return ACPPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(option.OptionId)}, nil
			}
		}
	}
	return DenyACPPermissionRequest(req)
}

// ACPCost carries the cumulative session cost optionally reported alongside a
// usage_update watermark. Amount is in ISO 4217 Currency units.
type ACPCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// ACPSessionUsage is the session context-window watermark an agent reports via
// usage_update notifications: tokens currently in context (Used) versus the
// total window Size, plus optional cumulative session cost. It lets a host
// render a "context bar" for an external engine the same way it does for the
// native one.
type ACPSessionUsage struct {
	Used int      `json:"used"`
	Size int      `json:"size"`
	Cost *ACPCost `json:"cost,omitempty"`
}

// ACPTurnUsage is the per-turn token usage an agent reports in the
// session/prompt result. Fields mirror the ACP protocol's Usage struct; cache
// and thought counts are optional and their exact semantics are agent
// specific (e.g. whether cachedRead is already included in inputTokens).
type ACPTurnUsage struct {
	InputTokens       int `json:"input_tokens,omitempty"`
	OutputTokens      int `json:"output_tokens,omitempty"`
	CachedReadTokens  int `json:"cached_read_tokens,omitempty"`
	CachedWriteTokens int `json:"cached_write_tokens,omitempty"`
	ThoughtTokens     int `json:"thought_tokens,omitempty"`
	TotalTokens       int `json:"total_tokens,omitempty"`
}

// acpMcpServers converts the agent's configured stdio MCP servers into ACP
// mcpServers entries passed with session/new and session/load. Stdio is the
// MCP transport every ACP agent MUST support, so no capability negotiation is
// needed; the agent connects to each listed server itself (M3a).
func acpMcpServers(cfg config.ACPAgentConfig) []acp.McpServer {
	out := make([]acp.McpServer, 0, len(cfg.McpServers))
	for _, server := range cfg.McpServers {
		command := strings.TrimSpace(server.Command)
		if command == "" {
			continue
		}
		name := strings.TrimSpace(server.Name)
		if name == "" {
			name = filepath.Base(command)
		}
		env := make([]acp.EnvVariable, 0, len(server.Env))
		for key, value := range server.Env {
			env = append(env, acp.EnvVariable{Name: key, Value: value})
		}
		args := append([]string{}, server.Args...)
		out = append(out, acp.McpServer{Stdio: &acp.McpServerStdio{
			Name:    name,
			Command: command,
			Args:    args,
			Env:     env,
		}})
	}
	return out
}

// acpTurnUsage converts an SDK prompt-response usage into the exported form.
// Every pointer field on the SDK type is optional; nil-safe.
func acpTurnUsage(u *acp.Usage) *ACPTurnUsage {
	if u == nil {
		return nil
	}
	out := &ACPTurnUsage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		TotalTokens:  u.TotalTokens,
	}
	if u.CachedReadTokens != nil {
		out.CachedReadTokens = *u.CachedReadTokens
	}
	if u.CachedWriteTokens != nil {
		out.CachedWriteTokens = *u.CachedWriteTokens
	}
	if u.ThoughtTokens != nil {
		out.ThoughtTokens = *u.ThoughtTokens
	}
	return out
}

// acpSessionUsage converts an SDK usage_update into the exported form.
func acpSessionUsage(u *acp.SessionUsageUpdate) *ACPSessionUsage {
	if u == nil {
		return nil
	}
	out := &ACPSessionUsage{Used: u.Used, Size: u.Size}
	if u.Cost != nil {
		out.Cost = &ACPCost{Amount: u.Cost.Amount, Currency: u.Cost.Currency}
	}
	return out
}

// ACPContentBlocksForMessage converts the content of one godex user message
// into ACP session/prompt content blocks (M2: 附件/多 content-block 支持).
// Text blocks always pass through; image blocks are included only when
// includeImages is true (i.e. the receiving agent advertised
// promptCapabilities.image during initialize). Anything else (tool results,
// thinking, …) is never part of a user-authored prompt and is skipped.
func ACPContentBlocksForMessage(msg protocol.Message, includeImages bool) []acp.ContentBlock {
	var out []acp.ContentBlock
	for _, block := range msg.Content {
		switch block.Type {
		case protocol.BlockText:
			if strings.TrimSpace(block.Text) != "" {
				out = append(out, acp.TextBlock(block.Text))
			}
		case protocol.BlockImage:
			if !includeImages {
				continue
			}
			if converted, ok := ACPImageContentBlock(block); ok {
				out = append(out, converted)
			}
		}
	}
	return out
}

// ACPImageContentBlock converts one godex protocol image block (base64 image
// source) into an ACP image prompt content block. ACP/MCP image content
// expects the raw base64 payload plus a separate mimeType, so a
// "data:<mime>;base64,<payload>" data URI is unwrapped when present.
func ACPImageContentBlock(block protocol.Block) (acp.ContentBlock, bool) {
	if block.Type != protocol.BlockImage || block.Source == nil {
		return acp.ContentBlock{}, false
	}
	data := block.Source.Data
	if lower := strings.ToLower(data); strings.HasPrefix(lower, "data:") {
		if i := strings.Index(lower, ";base64,"); i >= 0 {
			data = data[i+len(";base64,"):]
		}
	}
	mime := strings.TrimSpace(block.Source.MediaType)
	if mime == "" {
		mime = "image/png"
	}
	return acp.ImageBlock(data, mime), true
}

// ACPUpdate is one structured session/update event captured from the external
// engine, mapped onto GoDex events by the harness (P2 #4 unified event
// mapping). Kind is one of "plan", "tool_call", "tool_call_update",
// "thought_chunk", "message_chunk", or "usage_update".
type ACPUpdate struct {
	Kind       string         `json:"kind"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"toolCallId,omitempty"`
	Text       string         `json:"text,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	Raw        string         `json:"raw,omitempty"`
	// Plan is set when Kind == "plan": the agent's current execution plan
	// entries (complete list, client replaces the whole plan per update).
	// Kept structured so hosts can render the external engine's plan on the
	// same timeline as the native todo list instead of a generic warning.
	Plan []ACPPlanEntry `json:"plan,omitempty"`
	// SessionUsage is set when Kind == "usage_update": the agent's latest
	// context-window watermark for the session.
	SessionUsage *ACPSessionUsage `json:"sessionUsage,omitempty"`
}

// ACPPlanEntry mirrors one ACP plan entry (content, priority, status) in a
// host-facing form without importing the SDK.
type ACPPlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

// RunACPAgent runs one prompt against a configured ACP agent over stdio and
// returns the collected reply text plus session metadata. It is the exported
// form of the acp_agent tool's internal runner so engines can delegate turns.
func RunACPAgent(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int) (ACPRunResult, error) {
	return runACPAgent(ctx, agent, workspace, prompt, timeoutSeconds, nil, "")
}

// StreamACPAgent runs one prompt against a configured ACP agent, invoking
// onUpdate for every session/update event as it streams in (阶段 C streaming
// handle). The returned result still carries the full reply text and the
// structured update list.
func StreamACPAgent(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int, onUpdate func(ACPUpdate)) (ACPRunResult, error) {
	return runACPAgent(ctx, agent, workspace, prompt, timeoutSeconds, onUpdate, "")
}

// StreamACPAgentModel runs one prompt against a configured ACP agent with an
// optional model override applied through the standard session config
// mechanism (SetSessionConfigOption, config id "model").
func StreamACPAgentModel(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int, model string, onUpdate func(ACPUpdate)) (ACPRunResult, error) {
	return runACPAgent(ctx, agent, workspace, prompt, timeoutSeconds, onUpdate, model)
}

// acpSDKClient implements the acp.Client interface for the SDK's
// ClientSideConnection. It forwards every standard SessionUpdate event to the
// godex onUpdate callback (streaming) and accumulates the reply text.
type acpSDKClient struct {
	onUpdate func(ACPUpdate)
	// permissionHandler answers the agent's session/request_permission
	// requests (M4). Nil keeps the historical behaviour: every request is
	// answered with an error.
	permissionHandler ACPPermissionHandler

	// fsBridge, when non-nil, enables fs/read_text_file and fs/write_text_file
	// bridging: the external agent can ask godex (as host) to read/write text
	// files inside the workspace. Nil keeps the historical "not implemented"
	// behaviour (the agent sees fs capabilities as unsupported).
	fsBridge *acpFSBridge
	// terminalBridge, when non-nil, enables terminal/create, terminal/output,
	// terminal/wait_for_exit, terminal/kill and terminal/release bridging so
	// the external agent can run commands through the godex host. Nil keeps
	// the historical "not implemented" behaviour.
	terminalBridge *acpTerminalManager

	mu      sync.Mutex
	text    strings.Builder
	updates []string
	// lastSessionUsage is the most recent usage_update watermark the agent
	// reported for the current prompt run (reset at the start of each run so
	// a run never inherits a stale watermark from a previous turn).
	lastSessionUsage *ACPSessionUsage
	// toolNames maps a ToolCallId to the human-readable tool name announced
	// by the initial tool_call event, so a later tool_call_update that omits
	// the title keeps the real tool name instead of falling back to the
	// opaque call id (which the UI then shows as "call_xxx").
	toolNames map[string]string
	// toolInputs maps a ToolCallId to the raw input parameters announced by
	// the initial tool_call event. Some ACP agents (pi-acp, dsh) emit the
	// tool_call_update without re-sending rawInput, so the finished event
	// would otherwise lose the parameters and the UI could no longer expand
	// the tool call after it completes; the update falls back to this map.
	toolInputs map[string]map[string]any
}

var _ acp.Client = (*acpSDKClient)(nil)

func (c *acpSDKClient) addUpdate(u ACPUpdate) {
	c.mu.Lock()
	if u.Kind == "message_chunk" && u.Text != "" {
		c.text.WriteString(u.Text)
	}
	if u.Raw != "" {
		c.updates = append(c.updates, u.Raw)
	}
	c.mu.Unlock()
	if c.onUpdate != nil {
		c.onUpdate(u)
	}
}

// SessionUpdate is the SDK callback for every session/update notification the
// agent sends while processing a prompt.
func (c *acpSDKClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	u := params.Update
	raw, _ := json.Marshal(u)
	rawStr := string(raw)
	switch {
	case u.AgentMessageChunk != nil:
		text := ""
		if t := u.AgentMessageChunk.Content.Text; t != nil {
			text = t.Text
		}
		c.addUpdate(ACPUpdate{Kind: "message_chunk", Text: text, Raw: rawStr})
	case u.AgentThoughtChunk != nil:
		text := ""
		if t := u.AgentThoughtChunk.Content.Text; t != nil {
			text = t.Text
		}
		c.addUpdate(ACPUpdate{Kind: "thought_chunk", Text: text, Raw: rawStr})
	case u.ToolCall != nil:
		name := u.ToolCall.Title
		if name == "" {
			name = string(u.ToolCall.ToolCallId)
		}
		c.mu.Lock()
		if c.toolNames == nil {
			c.toolNames = map[string]string{}
		}
		if c.toolInputs == nil {
			c.toolInputs = map[string]map[string]any{}
		}
		callID := string(u.ToolCall.ToolCallId)
		c.toolNames[callID] = name
		if input := rawToMap(u.ToolCall.RawInput); input != nil {
			c.toolInputs[callID] = input
		}
		c.mu.Unlock()
		// pi-acp uses the full bash command as the tool title; shorten the
		// display name (the raw input keeps the full command, expandable in
		// the UI) so the running status bar / tool row stays single-line.
		name = shortenToolTitle(name)
		c.addUpdate(ACPUpdate{
			Kind:       "tool_call",
			Name:       name,
			ToolCallID: string(u.ToolCall.ToolCallId),
			Input:      rawToMap(u.ToolCall.RawInput),
			Raw:        rawStr,
		})
	case u.ToolCallUpdate != nil:
		// Prefer the update's title (the agent may refresh it as the call
		// progresses); fall back to the name recorded by the originating
		// tool_call so the event keeps the real tool name when the update
		// carries no title (dsh sends updates without one, which previously
		// made the UI display the opaque call id).
		name := ""
		if u.ToolCallUpdate.Title != nil {
			name = *u.ToolCallUpdate.Title
		}
		if name == "" {
			c.mu.Lock()
			if recorded, ok := c.toolNames[string(u.ToolCallUpdate.ToolCallId)]; ok {
				name = recorded
			}
			c.mu.Unlock()
		}
		if name == "" {
			name = string(u.ToolCallUpdate.ToolCallId)
		}
		// pi-acp uses the full bash command as the tool title, which would
		// otherwise make the feed row / status bar unboundedly tall. The full
		// title is preserved in the recorded input map (below), so shorten the
		// display name while keeping the parameters expandable.
		name = shortenToolTitle(name)
		// The update usually carries only the output (or nothing); fall back
		// to the parameters recorded by the originating tool_call so the
		// finished event keeps the input and the UI can still expand the row
		// after the tool completes.
		input := rawToMap(u.ToolCallUpdate.RawInput)
		if input == nil {
			c.mu.Lock()
			if recorded, ok := c.toolInputs[string(u.ToolCallUpdate.ToolCallId)]; ok {
				input = recorded
			}
			c.mu.Unlock()
		}
		c.addUpdate(ACPUpdate{
			Kind:       "tool_call_update",
			Name:       name,
			ToolCallID: string(u.ToolCallUpdate.ToolCallId),
			Input:      input,
			Raw:        rawStr,
		})
	case u.Plan != nil:
		entries := make([]ACPPlanEntry, 0, len(u.Plan.Entries))
		for _, e := range u.Plan.Entries {
			entries = append(entries, ACPPlanEntry{
				Content:  strings.TrimSpace(e.Content),
				Priority: string(e.Priority),
				Status:   string(e.Status),
			})
		}
		c.addUpdate(ACPUpdate{Kind: "plan", Plan: entries, Raw: rawStr})
	case u.UsageUpdate != nil:
		// Context-window watermark (used/size + optional cumulative cost).
		// Track the latest value on the client so the run result and the
		// persistent session can surface it after the turn completes.
		usage := acpSessionUsage(u.UsageUpdate)
		c.mu.Lock()
		c.lastSessionUsage = usage
		c.mu.Unlock()
		c.addUpdate(ACPUpdate{Kind: "usage_update", SessionUsage: usage, Raw: rawStr})
	}
	return nil
}

// resetSessionUsage clears the latest usage_update watermark; callers invoke
// it at the start of each prompt run so a run only carries its own data.
func (c *acpSDKClient) resetSessionUsage() {
	c.mu.Lock()
	c.lastSessionUsage = nil
	c.mu.Unlock()
}

// takeSessionUsage snapshots the latest usage_update watermark (nil if the
// agent has not sent one this run).
func (c *acpSDKClient) takeSessionUsage() *ACPSessionUsage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastSessionUsage == nil {
		return nil
	}
	out := *c.lastSessionUsage
	if c.lastSessionUsage.Cost != nil {
		cost := *c.lastSessionUsage.Cost
		out.Cost = &cost
	}
	return &out
}

func (c *acpSDKClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.mu.Lock()
	handler := c.permissionHandler
	c.mu.Unlock()
	if handler == nil {
		return acp.RequestPermissionResponse{}, fmt.Errorf("godex ACP client does not implement session/request_permission (no permission handler configured)")
	}
	return handler(ctx, params)
}

func (c *acpSDKClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	if c.fsBridge == nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("godex ACP client does not implement fs/read_text_file")
	}
	return c.fsBridge.ReadTextFile(ctx, params)
}

func (c *acpSDKClient) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	if c.fsBridge == nil {
		return acp.WriteTextFileResponse{}, fmt.Errorf("godex ACP client does not implement fs/write_text_file")
	}
	return c.fsBridge.WriteTextFile(ctx, params)
}

func (c *acpSDKClient) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	if c.terminalBridge == nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf("godex ACP client does not implement terminal/create")
	}
	return c.terminalBridge.CreateTerminal(ctx, params)
}

func (c *acpSDKClient) KillTerminal(ctx context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	if c.terminalBridge == nil {
		return acp.KillTerminalResponse{}, fmt.Errorf("godex ACP client does not implement terminal/kill")
	}
	return c.terminalBridge.KillTerminal(ctx, params)
}

func (c *acpSDKClient) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	if c.terminalBridge == nil {
		return acp.TerminalOutputResponse{}, fmt.Errorf("godex ACP client does not implement terminal/output")
	}
	return c.terminalBridge.TerminalOutput(ctx, params)
}

func (c *acpSDKClient) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	if c.terminalBridge == nil {
		return acp.ReleaseTerminalResponse{}, fmt.Errorf("godex ACP client does not implement terminal/release")
	}
	return c.terminalBridge.ReleaseTerminal(ctx, params)
}

func (c *acpSDKClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	if c.terminalBridge == nil {
		return acp.WaitForTerminalExitResponse{}, fmt.Errorf("godex ACP client does not implement terminal/wait_for_exit")
	}
	return c.terminalBridge.WaitForTerminalExit(ctx, params)
}

// rawToMap converts an arbitrary JSON value into a map for tool-call payloads.
// Object values pass through; string values are parsed as JSON (some agents
// serialize rawInput/rawOutput as a JSON string); anything else is nil (empty
// input).
func rawToMap(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(t), &m) == nil {
			return m
		}
	}
	return nil
}

// shortenToolTitle caps an ACP tool title so a single tool call cannot blow up
// the feed row / status bar. pi-acp uses the full bash command as the tool
// title ("cd repo && npm run build && ..."), which previously rendered as one
// unbounded status line. The full title stays available in the recorded input
// map (rawInput), so nothing is lost — only the display name is shortened.
func shortenToolTitle(title string) string {
	title = strings.TrimSpace(title)
	const maxToolTitleRunes = 48
	runes := []rune(title)
	if len(runes) <= maxToolTitleRunes {
		return title
	}
	return string(runes[:maxToolTitleRunes]) + "…"
}

// ACPModelOption is one selectable model value advertised by an ACP agent's
// session config (configOptions, id "model").
type ACPModelOption struct {
	Value string `json:"value"`
	Name  string `json:"name"`
}

// ACPReasoningEffortOption is one selectable reasoning-effort value advertised
// by an ACP agent's session config (configOptions, id "reasoning_effort").
// dsh advertises off/low/high/max with high as the default balance.
type ACPReasoningEffortOption struct {
	Value string `json:"value"`
	Name  string `json:"name"`
}

// ACPConfigOptions is the full set of selectable session config options an ACP
// agent advertises (models + reasoning effort). It backs the chat model/
// reasoning pickers for ACP template sessions.
type ACPConfigOptions struct {
	Models          []ACPModelOption          `json:"models"`
	ReasoningEffort []ACPReasoningEffortOption `json:"reasoning_efforts"`
}

// ResolveACPCommand resolves the configured agent command to an absolute path.
// ACP agents are often installed under user-local bin dirs (~/.local/bin,
// ~/bin, /opt/homebrew/bin) that a launchd-started host does not have on its
// PATH; without this, a service restart can fail to spawn the agent with
// "executable file not found in $PATH" until the shell environment happens to
// be re-imported (which is why the failure is intermittent).
func ResolveACPCommand(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("empty command")
	}
	if filepath.IsAbs(command) {
		if _, err := os.Stat(command); err == nil {
			return command, nil
		}
		return "", fmt.Errorf("command %q does not exist", command)
	}
	if resolved, err := exec.LookPath(command); err == nil {
		return resolved, nil
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	} {
		candidate := filepath.Join(dir, command)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("command %q not found in $PATH or common user bin dirs", command)
}

// DiscoverACPAgentModelOptions connects to a configured ACP agent, creates a
// throwaway session, reads the agent's configOptions (the standard mechanism
// agents use to advertise selectable models) and returns the model list. The
// process is torn down before returning.
func DiscoverACPAgentModelOptions(ctx context.Context, agent config.ACPAgentConfig, workspace string) ([]ACPModelOption, error) {
	opts, err := DiscoverACPAgentConfigOptions(ctx, agent, workspace)
	if err != nil {
		return nil, err
	}
	return opts.Models, nil
}

// DiscoverACPAgentConfigOptions connects to a configured ACP agent, creates a
// throwaway session, reads the agent's configOptions and returns every
// selectable session config option (models + reasoning effort). The process is
// torn down before returning.
func DiscoverACPAgentConfigOptions(ctx context.Context, agent config.ACPAgentConfig, workspace string) (*ACPConfigOptions, error) {
	if strings.TrimSpace(agent.Command) == "" {
		return nil, fmt.Errorf("ACP agent %q has no command", agent.ID)
	}
	command, err := ResolveACPCommand(agent.Command)
	if err != nil {
		return nil, fmt.Errorf("ACP agent %q: %w", agent.ID, err)
	}
	// ACP agents (dsh included) reject relative cwd on session/new; make sure
	// the workspace we hand to the subprocess is absolute.
	if !filepath.IsAbs(workspace) {
		abs, err := filepath.Abs(workspace)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace %q: %w", workspace, err)
		}
		workspace = abs
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, agent.Args...)
	if err := platformtooling.ConfigureCommandProcessGroup(cmd); err != nil {
		return nil, err
	}
	cmd.Dir = workspace
	cmd.Env = os.Environ()
	for key, value := range agent.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	stderrData := make(chan string, 1)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrData <- string(data)
	}()

	client := &acpSDKClient{}
	conn := acp.NewClientSideConnection(client, stdin, stdout)

	title := "GoDex"
	if _, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs:       acp.FileSystemCapabilities{},
			Terminal: false,
		},
		ClientInfo: &acp.Implementation{Name: "godex", Title: &title},
	}); err != nil {
		platformtooling.KillCommandProcessGroup(cmd)
		return nil, fmt.Errorf("acp initialize: %w", err)
	}
	newSess, err := conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        workspace,
		McpServers: acpMcpServers(agent),
	})
	if err != nil {
		platformtooling.KillCommandProcessGroup(cmd)
		return nil, fmt.Errorf("acp session/new: %w", err)
	}
	// Tear down the throwaway process: closing stdin signals EOF, and the
	// explicit cancel guarantees the process is killed even if it never
	// exits on EOF (a misbehaving agent must not hang the HTTP endpoint).
	_ = stdin.Close()
	cancel()
	_ = cmd.Wait()
	_ = <-stderrData

	var out ACPConfigOptions
	for _, opt := range newSess.ConfigOptions {
		sel := opt.Select
		if sel == nil {
			continue
		}
		// dsh (and other agents) may advertise grouped options:
		// [{group, name, options:[{value,name,description}]}], or a flat
		// ungrouped list. Handle both so the settings dropdown populates.
		switch sel.Id {
		case "model":
			out.Models = append(out.Models, parseACPSelectOptions(sel)...)
		case "reasoning_effort":
			out.ReasoningEffort = append(out.ReasoningEffort, parseACPReasoningEffortOptions(sel)...)
		}
	}
	return &out, nil
}

// parseACPSelectOptions flattens a configOptions select's ungrouped or grouped
// option lists into plain model options (value + display name).
func parseACPSelectOptions(sel *acp.SessionConfigOptionSelect) []ACPModelOption {
	var out []ACPModelOption
	if sel.Options.Ungrouped != nil {
		for _, option := range *sel.Options.Ungrouped {
			name := option.Name
			if name == "" {
				name = string(option.Value)
			}
			out = append(out, ACPModelOption{Value: string(option.Value), Name: name})
		}
	}
	if sel.Options.Grouped != nil {
		for _, group := range *sel.Options.Grouped {
			for _, option := range group.Options {
				name := option.Name
				if name == "" {
					name = string(option.Value)
				}
				out = append(out, ACPModelOption{Value: string(option.Value), Name: name})
			}
		}
	}
	return out
}

// parseACPReasoningEffortOptions flattens a configOptions select's ungrouped
// or grouped option lists into reasoning-effort options (value + display name).
func parseACPReasoningEffortOptions(sel *acp.SessionConfigOptionSelect) []ACPReasoningEffortOption {
	var out []ACPReasoningEffortOption
	if sel.Options.Ungrouped != nil {
		for _, option := range *sel.Options.Ungrouped {
			name := option.Name
			if name == "" {
				name = string(option.Value)
			}
			out = append(out, ACPReasoningEffortOption{Value: string(option.Value), Name: name})
		}
	}
	if sel.Options.Grouped != nil {
		for _, group := range *sel.Options.Grouped {
			for _, option := range group.Options {
				name := option.Name
				if name == "" {
					name = string(option.Value)
				}
				out = append(out, ACPReasoningEffortOption{Value: string(option.Value), Name: name})
			}
		}
	}
	return out
}

func runACPAgent(ctx context.Context, agent config.ACPAgentConfig, workspace, prompt string, timeoutSeconds int, onUpdate func(ACPUpdate), model string) (acpRunResult, error) {
	if strings.TrimSpace(agent.Command) == "" {
		return acpRunResult{}, fmt.Errorf("ACP agent %q has no command", agent.ID)
	}
	command, err := ResolveACPCommand(agent.Command)
	if err != nil {
		return acpRunResult{}, fmt.Errorf("ACP agent %q: %w", agent.ID, err)
	}
	// ACP agents (dsh included) reject relative cwd on session/new; make sure
	// the workspace we hand to the subprocess and session is absolute.
	if !filepath.IsAbs(workspace) {
		abs, err := filepath.Abs(workspace)
		if err != nil {
			return acpRunResult{}, fmt.Errorf("resolve workspace %q: %w", workspace, err)
		}
		workspace = abs
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = agent.TimeoutSeconds
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 600
	}
	// timeout_seconds is the idle timeout: the turn is aborted only when the
	// agent produces no output for that long. Active streaming resets the
	// timer (standard ACP semantics instead of a total-process cap).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, agent.Args...)
	if err := platformtooling.ConfigureCommandProcessGroup(cmd); err != nil {
		return acpRunResult{}, err
	}
	cmd.Dir = workspace
	cmd.Env = os.Environ()
	for key, value := range agent.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return acpRunResult{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return acpRunResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return acpRunResult{}, err
	}
	stderrData := make(chan string, 1)
	if err := cmd.Start(); err != nil {
		return acpRunResult{}, err
	}
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrData <- string(data)
	}()

	// Bridge fs and terminal requests back to the godex host so the external
	// agent can read/write workspace files and run commands through godex
	// instead of only its own sandbox. The terminal manager is torn down when
	// the run finishes so no bridged command outlives the turn.
	fsBridge, _ := newACPFSBridge(workspace)
	termManager := newACPTerminalManager(workspace)
	defer termManager.Close()
	client := &acpSDKClient{onUpdate: onUpdate, fsBridge: fsBridge, terminalBridge: termManager}
	conn := acp.NewClientSideConnection(client, stdin, stdout)

	// Idle watchdog: reset on every session/update event (via lastActivity),
	// abort the turn when the agent stays silent for timeout_seconds.
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, lastActivity.Load())) > time.Duration(timeoutSeconds)*time.Second {
					cancel()
					return
				}
			}
		}
	}()
	origOnUpdate := client.onUpdate
	client.onUpdate = func(u ACPUpdate) {
		lastActivity.Store(time.Now().UnixNano())
		if origOnUpdate != nil {
			origOnUpdate(u)
		}
	}

	title := "GoDex"
	initResp, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapabilities{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
			Terminal: true,
		},
		ClientInfo: &acp.Implementation{
			Name:  "godex",
			Title: &title,
		},
	})
	if err != nil {
		platformtooling.KillCommandProcessGroup(cmd)
		return acpRunResult{}, fmt.Errorf("acp initialize: %w", err)
	}
	_ = initResp

	newSess, err := conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        workspace,
		McpServers: acpMcpServers(agent),
	})
	if err != nil {
		platformtooling.KillCommandProcessGroup(cmd)
		return acpRunResult{}, fmt.Errorf("acp session/new: %w", err)
	}

	// Standard model selection: apply the override through the session config
	// option the agent advertised (config id "model").
	if model != "" {
		if _, err := conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{
				SessionId: newSess.SessionId,
				ConfigId:  "model",
				Value:     acp.SessionConfigValueId(model),
			},
		}); err != nil {
			platformtooling.KillCommandProcessGroup(cmd)
			return acpRunResult{}, fmt.Errorf("acp set model option %q: %w", model, err)
		}
	}

	promptResp, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: newSess.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock(prompt)},
	})
	if err != nil {
		platformtooling.KillCommandProcessGroup(cmd)
		return acpRunResult{}, fmt.Errorf("acp session/prompt: %w", err)
	}

	// Normal completion: stop the idle watchdog and tear down the process.
	// stdin EOF signals a graceful exit; the explicit cancel is the fallback
	// that kills the process via exec.CommandContext, so Wait cannot hang
	// even if the agent ignores the EOF (worst case today is blocking for
	// the full watchdog timeout).
	_ = stdin.Close()
	cancel()
	_ = cmd.Wait()
	<-watchdogDone
	stderrText := strings.TrimSpace(<-stderrData)

	client.mu.Lock()
	text := client.text.String()
	updates := append([]string{}, client.updates...)
	client.mu.Unlock()

	if stderrText != "" && strings.TrimSpace(text) == "" {
		updates = append(updates, "stderr: "+stderrText)
	}

	return acpRunResult{
		Agent:        agent.ID,
		SessionID:    string(newSess.SessionId),
		StopReason:   string(promptResp.StopReason),
		Text:         strings.TrimSpace(text),
		Updates:      updates,
		Usage:        acpTurnUsage(promptResp.Usage),
		SessionUsage: client.takeSessionUsage(),
	}, nil
}

// ACPSession is a persistent stdio connection to one ACP agent process with a
// live session id, so consecutive prompts continue the same external
// conversation instead of opening a fresh session per turn. When the
// underlying process dies (e.g. after a network drop), the caller can reopen
// with the recorded session id and the agent's session/load (or the unstable
// session/resume) restores the original conversation.
type ACPSession struct {
	agent     config.ACPAgentConfig
	workspace string
	model     string
	timeout   int
	// supportsImage records whether the agent advertised
	// promptCapabilities.image during initialize; the harness uses it to gate
	// image content blocks in prompts (M2).
	supportsImage bool

	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	conn       *acp.ClientSideConnection
	client     *acpSDKClient
	sessionID  acp.SessionId
	closed     bool
	stderr     strings.Builder
	procCancel context.CancelFunc // cancels the process lifetime context
}

// OpenACPSession spawns the agent process, initializes it, and creates a
// session. When resumeSessionID is non-empty the agent's session/load is tried
// first, then the unstable session/resume, falling back to a fresh session/new.
// Optional model and reasoningEffort overrides are applied through the
// standard session config options ("model" / "reasoning_effort") the agent
// advertises in configOptions.
func OpenACPSession(ctx context.Context, agent config.ACPAgentConfig, workspace, model, reasoningEffort string, timeoutSeconds int, resumeSessionID string) (*ACPSession, error) {
	if strings.TrimSpace(agent.Command) == "" {
		return nil, fmt.Errorf("ACP agent %q has no command", agent.ID)
	}
	// ACP agents (dsh included) reject relative cwd on session/new; make sure
	// the workspace we hand to the subprocess and session is absolute.
	if !filepath.IsAbs(workspace) {
		abs, err := filepath.Abs(workspace)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace %q: %w", workspace, err)
		}
		workspace = abs
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = agent.TimeoutSeconds
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 600
	}
	procCtx, cancelProc := context.WithCancel(context.Background())
	command, err := ResolveACPCommand(agent.Command)
	if err != nil {
		cancelProc()
		return nil, fmt.Errorf("ACP agent %q: %w", agent.ID, err)
	}
	cmd := exec.CommandContext(procCtx, command, agent.Args...)
	if err := platformtooling.ConfigureCommandProcessGroup(cmd); err != nil {
		cancelProc()
		return nil, err
	}
	cmd.Dir = workspace
	cmd.Env = os.Environ()
	for key, value := range agent.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancelProc()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancelProc()
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancelProc()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancelProc()
		return nil, err
	}
	s := &ACPSession{
		agent:      agent,
		workspace:  workspace,
		model:      model,
		timeout:    timeoutSeconds,
		cmd:        cmd,
		stdin:      stdin,
		client:     &acpSDKClient{},
		procCancel: cancelProc,
	}
	// Bridge fs and terminal requests back to the godex host so the external
	// engine (e.g. dsh as a whole-turn engine) can read/write workspace files
	// and run commands through godex instead of only its own sandbox.
	s.client.fsBridge, _ = newACPFSBridge(workspace)
	s.client.terminalBridge = newACPTerminalManager(workspace)
	go func() {
		data, _ := io.ReadAll(stderrPipe)
		s.mu.Lock()
		s.stderr.Write(data)
		s.mu.Unlock()
	}()
	s.conn = acp.NewClientSideConnection(s.client, stdin, stdout)

	title := "GoDex"
	initResp, err := s.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapabilities{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
			Terminal: true,
		},
		ClientInfo: &acp.Implementation{Name: "godex", Title: &title},
	})
	if err != nil {
		s.killProcess(cancelProc)
		return nil, fmt.Errorf("acp initialize: %w", err)
	}
	// Advertised prompt capabilities gate which content blocks we may send:
	// image/audio require an explicit capability, text/embedded context do not.
	s.supportsImage = initResp.AgentCapabilities.PromptCapabilities.Image
	openSession := func() (acp.SessionId, error) {
		newSess, err := s.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: workspace, McpServers: acpMcpServers(s.agent)})
		if err != nil {
			return "", err
		}
		return newSess.SessionId, nil
	}
	if resumeSessionID != "" {
		if _, err := s.conn.LoadSession(ctx, acp.LoadSessionRequest{SessionId: acp.SessionId(resumeSessionID), Cwd: workspace, McpServers: acpMcpServers(s.agent)}); err == nil {
			s.sessionID = acp.SessionId(resumeSessionID)
		} else if _, err := s.conn.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: acp.SessionId(resumeSessionID), Cwd: workspace, McpServers: acpMcpServers(s.agent)}); err == nil {
			s.sessionID = acp.SessionId(resumeSessionID)
		} else {
			id, err := openSession()
			if err != nil {
				s.killProcess(cancelProc)
				return nil, fmt.Errorf("acp session/new: %w", err)
			}
			s.sessionID = id
		}
	} else {
		id, err := openSession()
		if err != nil {
			s.killProcess(cancelProc)
			return nil, fmt.Errorf("acp session/new: %w", err)
		}
		s.sessionID = id
	}
	if model != "" {
		if _, err := s.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{
				SessionId: s.sessionID,
				ConfigId:  "model",
				Value:     acp.SessionConfigValueId(model),
			},
		}); err != nil {
			s.killProcess(cancelProc)
			return nil, fmt.Errorf("acp set model option %q: %w", model, err)
		}
	}
	if reasoningEffort != "" {
		if _, err := s.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{
				SessionId: s.sessionID,
				ConfigId:  "reasoning_effort",
				Value:     acp.SessionConfigValueId(reasoningEffort),
			},
		}); err != nil {
			s.killProcess(cancelProc)
			return nil, fmt.Errorf("acp set reasoning_effort option %q: %w", reasoningEffort, err)
		}
	}
	return s, nil
}

// SessionID returns the live session id (empty until the session is open).
func (s *ACPSession) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.sessionID)
}

// Done closes when the peer (agent process) disconnects. The session becomes
// unusable afterwards; callers reopen with SessionID to resume the conversation.
func (s *ACPSession) Done() <-chan struct{} { return s.conn.Done() }

// Prompt sends one text-only prompt on the live session and streams updates
// (convenience wrapper over PromptBlocks).
func (s *ACPSession) Prompt(ctx context.Context, prompt string, onUpdate func(ACPUpdate)) (acpRunResult, error) {
	if strings.TrimSpace(prompt) == "" {
		return acpRunResult{}, fmt.Errorf("empty ACP prompt")
	}
	return s.PromptBlocks(ctx, []acp.ContentBlock{acp.TextBlock(prompt)}, onUpdate)
}

// SupportsImage reports whether the agent advertised promptCapabilities.image
// during initialize (M2: only then may image content blocks be sent).
func (s *ACPSession) SupportsImage() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.supportsImage
}

// SetPermissionHandler installs the decision policy used to answer the
// agent's session/request_permission requests (M4 权限桥). Call it before the
// first prompt; a nil handler keeps the default behaviour (every request is
// answered with an error).
func (s *ACPSession) SetPermissionHandler(handler ACPPermissionHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return
	}
	s.client.mu.Lock()
	s.client.permissionHandler = handler
	s.client.mu.Unlock()
}

// PromptBlocks sends one prompt (arbitrary content blocks, e.g. text plus
// image attachments when the agent supports them) on the live session and
// streams updates. The idle watchdog aborts the prompt when the agent stays
// silent for timeout_seconds; the process and session are kept alive across
// prompts.
func (s *ACPSession) PromptBlocks(ctx context.Context, blocks []acp.ContentBlock, onUpdate func(ACPUpdate)) (acpRunResult, error) {
	if len(blocks) == 0 {
		return acpRunResult{}, fmt.Errorf("empty ACP prompt")
	}
	s.mu.Lock()
	if s.closed || s.conn == nil {
		s.mu.Unlock()
		return acpRunResult{}, fmt.Errorf("ACP session is closed")
	}
	conn := s.conn
	client := s.client
	sessionID := s.sessionID
	s.mu.Unlock()

	promptCtx, cancelPrompt := context.WithCancel(ctx)
	defer cancelPrompt()
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-promptCtx.Done():
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, lastActivity.Load())) > time.Duration(s.timeout)*time.Second {
					cancelPrompt()
					return
				}
			}
		}
	}()
	client.mu.Lock()
	client.text.Reset()
	client.updates = nil
	client.lastSessionUsage = nil
	client.onUpdate = func(u ACPUpdate) {
		lastActivity.Store(time.Now().UnixNano())
		if onUpdate != nil {
			onUpdate(u)
		}
	}
	client.mu.Unlock()
	defer func() {
		client.mu.Lock()
		client.onUpdate = nil
		client.mu.Unlock()
	}()

	promptResp, err := conn.Prompt(promptCtx, acp.PromptRequest{SessionId: sessionID, Prompt: blocks})
	// Stop the idle watchdog before waiting for it to wind down (it only exits
	// when promptCtx is cancelled; the deferred cancel runs at return, which
	// would deadlock the wait below).
	cancelPrompt()
	<-watchdogDone
	if err != nil {
		// Surface any stderr the agent produced while failing so the caller
		// can diagnose crashes / init errors without extra plumbing.
		s.mu.Lock()
		stderrText := strings.TrimSpace(s.stderr.String())
		s.mu.Unlock()
		if stderrText != "" {
			return acpRunResult{}, fmt.Errorf("acp session/prompt: %w (stderr: %s)", err, stderrText)
		}
		return acpRunResult{}, fmt.Errorf("acp session/prompt: %w", err)
	}
	client.mu.Lock()
	text := client.text.String()
	updates := append([]string{}, client.updates...)
	client.mu.Unlock()
	return acpRunResult{
		Agent:        s.agent.ID,
		SessionID:    string(sessionID),
		StopReason:   string(promptResp.StopReason),
		Text:         strings.TrimSpace(text),
		Updates:      updates,
		Usage:        acpTurnUsage(promptResp.Usage),
		SessionUsage: client.takeSessionUsage(),
	}, nil
}

// SessionUsage returns the most recent usage_update context-window watermark
// reported by the agent, or nil if the agent has not sent one yet. Each Prompt
// call resets the tracked watermark, so this always reflects the latest run.
func (s *ACPSession) SessionUsage() *ACPSessionUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client.takeSessionUsage()
}

// Close kills the agent process and releases the stdio pipes.
func (s *ACPSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cmd := s.cmd
	stdin := s.stdin
	cancel := s.procCancel
	client := s.client
	s.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		platformtooling.KillCommandProcessGroup(cmd)
		_ = cmd.Wait()
	}
	if client != nil && client.terminalBridge != nil {
		client.terminalBridge.Close()
	}
	return nil
}

func (s *ACPSession) killProcess(cancelProc context.CancelFunc) {
	cancelProc()
	if s.cmd != nil && s.cmd.Process != nil {
		platformtooling.KillCommandProcessGroup(s.cmd)
		_ = s.cmd.Wait()
	}
	if s.client != nil && s.client.terminalBridge != nil {
		s.client.terminalBridge.Close()
	}
}

func cloneACPAgents(agents map[string]config.ACPAgentConfig) map[string]config.ACPAgentConfig {
	out := make(map[string]config.ACPAgentConfig, len(agents))
	for id, agent := range agents {
		if len(agent.Args) > 0 {
			agent.Args = append([]string{}, agent.Args...)
		}
		if len(agent.Env) > 0 {
			env := make(map[string]string, len(agent.Env))
			for key, value := range agent.Env {
				env[key] = value
			}
			agent.Env = env
		}
		if len(agent.McpServers) > 0 {
			servers := make([]config.ACPMcpServer, len(agent.McpServers))
			for index, server := range agent.McpServers {
				server.Args = append([]string{}, server.Args...)
				if len(server.Env) > 0 {
					env := make(map[string]string, len(server.Env))
					for key, value := range server.Env {
						env[key] = value
					}
					server.Env = env
				}
				servers[index] = server
			}
			agent.McpServers = servers
		}
		out[id] = agent
	}
	return out
}

func formatACPAgents(agents map[string]config.ACPAgentConfig) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(agents))
	for id, agent := range agents {
		out = append(out, map[string]interface{}{
			"id":              id,
			"command":         agent.Command,
			"args":            append([]string{}, agent.Args...),
			"description":     agent.Description,
			"timeout_seconds": agent.TimeoutSeconds,
		})
	}
	return out
}
