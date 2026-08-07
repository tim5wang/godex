package backend

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/platform/stringutil"
	"github.com/tim5wang/godex/internal/sessiongraph"
	"github.com/tim5wang/godex/internal/tools"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Service) displayMessages(messages []protocol.Message) []protocol.Message {
	return snapshotDisplayMessages(messages)
}

// snapshotDisplayMessages returns the message view shipped in session
// snapshots. Two concerns drove this design:
//
//  1. reasoning_content is model-internal (the UIs render neither the
//     metadata field nor thinking blocks) and can dominate the payload —
//     a long session holds a full reasoning transcript per assistant
//     message. Stripping it here cuts the snapshot dramatically.
//  2. transcripts are no longer expanded inline. A compacted session's
//     transcript can hold thousands of messages (10+ MB of JSON), which
//     made every snapshot — and every snapshot refresh during a running
//     turn — expensive on remote/relayed connections. The compacted
//     summary message still renders its summary text; the full archive
//     stays available on disk for history search and on-demand recall.
func snapshotDisplayMessages(messages []protocol.Message) []protocol.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]protocol.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Metadata != nil && strings.TrimSpace(msg.Metadata.ReasoningContent) != "" {
			msg = msg.Clone()
			msg.Metadata.ReasoningContent = ""
		}
		out = append(out, msg)
	}
	return out
}

func activePermissionBlocker(pending []tools.PendingPermission, turns []TurnRecord, now time.Time) *PermissionBlocker {
	// Only consider permissions that are still actually pending (not approved/denied/expired).
	var stillPending []tools.PendingPermission
	for _, p := range pending {
		if p.Status == "" || p.Status == tools.PermissionStatusPending {
			stillPending = append(stillPending, p)
		}
	}
	if len(stillPending) == 0 {
		return nil
	}
	item := stillPending[0]
	for _, candidate := range stillPending {
		if candidate.CreatedAt.Before(item.CreatedAt) {
			item = candidate
		}
	}
	turnID := ""
	for i := len(turns) - 1; i >= 0; i-- {
		if strings.TrimSpace(turns[i].BlockedByPermissionID) == strings.TrimSpace(item.ID) || strings.TrimSpace(turns[i].PendingRequestID) == strings.TrimSpace(item.ID) {
			turnID = strings.TrimSpace(turns[i].ID)
			break
		}
	}
	status := item.Status
	if status == "" {
		status = tools.PermissionStatusPending
	}
	return &PermissionBlocker{
		RequestID: strings.TrimSpace(item.ID),
		Status:    status,
		TurnID:    turnID,
		Intent:    strings.TrimSpace(tools.PermissionIntentSummary(item)),
		Risk:      strings.TrimSpace(tools.PermissionRiskSummary(item.Request)),
		Expiry:    strings.TrimSpace(tools.PermissionExpirySummary(item, now)),
		ToolName:  strings.TrimSpace(item.Request.ToolName),
		Action:    strings.TrimSpace(item.Request.Action),
		Command:   strings.TrimSpace(item.Request.Command),
		Paths:     append([]string{}, item.Request.Paths...),
		Source:    strings.TrimSpace(item.Request.Source),
		Sender:    strings.TrimSpace(item.Request.Sender),
		CreatedAt: item.CreatedAt,
		ExpiresAt: item.ExpiresAt,
	}
}

func (s *Service) mainCapabilitySummary() []string {
	// Session-level identity uses a compact summary; detailed execution policy
	// remains enforced by the existing tool permission manager.
	return []string{"tool:*", "file:read:*", "file:write:workspace", "shell:approval_policy", "network:configured_tools"}
}

func cloneMapStringString(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func (s *Service) touchSession(session *sessionState, updatedAt time.Time) error {
	session.mu.Lock()
	session.updatedAt = updatedAt
	session.lastActive = updatedAt
	running := session.running
	session.mu.Unlock()

	if err := s.persistSession(session, updatedAt); err != nil {
		return err
	}

	session.events.Emit(events.Event{
		SessionID: session.id,
		Type:      events.EventSnapshotReady,
		Timestamp: updatedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: updatedAt,
			Running:   running,
		},
	})
	_ = s.writeSessionTimeline(session)
	return nil
}

func (s *Service) describeSession(session *sessionState) *OpenedSession {
	session.mu.RLock()
	defer session.mu.RUnlock()

	return &OpenedSession{
		SessionID:              session.id,
		Locator:                session.locator,
		ModelProfileID:         strings.TrimSpace(session.modelProfileID),
		ReasoningEffort:        normalizeSessionReasoningEffort(session.reasoningEffort),
		ParentSessionID:        strings.TrimSpace(session.parentSessionID),
		ForkedFromTurnID:       strings.TrimSpace(session.forkedFromTurnID),
		ForkedFromMessageIndex: cloneIntPtr(session.forkedFromMessageIndex),
		BranchTitle:            strings.TrimSpace(session.branchTitle),
		CreatedAt:              session.createdAt,
		UpdatedAt:              session.updatedAt,
	}
}

func (s *sessionState) setTitleIfEmpty(title string) {
	title = strings.TrimSpace(title)
	if title == "" || title == "New chat" {
		// Never overwrite with the placeholder; it carries no information.
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.title) != "" && strings.TrimSpace(s.title) != "New chat" {
		return
	}
	s.title = title
}

// maybeGenerateTitleAsync fires an async LLM call to generate a better title
// when the first user message is received. On success the session title and
// manifest are updated. Best-effort: failures and panics are silently ignored.
func (s *Service) maybeGenerateTitleAsync(session *sessionState, envelope message.Envelope) {
	if session == nil || session.agent == nil {
		return
	}
	firstMessage := strings.TrimSpace(envelope.BodyText())
	if firstMessage == "" {
		return
	}
	go func() {
		defer func() { recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Additional recover because client.Call may panic in test stubs
		var title string
		func() {
			defer func() { recover() }()
			t, err := session.agent.GenerateTitle(ctx, firstMessage)
			if err == nil {
				title = t
			}
		}()
		if title == "" {
			return
		}
		session.mu.Lock()
		session.title = title
		session.mu.Unlock()
		s.writeManifestForSession(session)
	}()
}

func (s *Service) writeManifestForSession(session *sessionState) {
	if session == nil || s == nil {
		return
	}
	session.mu.RLock()
	manifest := SessionManifest{
		SessionID:              session.id,
		Locator:                session.locator,
		Identity:               session.identity,
		Title:                  session.title,
		ModelProfileID:         session.modelProfileID,
		ReasoningEffort:        session.reasoningEffort,
		ParentSessionID:        session.parentSessionID,
		ForkedFromTurnID:       session.forkedFromTurnID,
		ForkedFromMessageIndex: session.forkedFromMessageIndex,
		BranchTitle:            session.branchTitle,
		StateDigest:            "",
		CreatedAt:              session.createdAt,
		UpdatedAt:              session.updatedAt,
		LastActivityAt:         session.lastActive,
	}
	session.mu.RUnlock()
	_ = s.writeManifest(manifest)
}

func (s *Service) sessionDir(sessionID string) string {
	return filepath.Join(s.cfg.SessionsDir, sessionID)
}

func (s *Service) sessionAttachmentsDir(sessionID string) string {
	return filepath.Join(s.sessionDir(sessionID), attachmentsDir)
}

func (s *Service) deleteUniqueTranscriptRefs(sessionID string, refs []string) error {
	if len(refs) == 0 || strings.TrimSpace(s.cfg.TranscriptsDir) == "" {
		return nil
	}
	others := s.collectTranscriptRefs(sessionID)
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, shared := others[ref]; shared {
			continue
		}
		name := filepath.Base(ref)
		if name == "." || name == "" || name != ref {
			continue
		}
		path := filepath.Join(s.cfg.TranscriptsDir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Service) collectTranscriptRefs(excludeSessionID string) map[string]struct{} {
	refs := make(map[string]struct{})

	s.mu.Lock()
	for id, session := range s.sessions {
		if id == excludeSessionID || session == nil {
			continue
		}
		for _, ref := range session.agent.TranscriptRefs() {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			refs[ref] = struct{}{}
		}
	}
	s.mu.Unlock()

	entries, err := os.ReadDir(s.cfg.SessionsDir)
	if err != nil {
		return refs
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == excludeSessionID {
			continue
		}
		state, err := s.readSessionState(entry.Name())
		if err != nil {
			continue
		}
		for _, ref := range sessionTranscriptRefs(state) {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			refs[ref] = struct{}{}
		}
	}
	return refs
}

func sessionTranscriptRefs(state *agent.SessionState) []string {
	if state == nil {
		return nil
	}
	return stringutil.Unique(append([]string{}, state.TranscriptRefs...))
}

func cloneInstallMemory(memory *skill.InstallMemory) *skill.InstallMemory {
	if memory == nil {
		return nil
	}
	cloned := *memory
	cloned.Categories = append([]string{}, memory.Categories...)
	return &cloned
}

func (s *Service) relativePath(path string) string {
	if rel, err := filepath.Rel(s.cfg.WorkspaceDir, path); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func eventReplayKey(event events.Event) string {
	return fmt.Sprintf("%s|%s|%s", event.Type, event.TurnID, event.Timestamp.Format(time.RFC3339Nano))
}

func normalizeTurnRecords(records []TurnRecord) []TurnRecord {
	out := make([]TurnRecord, 0, len(records))
	seen := make(map[string]int, len(records))
	for _, record := range records {
		record.ID = strings.TrimSpace(record.ID)
		if record.ID == "" {
			continue
		}
		record.Status = strings.TrimSpace(record.Status)
		if record.Status == "" {
			record.Status = "unknown"
		}
		record.Source = strings.TrimSpace(record.Source)
		record.Sender = strings.TrimSpace(record.Sender)
		record.Summary = turnSummary(record.Summary)
		record.PendingRequestID = strings.TrimSpace(record.PendingRequestID)
		record.BlockedByPermissionID = strings.TrimSpace(record.BlockedByPermissionID)
		record.Error = strings.TrimSpace(record.Error)
		record.RetryOf = strings.TrimSpace(record.RetryOf)
		record.CanRetry = false
		record.CanResume = false
		if record.UpdatedAt.IsZero() {
			record.UpdatedAt = record.StartedAt
		}
		if record.Envelope != nil {
			normalized := record.Envelope.Normalized()
			record.Envelope = &normalized
		}
		if idx, ok := seen[record.ID]; ok {
			out[idx] = mergeTurnRecord(out[idx], record)
			continue
		}
		seen[record.ID] = len(out)
		out = append(out, record)
	}
	return trimTurnRecords(out, persistedTurnLimit)
}

func normalizeQueuedTurns(items []QueuedTurn) []QueuedTurn {
	out := make([]QueuedTurn, 0, len(items))
	for _, item := range items {
		normalized := normalizeQueuedTurn(item)
		if strings.TrimSpace(normalized.ID) == "" {
			continue
		}
		out = append(out, normalized)
	}
	return out
}

func normalizeQueuedTurn(item QueuedTurn) QueuedTurn {
	item.ID = strings.TrimSpace(item.ID)
	item.Mode = normalizeQueueMode(item.Mode)
	item.Status = strings.TrimSpace(item.Status)
	if item.Status == "" {
		item.Status = "queued"
	}
	item.Source = strings.TrimSpace(item.Source)
	item.Sender = strings.TrimSpace(item.Sender)
	item.Summary = turnSummary(item.Summary)
	item.Envelope = item.Envelope.Normalized()
	if item.Source == "" {
		item.Source = string(item.Envelope.Source)
	}
	if item.Sender == "" {
		item.Sender = strings.TrimSpace(item.Envelope.Sender)
	}
	if item.Summary == "" {
		item.Summary = turnSummary(item.Envelope.BodyText())
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	return item
}

func cloneQueuedTurn(item QueuedTurn) QueuedTurn {
	item.Envelope = item.Envelope.Normalized()
	return item
}

func cloneQueuedTurns(items []QueuedTurn) []QueuedTurn {
	out := make([]QueuedTurn, len(items))
	for i, item := range items {
		out[i] = cloneQueuedTurn(item)
	}
	return out
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func mergeTurnRecord(existing, next TurnRecord) TurnRecord {
	merged := existing
	if next.Status != "" {
		merged.Status = next.Status
	}
	if next.Source != "" {
		merged.Source = next.Source
	}
	if next.Sender != "" {
		merged.Sender = next.Sender
	}
	if next.Summary != "" {
		merged.Summary = next.Summary
	}
	if merged.StartedAt.IsZero() || (!next.StartedAt.IsZero() && next.StartedAt.Before(merged.StartedAt)) {
		merged.StartedAt = next.StartedAt
	}
	if next.UpdatedAt.After(merged.UpdatedAt) || merged.UpdatedAt.IsZero() {
		merged.UpdatedAt = next.UpdatedAt
	}
	if next.CompletedAt != nil {
		completedAt := *next.CompletedAt
		merged.CompletedAt = &completedAt
	}
	if next.PendingRequestID != "" {
		merged.PendingRequestID = next.PendingRequestID
	}
	if next.BlockedByPermissionID != "" {
		merged.BlockedByPermissionID = next.BlockedByPermissionID
	}
	if next.PermissionStatus != "" {
		merged.PermissionStatus = next.PermissionStatus
	}
	if next.Error != "" {
		merged.Error = next.Error
	}
	if next.RetryOf != "" {
		merged.RetryOf = next.RetryOf
	}
	if next.ResumeAvailable {
		merged.ResumeAvailable = true
	}
	if next.RecoveryHint != "" {
		merged.RecoveryHint = next.RecoveryHint
	}
	if next.Phase != "" {
		merged.Phase = next.Phase
	}
	if next.InjectionCount != 0 {
		merged.InjectionCount = next.InjectionCount
	}
	if next.LastToolName != "" {
		merged.LastToolName = next.LastToolName
	}
	if next.Envelope != nil {
		envelope := next.Envelope.Normalized()
		merged.Envelope = &envelope
	}
	if len(next.Injections) > 0 {
		merged.Injections = append([]message.Envelope{}, next.Injections...)
	}
	if next.Envelope != nil || next.PriorMessageCount != 0 {
		merged.PriorMessageCount = next.PriorMessageCount
	}
	return merged
}

func cloneTurnRecord(record TurnRecord) TurnRecord {
	cloned := record
	if record.CompletedAt != nil {
		completedAt := *record.CompletedAt
		cloned.CompletedAt = &completedAt
	}
	if record.Envelope != nil {
		envelope := record.Envelope.Normalized()
		cloned.Envelope = &envelope
	}
	if len(record.Injections) > 0 {
		cloned.Injections = append([]message.Envelope{}, record.Injections...)
	}
	return cloned
}

func cloneTurnRecords(records []TurnRecord) []TurnRecord {
	out := make([]TurnRecord, len(records))
	for i, record := range records {
		out[i] = cloneTurnRecord(record)
	}
	return out
}

func trimTurnRecords(records []TurnRecord, limit int) []TurnRecord {
	if limit <= 0 || len(records) <= limit {
		return records
	}
	return append([]TurnRecord{}, records[len(records)-limit:]...)
}

func turnRecordIndex(records []TurnRecord, turnID string) int {
	for i := range records {
		if records[i].ID == turnID {
			return i
		}
	}
	return -1
}

func isTerminalTurnStatus(status string) bool {
	switch status {
	case "running", "canceling":
		return false
	default:
		return true
	}
}

func canRetryTurnStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "canceled", "error", "interrupted":
		return true
	default:
		return false
	}
}

func canResumeTurnStatus(status string) bool {
	return strings.TrimSpace(status) == "interrupted"
}

func turnSummary(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= 160 {
		return text
	}
	return string(runes[:160])
}

func forkMessageIndexForTurn(records []TurnRecord, turnID string, fallback int) int {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return fallback
	}
	for _, record := range records {
		if strings.TrimSpace(record.ID) == turnID {
			if record.PriorMessageCount < 0 {
				return 0
			}
			if record.PriorMessageCount > fallback {
				return fallback
			}
			return record.PriorMessageCount
		}
	}
	return fallback
}

func forkSessionKey(parent string, now time.Time) string {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		parent = "session"
	}
	suffix := randomSuffix(4)
	if suffix == "" {
		suffix = fmt.Sprintf("%x", now.UnixNano())
	}
	return fmt.Sprintf("%s-fork-%s", parent, suffix)
}

func randomSuffix(bytesLen int) string {
	if bytesLen <= 0 {
		bytesLen = 4
	}
	buf := make([]byte, bytesLen)
	if _, err := crand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func (s *sessionState) nextTurnID(now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnCounter++
	return fmt.Sprintf("turn-%d-%d", now.UnixNano(), s.turnCounter)
}

func withSessionLock(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionLockContextKey{}, strings.TrimSpace(sessionID))
}

func hasSessionLock(ctx context.Context, sessionID string) bool {
	if ctx == nil {
		return false
	}
	held, _ := ctx.Value(sessionLockContextKey{}).(string)
	return strings.TrimSpace(held) != "" && strings.TrimSpace(held) == strings.TrimSpace(sessionID)
}

func (s *Service) acquireSessionIfNeeded(ctx context.Context, sessionID string, session *sessionState) (func(), bool, error) {
	if hasSessionLock(ctx, sessionID) {
		return nil, false, nil
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	return release, true, nil
}

func normalizeLocator(locator SessionLocator) SessionLocator {
	normalized := SessionLocator{
		Channel: strings.ToLower(strings.TrimSpace(locator.Channel)),
		Key:     strings.TrimSpace(locator.Key),
		UserID:  strings.TrimSpace(locator.UserID),
	}
	if normalized.Channel == "" {
		normalized.Channel = "local"
	}
	if normalized.Key == "" {
		normalized.Key = "default"
	}
	if len(locator.Metadata) > 0 {
		normalized.Metadata = make(map[string]string, len(locator.Metadata))
		for key, value := range locator.Metadata {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key == "" {
				continue
			}
			normalized.Metadata[key] = value
		}
		if len(normalized.Metadata) == 0 {
			normalized.Metadata = nil
		}
	}
	return normalized
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func mergeCommandContextMetadata(base map[string]string, override map[string]string) map[string]string {
	merged := cloneStringMap(base)
	if len(override) == 0 {
		return merged
	}
	if merged == nil {
		merged = map[string]string{}
	}
	for key, value := range override {
		merged[key] = value
	}
	return merged
}

func stableSessionID(locator SessionLocator) string {
	normalized := normalizeLocator(locator)
	// Clean the project dir on the way into the hash so
	// "/a/b" and "/a/b/" or "/a/./b" — the same directory,
	// different surface forms — all map to the same session
	// id.  This keeps the session identity stable across
	// shells, editors, and CI scripts that often normalise
	// paths differently.
	data, _ := json.Marshal(struct {
		Channel    string `json:"channel"`
		Key        string `json:"key,omitempty"`
		UserID     string `json:"user_id,omitempty"`
		ProjectDir string `json:"project_dir,omitempty"`
	}{
		Channel:    normalized.Channel,
		Key:        normalized.Key,
		UserID:     normalized.UserID,
		ProjectDir: cleanProjectDir(normalized.Metadata[sessionProjectDirMetadataKey]),
	})
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%s-%s", normalized.Channel, hex.EncodeToString(sum[:8]))
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func readOptionalFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func (s *Service) buildRuntimeContext(sessionID string, locator SessionLocator, envelope message.Envelope) automation.SessionContext {
	ctx := automation.SessionContext{
		SessionID:       sessionID,
		LocatorChannel:  locator.Channel,
		LocatorKey:      locator.Key,
		LocatorUserID:   locator.UserID,
		Source:          string(envelope.Source),
		Sender:          envelope.Sender,
		AgentProfile:    s.effectiveAgentProfile(locator, envelope),
		SecurityProfile: s.effectiveSecurityProfile(),
		Metadata:        cloneStringMap(envelope.Metadata),
	}
	ctx.DefaultDelivery = defaultDeliveryTarget(sessionID, locator, envelope)
	return ctx
}

func attachSessionGraphContext(session *sessionState, ctx *automation.SessionContext) {
	if session == nil || ctx == nil {
		return
	}
	session.mu.RLock()
	graph := session.graph
	if graph == nil {
		session.mu.RUnlock()
		return
	}
	head, ok := graph.Head(sessiongraph.MainBranchID)
	if !ok {
		session.mu.RUnlock()
		return
	}
	session.mu.RUnlock()
	if ctx.Metadata == nil {
		ctx.Metadata = map[string]string{}
	}
	ctx.Metadata[sessionGraphBranchMetadataKey] = string(sessiongraph.MainBranchID)
	if head.Head != "" {
		ctx.Metadata[sessionGraphNodeMetadataKey] = string(head.Head)
	}
}

func (s *Service) effectiveSecurityProfile() string {
	if s != nil && s.cfg != nil {
		return strings.TrimSpace(s.cfg.Security.Profile)
	}
	return ""
}

func (s *Service) effectiveAgentProfile(locator SessionLocator, envelope message.Envelope) string {
	if profile := strings.TrimSpace(envelope.Metadata["agent_profile"]); profile != "" {
		return config.NormalizeAgentProfile(profile)
	}
	if profile := strings.TrimSpace(locator.Metadata["agent_profile"]); profile != "" {
		return config.NormalizeAgentProfile(profile)
	}
	if s != nil && s.cfg != nil {
		return s.cfg.DefaultAgentProfileForChannel(firstNonEmpty(string(envelope.Source), locator.Channel))
	}
	return config.AgentProfileGeneral
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func defaultDeliveryTarget(sessionID string, locator SessionLocator, envelope message.Envelope) automation.DeliveryTarget {
	switch envelope.Source {
	case message.SourceFeishu:
		return automation.DeliveryTarget{
			Kind:       "channel",
			Channel:    "feishu",
			SessionKey: locator.Key,
			Recipient:  envelope.Sender,
			Metadata:   cloneStringMap(envelope.Metadata),
		}
	case message.SourceWeixin:
		return automation.DeliveryTarget{
			Kind:       "channel",
			Channel:    "weixin",
			SessionKey: locator.Key,
			Recipient:  envelope.Sender,
			Metadata:   cloneStringMap(envelope.Metadata),
		}
	default:
		return automation.DeliveryTarget{
			Kind:       "session",
			SessionID:  sessionID,
			Channel:    locator.Channel,
			SessionKey: locator.Key,
			Recipient:  envelope.Sender,
			Metadata:   cloneStringMap(envelope.Metadata),
		}
	}
}

func assistantTextSince(messages []protocol.Message, start int) string {
	if start < 0 {
		start = 0
	}
	if start > len(messages) {
		start = len(messages)
	}
	for i := len(messages) - 1; i >= start; i-- {
		if messages[i].Role != protocol.RoleAssistant {
			continue
		}
		if text := strings.TrimSpace(protocol.MessageText(messages[i])); text != "" {
			return text
		}
	}
	return ""
}

func sanitizeAttachmentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "attachment"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "\n", "-", "\r", "-", "\t", " ")
	name = replacer.Replace(name)
	name = strings.Trim(name, ". ")
	if name == "" {
		return "attachment"
	}
	return name
}

func deriveSessionTitle(state agent.SessionState) string {
	for _, msg := range state.Messages {
		if msg.Role != protocol.RoleUser {
			continue
		}
		if text := strings.TrimSpace(protocol.MessageText(msg)); text != "" {
			return summarizeTitle(text)
		}
		if msg.Metadata == nil || len(msg.Metadata.Attachments) == 0 {
			continue
		}
		for _, attachment := range msg.Metadata.Attachments {
			if strings.TrimSpace(attachment.Name) != "" {
				return summarizeTitle(attachment.Name)
			}
		}
	}
	return "New chat"
}

func sessionTitleFromEnvelope(envelope message.Envelope) string {
	normalized := envelope.Normalized()
	if text := strings.TrimSpace(normalized.BodyText()); text != "" {
		return summarizeTitle(text)
	}
	for _, attachment := range normalized.Attachments {
		if strings.TrimSpace(attachment.Name) != "" {
			return summarizeTitle(attachment.Name)
		}
	}
	return ""
}

func summarizeTitle(raw string) string {
	raw = strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if raw == "" {
		return "New chat"
	}
	// Use up to 50 chars for better distinguishability.
	runes := []rune(raw)
	maxLen := 50
	if len(runes) <= maxLen {
		return raw
	}
	// Find a natural break point: prefer sentence-ending punctuation or whitespace.
	cut := maxLen
	for i := maxLen - 1; i >= maxLen*2/3 && i >= 0; i-- {
		switch runes[i] {
		case '.', '!', '?', '。', '！', '？', '\n', ';', '；':
			cut = i + 1
			goto done
		case ' ', '\t', ',', '，':
			cut = i
			goto done
		}
	}
done:
	truncated := strings.TrimRight(string(runes[:cut]), " \t\r\n,.;:!?，。！？、；：")
	if truncated == "" {
		truncated = string(runes[:maxLen])
	}
	return truncated + "…"
}

func newAttachmentID() (string, error) {
	var data [8]byte
	if _, err := crand.Read(data[:]); err != nil {
		return "", fmt.Errorf("generate attachment id: %w", err)
	}
	return hex.EncodeToString(data[:]), nil
}

func stateDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
