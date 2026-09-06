package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tim5wang/godex/internal/services/backend"
)

// registerDesktopRoutes mounts the desktop-shell bridge surface.
//
//	GET /desktop/events → SSE stream of task-completion events.
//
// The Tauri desktop shell subscribes to this stream to raise system
// notifications when an agent task finishes. This is a thin poll-based
// bridge: it watches the backend session list for running→idle transitions
// and pushes one SSE event per completed task, so it needs no changes to
// the agent event pipeline itself. It is web-token protected like the rest
// of the API surface.
func registerDesktopRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /desktop/events", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, http.ErrNotSupported)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ctx := r.Context()
		prevRunning := map[string]bool{}
		// Poll every 10s instead of 2s: ListSessions walks the whole sessions
		// directory on every tick, which adds measurable CPU/IO once the session
		// count grows. 10s keeps task-completion notifications near-real-time
		// without turning the event bridge into a scanner.
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		emit := func(event map[string]any) {
			data, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sessions, err := service.ListSessions(ctx, backend.SessionListFilter{})
				if err != nil {
					continue
				}
				current := make(map[string]bool, len(sessions))
				titles := make(map[string]string, len(sessions))
				for _, s := range sessions {
					current[s.SessionID] = s.Running
					titles[s.SessionID] = s.Title
				}
				// Emit one event per session that transitioned running→idle.
				for id, running := range current {
					if was, ok := prevRunning[id]; ok && was && !running {
						emit(map[string]any{
							"type":       "task_completed",
							"session_id": id,
							"title":      titles[id],
						})
					}
				}
				prevRunning = current
			}
		}
	})))
}
