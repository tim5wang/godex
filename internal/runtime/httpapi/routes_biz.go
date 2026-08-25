package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/services/usage"
)

// bizKeyContextKey is the request-context key carrying the authenticated
// business key for a biz-protected route.
type bizKeyContextKey struct{}

// withBizKeyAuth wraps a handler with business-key authentication. The
// presented `Authorization: Bearer biz_xxx` is verified against the usage
// service; on success the resolved BizAPIKey is stored in the request context
// (via BizKeyFromContext) for downstream handlers.
func withBizKeyAuth(usageService *usage.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret := extractProxyKeySecret(r)
		if !strings.HasPrefix(secret, usage.BizKeyPrefix) {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing or invalid biz key"))
			return
		}
		key, err := usageService.AuthenticateBizKey(secret)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		ctx := context.WithValue(r.Context(), bizKeyContextKey{}, key)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// BizKeyFromContext returns the authenticated business key stored by
// withBizKeyAuth, or nil if the route was not biz-authenticated.
func BizKeyFromContext(ctx context.Context) *usage.BizAPIKey {
	key, _ := ctx.Value(bizKeyContextKey{}).(*usage.BizAPIKey)
	return key
}

// registerBizRoutes registers the Agent Step Platform business-key admin API.
// These endpoints are admin-only (web-token protected), mirroring the usage
// key management surface in routes_usage.go.
func registerBizRoutes(mux *http.ServeMux, protected func(http.Handler) http.Handler, usageService *usage.Service) {
	if usageService == nil {
		return
	}
	mux.Handle("POST /v1/biz/keys", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req usage.BizKeyCreateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := usageService.CreateBizKey(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, resp)
	})))
	mux.Handle("GET /v1/biz/keys", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys, err := usageService.ListBizKeys()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, keys)
	})))
	mux.Handle("GET /v1/biz/keys/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, err := usageService.GetBizKey(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, key)
	})))
	mux.Handle("PATCH /v1/biz/keys/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req usage.BizKeyUpdateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		key, err := usageService.UpdateBizKey(r.PathValue("id"), req)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, key)
	})))
	mux.Handle("DELETE /v1/biz/keys/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := usageService.DeleteBizKey(r.PathValue("id")); err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("POST /v1/biz/keys/{id}/reset", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reset rotates the secret and returns the new plaintext exactly once
		// (mirrors /usage/keys/{id}/reset).
		resp, err := usageService.ResetBizKey(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})))
	mux.Handle("POST /v1/biz/keys/{id}/reveal", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reveal returns the plaintext secret after pin verification (bounded
		// wrong-pin attempts lock the reveal until a reset).
		var req usage.BizKeyRevealRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := usageService.RevealBizKey(r.PathValue("id"), req)
		if err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})))
}
