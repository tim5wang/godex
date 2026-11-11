package llmcapture

import (
	"encoding/json"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// Capture preserves harness-only fields without changing Request's wire JSON.
// This is a complete harness context snapshot, not a provider HTTP body.
type capturedRequest struct {
	protocol.Request
	RuntimeTail             string                   `json:"runtime_tail,omitempty"`
	ContextSources          []protocol.ContextSource `json:"context_sources,omitempty"`
	IncludeReasoningContent bool                     `json:"include_reasoning_content"`
}

func (r Record) MarshalJSON() ([]byte, error) {
	type plain Record
	return json.Marshal(struct {
		*plain
		CaptureFormat string          `json:"capture_format"`
		Request       capturedRequest `json:"request"`
	}{plain: (*plain)(&r), CaptureFormat: "godex_context_v2", Request: capturedRequest{Request: r.Request, RuntimeTail: r.Request.RuntimeTail, ContextSources: r.Request.ContextSources, IncludeReasoningContent: r.Request.IncludeReasoningContent}})
}

func (r *Record) UnmarshalJSON(data []byte) error {
	type plain Record
	decoded := struct {
		*plain
		Request capturedRequest `json:"request"`
	}{plain: (*plain)(r)}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	r.Request = decoded.Request.Request
	r.Request.RuntimeTail = decoded.Request.RuntimeTail
	r.Request.ContextSources = decoded.Request.ContextSources
	r.Request.IncludeReasoningContent = decoded.Request.IncludeReasoningContent
	return nil
}
