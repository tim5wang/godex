// Package idgen creates opaque prefixed runtime identifiers.
package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"
)

// New returns prefix plus cryptographic entropy. The time fallback preserves
// availability if the platform entropy source fails.
func New(prefix string, entropyBytes int) string {
	prefix = strings.TrimSpace(prefix)
	if entropyBytes <= 0 {
		entropyBytes = 12
	}
	buffer := make([]byte, entropyBytes)
	if _, err := io.ReadFull(rand.Reader, buffer); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(buffer)
}
