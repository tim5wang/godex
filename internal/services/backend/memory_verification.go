package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/platform/textutil"
)

type memoryVerificationGrant struct {
	EntityOnly bool              `json:"entity_only,omitempty"`
	ContextID  string            `json:"context_id"`
	Scope      workcontext.Scope `json:"scope"`
	Facts      map[string]int    `json:"facts"`
	GrantedAt  time.Time         `json:"granted_at"`
}

func cloneMemoryVerificationGrant(g *memoryVerificationGrant) *memoryVerificationGrant {
	if g == nil {
		return nil
	}
	v := *g
	v.Facts = map[string]int{}
	for id, version := range g.Facts {
		v.Facts[id] = version
	}
	return &v
}

var quotedVerificationText = regexp.MustCompile("(?s)```.*?```|`[^`]*`|“[^”]*”|「[^」]*」|\"[^\"]*\"")

// Only the actual interactive user envelope is inspected, before notes/runtime
// context are attached. Quoted examples and background messages grant nothing.
func requestsMemoryVerification(envelope message.Envelope) bool {
	switch envelope.Source {
	case message.SourceCLI, message.SourceTUI, message.SourceWeb, message.SourceVoice, message.SourceACP, message.SourceGateway, message.SourceFeishu, message.SourceWeixin:
	default:
		return false
	}
	text := strings.ToLower(strings.TrimSpace(quotedVerificationText.ReplaceAllString(envelope.BodyText(), "")))
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	for _, phrase := range []string{"如果我说", "如果想", "假设", "例如", "比如", "如何", "怎么实现", "能否支持", "是否支持", "可以做到吗", "解释", "设计", "提示词", "这段话", "不要修改", "不要改", "不修改", "不回写", "只读", "只给报告", "只报告", "不要核实", "不要核查", "暂不", "先不", "稍后", "取消", "do not verify", "don't verify", "explain", "design"} {
		if strings.Contains(text, phrase) {
			return false
		}
	}
	imperative := false
	for _, prefix := range []string{"请", "帮我", "开始", "现在", "深度", "逐一", "核实", "核查", "验证", "please verify", "please audit", "verify ", "audit ", "can you verify", "can you audit"} {
		if strings.HasPrefix(text, prefix) {
			imperative = true
			break
		}
	}
	memory := strings.Contains(text, "工作记忆") || strings.Contains(text, "桌面记忆") || (strings.Contains(text, "工作桌面") && strings.Contains(text, "记忆")) || strings.Contains(text, "desktop memory") || strings.Contains(text, "work memory")
	verify := strings.Contains(text, "核实") || strings.Contains(text, "核查") || strings.Contains(text, "验证") || strings.Contains(text, "verify") || strings.Contains(text, "audit")
	return imperative && memory && verify
}

func (s *Service) prepareMemoryVerificationGrant(session *sessionState, envelope message.Envelope, now time.Time) (*memoryVerificationGrant, error) {
	entityOnly := !requestsMemoryVerification(envelope)
	if entityOnly && !requestsEntityMaintenance(envelope) {
		return nil, nil
	}
	session.mu.RLock()
	scope, id := session.workContextScope, session.workContextID
	session.mu.RUnlock()
	if id == "" {
		return nil, fmt.Errorf("link this conversation to a work desktop before verifying memory")
	}
	store, err := s.WorkContexts()
	if err != nil {
		return nil, err
	}
	facts, err := store.Facts(context.Background(), scope, id)
	if err != nil {
		return nil, err
	}
	g := &memoryVerificationGrant{EntityOnly: entityOnly, ContextID: id, Scope: scope, Facts: map[string]int{}, GrantedAt: now}
	for _, f := range facts {
		if f.Status != "archived" && f.Status != "superseded" && f.Status != "rejected" {
			g.Facts[f.ID] = f.Version
		}
	}
	return g, nil
}

func (session *sessionState) setMemoryVerificationGrant(turnID string, g *memoryVerificationGrant) {
	if g == nil {
		return
	}
	session.turnsMu.Lock()
	defer session.turnsMu.Unlock()
	i := turnRecordIndex(session.turns, turnID)
	if i >= 0 {
		session.turns[i].MemoryVerification = cloneMemoryVerificationGrant(g)
	}
}

type memoryVerificationRuntime struct {
	grant             *memoryVerificationGrant
	sessionID, turnID string
	ctx               context.Context
	opMu              sync.Mutex
	mu                sync.RWMutex
	proofs            map[string]workcontext.VerificationEvidence
	finished          bool
}

const verificationFeedbackPrefix = "Desktop memory verification task:"

func (s *Service) beginMemoryVerification(ctx context.Context, session *sessionState, turnID string) (*memoryVerificationRuntime, func(), error) {
	session.agent.ReplaceRuntimeFeedback(verificationFeedbackPrefix, "")
	record, err := session.getTurnRecord(turnID)
	if errors.Is(err, ErrTurnNotFound) {
		return nil, func() {}, nil
	} // Legacy approval continuations create their record at completion.
	if err != nil {
		return nil, func() {}, err
	}
	if record.MemoryVerification == nil {
		return nil, func() {}, nil
	}
	g := cloneMemoryVerificationGrant(record.MemoryVerification)
	store, err := s.WorkContexts()
	if err != nil {
		return nil, func() {}, err
	}
	scope, id, err := store.SessionBinding(ctx, session.id)
	if err != nil || id != g.ContextID || scope != g.Scope {
		return nil, func() {}, fmt.Errorf("memory verification desktop binding changed; start a new explicit verification task")
	}
	runCtx, cancel := context.WithCancel(ctx)
	v := &memoryVerificationRuntime{grant: g, sessionID: session.id, turnID: turnID, ctx: runCtx, proofs: map[string]workcontext.VerificationEvidence{}}
	if session.timeline != nil {
		for _, event := range session.timeline.Entries(0) {
			v.Emit(event)
		}
	}
	detach := session.events.Attach(v)
	session.mu.Lock()
	session.memoryVerification = v
	session.mu.Unlock()
	session.agent.ReplaceRuntimeFeedback(verificationFeedbackPrefix, fmt.Sprintf(`%s The USER explicitly authorized a deep verification of this desktop for this task only. Frozen desktop=%s, target records=%d. Call work_context action=memory_inventory to read complete records in pages. Inspect original code, run meaningful tests or fetch primary sources with existing tools; this authorization grants memory updates only, no extra tool/system permissions. Successful evidence tool call IDs become usable by memory_apply; failed calls, memory summaries, assistant text and self-reported receipts do not count. Use batches of at most 16 operations: confirm, archive, merge, replace, resolve_conflict, block. Keep applicability and distinguish code evidence from actual deployed/runtime evidence. Preferences/decisions need original user sources (history_search role=user), not guesses. For pending supersedes proposals, merge the proposal and its live predecessor with both versions before confirming. To synthesize combined wording, replace then merge the remaining records into the new confirmed fact. A block must say decision, missing_evidence or access; never confirm or delete a fact merely to clear the list. Finish with memory_status then memory_finish; every original record must have an audited outcome. Do not claim success or leave ordinary unverified observations unprocessed. Finish all writes BEFORE the final report.`, verificationFeedbackPrefix, g.ContextID, len(g.Facts)))
	if g.EntityOnly {
		session.agent.ReplaceRuntimeFeedback(verificationFeedbackPrefix, verificationFeedbackPrefix+" The USER authorized entity catalog metadata maintenance only for this desktop. Use work_context entities, entity_save and entity_relation with current entity/fact versions and reasons. Aliases are equivalent names; use part_of for membership and cite a current source fact. This grant cannot confirm, archive, replace or merge underlying facts; memory_apply is forbidden. Finish metadata writes before your report. No extra tool or system permissions are granted.")
	}
	close := func() {
		cancel()
		detach()
		session.mu.Lock()
		if session.memoryVerification == v {
			session.memoryVerification = nil
		}
		session.mu.Unlock()
		session.agent.ReplaceRuntimeFeedback(verificationFeedbackPrefix, "")
	}
	return v, close, nil
}

func objectiveVerificationTool(name string, input map[string]any) bool {
	switch name {
	case "read_file", "grep", "lsp", "bash", "web_fetch", "fetch_url", "read_pdf", "history_search":
		return true
	}
	// Match the actual invoked MCP operation, never a keyword in its arguments.
	if name == "call_mcp_tool" {
		name, _ = input["tool"].(string)
	} else if _, operation, ok := strings.Cut(name, "__"); ok {
		name = operation
	} else {
		return false
	}
	switch name {
	case "get_code_snippet", "search_graph", "query_graph", "trace_path", "search_code", "check_index_coverage":
		return true
	}

	return false
}

func (v *memoryVerificationRuntime) Emit(event events.Event) {
	if event.Type != events.EventToolCallFinished || event.SessionID != v.sessionID || event.TurnID != v.turnID || event.Timestamp.Before(v.grant.GrantedAt) {
		return
	}
	p, ok := event.Payload.(events.ToolCallPayload)
	if !ok {
		raw, _ := json.Marshal(event.Payload)
		if json.Unmarshal(raw, &p) != nil {
			return
		}
	}
	if p.ID == "" || (strings.TrimSpace(p.Output) == "" && p.Error == "") || !objectiveVerificationTool(p.Name, p.Input) {
		return
	}
	successful := p.Error == "" && !p.TimedOut && (p.ExitCode == nil || *p.ExitCode == 0)
	var output map[string]any
	if json.Unmarshal([]byte(p.Output), &output) == nil {
		if code, ok := output["exit_code"].(float64); ok && code != 0 {
			successful = false
		}
		if success, ok := output["success"].(bool); ok && !success {
			successful = false
		}
	}
	sum := sha256.Sum256([]byte(p.Output))
	inputRaw, _ := json.Marshal(p.Input)
	var input map[string]any
	_ = json.Unmarshal(inputRaw, &input)
	proof := workcontext.VerificationEvidence{Successful: successful, Error: p.Error, ToolCallID: p.ID, ToolName: p.Name, Input: input, OutputSHA256: hex.EncodeToString(sum[:]), Excerpt: textutil.TruncateRunes(p.Output+"\n"+p.Error, 1600), ObservedAt: event.Timestamp.UTC().Format(time.RFC3339Nano)}
	v.mu.Lock()
	if !v.finished {
		v.proofs[p.ID] = proof
	}
	v.mu.Unlock()
}

func (s *Service) requireMemoryVerification(ctx context.Context, sessionID string, binding workMemoryBinding) (*memoryVerificationRuntime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session, err := s.requireSessionFor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	session.mu.RLock()
	v := session.memoryVerification
	session.mu.RUnlock()
	if v == nil || v.grant.ContextID != binding.ContextID || v.grant.Scope != binding.Scope {
		return nil, fmt.Errorf("memory verification is not authorized for this task/desktop; explicitly ask to verify this desktop's work memory in a new user turn")
	}
	v.mu.RLock()
	closed := v.finished
	v.mu.RUnlock()
	if closed || v.ctx.Err() != nil {
		return nil, fmt.Errorf("memory verification task has ended")
	}
	return v, nil
}

type memoryVerificationStatus struct {
	TaskID            string              `json:"task_id"`
	Total             int                 `json:"total"`
	Processed         int                 `json:"processed"`
	Remaining         []string            `json:"remaining"`
	Outcomes          map[string]int      `json:"outcomes"`
	Blockers          []map[string]string `json:"blockers,omitempty"`
	EvidenceIDs       []string            `json:"available_evidence_ids,omitempty"`
	FailedEvidenceIDs []string            `json:"failed_evidence_ids,omitempty"`
}

func (s *Service) memoryVerificationStatus(ctx context.Context, v *memoryVerificationRuntime) (memoryVerificationStatus, error) {
	status := memoryVerificationStatus{TaskID: v.turnID, Total: len(v.grant.Facts), Outcomes: map[string]int{}, Remaining: []string{}}
	store, err := s.WorkContexts()
	if err != nil {
		return status, err
	}
	records, err := store.MemoryVerificationHistory(ctx, v.grant.Scope, v.grant.ContextID, v.sessionID, v.turnID)
	if err != nil {
		return status, err
	}
	current, err := store.Facts(ctx, v.grant.Scope, v.grant.ContextID)
	if err != nil {
		return status, err
	}
	versions := map[string]int{}
	for _, f := range current {
		versions[f.ID] = f.Version
	}
	seen := map[string]bool{}
	for _, r := range records {
		for i := len(r.Entries) - 1; i >= 0; i-- {
			entry := r.Entries[i]
			for _, change := range entry.Changes {
				id := change.After.ID
				if _, target := v.grant.Facts[id]; !target || seen[id] || versions[id] != change.After.Version {
					continue
				}
				seen[id] = true
				status.Processed++
				status.Outcomes[entry.Action]++
				if entry.Action == "block" {
					status.Blockers = append(status.Blockers, map[string]string{"fact_id": id, "kind": entry.BlockKind, "reason": entry.Reason})
				}
			}
		}
	}
	for id := range v.grant.Facts {
		if !seen[id] {
			status.Remaining = append(status.Remaining, id)
		}
	}
	sort.Strings(status.Remaining)
	v.mu.RLock()
	for id, proof := range v.proofs {
		if proof.Successful {
			status.EvidenceIDs = append(status.EvidenceIDs, id)
		} else {
			status.FailedEvidenceIDs = append(status.FailedEvidenceIDs, id)
		}
	}
	v.mu.RUnlock()
	sort.Strings(status.EvidenceIDs)
	sort.Strings(status.FailedEvidenceIDs)
	return status, nil
}

func (s *Service) checkMemoryVerificationCompletion(ctx context.Context, v *memoryVerificationRuntime) error {
	if v == nil || v.grant.EntityOnly {
		return nil
	}
	status, err := s.memoryVerificationStatus(ctx, v)
	if err != nil {
		return err
	}
	if len(status.Remaining) > 0 {
		return &memoryVerificationIncompleteError{fmt.Sprintf("memory verification incomplete: %d/%d records still need an audited outcome; resume this task to continue", len(status.Remaining), status.Total)}
	}
	return nil
}

// An incomplete verification is a durable checkpoint, not a successful report.
type memoryVerificationIncompleteError struct{ message string }

func (e *memoryVerificationIncompleteError) Error() string { return e.message }
func (e *memoryVerificationIncompleteError) Class() conversation.TurnErrorClass {
	return conversation.TurnErrorRetryable
}

func (s *Service) finishMemoryVerificationRun(ctx context.Context, v *memoryVerificationRuntime, runErr error) error {
	if runErr != nil {
		return runErr
	}
	return s.checkMemoryVerificationCompletion(ctx, v)
}

func memoryVerificationTurnStatus(status string, runErr error) string {
	var incomplete *memoryVerificationIncompleteError
	if errors.As(runErr, &incomplete) {
		return "interrupted"
	}
	return status
}

func memoryVerificationApprovalTurnID(session *sessionState, original, fallback string) string {
	record, err := session.getTurnRecord(original)
	if err == nil && record.MemoryVerification != nil {
		return original
	}
	return fallback
}
