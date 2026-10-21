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

	// Event kinds. Only the two derivable kinds are emitted here; `card_finished`
	// needs the taskboard hook tracked separately as B2, and `turn_finished` is
	// judged client-side from the running flag that mobile/ already polls.
	watchPushKindApproval = "approval_requested"
	watchPushKindFlowTask = "flow_waiting_human"
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

// observeLocked turns newly appeared approvals into queued events.
func (q *watchPushQueue) observeLocked(approvals []watchApprovalItem, now time.Time) {
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
	q.trimLocked()
}

// snapshot derives any new events, then returns everything after the cursor.
func (q *watchPushQueue) snapshot(approvals []watchApprovalItem, since int64, deviceID string) (int64, []watchPushEvent) {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := q.now()
	q.pruneLocked(now)
	q.observeLocked(approvals, now)

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
		cursor, events := queue.snapshot(approvals, since, deviceID)
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
