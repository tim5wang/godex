package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/platform/textutil"
)

func (s *Service) handleWorkCollaboration(ctx context.Context, sessionID string, scope workcontext.Scope, id string, input workContextArgs) (bool, any, error) {
	switch input.Action {
	case "resources", "resource_put", "resource_remove", "delegation_options", "delegations", "delegation", "delegate", "dispatch":
	default:
		return false, nil, nil
	}
	store, err := s.WorkContexts()
	if err != nil {
		return true, nil, err
	}
	owner := memory.Owner{TenantID: scope.TenantID, UserID: scope.OwnerID}
	if caller, ok := memory.OwnerFromContext(ctx); ok && caller != owner {
		return true, nil, workcontext.ErrNotFound
	}
	ownerCtx := memory.WithOwner(ctx, owner)
	if _, err := s.requireSessionFor(ownerCtx, sessionID); err != nil {
		return true, nil, err
	}
	if input.Action == "delegate" || input.Action == "dispatch" {
		role, err := store.SessionRole(ctx, scope, id, sessionID)
		if err != nil {
			return true, nil, err
		}
		if role != "lead" {
			return true, nil, fmt.Errorf("%w: only this desktop's main conversation may delegate or dispatch", workcontext.ErrInvalid)
		}
	}
	switch input.Action {
	case "resources":
		detail, err := store.PromptState(ctx, scope, id)
		return true, detail.Resources, err
	case "resource_put":
		input.Resource.Source = "session://" + sessionID
		out, err := store.AddResource(ctx, scope, id, input.Resource)
		return true, out, err
	case "resource_remove":
		err := store.RemoveResource(ctx, scope, id, input.ResourceID)
		return true, map[string]bool{"unlinked": err == nil}, err
	case "delegation_options":
		ledger := s.TaskboardLedger()
		if ledger == nil {
			return true, nil, fmt.Errorf("taskboard unavailable")
		}
		templates, err := s.ListAgentTemplates()
		options := []map[string]string{}
		for _, tmpl := range templates {
			options = append(options, map[string]string{"id": tmpl.ID, "name": tmpl.Name, "description": tmpl.Description})
		}
		return true, map[string]any{"projects": ledger.ListProjects(), "templates": options}, err
	case "delegate":
		out, err := s.DelegateWork(ownerCtx, scope, id, input.Delegation)
		if err != nil {
			return true, nil, err
		}
		return true, map[string]any{"delegation_id": out.Delegation.ID, "card_id": out.Card.ID, "status": out.Card.Status, "started": false}, nil
	}
	view, err := s.WorkContextDetail(ctx, scope, id)
	if err != nil {
		return true, nil, err
	}
	if input.Action == "delegations" {
		items := []map[string]any{}
		for _, d := range view.Delegations {
			item := map[string]any{"id": d.ID, "title": d.Title, "card_id": d.CardID, "source_missing": d.SourceMissing}
			if d.Card != nil {
				item["status"] = d.Card.Status
				done, total := d.Card.ChecklistProgress()
				item["acceptance_done"], item["acceptance_total"] = done, total
				for _, run := range d.Card.Executions {
					if run.Host != nil {
						item["session_id"] = run.Host.SessionID
					}
					if strings.TrimSpace(run.Summary) != "" {
						item["summary"] = textutil.TruncateRunes(run.Summary, 600)
					}
				}
			}
			items = append(items, item)
		}
		return true, items, nil
	}
	for _, d := range view.Delegations {
		if d.ID != input.DelegationID {
			continue
		}
		if input.Action == "delegation" {
			return true, d, nil
		}
		if d.Card == nil {
			return true, nil, workcontext.ErrNotFound
		}
		execID, sid, err := NewTaskboardExecutor(s, s.TaskboardLedger()).Execute(ownerCtx, *d.Card)
		return true, map[string]string{"execution_id": execID, "session_id": sid, "delegation_id": d.ID}, err
	}
	return true, nil, workcontext.ErrNotFound
}

func collaborationToolProperties() map[string]interface{} {
	str := func(description string) map[string]string {
		return map[string]string{"type": "string", "description": description}
	}
	return map[string]interface{}{
		"resource": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"id":    str("Existing resource ID for editing; omit on a new mention (deduplicated by locator/kind/node)"),
			"title": str("Human-readable resource name"), "kind": str("web, directory, document, repository, artifact, note, etc."), "locator": str("URL, path or object locator; no credentials"), "node_id": str("Required for directories: explicit owning node; local means this execution node"), "purpose": str("Why this resource matters to the work"),
		}},
		"resource_id":   str("Resource to unlink from THIS desktop"),
		"delegation_id": str("Delegation ID from THIS desktop, for delegation or dispatch"),
		"delegation": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
			"request_id": str("Stable idempotency key, reuse exactly on retries"), "title": str("Short task objective"), "description": str("Short instructions and artifact references, not copied transcripts"), "acceptance": map[string]interface{}{"type": "array", "items": str("Testable acceptance criterion")}, "project_id": str("Existing taskboard project; optional only when there is exactly one"), "work_dir": str("Directory from this project's configured work_dirs"), "template_id": str("Existing worker template such as coder or researcher"),
		}},
	}
}

const workCollaborationPrefix = "GoDex work desktop collaboration policy:"

func (s *Service) refreshWorkCollaboration(session *sessionState, store *workcontext.Store, binding workMemoryBinding) {
	text := ""
	if binding.Err == nil && binding.ContextID != "" {
		role, err := store.SessionRole(context.Background(), binding.Scope, binding.ContextID, session.id)
		if err == nil {
			text = workCollaborationPrefix + "\nPersisted session role: " + role + ". "
			if role == "lead" {
				text += "You are the main conversation: lead other Agents, assist user decisions and keep this context short. Delegate complex long tasks via work_context delegate/dispatch after user authorization; answer small questions directly. Use short objectives, acceptance criteria and resource/artifact references. Read concise delegations status/results, not worker transcripts. User may explicitly ask you to work inline. Only humans accept task cards."
			} else if role == "worker" {
				text += "You execute one delegated task, not the user's leader. Read needed desktop state with work_context brief/resources/recall. Do not create further delegations or overwrite the global checkpoint. Store details in workspace artifacts, update your card with evidence and submit in_review; report concise outcomes/risks/references for the leader."
			}
			text += " Proactively record meaningful user-provided resources with resource_put and purpose; directories require a known node_id. Recording grants no access or execution rights."
		}
	}
	session.agent.ReplaceRuntimeFeedback(workCollaborationPrefix, text)
}
