package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/services/codexnative"
)

func codexNativeError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var submission *codexnative.SubmissionError
	var rpc *codexnative.RPCError
	body := map[string]any{"error": err.Error(), "outcomeUnknown": errors.As(err, &submission)}
	switch {
	case errors.Is(err, codexnative.ErrActiveWriter):
		status = http.StatusConflict
		body["code"] = "codex_active_writer"
		body["retryable"] = false
	case errors.Is(err, codexnative.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, codexnative.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, codexnative.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, codexnative.ErrUnavailable):
		status = http.StatusServiceUnavailable
	case errors.As(err, &rpc):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, body)
}

func decodeCodexBody(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, fmt.Errorf("expected one JSON object"))
		return false
	}
	return true
}

// The whole surface is node-owner scoped, just like files/terminal. In
// particular, an ordinary center login must not enumerate the host's history.
// No endpoint accepts arbitrary RPC methods, binaries, sockets or config.
func registerCodexNativeRoutes(mux *http.ServeMux, service *codexnative.Service, protected func(http.Handler) http.Handler) {
	register := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id := r.PathValue("id"); len(id) > 128 || strings.ContainsAny(id, "/\\\x00\r\n") {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid Codex thread id"))
				return
			}
			handler(w, r)
		})))
	}
	register("GET /external/codex/status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, service.Status(ctx))
	})
	register("GET /external/codex/threads", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		var result codexnative.ThreadPage
		var err error
		switch r.URL.Query().Get("view") {
		case "", "all":
			result, err = service.List(ctx, r.URL.Query().Get("cursor"), r.URL.Query().Get("q"))
		case "loaded":
			result, err = service.ListLoaded(ctx, r.URL.Query().Get("cursor"), r.URL.Query().Get("q"))
		default:
			err = fmt.Errorf("%w: view must be all or loaded", codexnative.ErrInvalid)
		}
		if err != nil {
			codexNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	register("GET /external/codex/threads/{id}/access", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		result, err := service.Access(ctx, r.PathValue("id"))
		if err != nil {
			codexNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	register("GET /external/codex/threads/{id}/models", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		result, err := service.Models(ctx, r.PathValue("id"))
		if err != nil {
			codexNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	})
	register("PATCH /external/codex/threads/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		var settings codexnative.ModelSettings
		if !decodeCodexBody(w, r, &settings) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		result, err := service.UpdateModelSettings(ctx, r.PathValue("id"), settings)
		principal, _ := PrincipalFromContext(r.Context())
		logger.Infof("codex native settings: actor=%q thread=%q model=%q effort=%q accepted=%t", principal.UserID, r.PathValue("id"), settings.Model, settings.Effort, err == nil)
		if err != nil {
			codexNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	register("GET /external/codex/threads/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		result, err := service.History(ctx, r.PathValue("id"), r.URL.Query().Get("cursor"))
		if err != nil {
			codexNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	register("GET /external/codex/threads/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		snapshot, events, leave, err := service.Watch(ctx, r.PathValue("id"))
		cancel()
		if err != nil {
			codexNativeError(w, err)
			return
		}
		defer leave()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("X-Accel-Buffering", "no")
		controller := http.NewResponseController(w)
		emit := func(value any) bool {
			_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
			data, err := json.Marshal(value)
			if err != nil {
				return false
			}
			if _, err = fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return false
			}
			return controller.Flush() == nil
		}
		if !emit(map[string]any{"method": "codex/snapshot", "params": snapshot}) {
			return
		}
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				if event.Sequence != 0 && event.Sequence <= snapshot.Sequence {
					continue
				}
				if !emit(event) {
					return
				}
			case <-ticker.C:
				_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
				if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
					return
				}
				if controller.Flush() != nil {
					return
				}
			}
		}
	})
	register("GET /external/codex/threads/{id}/queue", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		result, err := service.Queue(ctx, r.PathValue("id"))
		if err != nil {
			codexNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	register("POST /external/codex/threads/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		var action codexnative.Action
		if !decodeCodexBody(w, r, &action) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		result, err := service.Submit(ctx, r.PathValue("id"), action)
		principal, _ := PrincipalFromContext(r.Context())
		logger.Infof("codex native submit: actor=%q thread=%q mode=%q request=%q accepted=%t", principal.UserID, r.PathValue("id"), action.Mode, action.RequestID, err == nil)
		if err != nil {
			codexNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	register("POST /external/codex/threads/{id}/queue/start", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		result, err := service.StartQueue(ctx, r.PathValue("id"))
		principal, _ := PrincipalFromContext(r.Context())
		logger.Infof("codex native queue start: actor=%q thread=%q accepted=%t", principal.UserID, r.PathValue("id"), err == nil)
		if err != nil {
			codexNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	register("POST /external/codex/threads/{id}/interrupt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TurnID string `json:"turnId"`
		}
		if !decodeCodexBody(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		err := service.Interrupt(ctx, r.PathValue("id"), body.TurnID)
		principal, _ := PrincipalFromContext(r.Context())
		logger.Infof("codex native interrupt: actor=%q thread=%q turn=%q accepted=%t", principal.UserID, r.PathValue("id"), body.TurnID, err == nil)
		if err != nil {
			codexNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	register("POST /external/codex/threads/{id}/approvals", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RequestID string `json:"requestId"`
			Decision  string `json:"decision"`
		}
		if !decodeCodexBody(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		err := service.Approve(ctx, r.PathValue("id"), body.RequestID, body.Decision)
		principal, _ := PrincipalFromContext(r.Context())
		logger.Infof("codex native approval: actor=%q thread=%q decision=%q accepted=%t", principal.UserID, r.PathValue("id"), body.Decision, err == nil)
		if err != nil {
			codexNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}
