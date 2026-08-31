package conversation

import (
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// anthropicStream accumulates wire events into one protocol response. Keeping
// event mutation here leaves parseMessageStream responsible only for SSE framing.
type anthropicStream struct {
	handler  StreamHandler
	response *protocol.Response
	states   map[int]*streamBlockState
	order    []int
}

func newAnthropicStream(handler StreamHandler) *anthropicStream {
	return &anthropicStream{
		handler:  handler,
		response: &protocol.Response{},
		states:   make(map[int]*streamBlockState),
	}
}

func (s *anthropicStream) ensureState(index int, block protocol.Block) *streamBlockState {
	if state, ok := s.states[index]; ok {
		if state.block.Type == "" && block.Type != "" {
			state.block = block
		}
		return state
	}
	state := &streamBlockState{block: block}
	s.states[index] = state
	s.order = append(s.order, index)
	return state
}

func (s *anthropicStream) handle(event streamWireEvent) error {
	switch event.Type {
	case "message_start":
		s.handleMessageStart(event)
	case "content_block_start":
		s.handleBlockStart(event)
	case "content_block_delta":
		s.handleBlockDelta(event)
	case "content_block_stop":
		s.handleBlockStop(event.Index)
	case "message_delta":
		s.handleMessageDelta(event)
	case "message_stop":
		s.closePartialTools()
	case "ping":
	case "error":
		return streamEventError(event.Error)
	}
	s.forwardOpenAIToolDeltas(event)
	return nil
}

func (s *anthropicStream) handleMessageStart(event streamWireEvent) {
	if event.Message == nil {
		return
	}
	s.response.StopReason = event.Message.StopReason
	if event.Message.Usage == nil {
		return
	}
	usage := *event.Message.Usage
	usage.Normalize()
	s.response.Usage = &usage
	if s.handler.OnMessageStart != nil {
		s.handler.OnMessageStart(usage)
	}
}

func (s *anthropicStream) handleBlockStart(event streamWireEvent) {
	block := blockFromStreamStart(event.ContentBlock)
	state := s.ensureState(event.Index, block)
	if block.Type == protocol.BlockThinking {
		s.seedThinking(state, block)
	}
	if block.Type == protocol.BlockText && block.Text != "" && s.handler.OnTextDelta != nil {
		s.handler.OnTextDelta(block.Text)
	}
	if block.Type == protocol.BlockToolUse && s.handler.OnToolUse != nil {
		s.handler.OnToolUse(block, "")
	}
}

func blockFromStreamStart(content *streamContentBlock) protocol.Block {
	if content == nil {
		return protocol.Block{}
	}
	switch content.Type {
	case "thinking":
		return protocol.Block{Type: protocol.BlockThinking, Text: content.Thinking, Signature: content.Signature}
	case "redacted_thinking":
		return protocol.Block{Type: protocol.BlockThinking, Text: "[Reasoning redacted]", Signature: content.Data, Redacted: true}
	default:
		return protocol.Block{Type: protocol.BlockType(content.Type), Text: content.Text, ID: content.ID, Name: content.Name, Input: content.Input}
	}
}

func (s *anthropicStream) seedThinking(state *streamBlockState, block protocol.Block) {
	if block.Text != "" {
		state.partialThinking.WriteString(block.Text)
		if s.handler.OnThinkingDelta != nil {
			s.handler.OnThinkingDelta(block.Text, "")
		}
	}
	if block.Signature != "" {
		state.partialSignature.WriteString(block.Signature)
		if s.handler.OnThinkingDelta != nil {
			s.handler.OnThinkingDelta("", block.Signature)
		}
	}
	state.block.Text = state.partialThinking.String()
	state.block.Signature = state.partialSignature.String()
}

func (s *anthropicStream) handleBlockDelta(event streamWireEvent) {
	switch event.Delta.Type {
	case "text_delta":
		s.appendText(event.Index, event.Delta.Text)
	case "input_json_delta":
		s.appendToolInput(event.Index, event.Delta.PartialJSON)
	case "thinking_delta":
		s.appendThinking(event.Index, event.Delta.Thinking)
	case "signature_delta":
		s.appendSignature(event.Index, event.Delta.Signature)
	}
}

func (s *anthropicStream) appendText(index int, text string) {
	state := s.ensureState(index, protocol.TextBlock(""))
	state.block.Type = protocol.BlockText
	state.block.Text += text
	if text != "" && s.handler.OnTextDelta != nil {
		s.handler.OnTextDelta(text)
	}
}

func (s *anthropicStream) appendToolInput(index int, fragment string) {
	state := s.ensureState(index, protocol.ToolUseBlock("", "", nil))
	state.block.Type = protocol.BlockToolUse
	state.partialJSON.WriteString(fragment)
	if s.handler.OnToolUse != nil {
		s.handler.OnToolUse(state.block, state.partialJSON.String())
	}
}

func (s *anthropicStream) appendThinking(index int, text string) {
	state := s.ensureState(index, protocol.Block{Type: protocol.BlockThinking})
	state.block.Type = protocol.BlockThinking
	state.partialThinking.WriteString(text)
	state.block.Text = state.partialThinking.String()
	if text != "" && s.handler.OnThinkingDelta != nil {
		s.handler.OnThinkingDelta(text, "")
	}
}

func (s *anthropicStream) appendSignature(index int, signature string) {
	state := s.ensureState(index, protocol.Block{Type: protocol.BlockThinking})
	state.block.Type = protocol.BlockThinking
	state.partialSignature.WriteString(signature)
	state.block.Signature = state.partialSignature.String()
	if signature != "" && s.handler.OnThinkingDelta != nil {
		s.handler.OnThinkingDelta("", signature)
	}
}

func (s *anthropicStream) handleBlockStop(index int) {
	state, ok := s.states[index]
	if !ok {
		return
	}
	switch state.block.Type {
	case protocol.BlockToolUse:
		s.finalizeTool(state, true)
	case protocol.BlockThinking:
		state.block.Text = state.partialThinking.String()
		state.block.Signature = state.partialSignature.String()
	}
}

func (s *anthropicStream) finalizeTool(state *streamBlockState, notify bool) {
	raw := strings.TrimSpace(state.partialJSON.String())
	if raw != "" {
		state.block.Input, _ = recoverPartialToolInput(raw)
	}
	if state.block.Input == nil {
		state.block.Input = map[string]interface{}{}
	}
	if notify && s.handler.OnToolUse != nil {
		s.handler.OnToolUse(state.block, state.partialJSON.String())
	}
}

func (s *anthropicStream) handleMessageDelta(event streamWireEvent) {
	if event.Delta.StopReason != "" {
		s.response.StopReason = event.Delta.StopReason
	}
	usageDelta := event.Usage
	if usageDelta == nil {
		usageDelta = event.Delta.Usage
	}
	if usageDelta == nil {
		return
	}
	if s.response.Usage == nil {
		usage := *usageDelta
		usage.Normalize()
		s.response.Usage = &usage
		return
	}
	mergeUsageDelta(s.response.Usage, usageDelta)
}

func (s *anthropicStream) closePartialTools() {
	for _, state := range s.states {
		if state != nil && state.block.Type == protocol.BlockToolUse && len(state.block.Input) == 0 {
			s.finalizeTool(state, false)
		}
	}
}

func (s *anthropicStream) forwardOpenAIToolDeltas(event streamWireEvent) {
	if s.handler.OnToolUse == nil {
		return
	}
	for _, choice := range event.Choices {
		for _, call := range choice.Delta.ToolCalls {
			block := protocol.Block{Type: protocol.BlockToolUse, ID: call.ID, Name: call.Function.Name, Index: call.Index}
			s.handler.OnToolUse(block, call.Function.Arguments)
		}
	}
}

func (s *anthropicStream) result() *protocol.Response {
	s.response.Content = make([]protocol.Block, 0, len(s.order))
	for _, index := range s.order {
		block := s.states[index].block
		switch block.Type {
		case protocol.BlockText:
			s.response.Content = append(s.response.Content, protocol.TextBlock(block.Text))
		case protocol.BlockToolUse:
			s.response.Content = append(s.response.Content, protocol.ToolUseBlock(block.ID, block.Name, block.Input))
		case protocol.BlockThinking:
			s.response.Content = append(s.response.Content, protocol.ThinkingBlock(block.Text, block.Signature, block.Redacted))
		}
	}
	return s.response
}

func streamEventError(wireErr *streamWireError) error {
	if wireErr != nil && wireErr.Message != "" {
		return fmt.Errorf("stream error: %s", wireErr.Message)
	}
	return fmt.Errorf("stream error")
}
