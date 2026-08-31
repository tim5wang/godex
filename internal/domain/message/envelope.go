package message

import (
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// EnvelopeSource identifies where a runtime message originated.
type EnvelopeSource string

const (
	SourceCLI        EnvelopeSource = "cli"
	SourceTUI        EnvelopeSource = "tui"
	SourceInbox      EnvelopeSource = "inbox"
	SourceBackground EnvelopeSource = "background"
	SourceACP        EnvelopeSource = "acp"
	SourceWeb        EnvelopeSource = "web"
	SourceGateway    EnvelopeSource = "gateway"
	SourceFeishu     EnvelopeSource = "feishu"
	SourceWeixin     EnvelopeSource = "weixin"
	SourceCron       EnvelopeSource = "cron"
	SourceHeartbeat  EnvelopeSource = "heartbeat"
	SourceCommand    EnvelopeSource = "command"
	SourceStep       EnvelopeSource = "step"
	SourceVoice      EnvelopeSource = "voice"
)

// ContentPartType describes one inbound content part.
type ContentPartType string

const (
	ContentPartText       ContentPartType = "text"
	ContentPartAttachment ContentPartType = "attachment"
)

// ContentPart is one normalized user-facing content fragment.
type ContentPart struct {
	Type       ContentPartType `json:"type"`
	Text       string          `json:"text,omitempty"`
	MIMEType   string          `json:"mime_type,omitempty"`
	Attachment *AttachmentRef  `json:"attachment,omitempty"`
}

// AttachmentRef points to one uploaded or referenced attachment.
type AttachmentRef struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	Path      string `json:"path,omitempty"`
	URL       string `json:"url,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// Envelope is the common runtime message wrapper shared by CLI, inbox, and
// future web/gateway/cron entrypoints.
type Envelope struct {
	Source      EnvelopeSource    `json:"source"`
	SessionID   string            `json:"session_id,omitempty"`
	Sender      string            `json:"sender,omitempty"`
	Text        string            `json:"text,omitempty"`
	Content     string            `json:"content,omitempty"`
	Parts       []ContentPart     `json:"parts,omitempty"`
	Attachments []AttachmentRef   `json:"attachments,omitempty"`
	Timestamp   time.Time         `json:"timestamp"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// NewCLIEnvelope wraps a terminal user message in the common envelope format.
func NewCLIEnvelope(sessionID, sender, content string, now time.Time) Envelope {
	return NewTextEnvelope(SourceCLI, sessionID, sender, content, now)
}

// NewTextEnvelope wraps a plain-text message from any frontend source.
func NewTextEnvelope(source EnvelopeSource, sessionID, sender, content string, now time.Time) Envelope {
	return Envelope{
		Source:    source,
		SessionID: sessionID,
		Sender:    sender,
		Text:      content,
		Content:   content,
		Parts:     []ContentPart{{Type: ContentPartText, Text: content}},
		Timestamp: now,
	}
}

// EnvelopeFromMessage converts an inter-agent bus message into a generic envelope.
func EnvelopeFromMessage(source EnvelopeSource, sessionID string, msg Message) Envelope {
	metadata := map[string]string{
		"message_id": msg.ID,
		"msg_type":   string(msg.Type),
	}
	if msg.To != "" {
		metadata["recipient"] = msg.To
	}
	return Envelope{
		Source:    source,
		SessionID: sessionID,
		Sender:    msg.From,
		Text:      msg.Content,
		Content:   msg.Content,
		Parts:     []ContentPart{{Type: ContentPartText, Text: msg.Content}},
		Timestamp: msg.Timestamp,
		Metadata:  metadata,
	}
}

// FormatInboxMessages renders inter-agent inbox messages in the shared runtime format.
func FormatInboxMessages(messages []Message) string {
	envelopes := make([]Envelope, 0, len(messages))
	for _, msg := range messages {
		envelopes = append(envelopes, EnvelopeFromMessage(SourceInbox, "", msg))
	}
	return FormatEnvelopes("Inbox updates", envelopes)
}

// NewRuntimeEnvelope creates a generic runtime envelope for non-user updates
// such as background notifications.
func NewRuntimeEnvelope(source EnvelopeSource, sessionID, sender, content string, now time.Time, metadata map[string]string) Envelope {
	envelope := NewTextEnvelope(source, sessionID, sender, content, now)
	envelope.Metadata = metadata
	return envelope
}

// ToProtocolMessage converts the envelope into a protocol message for model input.
func (e Envelope) ToProtocolMessage(role string, kind protocol.MessageKind, ephemeral bool) protocol.Message {
	normalized := e.Normalized()
	text := normalized.PromptText()
	metadataText := normalized.BodyText()
	if displayText := strings.TrimSpace(normalized.Metadata["display_text"]); displayText != "" {
		metadataText = displayText
	}
	msg := protocol.Message{
		Role:    role,
		Content: []protocol.Block{protocol.TextBlock(text)},
		Metadata: &protocol.Metadata{
			Kind:           kind,
			Ephemeral:      ephemeral,
			Source:         string(normalized.Source),
			Sender:         normalized.Sender,
			Timestamp:      protocolTimestamp(normalized.Timestamp),
			Text:           metadataText,
			Parts:          normalized.protocolParts(),
			Attachments:    normalized.protocolAttachments(),
			AppObjectType:  normalized.Metadata["app_object_type"],
			AppObjectID:    normalized.Metadata["app_object_id"],
			AppObjectTitle: normalized.Metadata["app_object_title"],
		},
	}
	if msg.Metadata.Kind == "" &&
		!msg.Metadata.Ephemeral &&
		msg.Metadata.Source == "" &&
		msg.Metadata.Sender == "" &&
		msg.Metadata.Timestamp == "" &&
		msg.Metadata.Text == "" &&
		len(msg.Metadata.Parts) == 0 &&
		len(msg.Metadata.Attachments) == 0 &&
		msg.Metadata.AppObjectType == "" &&
		msg.Metadata.AppObjectID == "" &&
		msg.Metadata.AppObjectTitle == "" {
		msg.Metadata = nil
	}
	return msg
}

func protocolTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// Normalized returns a cloned envelope with text, parts, and attachments aligned.
func (e Envelope) Normalized() Envelope {
	normalized := Envelope{
		Source:    e.Source,
		SessionID: e.SessionID,
		Sender:    e.Sender,
		Text:      e.Text,
		Content:   e.Content,
		Timestamp: e.Timestamp,
		Metadata:  cloneEnvelopeMetadata(e.Metadata),
	}
	if strings.TrimSpace(normalized.Text) == "" && strings.TrimSpace(normalized.Content) != "" {
		normalized.Text = normalized.Content
	}
	if strings.TrimSpace(normalized.Content) == "" && strings.TrimSpace(normalized.Text) != "" {
		normalized.Content = normalized.Text
	}

	if len(e.Attachments) > 0 {
		normalized.Attachments = append([]AttachmentRef{}, e.Attachments...)
	}
	if len(e.Parts) > 0 {
		normalized.Parts = make([]ContentPart, 0, len(e.Parts))
		for _, part := range e.Parts {
			next := ContentPart{
				Type:     part.Type,
				Text:     part.Text,
				MIMEType: part.MIMEType,
			}
			if part.Attachment != nil {
				attachment := *part.Attachment
				next.Attachment = &attachment
			}
			normalized.Parts = append(normalized.Parts, next)
		}
	}
	if len(normalized.Attachments) == 0 && len(normalized.Parts) > 0 {
		for _, part := range normalized.Parts {
			if part.Attachment == nil {
				continue
			}
			normalized.Attachments = append(normalized.Attachments, *part.Attachment)
		}
	}

	if strings.TrimSpace(normalized.Text) != "" && !hasTextPart(normalized.Parts) {
		normalized.Parts = append([]ContentPart{{Type: ContentPartText, Text: normalized.Text}}, normalized.Parts...)
	}
	for _, attachment := range normalized.Attachments {
		if !hasAttachmentPart(normalized.Parts, attachment) {
			clone := attachment
			normalized.Parts = append(normalized.Parts, ContentPart{
				Type:       ContentPartAttachment,
				MIMEType:   attachment.MIMEType,
				Attachment: &clone,
			})
		}
	}
	return normalized
}

// BodyText returns the user-authored text portion of the envelope.
func (e Envelope) BodyText() string {
	if strings.TrimSpace(e.Text) != "" {
		return e.Text
	}
	if strings.TrimSpace(e.Content) != "" {
		return e.Content
	}
	var builder strings.Builder
	for _, part := range e.Parts {
		if part.Type == ContentPartText && part.Text != "" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

// DisplayText returns the best-effort human-readable envelope body.
func (e Envelope) DisplayText() string {
	return e.PromptText()
}

// PromptText renders the envelope into the text currently visible to the model.
func (e Envelope) PromptText() string {
	text := strings.TrimSpace(e.BodyText())
	attachmentSummary := e.AttachmentSummary()
	switch {
	case text != "" && attachmentSummary != "":
		return text + "\n\n" + attachmentSummary
	case attachmentSummary != "":
		return attachmentSummary
	default:
		return text
	}
}

// AttachmentSummary renders uploaded files in a stable prompt-friendly format.
func (e Envelope) AttachmentSummary() string {
	if len(e.Attachments) == 0 {
		return ""
	}
	lines := []string{"Attached files:"}
	for _, attachment := range e.Attachments {
		label := firstNonEmpty(attachment.Name, attachment.Path, attachment.URL, attachment.ID, "attachment")
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
			lines = append(lines, "- "+label)
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s (%s)", label, strings.Join(details, ", ")))
	}
	return strings.Join(lines, "\n")
}

// FormatEnvelopes renders runtime envelopes in a stable, human-readable format.
func FormatEnvelopes(header string, envelopes []Envelope) string {
	if len(envelopes) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString(header)
	builder.WriteString(":\n")
	for _, envelope := range envelopes {
		sender := envelope.Sender
		if sender == "" {
			sender = string(envelope.Source)
		}
		builder.WriteString(fmt.Sprintf("- [%s] %s at %s: %s\n",
			envelope.Source,
			sender,
			envelope.Timestamp.Format("2006-01-02 15:04:05"),
			envelope.DisplayText(),
		))
	}
	return strings.TrimRight(builder.String(), "\n")
}

func (e Envelope) protocolParts() []protocol.ContentPart {
	if len(e.Parts) == 0 {
		return nil
	}
	parts := make([]protocol.ContentPart, 0, len(e.Parts))
	for _, part := range e.Parts {
		next := protocol.ContentPart{
			Type:     string(part.Type),
			Text:     part.Text,
			MIMEType: part.MIMEType,
		}
		if part.Attachment != nil {
			attachment := part.Attachment.toProtocolAttachment()
			next.Attachment = &attachment
		}
		parts = append(parts, next)
	}
	return parts
}

// ProtocolAttachments returns cloned protocol attachment metadata for the envelope.
func (e Envelope) ProtocolAttachments() []protocol.Attachment {
	return e.protocolAttachments()
}

func (e Envelope) protocolAttachments() []protocol.Attachment {
	if len(e.Attachments) == 0 {
		return nil
	}
	attachments := make([]protocol.Attachment, 0, len(e.Attachments))
	for _, attachment := range e.Attachments {
		attachments = append(attachments, attachment.toProtocolAttachment())
	}
	return attachments
}

func (a AttachmentRef) toProtocolAttachment() protocol.Attachment {
	return protocol.Attachment{
		ID:        a.ID,
		Name:      a.Name,
		MIMEType:  a.MIMEType,
		Path:      a.Path,
		URL:       a.URL,
		SizeBytes: a.SizeBytes,
	}
}

func hasTextPart(parts []ContentPart) bool {
	for _, part := range parts {
		if part.Type == ContentPartText && strings.TrimSpace(part.Text) != "" {
			return true
		}
	}
	return false
}

func hasAttachmentPart(parts []ContentPart, attachment AttachmentRef) bool {
	for _, part := range parts {
		if part.Type != ContentPartAttachment || part.Attachment == nil {
			continue
		}
		if sameAttachment(*part.Attachment, attachment) {
			return true
		}
	}
	return false
}

func sameAttachment(left, right AttachmentRef) bool {
	switch {
	case left.ID != "" && right.ID != "":
		return left.ID == right.ID
	case left.Path != "" && right.Path != "":
		return left.Path == right.Path
	case left.URL != "" && right.URL != "":
		return left.URL == right.URL
	default:
		return left.Name != "" && left.Name == right.Name
	}
}

func cloneEnvelopeMetadata(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
