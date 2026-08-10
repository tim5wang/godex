package backend

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/notes"
	"github.com/tim5wang/godex/internal/domain/message"
)

const maxRelatedMemoriesForNote = 4

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

// GetRelatedMemories queries durable memory that is relevant to this note.
// Uses the note's tags as search keywords, falling back to title/summary if
// no tags are set.  Returns up to maxRelatedMemoriesForNote results.
func (s *Service) GetRelatedMemories(_ context.Context, noteID string) ([]memory.StoredMemory, error) {
	note, err := s.notesManager().Get(noteID)
	if err != nil {
		return nil, err
	}

	// Build query from tags first, then title/summary as fallback.
	query := ""
	if len(note.Tags) > 0 {
		query = strings.Join(note.Tags, " ")
	} else if strings.TrimSpace(note.Title) != "" {
		query = note.Title
		if strings.TrimSpace(note.Summary) != "" {
			query += " " + note.Summary
		}
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	results, err := s.memoryManager().Search(memory.SearchOptions{
		Query: query,
		Limit: maxRelatedMemoriesForNote,
	})
	if err != nil {
		return nil, err
	}
	return results, nil
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

	// Append related memory summaries as a footnote.
	related, err := s.GetRelatedMemories(context.Background(), note.ID)
	if err == nil && len(related) > 0 {
		builder.WriteString("\n\n---\nRelated memory entries:\n")
		for _, m := range related {
			builder.WriteString(fmt.Sprintf("- [%s/%s] %s", m.Type, m.Title, truncateInline(m.Summary, 100)))
			builder.WriteString("\n")
		}
		// Tell the agent it can search for more detail.
		builder.WriteString("\n(Use `tdai_memory_search` or `memory search` in the agent tools to explore related memories in detail.)\n")
	}

	builder.WriteString("\n\nUser request:\n")
	builder.WriteString(displayText)

	next := envelope.Normalized()
	next.Text = builder.String()
	next.Content = next.Text
	next.Parts = []message.ContentPart{{Type: message.ContentPartText, Text: next.Text}}
	next.Metadata = metadata
	return next, cloneMapStringString(metadata), nil
}

// truncateInline shortens a string to max runes, appending "…" if truncated.
func truncateInline(s string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "…"
}
