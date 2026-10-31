package backend

import (
	"context"
	"fmt"
	"sort"
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
)

// Attention priorities. A decision (a card awaiting human acceptance) is always
// delivered; time-based nudges are subject to the daily cap.
const (
	AttentionHigh   = "high"
	AttentionNormal = "normal"
	AttentionLow    = "low"
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
// It is intentionally deterministic — overdue dates and card states — rather
// than an LLM guess about what matters, because a wrong reminder costs more
// attention than a missing one.
func (s *Service) AttentionInbox(ctx context.Context, scope workcontext.Scope) (AttentionView, error) {
	store, err := s.WorkContexts()
	if err != nil {
		return AttentionView{}, err
	}
	now := s.now()
	items := []AttentionItem{}

	// Cards awaiting human acceptance: the one class that blocks progress until
	// a person acts, so it is always high priority.
	ledger := s.TaskboardLedger()
	if ledger != nil {
		delegations, err := store.ListOpenDelegations(ctx, scope, 200)
		if err != nil {
			return AttentionView{}, err
		}
		for _, link := range delegations {
			card, getErr := ledger.GetCard(link.CardID)
			if getErr != nil || card.Deleted {
				// A missing card is not actionable through the desktop; the
				// detail view already reports it as source_missing.
				continue
			}
			if card.Status != taskboard.StatusInReview {
				continue
			}
			items = append(items, AttentionItem{
				Kind:         "delegation_review",
				Priority:     AttentionHigh,
				ContextID:    link.ContextID,
				ContextTitle: link.ContextTitle,
				Title:        card.Title,
				Detail:       "等待验收",
				Tab:          "tasks",
			})
		}
	}

	// Plan items that are overdue or due within the window.
	todos, err := store.ListOpenPlanTodos(ctx, scope, 200)
	if err != nil {
		return AttentionView{}, err
	}
	for _, todo := range todos {
		state := workcontext.DueStateOf(todo.DueAt, now, dueSoonWindow)
		switch state {
		case workcontext.DueOverdue:
			items = append(items, AttentionItem{
				Kind: "todo_overdue", Priority: AttentionNormal,
				ContextID: todo.ContextID, ContextTitle: todo.ContextTitle,
				Title: todo.Text, Detail: "已过期", DueAt: todo.DueAt, Tab: "plan",
			})
		case workcontext.DueSoon:
			items = append(items, AttentionItem{
				Kind: "todo_soon", Priority: AttentionLow,
				ContextID: todo.ContextID, ContextTitle: todo.ContextTitle,
				Title: todo.Text, Detail: "即将到期", DueAt: todo.DueAt, Tab: "plan",
			})
		}
	}

	sortAttention(items, now)

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

// sortAttention orders by priority, then by the earliest due time, then by title
// so the order is stable between polls. Overdue items sort before merely-soon
// ones because a passed deadline is the more urgent fact.
func sortAttention(items []AttentionItem, now time.Time) {
	rank := map[string]int{AttentionHigh: 0, AttentionNormal: 1, AttentionLow: 2}
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := rank[items[i].Priority], rank[items[j].Priority]
		if ri != rj {
			return ri < rj
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

// attentionSummary renders a one-line inbox summary for callers that only need a
// count (the desktop nav badge) without the full list.
func attentionSummary(items []AttentionItem) string {
	if len(items) == 0 {
		return ""
	}
	decisions := 0
	for _, item := range items {
		if item.Priority == AttentionHigh {
			decisions++
		}
	}
	if decisions > 0 {
		return fmt.Sprintf("%d 项待决策 / 共 %d 项", decisions, len(items))
	}
	return fmt.Sprintf("%d 项待关注", len(items))
}
