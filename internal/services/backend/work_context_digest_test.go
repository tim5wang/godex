package backend

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/workcontext"
)

// digestTestService builds a service whose clock sits inside the morning catch-up window,
// which is the only time a digest may go out.
func digestTestService(t *testing.T, now time.Time) (*Service, *recordingNotifier, workcontext.Scope) {
	t.Helper()
	if !inDigestCatchUpWindow(now) {
		t.Fatalf("test clock %s is outside the digest window", now)
	}
	return newAttentionService(t, now)
}

// TestDigestBatchesTheMorningCatchUpIntoOnePage is the feature's whole point: the reminders
// that accumulated overnight arrive as ONE page instead of one push each. Without batching,
// waking up after quiet hours means a burst of separate notifications, which is the noise
// this stream is supposed to reduce.
func TestDigestBatchesTheMorningCatchUpIntoOnePage(t *testing.T) {
	base := time.Date(2026, 11, 9, 8, 30, 0, 0, time.Local)
	service, notifier, scope := digestTestService(t, base)
	first := mustStream(t, service, scope, "语音服务")
	second := mustStream(t, service, scope, "模型对齐")
	mustOverdueTodo(t, service, scope, first.ID, "补齐评测脚本", base)
	mustOverdueTodo(t, service, scope, first.ID, "固定权重参数", base)
	mustOverdueTodo(t, service, scope, second.ID, "对齐印尼语评测", base)
	// One more than the page spells out, so the "and N more" honesty can be asserted.
	mustOverdueTodo(t, service, scope, second.ID, "回滚流程演练", base)

	service.sweepAttentionOnce(context.Background())

	// Four waiting items, ONE delivery.
	if got := notifier.count(); got != 1 {
		t.Fatalf("the morning catch-up must be one page, got %d deliveries: %q", got, notifier.allBodies())
	}
	body := notifier.allBodies()
	if !strings.Contains(body, "4 项") {
		t.Fatalf("the page must say how many items are waiting, got %q", body)
	}
	// It names the streams, because that is how the user knows where to look.
	if !strings.Contains(body, "语音服务") {
		t.Fatalf("the page must name the stream an item belongs to, got %q", body)
	}
	// And it is honest about what it left out rather than silently truncating.
	if !strings.Contains(body, "另有") {
		t.Fatalf("a page that shows only some items must say how many it withheld, got %q", body)
	}
}

// TestDigestGoesOutAtMostOncePerDay pins the exactly-once property. The sweep runs every
// minute, so without a claim the digest would re-send all day.
func TestDigestGoesOutAtMostOncePerDay(t *testing.T) {
	base := time.Date(2026, 11, 9, 8, 30, 0, 0, time.Local)
	service, notifier, scope := digestTestService(t, base)
	stream := mustStream(t, service, scope, "语音服务")
	mustOverdueTodo(t, service, scope, stream.ID, "补齐评测脚本", base)

	service.sweepAttentionOnce(context.Background())
	if got := notifier.count(); got != 1 {
		t.Fatalf("first sweep should send the page, got %d", got)
	}
	// Every later sweep on the same day is silent.
	for i := 0; i < 3; i++ {
		service.sweepAttentionOnce(context.Background())
	}
	if got := notifier.count(); got != 1 {
		t.Fatalf("the digest must go out once per day, got %d deliveries", got)
	}

	// A new day is a new page.
	next := base.Add(24 * time.Hour)
	service.now = fixedNow(next)
	service.sweepAttentionOnce(context.Background())
	// The item was already reported, so the next day's page is empty and the digest is not
	// sent at all: re-announcing a still-overdue item every morning is the noise the
	// feature exists to remove.
	if got := notifier.count(); got != 1 {
		t.Fatalf("an already-reported item must not be re-announced, got %d deliveries: %q", got, notifier.allBodies())
	}
}

// TestDigestOnlyRunsInTheMorningWindow keeps the daily grouping scoped to the overnight
// catch-up. A single item surfacing at 14:00 should say what it is, not arrive as a
// one-line "daily summary" that hides the detail until it is opened.
func TestDigestOnlyRunsInTheMorningWindow(t *testing.T) {
	base := time.Date(2026, 11, 9, 14, 0, 0, 0, time.Local)
	service, notifier, scope := newAttentionService(t, base)
	stream := mustStream(t, service, scope, "语音服务")
	mustOverdueTodo(t, service, scope, stream.ID, "补齐评测脚本", base)

	service.sweepAttentionOnce(context.Background())

	if got := notifier.count(); got != 1 {
		t.Fatalf("an afternoon reminder should go out on its own, got %d deliveries", got)
	}
	if body := notifier.allBodies(); strings.Contains(body, "今日汇总") {
		t.Fatalf("outside the catch-up window the reminder must not be batched, got %q", body)
	}
	if body := notifier.allBodies(); !strings.Contains(body, "补齐评测脚本") {
		t.Fatalf("the individual reminder must say what it is about, got %q", body)
	}
}

// failingOnceNotifier fails its first delivery and then succeeds, modelling a transient
// push failure on the digest itself.
type failingOnceNotifier struct {
	inner   recordingNotifier
	failed  bool
	attempt int
}

func (n *failingOnceNotifier) NotifyOwner(ctx context.Context, owner, title, body string) (int, error) {
	n.attempt++
	if !n.failed {
		n.failed = true
		return 0, errors.New("push transport unavailable")
	}
	return n.inner.NotifyOwner(ctx, owner, title, body)
}

// TestFailedDigestDoesNotSwallowTheReminders is the safety property that decides the
// ordering in deliverPlanNudges: the day is claimed and the send happens BEFORE the items
// are recorded as reported. If those reminders were recorded first, a failed send would mark
// as "already told" items the user never saw — a reminder silently lost forever. Losing a
// day's digest is recoverable; swallowing a reminder is not.
func TestFailedDigestDoesNotSwallowTheReminders(t *testing.T) {
	base := time.Date(2026, 11, 9, 8, 30, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	notifier := &failingOnceNotifier{}
	service.SetNudgeNotifier(notifier)
	stream := mustStream(t, service, scope, "语音服务")
	mustOverdueTodo(t, service, scope, stream.ID, "补齐评测脚本", base)

	service.sweepAttentionOnce(context.Background())

	// The digest attempt failed, but the reminder was not lost: the sweep falls through to
	// the individual push for the same item, which succeeds on the second attempt.
	if notifier.inner.count() == 0 {
		t.Fatal("a failed digest must not swallow the reminder; it must still be delivered")
	}
	if body := notifier.inner.allBodies(); !strings.Contains(body, "补齐评测脚本") {
		t.Fatalf("the fallback delivery must carry the item, got %q", body)
	}
}

// TestDigestRespectsARecordedVerdict keeps the digest consistent with the inbox verbs: a row
// the owner already dismissed (or snoozed) is not a reminder, and batching must not
// resurrect it.
func TestDigestRespectsARecordedVerdict(t *testing.T) {
	base := time.Date(2026, 11, 9, 8, 30, 0, 0, time.Local)
	service, notifier, scope := digestTestService(t, base)
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatalf("WorkContexts: %v", err)
	}
	stream := mustStream(t, service, scope, "语音服务")
	mustOverdueTodo(t, service, scope, stream.ID, "已处置的过期项", base)

	// Dismiss the row at the group scope, the way the inbox's verb does.
	if _, err := store.UpsertAttentionAction(context.Background(), scope, stream.ID, workcontext.AttentionActionInput{
		ItemKey: workcontext.AttentionItemKey(AttentionKindTodoOverdue, stream.ID),
		Verb:    workcontext.AttentionVerbDismiss,
		Reason:  "已手工处理",
	}, base); err != nil {
		t.Fatalf("UpsertAttentionAction: %v", err)
	}

	service.sweepAttentionOnce(context.Background())

	if got := notifier.count(); got != 0 {
		t.Fatalf("a dismissed row must not be reported again, got %d deliveries: %q", got, notifier.allBodies())
	}
}
