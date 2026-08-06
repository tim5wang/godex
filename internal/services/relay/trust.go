package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// RelayTrustHeader is the internal header the node-side relay agent injects
// into requests it forwards to the local httpapi handler. The center-side
// ProxyHandler never forwards this header from the outside, so its presence
// proves the request arrived over an authenticated relay channel (the node
// dialed the center outbound with its per-node credential and the hub accepted
// its hello). The node's httpapi uses it to skip the web-token check for
// relay-originated requests, while direct/unauthorized callers still require
// the web token.
const RelayTrustHeader = "X-Godex-Relay-Trusted"

// SignRelayTrust computes the expected value of RelayTrustHeader for a node:
// an HMAC-SHA256 over the node id keyed by the node's credential. Only the
// node (which holds the credential) can produce a matching value, so a
// malicious client that reaches the node's local API directly cannot forge it.
func SignRelayTrust(nodeID, credential string) string {
	mac := hmac.New(sha256.New, []byte(credential))
	_, _ = mac.Write([]byte(nodeID))
	return hex.EncodeToString(mac.Sum(nil))
}

// ValidateRelayTrust reports whether the presented header value matches the
// signature for this node id + credential. Empty inputs always reject.
func ValidateRelayTrust(headerValue, nodeID, credential string) bool {
	if headerValue == "" || nodeID == "" || credential == "" {
		return false
	}
	expected := SignRelayTrust(nodeID, credential)
	return hmac.Equal([]byte(expected), []byte(headerValue))
}
