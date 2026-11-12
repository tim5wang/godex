package backend

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/domain/message"
)

func entityToolProperties() map[string]any {
	str := map[string]string{"type": "string"}
	version := map[string]any{"type": "integer", "minimum": 1}
	proof := map[string]any{"fact_id": str, "fact_version": version, "if_version": map[string]any{"type": "integer", "minimum": 0}, "reason": str, "archived": map[string]string{"type": "boolean"}}
	entity := map[string]any{}
	for k, v := range proof {
		entity[k] = v
	}
	entity["id"] = str
	entity["name"] = str
	entity["kind"] = map[string]any{"type": "string", "enum": []string{"file", "symbol", "tool", "module", "project", "concept", "node"}}
	entity["aliases"] = map[string]any{"type": "array", "maxItems": 24, "items": str}
	relation := map[string]any{}
	for k, v := range proof {
		relation[k] = v
	}
	relation["from_id"] = str
	relation["to_id"] = str
	relation["kind"] = map[string]any{"type": "string", "enum": []string{"part_of"}}
	return map[string]any{"entity": map[string]any{"type": "object", "properties": entity, "required": []string{"kind", "name", "fact_id", "fact_version", "reason"}}, "entity_relation": map[string]any{"type": "object", "properties": relation, "required": []string{"from_id", "to_id", "kind", "fact_id", "fact_version", "reason"}}}
}
func (s *Service) handleMemoryEntity(ctx context.Context, sessionID string, binding workMemoryBinding, input workContextArgs) (any, error) {
	if _, err := s.requireSessionFor(ctx, sessionID); err != nil {
		return nil, err
	}
	store, err := s.WorkContexts()
	if err != nil {
		return nil, err
	}
	if input.Action == "entities" {
		page, e := store.ListMemoryEntities(ctx, binding.Scope, binding.ContextID, input.Query, input.Cursor, input.Limit)
		if e != nil {
			return nil, e
		}
		relations, e := store.ListEntityRelations(ctx, binding.Scope, binding.ContextID)
		return map[string]any{"entities": page, "relations": relations}, e
	}
	v, err := s.requireMemoryVerification(ctx, sessionID, binding)
	if err != nil {
		return nil, err
	}
	v.opMu.Lock()
	defer v.opMu.Unlock()
	if _, err = s.requireMemoryVerification(ctx, sessionID, binding); err != nil {
		return nil, err
	}
	factID := input.Entity.FactID
	if input.Action == "entity_relation" {
		factID = input.EntityRelation.FactID
	}
	allowed, err := s.verificationAllowedIDs(ctx, v)
	if err != nil {
		return nil, err
	}
	if !allowed[factID] {
		return nil, fmt.Errorf("entity proof is outside the authorized task inventory")
	}
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(v.ctx, cancel)
	defer stop()
	defer cancel()
	switch input.Action {
	case "entity_save":
		return store.SaveMemoryEntity(callCtx, binding.Scope, binding.ContextID, input.Entity)
	case "entity_relation":
		return store.SaveEntityRelation(callCtx, binding.Scope, binding.ContextID, input.EntityRelation)
	default:
		return nil, workcontext.ErrInvalid
	}
}

// Entity maintenance is a separate, metadata-only grant. It never authorizes
// memory_apply or confirmation/archival of the underlying facts.
func requestsEntityMaintenance(envelope message.Envelope) bool {
	switch envelope.Source {
	case message.SourceCLI, message.SourceTUI, message.SourceWeb, message.SourceVoice, message.SourceACP, message.SourceGateway, message.SourceFeishu, message.SourceWeixin:
	default:
		return false
	}
	text := strings.ToLower(strings.TrimSpace(quotedVerificationText.ReplaceAllString(envelope.BodyText(), "")))
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	for _, deny := range []string{"如果我", "如果想", "假设", "例如", "比如", "如何", "解释", "设计", "提示词", "不要维护", "不要整理", "不要更新", "不要修改实体", "只读", "不回写", "先不", "稍后", "取消", "don't", "do not"} {
		if strings.Contains(text, deny) {
			return false
		}
	}
	imperative := false
	for _, prefix := range []string{"请", "帮我", "现在", "开始", "维护", "整理", "更新", "添加", "归档", "please ", "update ", "maintain ", "add "} {
		if strings.HasPrefix(text, prefix) {
			imperative = true
			break
		}
	}
	object := strings.Contains(text, "实体目录") || strings.Contains(text, "实体别名") || strings.Contains(text, "entity catalog")
	action := false
	for _, verb := range []string{"维护", "整理", "更新", "添加", "归档", "管理", "修改", "别名", "update", "maintain", "add", "archive"} {
		action = action || strings.Contains(text, verb)
	}
	return imperative && object && action
}

func (s *Service) captureTurnMemories(session *sessionState, now time.Time, turnID, status string, prior int) {
	if record, err := session.getTurnRecord(turnID); err == nil && record.MemoryVerification != nil && record.MemoryVerification.EntityOnly {
		return
	}
	s.captureSessionSignalCandidates(session, now, turnID)
	s.captureSinglePassMemory(session, turnID, status, prior)
}
