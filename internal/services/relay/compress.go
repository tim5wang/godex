package relay

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
)

// gzipThreshold is the minimum body size (bytes) before a frame body is worth
// compressing. Small control frames (pings, tcp acks, tiny tool results) would
// pay gzip overhead for nothing; LLM context bodies are many KB and compress
// 10-20x.
const gzipThreshold = 1024

// encodeBodyB64 renders a frame body for the wire: base64 always, gzip first
// when canGzip is true and the body is large enough to benefit. The caller
// stores the returned compressed flag on the frame. A nil/empty body is never
// compressed (the receiver treats it as no body).
func encodeBodyB64(data []byte, canGzip bool) (b64 string, compressed bool) {
	if len(data) == 0 {
		return "", false
	}
	if canGzip && len(data) > gzipThreshold {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(data)
		_ = zw.Close()
		return base64.StdEncoding.EncodeToString(buf.Bytes()), true
	}
	return base64.StdEncoding.EncodeToString(data), false
}

// decodeBodyB64 renders a wire body back to bytes, gunzipping when the frame
// declares it compressed. A malformed gzip stream is a protocol violation, but
// the relay treats it defensively: it falls back to the raw bytes rather than
// failing the whole connection over one bad frame.
func decodeBodyB64(b64 string, compressed bool) []byte {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) == 0 {
		return nil
	}
	if !compressed {
		return data
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return data
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		return data
	}
	return out
}

// hasCap reports whether a capability list contains cap.
func hasCap(caps []string, cap string) bool {
	for _, c := range caps {
		if c == cap {
			return true
		}
	}
	return false
}
