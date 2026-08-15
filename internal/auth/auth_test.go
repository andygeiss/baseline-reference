package auth_test

import (
	"strings"
	"testing"

	"github.com/andygeiss/baseline-reference/v3/internal/auth"
)

func TestPasswordRoundTrip(t *testing.T) {
	t.Parallel()

	hash, err := auth.HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	t.Run("the right password verifies", func(t *testing.T) {
		ok, err := auth.VerifyPassword(hash, "correct-horse")
		if err != nil || !ok {
			t.Errorf("VerifyPassword = %v, %v; want true, nil", ok, err)
		}
	})

	t.Run("a wrong password does not", func(t *testing.T) {
		ok, err := auth.VerifyPassword(hash, "wrong-horse")
		if err != nil {
			t.Fatalf("VerifyPassword: %v", err)
		}
		if ok {
			t.Error("a wrong password verified")
		}
	})

	t.Run("the hash is a PHC string carrying its parameters", func(t *testing.T) {
		// Storing the parameters is what makes them upgradable later.
		if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
			t.Errorf("hash = %q, want the argon2id PHC form", hash)
		}
	})
}

func TestEveryHashHasItsOwnSalt(t *testing.T) {
	t.Parallel()

	first, err := auth.HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := auth.HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	// Without a per-password salt, two people with the same password share a
	// hash, and one rainbow table opens both.
	if first == second {
		t.Error("the same password hashed to the same string twice")
	}
}

func TestVerifyRefusesAHashItDidNotWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		hash string
	}{
		{"empty", ""},
		{"not PHC at all", "hunter2"},
		{"a different algorithm", "$bcrypt$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA"},
		{"a future version", "$argon2id$v=99$m=19456,t=2,p=1$c2FsdA$aGFzaA"},
		{"missing fields", "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA"},
		{"unreadable parameters", "$argon2id$v=19$m=lots,t=2,p=1$c2FsdA$aGFzaA"},
		// argon2 panics on a zero parameter, so it must never reach it.
		{"a zero parameter", "$argon2id$v=19$m=0,t=2,p=1$c2FsdA$aGFzaA"},
		{"salt that is not base64", "$argon2id$v=19$m=19456,t=2,p=1$!!!!$aGFzaA"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ok, err := auth.VerifyPassword(tt.hash, "correct-horse")
			if ok {
				t.Error("a malformed hash verified")
			}
			if err == nil {
				t.Error("a malformed hash produced no error")
			}
		})
	}
}

func TestNeedsRehash(t *testing.T) {
	t.Parallel()

	t.Run("a hash at today's settings does not", func(t *testing.T) {
		hash, err := auth.HashPassword("correct-horse")
		if err != nil {
			t.Fatalf("HashPassword: %v", err)
		}
		weak, err := auth.NeedsRehash(hash)
		if err != nil || weak {
			t.Errorf("NeedsRehash = %v, %v; want false, nil", weak, err)
		}
	})

	t.Run("a weaker one does", func(t *testing.T) {
		weak, err := auth.NeedsRehash(oldHash)
		if err != nil || !weak {
			t.Errorf("NeedsRehash = %v, %v; want true, nil", weak, err)
		}
	})
}

// oldHash is shaped like a hash written under older, cheaper settings. The
// digest itself is arbitrary — what these tests use it for is its parameters.
const oldHash = "$argon2id$v=19$m=4096,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGg"

// TestAnOldHashIsStillReadable is the other half of the upgrade path.
// Verifying must use the parameters stored with the hash, not today's —
// otherwise raising the settings locks everybody out of their account.
//
// The digest is arbitrary, so it must not verify. What matters is that it fails
// as "wrong password" rather than "unreadable hash": the first is a login the
// person can retry, the second is an account nobody can get into again.
func TestAnOldHashIsStillReadable(t *testing.T) {
	t.Parallel()

	ok, err := auth.VerifyPassword(oldHash, "correct-horse")
	if err != nil {
		t.Fatalf("an old parameter set was refused as malformed: %v", err)
	}
	if ok {
		t.Error("an arbitrary digest verified")
	}
}

func TestNewToken(t *testing.T) {
	t.Parallel()

	secret, hash, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}

	if !strings.HasPrefix(secret, auth.TokenPrefix) {
		t.Errorf("secret = %q, want it to start with %q", secret, auth.TokenPrefix)
	}
	if hash == secret || strings.Contains(hash, secret) {
		t.Error("the stored hash contains the secret")
	}
	if got := auth.HashToken(secret); got != hash {
		t.Errorf("HashToken(secret) = %q, want the hash NewToken returned", got)
	}

	other, _, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if other == secret {
		t.Error("two tokens came out the same")
	}
}

func TestBearerToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"a plain bearer header", "Bearer abc123", "abc123"},
		// RFC 9110: the scheme is matched without regard to capitals.
		{"lowercase scheme", "bearer abc123", "abc123"},
		{"odd capitals", "BeArEr abc123", "abc123"},
		{"extra spaces around the token", "Bearer   abc123  ", "abc123"},
		{"no header at all", "", ""},
		{"another scheme", "Basic abc123", ""},
		{"the scheme alone", "Bearer", ""},
		{"the scheme and nothing after it", "Bearer ", ""},
		{"a token that only looks like one", "abc123", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := auth.BearerToken(tt.header); got != tt.want {
				t.Errorf("BearerToken(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}
