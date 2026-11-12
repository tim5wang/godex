package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
	"github.com/tim5wang/godex/internal/services/backend"
)

// registerWorkContextRoutes keeps this first personal desktop on the same
// instance/workspace authorization surface as sessions and the taskboard.
// It does not accidentally give relay-only tenant accounts center execution.
func registerWorkContextRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	gated := requireCenterExecution(protected)
	handle := func(pattern string, fn func(*http.Request, *workcontext.Store, workcontext.Scope) (any, error)) {
		mux.Handle(pattern, gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if !ok || !principal.Valid() {
				writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated principal"))
				return
			}
			scope := workcontext.Scope{TenantID: principal.TenantID, OwnerID: principal.UserID}
			store, err := service.WorkContexts()
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, err)
				return
			}
			// Bound JSON at the route boundary, including large source excerpts.
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
			}
			result, err := fn(r, store, scope)
			if err != nil {
				status := http.StatusInternalServerError
				switch {
				case errors.Is(err, workcontext.ErrNotFound), errors.Is(err, taskboard.ErrCardNotFound):
					status = http.StatusNotFound
				case errors.Is(err, workcontext.ErrConflict), errors.Is(err, workcontext.ErrArchived), errors.Is(err, taskboard.ErrVersionConflict):
					status = http.StatusConflict
				case errors.Is(err, workcontext.ErrInvalid):
					status = http.StatusBadRequest
				}
				writeError(w, status, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		})))
	}
	registerMemoryEntityRoutes(handle)
	id := func(r *http.Request) string { return strings.TrimSpace(r.PathValue("id")) }
	invalidJSON := func(err error) error { return fmt.Errorf("%w: %v", workcontext.ErrInvalid, err) }
	handle("GET /v1/work-contexts", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.List(r.Context(), scope)
	})
	// Attention inbox: the cross-stream "what needs me" list plus per-stream
	// unread counts, so the desktop wall can badge every stream from one call.
	handle("GET /v1/work-attention", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return service.AttentionInbox(r.Context(), scope)
	})
	// Every stream's linked sessions in one call, so the desktop list can say which
	// streams have a conversation running and which conversation was used last —
	// without opening each stream (which would be N detail reads for one list).
	// last_turn names the stream whose conversation just finished, so the card can say
	// WHAT moved rather than only that something moved.
	handle("GET /v1/work-context-sessions", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		links, err := s.SessionIndex(r.Context(), scope)
		if err != nil {
			return nil, err
		}
		turns, err := s.NewestSessionTurnByContext(r.Context(), scope, workcontext.SessionTurnEventKind)
		if err != nil {
			return nil, err
		}
		seen, err := s.LastSeenByContext(r.Context(), scope)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessions": links, "last_turn": turns, "seen": seen}, nil
	})
	handle("POST /v1/work-contexts", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.ContextInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.Create(r.Context(), scope, input)
	})
	handle("GET /v1/work-context-resources", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.ListResources(r.Context(), scope)
	})
	handle("GET /v1/work-contexts/{id}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return service.WorkContextDetail(r.Context(), scope, id(r))
	})
	handle("PATCH /v1/work-contexts/{id}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.ContextPatch
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.Patch(r.Context(), scope, id(r), input)
	})
	handle("GET /v1/work-contexts/{id}/checkpoints", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.Checkpoints(r.Context(), scope, id(r))
	})
	handle("POST /v1/work-contexts/{id}/checkpoints", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input struct {
			IfVersion  int                    `json:"if_version"`
			Checkpoint workcontext.Checkpoint `json:"checkpoint"`
		}
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.SaveCheckpoint(r.Context(), scope, id(r), input.IfVersion, input.Checkpoint)
	})
	handle("GET /v1/work-contexts/{id}/brief", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		detail, err := service.WorkContextDetail(r.Context(), scope, id(r))
		if err != nil {
			return nil, err
		}
		return map[string]any{"context": detail.Context, "events": detail.Events, "facts": detail.Facts, "delegations": detail.Delegations, "production_verified": false}, nil
	})
	handle("POST /v1/work-contexts/{id}/resources", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.ResourceInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.AddResource(r.Context(), scope, id(r), input)
	})
	handle("DELETE /v1/work-contexts/{id}/resources/{resourceID}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		err := s.RemoveResource(r.Context(), scope, id(r), r.PathValue("resourceID"))
		return map[string]bool{"ok": err == nil}, err
	})
	handle("POST /v1/work-contexts/{id}/relations", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.RelationInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.AddRelation(r.Context(), scope, id(r), input)
	})
	handle("DELETE /v1/work-contexts/{id}/relations/{relationID}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		err := s.RemoveRelation(r.Context(), scope, id(r), r.PathValue("relationID"))
		return map[string]bool{"ok": err == nil}, err
	})
	handle("POST /v1/work-contexts/{id}/facts", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.FactInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.CreateFact(r.Context(), scope, id(r), input)
	})
	handle("PATCH /v1/work-contexts/{id}/facts/{factID}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.FactReview
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.ReviewFact(r.Context(), scope, id(r), r.PathValue("factID"), input)
	})
	handle("GET /v1/work-contexts/{id}/recall", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.Recall(r.Context(), scope, id(r), r.URL.Query().Get("scope"), r.URL.Query().Get("q"))
	})
	handle("POST /v1/work-contexts/{id}/facts/{factID}/memory-candidate", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return service.ProposeWorkMemory(r.Context(), scope, id(r), r.PathValue("factID"))
	})
	handle("POST /v1/work-contexts/{id}/sessions", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input backend.WorkSessionInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return service.LinkWorkSession(r.Context(), scope, id(r), input)
	})
	handle("DELETE /v1/work-contexts/{id}/sessions/{sessionID}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		err := s.UnlinkSession(r.Context(), scope, id(r), r.PathValue("sessionID"))
		return map[string]bool{"ok": err == nil}, err
	})
	handle("POST /v1/work-contexts/{id}/delegations", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input backend.WorkDelegationInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return service.DelegateWork(r.Context(), scope, id(r), input)
	})
	handle("POST /v1/work-contexts/{id}/delegations/{delegationID}/capture", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return service.CaptureWorkResult(r.Context(), scope, id(r), r.PathValue("delegationID"))
	})
	handle("POST /v1/work-contexts/{id}/organize", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var options backend.DreamOptions
		if err := decodeJSONAllowEmpty(r, &options); err != nil {
			return nil, invalidJSON(err)
		}
		return service.OrganizeWorkContext(r.Context(), scope, id(r), options)
	})
	handle("GET /v1/work-contexts/{id}/memory", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.MemoryState(r.Context(), scope, id(r))
	})
	handle("PATCH /v1/work-contexts/{id}/memory", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input struct {
			Enabled         bool `json:"enabled"`
			IntervalSeconds int  `json:"interval_seconds"`
			TimeoutSeconds  int  `json:"timeout_seconds"`
		}
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.ConfigureDreamWithTimeout(r.Context(), scope, id(r), input.Enabled, input.IntervalSeconds, input.TimeoutSeconds, time.Now())
	})
	handle("GET /v1/work-contexts/{id}/memory/extractions", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.ExtractionHistory(r.Context(), scope, id(r))
	})
	handle("GET /v1/work-contexts/{id}/memory/verifications", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.MemoryVerificationHistory(r.Context(), scope, id(r), "", "")
	})
	handle("GET /v1/work-contexts/{id}/memory/issues", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.MemoryIssues(r.Context(), scope, id(r))
	})
	handle("POST /v1/work-contexts/{id}/memory/issues/{issueID}/resolve", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.ResolveMemoryIssue
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		if err := s.ResolveMemoryIssue(r.Context(), scope, id(r), r.PathValue("issueID"), input); err != nil {
			return nil, err
		}
		return map[string]bool{"resolved": true}, nil
	})
	handle("POST /v1/work-contexts/{id}/memory/runs/{runID}/revert", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.RevertDream(r.Context(), scope, id(r), r.PathValue("runID"))
	})
	handle("GET /v1/work-contexts/{id}/memory/runs", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.DreamRuns(r.Context(), scope, id(r))
	})
	handle("GET /v1/work-contexts/{id}/events", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		detail, err := s.Get(r.Context(), scope, id(r))
		if err != nil {
			return nil, err
		}
		return detail.Events, nil
	})
	// Plan items: a table, so each row is editable in place and queryable.
	handle("GET /v1/work-contexts/{id}/todos", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.ListTodos(r.Context(), scope, id(r))
	})
	handle("POST /v1/work-contexts/{id}/todos", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.TodoInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.CreateTodo(r.Context(), scope, id(r), input)
	})
	handle("PATCH /v1/work-contexts/{id}/todos/{todoID}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.TodoPatch
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		return s.PatchTodo(r.Context(), scope, id(r), r.PathValue("todoID"), input)
	})
	handle("DELETE /v1/work-contexts/{id}/todos/{todoID}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		if err := s.DeleteTodo(r.Context(), scope, id(r), r.PathValue("todoID")); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true}, nil
	})
	// Focus: what changed since this stream was last seen, plus the plan items.
	// Marking seen is separate and explicit (viewing is not an edit).
	handle("GET /v1/work-contexts/{id}/focus", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		return s.Focus(r.Context(), scope, id(r))
	})
	handle("POST /v1/work-contexts/{id}/seen", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		previous, err := s.MarkSeen(r.Context(), scope, id(r))
		if err != nil {
			return nil, err
		}
		return map[string]any{"previous_last_seen_at": previous}, nil
	})
	// Attention verbs: the owner rules on one inbox row (claim / snooze / dismiss).
	// Stored per (stream, item_key) in their own table rather than as events, so a
	// verb never shows up as an unread change — see §7 of the attention-economics
	// doc and internal/core/workcontext/attention_actions.go.
	//
	// This is a WRITE, but it is not an edit of the work: it deliberately does not
	// bump the context version and does not advance the seen watermark, so a row can
	// be ruled on without pretending the stream was read.
	handle("POST /v1/work-contexts/{id}/attention-actions", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		var input workcontext.AttentionActionInput
		if err := decodeJSON(r, &input); err != nil {
			return nil, invalidJSON(err)
		}
		// Through the service so the verb is validated against the SAME clock the
		// inbox reads at (see backend.RecordAttentionAction).
		return service.RecordAttentionAction(r.Context(), scope, id(r), input)
	})
	// Undo a verb. Idempotent: undoing something that is not recorded is already the
	// desired state, so a repeat call is not an error.
	handle("DELETE /v1/work-contexts/{id}/attention-actions/{itemKey}", func(r *http.Request, s *workcontext.Store, scope workcontext.Scope) (any, error) {
		// The item key is group-scoped ("{kind}:{context_id}") and therefore contains a
		// colon, so it is matched as a whole path segment and may arrive percent-encoded.
		itemKey := r.PathValue("itemKey")
		if decoded, err := url.PathUnescape(itemKey); err == nil {
			itemKey = decoded
		}
		if err := s.DeleteAttentionAction(r.Context(), scope, id(r), itemKey); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	})
}
