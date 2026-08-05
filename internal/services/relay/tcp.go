package relay

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"strings"
)

// TCPOpenPayload is the payload of a tcp_open frame (hub → node): the node
// should dial target and associate the resulting stream with conn_id.
type TCPOpenPayload struct {
	ConnID string `json:"conn_id"`
	Target string `json:"target"`
}

// TCPDataPayload is the payload of a tcp_data frame. The chunk itself travels
// in the frame's BodyB64 field to avoid double base64 encoding.
type TCPDataPayload struct {
	ConnID string `json:"conn_id"`
}

// TCPClosePayload is the payload of a tcp_close frame (either side).
type TCPClosePayload struct {
	ConnID string `json:"conn_id"`
	Reason string `json:"reason,omitempty"`
}

func tcpDataPayloadJSON(connID string) json.RawMessage {
	data, _ := json.Marshal(TCPDataPayload{ConnID: connID})
	return data
}

// decodedTCPChunk returns the raw bytes carried by a tcp_data frame.
func decodedTCPChunk(frame Frame) ([]byte, error) {
	if frame.BodyB64 == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(frame.BodyB64)
}

// AllowForward reports whether target is permitted by the node's forward_allow
// allowlist. Each entry is "host:port"; either side may be "*" to wildcard.
// An empty or nil allowlist denies everything (default-deny), and malformed
// targets are always rejected.
func AllowForward(allow []string, target string) bool {
	if target == "" {
		return false
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return false
	}
	for _, rule := range allow {
		ruleHost, rulePort, err := net.SplitHostPort(strings.TrimSpace(rule))
		if err != nil {
			continue
		}
		if (ruleHost == "*" || ruleHost == host) && (rulePort == "*" || rulePort == port) {
			return true
		}
	}
	return false
}
