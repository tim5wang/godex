// godex-feature: watch-push
// 手表推送队列（W5）：无 FCM 时的唯一通路。节点只维护「待推送事件」的有界队列，
// 由手机桥接（mobile/，Capacitor 本地通知）与腕上客户端拉取。
// 入口：GET /watch/push/pending、POST /watch/push/ack
// 文档：docs/wearos-watch-implementation-plan.md §W5、§6
package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/platform/idgen"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/watchauth"
)

const (
	// maxWatchPushEvents bounds the queue. A phone that has been offline for a
	// day must not be handed a month of stale alerts, and the queue lives in
	// memory on a node that also serves the agent.
	maxWatchPushEvents = 200
	// watchPushTTL expires events. A notification about an approval that has
	// long since timed out is worse than no notification.
	watchPushTTL = 24 * time.Hour

	// Event kinds. Three are derived on poll rather than emitted from a hook: the
	// sources either sit inside another component's mutex (the permission gate) or
	// live in a component with no path to this queue (the taskboard plugin), so
	// derivation is both lower-risk and the only option that does not invent a new
	// cross-assembly seam. `turn_finished` is judged client-side from the running
	// flag mobile/ already polls.
	watchPushKindApproval     = "approval_requested"
	watchPushKindFlowTask     = "flow_waiting_human"
	watchPushKindCardFinished = "card_finished"
)

// watchPushEvent is one notification to deliver. It stays flat and small because
// it travels to a wrist and through a phone's notification shade.
type watchPushEvent struct {
	ID        string    `json:"id"`
	Seq       int64     `json:"seq"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	DeviceID  string    `json:"device_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type watchPushPendingResponse struct {
	Cursor int64            `json:"cursor"`
	Events []watchPushEvent `json:"events"`
}

// watchPushQueue is the in-memory, bounded pending-event queue.
//
// Events are DERIVED, not emitted: on each poll the current approval aggregation
// is compared against what this queue has already seen, and the difference
// becomes new events. §W6 chose derivation over hooks because the permission gate
// registers pending requests while holding its own mutex, so emitting from there
// would risk blocking and reentrancy — and because a pull queue needs no write
// path at all.
type watchPushQueue struct {
	mu sync.Mutex
	// seq numbers events monotonically so a cursor is a single integer.
	seq    int64
	events []watchPushEvent
	// seen remembers which derivable items have already produced an event. It
	// carries a timestamp because the queue must not grow forever: entries are
	// pruned with the same TTL as the events they produced.
	seen map[string]time.Time
	now  func() time.Time
}

func newWatchPushQueue() *watchPushQueue {
	return &watchPushQueue{seen: map[string]time.Time{}, now: time.Now}
}

// watchPushKey identifies one derivable item across polls. A resolved approval
// leaves the aggregation, so the same key reappearing means the same request
// (which must not notify twice); a genuinely new request carries a new id.
func watchPushKey(item watchApprovalItem) string {
	switch item.Kind {
	case "permission":
		if item.RequestID == "" {
			return ""
		}
		return "permission:" + item.SessionID + ":" + item.RequestID
	case "flow_task":
		if item.TaskID == "" {
			return ""
		}
		return "flow_task:" + item.TaskID
	default:
		return ""
	}
}

// watchPushEventFor renders one derivable item into a deliverable event. The
// titles match the wording the wrist already uses for the same situations, so a
// notification reads the same whether it arrived via the phone or the watch.
func watchPushEventFor(item watchApprovalItem, seq int64, now time.Time) watchPushEvent {
	event := watchPushEvent{
		ID:        idgen.New("pe_", 8),
		Seq:       seq,
		CreatedAt: now,
		SessionID: item.SessionID,
		RequestID: item.RequestID,
	}
	body := firstNonEmptyString(item.Summary, item.ToolName, item.Prompt)
	switch item.Kind {
	case "flow_task":
		event.Kind = watchPushKindFlowTask
		event.Title = "等待处理"
		event.Body = body
	default:
		event.Kind = watchPushKindApproval
		event.Title = "需要批准"
		event.Body = body
	}
	return event
}

// watchFinishedCard is one taskboard card that has left in_progress, as reported
// by the ledger.
//
// Unlike an approval, this is informational: there is nothing for the wrist to
// decide, it only needs to learn that the work stopped and is waiting for a human.
type watchFinishedCard struct {
	CardID string
	Title  string
	// Status is in_review or done — the two states a card can only reach after
	// leaving in_progress.
	Status string
	// Anchor identifies this particular finish; see finishAnchor.
	Anchor string
}

// collectFinishedCards reports taskboard cards that have left in_progress.
//
// "Currently in_review or done" is exactly "has left in_progress": in_review is
// only reachable from in_progress and done only from in_review (allowedTransitions
// and CompleteCard), so no previous-state tracking is needed to detect the
// transition. A failed or cancelled run does NOT qualify — FinishExecution closes
// the execution but leaves the card in_progress, so the work is not finished and
// there is nothing to tell a human about.
//
// One consequence of deriving rather than hooking: on a node restart every card
// still sitting in_review/done is "new" to the queue and produces one event. That
// is bounded by the same 200-event cap and 24h TTL as every other kind, and is the
// same behaviour the approval derivation already has.
func collectFinishedCards(service *backend.Service) []watchFinishedCard {
	if service == nil {
		return nil
	}
	ledger := service.TaskboardLedger()
	if ledger == nil {
		return nil
	}
	out := make([]watchFinishedCard, 0)
	for _, card := range ledger.ListCards(taskboard.CardFilter{}) {
		if card.Status != taskboard.StatusInReview && card.Status != taskboard.StatusDone {
			continue
		}
		out = append(out, watchFinishedCard{
			CardID: card.ID,
			Title:  card.Title,
			Status: card.Status,
			Anchor: finishAnchor(card),
		})
	}
	return out
}

// finishAnchor identifies one finish so that the queue notifies once per finish
// and stays quiet while a card sits still.
//
// The execution that leaving in_progress ended is the anchor when there is one: it
// does not change while a reviewer adds comments or ticks checklist items (which
// bump the card version), and a re-dispatched card gets a new execution id, so a
// second finish notifies again — the board's core loop.
//
// The status is part of the identity because in_review → done is a SECOND finish,
// not a continuation of the first: the execution id does not change there (the run
// was already closed when the card left in_progress), so omitting the status would
// silently swallow the acceptance and leave the "已完成" wording unreachable.
//
// A card finished by hand has no execution, and "manual" is used rather than the
// card version for the same stability reason: a version-based key would re-notify
// on every review edit, which is routine and would spam the wrist.
//
// Known limit of this key: a card that is moved back to in_progress by hand and
// finished again without a new execution (no re-dispatch) keeps its key and stays
// quiet. Suppressing that is preferred to the alternative above, since a
// re-dispatch — the normal path — does start a new execution.
func finishAnchor(card taskboard.Card) string {
	execID := ""
	if n := len(card.Executions); n > 0 {
		execID = strings.TrimSpace(card.Executions[n-1].ID)
	}
	if execID == "" {
		execID = "manual"
	}
	return execID + ":" + card.Status
}

// watchPushKeyForCard identifies one finish across polls.
func watchPushKeyForCard(card watchFinishedCard) string {
	return "card_finished:" + card.CardID + ":" + card.Anchor
}

// watchPushEventForCard renders one finished card into a deliverable event. The
// title distinguishes "awaiting acceptance" from "accepted", because those ask
// different things of the reader.
func watchPushEventForCard(card watchFinishedCard, seq int64, now time.Time) watchPushEvent {
	title := "任务待验收"
	if card.Status == taskboard.StatusDone {
		title = "任务已完成"
	}
	return watchPushEvent{
		ID:        idgen.New("pe_", 8),
		Seq:       seq,
		Kind:      watchPushKindCardFinished,
		Title:     title,
		Body:      truncateRunes(card.Title, 140),
		CreatedAt: now,
		// RequestID is deliberately left empty: clients use it to route a tap to
		// the approval screen, and there is no approval behind this event.
	}
}

func (q *watchPushQueue) pruneLocked(now time.Time) {
	cutoff := now.Add(-watchPushTTL)
	kept := q.events[:0]
	for _, e := range q.events {
		if e.CreatedAt.After(cutoff) {
			kept = append(kept, e)
		}
	}
	q.events = kept
	for key, at := range q.seen {
		if !at.After(cutoff) {
			delete(q.seen, key)
		}
	}
}

func (q *watchPushQueue) trimLocked() {
	if len(q.events) <= maxWatchPushEvents {
		return
	}
	q.events = append([]watchPushEvent(nil), q.events[len(q.events)-maxWatchPushEvents:]...)
}

// observeLocked turns newly appeared approvals and finished cards into queued
// events.
func (q *watchPushQueue) observeLocked(approvals []watchApprovalItem, finished []watchFinishedCard, now time.Time) {
	for _, item := range approvals {
		key := watchPushKey(item)
		if key == "" {
			continue
		}
		if _, ok := q.seen[key]; ok {
			continue
		}
		q.seen[key] = now
		q.seq++
		q.events = append(q.events, watchPushEventFor(item, q.seq, now))
	}
	for _, card := range finished {
		if strings.TrimSpace(card.CardID) == "" {
			continue
		}
		key := watchPushKeyForCard(card)
		if _, ok := q.seen[key]; ok {
			continue
		}
		q.seen[key] = now
		q.seq++
		q.events = append(q.events, watchPushEventForCard(card, q.seq, now))
	}
	q.trimLocked()
}

// snapshot derives any new events, then returns everything after the cursor.
func (q *watchPushQueue) snapshot(approvals []watchApprovalItem, finished []watchFinishedCard, since int64, deviceID string) (int64, []watchPushEvent) {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := q.now()
	q.pruneLocked(now)
	q.observeLocked(approvals, finished, now)

	out := make([]watchPushEvent, 0, len(q.events))
	for _, e := range q.events {
		if e.Seq <= since {
			continue
		}
		// An empty DeviceID means "any paired watch": approval events are derived
		// from node-wide state and are not addressed to one wrist.
		if deviceID != "" && e.DeviceID != "" && e.DeviceID != deviceID {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return q.seq, out
}

// ack drops delivered events by id. Optional for cursor-based clients, but it
// lets a client that does not persist a cursor avoid re-delivery.
func (q *watchPushQueue) ack(ids []string) int {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	if len(wanted) == 0 {
		return 0
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.events[:0]
	removed := 0
	for _, e := range q.events {
		if wanted[e.ID] {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	q.events = kept
	return removed
}

// withWatchOrWebRead admits EITHER a watch credential holding watch:read OR the
// node's web token.
//
// Both are needed: the wrist presents its device credential, while the phone
// bridge (mobile/) authenticates with the web token it already uses for
// /api/sessions and has no wrist credential at all. A watch-only gate would leave
// the phone unable to drain the queue, which is the whole point of W5.
func withWatchOrWebRead(auth *watchauth.Service, protected func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if device, ok := authenticateWatchRequest(auth, r); ok {
				if !device.HasScope(watchauth.ScopeRead) {
					writeError(w, http.StatusForbidden,
						fmt.Errorf("watch device lacks required scope %q", watchauth.ScopeRead))
					return
				}
				auth.Touch(device.DeviceID)
				ctx := context.WithValue(r.Context(), watchDeviceContextKey{}, device)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			// Not a watch credential (or an unknown/revoked one): apply the normal
			// web-token gate, which answers 401 for a bad token.
			protected(next).ServeHTTP(w, r)
		})
	}
}

func registerWatchPushRoutes(
	mux *http.ServeMux,
	api watchAPI,
	queue *watchPushQueue,
	protected func(http.Handler) http.Handler,
) {
	if queue == nil {
		return
	}
	access := withWatchOrWebRead(api.auth, protected)

	mux.Handle("GET /watch/push/pending", access(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		since := int64(parseIntQuery(r.URL.Query().Get("since"), 0))
		deviceID := strings.TrimSpace(r.URL.Query().Get("device_id"))
		// Derivation reads the same aggregation the wrist's approval queue shows,
		// so a notification can never advertise something the queue does not hold.
		approvals := collectWatchApprovals(r.Context(), api.service)
		finished := collectFinishedCards(api.service)
		cursor, events := queue.snapshot(approvals, finished, since, deviceID)
		writeJSON(w, http.StatusOK, watchPushPendingResponse{Cursor: cursor, Events: events})
	})))

	mux.Handle("POST /watch/push/ack", access(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []string `json:"ids"`
		}
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"removed": queue.ack(req.IDs)})
	})))
}
