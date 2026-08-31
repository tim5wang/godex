package httpapi

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

func registerTurnRoutes(
	mux *http.ServeMux,
	manager *config.Manager,
	service *backend.Service,
	protected func(http.Handler) http.Handler,
) {
	mux.Handle("POST /sessions/{id}/messages", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req submitMessageRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		envelope := req.Envelope
		if strings.TrimSpace(envelope.Text) == "" && strings.TrimSpace(req.Text) != "" {
			envelope = message.NewRuntimeEnvelope(message.SourceGateway, r.PathValue("id"), req.Sender, req.Text, time.Now(), nil)
		}
		if envelope.Source == "" {
			envelope.Source = message.SourceGateway
		}
		if strings.TrimSpace(envelope.BodyText()) == "" && len(envelope.Attachments) == 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("message text or attachments required"))
			return
		}
		var (
			result *backend.SubmitResult
			err    error
		)
		if queueMode := strings.TrimSpace(req.QueueMode); queueMode != "" {
			result, err = service.SubmitAsync(r.Context(), r.PathValue("id"), envelope, backend.SubmitOptions{QueueMode: backend.QueueMode(queueMode)})
		} else {
			result, err = service.SubmitAsync(r.Context(), r.PathValue("id"), envelope)
		}
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	})))
	mux.Handle("GET /sessions/{id}/turns/{turnID}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record, err := service.GetTurn(r.Context(), r.PathValue("id"), r.PathValue("turnID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, record)
	})))
	mux.Handle("POST /sessions/{id}/turns/{turnID}/cancel", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.CancelTurn(r.Context(), r.PathValue("id"), r.PathValue("turnID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/queued/{queueID}/cancel", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.CancelQueuedTurn(r.Context(), r.PathValue("id"), r.PathValue("queueID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("POST /sessions/{id}/queued/{queueID}/steer", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.SteerQueuedTurn(r.Context(), r.PathValue("id"), r.PathValue("queueID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	})))
	mux.Handle("POST /sessions/{id}/turns/{turnID}/retry", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.RetryTurnAsync(r.Context(), r.PathValue("id"), r.PathValue("turnID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	})))
	mux.Handle("POST /sessions/{id}/turns/{turnID}/resume", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := service.ResumeTurnAsync(r.Context(), r.PathValue("id"), r.PathValue("turnID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	})))
	mux.Handle("POST /sessions/{id}/attachments", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, backend.MaxAttachmentUploadBytes()+(1<<20))
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		if r.MultipartForm == nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("no multipart files uploaded"))
			return
		}

		var uploaded []message.AttachmentRef
		for _, files := range r.MultipartForm.File {
			for _, header := range files {
				file, err := header.Open()
				if err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
				attachment, err := service.StoreAttachment(r.Context(), r.PathValue("id"), backend.AttachmentUpload{
					Name:     header.Filename,
					MIMEType: header.Header.Get("Content-Type"),
					Reader:   file,
				})
				closeErr := file.Close()
				if err != nil {
					writeError(w, statusForSessionError(err), err)
					return
				}
				if closeErr != nil {
					writeError(w, http.StatusInternalServerError, closeErr)
					return
				}
				uploaded = append(uploaded, attachment)
			}
		}
		if len(uploaded) == 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("no files uploaded"))
			return
		}
		writeJSON(w, http.StatusOK, attachmentListResponse{Attachments: uploaded})
	})))
	mux.Handle("GET /sessions/{id}/attachments/{attachmentID}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attachment, absolutePath, err := service.ResolveAttachment(r.PathValue("id"), r.PathValue("attachmentID"))
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		if attachment.MIMEType != "" {
			w.Header().Set("Content-Type", attachment.MIMEType)
		}
		if attachment.Name != "" {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": attachment.Name}))
		}
		http.ServeFile(w, r, absolutePath)
	})))
	mux.Handle("POST /sessions/{id}/commands", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req commandRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cmd, err := normalizeCommandRequest(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := service.ExecuteCommand(r.Context(), r.PathValue("id"), cmd)
		if err != nil && !errors.Is(err, commands.ErrUnknownCommand) {
			writeError(w, statusForSessionError(err), err)
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"result": result,
				"error":  err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
	mux.Handle("GET /sessions/{id}/events", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSessionEventStream(w, r, service, r.PathValue("id"))
	})))
}
