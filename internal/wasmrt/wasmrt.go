// Package wasmrt is the wazero-based WASM tool executor (research doc 阶段 B /
// P4 MVP). It runs Go/Rust/TinyGo-compiled WASM plugins inside the GoDex
// process with a versioned JSON ABI and a small set of explicit, controlled
// host calls (log, KV, workspace read). Full WASI filesystem/network/shell and
// env access are intentionally NOT exposed in the MVP.
//
// ABI (godex:plugin@0.1), all functions use i32 linear-memory pointers and
// NUL-terminated JSON strings:
//
//	godex_abi_version() -> ptr            ABI version string
//	godex_tools_list() -> ptr             JSON: {"tools":[ToolDecl...]}
//	godex_request_buffer() -> ptr         stable request mailbox (>=64KiB)
//	godex_invoke() -> ptr                 JSON response to the mailbox request
//
// The host writes the JSON request into the mailbox, then calls godex_invoke;
// the plugin replies with a pointer to a NUL-terminated JSON response. No
// per-call guest allocation is needed, so repeated calls do not leak memory.
package wasmrt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// ABI version implemented by this host and expected from plugins.
const (
	// ABIVersion is the versioned JSON ABI contract.
	ABIVersion = "godex:plugin@0.1"
	// MaxMemoryPages caps guest linear memory (default 32 MiB). Go-compiled
	// plugins need ~2 MiB minimum plus heap headroom, so the default is
	// generous; TinyGo plugins are much smaller.
	MaxMemoryPages = 512
	// DefaultCallTimeout bounds a single tool call.
	DefaultCallTimeout = 30 * time.Second
	// MailboxSize is the guest request buffer the host writes into.
	MailboxSize = 64 * 1024
	// MaxResponseSize bounds the JSON response read from the guest.
	MaxResponseSize = 4 * 1024 * 1024
)

// ToolDecl is the tools_list entry a plugin declares.
type ToolDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// PromptSection is one context/prompt contribution a plugin declares
// (P4 prompt/context contributor). Key is a stable identifier; Kind is one of
// "background" (system-prompt section) or "memory" (ephemeral message); Text
// is the contribution itself.
type PromptSection struct {
	Key  string `json:"key"`
	Kind string `json:"kind,omitempty"`
	Text string `json:"text"`
}

// PolicyRequest is the JSON sent to godex_policy for one tool call.
type PolicyRequest struct {
	Action string         `json:"action"` // "before" | "after"
	Tool   string         `json:"tool"`
	Input  map[string]any `json:"input,omitempty"`
	Result any            `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// PolicyDecision is the explicit decision a plugin returns (research doc §4:
// explicit decisions instead of a waterfall next() chain).
type PolicyDecision struct {
	// Action is one of "continue", "deny", or "replace".
	Action string `json:"action"`
	// Error carries the deny reason (code/message).
	Error *PolicyError `json:"error,omitempty"`
	// Result replaces the tool result for action "replace".
	Result any `json:"result,omitempty"`
}

// PolicyError is a structured deny reason.
type PolicyError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// PolicyDecision constants.
const (
	PolicyContinue = "continue"
	PolicyDeny     = "deny"
	PolicyReplace  = "replace"
)

// HostCallbacks are the controlled host functions exposed to plugins.
type HostCallbacks struct {
	// Log receives plugin log lines (best-effort).
	Log func(message string)
	// KVGet reads one plugin KV entry ("" when missing).
	KVGet func(key string) string
	// KVSet stores one plugin KV entry.
	KVSet func(key, value string)
	// WorkspaceRead reads a workspace file by relative path; returns "" and
	// err for missing/escaping paths.
	WorkspaceRead func(relPath string) (string, error)
	// HTTPGet performs a controlled HTTP GET through the host's policy engine
	// (allow/deny domains, timeout, max chars). Returns body text and err when
	// denied or failed. When nil, godex_http_get returns an error.
	HTTPGet func(ctx context.Context, rawURL string) (string, error)
	// CredentialGet returns a named secret for the current plugin, or an error
	// when the plugin is not authorized or the secret is unset (阶段 C
	// credential broker). The plugin id comes from Config.PluginID, so this
	// callback is already bound to the plugin. When nil, godex_credential_get
	// returns an error.
	CredentialGet func(name string) (string, error)
}

// Config controls one WASM plugin execution environment.
type Config struct {
	// Binary is the compiled .wasm module contents.
	Binary []byte
	// PluginID is the owning plugin id (used for namespaced host calls like
	// credential access; empty when unknown).
	PluginID string
	// Host are the controlled host callbacks (may be nil for no-op).
	Host HostCallbacks
	// CallTimeout bounds each tool call; zero uses DefaultCallTimeout.
	CallTimeout time.Duration
	// MaxMemoryPages caps guest memory; zero uses MaxMemoryPages.
	MaxMemoryPages uint32
	// MaxConcurrent bounds concurrent guest calls per runtime (default 4).
	MaxConcurrent int
}

// Plugin is one loaded WASM plugin runtime.
type Plugin struct {
	config          Config
	runtime         wazero.Runtime
	module          api.Module
	exportInvoke    api.Function
	exportToolsList api.Function
	exportABI       api.Function
	exportPrompts   api.Function
	exportPolicy    api.Function
	mailboxPtr      uint32
	mailboxOnce     sync.Once
	// sem bounds the number of callers queued on this plugin. The guest call
	// itself is serialized by callMu: Go wasm guests are single-threaded, so
	// concurrent guest execution would corrupt the guest runtime.
	sem       chan struct{}
	callMu    sync.Mutex
	closeOnce sync.Once
}

// request is the JSON envelope sent to godex_invoke.
type request struct {
	Action    string         `json:"action"`
	Tool      string         `json:"tool,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// response is the JSON envelope returned from godex_invoke.
type response struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Result any    `json:"result,omitempty"`
}

// NewPlugin compiles and instantiates the WASM module.
func NewPlugin(ctx context.Context, config Config) (*Plugin, error) {
	if len(config.Binary) == 0 {
		return nil, fmt.Errorf("wasm plugin: empty binary")
	}
	if config.CallTimeout <= 0 {
		config.CallTimeout = DefaultCallTimeout
	}
	pages := config.MaxMemoryPages
	if pages == 0 {
		pages = MaxMemoryPages
	}
	concurrency := config.MaxConcurrent
	if concurrency <= 0 {
		concurrency = 4
	}

	plugin := &Plugin{
		config: config,
		sem:    make(chan struct{}, concurrency),
	}

	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(pages))
	plugin.runtime = runtime
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("wasm plugin: instantiate wasi: %w", err)
	}
	if err := instantiateHostModule(ctx, runtime, config.Host); err != nil {
		_ = runtime.Close(ctx)
		return nil, err
	}

	moduleConfig := wazero.NewModuleConfig().
		WithStartFunctions("_initialize").
		WithSysWalltime().
		WithSysNanosleep()
	module, err := runtime.InstantiateWithConfig(ctx, config.Binary, moduleConfig)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("wasm plugin: instantiate module: %w", err)
	}
	plugin.module = module
	plugin.exportInvoke = module.ExportedFunction("godex_invoke")
	plugin.exportToolsList = module.ExportedFunction("godex_tools_list")
	plugin.exportABI = module.ExportedFunction("godex_abi_version")
	plugin.exportPrompts = module.ExportedFunction("godex_prompts_list")
	plugin.exportPolicy = module.ExportedFunction("godex_policy")
	if plugin.exportInvoke == nil || module.ExportedFunction("godex_request_buffer") == nil {
		_ = plugin.Close(ctx)
		return nil, fmt.Errorf("wasm plugin: missing godex_invoke/godex_request_buffer exports (ABI %s)", ABIVersion)
	}
	return plugin, nil
}

// instantiateHostModule wires the controlled host module "godex:host". Host
// functions receive the calling module via api.Module, so the guest module may
// be instantiated after the host module.
func instantiateHostModule(ctx context.Context, runtime wazero.Runtime, host HostCallbacks) error {
	builder := runtime.NewHostModuleBuilder("godex:host")

	readString := func(mod api.Module, ptr, length uint32) string {
		data, ok := mod.Memory().Read(ptr, length)
		if !ok {
			return ""
		}
		return string(data)
	}

	// godex_log(ptr, len)
	builder.NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, ptr, length uint32) {
		if host.Log != nil {
			host.Log(readString(mod, ptr, length))
		}
	}).Export("godex_log")

	// godex_kv_get(keyPtr, keyLen, outPtr, outLen) -> written bytes
	builder.NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, keyPtr, keyLen, outPtr, outLen uint32) uint32 {
		key := readString(mod, keyPtr, keyLen)
		value := ""
		if host.KVGet != nil {
			value = host.KVGet(key)
		}
		return writeString(mod, outPtr, outLen, value)
	}).Export("godex_kv_get")

	// godex_kv_set(keyPtr, keyLen, valPtr, valLen)
	builder.NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, keyPtr, keyLen, valPtr, valLen uint32) {
		if host.KVSet != nil {
			host.KVSet(readString(mod, keyPtr, keyLen), readString(mod, valPtr, valLen))
		}
	}).Export("godex_kv_set")

	// godex_workspace_read(relPtr, relLen, outPtr, outLen) -> written bytes
	builder.NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, relPtr, relLen, outPtr, outLen uint32) uint32 {
		rel := readString(mod, relPtr, relLen)
		value := ""
		if host.WorkspaceRead != nil {
			if data, err := host.WorkspaceRead(rel); err == nil {
				value = data
			}
		}
		return writeString(mod, outPtr, outLen, value)
	}).Export("godex_workspace_read")

	// godex_http_get(urlPtr, urlLen, outPtr, outLen) -> status (0 ok, 1 denied,
	// 2 error); body (up to outLen) is written to outPtr. Controlled by the
	// host's HTTP policy; plugins cannot bypass it.
	builder.NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, urlPtr, urlLen, outPtr, outLen uint32) uint32 {
		if host.HTTPGet == nil {
			return 2
		}
		rawURL := readString(mod, urlPtr, urlLen)
		body, err := host.HTTPGet(ctx, rawURL)
		if err != nil {
			return 2
		}
		if writeString(mod, outPtr, outLen, body) == 0 && body != "" {
			return 2
		}
		return 0
	}).Export("godex_http_get")

	// godex_credential_get(namePtr, nameLen, outPtr, outLen) -> status
	// (0 ok, 1 not allowed, 2 not set/error). The credential broker resolves
	// the plugin id (from Config.PluginID); plugins can only read secrets their
	// manifest authorized.
	builder.NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, namePtr, nameLen, outPtr, outLen uint32) uint32 {
		if host.CredentialGet == nil {
			return 2
		}
		name := readString(mod, namePtr, nameLen)
		value, err := host.CredentialGet(name)
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "not allowed") {
				return 1
			}
			return 2
		}
		if writeString(mod, outPtr, outLen, value) == 0 && value != "" {
			return 2
		}
		return 0
	}).Export("godex_credential_get")

	_, err := builder.Instantiate(ctx)
	return err
}

// writeString copies value into guest memory bounded by outLen, returning the
// number of bytes written (0 when the buffer is too small).
func writeString(mod api.Module, outPtr, outLen uint32, value string) uint32 {
	if outLen == 0 {
		return 0
	}
	n := uint32(len(value))
	if n > outLen {
		n = outLen
	}
	if !mod.Memory().Write(outPtr, []byte(value[:n])) {
		return 0
	}
	return n
}

// mailbox returns the guest request buffer pointer (cached).
func (p *Plugin) mailbox(ctx context.Context) (uint32, error) {
	var err error
	p.mailboxOnce.Do(func() {
		fn := p.module.ExportedFunction("godex_request_buffer")
		if fn == nil {
			err = fmt.Errorf("wasm plugin: missing godex_request_buffer export")
			return
		}
		results, callErr := fn.Call(ctx)
		if callErr != nil {
			err = callErr
			return
		}
		p.mailboxPtr = uint32(results[0])
	})
	return p.mailboxPtr, err
}

// ToolsList returns the tool declarations from the plugin.
func (p *Plugin) ToolsList(ctx context.Context) ([]ToolDecl, error) {
	p.callMu.Lock()
	defer p.callMu.Unlock()
	if p.exportToolsList == nil {
		return nil, fmt.Errorf("wasm plugin: missing godex_tools_list export")
	}
	results, err := p.exportToolsList.Call(ctx)
	if err != nil {
		return nil, fmt.Errorf("wasm plugin: tools_list: %w", err)
	}
	raw := p.readString(uint32(results[0]))
	if raw == "" {
		return nil, fmt.Errorf("wasm plugin: empty tools_list")
	}
	var list struct {
		Tools []ToolDecl `json:"tools"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("wasm plugin: tools_list: %w", err)
	}
	return list.Tools, nil
}

// ABI returns the plugin's declared ABI version.
func (p *Plugin) ABI(ctx context.Context) (string, error) {
	p.callMu.Lock()
	defer p.callMu.Unlock()
	if p.exportABI == nil {
		return "", nil
	}
	results, err := p.exportABI.Call(ctx)
	if err != nil {
		return "", err
	}
	return p.readString(uint32(results[0])), nil
}

// PromptSections returns the context/prompt contributions from the plugin via
// the godex_prompts_list export (P4 prompt/context contributor). A plugin that
// does not export godex_prompts_list contributes nothing.
func (p *Plugin) PromptSections(ctx context.Context) ([]PromptSection, error) {
	p.callMu.Lock()
	defer p.callMu.Unlock()
	if p.exportPrompts == nil {
		return nil, nil
	}
	results, err := p.exportPrompts.Call(ctx)
	if err != nil {
		return nil, fmt.Errorf("wasm plugin: prompts_list: %w", err)
	}
	raw := p.readString(uint32(results[0]))
	if raw == "" {
		return nil, nil
	}
	var list struct {
		Sections []PromptSection `json:"sections"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("wasm plugin: prompts_list: %w", err)
	}
	out := make([]PromptSection, 0, len(list.Sections))
	for _, section := range list.Sections {
		section.Key = strings.TrimSpace(section.Key)
		section.Text = strings.TrimSpace(section.Text)
		if section.Key == "" || section.Text == "" {
			continue
		}
		if section.Kind != "memory" {
			section.Kind = "background"
		}
		out = append(out, section)
	}
	return out, nil
}

// HasPolicy reports whether the plugin exports godex_policy.
func (p *Plugin) HasPolicy() bool { return p.exportPolicy != nil }

// PolicyCheck sends one before/after decision request to the plugin's
// godex_policy export. A plugin without the export always continues.
func (p *Plugin) PolicyCheck(ctx context.Context, req PolicyRequest) (PolicyDecision, error) {
	if p.exportPolicy == nil {
		return PolicyDecision{Action: PolicyContinue}, nil
	}
	p.callMu.Lock()
	defer p.callMu.Unlock()
	payload, err := json.Marshal(req)
	if err != nil {
		return PolicyDecision{}, err
	}
	mailbox, err := p.mailbox(ctx)
	if err != nil {
		return PolicyDecision{}, err
	}
	if uint32(len(payload))+1 > MailboxSize {
		return PolicyDecision{}, fmt.Errorf("wasm plugin: policy request too large")
	}
	if !p.module.Memory().Write(mailbox, append(payload, 0)) {
		return PolicyDecision{}, fmt.Errorf("wasm plugin: write policy mailbox")
	}
	results, err := p.exportPolicy.Call(ctx)
	if err != nil {
		return PolicyDecision{}, fmt.Errorf("wasm plugin: policy: %w", err)
	}
	raw := p.readString(uint32(results[0]))
	var decision PolicyDecision
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return PolicyDecision{}, fmt.Errorf("wasm plugin: malformed policy response: %w", err)
	}
	switch decision.Action {
	case "", PolicyContinue:
		decision.Action = PolicyContinue
	case PolicyDeny, PolicyReplace:
	default:
		return PolicyDecision{}, fmt.Errorf("wasm plugin: unknown policy action %q", decision.Action)
	}
	return decision, nil
}

// CallTool runs one tool call through the plugin.
func (p *Plugin) CallTool(ctx context.Context, name string, arguments map[string]any) (any, error) {	ctx, cancel := context.WithTimeout(ctx, p.config.CallTimeout)
	defer cancel()
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	p.callMu.Lock()
	defer p.callMu.Unlock()

	req := request{Action: "tool_call", Tool: name, Arguments: arguments}
	resp, err := p.invoke(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("wasm tool %s: %s", name, resp.Error)
	}
	return resp.Result, nil
}

// invoke writes the JSON request to the mailbox and reads the response.
func (p *Plugin) invoke(ctx context.Context, req request) (response, error) {
	mailbox, err := p.mailbox(ctx)
	if err != nil {
		return response{}, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return response{}, err
	}
	if uint32(len(payload))+1 > MailboxSize {
		return response{}, fmt.Errorf("wasm plugin: request too large (%d bytes)", len(payload))
	}
	if !p.module.Memory().Write(mailbox, append(payload, 0)) {
		return response{}, fmt.Errorf("wasm plugin: write request mailbox")
	}
	results, err := p.exportInvoke.Call(ctx)
	if err != nil {
		return response{}, fmt.Errorf("wasm plugin: invoke: %w", err)
	}
	raw := p.readString(uint32(results[0]))
	var resp response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return response{}, fmt.Errorf("wasm plugin: malformed invoke response: %w", err)
	}
	return resp, nil
}

// Close tears down the runtime.
func (p *Plugin) Close(ctx context.Context) error {
	var err error
	p.closeOnce.Do(func() {
		if p.module != nil {
			_ = p.module.Close(ctx)
		}
		err = p.runtime.Close(ctx)
	})
	return err
}

// readString reads a NUL-terminated string from guest memory (bounded by the
// remaining linear memory and MaxResponseSize).
func (p *Plugin) readString(ptr uint32) string {
	mem := p.module.Memory()
	if mem == nil {
		return ""
	}
	size := mem.Size()
	if uint64(ptr) >= uint64(size) {
		return ""
	}
	remaining := uint32(size) - ptr
	if remaining > MaxResponseSize {
		remaining = MaxResponseSize
	}
	data, ok := mem.Read(ptr, remaining)
	if !ok {
		return ""
	}
	for i, b := range data {
		if b == 0 {
			return string(data[:i])
		}
	}
	return string(data)
}
