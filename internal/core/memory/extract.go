package memory

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/platform/fsutil"
)

const (
	CandidatesFileName       = "candidates.json"
	legacyCandidatesFileName = "memory_candidates.json"
	suppressionsFileName     = "candidate_suppressions.json"
	candidateSuppressionTTL  = 30 * 24 * time.Hour
	maxInsightBridgeAdds     = 4
	maxTimelineBridgeAdds    = 2
)

// Candidate is a suggested durable memory extracted from a completed turn.
type Candidate struct {
	Fingerprint string    `json:"fingerprint"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	Content     string    `json:"content"`
	Type        Type      `json:"memory_type"`
	Source      string    `json:"source"`
	CreatedAt   time.Time `json:"created_at"`
}

// AcceptCandidateInput controls how a pending candidate is promoted into durable memory.
type AcceptCandidateInput struct {
	Fingerprint   string
	AlwaysInclude bool
}

// CandidateSuppression records a dismissed candidate so recurring automation
// does not immediately re-add the same suggestion into the inbox.
type CandidateSuppression struct {
	Fingerprint string    `json:"fingerprint,omitempty"`
	Key         string    `json:"key,omitempty"`
	Source      string    `json:"source,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

// Extractor captures high-value durable memory suggestions without writing them
// directly into the durable memory store.
type Extractor struct {
	manager *Manager
	tempDir string
}

// NewExtractor creates a new turn-end memory extractor.
func NewExtractor(manager *Manager, tempDir string) *Extractor {
	return &Extractor{manager: manager, tempDir: tempDir}
}

// SuggestionsPath returns the on-disk path for pending memory suggestions.
func (e *Extractor) SuggestionsPath() string {
	if e == nil || e.manager == nil {
		return ""
	}
	return e.manager.CandidatesPath()
}

func (e *Extractor) legacySuggestionsPath() string {
	if e == nil || strings.TrimSpace(e.tempDir) == "" {
		return ""
	}
	return filepath.Join(e.tempDir, legacyCandidatesFileName)
}

// Capture extracts and stores new memory candidates, deduping against existing
// durable memory and previously suggested candidates.
func (e *Extractor) Capture(messages []protocol.Message) ([]Candidate, error) {
	if e == nil || e.manager == nil {
		return nil, nil
	}
	if err := e.manager.ensureStore(); err != nil {
		return nil, err
	}

	candidates := extractCandidatesFromMessages(messages)
	return e.captureCandidates(candidates)
}

func (e *Extractor) loadCandidates() ([]Candidate, bool, error) {
	current, err := e.manager.ListCandidates()
	if err != nil {
		return nil, false, err
	}

	legacyPath := e.legacySuggestionsPath()
	if strings.TrimSpace(legacyPath) == "" {
		return current, false, nil
	}
	legacy, err := LoadCandidates(legacyPath)
	if os.IsNotExist(err) {
		return current, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	combined := append(append([]Candidate{}, current...), legacy...)
	return dedupeCandidates(combined), true, nil
}

func (e *Extractor) writeCandidates(candidates []Candidate) error {
	if err := e.manager.writeCandidates(candidates); err != nil {
		return err
	}
	legacyPath := e.legacySuggestionsPath()
	if strings.TrimSpace(legacyPath) == "" {
		return nil
	}
	if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (e *Extractor) loadSuppressions() ([]CandidateSuppression, bool, error) {
	suppressions, err := LoadCandidateSuppressions(e.manager.SuppressionsPath())
	if err != nil {
		return nil, false, err
	}
	pruned := pruneExpiredSuppressions(suppressions, time.Now().UTC())
	if len(pruned) != len(suppressions) {
		return pruned, true, nil
	}
	return pruned, false, nil
}

func extractCandidatesFromMessages(messages []protocol.Message) []Candidate {
	userText := lastPersistentText(messages, protocol.RoleUser)
	assistantText := lastPersistentText(messages, protocol.RoleAssistant)
	if userText == "" && assistantText == "" {
		return nil
	}

	candidates := make([]Candidate, 0, 2)
	if candidate, ok := extractChinesePreference(userText); ok {
		candidates = append(candidates, candidate)
	}
	if candidate, ok := extractGoValidationWorkflow(userText, assistantText); ok {
		candidates = append(candidates, candidate)
	}
	return dedupeCandidates(candidates)
}

// CaptureInsightsReport extracts durable memory suggestions from a structured insights report.
func (e *Extractor) CaptureInsightsReport(report *insights.Report) ([]Candidate, error) {
	return e.captureCandidates(extractCandidatesFromInsightsReport(report))
}

// CaptureTimeline extracts durable memory suggestions from recent runtime timeline events.
func (e *Extractor) CaptureTimeline(items []events.Event) ([]Candidate, error) {
	return e.captureCandidates(extractCandidatesFromTimeline(items))
}

func (e *Extractor) captureCandidates(candidates []Candidate) ([]Candidate, error) {
	if e == nil || e.manager == nil {
		return nil, nil
	}
	if err := e.manager.ensureStore(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	entries, err := e.manager.List()
	if err != nil {
		return nil, err
	}
	existingTitles := make(map[string]struct{}, len(entries))
	existingKeys := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		existingTitles[strings.ToLower(entry.Title)] = struct{}{}
		if key := candidateSemanticKey(entry.Type, entry.Title, entry.Summary, ""); key != "" {
			existingKeys[key] = struct{}{}
		}
	}

	stored, migratedLegacy, err := e.loadCandidates()
	if err != nil {
		return nil, err
	}
	existingFingerprints := make(map[string]struct{}, len(stored))
	for _, candidate := range stored {
		existingFingerprints[candidate.Fingerprint] = struct{}{}
		if key := candidateSemanticKey(candidate.Type, candidate.Title, candidate.Summary, candidate.Content); key != "" {
			existingKeys[key] = struct{}{}
		}
	}

	suppressions, prunedSuppressions, err := e.loadSuppressions()
	if err != nil {
		return nil, err
	}
	suppressedFingerprints := make(map[string]struct{}, len(suppressions))
	suppressedKeys := make(map[string]struct{}, len(suppressions))
	for _, suppression := range suppressions {
		if suppression.Fingerprint != "" {
			suppressedFingerprints[suppression.Fingerprint] = struct{}{}
		}
		if suppression.Key != "" {
			suppressedKeys[suppression.Key] = struct{}{}
		}
	}

	added := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := candidateSemanticKey(candidate.Type, candidate.Title, candidate.Summary, candidate.Content)
		if _, exists := existingTitles[strings.ToLower(candidate.Title)]; exists {
			continue
		}
		if _, exists := existingFingerprints[candidate.Fingerprint]; exists {
			continue
		}
		if _, exists := existingKeys[key]; exists {
			continue
		}
		if _, exists := suppressedFingerprints[candidate.Fingerprint]; exists {
			continue
		}
		if _, exists := suppressedKeys[key]; exists {
			continue
		}
		stored = append(stored, candidate)
		existingFingerprints[candidate.Fingerprint] = struct{}{}
		if key != "" {
			existingKeys[key] = struct{}{}
		}
		added = append(added, candidate)
	}
	if len(added) == 0 && !migratedLegacy && !prunedSuppressions {
		return nil, nil
	}
	if err := e.writeCandidates(stored); err != nil {
		return nil, err
	}
	if prunedSuppressions {
		if err := e.manager.writeSuppressions(suppressions); err != nil {
			return nil, err
		}
	}
	return added, nil
}

func lastPersistentText(messages []protocol.Message, role string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != role {
			continue
		}
		if msg.Metadata != nil && msg.Metadata.Ephemeral {
			continue
		}
		text := strings.TrimSpace(protocol.MessageText(msg))
		if text != "" {
			return text
		}
	}
	return ""
}

func extractChinesePreference(userText string) (Candidate, bool) {
	lower := strings.ToLower(strings.TrimSpace(userText))
	switch {
	case strings.Contains(lower, "请用中文"), strings.Contains(lower, "中文回复"), strings.Contains(lower, "中文回答"):
		content := strings.TrimSpace(userText)
		if content == "" {
			content = "The user prefers Chinese responses."
		}
		return newCandidate(
			"User Preference: Reply in Chinese",
			"Prefer Chinese responses in future sessions.",
			content,
			TypeUser,
			"turn-end-extractor",
		), true
	default:
		return Candidate{}, false
	}
}

func extractGoValidationWorkflow(userText, assistantText string) (Candidate, bool) {
	combined := strings.Join([]string{userText, assistantText}, "\n")
	if !strings.Contains(combined, "go test ./...") {
		return Candidate{}, false
	}

	summary := "Run go test ./... after Go changes."
	contentParts := []string{"Observed workflow guidance from a successful conversation turn."}
	contentParts = append(contentParts, "Run `go test ./...` after Go code changes.")
	if strings.Contains(combined, "go test -race ./...") {
		summary = "Run go test ./... and go test -race ./... after runtime or concurrency-sensitive Go changes."
		contentParts = append(contentParts, "Run `go test -race ./...` after runtime or concurrency-sensitive changes.")
	}
	if strings.Contains(combined, "go build -o godex ./cmd/godex") {
		contentParts = append(contentParts, "Use `go build -o godex ./cmd/godex` as a final binary validation step when appropriate.")
	}

	return newCandidate(
		"Workflow: Validate Go changes",
		summary,
		strings.Join(contentParts, "\n"),
		TypeWorkflow,
		"turn-end-extractor",
	), true
}

func extractCandidatesFromInsightsReport(report *insights.Report) []Candidate {
	if report == nil {
		return nil
	}

	candidates := make([]Candidate, 0, len(report.AgentMDAdditions)+len(report.Frictions))
	for _, item := range report.AgentMDAdditions {
		item = strings.TrimSpace(item)
		switch {
		case strings.HasPrefix(item, "Consider capturing this stable collaboration preference in `.godex/AGENT.local.md`: "):
			summary := strings.TrimPrefix(item, "Consider capturing this stable collaboration preference in `.godex/AGENT.local.md`: ")
			candidates = append(candidates, newInsightCandidate("User Preference", summary, TypeUser, "insights-bridge"))
		case strings.HasPrefix(item, "Consider codifying this recurring workflow in `AGENT.md` or `.godex/rules/*.md`: "):
			summary := strings.TrimPrefix(item, "Consider codifying this recurring workflow in `AGENT.md` or `.godex/rules/*.md`: ")
			candidates = append(candidates, newInsightCandidate("Workflow", summary, TypeWorkflow, "insights-bridge"))
		case strings.HasPrefix(item, "Consider promoting this durable project note into `AGENT.md`: "):
			summary := strings.TrimPrefix(item, "Consider promoting this durable project note into `AGENT.md`: ")
			candidates = append(candidates, newInsightCandidate("Project Note", summary, TypeProject, "insights-bridge"))
		}
	}
	for _, item := range report.Frictions {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		candidates = append(candidates, newInsightCandidate("Runtime Warning", item, TypeWarning, "insights-bridge"))
	}
	return limitCandidates(dedupeCandidates(candidates), maxInsightBridgeAdds)
}

func extractCandidatesFromTimeline(items []events.Event) []Candidate {
	if len(items) == 0 {
		return nil
	}

	timeoutCount := 0
	pathCount := 0
	inactiveToolCount := 0
	for _, item := range items {
		text := strings.ToLower(strings.TrimSpace(timelineEventText(item)))
		if text == "" {
			continue
		}
		if strings.Contains(text, "context deadline exceeded") {
			timeoutCount++
		}
		if strings.Contains(text, "no such file or directory") {
			pathCount++
		}
		if strings.Contains(text, "is not active") {
			inactiveToolCount++
		}
	}

	candidates := make([]Candidate, 0, 3)
	if timeoutCount >= 2 {
		candidates = append(candidates, newInsightCandidate("Runtime Warning", "Model/API timeouts are recurring and should be treated as a first-class runtime friction.", TypeWarning, "timeline-bridge"))
	}
	if pathCount >= 2 {
		candidates = append(candidates, newInsightCandidate("Runtime Warning", "Path resolution and file existence checks remain a recurring source of errors.", TypeWarning, "timeline-bridge"))
	}
	if inactiveToolCount >= 2 {
		candidates = append(candidates, newInsightCandidate("Runtime Warning", "Progressive tool loading is discoverable but still produces inactive-tool friction in practice.", TypeWarning, "timeline-bridge"))
	}
	return limitCandidates(dedupeCandidates(candidates), maxTimelineBridgeAdds)
}

func newInsightCandidate(prefix, summary string, candidateType Type, source string) Candidate {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return Candidate{}
	}
	title := prefix + ": " + shortCandidateTitle(summary)
	return newCandidate(title, summary, summary, candidateType, source)
}

func shortCandidateTitle(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" {
		return "Untitled"
	}
	runes := []rune(text)
	if len(runes) <= 48 {
		return text
	}
	return strings.TrimSpace(string(runes[:48])) + "..."
}

func candidateSemanticKey(candidateType Type, title, summary, content string) string {
	text := strings.TrimSpace(summary)
	if text == "" {
		text = strings.TrimSpace(title)
	}
	if text == "" {
		text = strings.TrimSpace(content)
	}
	text = normalizeCandidateText(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) > 160 {
		text = string(runes[:160])
	}
	return string(candidateType) + "|" + text
}

func normalizeCandidateText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var builder strings.Builder
	lastSpace := false
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r):
			builder.WriteRune(r)
			lastSpace = false
		case unicode.IsSpace(r), unicode.IsPunct(r), unicode.IsSymbol(r):
			if !lastSpace {
				builder.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

func timelineEventText(event events.Event) string {
	switch payload := event.Payload.(type) {
	case events.NoticePayload:
		return payload.Message
	case events.ToolCallPayload:
		if strings.TrimSpace(payload.Error) != "" {
			return payload.Error
		}
		return payload.Output
	case events.TurnPayload:
		return payload.Status
	default:
		return fmt.Sprint(payload)
	}
}

func newCandidate(title, summary, content string, candidateType Type, source string) Candidate {
	fingerprintInput := strings.Join([]string{string(candidateType), title, summary, content}, "\n")
	sum := sha1.Sum([]byte(fingerprintInput))
	return Candidate{
		Fingerprint: hex.EncodeToString(sum[:]),
		Title:       title,
		Summary:     summary,
		Content:     content,
		Type:        candidateType,
		Source:      source,
		CreatedAt:   time.Now().UTC(),
	}
}

func dedupeCandidates(candidates []Candidate) []Candidate {
	if len(candidates) <= 1 {
		return candidates
	}
	result := make([]Candidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.Fingerprint == "" {
			continue
		}
		if _, exists := seen[candidate.Fingerprint]; exists {
			continue
		}
		seen[candidate.Fingerprint] = struct{}{}
		result = append(result, candidate)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}

func limitCandidates(candidates []Candidate, max int) []Candidate {
	if max <= 0 || len(candidates) <= max {
		return candidates
	}
	return append([]Candidate{}, candidates[:max]...)
}

// CandidatesPath returns the durable inbox path for pending memory candidates.
func (m *Manager) CandidatesPath() string {
	return filepath.Join(m.dir, CandidatesFileName)
}

// SuppressionsPath returns the durable path for dismissed-candidate suppressions.
func (m *Manager) SuppressionsPath() string {
	return filepath.Join(m.dir, suppressionsFileName)
}

// ListCandidates returns pending memory suggestions from the durable inbox.
func (m *Manager) ListCandidates() ([]Candidate, error) {
	if err := m.ensureStore(); err != nil {
		return nil, err
	}
	return LoadCandidates(m.CandidatesPath())
}

// ListSuppressions returns dismissed candidate suppressions after pruning
// expired entries from the durable suppression store.
func (m *Manager) ListSuppressions() ([]CandidateSuppression, error) {
	if err := m.ensureStore(); err != nil {
		return nil, err
	}
	suppressions, err := LoadCandidateSuppressions(m.SuppressionsPath())
	if err != nil {
		return nil, err
	}
	pruned := pruneExpiredSuppressions(suppressions, time.Now().UTC())
	if len(pruned) != len(suppressions) {
		if err := m.writeSuppressions(pruned); err != nil {
			return nil, err
		}
	}
	return pruned, nil
}

// AcceptCandidate stores one candidate as durable memory and removes it from the inbox.
func (m *Manager) AcceptCandidate(fingerprint string) (*Entry, error) {
	return m.AcceptCandidateWithOptions(AcceptCandidateInput{Fingerprint: fingerprint})
}

// AcceptCandidateWithOptions stores one candidate as durable memory and allows
// additional promotion options such as pinning it into the stable core layer.
func (m *Manager) AcceptCandidateWithOptions(input AcceptCandidateInput) (*Entry, error) {
	fingerprint := strings.TrimSpace(input.Fingerprint)
	if fingerprint == "" {
		return nil, fmt.Errorf("missing fingerprint")
	}
	candidates, err := m.ListCandidates()
	if err != nil {
		return nil, err
	}

	next := make([]Candidate, 0, len(candidates))
	var match *Candidate
	for i := range candidates {
		candidate := candidates[i]
		if candidate.Fingerprint == fingerprint {
			candidateCopy := candidate
			match = &candidateCopy
			continue
		}
		next = append(next, candidate)
	}
	if match == nil {
		return nil, fmt.Errorf("candidate not found")
	}

	entry, err := m.Remember(SaveInput{
		Title:   match.Title,
		Summary: match.Summary,
		Content: match.Content,
		Type:    match.Type,
		Source:  match.Source,
		Tags:    candidateAcceptTags(input.AlwaysInclude),
	})
	if err != nil {
		return nil, err
	}
	if err := m.writeCandidates(next); err != nil {
		return nil, err
	}
	m.invalidateCandidateCache()
	after, _ := m.currentMemorySnapshot(entry.ID, entry.Title)
	if err := m.appendAudit(AuditLogEntry{
		Action:               AuditAcceptCandidate,
		MemoryID:             entry.ID,
		Title:                entry.Title,
		Type:                 entry.Type,
		Source:               match.Source,
		CandidateFingerprint: match.Fingerprint,
		After:                after,
	}); err != nil {
		return nil, err
	}
	return entry, nil
}

func candidateAcceptTags(alwaysInclude bool) []string {
	if alwaysInclude {
		return []string{"core"}
	}
	return nil
}

// DismissCandidate removes one candidate from the inbox without storing it.
func (m *Manager) DismissCandidate(fingerprint string) (*Candidate, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return nil, fmt.Errorf("missing fingerprint")
	}
	candidates, err := m.ListCandidates()
	if err != nil {
		return nil, err
	}

	next := make([]Candidate, 0, len(candidates))
	var match *Candidate
	for i := range candidates {
		candidate := candidates[i]
		if candidate.Fingerprint == fingerprint {
			candidateCopy := candidate
			match = &candidateCopy
			continue
		}
		next = append(next, candidate)
	}
	if match == nil {
		return nil, fmt.Errorf("candidate not found")
	}
	if err := m.writeCandidates(next); err != nil {
		return nil, err
	}
	m.invalidateCandidateCache()
	if err := m.recordCandidateSuppression(*match); err != nil {
		return nil, err
	}
	if err := m.appendAudit(AuditLogEntry{
		Action:               AuditDismissCandidate,
		Title:                match.Title,
		Type:                 match.Type,
		Source:               match.Source,
		CandidateFingerprint: match.Fingerprint,
		Message:              match.Summary,
	}); err != nil {
		return nil, err
	}
	return match, nil
}

func (m *Manager) writeCandidates(candidates []Candidate) error {
	if err := m.ensureStore(); err != nil {
		return err
	}
	if len(candidates) == 0 {
		m.invalidateCandidateCache()
		return fsutil.WriteFileAtomic(m.CandidatesPath(), []byte("[]\n"), 0644)
	}
	data, err := json.MarshalIndent(dedupeCandidates(candidates), "", "  ")
	if err != nil {
		return err
	}
	m.invalidateCandidateCache()
	return fsutil.WriteFileAtomic(m.CandidatesPath(), append(data, '\n'), 0644)
}

func (m *Manager) recordCandidateSuppression(candidate Candidate) error {
	suppressions, err := LoadCandidateSuppressions(m.SuppressionsPath())
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	suppression := CandidateSuppression{
		Fingerprint: candidate.Fingerprint,
		Key:         candidateSemanticKey(candidate.Type, candidate.Title, candidate.Summary, candidate.Content),
		Source:      candidate.Source,
		CreatedAt:   now,
		ExpiresAt:   now.Add(candidateSuppressionTTL),
	}
	suppressions = append(pruneExpiredSuppressions(suppressions, now), suppression)
	return m.writeSuppressions(suppressions)
}

func (m *Manager) writeSuppressions(suppressions []CandidateSuppression) error {
	if err := m.ensureStore(); err != nil {
		return err
	}
	if len(suppressions) == 0 {
		return fsutil.WriteFileAtomic(m.SuppressionsPath(), []byte("[]\n"), 0644)
	}
	data, err := json.MarshalIndent(dedupeSuppressions(suppressions), "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(m.SuppressionsPath(), append(data, '\n'), 0644)
}

func LoadCandidates(path string) ([]Candidate, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var candidates []Candidate
	if err := json.Unmarshal(data, &candidates); err != nil {
		return nil, err
	}
	return dedupeCandidates(candidates), nil
}

// LoadCandidateSuppressions loads persisted candidate suppressions from disk.
func LoadCandidateSuppressions(path string) ([]CandidateSuppression, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var suppressions []CandidateSuppression
	if err := json.Unmarshal(data, &suppressions); err != nil {
		return nil, err
	}
	return dedupeSuppressions(suppressions), nil
}

func pruneExpiredSuppressions(suppressions []CandidateSuppression, now time.Time) []CandidateSuppression {
	if len(suppressions) == 0 {
		return nil
	}
	kept := make([]CandidateSuppression, 0, len(suppressions))
	for _, suppression := range suppressions {
		if !suppression.ExpiresAt.IsZero() && !suppression.ExpiresAt.After(now) {
			continue
		}
		kept = append(kept, suppression)
	}
	return dedupeSuppressions(kept)
}

func dedupeSuppressions(suppressions []CandidateSuppression) []CandidateSuppression {
	if len(suppressions) <= 1 {
		return suppressions
	}
	result := make([]CandidateSuppression, 0, len(suppressions))
	seen := make(map[string]struct{}, len(suppressions))
	for i := len(suppressions) - 1; i >= 0; i-- {
		suppression := suppressions[i]
		key := suppression.Key
		if key == "" {
			key = suppression.Fingerprint
		}
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, suppression)
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}

func FormatCandidates(candidates []Candidate) string {
	lines := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		lines = append(lines, fmt.Sprintf("- [%s] %s — %s", candidate.Type, candidate.Title, candidate.Summary))
	}
	return strings.Join(lines, "\n")
}
