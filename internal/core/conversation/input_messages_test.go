package conversation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/media"
	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestBuildAPIMessagesUpgradesImageAttachmentToVisionInput(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(imagePath, []byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43}, 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	messages := []protocol.Message{{
		Role: protocol.RoleUser,
		Content: []protocol.Block{
			protocol.TextBlock("这张图是做什么用的？"),
		},
		Metadata: &protocol.Metadata{
			Text: "这张图是做什么用的？",
			Parts: []protocol.ContentPart{
				{Type: "text", Text: "这张图是做什么用的？"},
				{
					Type:     "attachment",
					MIMEType: "image/jpeg",
					Attachment: &protocol.Attachment{
						ID:       "att-1",
						Name:     "photo.jpg",
						MIMEType: "image/jpeg",
						Path:     imagePath,
					},
				},
			},
		},
	}}

	processor := media.NewProcessor(config.MediaConfig{
		OCR: config.OCRMediaConfig{Mode: "disabled"},
	}, dir, filepath.Join(dir, ".sessions"), filepath.Join(dir, "tmp"))
	apiMessages, err := BuildAPIMessages(context.Background(), messages, BuildInputOptions{
		SessionID:     "session-1",
		SupportsImage: true,
		Processor:     processor,
	})
	if err != nil {
		t.Fatalf("build api messages: %v", err)
	}
	if len(apiMessages) != 1 {
		t.Fatalf("expected one api message, got %d", len(apiMessages))
	}
	if len(apiMessages[0].Content) != 3 {
		t.Fatalf("expected prompt text + image summary + image input, got %#v", apiMessages[0].Content)
	}
	if apiMessages[0].Content[2].Type != protocol.BlockImage || apiMessages[0].Content[2].Source == nil {
		t.Fatalf("expected image block with source, got %#v", apiMessages[0].Content[2])
	}
	if apiMessages[0].Content[2].Source.MediaType != "image/jpeg" || apiMessages[0].Content[2].Source.Data == "" {
		t.Fatalf("unexpected image source: %#v", apiMessages[0].Content[2].Source)
	}
}

func TestBuildAPIMessagesExtractsTextAttachmentAndPersistsOverflow(t *testing.T) {
	dir := t.TempDir()
	attachmentPath := filepath.Join(dir, "notes.md")
	original := strings.Repeat("line of attachment text\n", 2000)
	if err := os.WriteFile(attachmentPath, []byte(original), 0644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	messages := []protocol.Message{{
		Role:    protocol.RoleUser,
		Content: []protocol.Block{protocol.TextBlock("请总结附件")},
		Metadata: &protocol.Metadata{
			Text: "请总结附件",
			Attachments: []protocol.Attachment{{
				ID:       "att-2",
				Name:     "notes.md",
				MIMEType: "text/markdown",
				Path:     attachmentPath,
			}},
		},
	}}

	processor := media.NewProcessor(config.MediaConfig{
		Document: config.DocumentMediaConfig{MaxChars: 1000},
	}, dir, filepath.Join(dir, ".sessions"), filepath.Join(dir, "tmp"))
	apiMessages, err := BuildAPIMessages(context.Background(), messages, BuildInputOptions{
		SessionID:     "session-2",
		SupportsImage: true,
		Processor:     processor,
	})
	if err != nil {
		t.Fatalf("build api messages: %v", err)
	}
	if len(apiMessages) != 1 || len(apiMessages[0].Content) != 2 {
		t.Fatalf("unexpected api messages: %#v", apiMessages)
	}
	text := apiMessages[0].Content[1].Text
	if !strings.Contains(text, `Extracted text from attachment "notes.md"`) {
		t.Fatalf("expected extracted attachment text, got %q", text)
	}
	if !strings.Contains(text, "[read-only attachment path: "+filepath.ToSlash(attachmentPath)+"]") {
		t.Fatalf("expected model-visible attachment path, got %q", text)
	}
	if !strings.Contains(text, "[Truncated. Full extracted text saved to") {
		t.Fatalf("expected persisted overflow notice, got %q", text)
	}
}

func TestBuildAPIMessagesSummarizesUnsupportedAttachmentTypes(t *testing.T) {
	messages := []protocol.Message{{
		Role:    protocol.RoleUser,
		Content: []protocol.Block{protocol.TextBlock("帮我看看这个二进制")},
		Metadata: &protocol.Metadata{
			Text: "帮我看看这个二进制",
			Attachments: []protocol.Attachment{{
				ID:       "att-3",
				Name:     "report.bin",
				MIMEType: "application/octet-stream",
				Path:     ".godex/report.bin",
			}},
		},
	}}

	processor := media.NewProcessor(config.MediaConfig{}, ".", filepath.Join(".", ".godex", ".sessions"), filepath.Join(".", ".godex", ".tmp"))
	apiMessages, err := BuildAPIMessages(context.Background(), messages, BuildInputOptions{
		SessionID:     "session-3",
		SupportsImage: true,
		Processor:     processor,
	})
	if err != nil {
		t.Fatalf("build api messages: %v", err)
	}
	if len(apiMessages) != 1 || len(apiMessages[0].Content) != 2 {
		t.Fatalf("unexpected api messages: %#v", apiMessages)
	}
	if !strings.Contains(apiMessages[0].Content[1].Text, "Parsing for this attached file type is not enabled") {
		t.Fatalf("expected unsupported attachment summary, got %q", apiMessages[0].Content[1].Text)
	}
}
