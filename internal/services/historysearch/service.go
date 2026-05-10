package historysearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/history"
)

type Service struct {
	sessionsDir    string
	transcriptsDir string
}

type boundRuntime struct {
	service *Service
	current history.Current
}

type storedSessionState struct {
	Messages       []protocol.Message `json:"messages"`
	TranscriptRefs []string           `json:"transcript_refs,omitempty"`
}

type storedManifest struct {
	SessionID string    `json:"session_id"`
	Title     string    `json:"title,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type searchableEntry struct {
	sourceKind   string
	sessionID    string
	sessionTitle string
	timestamp    time.Time
	role         string
	text         string
}

type transcriptOwner struct {
	sessionID string
	title     string
}

type sessionArchiveRefs struct {
	refs           []string
	normalizedRefs []string
	owner          transcriptOwner
}

func NewService(cfg *config.Config) *Service {
	if cfg == nil {
		return &Service{}
	}
	return &Service{
		sessionsDir:    strings.TrimSpace(cfg.SessionsDir),
		transcriptsDir: strings.TrimSpace(cfg.TranscriptsDir),
	}
}

func (s *Service) Bind(current history.Current) history.Runtime {
	return &boundRuntime{service: s, current: current}
}

func (r *boundRuntime) SearchHistory(ctx context.Context, sessionID string, runtimeCtx automation.SessionContext, req history.SearchRequest) (history.SearchResult, error) {
	return r.service.search(ctx, r.current, sessionID, runtimeCtx, req)
}

func (s *Service) search(_ context.Context, current history.Current, sessionID string, _ automation.SessionContext, req history.SearchRequest) (history.SearchResult, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return history.SearchResult{}, fmt.Errorf("missing query")
	}
	scope := normalizeHistorySearchScope(req.Scope)
	limit := req.Limit
	if limit <= 0 {
		limit = 5
	}
	role := normalizeSearchRole(req.Role)
	var entries []searchableEntry
	switch scope {
	case history.HistorySearchScopeCurrentSession:
		items, err := s.currentSessionEntries(current, strings.TrimSpace(sessionID))
		if err != nil {
			return history.SearchResult{}, err
		}
		entries = items
	case history.HistorySearchScopeSessionArchive:
		items, ok, err := s.sessionArchiveEntriesFromSidecar(current, strings.TrimSpace(sessionID), query, role)
		if err != nil {
			return history.SearchResult{}, err
		}
		if ok {
			entries = items
			break
		}
		fallbackItems, err := s.sessionArchiveEntries(current, strings.TrimSpace(sessionID))
		if err != nil {
			return history.SearchResult{}, err
		}
		entries = fallbackItems
	case history.HistorySearchScopeAllArchives:
		items, ok, err := s.allArchiveEntriesFromSidecar(query, role)
		if err != nil {
			return history.SearchResult{}, err
		}
		if ok {
			entries = items
			break
		}
		fallbackItems, err := s.allArchiveEntries()
		if err != nil {
			return history.SearchResult{}, err
		}
		entries = fallbackItems
	default:
		return history.SearchResult{}, fmt.Errorf("unsupported history search scope %q", req.Scope)
	}

	results := scoreHistoryEntries(entries, query, role)
	out := history.SearchResult{
		Scope:      scope,
		MatchCount: len(results),
	}
	if len(results) > limit {
		results = results[:limit]
	}
	out.Snippets = results
	return out, nil
}

func (s *Service) currentSessionEntries(current history.Current, sessionID string) ([]searchableEntry, error) {
	var messages []protocol.Message
	if current != nil {
		messages = current.GetMessages()
	}
	if len(messages) == 0 && sessionID != "" {
		state, _, err := s.readSessionFiles(sessionID)
		if err != nil {
			return nil, err
		}
		messages = state.Messages
	}
	if len(messages) == 0 {
		return nil, nil
	}
	_, manifest, _ := s.readSessionFiles(sessionID)
	title := deriveSessionTitle(messages)
	timestamp := time.Time{}
	if manifest != nil {
		if strings.TrimSpace(manifest.Title) != "" {
			title = strings.TrimSpace(manifest.Title)
		}
		timestamp = manifest.UpdatedAt
	}
	return visibleEntries("current_session", sessionID, title, timestamp, messages), nil
}

func (s *Service) sessionArchiveEntries(current history.Current, sessionID string) ([]searchableEntry, error) {
	archive := s.collectSessionArchiveRefs(current, sessionID)
	if len(archive.refs) == 0 {
		return nil, nil
	}

	entries := make([]searchableEntry, 0)
	for _, ref := range archive.refs {
		path := s.transcriptPath(ref)
		messages, modifiedAt, err := readTranscriptMessages(path)
		if err != nil {
			continue
		}
		entries = append(entries, visibleEntries("transcript", archive.owner.sessionID, archive.owner.title, modifiedAt, messages)...)
	}
	return entries, nil
}

func (s *Service) allArchiveEntries() ([]searchableEntry, error) {
	entries := make([]searchableEntry, 0)
	transcriptOwners := make(map[string]transcriptOwner)
	if strings.TrimSpace(s.sessionsDir) != "" {
		dirs, err := os.ReadDir(s.sessionsDir)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, dir := range dirs {
			if !dir.IsDir() {
				continue
			}
			sessionID := strings.TrimSpace(dir.Name())
			state, manifest, err := s.readSessionFiles(sessionID)
			if err != nil {
				continue
			}
			title, timestamp, refs := sessionArchiveMetadata(state, manifest, time.Time{})
			entries = append(entries, visibleEntries("session_state", sessionID, title, timestamp, state.Messages)...)
			for _, ref := range refs {
				transcriptOwners[ref] = transcriptOwner{sessionID: sessionID, title: title}
			}
		}
	}

	if strings.TrimSpace(s.transcriptsDir) == "" {
		return entries, nil
	}
	files, err := os.ReadDir(s.transcriptsDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasPrefix(file.Name(), "transcript_") || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.transcriptsDir, file.Name())
		messages, modifiedAt, err := readTranscriptMessages(path)
		if err != nil {
			continue
		}
		owner := transcriptOwners[file.Name()]
		title := owner.title
		if title == "" {
			title = deriveSessionTitle(messages)
		}
		entries = append(entries, visibleEntries("transcript", owner.sessionID, title, modifiedAt, messages)...)
	}
	return entries, nil
}

func (s *Service) collectSessionArchiveRefs(current history.Current, sessionID string) sessionArchiveRefs {
	sessionID = strings.TrimSpace(sessionID)
	refs := make([]string, 0)
	messages := make([]protocol.Message, 0)
	if current != nil {
		messages = current.GetMessages()
		refs = append(refs, current.TranscriptRefs()...)
	}

	title := ""
	if sessionID != "" {
		state, manifest, err := s.readSessionFiles(sessionID)
		if err == nil {
			if len(messages) == 0 {
				messages = state.Messages
			}
			refs = append(refs, state.TranscriptRefs...)
			if manifest != nil {
				title = strings.TrimSpace(manifest.Title)
			}
		}
	}

	refs = append(refs, extractTranscriptRefs(messages)...)
	return sessionArchiveRefs{
		refs:           uniqueStrings(refs),
		normalizedRefs: normalizeTranscriptRefs(refs),
		owner:          transcriptOwner{sessionID: sessionID, title: title},
	}
}

func sessionArchiveMetadata(state *storedSessionState, manifest *storedManifest, defaultTimestamp time.Time) (string, time.Time, []string) {
	var messages []protocol.Message
	refs := make([]string, 0)
	if state != nil {
		messages = state.Messages
		refs = append(refs, state.TranscriptRefs...)
	}

	title := deriveSessionTitle(messages)
	timestamp := defaultTimestamp
	if manifest != nil {
		if strings.TrimSpace(manifest.Title) != "" {
			title = strings.TrimSpace(manifest.Title)
		}
		if !manifest.UpdatedAt.IsZero() {
			timestamp = manifest.UpdatedAt
		}
	}

	refs = append(refs, extractTranscriptRefs(messages)...)
	return title, timestamp, normalizeTranscriptRefs(refs)
}

func (s *Service) readSessionFiles(sessionID string) (*storedSessionState, *storedManifest, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(s.sessionsDir) == "" {
		return &storedSessionState{}, nil, nil
	}
	dir := filepath.Join(s.sessionsDir, sessionID)
	stateData, stateErr := os.ReadFile(filepath.Join(dir, "state.json"))
	manifestData, manifestErr := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if stateErr != nil && manifestErr != nil {
		if os.IsNotExist(stateErr) || os.IsNotExist(manifestErr) {
			return &storedSessionState{}, nil, nil
		}
		return nil, nil, stateErr
	}

	state := &storedSessionState{}
	if stateErr == nil {
		if err := json.Unmarshal(stateData, state); err != nil {
			return nil, nil, err
		}
	}
	var manifest *storedManifest
	if manifestErr == nil {
		decoded := &storedManifest{}
		if err := json.Unmarshal(manifestData, decoded); err != nil {
			return nil, nil, err
		}
		manifest = decoded
	}
	return state, manifest, nil
}

func (s *Service) transcriptPath(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if filepath.IsAbs(ref) {
		return ref
	}
	if strings.TrimSpace(s.transcriptsDir) == "" {
		return ref
	}
	return filepath.Join(s.transcriptsDir, filepath.Base(ref))
}

func visibleEntries(sourceKind, sessionID, sessionTitle string, timestamp time.Time, messages []protocol.Message) []searchableEntry {
	entries := make([]searchableEntry, 0, len(messages))
	for _, msg := range messages {
		text, ok := visibleMessageText(msg)
		if !ok {
			continue
		}
		entries = append(entries, searchableEntry{
			sourceKind:   sourceKind,
			sessionID:    strings.TrimSpace(sessionID),
			sessionTitle: strings.TrimSpace(sessionTitle),
			timestamp:    timestamp,
			role:         strings.TrimSpace(msg.Role),
			text:         text,
		})
	}
	return entries
}

func visibleMessageText(msg protocol.Message) (string, bool) {
	if msg.Metadata != nil {
		if msg.Metadata.Ephemeral {
			return "", false
		}
		switch msg.Metadata.Kind {
		case protocol.KindMemory, protocol.KindInbox, protocol.KindBackground:
			return "", false
		}
	}
	parts := make([]string, 0, 2)
	if text := strings.TrimSpace(protocol.MessageText(msg)); text != "" {
		parts = append(parts, text)
	}
	if msg.Metadata != nil && len(msg.Metadata.Attachments) > 0 {
		names := make([]string, 0, len(msg.Metadata.Attachments))
		for _, attachment := range msg.Metadata.Attachments {
			name := strings.TrimSpace(attachment.Name)
			if name == "" {
				name = filepath.Base(strings.TrimSpace(attachment.Path))
			}
			if name != "" {
				names = append(names, name)
			}
		}
		if len(names) > 0 {
			parts = append(parts, "attachments: "+strings.Join(names, ", "))
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n\n"), true
}

func scoreHistoryEntries(entries []searchableEntry, query, role string) []history.SearchSnippet {
	normalizedQuery := normalizeHistorySearchText(query)
	terms := queryTerms(query)
	results := make([]history.SearchSnippet, 0)
	for _, entry := range entries {
		if role != "any" && entry.role != role {
			continue
		}
		score, matched := historyMatchScore(entry.text, normalizedQuery, terms)
		if score <= 0 {
			continue
		}
		results = append(results, history.SearchSnippet{
			SourceKind:   entry.sourceKind,
			SessionID:    entry.sessionID,
			SessionTitle: entry.sessionTitle,
			Timestamp:    entry.timestamp,
			Role:         entry.role,
			TextExcerpt:  excerptAroundMatch(entry.text, matched, 240),
			MatchTerms:   matched,
			Score:        score,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if !results[i].Timestamp.Equal(results[j].Timestamp) {
			return results[i].Timestamp.After(results[j].Timestamp)
		}
		if results[i].SourceKind != results[j].SourceKind {
			return results[i].SourceKind < results[j].SourceKind
		}
		if results[i].SessionID != results[j].SessionID {
			return results[i].SessionID < results[j].SessionID
		}
		return results[i].TextExcerpt < results[j].TextExcerpt
	})
	return results
}

func historyMatchScore(text, normalizedQuery string, terms []string) (int, []string) {
	haystack := normalizeHistorySearchText(text)
	if haystack == "" {
		return 0, nil
	}
	matched := make([]string, 0, len(terms)+1)
	score := 0
	if normalizedQuery != "" && strings.Contains(haystack, normalizedQuery) {
		score += 4
		matched = append(matched, normalizedQuery)
	}
	for _, term := range terms {
		if term == "" || term == normalizedQuery {
			continue
		}
		if strings.Contains(haystack, term) {
			score += 2
			matched = append(matched, term)
		}
	}
	if score == 0 {
		return 0, nil
	}
	return score, uniqueStrings(matched)
}

func readTranscriptMessages(path string) ([]protocol.Message, time.Time, error) {
	if strings.TrimSpace(path) == "" {
		return nil, time.Time{}, fmt.Errorf("missing transcript path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	var messages []protocol.Message
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, time.Time{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return messages, time.Time{}, nil
	}
	return messages, info.ModTime(), nil
}

func extractTranscriptRefs(messages []protocol.Message) []string {
	refs := make([]string, 0)
	for _, msg := range messages {
		if msg.Metadata == nil {
			continue
		}
		ref := strings.TrimSpace(msg.Metadata.Transcript)
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	return uniqueStrings(refs)
}

func deriveSessionTitle(messages []protocol.Message) string {
	for _, msg := range messages {
		if text := strings.TrimSpace(protocol.MessageText(msg)); text != "" {
			return summarizeTitle(text)
		}
		if msg.Metadata == nil {
			continue
		}
		for _, attachment := range msg.Metadata.Attachments {
			if name := strings.TrimSpace(attachment.Name); name != "" {
				return summarizeTitle(name)
			}
		}
	}
	return ""
}

func summarizeTitle(raw string) string {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\n", " "))
	runes := []rune(raw)
	if len(runes) <= 18 {
		return raw
	}
	return string(runes[:18]) + "…"
}

func normalizeHistorySearchScope(scope string) string {
	switch strings.TrimSpace(scope) {
	case "", history.HistorySearchScopeCurrentSession:
		return history.HistorySearchScopeCurrentSession
	case history.HistorySearchScopeSessionArchive:
		return history.HistorySearchScopeSessionArchive
	case history.HistorySearchScopeAllArchives:
		return history.HistorySearchScopeAllArchives
	default:
		return strings.TrimSpace(scope)
	}
}

func normalizeSearchRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "any":
		return "any"
	case "user":
		return protocol.RoleUser
	case "assistant":
		return protocol.RoleAssistant
	default:
		return strings.ToLower(strings.TrimSpace(role))
	}
}

func normalizeHistorySearchText(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func queryTerms(query string) []string {
	parts := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(query)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	if len(parts) == 0 {
		return nil
	}
	return uniqueStrings(parts)
}

func excerptAroundMatch(text string, matchTerms []string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" || limit <= 0 {
		return ""
	}
	clamp := func(value string) string {
		value = strings.TrimSpace(value)
		runes := []rune(value)
		if len(runes) <= limit {
			return value
		}
		if limit == 1 {
			return "…"
		}
		return string(runes[:limit-1]) + "…"
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	lower := strings.ToLower(text)
	index := -1
	match := ""
	for _, term := range matchTerms {
		if term == "" {
			continue
		}
		if idx := strings.Index(lower, term); idx >= 0 {
			index = idx
			match = term
			break
		}
	}
	if index < 0 {
		return clamp(string(runes[:limit]))
	}
	prefixRunes := utf8.RuneCountInString(text[:index])
	matchRunes := utf8.RuneCountInString(match)
	start := prefixRunes - limit/3
	if start < 0 {
		start = 0
	}
	end := start + limit
	if end < prefixRunes+matchRunes {
		end = prefixRunes + matchRunes
		start = end - limit
		if start < 0 {
			start = 0
		}
	}
	if end > len(runes) {
		end = len(runes)
	}
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(runes) {
		snippet += "…"
	}
	return clamp(snippet)
}

func uniqueStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
