package toolruntime

import (
	"context"
	"fmt"
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
	// baseBefore/baseAfter are the host-registered interceptors (never removed
	// by owner unregister); ownedBefore/ownedAfter carry plugin/package-owned
	// interceptors with reversible registration.
	baseBefore   []BeforeInterceptor
	baseAfter    []AfterInterceptor
	ownedBefore  []ownedBeforeInterceptor
	ownedAfter   []ownedAfterInterceptor
	// owners tracks the owning registration for each tool name. A non-empty
	// owner enables reversible unregister (dynamic plugin/package uninstall).
	owners map[string]string
	// generations tracks the registration generation per tool name; each
	// registration bumps the generation so stale handles can be detected.
	generations map[string]uint64
	// draining marks tool names that are being torn down: calls to a draining
	// tool are rejected while the teardown completes.
	draining map[string]struct{}
	nextGen  uint64
}

// NewToolHandler creates a new tool handler
func NewToolHandler() *ToolHandler {
	return &ToolHandler{
		tools:         make(map[string]Tool),
		meta:          make(map[string]ToolMeta),
		activeTools:   make(map[string]struct{}),
		bundleTools:   make(map[string][]string),
		bundleSummary: make(map[string]string),
		owners:        make(map[string]string),
		generations:   make(map[string]uint64),
		draining:      make(map[string]struct{}),
	}
}

// Registration is a reversible tool registration handle. Dispose unregisters
// the tool (and its bundle/active bookkeeping) if this handle is still the
// current registration for the tool name.
type Registration struct {
	handler    *ToolHandler
	name       string
	owner      string
	generation uint64
	done       bool
}

// Dispose reverses the registration. It is safe to call multiple times; later
// calls are no-ops. Only the current registration generation is removed, so a
// stale handle cannot unregister a newer replacement.
func (r *Registration) Dispose() {
	if r == nil || r.handler == nil {
		return
	}
	r.handler.mu.Lock()
	defer r.handler.mu.Unlock()
	if r.done {
		return
	}
	if r.handler.generations[r.name] != r.generation {
		// A newer registration replaced this one; nothing to remove.
		r.done = true
		return
	}
	r.handler.removeToolLocked(r.name)
	r.done = true
}

// Generation returns the registration generation, which can be compared with
// the handler's current generation for the name to detect staleness.
func (r *Registration) Generation() uint64 {
	if r == nil {
		return 0
	}
	return r.generation
}

// Owner returns the owner id recorded at registration time ("" if anonymous).
func (r *Registration) Owner() string {
	if r == nil {
		return ""
	}
	return r.owner
}

// CurrentGeneration returns the current registration generation for a tool
// name (0 when the tool is not registered).
func (h *ToolHandler) CurrentGeneration(name string) uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.generations[name]
}

// OwnerFor returns the owner id recorded for a tool name ("" if anonymous or
// not registered).
func (h *ToolHandler) OwnerFor(name string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.owners[name]
}

// Register adds a tool to the handler and returns a reversible registration
// handle.
func (h *ToolHandler) Register(tool Tool) *Registration {
	return h.RegisterWithMeta(tool, ToolMeta{AlwaysActive: true})
}

// RegisterWithMeta adds a tool with loading metadata to the handler and
// returns a reversible registration handle. Re-registering the same name
// replaces the previous registration (bumping the generation) without removing
// the prior handle's bookkeeping beyond what the replacement needs.
func (h *ToolHandler) RegisterWithMeta(tool Tool, meta ToolMeta) *Registration {
	// Anonymous registration never conflicts: RegisterOwned only rejects when
	// both the prior and new owner are non-empty and differ.
	registration, _ := h.RegisterOwned("", tool, meta)
	return registration
}

// RegisterOwned adds a tool owned by the named owner (e.g. a plugin or package
// id) and returns a reversible registration handle. Same-name registration by
// a different non-empty owner is rejected so dynamic components cannot
// silently clobber each other; re-registration by the same owner (or the
// anonymous owner) replaces the previous registration.
func (h *ToolHandler) RegisterOwned(owner string, tool Tool, meta ToolMeta) (*Registration, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	name := tool.Name()
	if prior := h.owners[name]; prior != "" && owner != "" && prior != owner {
		return nil, ErrToolConflict{Name: name, Owner: prior}
	}
	h.nextGen++
	generation := h.nextGen
	h.registerWithMetaLocked(tool, meta)
	h.owners[name] = owner
	h.generations[name] = generation
	delete(h.draining, name)
	return &Registration{handler: h, name: name, owner: owner, generation: generation}, nil
}

const (
	// BundleAlwaysOn groups host-resident meta tools (registered with
	// AlwaysActive) into a visible, selectable virtual bundle so templates
	// can include them explicitly instead of them being force-preserved
	// invisibly.
	BundleAlwaysOn = "always_on"
)

func (h *ToolHandler) registerWithMetaLocked(tool Tool, meta ToolMeta) {
	name := tool.Name()
	if existing, ok := h.meta[name]; ok && existing.Bundle != "" && existing.Bundle != meta.Bundle {
		h.bundleTools[existing.Bundle] = stringutil.Remove(h.bundleTools[existing.Bundle], name)
	}
	// Drop out of the virtual always_on bundle on re-registration without
	// AlwaysActive (e.g. a component replacing its own tool metadata).
	if prior, ok := h.meta[name]; ok && prior.AlwaysActive && !meta.AlwaysActive {
		h.bundleTools[BundleAlwaysOn] = stringutil.Remove(h.bundleTools[BundleAlwaysOn], name)
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
		h.bundleTools[BundleAlwaysOn] = stringutil.AppendUnique(h.bundleTools[BundleAlwaysOn], name)
		if h.bundleSummary[BundleAlwaysOn] == "" {
			h.bundleSummary[BundleAlwaysOn] = "Host-resident meta tools (memory, skills, compression, session management, tool exchange, ...)"
		}
	}
}

// removeToolLocked removes a tool and all of its bookkeeping. Callers must
// hold h.mu.
func (h *ToolHandler) removeToolLocked(name string) {
	if _, ok := h.tools[name]; !ok {
		return
	}
	delete(h.tools, name)
	meta := h.meta[name]
	if meta.Bundle != "" {
		h.bundleTools[meta.Bundle] = stringutil.Remove(h.bundleTools[meta.Bundle], name)
		if len(h.bundleTools[meta.Bundle]) == 0 {
			delete(h.bundleTools, meta.Bundle)
			delete(h.bundleSummary, meta.Bundle)
		}
	}
	if meta.AlwaysActive {
		h.bundleTools[BundleAlwaysOn] = stringutil.Remove(h.bundleTools[BundleAlwaysOn], name)
		if len(h.bundleTools[BundleAlwaysOn]) == 0 {
			delete(h.bundleTools, BundleAlwaysOn)
			delete(h.bundleSummary, BundleAlwaysOn)
		}
	}
	delete(h.meta, name)
	delete(h.activeTools, name)
	delete(h.owners, name)
	delete(h.generations, name)
	delete(h.draining, name)
}

// MarkDraining flags a tool as tearing down: subsequent Handle/HandleResult
// calls are rejected until the tool is re-registered or removed.
func (h *ToolHandler) MarkDraining(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.tools[name]; ok {
		h.draining[name] = struct{}{}
		delete(h.activeTools, name)
	}
}

// UnregisterOwner disposes every current registration owned by the given
// owner (e.g. all tools contributed by one plugin or package). Returns the
// names that were removed.
func (h *ToolHandler) UnregisterOwner(owner string) []string {
	if owner == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var removed []string
	for name, recorded := range h.owners {
		if recorded != owner {
			continue
		}
		removed = append(removed, name)
	}
	sort.Strings(removed)
	for _, name := range removed {
		h.removeToolLocked(name)
	}
	return removed
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
	owners := make(map[string]string, len(next.owners))
	for name, owner := range next.owners {
		owners[name] = owner
	}
	// Remap generations to continue from this handler's counter so stale
	// registration handles from either handler can never collide with the
	// swapped-in registrations.
	generations := make(map[string]uint64, len(next.generations))
	for name := range next.generations {
		h.nextGen++
		generations[name] = h.nextGen
	}
	draining := make(map[string]struct{}, len(next.draining))
	for name := range next.draining {
		draining[name] = struct{}{}
	}
	before := append([]BeforeInterceptor{}, next.before...)
	after := append([]AfterInterceptor{}, next.after...)
	baseBefore := append([]BeforeInterceptor{}, next.baseBefore...)
	baseAfter := append([]AfterInterceptor{}, next.baseAfter...)
	ownedBefore := append([]ownedBeforeInterceptor{}, next.ownedBefore...)
	ownedAfter := append([]ownedAfterInterceptor{}, next.ownedAfter...)
	next.mu.RUnlock()

	h.mu.Lock()
	h.tools = tools
	h.meta = meta
	h.activeTools = activeTools
	h.bundleTools = bundleTools
	h.bundleSummary = bundleSummary
	h.owners = owners
	h.generations = generations
	h.draining = draining
	h.before = before
	h.after = after
	h.baseBefore = baseBefore
	h.baseAfter = baseAfter
	h.ownedBefore = ownedBefore
	h.ownedAfter = ownedAfter
	h.mu.Unlock()
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
	if _, draining := h.draining[name]; draining {
		h.mu.RUnlock()
		return ToolResult{}, ErrToolDraining{Name: name}
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

// SetActiveTools replaces the active tool set with the named tools while
// preserving always-active tools. Unlike SetActiveBundles it addresses tools
// by name, which lets session creation modes (e.g. the minimal mode) pin the
// initial tool set to a small list such as read/write/edit/bash.
func (h *ToolHandler) SetActiveTools(names ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	desired := make(map[string]struct{}, len(names))
	for _, name := range stringutil.UniqueNonEmpty(names) {
		desired[name] = struct{}{}
	}

	for name, meta := range h.meta {
		if meta.AlwaysActive {
			h.activeTools[name] = struct{}{}
			continue
		}
		if _, ok := desired[name]; ok {
			h.activeTools[name] = struct{}{}
		} else {
			delete(h.activeTools, name)
		}
	}
}

// SetActiveToolsExact replaces the active tool set with exactly the named
// tools: always-active tools are NOT force-preserved. The template chain uses
// this so a preset like "only edit_file + bash" yields exactly that set;
// meta tools that must stay reachable should be listed explicitly (or via the
// "always_on" bundle). Legacy session modes keep using SetActiveTools, which
// preserves always-active tools for backward compatibility.
func (h *ToolHandler) SetActiveToolsExact(names ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	desired := make(map[string]struct{}, len(names))
	for _, name := range stringutil.UniqueNonEmpty(names) {
		desired[name] = struct{}{}
	}
	for name := range h.meta {
		if _, ok := desired[name]; ok {
			h.activeTools[name] = struct{}{}
		} else {
			delete(h.activeTools, name)
		}
	}
}

// AddBeforeInterceptors appends ordered before-interceptors to the handler runtime.
func (h *ToolHandler) AddBeforeInterceptors(interceptors ...BeforeInterceptor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, interceptor := range interceptors {
		if interceptor == nil {
			continue
		}
		h.baseBefore = append(h.baseBefore, interceptor)
		h.before = append(h.before, interceptor)
	}
}

// AddBeforeInterceptorsForTools appends ordered before-interceptors scoped to the named tools.
func (h *ToolHandler) AddBeforeInterceptorsForTools(toolNames []string, interceptors ...BeforeInterceptor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, interceptor := range interceptors {
		if interceptor == nil {
			continue
		}
		scoped := scopedBeforeInterceptor(toolNames, interceptor)
		h.baseBefore = append(h.baseBefore, scoped)
		h.before = append(h.before, scoped)
	}
}

// AddAfterInterceptors appends ordered after-interceptors to the handler runtime.
func (h *ToolHandler) AddAfterInterceptors(interceptors ...AfterInterceptor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, interceptor := range interceptors {
		if interceptor == nil {
			continue
		}
		h.baseAfter = append(h.baseAfter, interceptor)
		h.after = append(h.after, interceptor)
	}
}

// AddAfterInterceptorsForTools appends ordered after-interceptors scoped to the named tools.
func (h *ToolHandler) AddAfterInterceptorsForTools(toolNames []string, interceptors ...AfterInterceptor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, interceptor := range interceptors {
		if interceptor == nil {
			continue
		}
		scoped := scopedAfterInterceptor(toolNames, interceptor)
		h.baseAfter = append(h.baseAfter, scoped)
		h.after = append(h.after, scoped)
	}
}

// AddBeforeInterceptorsOwned appends a before-interceptor owned by the named
// component (plugin/package id) and returns a disposer that reverses it. The
// disposer is idempotent and safe to call after the handler was replaced.
func (h *ToolHandler) AddBeforeInterceptorsOwned(owner string, interceptor BeforeInterceptor) func() {
	if interceptor == nil {
		return func() {}
	}
	h.mu.Lock()
	h.ownedBefore = append(h.ownedBefore, ownedBeforeInterceptor{owner: owner, fn: interceptor})
	h.before = append(h.before, interceptor)
	h.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			h.removeOwnedBefore(owner, interceptor)
		})
	}
}

// AddAfterInterceptorsOwned appends an after-interceptor owned by the named
// component and returns a disposer that reverses it.
func (h *ToolHandler) AddAfterInterceptorsOwned(owner string, interceptor AfterInterceptor) func() {
	if interceptor == nil {
		return func() {}
	}
	h.mu.Lock()
	h.ownedAfter = append(h.ownedAfter, ownedAfterInterceptor{owner: owner, fn: interceptor})
	h.after = append(h.after, interceptor)
	h.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			h.removeOwnedAfter(owner, interceptor)
		})
	}
}

func (h *ToolHandler) removeOwnedBefore(owner string, target BeforeInterceptor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	kept := h.ownedBefore[:0]
	for _, item := range h.ownedBefore {
		if item.owner == owner && sameBefore(item.fn, target) {
			continue
		}
		kept = append(kept, item)
	}
	h.ownedBefore = kept
	h.rebuildOwnedBeforeLocked()
}

func (h *ToolHandler) removeOwnedAfter(owner string, target AfterInterceptor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	kept := h.ownedAfter[:0]
	for _, item := range h.ownedAfter {
		if item.owner == owner && sameAfter(item.fn, target) {
			continue
		}
		kept = append(kept, item)
	}
	h.ownedAfter = kept
	h.rebuildOwnedAfterLocked()
}

// UnregisterOwnerInterceptors reverses every owned before/after interceptor
// registered by the given owner (used on plugin/package unload).
func (h *ToolHandler) UnregisterOwnerInterceptors(owner string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	keptBefore := h.ownedBefore[:0]
	for _, item := range h.ownedBefore {
		if item.owner == owner {
			continue
		}
		keptBefore = append(keptBefore, item)
	}
	h.ownedBefore = keptBefore
	h.rebuildOwnedBeforeLocked()

	keptAfter := h.ownedAfter[:0]
	for _, item := range h.ownedAfter {
		if item.owner == owner {
			continue
		}
		keptAfter = append(keptAfter, item)
	}
	h.ownedAfter = keptAfter
	h.rebuildOwnedAfterLocked()
}

// rebuildOwnedBeforeLocked recomputes the effective before slice from the base
// interceptors plus owned interceptors. Callers must hold h.mu.
func (h *ToolHandler) rebuildOwnedBeforeLocked() {
	h.before = append(append([]BeforeInterceptor{}, h.baseBefore...), ownedBeforeFns(h.ownedBefore)...)
}

// rebuildOwnedAfterLocked recomputes the effective after slice. Callers must
// hold h.mu.
func (h *ToolHandler) rebuildOwnedAfterLocked() {
	h.after = append(append([]AfterInterceptor{}, h.baseAfter...), ownedAfterFns(h.ownedAfter)...)
}

type ownedBeforeInterceptor struct {
	owner string
	fn    BeforeInterceptor
}

type ownedAfterInterceptor struct {
	owner string
	fn    AfterInterceptor
}

func ownedBeforeFns(items []ownedBeforeInterceptor) []BeforeInterceptor {
	out := make([]BeforeInterceptor, 0, len(items))
	for _, item := range items {
		out = append(out, item.fn)
	}
	return out
}

func ownedAfterFns(items []ownedAfterInterceptor) []AfterInterceptor {
	out := make([]AfterInterceptor, 0, len(items))
	for _, item := range items {
		out = append(out, item.fn)
	}
	return out
}

func sameBefore(a, b BeforeInterceptor) bool {
	return fmt.Sprintf("%p", a) == fmt.Sprintf("%p", b)
}

func sameAfter(a, b AfterInterceptor) bool {
	return fmt.Sprintf("%p", a) == fmt.Sprintf("%p", b)
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
