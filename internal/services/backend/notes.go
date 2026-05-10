package backend

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/notes"
	"github.com/tim5wang/godex/internal/domain/message"
)

func (s *Service) notesManager() *notes.Manager {
	dir := filepath.Join(s.cfg.HomeDir, "notes")
	if s.cfg.HomeDir == "" {
		dir = filepath.Join(s.cfg.StateDir, "notes")
	}
	return notes.NewManager(dir)
}

func (s *Service) ListNotes(_ context.Context, opts notes.SearchOptions) ([]notes.Note, error) {
	return s.notesManager().List(opts)
}

func (s *Service) GetNote(_ context.Context, id string) (notes.Note, error) {
	return s.notesManager().Get(id)
}

func (s *Service) SaveNote(_ context.Context, input notes.SaveInput) (notes.Note, error) {
	return s.notesManager().Save(input)
}

func (s *Service) DeleteNote(_ context.Context, id string) (notes.Note, error) {
	return s.notesManager().Delete(id)
}

func (s *Service) envelopeWithNoteContext(envelope message.Envelope) (message.Envelope, map[string]string, error) {
	noteID := strings.TrimSpace(envelope.Metadata["note_id"])
	if noteID == "" && envelope.Metadata["app_object_type"] == "note" {
		noteID = strings.TrimSpace(envelope.Metadata["app_object_id"])
	}
	if noteID == "" {
		return envelope, cloneMapStringString(envelope.Metadata), nil
	}

	note, err := s.notesManager().Get(noteID)
	if err != nil {
		return message.Envelope{}, nil, err
	}
	displayText := envelope.BodyText()
	metadata := cloneMapStringString(envelope.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["note_id"] = note.ID
	metadata["app_object_type"] = "note"
	metadata["app_object_id"] = note.ID
	metadata["app_object_title"] = note.Title
	if strings.TrimSpace(displayText) != "" {
		metadata["display_text"] = displayText
	}

	var builder strings.Builder
	builder.WriteString("Current note context:\n")
	builder.WriteString("id: ")
	builder.WriteString(note.ID)
	builder.WriteString("\n")
	builder.WriteString("title: ")
	builder.WriteString(note.Title)
	builder.WriteString("\n")
	if len(note.Tags) > 0 {
		builder.WriteString("tags: ")
		builder.WriteString(strings.Join(note.Tags, ", "))
		builder.WriteString("\n")
	}
	if strings.TrimSpace(note.Summary) != "" {
		builder.WriteString("summary: ")
		builder.WriteString(note.Summary)
		builder.WriteString("\n")
	}
	builder.WriteString("\nNote content:\n")
	builder.WriteString(note.Content)
	builder.WriteString("\n\nUser request:\n")
	builder.WriteString(displayText)

	next := envelope.Normalized()
	next.Text = builder.String()
	next.Content = next.Text
	next.Parts = []message.ContentPart{{Type: message.ContentPartText, Text: next.Text}}
	next.Metadata = metadata
	return next, cloneMapStringString(metadata), nil
}
