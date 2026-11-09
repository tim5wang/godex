package workcontext

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// DigestDay renders a time as the local calendar day key used by the digest ledger.
//
// Local, not UTC: the daily digest is a human-facing boundary ("yesterday evening and
// overnight"), so it must follow the same local-midnight boundary as quiet hours and the
// daily nudge cap. Using UTC here would make the digest fire at a different hour than the
// quiet window it exists to cover.
func DigestDay(now time.Time) string {
	return now.Format("2006-01-02")
}

// ClaimDigestDelivery atomically claims today's digest for one owner, reporting whether
// THIS call won the claim.
//
// It is the exactly-once guarantee for the daily digest: the primary key is
// (tenant, owner, day), so the first sweep that inserts wins and every later sweep on the
// same day inserts nothing and therefore stays silent. Without a claim, a one-minute sweep
// would re-send the digest all day.
//
// A separate table rather than a per-stream event because UnreadCounts is kind-agnostic
// (see unread_kinds_test.go): a digest event would raise the stream's unread badge and
// appear in "what changed", so the notification about noise would itself become noise.
// Like wc_seen and wc_attention_actions, this records how the OWNER relates to the system
// rather than what a stream contains, so it never bumps a context version.
func (s *Store) ClaimDigestDelivery(ctx context.Context, scope Scope, day string) (bool, error) {
	tenantID, ownerID := scopeArgs(scope)
	day = strings.TrimSpace(day)
	if day == "" {
		return false, ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO wc_digest_deliveries(tenant_id, owner_id, day, item_count, delivered_at)
		VALUES(?, ?, ?, 0, ?)
		ON CONFLICT(tenant_id, owner_id, day) DO NOTHING`,
		tenantID, ownerID, day, nowRFC3339())
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// RecordDigestItemCount stores how many items the day's digest actually reported, so the
// delivered digest can be read back and compared against what the reminder stream did.
func (s *Store) RecordDigestItemCount(ctx context.Context, scope Scope, day string, count int) error {
	tenantID, ownerID := scopeArgs(scope)
	day = strings.TrimSpace(day)
	if day == "" {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE wc_digest_deliveries SET item_count=?
		WHERE tenant_id=? AND owner_id=? AND day=?`,
		count, tenantID, ownerID, day)
	return err
}

// DigestDeliveredOn reports whether the owner's digest for a day has already gone out.
func (s *Store) DigestDeliveredOn(ctx context.Context, scope Scope, day string) (bool, error) {
	tenantID, ownerID := scopeArgs(scope)
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wc_digest_deliveries
		WHERE tenant_id=? AND owner_id=? AND day=?`,
		tenantID, ownerID, strings.TrimSpace(day)).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// HasEventDedup reports whether an event with this dedup key already exists for this OWNER.
//
// Read-only on purpose. The digest needs to know which reminders are already recorded
// BEFORE it composes its page (an item reported yesterday must not be re-announced today
// just because it is still overdue), but it must not record them first: recording is what
// marks a reminder as "already told", so writing before a successful send would swallow the
// reminder permanently if the send then failed. Checking first keeps the write in its safe
// place, after delivery.
//
// Joins contexts to filter on owner_id, exactly like CountEventsByKindSince. Owner isolation
// is the kind of thing this store enforces in the query rather than at the call site, so a
// future caller cannot accidentally probe another owner's events with a known context id.
func (s *Store) HasEventDedup(ctx context.Context, scope Scope, contextID, dedup string) (bool, error) {
	tenantID, ownerID := scopeArgs(scope)
	var found int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM wc_events e
		JOIN wc_contexts c ON c.tenant_id = e.tenant_id AND c.id = e.context_id
		WHERE e.tenant_id=? AND c.owner_id=? AND e.context_id=? AND e.dedup_key=? LIMIT 1`,
		tenantID, ownerID, strings.TrimSpace(contextID), strings.TrimSpace(dedup)).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
