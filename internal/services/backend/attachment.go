package backend

import (
	"context"
	"fmt"
	"github.com/tim5wang/godex/internal/domain/message"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

func (s *Service) materializeArtifactPaths(sessionID string, paths []string) ([]message.AttachmentRef, []string) {
	if len(paths) == 0 {
		return nil, nil
	}
	attachments := make([]message.AttachmentRef, 0, len(paths))
	warnings := make([]string, 0)
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}
		absolutePath, err := filepath.Abs(path)
		if err == nil {
			path = absolutePath
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		attachment, err := s.storeArtifactPath(sessionID, path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to attach generated file %s: %v", path, err))
			continue
		}
		attachments = append(attachments, attachment)
	}
	return attachments, warnings
}

func (s *Service) storeArtifactPath(sessionID, path string) (message.AttachmentRef, error) {
	file, err := os.Open(path)
	if err != nil {
		return message.AttachmentRef{}, err
	}
	defer file.Close()

	name := filepath.Base(path)
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	return s.StoreAttachment(context.Background(), sessionID, AttachmentUpload{
		Name:     name,
		MIMEType: mimeType,
		Reader:   file,
	})
}

// StoreAttachment persists one uploaded file inside the session attachment directory.
func (s *Service) StoreAttachment(ctx context.Context, sessionID string, upload AttachmentUpload) (message.AttachmentRef, error) {
	_ = ctx
	if _, err := s.requireSession(sessionID); err != nil {
		return message.AttachmentRef{}, err
	}
	if upload.Reader == nil {
		return message.AttachmentRef{}, fmt.Errorf("missing attachment reader")
	}

	name := strings.TrimSpace(upload.Name)
	if name == "" {
		name = "attachment"
	}
	attachmentID, err := newAttachmentID()
	if err != nil {
		return message.AttachmentRef{}, err
	}

	dir := s.sessionAttachmentsDir(sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return message.AttachmentRef{}, err
	}

	storedName := fmt.Sprintf("%s-%s", attachmentID, sanitizeAttachmentName(name))
	absolutePath := filepath.Join(dir, storedName)
	file, err := os.Create(absolutePath)
	if err != nil {
		return message.AttachmentRef{}, err
	}
	limit := MaxAttachmentUploadBytes()
	limitedReader := &io.LimitedReader{R: upload.Reader, N: limit + 1}
	size, copyErr := io.Copy(file, limitedReader)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(absolutePath)
		return message.AttachmentRef{}, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(absolutePath)
		return message.AttachmentRef{}, closeErr
	}
	if size > limit {
		_ = os.Remove(absolutePath)
		return message.AttachmentRef{}, fmt.Errorf("attachment %q exceeds max size of %d bytes", name, limit)
	}

	mimeType := strings.TrimSpace(upload.MIMEType)
	if mimeType == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	}

	return message.AttachmentRef{
		ID:        attachmentID,
		Name:      name,
		MIMEType:  mimeType,
		Path:      s.relativePath(absolutePath),
		URL:       fmt.Sprintf("/sessions/%s/attachments/%s", sessionID, attachmentID),
		SizeBytes: size,
	}, nil
}

// ResolveAttachment finds one persisted attachment by session and attachment ID.
func (s *Service) ResolveAttachment(sessionID, attachmentID string) (message.AttachmentRef, string, error) {
	attachmentID = strings.TrimSpace(attachmentID)
	if attachmentID == "" {
		return message.AttachmentRef{}, "", fmt.Errorf("missing attachment id")
	}

	dir := s.sessionAttachmentsDir(sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return message.AttachmentRef{}, "", newAttachmentNotFoundError(attachmentID)
		}
		return message.AttachmentRef{}, "", err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name != attachmentID && !strings.HasPrefix(name, attachmentID+"-") {
			continue
		}
		absolutePath := filepath.Join(dir, name)
		info, err := entry.Info()
		if err != nil {
			return message.AttachmentRef{}, "", err
		}
		displayName := strings.TrimPrefix(name, attachmentID+"-")
		if displayName == "" {
			displayName = name
		}
		mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(displayName)))
		return message.AttachmentRef{
			ID:        attachmentID,
			Name:      displayName,
			MIMEType:  mimeType,
			Path:      s.relativePath(absolutePath),
			URL:       fmt.Sprintf("/sessions/%s/attachments/%s", sessionID, attachmentID),
			SizeBytes: info.Size(),
		}, absolutePath, nil
	}
	return message.AttachmentRef{}, "", newAttachmentNotFoundError(attachmentID)
}

// Snapshot returns the unified current session view.
func (s *Service) Snapshot(ctx context.Context, sessionID string) (Snapshot, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	return s.snapshotFromSession(session), nil
}

// Models returns the available model profiles and optional session override.
