package conversation

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/media"
	"github.com/tim5wang/godex/internal/contracts/protocol"
)

type AttachmentProcessor interface {
	BuildBlocks(ctx context.Context, buildCtx media.BuildContext, attachment protocol.Attachment) ([]protocol.Block, error)
}

type BuildInputOptions struct {
	SessionID     string
	SupportsImage bool
	Processor     AttachmentProcessor
}

// BuildAPIMessages converts stored protocol messages into request-side API
// messages, upgrading attachments into rich model inputs through the shared
// attachment processor.
func BuildAPIMessages(ctx context.Context, messages []protocol.Message, opts BuildInputOptions) ([]protocol.APIMessage, error) {
	apiMessages := make([]protocol.APIMessage, 0, len(messages))
	for _, msg := range messages {
		apiMessage, ok, err := buildAPIMessage(ctx, msg, opts)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		apiMessages = append(apiMessages, apiMessage)
	}
	return apiMessages, nil
}

func buildAPIMessage(ctx context.Context, msg protocol.Message, opts BuildInputOptions) (protocol.APIMessage, bool, error) {
	if !shouldUpgradeUserMessage(msg) {
		plain := protocol.ToAPIMessages([]protocol.Message{msg})
		if len(plain) == 0 {
			return protocol.APIMessage{}, false, nil
		}
		return plain[0], true, nil
	}

	content, err := buildUserInputBlocks(ctx, msg.Metadata, opts)
	if err != nil {
		return protocol.APIMessage{}, false, err
	}
	if len(content) == 0 {
		return protocol.APIMessage{}, false, nil
	}
	return protocol.APIMessage{Role: msg.Role, Content: content}, true, nil
}

func shouldUpgradeUserMessage(msg protocol.Message) bool {
	if msg.Role != protocol.RoleUser || msg.Metadata == nil || msg.Metadata.Ephemeral {
		return false
	}
	return len(msg.Metadata.Parts) > 0 || len(msg.Metadata.Attachments) > 0 || strings.TrimSpace(msg.Metadata.Text) != ""
}

func buildUserInputBlocks(ctx context.Context, metadata *protocol.Metadata, opts BuildInputOptions) ([]protocol.Block, error) {
	if metadata == nil {
		return nil, nil
	}

	blocks := make([]protocol.Block, 0, len(metadata.Parts)+2)
	attachmentsSeen := make(map[string]struct{})
	buildCtx := media.BuildContext{
		SessionID:     opts.SessionID,
		SupportsImage: opts.SupportsImage,
	}

	if len(metadata.Parts) > 0 {
		for _, part := range metadata.Parts {
			switch strings.TrimSpace(part.Type) {
			case "text":
				if text := strings.TrimSpace(part.Text); text != "" {
					blocks = append(blocks, protocol.TextBlock(text))
				}
			case "attachment":
				if part.Attachment == nil {
					continue
				}
				next, err := attachmentBlocks(ctx, *part.Attachment, opts.Processor, buildCtx)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, next...)
				attachmentsSeen[attachmentKey(*part.Attachment)] = struct{}{}
			}
		}
	}

	if len(blocks) == 0 {
		if text := strings.TrimSpace(metadata.Text); text != "" {
			blocks = append(blocks, protocol.TextBlock(text))
		}
	}

	for _, attachment := range metadata.Attachments {
		key := attachmentKey(attachment)
		if _, ok := attachmentsSeen[key]; ok {
			continue
		}
		next, err := attachmentBlocks(ctx, attachment, opts.Processor, buildCtx)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, next...)
	}

	return blocks, nil
}

func attachmentBlocks(ctx context.Context, attachment protocol.Attachment, processor AttachmentProcessor, buildCtx media.BuildContext) ([]protocol.Block, error) {
	if processor == nil {
		return []protocol.Block{protocol.TextBlock(fallbackAttachmentSummary(attachment))}, nil
	}
	return processor.BuildBlocks(ctx, buildCtx, attachment)
}

func fallbackAttachmentSummary(attachment protocol.Attachment) string {
	label := strings.TrimSpace(attachment.Name)
	if label == "" {
		label = filepath.Base(strings.TrimSpace(attachment.Path))
	}
	if label == "" || label == "." {
		label = "attachment"
	}
	details := make([]string, 0, 3)
	if attachment.MIMEType != "" {
		details = append(details, attachment.MIMEType)
	}
	if attachment.Path != "" {
		details = append(details, "path="+attachment.Path)
	}
	if attachment.SizeBytes > 0 {
		details = append(details, fmt.Sprintf("size=%d", attachment.SizeBytes))
	}
	if len(details) == 0 {
		return "Attached file metadata only: " + label
	}
	return fmt.Sprintf("Attached file metadata only: %s (%s)", label, strings.Join(details, ", "))
}

func attachmentKey(attachment protocol.Attachment) string {
	switch {
	case strings.TrimSpace(attachment.ID) != "":
		return "id:" + strings.TrimSpace(attachment.ID)
	case strings.TrimSpace(attachment.Path) != "":
		return "path:" + strings.TrimSpace(attachment.Path)
	case strings.TrimSpace(attachment.URL) != "":
		return "url:" + strings.TrimSpace(attachment.URL)
	default:
		return "name:" + strings.TrimSpace(attachment.Name)
	}
}
