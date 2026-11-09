package backend

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/platform/logger"
)

const (
	// attentionDigestTopItems bounds how many item titles the one page spells out. The page
	// is a heads-up, not the list: the full, drill-down version is the cross-stream inbox
	// the user already has, and a page that reproduced it would be a second inbox to read.
	attentionDigestTopItems = 3
	// attentionDigestCatchUpHours is the morning window in which a digest may go out,
	// measured from the end of quiet hours. Batching is for the OVERNIGHT catch-up — the
	// reminders that accumulated while nothing could be delivered. Outside the window a
	// reminder goes out on its own, because a single item surfacing at 14:00 should say what
	// it is, not arrive as a one-line "daily summary".
	attentionDigestCatchUpHours = 4
)

// planNudge is one plan-item reminder the sweep wants to deliver, before the delivery route
// is chosen. Collecting them first is what makes a digest possible at all: the same
// candidates either go out one push each, or collapse into ONE page.
type planNudge struct {
	contextID string
	// dedup is the permanent once-per-item key, identical to the individual push's, so a
	// digested item is recorded exactly as a pushed one and can never fire twice.
	dedup string
	// itemKind/entityID identify the inbox row this reminder corresponds to, so a verdict the
	// user applied in the inbox suppresses the reminder on this path too.
	itemKind string
	entityID string
	// title is the reminder line ("计划项已过期: …"); body is the stream it belongs to.
	title string
	body  string
}

// nudgeSuppressed reports whether a row is not a reminder at all: the owner already ruled on
// it. The resolution order matches the inbox exactly (entry verdict beats group verdict),
// extracted so the digest path and the individual push path cannot disagree about which rows
// are still live.
func nudgeSuppressed(actions map[string]workcontext.AttentionAction,
	itemKind, contextID, entityID string, now time.Time) bool {
	entryKey := workcontext.AttentionEntryKeyFor(itemKind, contextID, entityID)
	groupKey := workcontext.AttentionItemKey(itemKind, contextID)
	action, ok := workcontext.AttentionActionFor(actions, entryKey, groupKey)
	if !ok {
		return false
	}
	return workcontext.AttentionActionActive(action, now)
}

// inDigestCatchUpWindow reports whether now falls in the morning batch window (see
// attentionDigestCatchUpHours).
func inDigestCatchUpWindow(now time.Time) bool {
	hour := now.Hour()
	return hour >= quietHoursEnd && hour < quietHoursEnd+attentionDigestCatchUpHours
}

// digestDue reports whether the daily digest is still owed for this local day.
//
// Note what this does NOT do: it does not rescue reminders blocked by quiet hours. Those
// were never lost — pushAllowed runs BEFORE the event is recorded, so a blocked reminder
// stays unrecorded and simply retries. The digest exists so that the retries arrive as ONE
// page at the end of the quiet window instead of as a burst.
func (s *Service) digestDue(ctx context.Context, store *workcontext.Store, scope workcontext.Scope, now time.Time) bool {
	if !inDigestCatchUpWindow(now) {
		return false
	}
	delivered, err := store.DigestDeliveredOn(ctx, scope, workcontext.DigestDay(now))
	if err != nil {
		// Counting failed: staying silent is the safer failure for a notifier.
		return false
	}
	return !delivered
}

// composeDigest renders the one page: how many items are waiting, and which ones.
func composeDigest(items []planNudge) (title, body string) {
	total := len(items)
	title = fmt.Sprintf("今日汇总：%d 项待处理", total)
	if total == 0 {
		return title, ""
	}
	shown := items
	if len(shown) > attentionDigestTopItems {
		shown = shown[:attentionDigestTopItems]
	}
	lines := make([]string, 0, len(shown)+1)
	for _, item := range shown {
		// The stream matters as much as the line: it is how the user knows where to look, and
		// an item's title alone can be a fragment.
		if item.body == "" {
			lines = append(lines, item.title)
			continue
		}
		lines = append(lines, item.title+"（"+item.body+"）")
	}
	body = strings.Join(lines, "；")
	if total > len(shown) {
		body += fmt.Sprintf("；另有 %d 项", total-len(shown))
	}
	return title, body
}

// deliverPlanNudges tries to deliver this pass's plan-item reminders as ONE daily page,
// reporting whether it handled them. "false" means the caller must fall back to the
// individual pushes — which happens whenever no digest is owed (outside the catch-up window,
// or already sent today), the owner's budget is spent, nothing is newly worth reporting, or
// the send failed.
//
// Cost discipline: the "already recorded?" check runs ONLY here, after the cheap due checks,
// because it needs a query per item and this function is called every sweep. The individual
// path does not need it — its own write is the dedup.
//
// Ordering, deliberately: claim the day, send, and only then record the items as reported.
// A failed send must not absorb them — recording first would mark as "already told"
// reminders the user never received, which is the permanent-swallow failure the individual
// path's ordering exists to avoid. Losing a day costs one digest; swallowing a reminder
// costs the feature.
func (s *Service) deliverPlanNudges(ctx context.Context, store *workcontext.Store, scope workcontext.Scope,
	now time.Time, actions map[string]workcontext.AttentionAction, candidates []planNudge) bool {
	if len(candidates) == 0 {
		return false
	}
	if !s.digestDue(ctx, store, scope, now) {
		return false
	}
	notifier := s.notifier()
	if notifier == nil {
		return false
	}

	// Only items that are neither ruled on nor already announced belong on the page. An
	// item still overdue tomorrow was already reported today, and re-announcing it every
	// morning is exactly the noise this feature exists to remove.
	fresh := make([]planNudge, 0, len(candidates))
	for _, item := range candidates {
		if nudgeSuppressed(actions, item.itemKind, item.contextID, item.entityID, now) {
			continue
		}
		announced, err := store.HasEventDedup(ctx, scope, item.contextID, item.dedup)
		if err != nil {
			continue
		}
		if announced {
			continue
		}
		fresh = append(fresh, item)
	}
	if len(fresh) == 0 {
		return false
	}

	// The page is bounded by what is left of the day's budget, and it CONSUMES one slot per
	// item reported: the budget bounds how much attention-demanding content is pushed, and a
	// page of five items asks for five items' worth of attention even though it is one packet.
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	sent, err := store.CountEventsByKindSince(ctx, scope, nudgeEventKind, midnight)
	if err != nil || sent >= attentionDailyCap {
		return false
	}
	items := fresh
	if remaining := attentionDailyCap - sent; len(items) > remaining {
		items = items[:remaining]
	}

	title, body := composeDigest(items)
	day := workcontext.DigestDay(now)
	claimed, err := store.ClaimDigestDelivery(ctx, scope, day)
	if err != nil {
		logger.Warnf("attention digest: claim %s: %v", scope.OwnerID, err)
		return false
	}
	if !claimed {
		// Another sweep already sent today's page; anything new is handled by the normal path.
		return false
	}
	if _, err := notifier.NotifyOwner(ctx, workcontext.ScopeKey(scope), "GoDex 工作桌面", title+" · "+body); err != nil {
		logger.Warnf("attention digest: notify %s: %v", scope.OwnerID, err)
		// Not absorbed: the items fall through to individual pushes, so a failed digest does
		// not swallow them.
		return false
	}
	for _, item := range items {
		if _, err := store.RecordEventIfNew(ctx, scope, item.contextID, nudgeEventKind, item.title, "attention", item.dedup); err != nil {
			logger.Warnf("attention digest: record %s: %v", item.dedup, err)
		}
	}
	if err := store.RecordDigestItemCount(ctx, scope, day, len(items)); err != nil {
		logger.Warnf("attention digest: record count %s: %v", scope.OwnerID, err)
	}
	return true
}
