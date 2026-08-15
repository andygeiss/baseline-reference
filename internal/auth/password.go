// Package auth turns passwords and tokens into things safe to store. It knows
// nothing about HTTP, storage, or this app's types.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// current holds the OWASP-recommended argon2id parameters. Re-check them
// whenever the baseline's patterns/go-auth-sessions.md is re-verified; raising
// them is safe, because NeedsRehash below upgrades stored hashes on login.
var current = params{memory: 19 * 1024, time: 2, threads: 1} // 19 MiB, t=2, p=1

const (
	keyLen  = 32
	saltLen = 16
)

// ErrBadHash means a stored hash is not one this package wrote.
var ErrBadHash = errors.New("password hash is malformed")

type params struct {
	memory  uint32
	time    uint32
	threads uint8
}

// HashPassword returns a PHC string holding the parameters, the salt, and the
// hash. Storing the parameters is what makes them upgradable: a login can see
// that a stored hash is weaker than today's rules and rewrite it.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("reading salt: %w", err)
	}
	return encode(current, salt, key(password, salt, current)), nil
}

// VerifyPassword reports whether password produced hash.
//
// It returns an error only when the stored hash is unreadable, which is a bug
// or a corrupted row, never a wrong password.
func VerifyPassword(hash, password string) (bool, error) {
	p, salt, want, err := decode(hash)
	if err != nil {
		return false, err
	}
	// The stored parameters, not the current ones: a hash written under older
	// settings must still verify, or raising the settings locks everybody out.
	got := key(password, salt, p)
	// Constant time: a byte-by-byte compare leaks how much of the hash matched,
	// one request at a time.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NeedsRehash reports whether a stored hash was written under weaker settings
// than today's. Call it after a successful login — that is the one moment the
// plaintext is in hand and a stronger hash can replace the old one.
func NeedsRehash(hash string) (bool, error) {
	p, _, _, err := decode(hash)
	if err != nil {
		return false, err
	}
	return p.memory < current.memory || p.time < current.time, nil
}

// DummyHash is a hash of a password nobody has. Verifying against it is how the
// login handler spends the same time on an unknown name as on a known one —
// without it, a fast "no such user" answer tells an attacker which names exist.
//
// It is built once at startup, because building it costs a real argon2id run.
func DummyHash() (string, error) {
	return HashPassword("this password belongs to nobody")
}

func key(password string, salt []byte, p params) []byte {
	return argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, keyLen)
}

func encode(p params, salt, hash []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash))
}

// decode reads back what encode wrote, refusing anything it does not recognize.
func decode(phc string) (p params, salt, hash []byte, err error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, ErrBadHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return p, nil, nil, ErrBadHash
	}
	if p.memory == 0 || p.time == 0 || p.threads == 0 {
		return p, nil, nil, ErrBadHash // argon2 panics on a zero parameter
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return p, nil, nil, ErrBadHash
	}
	if hash, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return p, nil, nil, ErrBadHash
	}
	if len(salt) == 0 || len(hash) == 0 {
		return p, nil, nil, ErrBadHash
	}
	return p, salt, hash, nil
}
