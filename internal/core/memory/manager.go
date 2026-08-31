package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/platform/fsutil"
)

const (
	EntrypointName   = "MEMORY.md"
	IndexFileName    = "index.json"
	maxPromptLines   = 200
	maxPromptBytes   = 25000
	maxRetrievedBody = 500
)

// Type represents one supported durable memory type.
type Type string

const (
	TypeIdentity   Type = "identity"
	TypeUser       Type = "user"
	TypeWorkflow   Type = "workflow"
	TypeProject    Type = "project"
	TypeWarning    Type = "warning"
	TypeWorkMethod Type = "work_method"
	TypeWorkFact   Type = "work_fact"
)

// Status represents the lifecycle state of one durable memory entry.
type Status string

const (
	// StatusActive is the default: the memory participates in recall and
	// context injection.
	StatusActive Status = "active"
	// StatusArchived hides the memory from default recall and injection while
	// keeping it recoverable through the management UI / API.
	StatusArchived Status = "archived"
)

// StatusAll is a search-only sentinel meaning "do not filter by status".
const StatusAll Status = "all"

// Entry is one durable memory entry.
type Entry struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	File        string    `json:"file"`
	Summary     string    `json:"summary"`
	Type        Type      `json:"type"`
	Source      string    `json:"source,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Tags        []string  `json:"tags,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	// Status is the lifecycle state ("active" default, "archived").
	Status Status `json:"status,omitempty"`
	// LastReferencedAt records the last time this memory was selected into
	// prompt context (recall or injection). Zero means it has never been used.
	LastReferencedAt time.Time `json:"last_referenced_at,omitempty"`
}

// SaveInput describes one memory to save.
type SaveInput struct {
	Title   string
	Summary string
	Content string
	Type    Type
	Source  string
	Tags    []string
}

// UpdateInput updates one durable memory entry identified by title or file.
type UpdateInput struct {
	Match   ForgetInput
	Title   string
	Summary string
	Content string
	Type    Type
	Source  string
	Tags    []string
}

// ForgetInput identifies one durable memory entry to delete.
type ForgetInput struct {
	Title string
	File  string
}

// SearchOptions controls metadata filtering when browsing memories.
type SearchOptions struct {
	Query  string
	Type   Type
	Tag    string
	Source string
	// Status filters by lifecycle state. Empty means active-only (the default
	// recall behavior); StatusArchived lists only archived; StatusAll includes
	// both. Archived memories are excluded from default recall.
	Status Status
	Limit  int
}

// RelevantMemory is a retrieved memory entry plus its content.
type RelevantMemory struct {
	Entry
	Content string `json:"content"`
	Score   int    `json:"score,omitempty"`
}

// StoredMemory is one durable memory entry with its full content body.
type StoredMemory struct {
	Entry
	Content string `json:"content"`
}

type Manager struct {
	dir   string
	scope scope.Id
	mu    sync.RWMutex

	contentCache map[string]memoryFileCacheEntry
	readFile     func(string) ([]byte, error)
	statFile     func(string) (os.FileInfo, error)

	// candidateCountCache caches the number of pending memory candidates.
	// -1 means "not cached, needs refresh".
	candidateCountCache int
}

type memoryFileCacheEntry struct {
	modTime time.Time
	size    int64
	record  StoredMemory
}

type indexEnvelope struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// NewManager creates a new memory manager rooted at dir (unspecified scope).
func NewManager(dir string) *Manager {
	return &Manager{
		dir:                 dir,
		contentCache:        make(map[string]memoryFileCacheEntry),
		readFile:            os.ReadFile,
		statFile:            os.Stat,
		candidateCountCache: -1,
	}
}

// NewScopedManager creates a memory manager for one scope (roadmap 6.2).
// The scope is recorded on the manager and its storage root is derived from
// baseDir: an empty scope or an org scope stays at baseDir (the shared
// org/legacy layer), while a session or personal scope is isolated under
// baseDir/<scope storage key> so different sessions never read each other's
// memory. All existing Manager methods operate on this root, so callers only
// need to construct the right manager per scope.
func NewScopedManager(baseDir string, s scope.Id) *Manager {
	dir := baseDir
	if kind, _, ok := scope.Parse(s); ok && kind != scope.KindOrg {
		dir = filepath.Join(baseDir, scope.StorageKey(s))
	}
	return &Manager{
		dir:                 dir,
		scope:               s,
		contentCache:        make(map[string]memoryFileCacheEntry),
		readFile:            os.ReadFile,
		statFile:            os.Stat,
		candidateCountCache: -1,
	}
}

// Scope returns the scope this manager is bound to, or "" when unspecified.
func (m *Manager) Scope() scope.Id {
	if m == nil {
		return ""
	}
	return m.scope
}

// Dir returns the memory directory path.
func (m *Manager) Dir() string {
	return m.dir
}

// CandidateCount returns the number of pending memory candidates, using a
// cached count that is invalidated when candidates are written, accepted,
// or dismissed. This avoids reading and decoding the full candidates file
// on every agent turn.
func (m *Manager) CandidateCount() (int, error) {
	m.mu.RLock()
	cached := m.candidateCountCache
	m.mu.RUnlock()
	if cached >= 0 {
		return cached, nil
	}
	candidates, err := m.ListCandidates()
	if err != nil {
		return 0, err
	}
	count := len(candidates)
	m.mu.Lock()
	m.candidateCountCache = count
	m.mu.Unlock()
	return count, nil
}

// invalidateCandidateCache marks the cached candidate count as stale.
func (m *Manager) invalidateCandidateCache() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.candidateCountCache = -1
}

// BuildPromptSection renders a system-prompt section describing persistent memory.
func (m *Manager) BuildPromptSection() (string, error) {
	if err := m.ensureStore(); err != nil {
		return "", err
	}

	indexPath := filepath.Join(m.dir, EntrypointName)
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return "", err
	}

	indexContent := truncateForPrompt(strings.TrimSpace(string(data)))
	if indexContent == "" {
		indexContent = "(empty)"
	}

	lines := []string{
		"# Memory",
		"This is durable project memory shared across future sessions. Use it only for facts, preferences, workflows, or warnings that will matter beyond the current conversation.",
		fmt.Sprintf("- Memory directory: %s", m.dir),
		"- Use memory tool (action=remember/list/search/get/forget/candidates/accept/dismiss) to manage durable cross-session memory. Do not use memory tools for short-lived task progress.",
		fmt.Sprintf("Current memory index (%s):", EntrypointName),
		indexContent,
	}
	return strings.Join(lines, "\n"), nil
}

// Remember creates or updates one memory entry and rewrites the indexes.
func (m *Manager) Remember(input SaveInput) (*Entry, error) {
	if err := validateSaveInput(input); err != nil {
		return nil, err
	}
	if err := m.ensureStore(); err != nil {
		return nil, err
	}

	entries, err := m.readEntries()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	titleKey := strings.ToLower(strings.TrimSpace(input.Title))
	entry := Entry{
		Title:       strings.TrimSpace(input.Title),
		Summary:     strings.TrimSpace(input.Summary),
		Type:        input.Type,
		Source:      strings.TrimSpace(input.Source),
		UpdatedAt:   now,
		Tags:        normalizeTags(input.Tags),
		Fingerprint: computeMemoryFingerprint(input.Type, strings.TrimSpace(input.Title), strings.TrimSpace(input.Summary), strings.TrimSpace(input.Content)),
	}

	existingIndex := -1
	var before *StoredMemory
	for i, existing := range entries {
		if strings.ToLower(existing.Title) == titleKey {
			existingIndex = i
			if record, err := m.readStoredMemory(existing); err == nil {
				before = &record
			}
			entry.ID = existing.ID
			entry.File = existing.File
			entry.CreatedAt = existing.CreatedAt
			if entry.Source == "" {
				entry.Source = existing.Source
			}
			if len(entry.Tags) == 0 {
				entry.Tags = append([]string{}, existing.Tags...)
			}
			break
		}
	}
	if entry.ID == "" {
		entry.ID = newMemoryID(entry.Title, now)
	}
	if entry.File == "" {
		entry.File = uniqueFileName(entries, slugify(entry.Title))
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}

	content := strings.TrimSpace(input.Content)
	if existingIndex >= 0 && before != nil {
		// Same-title updates fold new facts into prior content with
		// normalize-based dedup (foldCapture): repeated lines are skipped,
		// genuinely new lines are appended, so repeated remembers of the
		// same facts never accumulate duplicates.
		merged, _ := foldCapture(before.Content, content)
		content = merged
	}

	path := filepath.Join(m.dir, entry.File)
	rendered := renderMemoryFile(entry, content)
	if err := fsutil.WriteFileAtomic(path, []byte(rendered), 0644); err != nil {
		return nil, err
	}
	m.invalidateMemoryFile(entry.File)

	if existingIndex >= 0 {
		entries[existingIndex] = entry
	} else {
		entries = append(entries, entry)
	}
	if err := m.writeEntries(entries); err != nil {
		return nil, err
	}
	m.syncSidecarEntry(entry)
	after := StoredMemory{Entry: entry, Content: content}
	if err := m.appendAudit(AuditLogEntry{
		Action: AuditRemember,
		Before: before,
		After:  &after,
		Source: strings.TrimSpace(input.Source),
	}); err != nil {
		return nil, err
	}

	return &entry, nil
}

// Update modifies one indexed memory entry while preserving its durable ID and file path.
func (m *Manager) Update(input UpdateInput) (*Entry, error) {
	if strings.TrimSpace(input.Match.Title) == "" && strings.TrimSpace(input.Match.File) == "" {
		return nil, fmt.Errorf("missing title or file")
	}
	saveInput := SaveInput{
		Title:   input.Title,
		Summary: input.Summary,
		Content: input.Content,
		Type:    input.Type,
		Source:  input.Source,
		Tags:    input.Tags,
	}
	if err := validateSaveInput(saveInput); err != nil {
		return nil, err
	}
	if err := m.ensureStore(); err != nil {
		return nil, err
	}

	entries, err := m.readEntries()
	if err != nil {
		return nil, err
	}

	matchIndex := -1
	for i, entry := range entries {
		if matchesForgetInput(entry, input.Match) {
			matchIndex = i
			break
		}
	}
	if matchIndex < 0 {
		return nil, fmt.Errorf("memory not found")
	}

	titleKey := strings.ToLower(strings.TrimSpace(input.Title))
	for i, existing := range entries {
		if i == matchIndex {
			continue
		}
		if strings.ToLower(existing.Title) == titleKey {
			return nil, fmt.Errorf("memory title already exists")
		}
	}

	existing := entries[matchIndex]
	before, err := m.readStoredMemory(existing)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	entry := Entry{
		ID:          existing.ID,
		Title:       strings.TrimSpace(input.Title),
		File:        existing.File,
		Summary:     strings.TrimSpace(input.Summary),
		Type:        input.Type,
		Source:      strings.TrimSpace(input.Source),
		CreatedAt:   existing.CreatedAt,
		UpdatedAt:   now,
		Tags:        normalizeTags(input.Tags),
		Fingerprint: computeMemoryFingerprint(input.Type, strings.TrimSpace(input.Title), strings.TrimSpace(input.Summary), strings.TrimSpace(input.Content)),
	}

	path := filepath.Join(m.dir, entry.File)
	rendered := renderMemoryFile(entry, strings.TrimSpace(input.Content))
	if err := fsutil.WriteFileAtomic(path, []byte(rendered), 0644); err != nil {
		return nil, err
	}
	m.invalidateMemoryFile(entry.File)

	entries[matchIndex] = entry
	if err := m.writeEntries(entries); err != nil {
		return nil, err
	}
	m.syncSidecarEntry(entry)
	after := StoredMemory{Entry: entry, Content: strings.TrimSpace(input.Content)}
	if err := m.appendAudit(AuditLogEntry{
		Action: AuditUpdate,
		Before: &before,
		After:  &after,
		Source: strings.TrimSpace(input.Source),
	}); err != nil {
		return nil, err
	}

	return &entry, nil
}

// Forget deletes one indexed memory entry and rewrites the indexes.
func (m *Manager) Forget(input ForgetInput) (*Entry, error) {
	if strings.TrimSpace(input.Title) == "" && strings.TrimSpace(input.File) == "" {
		return nil, fmt.Errorf("missing title or file")
	}
	if err := m.ensureStore(); err != nil {
		return nil, err
	}

	entries, err := m.readEntries()
	if err != nil {
		return nil, err
	}

	matchIndex := -1
	for i, entry := range entries {
		if matchesForgetInput(entry, input) {
			matchIndex = i
			break
		}
	}
	if matchIndex < 0 {
		return nil, fmt.Errorf("memory not found")
	}

	entry := entries[matchIndex]
	before, err := m.readStoredMemory(entry)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(filepath.Join(m.dir, entry.File)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	m.invalidateMemoryFile(entry.File)

	entries = append(entries[:matchIndex], entries[matchIndex+1:]...)
	if err := m.writeEntries(entries); err != nil {
		return nil, err
	}
	m.deleteSidecarEntry(entry.ID)
	if err := m.appendAudit(AuditLogEntry{
		Action: AuditForget,
		Before: &before,
	}); err != nil {
		return nil, err
	}
	return &entry, nil
}

// List returns all indexed memories sorted by most recent update.
func (m *Manager) List() ([]Entry, error) {
	if err := m.ensureStore(); err != nil {
		return nil, err
	}
	return m.readEntries()
}

// ListByType returns durable memories filtered by memory type.
func (m *Manager) ListByType(memoryType Type) ([]Entry, error) {
	return m.listBySearchOptions(SearchOptions{Type: memoryType})
}

// ListByTag returns durable memories filtered by one tag.
func (m *Manager) ListByTag(tag string) ([]Entry, error) {
	return m.listBySearchOptions(SearchOptions{Tag: tag})
}

// ListBySource returns durable memories filtered by one source.
func (m *Manager) ListBySource(source string) ([]Entry, error) {
	return m.listBySearchOptions(SearchOptions{Source: source})
}

// Get returns one memory entry plus its body content.
func (m *Manager) Get(idOrTitle string) (*StoredMemory, error) {
	if err := m.ensureStore(); err != nil {
		return nil, err
	}
	idOrTitle = strings.TrimSpace(idOrTitle)
	if idOrTitle == "" {
		return nil, fmt.Errorf("missing id or title")
	}
	entries, err := m.readEntries()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.ID == idOrTitle || strings.EqualFold(entry.Title, idOrTitle) {
			record, err := m.readStoredMemory(entry)
			if err != nil {
				return nil, err
			}
			return &record, nil
		}
	}
	return nil, fmt.Errorf("memory not found")
}

// Search lists durable memories using keyword and metadata filters.
func (m *Manager) Search(opts SearchOptions) ([]StoredMemory, error) {
	if results, err := m.searchWithSidecar(opts); err == nil {
		return results, nil
	}
	return m.searchLinear(opts)
}

func (m *Manager) searchLinear(opts SearchOptions) ([]StoredMemory, error) {
	if err := m.ensureStore(); err != nil {
		return nil, err
	}
	entries, err := m.readEntries()
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	queryLower := strings.ToLower(strings.TrimSpace(opts.Query))
	terms := extractTerms(opts.Query)
	tagFilter := strings.ToLower(strings.TrimSpace(opts.Tag))
	sourceFilter := strings.ToLower(strings.TrimSpace(opts.Source))
	relevant := make([]StoredMemory, 0, len(entries))
	for _, entry := range entries {
		if opts.Type != "" && entry.Type != opts.Type {
			continue
		}
		if !entryStatusMatches(entry, opts.Status) {
			continue
		}
		if tagFilter != "" && !entryHasTag(entry, tagFilter) {
			continue
		}
		if sourceFilter != "" && !strings.EqualFold(entry.Source, sourceFilter) {
			continue
		}
		record, err := m.readStoredMemory(entry)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		score := scoreRelevantMemory(queryLower, terms, record.Entry, record.Content)
		if queryLower == "" && len(terms) == 0 {
			score = 1
		}
		if score == 0 {
			continue
		}
		relevant = append(relevant, StoredMemory{
			Entry:   record.Entry,
			Content: record.Content,
		})
	}

	sort.SliceStable(relevant, func(i, j int) bool {
		left := scoreRelevantMemory(queryLower, terms, relevant[i].Entry, relevant[i].Content)
		right := scoreRelevantMemory(queryLower, terms, relevant[j].Entry, relevant[j].Content)
		if left == right {
			if relevant[i].UpdatedAt.Equal(relevant[j].UpdatedAt) {
				return relevant[i].Title < relevant[j].Title
			}
			return relevant[i].UpdatedAt.After(relevant[j].UpdatedAt)
		}
		return left > right
	})
	if opts.Limit > 0 && len(relevant) > opts.Limit {
		relevant = relevant[:opts.Limit]
	}
	return relevant, nil
}

func (m *Manager) listBySearchOptions(opts SearchOptions) ([]Entry, error) {
	results, err := m.Search(opts)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(results))
	for _, result := range results {
		entries = append(entries, result.Entry)
	}
	return entries, nil
}

// FindRelevant returns up to limit relevant memories for the query.
func (m *Manager) FindRelevant(query string, limit int) ([]RelevantMemory, error) {
	if strings.TrimSpace(query) == "" || limit <= 0 {
		return nil, nil
	}

	results, err := m.Search(SearchOptions{Query: query, Limit: limit})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}

	queryLower := strings.ToLower(strings.TrimSpace(query))
	terms := extractTerms(query)
	relevant := make([]RelevantMemory, 0, len(results))
	for _, result := range results {
		relevant = append(relevant, RelevantMemory{
			Entry:   result.Entry,
			Content: truncateBody(result.Content),
			Score:   scoreRelevantMemory(queryLower, terms, result.Entry, result.Content),
		})
	}
	return relevant, nil
}

func (m *Manager) readStoredMemory(entry Entry) (StoredMemory, error) {
	record, err := m.readMemoryRecord(entry.File)
	if err != nil {
		return StoredMemory{}, err
	}
	record.Entry = mergeEntryMetadata(entry, record.Entry)
	return record, nil
}

func mergeEntryMetadata(indexEntry Entry, fileEntry Entry) Entry {
	merged := indexEntry
	if merged.ID == "" {
		merged.ID = fileEntry.ID
	}
	if merged.Title == "" {
		merged.Title = fileEntry.Title
	}
	if merged.File == "" {
		merged.File = fileEntry.File
	}
	if merged.Summary == "" {
		merged.Summary = fileEntry.Summary
	}
	if merged.Type == "" {
		merged.Type = fileEntry.Type
	}
	if merged.Source == "" {
		merged.Source = fileEntry.Source
	}
	if merged.CreatedAt.IsZero() {
		merged.CreatedAt = fileEntry.CreatedAt
	}
	if merged.UpdatedAt.IsZero() {
		merged.UpdatedAt = fileEntry.UpdatedAt
	}
	if len(merged.Tags) == 0 {
		merged.Tags = append([]string{}, fileEntry.Tags...)
	}
	if merged.Fingerprint == "" {
		merged.Fingerprint = fileEntry.Fingerprint
	}
	if merged.Status == "" {
		merged.Status = fileEntry.Status
	}
	if merged.LastReferencedAt.IsZero() {
		merged.LastReferencedAt = fileEntry.LastReferencedAt
	}
	return merged
}

func (m *Manager) readMemoryRecord(filename string) (StoredMemory, error) {
	path := filepath.Join(m.dir, filename)
	info, err := m.statFile(path)
	if err != nil {
		return StoredMemory{}, err
	}

	m.mu.RLock()
	cached, ok := m.contentCache[filename]
	m.mu.RUnlock()
	if ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		return cached.record, nil
	}

	data, err := m.readFile(path)
	if err != nil {
		return StoredMemory{}, err
	}

	record := parseMemoryRecord(filename, data, info.ModTime().UTC())
	m.mu.Lock()
	m.contentCache[filename] = memoryFileCacheEntry{
		modTime: info.ModTime(),
		size:    info.Size(),
		record:  record,
	}
	m.mu.Unlock()
	return record, nil
}

func (m *Manager) invalidateMemoryFile(filename string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.contentCache, filename)
}
