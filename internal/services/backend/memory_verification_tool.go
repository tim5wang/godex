package backend

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/core/workcontext"
)

// Tool-facing operations deliberately exclude host evidence receipts, identity,
// desktop and authorization fields. Only evidence IDs may come from the model.
type memoryVerificationToolOperation struct {
	Action      string                 `json:"action"`
	FactID      string                 `json:"fact_id,omitempty"`
	IfVersion   int                    `json:"if_version,omitempty"`
	FactIDs     []string               `json:"fact_ids,omitempty"`
	Versions    map[string]int         `json:"versions,omitempty"`
	KeepID      string                 `json:"keep_id,omitempty"`
	IssueID     string                 `json:"issue_id,omitempty"`
	Reason      string                 `json:"reason"`
	BlockKind   string                 `json:"block_kind,omitempty"`
	Replacement *workcontext.FactInput `json:"replacement,omitempty"`
	EvidenceIDs []string               `json:"evidence_ids,omitempty"`
}

func memoryVerificationToolProperties() map[string]any {
	str := map[string]string{"type": "string"}
	return map[string]any{
		"cursor": map[string]any{"type": "integer", "description": "Zero-based cursor from memory_inventory; stable for this task."},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 12},
		"operations": map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "items": map[string]any{"type": "object", "required": []string{"action", "reason"}, "properties": map[string]any{
			"action":  map[string]any{"type": "string", "enum": []string{"confirm", "archive", "merge", "replace", "resolve_conflict", "block"}},
			"fact_id": str, "if_version": map[string]any{"type": "integer", "minimum": 1, "description": "Mandatory current fact version for confirm/archive/replace/block; group actions require versions for every member."}, "fact_ids": map[string]any{"type": "array", "minItems": 2, "maxItems": 32, "items": str},
			"versions": map[string]any{"type": "object", "additionalProperties": map[string]string{"type": "integer"}}, "keep_id": str, "issue_id": str, "reason": str,
			"block_kind":   map[string]any{"type": "string", "enum": []string{"decision", "missing_evidence", "access"}},
			"evidence_ids": map[string]any{"type": "array", "maxItems": 8, "items": str, "description": "Objective tool call IDs from THIS task. Confirmation needs successful calls; failed calls may substantiate an access blocker. Never invent IDs or use memory/work_context summaries."},
			"replacement":  map[string]any{"type": "object", "properties": map[string]any{"title": str, "content": str, "scope": str, "valid_until": str}, "required": []string{"title", "content"}},
		}}},
	}
}

func (s *Service) handleMemoryVerification(ctx context.Context, sessionID string, binding workMemoryBinding, input workContextArgs) (any, error) {
	v, err := s.requireMemoryVerification(ctx, sessionID, binding)
	if err != nil {
		return nil, err
	}
	if v.grant.EntityOnly {
		return nil, fmt.Errorf("entity maintenance authorizes metadata only, not fact verification actions")
	}
	v.opMu.Lock()
	defer v.opMu.Unlock()
	if _, err = s.requireMemoryVerification(ctx, sessionID, binding); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(v.ctx, cancel)
	defer stop()
	defer cancel()
	ctx = callCtx
	switch input.Action {
	case "memory_inventory":
		return s.memoryVerificationInventory(ctx, v, input.Cursor, input.Limit)
	case "memory_status":
		return s.memoryVerificationStatus(ctx, v)
	case "memory_finish":
		if err = s.checkMemoryVerificationCompletion(ctx, v); err != nil {
			return nil, err
		}
		status, e := s.memoryVerificationStatus(ctx, v)
		if e != nil {
			return nil, e
		}
		v.mu.Lock()
		v.finished = true
		v.mu.Unlock()
		return status, nil
	case "memory_apply":
		return s.applyMemoryVerificationTool(ctx, v, input.Operations)
	}
	return nil, fmt.Errorf("unknown memory verification action")
}

func (s *Service) memoryVerificationInventory(ctx context.Context, v *memoryVerificationRuntime, cursor, limit int) (any, error) {
	if cursor < 0 {
		return nil, workcontext.ErrInvalid
	}
	if limit <= 0 {
		limit = 8
	}
	limit = min(limit, 12)
	ids := make([]string, 0, len(v.grant.Facts))
	for id := range v.grant.Facts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if cursor > len(ids) {
		return nil, workcontext.ErrInvalid
	}
	end := min(cursor+limit, len(ids))
	store, err := s.WorkContexts()
	if err != nil {
		return nil, err
	}
	facts := []workcontext.Fact{}
	pageIDs := map[string]bool{}
	for _, id := range ids[cursor:end] {
		f, e := store.Fact(ctx, v.grant.Scope, v.grant.ContextID, id)
		if e != nil {
			return nil, e
		}
		facts = append(facts, f)
		pageIDs[f.ID] = true
	}
	issues, err := store.MemoryIssues(ctx, v.grant.Scope, v.grant.ContextID)
	if err != nil {
		return nil, err
	}
	relevantIssues := []workcontext.MemoryIssue{}
	for _, issue := range issues {
		for _, f := range issue.Facts {
			if pageIDs[f.ID] {
				relevantIssues = append(relevantIssues, issue)
				break
			}
		}
	}
	next := -1
	if end < len(ids) {
		next = end
	}
	status, err := s.memoryVerificationStatus(ctx, v)
	if err != nil {
		return nil, err
	}
	return map[string]any{"facts": facts, "issues": relevantIssues, "next_cursor": next, "status": status}, nil
}

func (s *Service) verificationAllowedIDs(ctx context.Context, v *memoryVerificationRuntime) (map[string]bool, error) {
	ids := map[string]bool{}
	for id := range v.grant.Facts {
		ids[id] = true
	}
	store, err := s.WorkContexts()
	if err != nil {
		return nil, err
	}
	history, err := store.MemoryVerificationHistory(ctx, v.grant.Scope, v.grant.ContextID, v.sessionID, v.turnID)
	if err != nil {
		return nil, err
	}
	for _, r := range history {
		for _, entry := range r.Entries {
			for _, change := range entry.Changes {
				if change.Before == nil {
					ids[change.After.ID] = true
				}
			}
		}
	}
	return ids, nil
}

func (s *Service) applyMemoryVerificationTool(ctx context.Context, v *memoryVerificationRuntime, inputs []memoryVerificationToolOperation) (any, error) {
	if len(inputs) == 0 || len(inputs) > 16 {
		return nil, workcontext.ErrInvalid
	}
	allowed, err := s.verificationAllowedIDs(ctx, v)
	if err != nil {
		return nil, err
	}
	store, err := s.WorkContexts()
	if err != nil {
		return nil, err
	}
	var ops []workcontext.VerificationOperation
	for _, input := range inputs {
		ids := append([]string(nil), input.FactIDs...)
		if input.FactID != "" {
			ids = append(ids, input.FactID)
		}
		if input.KeepID != "" {
			ids = append(ids, input.KeepID)
		}
		for id := range input.Versions {
			ids = append(ids, id)
		}
		for _, id := range ids {
			if !allowed[id] {
				return nil, fmt.Errorf("fact %s is outside the authorized task inventory", id)
			}
		}
		op := workcontext.VerificationOperation{Action: input.Action, FactID: input.FactID, IfVersion: input.IfVersion, FactIDs: input.FactIDs, Versions: input.Versions, KeepID: input.KeepID, IssueID: input.IssueID, Reason: input.Reason, BlockKind: input.BlockKind, Replacement: input.Replacement}
		for _, id := range input.EvidenceIDs {
			v.mu.RLock()
			proof, ok := v.proofs[id]
			v.mu.RUnlock()
			if !ok {
				return nil, fmt.Errorf("evidence %s is not a successful objective tool result from this task", id)
			}
			if proof.ToolName == "history_search" {
				if proof.Input["role"] != "user" {
					return nil, fmt.Errorf("history evidence must search role=user; assistant claims cannot verify preferences or decisions")
				}
				for _, factID := range ids {
					fact, e := store.Fact(ctx, v.grant.Scope, v.grant.ContextID, factID)
					if e != nil {
						return nil, e
					}
					if fact.Kind != "preference" && fact.Kind != "decision" {
						return nil, fmt.Errorf("conversation history can verify user preferences/decisions, not current code or runtime facts")
					}
				}
			}
			op.Evidence = append(op.Evidence, proof)
		}
		ops = append(ops, op)
	}
	result, err := store.ApplyMemoryVerification(ctx, v.grant.Scope, v.grant.ContextID, v.sessionID, v.turnID, ops)
	if err != nil {
		return nil, err
	}
	changes := []map[string]any{}
	for _, entry := range result.Entries {
		for _, change := range entry.Changes {
			f := change.After
			changes = append(changes, map[string]any{"fact_id": f.ID, "status": f.Status, "version": f.Version, "source": f.Source, "action": entry.Action})
		}
	}
	status, err := s.memoryVerificationStatus(ctx, v)
	return map[string]any{"audit_id": result.ID, "changes": changes, "status": status}, err
}

func isMemoryVerificationAction(action string) bool { return strings.HasPrefix(action, "memory_") }
