package relay

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeDecodeHelloFrame(t *testing.T) {
	frame := Frame{
		Type:       FrameHello,
		NodeID:     "node_abc123",
		Credential: "ck_secret",
		Version:    "v1.2.0",
		Caps:       []string{"chat", "terminal", "files"},
	}
	data, err := EncodeFrame(frame)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(data), `"node_abc123"`) {
		t.Fatalf("encoded frame missing node id: %s", data)
	}
	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Type != FrameHello || decoded.NodeID != "node_abc123" ||
		decoded.Credential != "ck_secret" || decoded.Version != "v1.2.0" {
		t.Fatalf("round trip mismatch: %#v", decoded)
	}
	if len(decoded.Caps) != 3 || decoded.Caps[1] != "terminal" {
		t.Fatalf("caps mismatch: %#v", decoded.Caps)
	}
}

func TestEncodeDecodeRequestResponse(t *testing.T) {
	req := Frame{
		Type:    FrameRequest,
		ReqID:   "r_42",
		Method:  "GET",
		Path:    "/meta",
		Query:   "verbose=1",
		Headers: map[string]string{"Authorization": "Bearer tok"},
		BodyB64: "aGVsbG8=",
	}
	data, err := EncodeFrame(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.ReqID != "r_42" || decoded.Method != "GET" || decoded.Path != "/meta" {
		t.Fatalf("request round trip mismatch: %#v", decoded)
	}
	if decoded.Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("headers mismatch: %#v", decoded.Headers)
	}

	resp := Frame{Type: FrameResponse, ReqID: "r_42", Status: 200, BodyB64: "e30="}
	respData, err := EncodeFrame(resp)
	if err != nil {
		t.Fatalf("encode resp: %v", err)
	}
	decodedResp, err := DecodeFrame(respData)
	if err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if decodedResp.Status != 200 || decodedResp.BodyB64 != "e30=" {
		t.Fatalf("response round trip mismatch: %#v", decodedResp)
	}
}

func TestDecodeRejectsMalformedJSON(t *testing.T) {
	if _, err := DecodeFrame([]byte("{not json")); err == nil {
		t.Fatal("expected error for malformed json")
	}
}

func TestDecodePreservesExtraPayload(t *testing.T) {
	raw := `{"type":"event","kind":"session_updated","payload":{"sid":"s1","phase":"run"}}`
	frame, err := DecodeFrame([]byte(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if frame.Type != FrameEvent || frame.Kind != "session_updated" {
		t.Fatalf("event frame mismatch: %#v", frame)
	}
	var payload map[string]string
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if payload["sid"] != "s1" || payload["phase"] != "run" {
		t.Fatalf("payload mismatch: %#v", payload)
	}
}

func TestDecodeRejectsUnknownFrameType(t *testing.T) {
	if _, err := DecodeFrame([]byte(`{"type":"mystery"}`)); err == nil {
		t.Fatal("expected error for unknown frame type")
	}
}
