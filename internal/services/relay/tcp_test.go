package relay

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func tcpOpenPayloadJSON(connID, target string) json.RawMessage {
	data, _ := json.Marshal(TCPOpenPayload{ConnID: connID, Target: target})
	return data
}

func tcpClosePayloadJSON(connID, reason string) json.RawMessage {
	data, _ := json.Marshal(TCPClosePayload{ConnID: connID, Reason: reason})
	return data
}

func TestEncodeDecodeTCPOpenFrame(t *testing.T) {
	frame := Frame{
		Type:    FrameTCPOpen,
		Payload: tcpOpenPayloadJSON("c_1", "10.0.0.5:3306"),
	}
	data, err := EncodeFrame(frame)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Type != FrameTCPOpen {
		t.Fatalf("type mismatch: %q", decoded.Type)
	}
	var payload TCPOpenPayload
	if err := json.Unmarshal(decoded.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if payload.ConnID != "c_1" || payload.Target != "10.0.0.5:3306" {
		t.Fatalf("payload mismatch: %#v", payload)
	}
}

func TestEncodeDecodeTCPDataFrame(t *testing.T) {
	frame := Frame{
		Type:    FrameTCPData,
		Payload: tcpDataPayloadJSON("c_1"),
		BodyB64: base64.StdEncoding.EncodeToString([]byte("ping")),
	}
	data, err := EncodeFrame(frame)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Type != FrameTCPData {
		t.Fatalf("type mismatch: %q", decoded.Type)
	}
	raw, err := decodedTCPChunk(decoded)
	if err != nil {
		t.Fatalf("chunk decode: %v", err)
	}
	if string(raw) != "ping" {
		t.Fatalf("chunk mismatch: %q", raw)
	}
}

func TestEncodeDecodeTCPCloseFrame(t *testing.T) {
	frame := Frame{
		Type:    FrameTCPClose,
		Payload: tcpClosePayloadJSON("c_1", "target refused"),
	}
	data, err := EncodeFrame(frame)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var payload TCPClosePayload
	if err := json.Unmarshal(decoded.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if payload.ConnID != "c_1" || payload.Reason != "target refused" {
		t.Fatalf("payload mismatch: %#v", payload)
	}
}

func TestAllowForward(t *testing.T) {
	cases := []struct {
		name   string
		allow  []string
		target string
		want   bool
	}{
		{"exact host:port match", []string{"10.0.0.5:3306"}, "10.0.0.5:3306", true},
		{"host mismatch", []string{"10.0.0.5:3306"}, "10.0.0.6:3306", false},
		{"port mismatch", []string{"10.0.0.5:3306"}, "10.0.0.5:5432", false},
		{"wildcard port matches", []string{"127.0.0.1:*"}, "127.0.0.1:3306", true},
		{"wildcard port but host differs", []string{"127.0.0.1:*"}, "10.0.0.5:3306", false},
		{"wildcard host matches", []string{"*:3306"}, "10.0.0.5:3306", true},
		{"wildcard both matches", []string{"*:*"}, "10.0.0.5:3306", true},
		{"empty allowlist denies all", nil, "10.0.0.5:3306", false},
		{"empty allowlist entry denies", []string{""}, "10.0.0.5:3306", false},
		{"malformed target denied", []string{"*:*"}, "no-port-here", false},
		{"second rule matches", []string{"10.0.0.5:3306", "db.internal:5432"}, "db.internal:5432", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AllowForward(tc.allow, tc.target); got != tc.want {
				t.Fatalf("AllowForward(%v, %q) = %v, want %v", tc.allow, tc.target, got, tc.want)
			}
		})
	}
}
