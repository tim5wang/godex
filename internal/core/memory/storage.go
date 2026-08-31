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
	"time"
	"unicode"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

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
	case TypeIdentity, TypeUser, TypeWorkflow, TypeProject, TypeWarning,
		TypeWorkMethod, TypeWorkFact:
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
		"Status: " + string(normalizeStatus(entry.Status)),
	}
	if !entry.LastReferencedAt.IsZero() {
		lines = append(lines, "LastReferenced: "+entry.LastReferencedAt.Format(time.RFC3339))
	}
	lines = append(lines, "", strings.TrimSpace(content))
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
		case "status":
			record.Status = normalizeStatus(Status(strings.ToLower(value)))
		case "lastreferenced", "last_referenced":
			if ts, err := time.Parse(time.RFC3339, value); err == nil {
				record.LastReferencedAt = ts
			}
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

// normalizeStatus coerces a raw status string to a valid lifecycle state.
// Unknown or empty values fall back to active so archived must be explicitly
// written, never accidentally preserved from a typo.
func normalizeStatus(value Status) Status {
	switch value {
	case StatusArchived:
		return StatusArchived
	default:
		return StatusActive
	}
}

// entryStatusMatches reports whether an entry satisfies a search status filter.
// Empty status means active-only (the default recall behavior); StatusArchived
// matches only archived; StatusAll matches both.
func entryStatusMatches(entry Entry, filter Status) bool {
	switch filter {
	case StatusAll:
		return true
	case StatusArchived:
		return normalizeStatus(entry.Status) == StatusArchived
	default: // "" or StatusActive
		return normalizeStatus(entry.Status) != StatusArchived
	}
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
