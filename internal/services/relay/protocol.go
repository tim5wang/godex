package relay

import (
	"encoding/json"
	"fmt"
)

// FrameType identifies the relay frame kind exchanged between a node and the
// center hub over a single WebSocket connection.
type FrameType string

const (
	// FrameHello is sent by a node immediately after connecting: node_id,
	// credential, version, and capabilities.
	FrameHello FrameType = "hello"
	// FrameHelloOK is sent by the hub once a hello frame is accepted.
	FrameHelloOK FrameType = "hello_ok"
	// FrameHelloError is sent by the hub when a hello frame is rejected.
	FrameHelloError FrameType = "hello_err"
	// FrameRequest carries a forwarded API request from hub to node.
	FrameRequest FrameType = "request"
	// FrameResponse carries the final response for a non-streaming request.
	FrameResponse FrameType = "response"
	// FrameStream carries a chunk of a streaming response body.
	FrameStream FrameType = "stream"
	// FrameStreamEnd marks the end of a streaming response.
	FrameStreamEnd FrameType = "stream_end"
	// FrameStreamInput carries reverse-direction bytes (e.g. terminal input).
	FrameStreamInput FrameType = "stream_input"
	// FramePing is an application-level liveness probe (hub → node).
	FramePing FrameType = "ping"
	// FramePong acknowledges a ping (node → hub).
	FramePong FrameType = "pong"
	// FrameEvent carries an unsolicited node→hub state update (session/job/approval).
	FrameEvent FrameType = "event"
	// FrameError carries a per-request error reply.
	FrameError FrameType = "error"
	// FrameClose is a graceful shutdown notice from either side.
	FrameClose FrameType = "close"
)

var validFrameTypes = map[FrameType]bool{
	FrameHello:       true,
	FrameHelloOK:     true,
	FrameHelloError:  true,
	FrameRequest:     true,
	FrameResponse:    true,
	FrameStream:      true,
	FrameStreamEnd:   true,
	FrameStreamInput: true,
	FramePing:        true,
	FramePong:        true,
	FrameEvent:       true,
	FrameError:       true,
	FrameClose:       true,
}

// Frame is the JSON envelope for every relay message. One WebSocket carries
// many concurrent requests multiplexed by ReqID.
type Frame struct {
	Type       FrameType         `json:"type"`
	Seq        int64             `json:"seq,omitempty"`
	T          int64             `json:"t,omitempty"`
	ReqID      string            `json:"req_id,omitempty"`
	NodeID     string            `json:"node_id,omitempty"`
	Credential string            `json:"credential,omitempty"`
	Version    string            `json:"version,omitempty"`
	Caps       []string          `json:"caps,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Method     string            `json:"method,omitempty"`
	Path       string            `json:"path,omitempty"`
	Query      string            `json:"query,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Status     int               `json:"status,omitempty"`
	BodyB64    string            `json:"body_b64,omitempty"`
	Final      bool              `json:"final,omitempty"`
	Kind       string            `json:"kind,omitempty"`
	Payload    json.RawMessage   `json:"payload,omitempty"`
}

// EncodeFrame serializes a frame to its wire representation.
func EncodeFrame(f Frame) ([]byte, error) {
	return json.Marshal(f)
}

// DecodeFrame parses a wire representation back into a Frame. It rejects
// malformed JSON and unknown frame types so callers never dispatch on
// unexpected message kinds.
func DecodeFrame(data []byte) (Frame, error) {
	var frame Frame
	if err := json.Unmarshal(data, &frame); err != nil {
		return Frame{}, fmt.Errorf("decode relay frame: %w", err)
	}
	if !validFrameTypes[frame.Type] {
		return Frame{}, fmt.Errorf("unknown relay frame type %q", frame.Type)
	}
	return frame, nil
}
