package message

import (
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
)

func TestNewCLIEnvelopeToProtocolMessage(t *testing.T) {
	env := NewCLIEnvelope("repl", "lead", "hello", time.Unix(123, 0))
	if env.Text != "hello" {
		t.Fatalf("expected text field to be populated, got %+v", env)
	}
	if len(env.Parts) != 1 || env.Parts[0].Text != "hello" {
		t.Fatalf("expected text content part, got %+v", env.Parts)
	}
	msg := env.ToProtocolMessage(protocol.RoleUser, "", false)
	if msg.Role != protocol.RoleUser || protocol.MessageText(msg) != "hello" {
		t.Fatalf("unexpected protocol message %+v", msg)
	}
	if msg.Metadata == nil || msg.Metadata.Text != "hello" || msg.Metadata.Source != string(SourceCLI) {
		t.Fatalf("expected protocol metadata to preserve envelope context, got %+v", msg.Metadata)
	}
	if msg.Metadata.Timestamp != "1970-01-01T00:02:03Z" {
		t.Fatalf("expected protocol metadata timestamp, got %+v", msg.Metadata)
	}
}

func TestEnvelopeToProtocolMessageIncludesAttachmentSummaryAndMetadata(t *testing.T) {
	env := Envelope{
		Source:    SourceWeb,
		SessionID: "web-1",
		Sender:    "lead",
		Text:      "Please review this file.",
		Attachments: []AttachmentRef{
			{
				ID:        "att-1",
				Name:      "notes.txt",
				MIMEType:  "text/plain",
				Path:      ".godex/.sessions/web-1/attachments/att-1-notes.txt",
				SizeBytes: 12,
			},
		},
		Timestamp: time.Unix(456, 0),
	}

	msg := env.ToProtocolMessage(protocol.RoleUser, "", false)
	text := protocol.MessageText(msg)
	for _, want := range []string{
		"Please review this file.",
		"Attached files:",
		"notes.txt",
		"path=.godex/.sessions/web-1/attachments/att-1-notes.txt",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected prompt text to contain %q, got %q", want, text)
		}
	}
	if msg.Metadata == nil || len(msg.Metadata.Attachments) != 1 || msg.Metadata.Attachments[0].ID != "att-1" {
		t.Fatalf("expected attachment metadata, got %+v", msg.Metadata)
	}
}

func TestFormatEnvelopes(t *testing.T) {
	text := FormatEnvelopes("Runtime updates", []Envelope{
		NewRuntimeEnvelope(SourceBackground, "repl", "background", "task finished", time.Date(2026, time.April, 18, 10, 0, 0, 0, time.UTC), map[string]string{"task_id": "1"}),
	})
	for _, want := range []string{
		"Runtime updates:",
		"[background] background",
		"task finished",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected formatted envelopes to contain %q, got %q", want, text)
		}
	}
}
