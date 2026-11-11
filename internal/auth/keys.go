package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// Key format (D5): ckp_<id>_<secret>
//   - id: 12 lowercase base32 characters (60 random bits, not secret)
//   - secret: 43 base62 characters (>= 256 bits from crypto/rand)
const (
	KeyPrefix    = "ckp_"
	keyIDLen     = 12
	keySecretLen = 43
	keyLen       = len(KeyPrefix) + keyIDLen + 1 + keySecretLen
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// newKeyID returns a random key ID.
func newKeyID() string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	return idEncoding.EncodeToString(b[:])[:keyIDLen]
}

// newSecret returns 43 uniformly random base62 characters. Rejection
// sampling avoids modulo bias: 43 * log2(62) ≈ 256.03 bits.
func newSecret() string {
	out := make([]byte, 0, keySecretLen)
	var buf [64]byte
	for len(out) < keySecretLen {
		_, _ = rand.Read(buf[:])
		for _, b := range buf {
			if b >= 248 { // 248 = 4 * 62
				continue
			}
			out = append(out, base62[b%62])
			if len(out) == keySecretLen {
				break
			}
		}
	}
	return string(out)
}

// generateKey returns a new key and its ID.
func generateKey() (key, id string) {
	id = newKeyID()
	return KeyPrefix + id + "_" + newSecret(), id
}

// parseKey extracts the ID from a well-formed key.
func parseKey(key string) (id string, ok bool) {
	if len(key) != keyLen || !strings.HasPrefix(key, KeyPrefix) || key[len(KeyPrefix)+keyIDLen] != '_' {
		return "", false
	}
	id = key[len(KeyPrefix) : len(KeyPrefix)+keyIDLen]
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return "", false
		}
	}
	for _, c := range key[len(KeyPrefix)+keyIDLen+1:] {
		if !strings.ContainsRune(base62, c) {
			return "", false
		}
	}
	return id, true
}

// hashKey returns the stored verifier for a key (D5).
func hashKey(key string) []byte {
	h := sha256.Sum256([]byte(key))
	return h[:]
}

// DisplayKey is the non-secret representation shown after creation.
func DisplayKey(id string) string { return KeyPrefix + id + "_…" }
