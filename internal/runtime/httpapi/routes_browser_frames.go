package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/tools"
)

// browserFrameProvider is the subset of tools.BrowserService the frame-stream
// endpoint depends on. Declared as an interface so tests can inject a fake.
type browserFrameProvider interface {
	ListPages(sessionID string) []tools.BrowserPage
	ValidateFramePage(sessionID, pageID string) error
	SubscribeFrames(sessionID, pageID string) (<-chan tools.BrowserFrame, func())
}

// registerBrowserFrameRoutes mounts the frame-stream WebSocket endpoint:
//
//	GET /browser/frames?session=<sessionID>&page=<pageID>
//
// Note the path has NO /api prefix: webui.NewHandler strips the /api prefix
// before delegating to the API handler, so every route here must be
// registered prefix-free (like /sessions, /preview/static, ...).
//
// Auth reuses the web token (Bearer header or ?token= query, like the voice
// endpoint — browsers cannot set an Authorization header on WebSocket
// upgrades). Session ownership is enforced with a strong check: the requested
// page must belong to the requested session (a page lives in exactly one
// session's registry, so cross-session page IDs are rejected). When page is
// omitted, the session's most recently used page is streamed.
func registerBrowserFrameRoutes(mux *http.ServeMux, service *backend.Service, protected func(http.Handler) http.Handler, tokenProvider func() string) {
	registerBrowserFrameRoutesWithProvider(mux, func() browserFrameProvider {
		if service == nil {
			return nil
		}
		return service.BrowserService()
	}, protected, tokenProvider)
}

// registerBrowserFrameRoutesWithProvider is the injectable core of the
// frame-stream endpoint (tests pass a fake provider).
func registerBrowserFrameRoutesWithProvider(mux *http.ServeMux, provider func() browserFrameProvider, protected func(http.Handler) http.Handler, tokenProvider func() string) {
	if provider == nil {
		return
	}
	auth := voiceQueryTokenAuth(protected, tokenProvider)
	mux.Handle("GET /browser/frames", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		browser := provider()
		if browser == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("browser service unavailable"))
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
		pageID := strings.TrimSpace(r.URL.Query().Get("page"))
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("missing session query param"))
			return
		}
		if pageID == "" {
			// Default to the session's most recently used page (frontend can
			// subscribe without knowing the page ID up front).
			pages := browser.ListPages(sessionID)
			if len(pages) == 0 {
				writeError(w, http.StatusNotFound, fmt.Errorf("no browser pages for session"))
				return
			}
			pageID = pages[0].PageID
		}
		// Strong session-ownership check: the page must exist in this session.
		if err := browser.ValidateFramePage(sessionID, pageID); err != nil {
			writeError(w, http.StatusForbidden, fmt.Errorf("frame stream not allowed for this session/page: %w", err))
			return
		}

		upgrader := websocket.Upgrader{
			HandshakeTimeout: 5 * time.Second,
			CheckOrigin:      func(*http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		frames, cancel := browser.SubscribeFrames(sessionID, pageID)
		defer cancel()
		for {
			select {
			case <-r.Context().Done():
				return
			case frame, ok := <-frames:
				if !ok {
					// Stream ended (idle stop, page closed, or invalid).
					return
				}
				if err := conn.WriteJSON(frame); err != nil {
					return
				}
			}
		}
	})))
}
