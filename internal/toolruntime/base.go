package toolruntime

import (
	"context"
	"sort"
	"sync"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/platform/stringutil"
)

// ToolMeta describes how a tool participates in progressive loading.
type ToolMeta struct {
	Bundle        string
	Summary       string
	DefaultActive bool
	AlwaysActive  bool
}

// BundleCatalogItem describes one loadable tool bundle.
type BundleCatalogItem struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Tools   []string `json:"tools"`
	Active  bool     `json:"active"`
}

// ToolCatalog describes which bundles and tools are currently available.
type ToolCatalog struct {
	ActiveBundles     []string            `json:"active_bundles"`
	AlwaysActiveTools []string            `json:"always_active_tools"`
	Bundles           []BundleCatalogItem `json:"bundles"`
}

// ToolHandler routes tool calls to appropriate tools
type ToolHandler struct {
	mu            sync.RWMutex
	tools         map[string]Tool
	meta          map[string]ToolMeta
	activeTools   map[string]struct{}
	bundleTools   map[string][]string
	bundleSummary map[string]string
	before        []BeforeInterceptor
	after         []AfterInterceptor
}

// NewToolHandler creates a new tool handler
func NewToolHandler() *ToolHandler {
	return &ToolHandler{
		tools:         make(map[string]Tool),
		meta:          make(map[string]ToolMeta),
		activeTools:   make(map[string]struct{}),
		bundleTools:   make(map[string][]string),
		bundleSummary: make(map[string]string),
	}
}

// ReplaceWith atomically swaps the handler registry contents while preserving
// the handler instance identity held by running callers.
func (h *ToolHandler) ReplaceWith(next *ToolHandler) {
	if h == nil || next == nil || h == next {
		return
	}
	next.mu.RLock()
	tools := make(map[string]Tool, len(next.tools))
	for name, tool := range next.tools {
		tools[name] = tool
	}
	meta := make(map[string]ToolMeta, len(next.meta))
	for name, item := range next.meta {
		meta[name] = item
	}
	activeTools := make(map[string]struct{}, len(next.activeTools))
	for name := range next.activeTools {
		activeTools[name] = struct{}{}
	}
	bundleTools := make(map[string][]string, len(next.bundleTools))
	for bundle, names := range next.bundleTools {
		bundleTools[bundle] = append([]string{}, names...)
	}
	bundleSummary := make(map[string]string, len(next.bundleSummary))
	for bundle, summary := range next.bundleSummary {
		bundleSummary[bundle] = summary
	}
	before := append([]BeforeInterceptor{}, next.before...)
	after := append([]AfterInterceptor{}, next.after...)
	next.mu.RUnlock()

	h.mu.Lock()
	h.tools = tools
	h.meta = meta
	h.activeTools = activeTools
	h.bundleTools = bundleTools
	h.bundleSummary = bundleSummary
	h.before = before
	h.after = after
	h.mu.Unlock()
}

// Register adds a tool to the handler
func (h *ToolHandler) Register(tool Tool) {
	h.RegisterWithMeta(tool, ToolMeta{AlwaysActive: true})
}

// RegisterWithMeta adds a tool with loading metadata to the handler.
func (h *ToolHandler) RegisterWithMeta(tool Tool, meta ToolMeta) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.registerWithMetaLocked(tool, meta)
}

func (h *ToolHandler) registerWithMetaLocked(tool Tool, meta ToolMeta) {
	name := tool.Name()
	if existing, ok := h.meta[name]; ok && existing.Bundle != "" && existing.Bundle != meta.Bundle {
		h.bundleTools[existing.Bundle] = stringutil.Remove(h.bundleTools[existing.Bundle], name)
	}

	h.tools[name] = tool
	h.meta[name] = meta

	if meta.Bundle != "" {
		h.bundleTools[meta.Bundle] = stringutil.AppendUnique(h.bundleTools[meta.Bundle], name)
		if meta.Summary != "" && h.bundleSummary[meta.Bundle] == "" {
			h.bundleSummary[meta.Bundle] = meta.Summary
		}
	}

	if meta.AlwaysActive {
		h.activeTools[name] = struct{}{}
	}
}

// ActivateDefaults enables default and always-active tools.
func (h *ToolHandler) ActivateDefaults() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.activateDefaultsLocked()
}

func (h *ToolHandler) activateDefaultsLocked() {
	for name, meta := range h.meta {
		if meta.AlwaysActive || meta.DefaultActive {
			h.activeTools[name] = struct{}{}
		}
	}
}

// ResetActiveToolsToDefaults drops transient bundle activations and restores
// only always-active and default-active tools.
func (h *ToolHandler) ResetActiveToolsToDefaults() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for name := range h.activeTools {
		delete(h.activeTools, name)
	}
	h.activateDefaultsLocked()
}

// Get returns a tool by name
func (h *ToolHandler) Get(name string) Tool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.tools[name]
}

// List returns all registered tool names
func (h *ToolHandler) List() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	names := make([]string, 0, len(h.tools))
	for name := range h.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Handle processes a tool call
func (h *ToolHandler) Handle(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	result, err := h.HandleResult(ctx, name, args)
	if err != nil {
		return "", err
	}
	return serializeToolResult(result)
}

// HandleResult processes a tool call and returns the full typed runtime result.
func (h *ToolHandler) HandleResult(ctx context.Context, name string, args map[string]interface{}) (ToolResult, error) {
	h.mu.RLock()
	tool, ok := h.tools[name]
	if !ok {
		active := h.activeNamesLocked()
		h.mu.RUnlock()
		return ToolResult{}, ErrToolNotFound{Name: name, Available: active}
	}
	if _, ok := h.activeTools[name]; !ok {
		bundle := h.meta[name].Bundle
		h.mu.RUnlock()
		return ToolResult{}, ErrToolInactive{Name: name, Bundle: bundle}
	}
	schema := tool.Spec().ToolSchema().InputSchema
	aliases := tool.Spec().Aliases
	before := append([]BeforeInterceptor{}, h.before...)
	after := append([]AfterInterceptor{}, h.after...)
	h.mu.RUnlock()
	// Apply aliases before validation so legacy parameter names (e.g. "todos"
	// → "items") pass schema-required checks. The map mutation is safe because
	// prepare() applies aliases idempotently.
	applyAliases(args, aliases)
	if err := validateToolInputSchema(name, args, schema); err != nil {
		return ToolResult{}, err
	}
	return executeToolRuntimeResult(ctx, tool, args, before, after)
}

// Schemas returns registered tool schemas, optionally filtered by name.
func (h *ToolHandler) Schemas(names ...string) []protocol.ToolSchema {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.schemasFromNames(h.selectedNames(names...))
}

// ActiveSchemas returns active tool schemas, optionally filtered by name.
func (h *ToolHandler) ActiveSchemas(names ...string) []protocol.ToolSchema {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.schemasFromNames(h.activeNames(names...))
}

// IsActive reports whether a tool is currently active.
func (h *ToolHandler) IsActive(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.activeTools[name]
	return ok
}

// Catalog returns bundle and active-tool information for prompt rendering and tool exchange.
func (h *ToolHandler) Catalog() ToolCatalog {
	h.mu.RLock()
	defer h.mu.RUnlock()
	alwaysActive := make([]string, 0)
	for name, meta := range h.meta {
		if meta.AlwaysActive {
			alwaysActive = append(alwaysActive, name)
		}
	}
	sort.Strings(alwaysActive)

	bundleNames := make([]string, 0, len(h.bundleTools))
	for name := range h.bundleTools {
		bundleNames = append(bundleNames, name)
	}
	sort.Strings(bundleNames)

	items := make([]BundleCatalogItem, 0, len(bundleNames))
	activeBundles := make([]string, 0, len(bundleNames))
	for _, bundle := range bundleNames {
		toolNames := append([]string{}, h.bundleTools[bundle]...)
		sort.Strings(toolNames)
		active := false
		for _, toolName := range toolNames {
			if _, ok := h.activeTools[toolName]; ok {
				active = true
				break
			}
		}
		if active {
			activeBundles = append(activeBundles, bundle)
		}
		items = append(items, BundleCatalogItem{
			Name:    bundle,
			Summary: h.bundleSummary[bundle],
			Tools:   toolNames,
			Active:  active,
		})
	}

	return ToolCatalog{
		ActiveBundles:     activeBundles,
		AlwaysActiveTools: alwaysActive,
		Bundles:           items,
	}
}

// ActivateBundles enables all tools in the named bundles.
func (h *ToolHandler) ActivateBundles(names ...string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	changed := make([]string, 0, len(names))
	for _, bundle := range stringutil.UniqueNonEmpty(names) {
		toolNames := h.bundleTools[bundle]
		if len(toolNames) == 0 {
			continue
		}
		bundleChanged := false
		for _, name := range toolNames {
			if _, ok := h.activeTools[name]; !ok {
				h.activeTools[name] = struct{}{}
				bundleChanged = true
			}
		}
		if bundleChanged {
			changed = append(changed, bundle)
		}
	}
	sort.Strings(changed)
	return changed
}

// DeactivateBundles disables non-always-active tools in the named bundles.
func (h *ToolHandler) DeactivateBundles(names ...string) (changed []string, blocked []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, bundle := range stringutil.UniqueNonEmpty(names) {
		toolNames := h.bundleTools[bundle]
		if len(toolNames) == 0 {
			continue
		}
		bundleBlocked := false
		bundleChanged := false
		for _, name := range toolNames {
			if h.meta[name].AlwaysActive {
				bundleBlocked = true
				continue
			}
			if _, ok := h.activeTools[name]; ok {
				delete(h.activeTools, name)
				bundleChanged = true
			}
		}
		if bundleChanged {
			changed = append(changed, bundle)
		}
		if bundleBlocked {
			blocked = append(blocked, bundle)
		}
	}
	sort.Strings(changed)
	sort.Strings(blocked)
	return changed, blocked
}

// SetActiveBundles replaces the active bundle set while preserving always-active tools.
func (h *ToolHandler) SetActiveBundles(names ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	desired := make(map[string]struct{}, len(names))
	for _, bundle := range stringutil.UniqueNonEmpty(names) {
		desired[bundle] = struct{}{}
	}

	for name, meta := range h.meta {
		if meta.AlwaysActive {
			h.activeTools[name] = struct{}{}
			continue
		}
		delete(h.activeTools, name)
	}

	for bundle := range desired {
		for _, name := range h.bundleTools[bundle] {
			h.activeTools[name] = struct{}{}
		}
	}
}

// AddBeforeInterceptors appends ordered before-interceptors to the handler runtime.
func (h *ToolHandler) AddBeforeInterceptors(interceptors ...BeforeInterceptor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.before = append(h.before, interceptors...)
}

// AddBeforeInterceptorsForTools appends ordered before-interceptors scoped to the named tools.
func (h *ToolHandler) AddBeforeInterceptorsForTools(toolNames []string, interceptors ...BeforeInterceptor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, interceptor := range interceptors {
		if interceptor == nil {
			continue
		}
		h.before = append(h.before, scopedBeforeInterceptor(toolNames, interceptor))
	}
}

// AddAfterInterceptors appends ordered after-interceptors to the handler runtime.
func (h *ToolHandler) AddAfterInterceptors(interceptors ...AfterInterceptor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.after = append(h.after, interceptors...)
}

// AddAfterInterceptorsForTools appends ordered after-interceptors scoped to the named tools.
func (h *ToolHandler) AddAfterInterceptorsForTools(toolNames []string, interceptors ...AfterInterceptor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, interceptor := range interceptors {
		if interceptor == nil {
			continue
		}
		h.after = append(h.after, scopedAfterInterceptor(toolNames, interceptor))
	}
}

// BundleForTool returns the bundle name for a tool, if any.
func (h *ToolHandler) BundleForTool(name string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.meta[name].Bundle
}

func (h *ToolHandler) selectedNames(names ...string) []string {
	selected := make([]string, 0)
	if len(names) == 0 {
		selected = h.List()
	} else {
		seen := make(map[string]struct{}, len(names))
		for _, name := range names {
			if _, ok := seen[name]; ok {
				continue
			}
			if h.Get(name) == nil {
				continue
			}
			seen[name] = struct{}{}
			selected = append(selected, name)
		}
		sort.Strings(selected)
	}
	return selected
}

func (h *ToolHandler) activeNames(names ...string) []string {
	selected := h.selectedNames(names...)
	active := make([]string, 0, len(selected))
	for _, name := range selected {
		if h.IsActive(name) {
			active = append(active, name)
		}
	}
	return active
}

func (h *ToolHandler) activeNamesLocked() []string {
	active := make([]string, 0, len(h.activeTools))
	for name := range h.activeTools {
		active = append(active, name)
	}
	sort.Strings(active)
	return active
}

func (h *ToolHandler) schemasFromNames(selected []string) []protocol.ToolSchema {
	schemas := make([]protocol.ToolSchema, 0, len(selected))
	for _, name := range selected {
		schemas = append(schemas, h.tools[name].Spec().ToolSchema())
	}
	return schemas
}

func scopedBeforeInterceptor(toolNames []string, interceptor BeforeInterceptor) BeforeInterceptor {
	allowed := make(map[string]struct{}, len(toolNames))
	for _, name := range stringutil.UniqueNonEmpty(toolNames) {
		allowed[name] = struct{}{}
	}
	if len(allowed) == 0 {
		return interceptor
	}
	return func(ctx context.Context, call *ToolCall) (*ToolResult, error) {
		if _, ok := allowed[call.Name]; !ok {
			return nil, nil
		}
		return interceptor(ctx, call)
	}
}

func scopedAfterInterceptor(toolNames []string, interceptor AfterInterceptor) AfterInterceptor {
	allowed := make(map[string]struct{}, len(toolNames))
	for _, name := range stringutil.UniqueNonEmpty(toolNames) {
		allowed[name] = struct{}{}
	}
	if len(allowed) == 0 {
		return interceptor
	}
	return func(ctx context.Context, call *ToolCall, result ToolResult, err error) (ToolResult, error) {
		if _, ok := allowed[call.Name]; !ok {
			return result, err
		}
		return interceptor(ctx, call, result, err)
	}
}
