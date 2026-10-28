package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	rtchannels "github.com/tim5wang/godex/internal/runtime/channels"
)

func registerChannelStatusRoute(mux *http.ServeMux, channels statusProvider, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /channels", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if channels == nil {
			writeJSON(w, http.StatusOK, rtchannels.StatusReport{GeneratedAt: time.Now(), Channels: nil})
			return
		}
		writeJSON(w, http.StatusOK, channels.StatusReport())
	})))
}

func registerWeixinRoutes(mux *http.ServeMux, weixinAuth weixinAuthProvider, protected func(http.Handler) http.Handler) {
	// Binding the bot decides which WeChat identity drives an agent on this
	// instance, so it is platform administration, not a per-user setting. Without
	// this gate any signed-in tenant user could scan the QR and hand the bot to
	// themselves — the same class of hole as editing channels.weixin.allow_from.
	//
	// Node actors are admitted because /channels/* is node-scoped: with a remote
	// node active the request is proxied there and arrives as the relay-trust node
	// identity, so a platform-only gate would make remote bot setup impossible.
	operatorOnly := requirePlatformAdminOrNodeActor(protected)
	mux.Handle("GET /channels/weixin/auth", operatorOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if weixinAuth == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("weixin web auth unavailable"))
			return
		}
		status, err := weixinAuth.Status(r.Context(), strings.TrimSpace(r.URL.Query().Get("account_id")))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})))
	mux.Handle("POST /channels/weixin/auth/start", operatorOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if weixinAuth == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("weixin web auth unavailable"))
			return
		}
		var req accountRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		status, err := weixinAuth.Start(r.Context(), req.AccountID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})))
	mux.Handle("POST /channels/weixin/auth/logout", operatorOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if weixinAuth == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("weixin web auth unavailable"))
			return
		}
		var req accountRequest
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		status, err := weixinAuth.Logout(r.Context(), req.AccountID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})))
}
