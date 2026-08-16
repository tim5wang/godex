package pluginrt

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Manager owns plugin instances and the scope-aware capability registry. It
// validates dependency graphs before activation and supports transactional
// reload: a failed activation never replaces the currently active registry.
type Manager struct {
	mu        sync.Mutex
	graph     *Graph
	registry  *Registry
	instances map[string]*Instance // plugin id -> instance
	nextGen   uint64
	// platformCapabilities, when set, is a predicate answering whether the
	// platform itself provides a capability (so requires can be satisfied
	// without a plugin).
	platform func(capabilityName string) bool
}

// NewManager creates an empty plugin manager. platform may be nil.
func NewManager(platform func(capabilityName string) bool) *Manager {
	return &Manager{
		graph:     NewGraph(platform),
		registry:  NewRegistry(),
		instances: make(map[string]*Instance),
		platform:  platform,
	}
}

// List returns the active plugin instances sorted by id.
func (m *Manager) List() []*Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.instances))
	for id := range m.instances {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*Instance, 0, len(ids))
	for _, id := range ids {
		out = append(out, m.instances[id])
	}
	return out
}

// Get returns the instance with the given id, or nil.
func (m *Manager) Get(id string) *Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.instances[id]
}

// Activate validates the candidate's dependency graph against the current set
// (with the candidate replacing any same-id instance) and, on success, starts
// it and records its provides in the registry. Activation is transactional:
// if Start fails, the registry and instance set are untouched.
func (m *Manager) Activate(ctx context.Context, plugin NativePlugin) (*Instance, error) {
	manifest := plugin.Manifest()
	if err := manifest.Validate(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	installed := make([]Manifest, 0, len(m.instances))
	for _, instance := range m.instances {
		installed = append(installed, instance.Manifest())
	}
	if report := m.graph.Validate(manifest, installed); !report.Empty() {
		return nil, fmt.Errorf("plugin %s dependency check failed: %s", manifest.ID, report.Error())
	}

	m.nextGen++
	instance := &Instance{
		manifest:   manifest,
		plugin:     plugin,
		generation: m.nextGen,
		ledger:     NewLedger(),
	}
	if err := instance.Start(ctx); err != nil {
		return nil, fmt.Errorf("plugin %s failed to start: %w", manifest.ID, err)
	}

	// Commit: replace same-id instance (reverting its effects and revoking its
	// registry records) then register the new instance's provides.
	if prior := m.instances[manifest.ID]; prior != nil {
		_ = prior.Stop(ctx)
		// Revoke under the prior instance's scope: records were registered with
		// that scope, and revoking with the candidate's scope would leak stale
		// records when a plugin changes scope between versions.
		m.registry.RevokeAll(string(prior.Manifest().Scope), manifest.ID)
	}
	m.instances[manifest.ID] = instance
	for _, provided := range manifest.Provides {
		m.registry.Record(string(manifest.Scope), provided, manifest.ID)
	}
	return instance, nil
}

// Deactivate stops and removes the named plugin, reverting its effects and
// revoking its registry records. It is idempotent (missing plugin is a no-op).
func (m *Manager) Deactivate(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	instance := m.instances[id]
	if instance == nil {
		return nil
	}
	if err := instance.Stop(ctx); err != nil {
		return err
	}
	m.registry.RevokeAll(string(instance.manifest.Scope), id)
	delete(m.instances, id)
	return nil
}

// Registry exposes the scope-aware capability table for querying.
func (m *Manager) Registry() *Registry { return m.registry }

// PromptSections aggregates context/prompt contributions from every active
// plugin that provides them (P4 prompt/context contributor). Sections are
// returned in plugin registration order, prefixed by the plugin id for stable
// de-duplication.
func (m *Manager) PromptSections() []PromptSection {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.instances))
	for id := range m.instances {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []PromptSection
	for _, id := range ids {
		provider, ok := m.instances[id].plugin.(interface{ PromptSections() []PromptSection })
		if !ok {
			continue
		}
		for _, section := range provider.PromptSections() {
			section.PluginID = id
			out = append(out, section)
		}
	}
	return out
}

// Prepare begins a transactional reload of one plugin: it validates the
// candidate graph (shadow validation) without touching the live state. The
// returned transaction commits or rolls back atomically.
func (m *Manager) Prepare(ctx context.Context, plugin NativePlugin) (*Transaction, error) {
	manifest := plugin.Manifest()
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	installed := make([]Manifest, 0, len(m.instances))
	for _, instance := range m.instances {
		installed = append(installed, instance.Manifest())
	}
	if report := m.graph.Validate(manifest, installed); !report.Empty() {
		return nil, fmt.Errorf("plugin %s dependency check failed: %s", manifest.ID, report.Error())
	}
	prior := m.instances[manifest.ID]
	return &Transaction{manager: m, candidate: plugin, manifest: manifest, prior: prior}, nil
}

// Transaction is a prepared plugin activation that commits or rolls back
// atomically. A bad candidate never replaces the current active registry.
type Transaction struct {
	mu        sync.Mutex
	manager   *Manager
	candidate NativePlugin
	manifest  Manifest
	prior     *Instance
	done      bool
}

// Commit applies the transaction: stop the prior instance, activate the
// candidate, and swap registry records. It is a no-op after Rollback.
func (tx *Transaction) Commit(ctx context.Context) (*Instance, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return tx.manager.Get(tx.manifest.ID), nil
	}
	tx.manager.mu.Lock()
	defer tx.manager.mu.Unlock()
	if tx.manager.instances[tx.manifest.ID] != tx.prior {
		tx.done = true
		return nil, fmt.Errorf("plugin %s transaction is stale", tx.manifest.ID)
	}
	tx.done = true

	tx.manager.nextGen++
	instance := &Instance{
		manifest:   tx.manifest,
		plugin:     tx.candidate,
		generation: tx.manager.nextGen,
		ledger:     NewLedger(),
	}
	if err := instance.Start(ctx); err != nil {
		return nil, fmt.Errorf("plugin %s failed to start: %w", tx.manifest.ID, err)
	}
	if tx.prior != nil {
		_ = tx.prior.Stop(ctx)
		tx.manager.registry.RevokeAll(string(tx.prior.Manifest().Scope), tx.manifest.ID)
	}
	tx.manager.instances[tx.manifest.ID] = instance
	for _, provided := range tx.manifest.Provides {
		tx.manager.registry.Record(string(tx.manifest.Scope), provided, tx.manifest.ID)
	}
	return instance, nil
}

// Rollback aborts the transaction: the live registry and instances are never
// touched (Prepare is non-mutating), and a later Commit becomes a no-op.
func (tx *Transaction) Rollback() {
	tx.mu.Lock()
	tx.done = true
	tx.mu.Unlock()
}
