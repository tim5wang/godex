package workcontext

import (
	"context"
	"errors"
	"testing"
	"time"
)

// attentionKindForTest is a local stand-in for an inbox row kind: the item key is a
// plain "{kind}:{context_id}" string, so these tests exercise the storage rules
// without depending on any particular signal kind.
const attentionKindForTest = "todo_overdue"

// attentionActionScope returns the scope used by the action tests (a second owner
// exists so isolation can be exercised).
func attentionActionScope() Scope { return Scope{TenantID: "tenant-a", OwnerID: "owner-1"} }

// actionTestNow is the clock handed to the store in these tests. It is passed in
// rather than read inside the store precisely so a "wake time in the future" claim
// can be asserted without depending on the real date.
func actionTestNow() time.Time {
	return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
}

// TestAttentionActionUpsertIsIdempotentAndReplacesVerb pins the two properties the
// inbox depends on: acting twice on one row leaves ONE state, and a later verb
// replaces the earlier one (so "snoozed then dismissed" is one fact, not two).
func TestAttentionActionUpsertIsIdempotentAndReplacesVerb(t *testing.T) {
	store := newTestStore(t)
	scope := attentionActionScope()
	ctx := context.Background()
	now := actionTestNow()
	stream := mustCreate(t, store, scope, "动词语义")
	key := AttentionItemKey(attentionKindForTest, stream.ID)

	snooze := AttentionActionInput{ItemKey: key, Verb: AttentionVerbSnooze,
		UntilAt: now.Add(24 * time.Hour).UTC().Format(time.RFC3339)}
	if _, err := store.UpsertAttentionAction(ctx, scope, stream.ID, snooze, now); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	// Same verb again: idempotent, still one row.
	if _, err := store.UpsertAttentionAction(ctx, scope, stream.ID, snooze, now); err != nil {
		t.Fatalf("snooze repeat: %v", err)
	}
	if rows := countAttentionActionRows(t, store, scope); rows != 1 {
		t.Fatalf("a repeated verb must leave one row, got %d", rows)
	}

	// A different verb replaces it rather than stacking.
	if _, err := store.UpsertAttentionAction(ctx, scope, stream.ID, AttentionActionInput{
		ItemKey: key, Verb: AttentionVerbDismiss, Reason: "不再需要",
	}, now); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if rows := countAttentionActionRows(t, store, scope); rows != 1 {
		t.Fatalf("replacing a verb must not add a row, got %d", rows)
	}
	index, err := store.AttentionActionIndex(ctx, scope)
	if err != nil {
		t.Fatalf("AttentionActionIndex: %v", err)
	}
	if got := index[key].Verb; got != AttentionVerbDismiss {
		t.Fatalf("the later verb should win, got %q", got)
	}
	if got := index[key].Reason; got != "不再需要" {
		t.Fatalf("the reason must be kept for undo, got %q", got)
	}
}

// TestAttentionActionValidationRejectsIncompleteInputs covers the validation that
// keeps a verb from being recorded without the context needed to undo it.
func TestAttentionActionValidationRejectsIncompleteInputs(t *testing.T) {
	store := newTestStore(t)
	scope := attentionActionScope()
	ctx := context.Background()
	now := actionTestNow()
	stream := mustCreate(t, store, scope, "校验")
	key := AttentionItemKey(attentionKindForTest, stream.ID)

	// An unknown verb must not be silently stored.
	if _, err := store.UpsertAttentionAction(ctx, scope, stream.ID, AttentionActionInput{
		ItemKey: key, Verb: "archive",
	}, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown verb should be invalid, got %v", err)
	}
	// Dismiss hides a whole group of work, so it MUST carry a reason: without one it
	// is indistinguishable from losing the work.
	if _, err := store.UpsertAttentionAction(ctx, scope, stream.ID, AttentionActionInput{
		ItemKey: key, Verb: AttentionVerbDismiss,
	}, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dismiss without a reason should be invalid, got %v", err)
	}
	// A snooze must wake in the future; a past wake time is a no-op disguised as a verb.
	if _, err := store.UpsertAttentionAction(ctx, scope, stream.ID, AttentionActionInput{
		ItemKey: key, Verb: AttentionVerbSnooze,
		UntilAt: now.Add(-time.Hour).UTC().Format(time.RFC3339),
	}, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a past wake time should be invalid, got %v", err)
	}
	// Blank item key: nothing to attach the verb to.
	if _, err := store.UpsertAttentionAction(ctx, scope, stream.ID, AttentionActionInput{
		Verb: AttentionVerbClaim,
	}, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a blank item key should be invalid, got %v", err)
	}
	// A snooze without a wake time defaults rather than failing, so the common case
	// needs no clock arithmetic from the client.
	action, err := store.UpsertAttentionAction(ctx, scope, stream.ID, AttentionActionInput{
		ItemKey: key, Verb: AttentionVerbSnooze,
	}, now)
	if err != nil {
		t.Fatalf("default snooze: %v", err)
	}
	if action.UntilAt == "" {
		t.Fatal("a snooze without a wake time should get the default")
	}
	wake, err := time.Parse(time.RFC3339, action.UntilAt)
	if err != nil {
		t.Fatalf("the default wake time should be RFC3339, got %q", action.UntilAt)
	}
	if want := now.Add(DefaultSnoozeDuration); !wake.Equal(want) {
		t.Fatalf("the default wake time should be now+%s, got %s", DefaultSnoozeDuration, wake)
	}
}

// TestAttentionActionScopesIsolateOwners guards the boundary: one owner's verdict on
// a row must never silence another owner's inbox.
func TestAttentionActionScopesIsolateOwners(t *testing.T) {
	store := newTestStore(t)
	alice := attentionActionScope()
	bob := Scope{TenantID: "tenant-a", OwnerID: "owner-2"}
	ctx := context.Background()
	now := actionTestNow()
	aliceStream := mustCreate(t, store, alice, "Alice 的主线")
	bobStream := mustCreate(t, store, bob, "Bob 的主线")

	key := AttentionItemKey(attentionKindForTest, aliceStream.ID)
	if _, err := store.UpsertAttentionAction(ctx, alice, aliceStream.ID, AttentionActionInput{
		ItemKey: key, Verb: AttentionVerbDismiss, Reason: "Alice 的决定",
	}, now); err != nil {
		t.Fatalf("alice dismiss: %v", err)
	}

	// Bob's index must not contain Alice's verdict...
	bobIndex, err := store.AttentionActionIndex(ctx, bob)
	if err != nil {
		t.Fatalf("bob index: %v", err)
	}
	if _, ok := bobIndex[key]; ok {
		t.Fatal("Bob's index leaked Alice's action")
	}
	// ...and Bob cannot write a verb onto Alice's stream at all.
	if _, err := store.UpsertAttentionAction(ctx, bob, aliceStream.ID, AttentionActionInput{
		ItemKey: AttentionItemKey(attentionKindForTest, aliceStream.ID), Verb: AttentionVerbClaim,
	}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bob writing to Alice's stream should be not-found, got %v", err)
	}
	// Bob's own stream still works, so the check is isolation and not a blanket refusal.
	if _, err := store.UpsertAttentionAction(ctx, bob, bobStream.ID, AttentionActionInput{
		ItemKey: AttentionItemKey(attentionKindForTest, bobStream.ID), Verb: AttentionVerbClaim,
	}, now); err != nil {
		t.Fatalf("bob claim on his own stream: %v", err)
	}
}

// TestAttentionActionUndoIsIdempotent covers reversibility: undoing a verb restores
// the row, and undoing a verb that is not recorded is already the desired state.
func TestAttentionActionUndoIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	scope := attentionActionScope()
	ctx := context.Background()
	now := actionTestNow()
	stream := mustCreate(t, store, scope, "撤销")
	key := AttentionItemKey(attentionKindForTest, stream.ID)

	if _, err := store.UpsertAttentionAction(ctx, scope, stream.ID, AttentionActionInput{
		ItemKey: key, Verb: AttentionVerbDismiss, Reason: "误判",
	}, now); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if err := store.DeleteAttentionAction(ctx, scope, stream.ID, key); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if rows := countAttentionActionRows(t, store, scope); rows != 0 {
		t.Fatalf("undo should remove the row, got %d", rows)
	}
	// Repeating the undo is not an error.
	if err := store.DeleteAttentionAction(ctx, scope, stream.ID, key); err != nil {
		t.Fatalf("repeat undo should be a no-op, got %v", err)
	}
}

// TestAttentionActionActiveDecidesByWakeTime pins expiry semantics: a snooze stops
// applying once its wake time passes, decided when the row is read, so no scheduled
// job is needed and none can be missed.
func TestAttentionActionActiveDecidesByWakeTime(t *testing.T) {
	now := actionTestNow()
	wake := now.Add(24 * time.Hour).UTC().Format(time.RFC3339)

	snoozed := AttentionAction{Verb: AttentionVerbSnooze, UntilAt: wake}
	if !AttentionActionActive(snoozed, now) {
		t.Fatal("a snooze inside its window must still apply")
	}
	if AttentionActionActive(snoozed, now.Add(25*time.Hour)) {
		t.Fatal("a snooze past its wake time must stop applying")
	}
	// A snooze with no wake time applies rather than silently un-snoozing.
	if !AttentionActionActive(AttentionAction{Verb: AttentionVerbSnooze}, now) {
		t.Fatal("a snooze without a wake time should apply")
	}
	// An unparseable wake time must NOT silence the row forever: fail open.
	if AttentionActionActive(AttentionAction{Verb: AttentionVerbSnooze, UntilAt: "later"}, now) {
		t.Fatal("an unparseable wake time must not hide the row forever")
	}
	// Dismiss and claim do not expire.
	for _, verb := range []string{AttentionVerbDismiss, AttentionVerbClaim} {
		if !AttentionActionActive(AttentionAction{Verb: verb}, now.Add(1000*time.Hour)) {
			t.Fatalf("%s must not expire", verb)
		}
	}
	// An unknown stored verb must not hide a row.
	if AttentionActionActive(AttentionAction{Verb: "archive"}, now) {
		t.Fatal("an unknown verb must not hide a row")
	}
}

// TestAttentionItemKeyRoundTrip pins the group-scoped identity both the writer and
// the reader derive, because a mismatch would silently detach a verb from its row.
func TestAttentionItemKeyRoundTrip(t *testing.T) {
	key := AttentionItemKey("todo_overdue", "ctx-9")
	if key != "todo_overdue:ctx-9" {
		t.Fatalf("unexpected key %q", key)
	}
	if kind := AttentionActionKind(key); kind != "todo_overdue" {
		t.Fatalf("unexpected kind %q", kind)
	}
}

// TestAttentionEntryKeyRequiresAnEntityID pins the honest limit of per-entry verbs: a
// kind whose entries have no stable identity gets NO entry key rather than a synthetic
// one. A title hash would look like a solution and silently detach the verdict the
// moment the text is edited.
func TestAttentionEntryKeyRequiresAnEntityID(t *testing.T) {
	if got := AttentionEntryKeyFor("todo_overdue", "ctx-1", "todo-7"); got != "todo_overdue:ctx-1:todo-7" {
		t.Fatalf("unexpected entry key %q", got)
	}
	if got := AttentionEntryKeyFor("checkpoint_question", "ctx-1", ""); got != "" {
		t.Fatalf("a kind without an entity id must yield no entry key, got %q", got)
	}
}

// TestAttentionGroupKeyOfStripsTheEntryID pins that a per-entry verdict can still be
// attributed to its row, which the sweep relies on to not push about something the user
// already ruled on.
func TestAttentionGroupKeyOfStripsTheEntryID(t *testing.T) {
	cases := map[string]string{
		"todo_overdue:ctx-1:todo-7": "todo_overdue:ctx-1",
		"todo_overdue:ctx-1":        "todo_overdue:ctx-1",
		"todo_overdue":              "todo_overdue",
		"kind:context:entry":        "kind:context",
	}
	for in, want := range cases {
		if got := AttentionGroupKeyOf(in); got != want {
			t.Fatalf("AttentionGroupKeyOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAttentionActionForPrefersTheEntryVerdict pins the precedence rule the read path
// and the sweep share: the more specific statement wins. If these two disagreed, a row
// could be hidden from the inbox and still pushed about.
func TestAttentionActionForPrefersTheEntryVerdict(t *testing.T) {
	group := AttentionAction{ItemKey: "todo_overdue:ctx-1", Verb: AttentionVerbDismiss}
	entry := AttentionAction{ItemKey: "todo_overdue:ctx-1:todo-7", Verb: AttentionVerbClaim}
	actions := map[string]AttentionAction{group.ItemKey: group, entry.ItemKey: entry}

	got, ok := AttentionActionFor(actions, entry.ItemKey, group.ItemKey)
	if !ok || got.Verb != AttentionVerbClaim {
		t.Fatalf("the entry verdict must win, got %+v (ok=%v)", got, ok)
	}
	// Without an entry verdict, the group verdict applies.
	got, ok = AttentionActionFor(actions, "todo_overdue:ctx-1:other", group.ItemKey)
	if !ok || got.Verb != AttentionVerbDismiss {
		t.Fatalf("the group verdict must apply when no entry verdict exists, got %+v (ok=%v)", got, ok)
	}
	// With neither, nothing applies.
	if _, ok := AttentionActionFor(actions, "", "todo_soon:ctx-1"); ok {
		t.Fatal("no recorded verdict should report ok=false")
	}
}

// TestCountAttentionActionsSinceUsesSupliedClock pins the metric's day boundary: it
// counts decisions at or after the instant given, which is what makes "today's
// decisions" mean the caller's day rather than the server's.
func TestCountAttentionActionsSinceUsesSupliedClock(t *testing.T) {
	store := newTestStore(t)
	scope := attentionActionScope()
	ctx := context.Background()
	now := actionTestNow()
	stream := mustCreate(t, store, scope, "按日计数")
	key := AttentionItemKey(attentionKindForTest, stream.ID)

	if _, err := store.UpsertAttentionAction(ctx, scope, stream.ID, AttentionActionInput{
		ItemKey: key, Verb: AttentionVerbClaim,
	}, now); err != nil {
		t.Fatalf("claim: %v", err)
	}
	count, err := store.CountAttentionActionsSince(ctx, scope, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("count since earlier: %v", err)
	}
	if count != 1 {
		t.Fatalf("a decision after the boundary should count, got %d", count)
	}
	count, err = store.CountAttentionActionsSince(ctx, scope, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("count since later: %v", err)
	}
	if count != 0 {
		t.Fatalf("a decision before the boundary must not count, got %d", count)
	}
}

// countAttentionActionRows reads the table directly (same package) so the
// idempotency assertions test storage rather than the shape of a return value.
func countAttentionActionRows(t *testing.T, store *Store, scope Scope) int {
	t.Helper()
	tenantID, ownerID := scopeArgs(scope)
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wc_attention_actions WHERE tenant_id=? AND owner_id=?`,
		tenantID, ownerID).Scan(&count); err != nil {
		t.Fatalf("count actions: %v", err)
	}
	return count
}
