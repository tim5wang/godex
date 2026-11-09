package backend

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
)

// This file turns the work desktop from a place you must visit into a place that
// tells you when it needs you. Two things live here:
//
//  1. AttentionInbox: a cross-stream read model of "what needs a decision from
//     me", so six or seven parallel streams can be triaged from one list instead
//     of by opening each desktop.
//  2. The nudge sweep: a background pass that records progress on the desk
//     WITHOUT the user reading the stream first (previously progress only became
//     an event when someone opened the detail endpoint), and pushes a browser
//     notification only for things that actually need a human.
//
// Deliberate discipline (otherwise six parallel streams become six naggers):
//   - Only DECISION-class changes push in real time. Plain progress is recorded
//     as an event, which raises the stream's unread badge, but stays silent.
//   - Every notification is deduplicated by the event table's UNIQUE dedup_key,
//     so a sweep that runs every minute cannot notify twice about the same thing.
//   - Quiet hours and a per-day cap bound the noise; they are constants for now
//     rather than config because no operator has asked to tune them yet.
const (
	// attentionSweepInterval is deliberately modest: the sweep reads local SQLite
	// and the taskboard ledger, and its only irreversible effect (a push) is
	// deduplicated anyway. One minute keeps "a card finished" feeling immediate.
	attentionSweepInterval = time.Minute
	// dueSoonWindow is how far ahead a plan item counts as "due soon".
	dueSoonWindow = 24 * time.Hour
	// attentionDailyCap bounds non-urgent nudges per owner per local day.
	attentionDailyCap = 12
	// attentionPerStreamDailyCap bounds one stream's share of that budget. Without
	// it one busy stream can spend every nudge the owner gets in a day, and the
	// other streams go silent — the exact unfairness a single global cap hides.
	attentionPerStreamDailyCap = 4
	// unreadAgingAfter is the FALLBACK aging threshold, used when a stream has too
	// little history to have a pace of its own. Past the threshold the count is reported
	// as aged: the badge is demoted to a muted summary instead of a red dot, because a
	// marker nobody clears stops being a request and becomes background debt. It is NOT
	// hidden — hiding remembered content would be the worse failure.
	unreadAgingAfter = 72 * time.Hour
	// The aging threshold is ADAPTIVE: three times a stream's own recent event stride,
	// clamped between the bounds below. A fixed threshold is wrong in both directions —
	// a stream that legitimately moves twice a week is not stale after three days, and a
	// stream that moves hourly is stale long before that. The stride between a stream's
	// own events is the only honest scale for "has this gone quiet?".
	//
	// The floor keeps a fast stream from being called stale almost immediately (which
	// would mute a badge the user has not had a chance to see), and the ceiling keeps a
	// genuinely dormant stream from being treated as active forever.
	unreadAgingFloor          = 24 * time.Hour
	unreadAgingCeiling        = 14 * 24 * time.Hour
	unreadAgingStrideMultiple = 3
	unreadAgingLookback       = 30 * 24 * time.Hour
	unreadAgingMinSamples     = 3
	// quiet hours are in server-local time.
	quietHoursStart = 22
	quietHoursEnd   = 8
	// eventListenerKind is the event kind used for nudges, kept separate from
	// "task_status" so the daily cap counts notifications, not progress.
	nudgeEventKind = "nudge"
	// attentionCheckpointLimit bounds how many recorded blockers/open questions the
	// inbox carries in total. A stream accumulates these over months; the inbox is a
	// triage list, not an archive.
	attentionCheckpointLimit = 50
	// attentionGroupTitlesLimit caps the collapsed titles a grouped row spells out,
	// so a stream that moved twenty times does not turn into a paragraph.
	attentionGroupTitlesLimit = 4
	// attentionGroupEntriesLimit caps how many per-entry verdict targets a grouped row
	// carries. It is deliberately larger than the title list: titles are for READING the
	// row, entries are for ACTING on it, and "ignore this one" is useless if the entry
	// the user meant is not addressable.
	attentionGroupEntriesLimit = 20
)

// Attention priorities. A decision (a card awaiting human acceptance) is always
// delivered; time-based nudges are subject to the daily cap.
const (
	AttentionHigh   = "high"
	AttentionNormal = "normal"
	AttentionLow    = "low"
)

// Attention kinds. Each kind routes to the tab that resolves it, and the frontend
// labels a row from its kind, so adding a kind needs no prose from the backend.
const (
	AttentionKindDelegationReview   = "delegation_review"
	AttentionKindDelegationMissing  = "delegation_missing"
	AttentionKindFactReview         = "fact_review"
	AttentionKindTodoOverdue        = "todo_overdue"
	AttentionKindTodoSoon           = "todo_soon"
	AttentionKindTodoBlocked        = "todo_blocked"
	AttentionKindCheckpointBlocker  = "checkpoint_blocker"
	AttentionKindCheckpointQuestion = "checkpoint_question"
)

// AttentionEntry is ONE entry inside a grouped row.
//
// It exists so a verdict can be scoped to a single entry ("ignore this one, not all
// three") without weakening the group-level verbs. Key is the per-entry action key, or
// empty when the kind has NO stable entity id — the stream's recorded
// blockers/open questions are free text inside the checkpoint, so they can only be
// acted on as a group, and pretending otherwise (a title hash) would silently detach
// the verdict the moment the text is edited.
type AttentionEntry struct {
	Key    string `json:"key,omitempty"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	DueAt  string `json:"due_at,omitempty"`
	// Action is the verdict recorded on THIS entry (not on the row), when there is
	// one. It exists because a claimed entry stays on the list — a claim is work, not
	// deferral — and "I took this one" has to be visible or the claim reads as a
	// no-op that silently did nothing.
	Action *workcontext.AttentionAction `json:"action,omitempty"`
}

// AttentionItem is one actionable line in the inbox.
type AttentionItem struct {
	Kind         string `json:"kind"`
	Priority     string `json:"priority"`
	ContextID    string `json:"context_id"`
	ContextTitle string `json:"context_title"`
	Title        string `json:"title"`
	Detail       string `json:"detail"`
	DueAt        string `json:"due_at,omitempty"`
	// Tab is the desktop tab that resolves the item, so the UI can jump straight
	// to the place where the work is done instead of to the stream's default tab.
	Tab string `json:"tab"`
	// Count is how many same-kind entries this row stands for. Zero means a single
	// entry (omitted); 2+ means the row collapsed repeated noise from one stream,
	// which is what keeps several parallel streams from stacking duplicates.
	Count int `json:"count,omitempty"`
	// BlockedCount is how many other streams declare a dependency on this one — the
	// leverage dimension: a stalled stream three others wait on is worth more
	// attention than a stalled stream nobody depends on.
	BlockedCount int `json:"blocked_count,omitempty"`
	// Also lists the collapsed titles (capped), so a grouped row is still readable
	// without opening the stream.
	Also []string `json:"also,omitempty"`
	// Entries are the individual entries a grouped row stands for, present only when
	// the row actually collapsed more than one. They are what makes a per-entry verdict
	// possible: the row's own verbs still cover the whole group, and each entry can be
	// ruled on by itself. Empty for a single-entry row, where the two are the same thing.
	Entries []AttentionEntry `json:"entries,omitempty"`
	// EntityID is the underlying record id (plan item / fact / card) used to derive a
	// per-entry key. It is internal: the wire shape exposes Entries instead, so clients
	// cannot mistake it for an action key.
	EntityID string `json:"-"`
	// ItemKey is the group-scoped identity a verb is stored against
	// ("{kind}:{context_id}"), so the UI acts on exactly the row it renders.
	ItemKey string `json:"item_key"`
	// Action is the owner's recorded verb on this row, when there is one. A claim
	// keeps the row actionable, so it stays in Items carrying its state.
	Action *workcontext.AttentionAction `json:"action,omitempty"`
}

// AttentionMetrics is the attention ledger: how much of the day's notification
// budget has been spent, and how much unread work is open. It is computed from
// data the inbox already reads, so showing it costs no extra query and no second
// source of truth.
type AttentionMetrics struct {
	// NudgesToday is how many notifications the sweep has delivered to this owner
	// since local midnight. The caps are reported alongside it so the number is
	// readable as "N of M" rather than as a bare figure with no scale.
	NudgesToday  int `json:"nudges_today"`
	OwnerCap     int `json:"owner_cap"`
	PerStreamCap int `json:"per_stream_cap"`
	// UnreadTotal sums the per-stream unread counts; AgedStreams counts streams
	// whose unread work has gone stale (see unreadAgingAfter).
	UnreadTotal int `json:"unread_total"`
	AgedStreams int `json:"aged_streams"`
	// ActionsToday is the DENOMINATOR of the attention-yield ratio: how many
	// decisions the human actually made today (each claim/snooze/dismiss counts
	// once, since a row holds one verb).
	ActionsToday int `json:"actions_today"`
	// CompletedToday is the NUMERATOR: plan items the owner finished today, counted from
	// the exact completion stamp (see Todo.CompletedAt) rather than from updated_at.
	//
	// Both sides of the attention-yield ratio are exposed as raw counts and the ratio is
	// derived from them where it is displayed, so there is exactly one formula and the
	// ratio can never disagree with the numbers printed next to it.
	CompletedToday int `json:"completed_today"`
}

// DeferredAttention is a row the owner has already ruled on (snoozed until later,
// or dismissed with a reason), together with the decision that deferred it.
//
// Deferred rows are reported rather than dropped: a suggestion that silently
// disappears is indistinguishable from a bug, and the reason a whole group was
// dismissed is exactly the context needed to undo it.
type DeferredAttention struct {
	Item   AttentionItem               `json:"item"`
	Action workcontext.AttentionAction `json:"action"`
}

// AttentionView is the inbox plus per-stream unread counts, so the desktop wall
// can show a badge for every stream from the same request.
type AttentionView struct {
	Items  []AttentionItem `json:"items"`
	Unread map[string]int  `json:"unread"`
	// Aged names the streams whose unread changes have gone stale (see
	// unreadAgingAfter). The count is still reported in Unread; this only tells the
	// UI to render it as a muted summary rather than an alert.
	Aged map[string]bool `json:"aged"`
	// Deferred holds the rows the owner has already ruled on (snoozed, or
	// dismissed with a reason). They are reported rather than dropped so a deferred
	// item is visibly deferred instead of looking like it vanished, and so the
	// reason can be read back when undoing it.
	Deferred    []DeferredAttention `json:"deferred"`
	Counts      map[string]int      `json:"counts"`
	Metrics     AttentionMetrics    `json:"metrics"`
	GeneratedAt string              `json:"generated_at"`
}

// NudgeNotifier delivers a notification to one owner's browsers. Narrow on
// purpose: the backend owns what to say and when, the push service owns how.
type NudgeNotifier interface {
	NotifyOwner(ctx context.Context, owner, title, body string) (int, error)
}

// SetNudgeNotifier wires the push transport. Nil disables pushes while leaving
// the inbox and the event recording fully working, so a center without push
// configured still gets the desktop wall and the inbox.
func (s *Service) SetNudgeNotifier(notifier NudgeNotifier) {
	s.nudgeMu.Lock()
	defer s.nudgeMu.Unlock()
	s.nudgeNotifier = notifier
}

func (s *Service) notifier() NudgeNotifier {
	s.nudgeMu.Lock()
	defer s.nudgeMu.Unlock()
	return s.nudgeNotifier
}

// RecordAttentionAction applies one verb to one inbox row on behalf of the owner.
//
// It lives on the Service rather than being called on the store from the route
// because every time-dependent decision in this subsystem must read the SAME clock
// the inbox projection reads: the "is this wake time in the future?" validation here
// and the "is this snooze still in force?" check on read would otherwise be able to
// disagree about what "now" is.
func (s *Service) RecordAttentionAction(ctx context.Context, scope workcontext.Scope, contextID string, input workcontext.AttentionActionInput) (workcontext.AttentionAction, error) {
	store, err := s.WorkContexts()
	if err != nil {
		return workcontext.AttentionAction{}, err
	}
	return store.UpsertAttentionAction(ctx, scope, contextID, input, s.now())
}

// AttentionInbox assembles the cross-stream "needs me" list for one owner.
//
// It is intentionally deterministic — dates, card states, recorded blockers —
// rather than an LLM guess about what matters, because a wrong reminder costs
// more attention than a missing one.
//
// Coverage is deliberately wider than "a card awaits acceptance + a date passed":
// those two alone still left the user scanning the rest of the desktop by hand,
// which is the O(N) behaviour the inbox exists to remove. It now also surfaces
// candidate facts, blocked plan items, lost delegation cards and the stream's own
// recorded blockers/open questions.
//
// What it deliberately does NOT surface is any signal that cannot be attributed to
// (tenant, owner) — see the design doc §AE1 on flow runs, which are excluded for
// isolation reasons, not for scope reasons.
func (s *Service) AttentionInbox(ctx context.Context, scope workcontext.Scope) (AttentionView, error) {
	store, err := s.WorkContexts()
	if err != nil {
		return AttentionView{}, err
	}
	now := s.now()

	delegations, err := s.delegationAttention(ctx, store, scope)
	if err != nil {
		return AttentionView{}, err
	}
	facts, err := s.factAttention(ctx, store, scope)
	if err != nil {
		return AttentionView{}, err
	}
	plans, err := s.planAttention(ctx, store, scope, now)
	if err != nil {
		return AttentionView{}, err
	}
	checkpoints, err := s.checkpointAttention(ctx, store, scope)
	if err != nil {
		return AttentionView{}, err
	}
	items := make([]AttentionItem, 0, len(delegations)+len(facts)+len(plans)+len(checkpoints))
	items = append(items, delegations...)
	items = append(items, facts...)
	items = append(items, plans...)
	items = append(items, checkpoints...)

	// Leverage, read once for the whole inbox: how many other streams declare a
	// dependency on each stream.
	leverage, err := store.DownstreamBlockedCounts(ctx, scope)
	if err != nil {
		return AttentionView{}, err
	}
	items = groupAttention(items)
	// Every row's identity is stamped in ONE place (right after grouping) rather
	// than at each of the seven construction sites: the key is group-scoped by
	// definition, so deriving it here is what guarantees the UI acts on exactly the
	// row it renders.
	for i := range items {
		items[i].ItemKey = workcontext.AttentionItemKey(items[i].Kind, items[i].ContextID)
		items[i].BlockedCount = leverage[items[i].ContextID]
	}

	// Apply the owner's recorded verbs. This is where a snooze expires: the wake time
	// is compared against now while READING, so no background job has to wake up and
	// no scheduled task can be missed — the row simply reappears once due.
	actions, err := store.AttentionActionIndex(ctx, scope)
	if err != nil {
		return AttentionView{}, err
	}
	active, deferred := applyAttentionActions(items, actions, now)
	sortAttention(active, leverage)

	unread, err := store.UnreadCounts(ctx, scope)
	if err != nil {
		return AttentionView{}, err
	}
	aged, err := s.agedUnreadStreams(ctx, store, scope, now)
	if err != nil {
		return AttentionView{}, err
	}
	counts := map[string]int{}
	for _, item := range active {
		counts[item.Kind]++
	}
	metrics, err := s.attentionMetrics(ctx, store, scope, now, unread, aged, len(deferred))
	if err != nil {
		return AttentionView{}, err
	}
	return AttentionView{Items: active, Unread: unread, Aged: aged, Deferred: deferred, Counts: counts, Metrics: metrics, GeneratedAt: now.UTC().Format(time.RFC3339Nano)}, nil
}

// attentionMetrics builds the attention ledger from data already read above, plus
// one cheap count of today's delivered nudges. A failed count degrades to zero
// spent rather than failing the whole inbox: the inbox is the useful part and a
// missing ledger must not take it down.
func (s *Service) attentionMetrics(ctx context.Context, store *workcontext.Store, scope workcontext.Scope,
	now time.Time, unread map[string]int, aged map[string]bool, deferred int) (AttentionMetrics, error) {
	metrics := AttentionMetrics{
		OwnerCap:     attentionDailyCap,
		PerStreamCap: attentionPerStreamDailyCap,
		AgedStreams:  len(aged),
	}
	for _, count := range unread {
		metrics.UnreadTotal += count
	}
	// The inbox is read-only and must not be the thing that decides whether a nudge
	// was delivered, so a failed count is swallowed into 0.
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if sent, err := store.CountEventsByKindSince(ctx, scope, nudgeEventKind, midnight); err == nil {
		metrics.NudgesToday = sent
	}
	// Decisions the human made today — the denominator of the attention-yield
	// ratio. Counted from recorded verbs, so only the owner's own actions count.
	if made, err := store.CountAttentionActionsSince(ctx, scope, midnight); err == nil {
		metrics.ActionsToday = made
	}
	// Work the owner finished today — the numerator. Same swallow-to-zero treatment as
	// the counts above: a missing numerator must not take the inbox down, and a zero
	// numerator with a non-zero denominator is a real (and informative) state.
	if done, err := store.CountTodosCompletedSince(ctx, scope, midnight); err == nil {
		metrics.CompletedToday = done
	}
	return metrics, nil
}

// agedUnreadStreams reports which streams have unread changes older than their own
// aging threshold. A stream with NO unread is never aged: there is nothing to demote,
// and reporting it would put a permanent grey marker on quiet streams.
func (s *Service) agedUnreadStreams(ctx context.Context, store *workcontext.Store, scope workcontext.Scope, now time.Time) (map[string]bool, error) {
	newest, err := store.NewestUnreadEventAt(ctx, scope)
	if err != nil {
		return nil, err
	}
	thresholds, err := s.agingThresholds(ctx, store, scope)
	if err != nil {
		return nil, err
	}
	aged := map[string]bool{}
	for id, at := range newest {
		// A stream with too little history to have a measurable pace gets the fallback.
		threshold, ok := thresholds[id]
		if !ok {
			threshold = unreadAgingAfter
		}
		if at.Before(now.Add(-threshold)) {
			aged[id] = true
		}
	}
	return aged, nil
}

// agingThresholds derives, per stream, how long a silence may last before an unread
// marker stops counting as a request.
//
// The scale is the stream's OWN pace: three times the median stride between its recent
// events, clamped to [unreadAgingFloor, unreadAgingCeiling]. Streams with too little
// history get no entry here and the caller falls back to the fixed threshold, because a
// threshold derived from one or two strides is noise that would jump between reads.
func (s *Service) agingThresholds(ctx context.Context, store *workcontext.Store, scope workcontext.Scope) (map[string]time.Duration, error) {
	stamps, err := store.RecentEventStamps(ctx, scope, unreadAgingLookback, 2000)
	if err != nil {
		return nil, err
	}
	return thresholdsFromStamps(stamps), nil
}

// thresholdsFromStamps is the pure part of the cadence calculation, kept separate so it
// can be tested without a database.
//
// Reminder ("nudge") and progress-probe ("task_status") events are EXCLUDED: they are
// written by the attention system itself, so counting them would make nudging a quiet
// stream look like activity and reset the very staleness that triggered the reminder —
// the stream would then never age, which is exactly backwards.
func thresholdsFromStamps(stamps []workcontext.EventStamp) map[string]time.Duration {
	previous := map[string]time.Time{}
	strides := map[string][]time.Duration{}
	for _, stamp := range stamps {
		if stamp.Kind == nudgeEventKind || stamp.Kind == "task_status" {
			continue
		}
		if last, ok := previous[stamp.ContextID]; ok {
			if stride := last.Sub(stamp.At); stride > 0 {
				strides[stamp.ContextID] = append(strides[stamp.ContextID], stride)
			}
		}
		previous[stamp.ContextID] = stamp.At
	}
	out := map[string]time.Duration{}
	for id, list := range strides {
		if len(list) < unreadAgingMinSamples {
			continue
		}
		out[id] = clampAgingThreshold(medianDuration(list))
	}
	return out
}

// clampAgingThreshold turns a measured pace into a threshold, bounded on both sides: the
// floor stops a fast stream from being called stale before the user could plausibly have
// seen the badge, and the ceiling stops a dormant stream from being treated as active
// forever.
func clampAgingThreshold(stride time.Duration) time.Duration {
	threshold := stride * unreadAgingStrideMultiple
	if threshold < unreadAgingFloor {
		return unreadAgingFloor
	}
	if threshold > unreadAgingCeiling {
		return unreadAgingCeiling
	}
	return threshold
}

// medianDuration returns the median of the durations, sorting a copy so the caller's
// slice is untouched. The median (not the mean) is deliberate: one long gap — a holiday,
// a blocked week — must not drag the threshold out of shape.
func medianDuration(values []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// delegationAttention collects delegated cards: the ones awaiting a human, and the
// ones whose card has vanished. A card waiting on a person is the one class that
// blocks progress until someone acts, so it is always high priority.
func (s *Service) delegationAttention(ctx context.Context, store *workcontext.Store, scope workcontext.Scope) ([]AttentionItem, error) {
	ledger := s.TaskboardLedger()
	if ledger == nil {
		return nil, nil
	}
	delegations, err := store.ListOpenDelegations(ctx, scope, 200)
	if err != nil {
		return nil, err
	}
	items := []AttentionItem{}
	for _, link := range delegations {
		card, getErr := ledger.GetCard(link.CardID)
		if getErr != nil || card.Deleted {
			// The card is gone. The detail view reports source_missing, but a link
			// nobody can resolve is itself something the user must decide about, so
			// it appears as its own state instead of silently vanishing.
			items = append(items, AttentionItem{
				Kind: AttentionKindDelegationMissing, Priority: AttentionNormal,
				ContextID: link.ContextID, ContextTitle: link.ContextTitle,
				Title: link.Title, Detail: "委派卡已丢失", Tab: "delegations",
				// The card id is stable even though the card is gone, so this entry can be
				// ruled on individually.
				EntityID: link.CardID,
			})
			continue
		}
		if card.Status != taskboard.StatusInReview {
			continue
		}
		items = append(items, AttentionItem{
			Kind: AttentionKindDelegationReview, Priority: AttentionHigh,
			ContextID: link.ContextID, ContextTitle: link.ContextTitle,
			Title: card.Title, Detail: "等待验收", Tab: "tasks", EntityID: card.ID,
		})
	}
	return items, nil
}

// factAttention collects facts still awaiting review. "candidate" is the only
// fact state that needs a person; the others are terminal for triage purposes.
func (s *Service) factAttention(ctx context.Context, store *workcontext.Store, scope workcontext.Scope) ([]AttentionItem, error) {
	facts, err := store.ListCandidateFacts(ctx, scope, 200)
	if err != nil {
		return nil, err
	}
	items := []AttentionItem{}
	for _, fact := range facts {
		items = append(items, AttentionItem{
			Kind: AttentionKindFactReview, Priority: AttentionNormal,
			ContextID: fact.ContextID, ContextTitle: fact.ContextTitle,
			Title: fact.Title, Detail: "待审核事实", Tab: "knowledge", EntityID: fact.ID,
		})
	}
	return items, nil
}

// planAttention collects plan items that are overdue, due soon, or explicitly
// blocked. A blocked item is included whether or not it carries a due date: it is
// stuck either way, and "stuck with no date" is exactly the case a reminder is
// for.
func (s *Service) planAttention(ctx context.Context, store *workcontext.Store, scope workcontext.Scope, now time.Time) ([]AttentionItem, error) {
	todos, err := store.ListOpenPlanTodos(ctx, scope, 200)
	if err != nil {
		return nil, err
	}
	items := []AttentionItem{}
	for _, todo := range todos {
		// A blocked item is reported once, as blocked: ListOpenPlanTodos cannot
		// filter it out (it exists for deadlines) and a blocked item with a date
		// would otherwise appear twice, as "blocked" AND as "overdue".
		if todo.Status == "blocked" {
			continue
		}
		switch workcontext.DueStateOf(todo.DueAt, now, dueSoonWindow) {
		case workcontext.DueOverdue:
			items = append(items, AttentionItem{
				Kind: AttentionKindTodoOverdue, Priority: AttentionNormal,
				ContextID: todo.ContextID, ContextTitle: todo.ContextTitle,
				Title: todo.Text, Detail: "已过期", DueAt: todo.DueAt, Tab: "plan",
				EntityID: todo.ID,
			})
		case workcontext.DueSoon:
			items = append(items, AttentionItem{
				Kind: AttentionKindTodoSoon, Priority: AttentionLow,
				ContextID: todo.ContextID, ContextTitle: todo.ContextTitle,
				Title: todo.Text, Detail: "即将到期", DueAt: todo.DueAt, Tab: "plan",
				EntityID: todo.ID,
			})
		}
	}
	blocked, err := store.ListBlockedPlanTodos(ctx, scope, 200)
	if err != nil {
		return nil, err
	}
	for _, todo := range blocked {
		items = append(items, AttentionItem{
			Kind: AttentionKindTodoBlocked, Priority: AttentionNormal,
			ContextID: todo.ContextID, ContextTitle: todo.ContextTitle,
			Title: todo.Text, Detail: "计划项受阻", DueAt: todo.DueAt, Tab: "plan",
			EntityID: todo.ID,
		})
	}
	return items, nil
}

// checkpointAttention collects the stream's own recorded blockers and open
// questions. They are the user's explicit "this is not resolved" markers, so they
// belong in the same list as deadlines rather than only inside one stream's
// overview card.
func (s *Service) checkpointAttention(ctx context.Context, store *workcontext.Store, scope workcontext.Scope) ([]AttentionItem, error) {
	streams, err := store.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	items := []AttentionItem{}
	for _, stream := range streams {
		// List returns archived streams too; an archived stream is read-only and
		// must not ask for attention.
		if stream.Status == "archived" {
			continue
		}
		items = appendCheckpointList(items, stream, stream.Checkpoint.Blockers,
			AttentionKindCheckpointBlocker, AttentionNormal, "检查点阻塞")
		items = appendCheckpointList(items, stream, stream.Checkpoint.OpenQuestions,
			AttentionKindCheckpointQuestion, AttentionLow, "待确认问题")
		if len(items) >= attentionCheckpointLimit {
			break
		}
	}
	return items, nil
}

// appendCheckpointList adds one line per non-blank entry, honouring the overall
// cap: these lists grow for the life of a stream and the inbox is a triage view.
func appendCheckpointList(items []AttentionItem, stream workcontext.Context, list []string, kind, priority, detail string) []AttentionItem {
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		items = append(items, AttentionItem{
			Kind: kind, Priority: priority,
			ContextID: stream.ID, ContextTitle: stream.Title,
			Title: entry, Detail: detail, Tab: "overview",
		})
		if len(items) >= attentionCheckpointLimit {
			break
		}
	}
	return items
}

// groupAttention collapses same-kind items from the same stream into one row.
// Attention cost tracks the number of ROWS, not the number of facts, so "this
// stream moved five times" must read as one line that says five — that is the
// difference between a scannable inbox and a wall.
//
// It also records the individual ENTRIES it collapsed (with their per-entry action
// keys), which is what makes "ignore this one, not all three" possible. A row of one
// keeps no entry list: the row and the entry are the same thing there, and a
// one-element list would only be a second way to say the same sentence.
func groupAttention(items []AttentionItem) []AttentionItem {
	type groupKey struct{ kind, contextID string }
	index := map[groupKey]int{}
	out := make([]AttentionItem, 0, len(items))
	entryOf := func(item AttentionItem) AttentionEntry {
		return AttentionEntry{
			Key:   workcontext.AttentionEntryKeyFor(item.Kind, item.ContextID, item.EntityID),
			Title: item.Title, Detail: item.Detail, DueAt: item.DueAt,
		}
	}
	for _, item := range items {
		key := groupKey{item.Kind, item.ContextID}
		at, seen := index[key]
		if !seen {
			index[key] = len(out)
			out = append(out, item)
			continue
		}
		merged := &out[at]
		if merged.Count == 0 {
			// The first entry is only NOW known to be part of a group, so backfill it: the
			// collapsed row must stand for every entry, not all-but-the-first.
			merged.Count = 1
			merged.Entries = append(merged.Entries, entryOf(*merged))
		}
		merged.Count++
		if len(merged.Also) < attentionGroupTitlesLimit {
			merged.Also = append(merged.Also, item.Title)
		}
		if len(merged.Entries) < attentionGroupEntriesLimit {
			merged.Entries = append(merged.Entries, entryOf(item))
		}
		if earlierDue(item.DueAt, merged.DueAt) {
			merged.DueAt = item.DueAt
		}
	}
	return out
}

// applyAttentionActions splits the rows into the working list and the deferred list
// using the owner's recorded verbs.
//
// Two levels of verdict are honoured, and the more SPECIFIC one wins:
//   - an ENTRY-level verb (keyed by the underlying record id) rules on one entry of a
//     collapsed row — "ignore this one, not all three";
//   - a GROUP-level verb rules on the whole row.
//
// Entry-level wins because it is the more deliberate statement: someone who singled
// out one entry has said more than someone who ruled on the row, so letting the broader
// verdict override the narrower one would silently discard their precise intent.
//
// A row leaves the list only when ALL of its entries are deferred. Otherwise it stays
// with what is left, because dropping a row whose first entry was ignored would hide
// the siblings the user never ruled on.
func applyAttentionActions(items []AttentionItem, actions map[string]workcontext.AttentionAction, now time.Time) ([]AttentionItem, []DeferredAttention) {
	active := make([]AttentionItem, 0, len(items))
	deferred := []DeferredAttention{}
	for _, item := range items {
		groupAction, hasGroupAction := actions[item.ItemKey]

		if len(item.Entries) == 0 {
			// No per-entry keys to consider (a single-entry row, or a kind whose entries
			// have no stable id): the group verdict is the only verdict there is.
			if !hasGroupAction || !workcontext.AttentionActionActive(groupAction, now) {
				active = append(active, item)
				continue
			}
			if groupAction.Verb == workcontext.AttentionVerbClaim {
				claimed := item
				claimed.Action = &groupAction
				active = append(active, claimed)
				continue
			}
			deferred = append(deferred, DeferredAttention{Item: item, Action: groupAction})
			continue
		}

		// A grouped row: partition its entries by whether each one is deferred.
		remaining := []AttentionEntry{}
		for _, entry := range item.Entries {
			verdict := groupAction
			hasVerdict := hasGroupAction
			if entry.Key != "" {
				if entryAction, ok := actions[entry.Key]; ok {
					verdict, hasVerdict = entryAction, true
				}
			}
			if !hasVerdict || !workcontext.AttentionActionActive(verdict, now) {
				remaining = append(remaining, entry)
				continue
			}
			if verdict.Verb == workcontext.AttentionVerbClaim {
				// A claim is work, not deferral, so the entry stays listed — carrying its own
				// state, otherwise "I took this one" would look like it did nothing.
				claimed := entry
				verdictCopy := verdict
				claimed.Action = &verdictCopy
				remaining = append(remaining, claimed)
				continue
			}
			// A deferred entry is reported as its own line: the verdict applied to that
			// one entry, and folding it into the row's copy would lose which entry it was.
			deferred = append(deferred, DeferredAttention{
				Item: AttentionItem{
					Kind: item.Kind, Priority: item.Priority,
					ContextID: item.ContextID, ContextTitle: item.ContextTitle,
					Title: entry.Title, Detail: entry.Detail, DueAt: entry.DueAt,
					Tab: item.Tab, ItemKey: entry.Key,
				},
				Action: verdict,
			})
		}
		if len(remaining) == 0 {
			continue
		}
		// The row survives with what is left of it. A single remaining entry is no longer a
		// group, so it is reported as that entry rather than as a one-item group.
		keeper := item
		keeper.Entries = nil
		keeper.Also = nil
		keeper.Title = remaining[0].Title
		keeper.Detail = remaining[0].Detail
		keeper.DueAt = remaining[0].DueAt
		if len(remaining) == 1 {
			keeper.Count = 0
		} else {
			keeper.Count = len(remaining)
			keeper.Entries = remaining
			for _, entry := range remaining[1:] {
				if len(keeper.Also) < attentionGroupTitlesLimit {
					keeper.Also = append(keeper.Also, entry.Title)
				}
			}
		}
		if hasGroupAction && groupAction.Verb == workcontext.AttentionVerbClaim &&
			workcontext.AttentionActionActive(groupAction, now) {
			keeper.Action = &groupAction
		}
		// A row that collapsed to a single surviving entry must carry THAT entry's own
		// verdict, or a claim made on the entry would disappear from the row it lives on.
		if keeper.Count == 0 && len(remaining) == 1 && remaining[0].Action != nil {
			keeper.Action = remaining[0].Action
		}
		active = append(active, keeper)
	}
	return active, deferred
}

// earlierDue reports whether dueA is strictly before dueB. Unparseable values
// never win: a reminder built on a bad timestamp is worse than none.
func earlierDue(dueA, dueB string) bool {
	a, aok := workcontext.ParseDueAt(dueA)
	if !aok {
		return false
	}
	b, bok := workcontext.ParseDueAt(dueB)
	if !bok {
		return true
	}
	return a.Before(b)
}

// sortAttention orders the inbox so the top of the list is what most deserves the
// next slice of attention: priority first, then leverage (how many other streams
// are blocked on this one), then the earliest due time, then a stable tiebreak so
// the order does not jitter between polls. Overdue sorts before merely-soon
// because a passed deadline is the more urgent fact.
func sortAttention(items []AttentionItem, leverage map[string]int) {
	rank := map[string]int{AttentionHigh: 0, AttentionNormal: 1, AttentionLow: 2}
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := rank[items[i].Priority], rank[items[j].Priority]
		if ri != rj {
			return ri < rj
		}
		li, lj := leverage[items[i].ContextID], leverage[items[j].ContextID]
		if li != lj {
			return li > lj
		}
		di, iok := workcontext.ParseDueAt(items[i].DueAt)
		dj, jok := workcontext.ParseDueAt(items[j].DueAt)
		switch {
		case iok && jok && !di.Equal(dj):
			return di.Before(dj)
		case iok != jok:
			return iok
		}
		if items[i].ContextTitle != items[j].ContextTitle {
			return items[i].ContextTitle < items[j].ContextTitle
		}
		return items[i].Title < items[j].Title
	})
}

// StartAttentionSweep launches the background progress/nudge pass. It is
// idempotent, and it shares the service's process lock: two runtimes on one
// state directory would double-notify.
func (s *Service) StartAttentionSweep(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("backend service unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	if s.attentionCancel != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.attentionCancel = cancel
	s.attentionDone = done
	go s.runAttentionSweep(runCtx, done)
	return nil
}

// StopAttentionSweep cancels the sweep and waits for its pass to finish.
func (s *Service) StopAttentionSweep(ctx context.Context) {
	s.attentionMu.Lock()
	cancel := s.attentionCancel
	done := s.attentionDone
	s.attentionCancel = nil
	s.attentionDone = nil
	s.attentionMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return
	}
	if ctx == nil {
		<-done
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *Service) runAttentionSweep(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(attentionSweepInterval)
	defer ticker.Stop()
	// One immediate pass so a just-finished card does not wait a full interval.
	s.sweepAttentionOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepAttentionOnce(ctx)
		}
	}
}

// sweepAttentionOnce walks every owner's streams: records card progress as
// events (so the wall's unread badge is honest without opening the stream) and
// pushes the subset that needs a human.
func (s *Service) sweepAttentionOnce(ctx context.Context) {
	store, err := s.WorkContexts()
	if err != nil {
		return
	}
	scopes, err := store.ListOwnedScopes(ctx)
	if err != nil {
		// A canceled context is normal shutdown, not a fault: the sweep shares the
		// service run context, so Stop cancels it mid-pass by design.
		if ctx.Err() == nil {
			logger.Warnf("attention sweep: list scopes: %v", err)
		}
		return
	}
	for _, owned := range scopes {
		if ctx.Err() != nil {
			return
		}
		s.sweepScope(ctx, store, owned.Scope)
	}
}

func (s *Service) sweepScope(ctx context.Context, store *workcontext.Store, scope workcontext.Scope) {
	now := s.now()
	ledger := s.TaskboardLedger()

	// The owner's recorded verbs, read ONCE for the whole pass. Suppression is
	// checked from this snapshot, and it is checked in pushNudge BEFORE the event is
	// recorded — recording first would mark a snoozed reminder as "already sent",
	// which is the permanent-swallow failure pushNudge's ordering exists to prevent.
	actions, err := store.AttentionActionIndex(ctx, scope)
	if err != nil {
		if ctx.Err() == nil {
			logger.Warnf("attention sweep: read actions: %v", err)
		}
		return
	}

	// Progress from delegated work. This is the fix for "progress only appears
	// once you open the desktop": the event is written here, on a timer, using
	// the SAME dedup key the read path uses — so reading the desktop later does
	// not duplicate it, and the wall's unread count is truthful.
	if ledger != nil {
		delegations, err := store.ListOpenDelegations(ctx, scope, 200)
		if err != nil {
			if ctx.Err() == nil {
				logger.Warnf("attention sweep: list delegations: %v", err)
			}
			return
		}
		for _, link := range delegations {
			card, getErr := ledger.GetCard(link.CardID)
			if getErr != nil || card.Deleted {
				continue
			}
			statusKey := fmt.Sprintf("task:%s:v%d", card.ID, card.Version)
			isNew, err := store.RecordEventIfNew(ctx, scope, link.ContextID, "task_status",
				card.Title+" · "+card.Status, "taskboard://"+card.ID, statusKey)
			if err != nil {
				continue
			}
			// Only a card waiting on a human is worth an interruption; every
			// other transition already raised the unread badge.
			if isNew && card.Status == taskboard.StatusInReview {
				s.pushNudge(ctx, store, scope, now, nudgeEventKind, link.ContextID,
					fmt.Sprintf("nudge:card:%s:v%d", card.ID, card.Version),
					AttentionKindDelegationReview, card.ID,
					card.Title+" 等待验收", "委派任务已完成，等待你验收（"+link.ContextTitle+"）", actions)
			}
		}
	}

	// Overdue and soon-due plan items. Collected first, then routed: the same candidates
	// either collapse into ONE morning page (see deliverPlanNudges) or go out one push each.
	// Collecting is what makes the choice possible — and the choice must be made before any
	// push, since a delivered reminder is deduplicated forever and could not be re-batched.
	todos, err := store.ListOpenPlanTodos(ctx, scope, 200)
	if err != nil {
		if ctx.Err() == nil {
			logger.Warnf("attention sweep: list plan items: %v", err)
		}
		return
	}
	candidates := make([]planNudge, 0, len(todos))
	for _, todo := range todos {
		state := workcontext.DueStateOf(todo.DueAt, now, dueSoonWindow)
		if state == workcontext.DueNone {
			continue
		}
		body := "计划项即将到期"
		itemKind := AttentionKindTodoSoon
		if state == workcontext.DueOverdue {
			body = "计划项已过期"
			itemKind = AttentionKindTodoOverdue
		}
		candidates = append(candidates, planNudge{
			contextID: todo.ContextID,
			dedup:     fmt.Sprintf("nudge:todo:%s:%s:%s", todo.ID, todo.DueAt, state),
			itemKind:  itemKind,
			entityID:  todo.ID,
			title:     body + ": " + todo.Text,
			body:      todo.ContextTitle,
		})
	}
	if s.deliverPlanNudges(ctx, store, scope, now, actions, candidates) {
		return
	}
	for _, candidate := range candidates {
		s.pushNudge(ctx, store, scope, now, nudgeEventKind, candidate.contextID,
			candidate.dedup, candidate.itemKind, candidate.entityID, candidate.title, candidate.body, actions)
	}
}

// pushNudge delivers a reminder, at most once.
//
// Ordering matters and is deliberate, in TWO steps. The owner's verb is honoured
// first (a snoozed or dismissed row is not a reminder at all), then the discipline
// check, and only then is the event recorded. Recording before the discipline check
// would make a quiet-hours-blocked reminder count as "already sent" forever,
// silently swallowing it — the one outcome a reminder feature must never produce.
// This way a blocked nudge simply retries on the next sweep, while a delivered one
// is deduplicated permanently by its dedup key.
//
// itemKind is the inbox row kind this notification corresponds to, and entityID is the
// underlying record id. Both are needed here because inbox verbs are stored against
// inbox identities (group key "{kind}:{context_id}", entry key "{kind}:{context_id}:{entityID}")
// and the sweep must respect a verdict the user applied from the inbox rather than
// inventing a second, event-shaped identity for the same thing.
func (s *Service) pushNudge(ctx context.Context, store *workcontext.Store, scope workcontext.Scope,
	now time.Time, kind, contextID, dedup, itemKind, entityID, title, body string,
	actions map[string]workcontext.AttentionAction) {
	// Same resolution order as the inbox (entry verdict beats group verdict), so a row
	// the user ruled on in the inbox is not then pushed about.
	entryKey := workcontext.AttentionEntryKeyFor(itemKind, contextID, entityID)
	groupKey := workcontext.AttentionItemKey(itemKind, contextID)
	if action, ok := workcontext.AttentionActionFor(actions, entryKey, groupKey); ok &&
		workcontext.AttentionActionActive(action, now) {
		return
	}
	notifier := s.notifier()
	if notifier == nil {
		return
	}
	if !s.pushAllowed(ctx, store, scope, contextID, now) {
		return
	}
	isNew, err := store.RecordEventIfNew(ctx, scope, contextID, kind, title, "attention", dedup)
	if err != nil || !isNew {
		return
	}
	if _, err := notifier.NotifyOwner(ctx, workcontext.ScopeKey(scope), "GoDex 工作桌面", title+" · "+body); err != nil {
		logger.Warnf("attention sweep: notify %s: %v", scope.OwnerID, err)
	}
}

// pushAllowed applies quiet hours, the owner's per-day cap and a per-stream share
// of that cap to non-decision nudges.
//
// Decision-class items bypass the cap in principle, but they are requested with
// the same helper today because their dedup key already bounds them to one per
// card version.
//
// The per-stream share exists because one global cap is unfairly silent: with only
// an owner-level budget, whichever stream happens to have the most due items
// consumes it and the other streams never speak. Both limits are applied here,
// BEFORE the event is recorded — see pushNudge on why that ordering matters.
func (s *Service) pushAllowed(ctx context.Context, store *workcontext.Store, scope workcontext.Scope, contextID string, now time.Time) bool {
	if inQuietHours(now) {
		return false
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	sent, err := store.CountEventsByKindSince(ctx, scope, nudgeEventKind, midnight)
	if err != nil {
		// Counting failed: staying silent is the safer failure for a notifier.
		return false
	}
	if sent >= attentionDailyCap {
		return false
	}
	streamSent, err := store.CountEventsByKindSinceForContext(ctx, scope, contextID, nudgeEventKind, midnight)
	if err != nil {
		return false
	}
	return streamSent < attentionPerStreamDailyCap
}

// inQuietHours reports whether local time falls in the silent window. The window
// wraps midnight, so the comparison is a disjunction rather than a range.
func inQuietHours(now time.Time) bool {
	hour := now.Hour()
	if quietHoursStart > quietHoursEnd {
		return hour >= quietHoursStart || hour < quietHoursEnd
	}
	return hour >= quietHoursStart && hour < quietHoursEnd
}
