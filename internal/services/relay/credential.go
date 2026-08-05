package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// GenerateCredential creates a new per-node credential with a ck_ prefix.
// The plaintext is returned exactly once to the caller (e.g. the center admin
// copies it into the node config); the center only stores its hash.
func GenerateCredential() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "ck_" + hex.EncodeToString(buf), nil
}

// HashCredential returns a deterministic non-reversible digest of a credential.
func HashCredential(cred string) string {
	sum := sha256.Sum256([]byte(cred))
	return hex.EncodeToString(sum[:])
}

// ValidateCredential reports whether the presented credential matches the
// stored hash. An empty stored hash always rejects so unprovisioned nodes
// cannot authenticate.
func ValidateCredential(cred, hash string) bool {
	if hash == "" {
		return false
	}
	return HashCredential(cred) == hash
}
