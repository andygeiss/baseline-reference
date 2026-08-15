package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// TokenPrefix marks a string as one of this app's machine tokens. It costs
// nothing and it makes a leaked token recognizable — to a secret scanner, and
// to whoever finds it pasted somewhere.
const TokenPrefix = "gochat_"

// NewToken returns the secret to show the caller once, and the hash to store.
//
// 32 bytes from crypto/rand: nothing brute-forces that, which is why the hash
// below is SHA-256 rather than argon2id. A password is short and guessable, so
// it needs a slow hash; this secret is not, and argon2id would spend 19 MiB of
// memory on every request to protect something that needs no protecting.
func NewToken() (secret, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("reading token: %w", err)
	}
	secret = TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return secret, HashToken(secret), nil
}

// HashToken returns the stored form of a token secret.
func HashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// BearerToken returns the token in an Authorization header, or "" when there is
// none. The scheme is matched without regard to capitals, as RFC 9110 requires.
func BearerToken(header string) string {
	const scheme = "bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return ""
	}
	return strings.TrimSpace(header[len(scheme):])
}
