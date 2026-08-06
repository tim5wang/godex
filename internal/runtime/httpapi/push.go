package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/services/webpush"
)

// PushHandler serves the center-side Web Push API:
//
//	GET  /push/public-key   → VAPID application server key (base64url)
//	POST /push/subscribe    → register a PushSubscription {endpoint, keys}
//	POST /push/unsubscribe  → remove a subscription by endpoint
//	POST /push/test         → send a test notification to all subscribers
//
// Subscriptions live only in memory (the center persists no push state),
// matching the node-mesh "center only relays live events" decision.
type PushHandler struct {
	service   *webpush.Service
	authorize func(*http.Request) bool
}

// NewPushHandler creates the push endpoint. authorize may be nil to accept
// every request (web token check).
func NewPushHandler(service *webpush.Service, authorize func(*http.Request) bool) *PushHandler {
	return &PushHandler{service: service, authorize: authorize}
}

func (h *PushHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.authorize != nil && !h.authorize(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/push")
	switch {
	case r.Method == http.MethodGet && (path == "/public-key" || path == ""):
		writeJSON(w, http.StatusOK, map[string]string{"public_key": h.service.PublicKey()})
	case r.Method == http.MethodPost && path == "/subscribe":
		h.subscribe(w, r)
	case r.Method == http.MethodPost && path == "/unsubscribe":
		h.unsubscribe(w, r)
	case r.Method == http.MethodPost && path == "/test":
		h.test(w, r)
	default:
		writeError(w, http.StatusNotFound, http.ErrNotSupported)
	}
}

func (h *PushHandler) subscribe(w http.ResponseWriter, r *http.Request) {
	var sub webpush.Subscription
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.service.Subscribe(sub); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *PushHandler) unsubscribe(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	h.service.Unsubscribe(payload.Endpoint)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *PushHandler) test(w http.ResponseWriter, r *http.Request) {
	notified, err := h.service.Notify(r.Context(), "GoDex", "Test notification from GoDex center")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"notified": notified})
}
