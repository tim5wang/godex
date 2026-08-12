package memory

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

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

func (m *Manager) ensureStore() error {
	if err := os.MkdirAll(m.dir, 0755); err != nil {
		return err
	}
	indexPath := filepath.Join(m.dir, EntrypointName)
	if _, err := os.Stat(indexPath); errors.Is(err, os.ErrNotExist) {
		if err := fsutil.WriteFileAtomic(indexPath, []byte(""), 0644); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	jsonIndexPath := filepath.Join(m.dir, IndexFileName)
	if _, err := os.Stat(jsonIndexPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	legacyEntries, err := m.readLegacyEntries()
	if err != nil {
		return err
	}
	return m.writeEntries(legacyEntries)
}

func (m *Manager) readLegacyEntries() ([]Entry, error) {
	indexPath := filepath.Join(m.dir, EntrypointName)
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	legacy := parseIndex(string(data))
	if len(legacy) == 0 {
		return []Entry{}, nil
	}

	entries := make([]Entry, 0, len(legacy))
	for _, entry := range legacy {
		path := filepath.Join(m.dir, entry.File)
		info, err := m.statFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		record, err := m.readMemoryRecord(entry.File)
		if err != nil {
			return nil, err
		}
		merged := mergeEntryMetadata(entry, record.Entry)
		if merged.ID == "" {
			merged.ID = newMemoryID(merged.Title, info.ModTime().UTC())
		}
		if merged.File == "" {
			merged.File = entry.File
		}
		if merged.CreatedAt.IsZero() {
			merged.CreatedAt = info.ModTime().UTC()
		}
		if merged.UpdatedAt.IsZero() {
			merged.UpdatedAt = info.ModTime().UTC()
		}
		if merged.Fingerprint == "" {
			merged.Fingerprint = computeMemoryFingerprint(merged.Type, merged.Title, merged.Summary, record.Content)
		}
		entries = append(entries, merged)
	}
	sortEntries(entries)
	return entries, nil
}

func (m *Manager) readEntries() ([]Entry, error) {
	data, err := os.ReadFile(filepath.Join(m.dir, IndexFileName))
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return []Entry{}, nil
	}
	var envelope indexEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	entries := append([]Entry{}, envelope.Entries...)
	sortEntries(entries)
	return entries, nil
}

func (m *Manager) writeEntries(entries []Entry) error {
	sortEntries(entries)

	payload, err := json.MarshalIndent(indexEnvelope{
		Version: 1,
		Entries: entries,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(m.dir, IndexFileName), append(payload, '\n'), 0644); err != nil {
		return err
	}

	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, formatIndexLine(entry))
	}
	return fsutil.WriteFileAtomic(filepath.Join(m.dir, EntrypointName), []byte(strings.Join(lines, "\n")), 0644)
}

func sortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].UpdatedAt.Equal(entries[j].UpdatedAt) {
			return entries[i].Title < entries[j].Title
		}
		return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
	})
}

func validateSaveInput(input SaveInput) error {
	switch input.Type {
	case TypeIdentity, TypeUser, TypeWorkflow, TypeProject, TypeWarning:
	default:
		return fmt.Errorf("unsupported memory type %q", input.Type)
	}
	if strings.TrimSpace(input.Title) == "" {
		return fmt.Errorf("missing title")
	}
	if strings.TrimSpace(input.Summary) == "" {
		return fmt.Errorf("missing summary")
	}
	if strings.TrimSpace(input.Content) == "" {
		return fmt.Errorf("missing content")
	}
	return nil
}

func matchesForgetInput(entry Entry, input ForgetInput) bool {
	title := strings.TrimSpace(input.Title)
	file := strings.TrimSpace(input.File)
	switch {
	case title != "" && file != "":
		return strings.EqualFold(entry.Title, title) && entry.File == file
	case title != "":
		return strings.EqualFold(entry.Title, title)
	case file != "":
		return entry.File == file
	default:
		return false
	}
}

func renderMemoryFile(entry Entry, content string) string {
	lines := []string{
		"# " + entry.Title,
		"",
		"ID: " + entry.ID,
		"Type: " + string(entry.Type),
		"Summary: " + entry.Summary,
		"Created: " + entry.CreatedAt.Format(time.RFC3339),
		"Updated: " + entry.UpdatedAt.Format(time.RFC3339),
		"Source: " + entry.Source,
		"Tags: " + strings.Join(entry.Tags, ", "),
		"Fingerprint: " + entry.Fingerprint,
		"",
		strings.TrimSpace(content),
	}
	return strings.TrimSpace(strings.Join(lines, "\n")) + "\n"
}

func parseMemoryRecord(filename string, data []byte, fallbackModTime time.Time) StoredMemory {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	record := StoredMemory{
		Entry: Entry{
			File:      filename,
			CreatedAt: fallbackModTime,
			UpdatedAt: fallbackModTime,
		},
	}
	if len(lines) == 0 {
		return record
	}

	idx := 0
	if strings.HasPrefix(lines[0], "# ") {
		record.Title = strings.TrimSpace(strings.TrimPrefix(lines[0], "# "))
		idx = 1
	}

	for idx < len(lines) && strings.TrimSpace(lines[idx]) == "" {
		idx++
	}

	for idx < len(lines) {
		line := strings.TrimSpace(lines[idx])
		if line == "" {
			idx++
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			break
		}
		key = strings.TrimSpace(strings.ToLower(key))
		value = strings.TrimSpace(value)
		switch key {
		case "id":
			record.ID = value
		case "type":
			record.Type = Type(strings.ToLower(value))
		case "summary":
			record.Summary = value
		case "created":
			if ts, err := time.Parse(time.RFC3339, value); err == nil {
				record.CreatedAt = ts
			}
		case "updated":
			if ts, err := time.Parse(time.RFC3339, value); err == nil {
				record.UpdatedAt = ts
			}
		case "source":
			record.Source = value
		case "tags":
			record.Tags = normalizeTags(strings.Split(value, ","))
		case "fingerprint":
			record.Fingerprint = value
		default:
			goto content
		}
		idx++
	}

content:
	body := strings.TrimSpace(strings.Join(lines[idx:], "\n"))
	record.Content = body
	if record.ID == "" {
		record.ID = newMemoryID(record.Title, fallbackModTime)
	}
	if record.Fingerprint == "" {
		record.Fingerprint = computeMemoryFingerprint(record.Type, record.Title, record.Summary, body)
	}
	return record
}

func formatIndexLine(entry Entry) string {
	return fmt.Sprintf("- [%s](%s) - %s - %s", entry.Title, entry.File, entry.Type, entry.Summary)
}

var indexLinePattern = regexp.MustCompile(`^- \[(.+)\]\(([^)]+)\) - ([a-z]+) - (.+)$`)

func parseIndex(content string) []Entry {
	lines := strings.Split(content, "\n")
	entries := make([]Entry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		matches := indexLinePattern.FindStringSubmatch(line)
		if len(matches) != 5 {
			continue
		}
		entries = append(entries, Entry{
			Title:   matches[1],
			File:    matches[2],
			Type:    Type(matches[3]),
			Summary: matches[4],
		})
	}
	return entries
}

func uniqueFileName(entries []Entry, base string) string {
	if base == "" {
		base = "memory"
	}
	used := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		used[entry.File] = struct{}{}
	}
	candidate := base + ".md"
	if _, exists := used[candidate]; !exists {
		return candidate
	}
	for i := 2; ; i++ {
		candidate = fmt.Sprintf("%s_%d.md", base, i)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func newMemoryID(title string, now time.Time) string {
	base := strings.TrimSpace(title) + "|" + now.UTC().Format(time.RFC3339Nano)
	sum := sha1.Sum([]byte(base))
	return "mem-" + hex.EncodeToString(sum[:])[:12]
}

func computeMemoryFingerprint(memoryType Type, title, summary, content string) string {
	sum := sha1.Sum([]byte(strings.Join([]string{string(memoryType), title, summary, content}, "\n")))
	return hex.EncodeToString(sum[:])
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	result := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	sort.Strings(result)
	return result
}

func entryHasTag(entry Entry, tag string) bool {
	for _, existing := range entry.Tags {
		if strings.EqualFold(existing, tag) {
			return true
		}
	}
	return false
}

func slugify(title string) string {
	title = strings.TrimSpace(strings.ToLower(title))
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range title {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(r)
			lastUnderscore = false
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '/':
			if !lastUnderscore && builder.Len() > 0 {
				builder.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(builder.String(), "_")
}

func truncateForPrompt(content string) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	lineTruncated := false
	if len(lines) > maxPromptLines {
		lines = lines[:maxPromptLines]
		lineTruncated = true
	}
	truncated := strings.Join(lines, "\n")
	byteTruncated := false
	if len(truncated) > maxPromptBytes {
		truncated = truncated[:maxPromptBytes]
		byteTruncated = true
	}
	if lineTruncated || byteTruncated {
		truncated += "\n\n[Memory index truncated]"
	}
	return truncated
}

func truncateBody(content string) string {
	content = strings.TrimSpace(content)
	if len(content) <= maxRetrievedBody {
		return content
	}
	return strings.TrimSpace(content[:maxRetrievedBody]) + "..."
}

func scoreRelevantMemory(queryLower string, terms []string, entry Entry, content string) int {
	title := strings.ToLower(entry.Title)
	summary := strings.ToLower(entry.Summary)
	body := strings.ToLower(content)
	source := strings.ToLower(entry.Source)

	score := 0
	if queryLower != "" {
		if strings.Contains(title, queryLower) {
			score += 12
		}
		if strings.Contains(summary, queryLower) {
			score += 10
		}
		if strings.Contains(body, queryLower) {
			score += 4
		}
		if strings.Contains(source, queryLower) {
			score += 3
		}
		for _, tag := range entry.Tags {
			if strings.Contains(strings.ToLower(tag), queryLower) {
				score += 3
			}
		}
	}
	for _, term := range terms {
		if strings.Contains(title, term) {
			score += 6
		}
		if strings.Contains(summary, term) {
			score += 4
		}
		if strings.Contains(body, term) {
			score += 1
		}
		if strings.Contains(source, term) {
			score += 2
		}
		for _, tag := range entry.Tags {
			if strings.Contains(strings.ToLower(tag), term) {
				score += 2
			}
		}
	}
	// Type bonus: work_method and work_fact are process/contextual knowledge
	// that should rank higher when they match the query.
	switch entry.Type {
	case TypeWorkMethod:
		score += 3
	case TypeWorkFact:
		score += 2
	}
	return score
}

func extractTerms(query string) []string {
	terms := make([]string, 0)
	seen := make(map[string]struct{})
	var builder strings.Builder
	flush := func() {
		if builder.Len() == 0 {
			return
		}
		term := strings.ToLower(strings.TrimSpace(builder.String()))
		builder.Reset()
		if len([]rune(term)) < 2 {
			return
		}
		if _, exists := seen[term]; exists {
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}

	for _, r := range query {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r):
			builder.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return terms
}
