package workcontext

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// This file stores the verbs a human applies to attention rows (claim / snooze /
// dismiss) in their OWN table rather than as events.
//
// Why not events: wc_events is what UnreadCounts and Focus.changes read, and both
// are kind-agnostic (see unread_kinds_test.go). Recording a verb as an event would
// make the user's own "I dismissed this" show up as an unread change and be
// reported back to them as work needs attention.
//
// The table is deliberately shaped like wc_seen: it holds how the OWNER relates to
// a row, not what the stream contains, so it never touches the context version.
// It is also the ledger the attention-economics metrics are computed from.

// Attention verbs. The stored value is the verb name, so an unknown verb cannot be
// silently accepted.
const (
	AttentionVerbClaim   = "claim"
	AttentionVerbSnooze  = "snooze"
	AttentionVerbDismiss = "dismiss"
)

// DefaultSnoozeDuration is used when a snooze arrives without an explicit wake
// time. Three days is long enough to clear the current context switch and short
// enough that the item comes back within the same work cycle.
const DefaultSnoozeDuration = 72 * time.Hour

// AttentionAction is one verb the owner applied to one attention row.
//
// ItemKey is GROUP-scoped ("{kind}:{context_id}") because the inbox collapses
// same-kind rows from one stream into a single line — the user acts on the line
// they can see, not on an invisible member of it. Dismiss therefore has to carry a
// reason, and every action is reversible (that is what DeleteAttentionAction is
// for).
type AttentionAction struct {
	ItemKey   string `json:"item_key"`
	ContextID string `json:"context_id"`
	Kind      string `json:"kind"`
	Verb      string `json:"verb"`
	// UntilAt is when a snooze stops applying. Empty for verbs that do not expire.
	UntilAt   string `json:"until_at,omitempty"`
	Reason    string `json:"reason,omitempty"`
	ActorID   string `json:"actor_id,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// AttentionActionInput is the write payload for one verb.
type AttentionActionInput struct {
	ItemKey string `json:"item_key"`
	Verb    string `json:"verb"`
	// UntilAt is RFC3339. Only meaningful for snooze; empty means the default.
	UntilAt string `json:"until_at,omitempty"`
	// Reason is required for dismiss: hiding a whole group of work without a
	// recorded reason is indistinguishable from losing it.
	Reason  string `json:"reason,omitempty"`
	ActorID string `json:"actor_id,omitempty"`
}

// AttentionItemKey builds the GROUP-scoped key a verb is stored against when the
// verdict covers every entry of a row. Both the writer and the reader go through
// this one function so they cannot disagree about which row an action belongs to.
func AttentionItemKey(kind, contextID string) string {
	return strings.TrimSpace(kind) + ":" + strings.TrimSpace(contextID)
}

// AttentionEntryKey builds the key for ONE entry of a row, used when the verdict
// covers a single entry rather than the whole group ("ignore this one, not all
// three"). entityID is the underlying record's id — a plan item, a fact, a card — so
// it is stable across reads.
//
// Only kinds that HAVE such an id can be addressed this way. The stream's own
// recorded blockers/open questions are free text inside the checkpoint, so they have
// no per-entry key; see AttentionEntryKeyFor.
func AttentionEntryKey(kind, contextID, entityID string) string {
	return AttentionItemKey(kind, contextID) + ":" + strings.TrimSpace(entityID)
}

// AttentionEntryKeyFor returns the per-entry key when the kind carries a stable
// entity id, and "" when it does not. Callers use the empty string to mean "this kind
// can only be acted on as a whole group", rather than inventing a fake identity
// (a title hash would silently detach the verdict the moment the text is edited).
func AttentionEntryKeyFor(kind, contextID, entityID string) string {
	if strings.TrimSpace(entityID) == "" {
		return ""
	}
	return AttentionEntryKey(kind, contextID, entityID)
}

// AttentionGroupKeyOf maps any action key — group or entry — back to the group it
// belongs to, so a per-entry verdict can still be attributed to its row. A group key
// is returned unchanged.
//
// Keys are "kind:contextID[:entityID]". Ids in this store are generated hex strings
// (see newID) and never contain a colon, so splitting on the first two separators is
// unambiguous; the entity id is taken as the remainder rather than split again.
func AttentionGroupKeyOf(key string) string {
	key = strings.TrimSpace(key)
	kind, rest, ok := strings.Cut(key, ":")
	if !ok {
		return key
	}
	contextID, _, hasEntry := strings.Cut(rest, ":")
	if !hasEntry {
		return key
	}
	return kind + ":" + contextID
}

// AttentionActionKind returns the item kind encoded in an action key (group or entry).
func AttentionActionKind(itemKey string) string {
	kind, _, _ := strings.Cut(itemKey, ":")
	return kind
}

// AttentionActionActive reports whether an action still applies at now. A snooze
// whose wake time has passed stops applying WITHOUT needing a background job: the
// expiry is decided when the row is read (see the inbox projection).
func AttentionActionActive(action AttentionAction, now time.Time) bool {
	switch action.Verb {
	case AttentionVerbDismiss, AttentionVerbClaim:
		return true
	case AttentionVerbSnooze:
		if strings.TrimSpace(action.UntilAt) == "" {
			return true
		}
		wake, err := time.Parse(time.RFC3339, action.UntilAt)
		if err != nil {
			// An unparseable wake time must not silence a row forever.
			return false
		}
		return wake.After(now)
	default:
		return false
	}
}

// UpsertAttentionAction records one verb, replacing any earlier verb on the same
// row so a row has exactly one state ("snoozed" then "dismissed" is one fact, not
// two). It is idempotent: applying the same verb twice leaves one row.
//
// now is passed IN rather than read from the wall clock here, for the same reason
// AttentionActionActive takes it: every time-dependent decision in this subsystem
// (the "is this snooze still in force?" check when reading, the "is the wake time in
// the future?" check when writing, and the day boundary the metrics count against)
// must agree. Reading the clock separately in the store would let a validation and a
// read disagree about what "now" is.
func (s *Store) UpsertAttentionAction(ctx context.Context, scope Scope, contextID string, input AttentionActionInput, now time.Time) (AttentionAction, error) {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	if err := checkScopeWritable(scope); err != nil {
		return AttentionAction{}, err
	}
	if err := requiredText("item key", input.ItemKey); err != nil {
		return AttentionAction{}, err
	}
	verb := strings.TrimSpace(input.Verb)
	switch verb {
	case AttentionVerbClaim, AttentionVerbSnooze, AttentionVerbDismiss:
	default:
		return AttentionAction{}, fmt.Errorf("%w: unsupported verb %q", ErrInvalid, verb)
	}

	untilAt := strings.TrimSpace(input.UntilAt)
	reason := strings.TrimSpace(input.Reason)
	switch verb {
	case AttentionVerbSnooze:
		if untilAt == "" {
			untilAt = now.Add(DefaultSnoozeDuration).UTC().Format(time.RFC3339)
		} else {
			wake, err := time.Parse(time.RFC3339, untilAt)
			if err != nil {
				return AttentionAction{}, fmt.Errorf("%w: until_at must be RFC3339", ErrInvalid)
			}
			if !wake.After(now) {
				return AttentionAction{}, fmt.Errorf("%w: until_at must be in the future", ErrInvalid)
			}
		}
	case AttentionVerbDismiss:
		if err := requiredText("reason", reason); err != nil {
			return AttentionAction{}, err
		}
		untilAt = ""
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AttentionAction{}, err
	}
	defer tx.Rollback()
	if _, err := requireEditableContextTx(ctx, tx, tenantID, ownerID, contextID); err != nil {
		return AttentionAction{}, err
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wc_attention_actions(tenant_id, context_id, owner_id, item_key, verb, until_at, reason, actor_id, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id, context_id, item_key) DO UPDATE SET
			verb=excluded.verb, until_at=excluded.until_at, reason=excluded.reason,
			actor_id=excluded.actor_id, updated_at=excluded.updated_at`,
		tenantID, contextID, ownerID, input.ItemKey, verb, untilAt, reason, strings.TrimSpace(input.ActorID), stamp, stamp); err != nil {
		return AttentionAction{}, err
	}
	if err := tx.Commit(); err != nil {
		return AttentionAction{}, err
	}
	return AttentionAction{
		ItemKey: input.ItemKey, ContextID: contextID, Kind: AttentionActionKind(input.ItemKey),
		Verb: verb, UntilAt: untilAt, Reason: reason, ActorID: strings.TrimSpace(input.ActorID),
		CreatedAt: stamp, UpdatedAt: stamp,
	}, nil
}

// DeleteAttentionAction undoes a verb. It is idempotent: undoing something that is
// not recorded is already the desired state, so it is not an error.
func (s *Store) DeleteAttentionAction(ctx context.Context, scope Scope, contextID, itemKey string) error {
	tenantID, ownerID := scopeArgs(scope)
	contextID = strings.TrimSpace(contextID)
	itemKey = strings.TrimSpace(itemKey)
	if err := checkScopeWritable(scope); err != nil {
		return err
	}
	if err := requiredText("item key", itemKey); err != nil {
		return err
	}
	if _, err := s.getContext(ctx, tenantID, ownerID, contextID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM wc_attention_actions
		WHERE tenant_id=? AND owner_id=? AND context_id=? AND item_key=?`,
		tenantID, ownerID, contextID, itemKey)
	return err
}

// AttentionActionFor picks the verdict that applies to one row, preferring the more
// SPECIFIC one: an entry-level verdict (a single record was singled out) over a
// group-level one. The read path and the nudge sweep both go through this, so a row
// hidden from the inbox is also not pushed about — the two must never disagree about
// whether the user already ruled on something.
func AttentionActionFor(actions map[string]AttentionAction, entryKey, groupKey string) (AttentionAction, bool) {
	if entryKey != "" {
		if action, ok := actions[strings.TrimSpace(entryKey)]; ok {
			return action, true
		}
	}
	action, ok := actions[strings.TrimSpace(groupKey)]
	return action, ok
}

// AttentionActionIndex returns every recorded verb for the scope, keyed by item
// key. The inbox needs the whole set at once (it is a cross-stream list), and the
// sweep needs it per stream, so one query serves both rather than one per row.
//
// Expired snoozes are still returned; callers use AttentionActionActive to decide,
// because only they know the "now" they are reading at.
func (s *Store) AttentionActionIndex(ctx context.Context, scope Scope) (map[string]AttentionAction, error) {
	tenantID, ownerID := scopeArgs(scope)
	rows, err := s.db.QueryContext(ctx, `
		SELECT item_key, context_id, verb, until_at, reason, actor_id, created_at, updated_at
		FROM wc_attention_actions
		WHERE tenant_id=? AND owner_id=?
		ORDER BY updated_at DESC
		LIMIT 500`, tenantID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AttentionAction{}
	for rows.Next() {
		var action AttentionAction
		if err := rows.Scan(&action.ItemKey, &action.ContextID, &action.Verb, &action.UntilAt,
			&action.Reason, &action.ActorID, &action.CreatedAt, &action.UpdatedAt); err != nil {
			return nil, err
		}
		action.Kind = AttentionActionKind(action.ItemKey)
		out[action.ItemKey] = action
	}
	return out, rows.Err()
}

// CountAttentionActionsSince counts verbs recorded since a timestamp — the
// denominator of the attention-yield ratio ("work completed per decision made").
// It counts the owner's own actions from the actions table, not from events, so it
// cannot be inflated by anything the system did on its own.
func (s *Store) CountAttentionActionsSince(ctx context.Context, scope Scope, since time.Time) (int, error) {
	tenantID, ownerID := scopeArgs(scope)
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wc_attention_actions
		WHERE tenant_id=? AND owner_id=? AND updated_at >= ?`,
		tenantID, ownerID, since.UTC().Format(time.RFC3339Nano)).Scan(&count)
	return count, err
}
