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
	mux.Handle("GET /channels/weixin/auth", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("POST /channels/weixin/auth/start", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("POST /channels/weixin/auth/logout", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
