package workcontext

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Timestamps in this package are TEXT and the queries compare them with plain `>`/`>=`, which
// SQLite evaluates bytewise. That makes the stored width part of the contract: lexical order
// only equals chronological order while the fraction keeps a constant width. These tests pin
// both halves — the writer's width and the comparison's outcome — because the failure they
// guard against is intermittent and surfaced far away, as a stream still reporting unread
// items right after being marked seen.

// TestStoredStampsHaveFixedWidth is the invariant itself. RFC3339Nano trims trailing zeros, so
// "...:03.9Z" and "...:03.95Z" would compare with the SHORTER string winning on its literal 'Z'
// (0x5A sorts above every digit) — i.e. a chronologically earlier event looking later than the
// watermark that already covered it.
func TestStoredStampsHaveFixedWidth(t *testing.T) {
	// Two instants in the same second where the nanosecond fraction trims differently: the
	// first is earlier but, under RFC3339Nano, would render SHORTER and sort LARGER.
	early := time.Date(2026, 11, 11, 15, 26, 3, 900000000, time.UTC)
	late := time.Date(2026, 11, 11, 15, 26, 3, 950000000, time.UTC)

	earlyStamp := formatStamp(early)
	lateStamp := formatStamp(late)

	if len(earlyStamp) != len(lateStamp) {
		t.Fatalf("stamps must have equal width to compare bytewise: %q vs %q", earlyStamp, lateStamp)
	}
	if earlyStamp >= lateStamp {
		t.Fatalf("an earlier instant must sort before a later one: %q vs %q", earlyStamp, lateStamp)
	}
	// And the writer used by every stored timestamp must agree with the renderer.
	if got := nowRFC3339(); len(got) != len(earlyStamp) {
		t.Fatalf("nowRFC3339 must use the canonical layout: got %q, want width %d", got, len(earlyStamp))
	}
	// The canonical form must still be parseable by the tolerant readers in this package.
	if _, err := time.Parse(time.RFC3339Nano, earlyStamp); err != nil {
		t.Fatalf("canonical stamp must remain RFC3339Nano-parseable: %v", err)
	}
	if !strings.HasSuffix(earlyStamp, "Z") {
		t.Fatalf("stamps are stored in UTC with a Z suffix, got %q", earlyStamp)
	}
}

// TestUnreadIgnoresEventCoveredByWatermark is the behaviour that broke: an event written BEFORE
// the watermark must not be reported as unread. With RFC3339Nano widths this counted 1.
func TestUnreadIgnoresEventCoveredByWatermark(t *testing.T) {
	store := newTestStore(t)
	scope := testScope()
	ctx := context.Background()
	stream := mustCreate(t, store, scope, "水位线顺序")

	// Create() records its own "context_created" event stamped with the real clock, which would
	// sit after any controlled stamp below. Clear events so the pair written here is the ONLY
	// input to the comparison — otherwise the test measures the creation event, not the ordering.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM wc_events WHERE tenant_id=? AND context_id=?`, scope.TenantID, stream.ID); err != nil {
		t.Fatalf("clear events: %v", err)
	}

	// An event, then the watermark a moment later — the ordinary "read it, then mark seen"
	// sequence, and the exact shape that used to invert.
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	early := time.Date(2026, 11, 11, 15, 26, 3, 900000000, time.UTC)
	late := time.Date(2026, 11, 11, 15, 26, 3, 950000000, time.UTC)
	// dedup_key carries a UNIQUE(tenant_id, context_id, dedup_key) constraint, so the two rows
	// need distinct values; it plays no part in the ordering under test.
	exec(`INSERT INTO wc_events(tenant_id, id, context_id, kind, title, source, dedup_key, created_at)
		VALUES(?, 'ev-1', ?, 'todo_created', 't', 'desktop', 'ev-1', ?)`, scope.TenantID, stream.ID, formatStamp(early))
	exec(`INSERT INTO wc_seen(tenant_id, context_id, owner_id, last_seen_at) VALUES(?, ?, ?, ?)`,
		scope.TenantID, stream.ID, scope.OwnerID, formatStamp(late))

	counts, err := store.UnreadCounts(ctx, scope)
	if err != nil {
		t.Fatalf("UnreadCounts: %v", err)
	}
	if counts[stream.ID] != 0 {
		t.Fatalf("an event older than the watermark must not count as unread, got %d", counts[stream.ID])
	}

	// The complementary case must still work: an event AFTER the watermark IS unread. Without
	// this, "always report zero" would pass the check above.
	exec(`INSERT INTO wc_events(tenant_id, id, context_id, kind, title, source, dedup_key, created_at)
		VALUES(?, 'ev-2', ?, 'todo_created', 't', 'desktop', 'ev-2', ?)`,
		scope.TenantID, stream.ID, formatStamp(late.Add(time.Millisecond)))
	counts, err = store.UnreadCounts(ctx, scope)
	if err != nil {
		t.Fatalf("UnreadCounts after new event: %v", err)
	}
	if counts[stream.ID] != 1 {
		t.Fatalf("an event newer than the watermark must count as unread, got %d", counts[stream.ID])
	}
}

// TestUnreadCountsEveryEventWithNoWatermark keeps the first-look semantics: with nothing marked
// seen, every recorded event is unread. This is what makes the fixed-width change safe rather
// than a change in meaning.
func TestUnreadCountsEveryEventWithNoWatermark(t *testing.T) {
	store := newTestStore(t)
	scope := testScope()
	ctx := context.Background()
	stream := mustCreate(t, store, scope, "首次查看")

	// Same reason as above: count only the event this test writes, not the creation event.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM wc_events WHERE tenant_id=? AND context_id=?`, scope.TenantID, stream.ID); err != nil {
		t.Fatalf("clear events: %v", err)
	}
	if err := store.RecordEvent(ctx, scope, stream.ID, "todo_created", "t", "desktop", ""); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	counts, err := store.UnreadCounts(ctx, scope)
	if err != nil {
		t.Fatalf("UnreadCounts: %v", err)
	}
	if counts[stream.ID] != 1 {
		t.Fatalf("with no watermark every event is unread, got %d", counts[stream.ID])
	}
}
