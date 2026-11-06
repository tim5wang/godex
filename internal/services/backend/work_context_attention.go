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
}

// AttentionView is the inbox plus per-stream unread counts, so the desktop wall
// can show a badge for every stream from the same request.
type AttentionView struct {
	Items       []AttentionItem `json:"items"`
	Unread      map[string]int  `json:"unread"`
	Counts      map[string]int  `json:"counts"`
	GeneratedAt string          `json:"generated_at"`
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
	for i := range items {
		items[i].BlockedCount = leverage[items[i].ContextID]
	}
	sortAttention(items, leverage)

	unread, err := store.UnreadCounts(ctx, scope)
	if err != nil {
		return AttentionView{}, err
	}
	counts := map[string]int{}
	for _, item := range items {
		counts[item.Kind]++
	}
	return AttentionView{Items: items, Unread: unread, Counts: counts, GeneratedAt: now.UTC().Format(time.RFC3339Nano)}, nil
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
			})
			continue
		}
		if card.Status != taskboard.StatusInReview {
			continue
		}
		items = append(items, AttentionItem{
			Kind: AttentionKindDelegationReview, Priority: AttentionHigh,
			ContextID: link.ContextID, ContextTitle: link.ContextTitle,
			Title: card.Title, Detail: "等待验收", Tab: "tasks",
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
			Title: fact.Title, Detail: "待审核事实", Tab: "knowledge",
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
			})
		case workcontext.DueSoon:
			items = append(items, AttentionItem{
				Kind: AttentionKindTodoSoon, Priority: AttentionLow,
				ContextID: todo.ContextID, ContextTitle: todo.ContextTitle,
				Title: todo.Text, Detail: "即将到期", DueAt: todo.DueAt, Tab: "plan",
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
func groupAttention(items []AttentionItem) []AttentionItem {
	type groupKey struct{ kind, contextID string }
	index := map[groupKey]int{}
	out := make([]AttentionItem, 0, len(items))
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
			merged.Count = 1
		}
		merged.Count++
		if len(merged.Also) < attentionGroupTitlesLimit {
			merged.Also = append(merged.Also, item.Title)
		}
		if earlierDue(item.DueAt, merged.DueAt) {
			merged.DueAt = item.DueAt
		}
	}
	return out
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
					card.Title+" 等待验收", "委派任务已完成，等待你验收（"+link.ContextTitle+"）")
			}
		}
	}

	// Overdue and soon-due plan items.
	todos, err := store.ListOpenPlanTodos(ctx, scope, 200)
	if err != nil {
		if ctx.Err() == nil {
			logger.Warnf("attention sweep: list plan items: %v", err)
		}
		return
	}
	for _, todo := range todos {
		state := workcontext.DueStateOf(todo.DueAt, now, dueSoonWindow)
		if state == workcontext.DueNone {
			continue
		}
		body := "计划项即将到期"
		if state == workcontext.DueOverdue {
			body = "计划项已过期"
		}
		s.pushNudge(ctx, store, scope, now, nudgeEventKind, todo.ContextID,
			fmt.Sprintf("nudge:todo:%s:%s:%s", todo.ID, todo.DueAt, state),
			body+": "+todo.Text, todo.ContextTitle)
	}
}

// pushNudge delivers a reminder, at most once.
//
// Ordering matters and is deliberate: the discipline check runs BEFORE the event
// is recorded. Recording first would make a quiet-hours-blocked reminder count as
// "already sent" forever, silently swallowing it — the one outcome a reminder
// feature must never produce. This way a blocked nudge simply retries on the next
// sweep, while a delivered one is deduplicated permanently by its dedup key.
func (s *Service) pushNudge(ctx context.Context, store *workcontext.Store, scope workcontext.Scope,
	now time.Time, kind, contextID, dedup, title, body string) {
	notifier := s.notifier()
	if notifier == nil {
		return
	}
	if !s.pushAllowed(ctx, store, scope, now) {
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

// pushAllowed applies quiet hours and the per-day cap to non-decision nudges.
// Decision-class items bypass the cap in principle, but they are requested with
// the same helper today because their dedup key already bounds them to one per
// card version.
func (s *Service) pushAllowed(ctx context.Context, store *workcontext.Store, scope workcontext.Scope, now time.Time) bool {
	if inQuietHours(now) {
		return false
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	sent, err := store.CountEventsByKindSince(ctx, scope, nudgeEventKind, midnight)
	if err != nil {
		// Counting failed: staying silent is the safer failure for a notifier.
		return false
	}
	return sent < attentionDailyCap
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
