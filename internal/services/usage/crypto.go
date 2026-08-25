package usage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// masterKeyFileName is the AES-256 master key persisted under the state dir.
// It encrypts business-key secrets at rest so a pin-verified reveal can return
// plaintext later (a bare key_hash is one-way and forces "reset to see").
const masterKeyFileName = "usage-master.key"

// loadOrCreateMasterKey reads the 32-byte master key from stateDir, creating
// it on first run (0600). Losing this file makes previously encrypted secrets
// unrecoverable — callers should treat it like the database itself.
func loadOrCreateMasterKey(stateDir string) ([]byte, error) {
	path := filepath.Join(stateDir, masterKeyFileName)
	if data, err := os.ReadFile(path); err == nil && len(data) == 32 {
		return data, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	return key, nil
}

// encryptSecret seals plaintext with AES-256-GCM (random nonce prepended,
// base64 output). Output is self-contained for decryptSecret.
func encryptSecret(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// decryptSecret opens an encryptSecret payload.
func decryptSecret(key []byte, encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	plain, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(plain), nil
}

// hashPin returns HMAC-SHA256(masterKey, pin) as hex. Keyed so a leaked hash
// cannot be brute-forced offline without the master key.
func hashPin(key []byte, pin string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(pin))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyPin constant-time compares pin against the stored hash.
func verifyPin(key []byte, pin, hash string) bool {
	return hmac.Equal([]byte(hashPin(key, pin)), []byte(hash))
}
