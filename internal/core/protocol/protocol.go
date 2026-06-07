package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
)

type BlockType string

type MessageKind string

const (
	BlockText       BlockType = "text"
	BlockImage      BlockType = "image"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

const (
	KindSummary    MessageKind = "summary"
	KindInbox      MessageKind = "inbox"
	KindBackground MessageKind = "background"
	KindMemory     MessageKind = "memory"
)

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

type Metadata struct {
	Kind             MessageKind   `json:"kind,omitempty"`
	Ephemeral        bool          `json:"ephemeral,omitempty"`
	Transcript       string        `json:"transcript,omitempty"`
	Source           string        `json:"source,omitempty"`
	Sender           string        `json:"sender,omitempty"`
	Timestamp        string        `json:"timestamp,omitempty"`
	Text             string        `json:"text,omitempty"`
	Parts            []ContentPart `json:"parts,omitempty"`
	Attachments      []Attachment  `json:"attachments,omitempty"`
	AppObjectType    string        `json:"app_object_type,omitempty"`
	AppObjectID      string        `json:"app_object_id,omitempty"`
	AppObjectTitle   string        `json:"app_object_title,omitempty"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
}

type ContentPart struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	MIMEType   string      `json:"mime_type,omitempty"`
	Attachment *Attachment `json:"attachment,omitempty"`
}

type Attachment struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	Path      string `json:"path,omitempty"`
	URL       string `json:"url,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

type Block struct {
	Type      BlockType              `json:"type"`
	Text      string                 `json:"text,omitempty"`
	Source    *ImageSource           `json:"source,omitempty"`
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	ToolUseID string                 `json:"tool_use_id,omitempty"`
	Content   string                 `json:"content,omitempty"`
	// Index is the wire-level ordering hint for protocols that
	// distinguish multiple tool calls in a single assistant turn
	// (e.g. OpenAI's chat.completion.chunk tool_calls[].index, which
	// the OpenAI SDK uses to dedupe chunks across a stream). It is
	// not used by Anthropic (which uses content_block index instead)
	// and is left at zero for upstream responses that don't surface
	// it. The field is intentionally not part of the canonical
	// protocol — it is a passthrough so the gateway can forward the
	// upstream's index to the wire without losing it.
	Index int `json:"-"`
}

type Message struct {
	Role     string    `json:"role"`
	Content  []Block   `json:"content"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

type APIMessage struct {
	Role             string  `json:"role"`
	Content          []Block `json:"content"`
	ReasoningContent string  `json:"reasoning_content,omitempty"`
}

type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// CacheRetentionShort is the default cache retention duration (~5 minutes).
const CacheRetentionShort = "short"

// CacheRetentionLong requests 24-hour cache retention where supported.
const CacheRetentionLong = "24h"

type Request struct {
	Model                string       `json:"model"`
	MaxTokens            int          `json:"max_tokens"`
	ReasoningEffort      string       `json:"reasoning_effort,omitempty"`
	System               string       `json:"system"`
	Messages             []APIMessage `json:"messages"`
	Tools                []ToolSchema `json:"tools,omitempty"`
	Stream               bool         `json:"stream,omitempty"`
	PromptCacheKey       string       `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string       `json:"prompt_cache_retention,omitempty"`
	// AnthropicNative signals that the target provider is a native Anthropic API.
	// When true, marshalAnthropicBody may use content-block array format with
	// cache_control on the system prompt (which some compatible providers reject).
	AnthropicNative bool `json:"-"`
}

type Response struct {
	Content          []Block `json:"content"`
	StopReason       string  `json:"stop_reason"`
	ReasoningContent string  `json:"reasoning_content,omitempty"`
	Usage            *Usage  `json:"usage,omitempty"`
}

type Usage struct {
	InputTokens      int  `json:"input_tokens,omitempty"`
	OutputTokens     int  `json:"output_tokens,omitempty"`
	// CacheReadTokens is the canonical cache-read field used by OpenAI-style
	// providers (cached_tokens / cache_read_tokens) and is also what the
	// usage reporting layer aggregates. The Anthropic alias
	// (cache_read_input_tokens) is decoded into the same field below.
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
	// CacheWriteTokens is the canonical cache-write field used by OpenAI-style
	// providers (cache_creation_tokens). Anthropic's
	// cache_creation_input_tokens is decoded into the same field below.
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`
	Estimated        bool   `json:"estimated,omitempty"`
	// CacheReadTokensAnthropic / CacheWriteTokensAnthropic are alternate
	// names that Anthropic uses; they are decoded into the canonical
	// CacheReadTokens / CacheWriteTokens fields above. They are kept here
	// purely so the JSON decoder can match them by name; downstream code
	// should always read the canonical fields.
	CacheReadTokensAnthropic  int `json:"cache_read_input_tokens,omitempty"`
	CacheWriteTokensAnthropic int `json:"cache_creation_input_tokens,omitempty"`
}

// Normalize collapses the Anthropic and OpenAI cache field aliases into the
// canonical CacheReadTokens / CacheWriteTokens fields and clears the alias
// fields so the rest of the system only ever sees the canonical names.
func (u *Usage) Normalize() {
	if u == nil {
		return
	}
	if u.CacheReadTokens == 0 {
		u.CacheReadTokens = u.CacheReadTokensAnthropic
	}
	if u.CacheWriteTokens == 0 {
		u.CacheWriteTokens = u.CacheWriteTokensAnthropic
	}
	u.CacheReadTokensAnthropic = 0
	u.CacheWriteTokensAnthropic = 0
}

func TextBlock(text string) Block {
	return Block{Type: BlockText, Text: text}
}

func ImageBlock(mediaType, data string) Block {
	return Block{
		Type: BlockImage,
		Source: &ImageSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      data,
		},
	}
}

func ToolUseBlock(id, name string, input map[string]interface{}) Block {
	return Block{Type: BlockToolUse, ID: id, Name: name, Input: cloneMap(input)}
}

func ToolResultBlock(toolUseID, content string) Block {
	return Block{Type: BlockToolResult, ToolUseID: toolUseID, Content: content}
}

func NewMessage(role string, blocks ...Block) Message {
	return Message{Role: role, Content: cloneBlocks(blocks)}
}

func NewTextMessage(role, text string) Message {
	return NewMessage(role, TextBlock(text))
}

func NewEphemeralTextMessage(kind MessageKind, text string) Message {
	return Message{
		Role:    RoleUser,
		Content: []Block{TextBlock(text)},
		Metadata: &Metadata{
			Kind:      kind,
			Ephemeral: true,
		},
	}
}

func NewSummaryMessage(text, transcript string) Message {
	return Message{
		Role:    RoleUser,
		Content: []Block{TextBlock(text)},
		Metadata: &Metadata{
			Kind:       KindSummary,
			Transcript: transcript,
		},
	}
}

func MessageFromResponse(resp Response) Message {
	blocks := make([]Block, 0, len(resp.Content))
	for _, block := range resp.Content {
		switch block.Type {
		case BlockText:
			blocks = append(blocks, TextBlock(block.Text))
		case BlockToolUse:
			blocks = append(blocks, ToolUseBlock(block.ID, block.Name, block.Input))
		}
	}
	msg := Message{Role: RoleAssistant, Content: blocks}
	if strings.TrimSpace(resp.ReasoningContent) != "" {
		msg.Metadata = &Metadata{ReasoningContent: resp.ReasoningContent}
	}
	return msg
}

func MessageText(msg Message) string {
	return BlocksText(msg.Content)
}

func BlocksText(blocks []Block) string {
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type == BlockText {
			builder.WriteString(block.Text)
		}
	}
	return builder.String()
}

func ToolUses(blocks []Block) []Block {
	result := make([]Block, 0)
	for _, block := range blocks {
		if block.Type == BlockToolUse {
			result = append(result, Block{Type: block.Type, ID: block.ID, Name: block.Name, Input: cloneMap(block.Input)})
		}
	}
	return result
}

func HasToolUse(blocks []Block) bool {
	for _, block := range blocks {
		if block.Type == BlockToolUse {
			return true
		}
	}
	return false
}

func CloneMessages(messages []Message) []Message {
	result := make([]Message, 0, len(messages))
	for _, msg := range messages {
		result = append(result, msg.Clone())
	}
	return result
}

func (m Message) Clone() Message {
	clone := Message{Role: m.Role, Content: cloneBlocks(m.Content)}
	if m.Metadata != nil {
		clone.Metadata = cloneMetadata(m.Metadata)
	}
	return clone
}

func ToAPIMessages(messages []Message) []APIMessage {
	apiMessages := make([]APIMessage, 0, len(messages))
	for _, msg := range messages {
		content := apiBlocks(msg.Content)
		if len(content) == 0 {
			// Repair: don't skip the message; insert a placeholder so the
			// conversation structure is preserved and the API never sees a
			// message with a missing "content" field.
			content = []Block{TextBlock("(message content unavailable)")}
		}
		apiMessage := APIMessage{Role: msg.Role, Content: content}
		if msg.Metadata != nil && msg.Role == RoleAssistant {
			apiMessage.ReasoningContent = msg.Metadata.ReasoningContent
		}
		apiMessages = append(apiMessages, apiMessage)
	}
	return apiMessages
}

func ToolSchemaFromMap(schema map[string]interface{}) ToolSchema {
	name, _ := schema["name"].(string)
	description, _ := schema["description"].(string)
	inputSchema, _ := schema["input_schema"].(map[string]interface{})
	return ToolSchema{
		Name:        name,
		Description: description,
		InputSchema: cloneMap(inputSchema),
	}
}

func cloneBlocks(blocks []Block) []Block {
	result := make([]Block, 0, len(blocks))
	for _, block := range blocks {
		result = append(result, Block{
			Type:      block.Type,
			Text:      block.Text,
			Source:    cloneImageSource(block.Source),
			ID:        block.ID,
			Name:      block.Name,
			Input:     cloneMap(block.Input),
			ToolUseID: block.ToolUseID,
			Content:   block.Content,
		})
	}
	return result
}

func apiBlocks(blocks []Block) []Block {
	result := make([]Block, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case BlockText:
			result = append(result, TextBlock(block.Text))
		case BlockImage:
			if block.Source != nil {
				result = append(result, ImageBlock(block.Source.MediaType, block.Source.Data))
			}
		case BlockToolUse:
			input := cloneMap(block.Input)
			if input == nil {
				input = map[string]interface{}{}
			}
			result = append(result, ToolUseBlock(block.ID, block.Name, input))
		case BlockToolResult:
			result = append(result, ToolResultBlock(block.ToolUseID, block.Content))
		default:
			// Repair: convert unrecognized block types to text blocks
			// so the message content is never silently lost.
			fallback := blockTextOrDefault(block)
			if fallback != "" {
				result = append(result, TextBlock(fallback))
			}
		}
	}
	return result
}

// blockTextOrDefault extracts the best available text from any block,
// used as a fallback when a block has an unrecognized type.
func blockTextOrDefault(block Block) string {
	if block.Text != "" {
		return block.Text
	}
	if block.Content != "" {
		return block.Content
	}
	if block.Name != "" {
		return block.Name
	}
	return ""
}

func cloneMetadata(metadata *Metadata) *Metadata {
	if metadata == nil {
		return nil
	}
	cloned := *metadata
	if len(metadata.Parts) > 0 {
		cloned.Parts = make([]ContentPart, 0, len(metadata.Parts))
		for _, part := range metadata.Parts {
			next := ContentPart{
				Type:     part.Type,
				Text:     part.Text,
				MIMEType: part.MIMEType,
			}
			if part.Attachment != nil {
				attachment := *part.Attachment
				next.Attachment = &attachment
			}
			cloned.Parts = append(cloned.Parts, next)
		}
	}
	if len(metadata.Attachments) > 0 {
		cloned.Attachments = append([]Attachment{}, metadata.Attachments...)
	}
	return &cloned
}

func (b Block) MarshalJSON() ([]byte, error) {
	payload := map[string]interface{}{
		"type": b.Type,
	}
	switch b.Type {
	case BlockText:
		payload["text"] = b.Text
	case BlockImage:
		if b.Source != nil {
			payload["source"] = b.Source
		}
	case BlockToolUse:
		payload["id"] = b.ID
		payload["name"] = b.Name
		if b.Input == nil {
			payload["input"] = map[string]interface{}{}
		} else {
			payload["input"] = b.Input
		}
	case BlockToolResult:
		payload["tool_use_id"] = b.ToolUseID
		payload["content"] = b.Content
	default:
		if b.Text != "" {
			payload["text"] = b.Text
		}
		if b.Source != nil {
			payload["source"] = b.Source
		}
		if b.ID != "" {
			payload["id"] = b.ID
		}
		if b.Name != "" {
			payload["name"] = b.Name
		}
		if b.Input != nil {
			payload["input"] = b.Input
		}
		if b.ToolUseID != "" {
			payload["tool_use_id"] = b.ToolUseID
		}
		if b.Content != "" {
			payload["content"] = b.Content
		}
	}
	return json.Marshal(payload)
}

func cloneImageSource(source *ImageSource) *ImageSource {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]interface{}, len(input))
	for key, value := range input {
		result[key] = cloneValue(value)
	}
	return result
}

func cloneValue(value interface{}) interface{} {
	if value == nil {
		return nil
	}

	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneMap(typed)
	case []interface{}:
		return cloneSlice(typed)
	}

	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Map:
		if rv.IsNil() {
			return value
		}
		cloned := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			cloned.SetMapIndex(iter.Key(), reflect.ValueOf(cloneValue(iter.Value().Interface())))
		}
		return cloned.Interface()
	case reflect.Slice:
		if rv.IsNil() {
			return value
		}
		cloned := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			cloned.Index(i).Set(reflect.ValueOf(cloneValue(rv.Index(i).Interface())))
		}
		return cloned.Interface()
	default:
		return value
	}
}

func cloneSlice(input []interface{}) []interface{} {
	if len(input) == 0 {
		return nil
	}
	result := make([]interface{}, 0, len(input))
	for _, value := range input {
		result = append(result, cloneValue(value))
	}
	return result
}
