package domain

import "time"

// ResetLifetime is how long a password-reset link works. Long enough to walk to
// the other machine the mail arrived on, short enough that a link forwarded by
// accident is already dead.
const ResetLifetime = time.Hour

// Reset is one outstanding password-reset link.
//
// Hash is the SHA-256 of the token, and it is the primary key: the plaintext
// exists in exactly one place, the message that was sent. A leaked database
// leaks nothing anybody can redeem.
type Reset struct {
	Hash      string
	UserID    string
	ExpiresAt time.Time
}

// Expired reports whether this link is past its hour.
func (r Reset) Expired(now time.Time) bool { return now.After(r.ExpiresAt) }
